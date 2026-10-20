// Package team coordinates bounded local master/reviewer tasks through one
// authoritative broker. Roles propose text; they do not edit files or publish
// external actions. Model review is advisory and never human acceptance.
package team

import (
	"context"
	"errors"
	"time"

	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
)

var (
	ErrPending = errors.New("role is pending or uncertain; inspect state, do not automatically retry generation")
	ErrQuality = errors.New("team quality checks did not pass; no fallback was attempted")
	ErrExpired = errors.New("approved snapshot reuse window expired; explicitly review a new revision")
	ErrRecord  = errors.New("invalid shared team record")
)

type Role struct {
	Ollama          *inference.OllamaOptions `json:"ollama,omitempty"`
	ID              string                   `json:"id"`
	Driver          string                   `json:"driver"` // ollama or codex-oss; no hosted driver
	Model           string                   `json:"model"`
	ModelDigest     string                   `json:"model_digest"`
	Instructions    string                   `json:"instructions"`
	MaxInputTokens  int64                    `json:"max_input_tokens"`
	MaxOutputTokens int64                    `json:"max_output_tokens"`
}

type Request struct {
	// PolicyRevision names a reviewed execution-policy change without changing
	// the task/data revision bound to a previously measured baseline.
	PolicyRevision  string                  `json:"policy_revision,omitempty"`
	Schema          int                     `json:"schema"`
	Task            string                  `json:"task"`
	Revision        string                  `json:"revision"`
	Instructions    string                  `json:"instructions"`
	Input           string                  `json:"input"`
	Context         []inference.ContextItem `json:"context,omitempty"`
	Master          Role                    `json:"master"`
	Reviewers       []Role                  `json:"reviewers"`
	Checks          []evaluation.Check      `json:"checks"`
	ReuseForSeconds int64                   `json:"reuse_for_seconds"`
}

type Factory func(context.Context, Role) (delegation.Runner, error)

type Engine struct {
	Broker   inference.Broker
	Factory  Factory
	WorkerID string
	Now      func() time.Time
	// Admit may wait for local capacity before executing a new role's claim and
	// model call. Cache reads/finalization do not need inference admission.
	Admit func(context.Context, func(context.Context) error) error
}

type Usage struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	Known           bool  `json:"known"`
	GenerationCalls int   `json:"generation_calls"`
}

type Step struct {
	OutcomeTrackingStatus string `json:"outcome_tracking_status"`
	Role                  string `json:"role"`
	State                 string `json:"state"`
	WorkerID              string `json:"worker_id,omitempty"`
	Reused                bool   `json:"reused"`
	OutputSHA256          string `json:"output_sha256,omitempty"`
	Usage                 Usage  `json:"this_run_local_usage"`
}

type Report struct {
	AccountingComplete  bool                     `json:"accounting_complete"`
	Schema              int                      `json:"schema"`
	RequestSHA256       string                   `json:"request_sha256"`
	Status              string                   `json:"status"`
	Steps               []Step                   `json:"steps"`
	Text                string                   `json:"text,omitempty"` // exposed only after every gate passes
	OutputSHA256        string                   `json:"output_sha256,omitempty"`
	Checks              []evaluation.CheckResult `json:"checks,omitempty"`
	LocalUsage          Usage                    `json:"this_run_local_usage"`
	HostedTokens        int64                    `json:"this_run_hosted_tokens"`
	HostedUsageKnown    bool                     `json:"hosted_usage_known"`
	ProviderCostUSD     float64                  `json:"provider_cost_usd"`
	PaidFallbackEnabled bool                     `json:"paid_fallback_enabled"`
	HumanAcceptance     string                   `json:"human_acceptance"`
}

type record struct {
	Schema          int                `json:"schema"`
	RequestSHA256   string             `json:"request_sha256"`
	Role            string             `json:"role"`
	WorkerID        string             `json:"worker_id"`
	CandidateSHA256 string             `json:"candidate_sha256,omitempty"`
	State           string             `json:"state"`
	CreatedAt       time.Time          `json:"created_at"`
	ExpiresAt       time.Time          `json:"expires_at"`
	Outcome         *delegation.Result `json:"outcome,omitempty"`
}
