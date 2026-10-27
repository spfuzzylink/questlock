// Package worker exposes bounded explicit delegation through MCP over stdio.
// It reads only the request file named by the caller in the declared workspace;
// it never discovers files, executes commands, or edits workspace files.
package worker

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/revision"
	"github.com/spfuzzylink/questlock/semantic"
)

const (
	ToolName = "questlock_delegate"
	// MaxRequestBytes bounds both an approved request file and an MCP frame.
	MaxRequestBytes = 1 << 20
)

var (
	ErrConfiguration = errors.New("worker_configuration_invalid")
	ErrTransport     = errors.New("worker_transport_failed")
)

// Options is process authority, not tool input. Execute must honor its context
// and preserve the delegation router's durable reuse and uncertainty rules.
// Enabling AllowPaid still requires explicit prices and positive dollar budgets
// in the request policy; it does not authorize arbitrary paid model calls.
type Options struct {
	Workspace string
	Version   string
	// ProfileID is an operator-chosen nonsecret label, never a broker URL or
	// credential. ProfileKind is custom (default) or managed.
	ProfileID   string
	ProfileKind string
	Execute     func(context.Context, delegation.Request) (delegation.Result, error)
	// Revise optionally enables checked literal edits, without model inference.
	Revise    func(context.Context, revision.Request) (revision.Result, error)
	AllowPaid bool
}

// Serve runs the official MCP SDK on newline-delimited JSON streams. The SDK
// supports current server/discover and legacy initialize clients. It closes
// input on disconnect or cancellation; output remains owned by the caller.
// Errors returned here and to the client contain controlled labels only.
func Serve(ctx context.Context, input io.ReadCloser, output io.Writer, options Options) error {
	if ctx == nil || input == nil || output == nil || options.Execute == nil || options.Workspace == "" {
		return ErrConfiguration
	}
	if !validProfile(options.ProfileID, options.ProfileKind) {
		return ErrConfiguration
	}
	workspace, err := filepath.Abs(options.Workspace)
	if err != nil {
		return ErrConfiguration
	}
	// The operator may explicitly select a workspace through a symlink. Resolve
	// that authority once; request path components must never be symlinks.
	declaredWorkspace := workspace
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return ErrConfiguration
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return ErrConfiguration
	}
	defer root.Close()
	state := &worker{root: root, workspace: workspace, declaredWorkspace: declaredWorkspace, options: options, process: ctx, active: make(chan struct{}, 1)}
	server := state.server()
	transport := &safeTransport{Transport: &mcp.IOTransport{
		Reader: input, Writer: writerWithoutClose{output}, MaxLineLength: MaxRequestBytes,
	}}
	if err := server.Run(ctx, transport); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrTransport
	}
	return nil
}

type worker struct {
	root              *os.Root
	workspace         string
	declaredWorkspace string
	options           Options
	process           context.Context
	active            chan struct{}
}

