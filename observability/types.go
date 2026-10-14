// Package observability exports bounded execution metadata. Its event types
// deliberately have no prompt, response, task label, user identity, or raw
// error fields. Export configuration is separate from inference identity.
package observability

import (
	"context"
	"time"
)

// Outcome is an allowlisted, content-free execution classification.
type Outcome string

const (
	Complete      Outcome = "complete"
	Reused        Outcome = "reused"
	Pending       Outcome = "pending"
	BudgetBlocked Outcome = "budget_blocked"
	Incomplete    Outcome = "incomplete"
	LimitExceeded Outcome = "limit_exceeded"
	Uncertain     Outcome = "uncertain"
	NotDurable    Outcome = "not_durable"
	Failed        Outcome = "error"
)

// Usage uses Questlock's inclusive input and output totals. Input is partitioned
// into uncached, cache-read, and cache-write tokens. Reasoning is a subset of
// output. Exporters must convert these into non-overlapping backend buckets.
type Usage struct {
	InputTokens         int64
	UncachedInputTokens int64
	CacheReadTokens     int64
	CacheWriteTokens    int64
	OutputTokens        int64
	ReasoningTokens     int64
}

// Generation exists only when a provider Generate call was attempted. An
// uncertain call has KnownUsage=false; missing billing is not reported as zero.
// The interval measures the whole provider call. First content arrival is
// available only through explicit streaming measurements below. Model must be
// a model identifier, never a provider URL or user input.
type Generation struct {
	StartedAt, EndedAt time.Time
	Model              string
	Outcome            Outcome
	KnownUsage         bool
	Usage              Usage
	CostUSD            *float64
	// Optional streaming-only client observations. FirstTokenMS is relative to
	// the generation start; OutputMS is first-to-last nonempty content chunk.
	// These never stand for server compute time or per-token throughput.
	FirstTokenMS, OutputMS *float64
	TimingBasis            string
}

// Execution describes one Runner invocation. Reuse exports a root span with
// zero new usage and no generation child. Original and avoided usage remain
// metadata, never new billable generation usage. Costs are explicit configured
// estimates; they do not establish provider invoice savings. Fingerprint is an
// optional SHA-256 digest, not a task name or revision.
type Execution struct {
	StartedAt, EndedAt                        time.Time
	Fingerprint, Provider, Model              string
	Outcome                                   Outcome
	Reused, Durable                           bool
	ContextBytes, TotalContextBytes           int
	SelectedContext, OmittedContext           int
	ThisRunUsage, OriginalUsage, AvoidedUsage Usage
	CostUSD, AvoidedCostUSD                   *float64
	Generation                                *Generation
	// PhaseMS contains only the fixed internal phases accepted by the exporter.
	// It is metadata; it does not create additional spans or expose task labels.
	PhaseMS map[string]float64
}

// Receipt correlates exported spans without retaining their content. IDs are
// returned even on HTTP failure so callers can investigate an ambiguous export;
// only a nil error indicates that this endpoint acknowledged all spans.
type Receipt struct {
	TraceID          string `json:"trace_id"`
	RootSpanID       string `json:"root_span_id"`
	GenerationSpanID string `json:"generation_span_id,omitempty"`
}

// Exporter is optional. A caller must treat errors as telemetry failures and
// must never retry inference or change the durable generation result for them.
type Exporter interface {
	Export(context.Context, Execution) (Receipt, error)
}
