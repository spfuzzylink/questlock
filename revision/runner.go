package revision

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/outcomes"
	"github.com/spfuzzylink/questlock/protocol"
)

var (
	ErrSourceMismatch = errors.New("checked revision source hash, reference, or stored provenance does not match")
	ErrCountMismatch  = errors.New("checked revision literal occurrence count does not match")
	ErrQualityFailed  = errors.New("checked revision did not pass every declared deterministic check")
	ErrPublication    = errors.New("checked revision publication is not confirmed; output withheld")
)

// record deliberately contains no timestamp: identical operations from two
// workers must produce byte-identical immutable content. Accounting is separate.
type record struct {
	Schema        int                      `json:"schema"`
	RequestSHA256 string                   `json:"request_sha256"`
	SourceSHA256  string                   `json:"source_sha256"`
	OutputSHA256  string                   `json:"output_sha256"`
	Source        *Ref                     `json:"source,omitempty"`
	Text          string                   `json:"text"`
	SourceChecks  []evaluation.CheckResult `json:"source_checks"`
	Checks        []evaluation.CheckResult `json:"checks"`
}

// Plan executes and checks a bounded transformation without publication or
// outcome registration. It reads a broker only when Source pins an artifact.
func (r Runner) Plan(ctx context.Context, request Request) (result Result, runErr error) {
	started := time.Now()
	defer func() { result.ElapsedMS = float64(time.Since(started)) / float64(time.Millisecond) }()
	result, _, runErr = r.prepare(ctx, request)
	return result, runErr
}

func (r Runner) prepare(ctx context.Context, request Request) (Result, string, error) {
	result := Result{Status: "stopped", CostBasis: "no_generation", SourceChecks: []evaluation.CheckResult{}, Checks: []evaluation.CheckResult{}}
	if err := Validate(request); err != nil {
		return result, "", err
	}
	result.RequestSHA256, result.SourceSHA256 = requestDigest(request), request.Source.SHA256
	if err := ctx.Err(); err != nil {
		return result, "", err
	}
	text, err := r.source(ctx, request.Source)
	if err != nil {
		return result, "", err
	}
	if Digest(text) != request.Source.SHA256 {
		return result, "", ErrSourceMismatch
	}
	var passed bool
	result.SourceChecks, passed = evaluation.CheckOutput(text, request.SourceChecks)
	if !passed {
		return result, "", ErrQualityFailed
	}
	for _, edit := range request.Edits {
		if err := ctx.Err(); err != nil {
			return result, "", err
		}
		count := strings.Count(text, edit.Old)
		if count != edit.ExpectedMatches {
			return result, "", ErrCountMismatch
		}
		// Check expansion before allocating. All operands are tightly bounded;
		// retain the check for every intermediate step, even if a later shrinks.
		size := len(text) + count*(len(edit.New)-len(edit.Old))
		if size < 1 || size > MaxTextBytes {
			return result, "", invalid("an intermediate or final output exceeds 1..262144 bytes")
		}
		text = strings.ReplaceAll(text, edit.Old, edit.New)
	}
	// Reject a sequence that cancels itself, for inline and referenced text.
	if Digest(text) == request.Source.SHA256 {
		return result, "", invalid("edits must change the final source text")
	}
	result.OutputSHA256, result.CostBasis = Digest(text), "deterministic_edit"
	result.Checks, passed = evaluation.CheckOutput(text, request.Checks)
	if !passed {
		result.Status = "quality_failed"
		return result, "", ErrQualityFailed
	}
	stored := record{Schema: Schema, RequestSHA256: result.RequestSHA256, SourceSHA256: result.SourceSHA256, OutputSHA256: result.OutputSHA256, Text: text, SourceChecks: result.SourceChecks, Checks: result.Checks}
	if request.Source.Text == nil {
		stored.Source = &Ref{Key: request.Source.Key, Version: request.Source.Version, SHA256: request.Source.SHA256}
	}
	encoded, err := json.Marshal(stored)
	if err != nil || len(encoded) > protocol.MaxContentBytes {
		return result, "", invalid("encoded result exceeds broker artifact limit")
	}
	if err := ctx.Err(); err != nil {
		return result, "", err
	}
	result.Status, result.Text = "planned", text
	return result, string(encoded), nil
}

func (r Runner) source(ctx context.Context, source Source) (string, error) {
	if source.Text != nil {
		return *source.Text, nil
	}
	if r.Broker == nil {
		return "", invalid("broker is required for referenced source")
	}
	artifact, err := r.Broker.Get(ctx, source.Key)
	if err != nil {
		return "", safeSourceError(err)
	}
	parent, err := decodeRecord(artifact, source.Key)
	if err != nil || artifact.Version != source.Version || parent.OutputSHA256 != source.SHA256 {
		return "", ErrSourceMismatch
	}
	return parent.Text, nil
}