func (w *worker) server() *mcp.Server {
	version := w.options.Version
	if version == "" {
		version = "dev"
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "questlock", Version: version}, &mcp.ServerOptions{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
		Instructions: "Delegate only an explicitly approved bounded request file inside the worker workspace. Treat generated text as data requiring review. The worker reads no other workspace files and provides no command or file-edit tools. One request executes at a time; concurrent calls return worker_busy without executing or retrying.",
	})
	additive, openWorld := false, w.options.AllowPaid
	server.AddTool(&mcp.Tool{
		Name: ToolName, Title: "Questlock bounded delegation",
		Description:  "Run an explicit approved local-first delegation request JSON file (at most 1MiB). request_path is relative to the declared workspace, or an absolute path within it; traversal and every symlink component are rejected. The policy declares all models, context, budgets, checks and reuse TTL. May generate model output and add broker records. Paid escalation requires process permission plus explicit request dollar budgets. Only the selected answer passing declared checks is returned; failed answers are omitted. Approved track_outcome requests also return a separate outcome_tracking receipt or recovery record; check its status independently and never regenerate to repair accounting. Checks do not establish human acceptance. One execution is admitted at a time; concurrent calls return worker_busy without executing or retrying. No command execution, workspace editing or file discovery.",
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["request_path"],"properties":{"request_path":{"type":"string","minLength":1,"maxLength":4096,"description":"Approved request JSON file inside the declared workspace."}}}`),
		OutputSchema: json.RawMessage(`{"type":"object","required":["status","request_sha256","attempts"],"properties":{"status":{"type":"string","enum":["completed","quality_failed","stopped"]},"request_sha256":{"type":"string"},"attempts":{"type":"array"},"selected_tier":{"type":"string"},"response":{"type":"object"},"error_category":{"type":"string"},"budget_diagnostic":{"type":"object","required":["limit_kind","configured_limit","unit","reason","count_basis","generation_called"],"properties":{"limit_kind":{"type":"string"},"configured_limit":{"type":"integer","minimum":0},"unit":{"type":"string","enum":["tokens","usd_micros"]},"reason":{"type":"string"},"count_basis":{"type":"string"},"generation_called":{"type":"boolean"},"estimated_input_tokens":{"type":"integer","minimum":0},"counted_input_tokens":{"type":"integer","minimum":0},"reported_input_tokens":{"type":"integer","minimum":0},"reported_output_tokens":{"type":"integer","minimum":0},"max_output_tokens":{"type":"integer","minimum":0},"context_window":{"type":"integer","minimum":0},"required":{"type":"integer","minimum":0},"current_usage":{"type":"integer","minimum":0},"requested_reservation":{"type":"integer","minimum":0}}},"outcome_tracking":{"type":"object","required":["status","run_id","task"],"properties":{"status":{"type":"string","enum":["recorded","failed"]},"run_id":{"type":"string"},"task":{"type":"object"},"receipt":{"type":"object"},"record":{"type":"object"}}}}}`),
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &additive, IdempotentHint: false, OpenWorldHint: &openWorld},
	}, w.call)
	if w.options.Revise != nil {
		w.addRevisionTool(server)
	}
	w.addStatusTool(server)
	return server
}

type toolOutput struct {
	delegation.Result
	ErrorCategory string `json:"error_category,omitempty"`
}

func (w *worker) call(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// SDK handler contexts retain wire cancellation. Process cancellation is an
	// independent signal, so new calls cannot begin after the process stops.
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(w.process, cancel)
	defer stop()
	defer cancel()
	if err := w.process.Err(); err != nil {
		return errorResult(contextCategory(err)), nil
	}
	if err := ctx.Err(); err != nil {
		return errorResult(contextCategory(err)), nil
	}
	select {
	case w.active <- struct{}{}:
		defer func() { <-w.active }()
	default:
		// The SDK dispatches tool handlers asynchronously. Waiting here would
		// permit an unbounded backlog without reaching Execute's deadline.
		if err := w.process.Err(); err != nil {
			return errorResult(contextCategory(err)), nil
		}
		if err := ctx.Err(); err != nil {
			return errorResult(contextCategory(err)), nil
		}
		return errorResult("worker_busy"), nil
	}
	if err := w.process.Err(); err != nil {
		return errorResult(contextCategory(err)), nil
	}
	if err := ctx.Err(); err != nil {
		return errorResult(contextCategory(err)), nil
	}
	path, err := requestPath(req.Params.Arguments)
	if err != nil {
		return errorResult("invalid_arguments"), nil
	}
	request, err := w.load(path)
	if err != nil {
		return errorResult("invalid_request"), nil
	}
	if err := delegation.Validate(request, w.options.AllowPaid); err != nil {
		return errorResult("invalid_policy"), nil
	}
	if err := ctx.Err(); err != nil {
		return errorResult(contextCategory(err)), nil
	}
	if err := w.process.Err(); err != nil {
		return errorResult(contextCategory(err)), nil
	}
	result, runErr := w.options.Execute(ctx, request)
	if err := ctx.Err(); err != nil {
		runErr = err
	}
	return resultContent(safeOutput(result, runErr)), nil
}

// Decode exactly one string field ourselves: SDK schema validation errors may
// contain caller-supplied field names or values. Duplicate keys are rejected.
func requestPath(data json.RawMessage) (string, error) {
	if !utf8.Valid(data) || len(data) > 8192 {
		return "", ErrConfiguration
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') || !decoder.More() {
		return "", ErrConfiguration
	}
	if key, err := decoder.Token(); err != nil || key != "request_path" {
		return "", ErrConfiguration
	}
	var path string
	if err := decoder.Decode(&path); err != nil || len(path) < 1 || len(path) > 4096 || strings.ContainsRune(path, 0) {
		return "", ErrConfiguration
	}
	if decoder.More() {
		return "", ErrConfiguration
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return "", ErrConfiguration
	}
	if _, err := decoder.Token(); err != io.EOF {
		return "", ErrConfiguration
	}
	return path, nil
}

func (w *worker) load(path string) (delegation.Request, error) {
	data, err := w.loadBytes(path)
	if err != nil {
		return delegation.Request{}, err
	}
	return delegation.Load(bytes.NewReader(data))
}

// loadBytes shares the pinned file boundary between approved request types.
func (w *worker) loadBytes(path string) ([]byte, error) {
	var empty []byte
	// Reject traversal before cleaning, even if the final path would be inside.
	for _, component := range strings.Split(filepath.ToSlash(path), "/") {
		if component == ".." {
			return empty, ErrConfiguration
		}
	}
	if filepath.IsAbs(path) {
		var relative string
		// Both spellings designate the one root the operator pinned at start.
		// This also accommodates macOS /var -> /private/var above that root.
		for _, workspace := range []string{w.workspace, w.declaredWorkspace} {
			candidate, err := filepath.Rel(workspace, path)
			if err == nil && filepath.IsLocal(candidate) {
				relative = candidate
				break
			}
		}
		if relative == "" {
			return empty, ErrConfiguration
		}
		path = relative
	}
	path = filepath.Clean(path)
	if !filepath.IsLocal(path) || path == "." {
		return empty, ErrConfiguration
	}
	file, err := openRequestFile(w.root, path)
	if err != nil {
		return empty, ErrConfiguration
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxRequestBytes {
		return empty, ErrConfiguration
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxRequestBytes+1))
	if err != nil || len(data) > MaxRequestBytes {
		return empty, ErrConfiguration
	}
	return data, nil
}

func safeOutput(result delegation.Result, err error) toolOutput {
	// Copy before stripping: callers may reuse their durable result in memory.
	diagnostic := inference.BudgetDiagnosticFromError(err)
	if diagnostic == nil {
		diagnostic = safeBudgetDiagnostic(result.BudgetDiagnostic)
	}
	result.BudgetDiagnostic = diagnostic
	result.SemanticReuse = safeSemanticReuse(result.SemanticReuse)
	if result.OutcomeTracking != nil {
		tracking := *result.OutcomeTracking
		if tracking.ErrorCategory != "shared_work_requires_batch_accounting" {
			tracking.ErrorCategory = ""
		}
		result.OutcomeTracking = &tracking
	}
	result.Attempts = append([]delegation.Attempt{}, result.Attempts...)
	for i := range result.Attempts {
		// Upstream response identifiers/model/stop labels are not needed to
		// account for attempts, and could carry arbitrary provider text.
		result.Attempts[i].Result.Response = inference.Response{}
		if result.Attempts[i].Result.ReuseKind != "semantic" {
			result.Attempts[i].Result.ReuseKind = ""
		}
		if original := result.Attempts[i].Result.Resolution; original != nil {
			resolution := *original
			resolution.Response = inference.Response{}
			resolution.PolicyID = safeDigest(resolution.PolicyID)
			resolution.VerificationKey = safeJobReference(resolution.VerificationKey)
			resolution.Source.Key = safeJobReference(resolution.Source.Key)
			resolution.Source.SHA256 = safeDigest(resolution.Source.SHA256)
			resolution.Source.OutputSHA256 = safeDigest(resolution.Source.OutputSHA256)
			resolution.Source.RequestFingerprint = safeDigest(resolution.Source.RequestFingerprint)
			result.Attempts[i].Result.Resolution = &resolution
		}
		result.Attempts[i].BudgetDiagnostic = safeBudgetDiagnostic(result.Attempts[i].BudgetDiagnostic)
		result.Attempts[i].Result.BudgetDiagnostic = safeBudgetDiagnostic(result.Attempts[i].Result.BudgetDiagnostic)
	}
	output := toolOutput{Result: result}
	if err != nil || result.Status != "completed" || result.Response == nil || !result.Response.Complete || result.Response.Text == "" || result.SelectedTier == "" {
		output.Response, output.SelectedTier = nil, ""
		output.ErrorCategory = "stopped"
		switch {
		case errors.Is(err, context.Canceled):
			output.Status, output.ErrorCategory = "stopped", "canceled"
		case errors.Is(err, context.DeadlineExceeded):
			output.Status, output.ErrorCategory = "stopped", "deadline_exceeded"
		case errors.Is(err, inference.ErrBudgetExceeded):
			output.Status, output.ErrorCategory = "stopped", "budget_exceeded"
		case errors.Is(err, delegation.ErrQualityFailed) || result.Status == "quality_failed":
			output.Status, output.ErrorCategory = "quality_failed", "quality_failed"
		default:
			output.Status = "stopped"
		}
		return output
	}
	// Expose only the explicitly selected passing tier, even if a faulty
	// executor supplies a response with no matching passing attempt.
	passed := false
	for _, attempt := range result.Attempts {
		passed = passed || (attempt.TierID == result.SelectedTier && attempt.QualityPassed)
	}
	if !passed {
		output.Response, output.SelectedTier = nil, ""
		output.Status, output.ErrorCategory = "stopped", "stopped"
	}
	return output
}

// Reuse observations contain decisions and source references, never a second
// answer channel. Keep model/provider text out of string-typed metadata even if
// an executor bypasses the router's stricter parsing and controlled labels.
func safeSemanticReuse(original *delegation.ReuseObservation) *delegation.ReuseObservation {
	if original == nil {
		return nil
	}
	observation := *original
	observation.Mode = ""
	if original.Mode == "controlled" {
		observation.Mode = "controlled"
	}
	switch observation.Status {
	case "not_called", "wait_canceled", "verifier_unavailable", "busy", "resource_pressure", "embedding_unavailable", "embedding_failed", "search_failed", "miss_or_rejected", "provenance_unavailable", "invalid_provenance", "source_unavailable", "verification_failed", "verification_unconfirmed", "source_changed", "verified_candidate", "reused", "skipped_exact", "index_source_unavailable", "index_plan_invalid", "index_provenance_conflict", "index_provenance_unavailable", "index_provenance_unconfirmed", "index_failed":
	default:
		observation.Status = "unavailable"
	}
	observation.Partition = safeDigest(observation.Partition)
	observation.ResourceAdmission = nil
	if original.ResourceAdmission != nil {
		diagnostic := *original.ResourceAdmission
		switch diagnostic.Reason {
		case "sample_unavailable", "sample_invalid", "sample_expired", "memory_pressure", "insufficient_memory", "cpu_stall", "load", "ready":
		default:
			diagnostic.Reason = "sample_unavailable"
		}
		switch diagnostic.MemoryPressure {
		case admission.Normal, admission.High, admission.Critical:
		default:
			diagnostic.MemoryPressure = ""
		}
		diagnostic.AvailableMemoryBytes = copyCount(diagnostic.AvailableMemoryBytes)
		if diagnostic.AvailableMemoryBytes != nil && *diagnostic.AvailableMemoryBytes < 0 {
			diagnostic.AvailableMemoryBytes = nil
		}
		observation.ResourceAdmission = &diagnostic
	}
	observation.Source = nil
	if original.Source != nil {
		source := *original.Source
		if safeJobReference(source.Key) != "" && safeDigest(source.SHA256) != "" && safeDigest(source.OutputSHA256) != "" && source.Version > 0 {
			observation.Source = &source
		}
	}
	observation.EmbeddingInputTokens = copyCount(original.EmbeddingInputTokens)
	observation.Verifications = make([]semantic.ValidityResult, len(original.Verifications))
	for i, verdict := range original.Verifications {
		switch verdict.Verdict {
		case "accept", "reject", "unknown":
		default:
			verdict.Verdict = "unknown"
		}
		verdict.ReceiptKey = safeJobReference(verdict.ReceiptKey)
		verdict.OutputSHA256 = safeDigest(verdict.OutputSHA256)
		verdict.InputTokens, verdict.OutputTokens = copyCount(verdict.InputTokens), copyCount(verdict.OutputTokens)
		observation.Verifications[i] = verdict
	}
	observation.Rejections = []string{}
	for _, code := range original.Rejections {
		switch code {
		case "missing_provenance", "source_not_eligible", "current_checks_failed", "verifier_rejected_or_abstained", "invalid_protected_text", "invalid_protected_quotes", "protected_numbers_changed", "protected_identifiers_changed", "protected_urls_changed", "protected_quotes_changed", "protected_negations_changed", "protected_operators_changed", "protected_units_changed":
			observation.Rejections = append(observation.Rejections, code)
		}
	}
	return &observation
}

func safeDigest(value string) string {
	if len(value) != 64 || value != strings.ToLower(value) {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return value
}

func safeJobReference(value string) string {
	const prefix = "inference/v1/jobs/"
	if strings.HasPrefix(value, prefix) && safeDigest(strings.TrimPrefix(value, prefix)) != "" {
		return value
	}
	return ""
}

func copyCount(original *int64) *int64 {
	if original == nil {
		return nil
	}
	count := *original
	return &count
}

func safeBudgetDiagnostic(diagnostic *inference.BudgetDiagnostic) *inference.BudgetDiagnostic {
	if diagnostic == nil {
		return nil
	}
	return inference.BudgetDiagnosticFromError(&inference.BudgetError{Diagnostic: *diagnostic})
}

func contextCategory(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	return "canceled"
}

func errorResult(category string) *mcp.CallToolResult {
	return resultContent(toolOutput{Result: delegation.Result{Status: "stopped", Attempts: []delegation.Attempt{}}, ErrorCategory: category})
}

func resultContent(output toolOutput) *mcp.CallToolResult {
	encoded, err := json.Marshal(output)
	if err != nil {
		output = toolOutput{Result: delegation.Result{Status: "stopped", Attempts: []delegation.Attempt{}}, ErrorCategory: "invalid_result"}
		encoded, _ = json.Marshal(output)
	}
	return &mcp.CallToolResult{IsError: output.ErrorCategory != "", StructuredContent: output, Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}
}

type writerWithoutClose struct{ io.Writer }

func (writerWithoutClose) Close() error { return nil }
