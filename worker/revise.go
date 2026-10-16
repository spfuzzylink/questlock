package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/revision"
)

const ReviseToolName = "questlock_revise"

func (w *worker) addRevisionTool(server *mcp.Server) {
	additive, openWorld := false, false
	server.AddTool(&mcp.Tool{
		Name: ReviseToolName, Title: "Questlock checked revision",
		Description:  "Apply an explicitly approved checked-revision JSON request (at most 1MiB) inside the declared workspace. Source text must be inline or an exact broker revision artifact reference with version and SHA-256. Only literal replacements with exact match counts are supported. Source and output checks must pass before durable publication; failure withholds text. Never calls a model, uses provider credentials, discovers files, edits workspace files or executes commands. Shares one execution slot with questlock_delegate; busy calls are rejected without retry. Returns zero generation usage and provider token cost, not zero host or broker cost. Checks do not establish human acceptance; track_outcome returns a separate receipt or recovery record.",
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["request_path"],"properties":{"request_path":{"type":"string","minLength":1,"maxLength":4096,"description":"Approved checked-revision JSON file inside the declared workspace."}}}`),
		OutputSchema: json.RawMessage(`{"type":"object","required":["status","generation_called","this_run_usage","provider_cost_usd","cost_basis"],"properties":{"status":{"type":"string","enum":["completed","quality_failed","stopped"]},"request_sha256":{"type":"string"},"text":{"type":"string"},"error_category":{"type":"string"}}}`),
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &additive, IdempotentHint: false, OpenWorldHint: &openWorld},
	}, w.callRevision)
}

type revisionOutput struct {
	revision.Result
	ErrorCategory string `json:"error_category,omitempty"`
}

func (w *worker) callRevision(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(w.process, cancel)
	defer stop()
	defer cancel()
	if err := w.process.Err(); err != nil {
		return revisionError(contextCategory(err)), nil
	}
	if err := ctx.Err(); err != nil {
		return revisionError(contextCategory(err)), nil
	}
	select {
	case w.active <- struct{}{}:
		defer func() { <-w.active }()
	default:
		if err := w.process.Err(); err != nil {
			return revisionError(contextCategory(err)), nil
		}
		if err := ctx.Err(); err != nil {
			return revisionError(contextCategory(err)), nil
		}
		return revisionError("worker_busy"), nil
	}
	if err := ctx.Err(); err != nil {
		return revisionError(contextCategory(err)), nil
	}
	if err := w.process.Err(); err != nil {
		return revisionError(contextCategory(err)), nil
	}
	path, err := requestPath(req.Params.Arguments)
	if err != nil {
		return revisionError("invalid_arguments"), nil
	}
	data, err := w.loadBytes(path)
	if err != nil {
		return revisionError("invalid_request"), nil
	}
	request, err := revision.Load(bytes.NewReader(data))
	if err != nil {
		return revisionError("invalid_request"), nil
	}
	if err := revision.Validate(request); err != nil {
		return revisionError("invalid_policy"), nil
	}
	if err := ctx.Err(); err != nil {
		return revisionError(contextCategory(err)), nil
	}
	if err := w.process.Err(); err != nil {
		return revisionError(contextCategory(err)), nil
	}
	result, runErr := w.options.Revise(ctx, request)
	if err := ctx.Err(); err != nil {
		runErr = err
	}
	return revisionContent(safeRevisionOutput(request, result, runErr)), nil
}

// The MCP boundary independently checks selected text and proof shape. It never
// returns failed candidate text or raw executor errors, even for a faulty hook.
func safeRevisionOutput(request revision.Request, result revision.Result, err error) revisionOutput {
	output := revisionOutput{Result: result}
	requestDigest, digestErr := revision.RequestDigest(request)
	outputDigest := revision.Digest(result.Text)
	outputChecks, outputPassed := evaluation.CheckOutput(result.Text, request.Checks)
	sourcePassed := len(result.SourceChecks) == len(request.SourceChecks) && len(request.SourceChecks) > 0
	for i, check := range result.SourceChecks {
		if i >= len(request.SourceChecks) || !check.Passed || check.ID != request.SourceChecks[i].ID || check.Kind != request.SourceChecks[i].Kind {
			sourcePassed = false
		}
	}
	if request.Source.Text != nil {
		verified, passed := evaluation.CheckOutput(*request.Source.Text, request.SourceChecks)
		sourcePassed = sourcePassed && passed && revision.Digest(*request.Source.Text) == request.Source.SHA256
		output.SourceChecks = verified
	}
	artifactValid := result.Durable && result.Artifact != nil && result.Artifact.Version == 1 && result.Artifact.Key == revision.ResultPrefix+requestDigest && result.Artifact.SHA256 == result.OutputSHA256
	accountingValid := !result.GenerationCalled && result.ThisRunUsage == (inference.Usage{}) && result.ProviderCostUSD == 0 && ((!result.Reused && result.CostBasis == "deterministic_edit") || (result.Reused && result.CostBasis == "reuse_zero"))
	valid := err == nil && digestErr == nil && result.Status == "completed" && artifactValid && accountingValid && result.RequestSHA256 == requestDigest && result.SourceSHA256 == request.Source.SHA256 && result.Text != "" && result.OutputSHA256 == outputDigest && outputPassed && sourcePassed
	if valid {
		output.Checks = outputChecks
		return output
	}
	output.Text = ""
	output.Status, output.ErrorCategory = "stopped", "stopped"
	switch {
	case errors.Is(err, context.Canceled):
		output.ErrorCategory = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		output.ErrorCategory = "deadline_exceeded"
	case result.Status == "quality_failed":
		output.Status, output.ErrorCategory = "quality_failed", "quality_failed"
	}
	return output
}

func revisionError(category string) *mcp.CallToolResult {
	return revisionContent(revisionOutput{Result: revision.Result{Status: "stopped", CostBasis: "no_generation"}, ErrorCategory: category})
}

func revisionContent(output revisionOutput) *mcp.CallToolResult {
	encoded, err := json.Marshal(output)
	if err != nil {
		output = revisionOutput{Result: revision.Result{Status: "stopped", CostBasis: "no_generation"}, ErrorCategory: "invalid_result"}
		encoded, _ = json.Marshal(output)
	}
	return &mcp.CallToolResult{IsError: output.ErrorCategory != "", StructuredContent: output, Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}
}