// Run publishes once with expected version zero. Known conflicts may be read
// once to prove identical immutable content. Transport uncertainty never causes
// another publication, edit execution, model call, or disclosure of the output.
func (r Runner) Run(ctx context.Context, request Request) (result Result, runErr error) {
	started := time.Now()
	runID := ""
	defer func() {
		if request.TrackOutcome && result.RequestSHA256 != "" && runID != "" {
			r.track(ctx, request, &result, runID)
		}
		result.ElapsedMS = float64(time.Since(started)) / float64(time.Millisecond)
	}()
	if request.TrackOutcome {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return Result{Status: "stopped", CostBasis: "no_generation"}, invalid("cannot allocate outcome run ID")
		}
		runID = "revision-" + hex.EncodeToString(random[:])
	}
	var content string
	result, content, runErr = r.prepare(ctx, request)
	if runErr != nil {
		return result, runErr
	}
	// From this point output is withheld until the immutable publication is
	// confirmed; a prepared plan alone is not a durable run.
	result.Text, result.Status = "", "stopped"
	if r.Broker == nil {
		return result, invalid("broker is required to publish checked revisions")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	key := ResultPrefix + result.RequestSHA256
	published, err := r.Broker.Publish(ctx, protocol.PublishRequest{Key: key, OperationID: "revision-" + result.RequestSHA256, ExpectedVersion: 0, Content: content})
	artifact := published.Artifact
	reused := published.Replayed
	if err != nil {
		if !brokerCode(err, "version_conflict") {
			return result, safePublicationError(err)
		}
		artifact, err = r.Broker.Get(ctx, key)
		if err != nil {
			return result, safePublicationError(err)
		}
		reused = true
	}
	stored, err := decodeRecord(artifact, key)
	if err != nil || artifact.Content != content {
		return result, ErrPublication
	}
	result.Durable, result.Reused = true, reused
	result.Artifact = &Ref{Key: key, Version: artifact.Version, SHA256: stored.OutputSHA256}
	if reused {
		result.CostBasis = "reuse_zero"
	}
	// A confirmed commit may race with caller cancellation. Preserve its
	// durable receipt/accounting but withhold the text from the canceled run.
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.Status, result.Text = "completed", stored.Text
	return result, nil
}

func decodeRecord(artifact protocol.Artifact, key string) (record, error) {
	var stored record
	if artifact.Key != key || artifact.Version != 1 || !resultKey(key) || len(artifact.Content) > protocol.MaxContentBytes {
		return stored, ErrSourceMismatch
	}
	if err := strictDecode([]byte(artifact.Content), &stored); err != nil {
		return record{}, ErrSourceMismatch
	}
	if stored.Schema != Schema || stored.RequestSHA256 != strings.TrimPrefix(key, ResultPrefix) || !hash(stored.SourceSHA256) || !hash(stored.OutputSHA256) || stored.SourceSHA256 == stored.OutputSHA256 || !validText(stored.Text) || stored.OutputSHA256 != Digest(stored.Text) || !passingEvidence(stored.SourceChecks) || !passingEvidence(stored.Checks) {
		return record{}, ErrSourceMismatch
	}
	if stored.Source != nil && (!resultKey(stored.Source.Key) || stored.Source.Version != 1 || stored.Source.SHA256 != stored.SourceSHA256) {
		return record{}, ErrSourceMismatch
	}
	return stored, nil
}

func passingEvidence(checks []evaluation.CheckResult) bool {
	if len(checks) < 1 || len(checks) > 64 {
		return false
	}
	seen := make(map[string]bool)
	for _, check := range checks {
		if !check.Passed || seen[check.ID] {
			return false
		}
		seen[check.ID] = true
		// Reuse the canonical ID validator without manufacturing a model plan.
		probe := evaluation.Check{ID: check.ID, Kind: "contains", Text: "x"}
		if evaluation.ValidateChecks([]evaluation.Check{probe}) != nil {
			return false
		}
		switch check.Kind {
		case "contains", "not_contains", "json_valid", "json_equals":
		default:
			return false
		}
	}
	return true
}

func brokerCode(err error, code string) bool {
	var e *client.Error
	return errors.As(err, &e) && e.Code == code
}
func safeSourceError(err error) error {
	for _, safe := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, safe) {
			return errors.Join(ErrSourceMismatch, safe)
		}
	}
	return ErrSourceMismatch
}
func safePublicationError(err error) error {
	for _, safe := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, safe) {
			return errors.Join(ErrPublication, safe)
		}
	}
	return ErrPublication
}

func (r Runner) track(ctx context.Context, request Request, result *Result, runID string) {
	zero := float64(0)
	complete := result.OutputSHA256 != "" && len(result.Checks) > 0
	passed := passingEvidence(result.SourceChecks) && passingEvidence(result.Checks)
	attempt := outcomes.Attempt{TierID: "checked_revision", Fingerprint: result.RequestSHA256, OutputSHA256: result.OutputSHA256, CheckPassed: passed, Complete: complete, Durable: result.Durable, Reused: result.Reused, GenerationCalled: false, UsageKnown: true, Usage: inference.Usage{}, EstimatedCostUSD: &zero, CostBasis: result.CostBasis}
	task := outcomes.Task{ID: request.Task, Revision: request.Revision}
	run := outcomes.Run{ID: runID, RequestDigest: result.RequestSHA256, Status: result.Status, Attempts: []outcomes.Attempt{attempt}}
	tracking := &delegation.OutcomeTracking{Status: "failed", RunID: runID, Task: task, Record: &run}
	result.OutcomeTracking = tracking
	registration, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	receipt, err := (outcomes.Store{Broker: r.Broker}).Record(registration, task, run)
	if err == nil {
		tracking.Status, tracking.Receipt, tracking.Record = "recorded", &receipt, nil
	}
}
