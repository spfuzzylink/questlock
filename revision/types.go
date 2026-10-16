// Package revision applies explicitly requested literal edits to pinned text.
// It has no model, provider, tool, semantic rewriting, or automatic fallback.
package revision

import (
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
)

const (
	Schema              = 1
	MaxRequestBytes     = 1 << 20
	MaxTextBytes        = 256 << 10
	MaxEdits            = 32
	MaxReplacementBytes = 16 << 10
	ResultPrefix        = "revision/v1/results/"
)

// Request binds a transformation and its checks to a task revision. Checks
// establish only their declared predicates; human acceptance is never inferred.
type Request struct {
	Schema       int                `json:"schema"`
	Task         string             `json:"task"`
	Revision     string             `json:"revision"`
	Source       Source             `json:"source"`
	Edits        []Edit             `json:"edits"`
	SourceChecks []evaluation.Check `json:"source_checks"`
	Checks       []evaluation.Check `json:"checks"`
	TrackOutcome bool               `json:"track_outcome,omitempty"`
}

// Source is either inline text or an exact immutable revision artifact. SHA256
// is always the lowercase digest of the unmodified source text, not its JSON.
type Source struct {
	Text    *string `json:"text,omitempty"`
	Key     string  `json:"key,omitempty"`
	Version int64   `json:"version,omitempty"`
	SHA256  string  `json:"sha256"`
}

// Edit replaces every nonoverlapping literal occurrence in the current text.
// Edits run in order; ExpectedMatches counts occurrences before that edit.
type Edit struct {
	Old             string `json:"old"`
	New             string `json:"new"`
	ExpectedMatches int    `json:"expected_matches"`
}

// Ref pins a revision result without duplicating its text in a response.
type Ref struct {
	Key     string `json:"key"`
	Version int64  `json:"version"`
	SHA256  string `json:"sha256"`
}

type Runner struct{ Broker inference.Broker }

type Result struct {
	Status           string                      `json:"status"`
	RequestSHA256    string                      `json:"request_sha256,omitempty"`
	SourceSHA256     string                      `json:"source_sha256,omitempty"`
	OutputSHA256     string                      `json:"output_sha256,omitempty"`
	Text             string                      `json:"text,omitempty"`
	SourceChecks     []evaluation.CheckResult    `json:"source_checks"`
	Checks           []evaluation.CheckResult    `json:"checks"`
	Reused           bool                        `json:"reused"`
	Durable          bool                        `json:"durable"`
	Artifact         *Ref                        `json:"artifact,omitempty"`
	GenerationCalled bool                        `json:"generation_called"`
	ThisRunUsage     inference.Usage             `json:"this_run_usage"`
	ProviderCostUSD  float64                     `json:"provider_cost_usd"`
	CostBasis        string                      `json:"cost_basis"`
	ElapsedMS        float64                     `json:"elapsed_ms"`
	OutcomeTracking  *delegation.OutcomeTracking `json:"outcome_tracking,omitempty"`
}
