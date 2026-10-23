// Package delegation runs an explicit bounded local-first task policy. It can
// escalate once after a known complete durable answer fails declared checks;
// it never retries uncertain calls, intercepts an agent session, or picks models.
package delegation

import (
	"context"

	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/outcomes"
)

// Request.Spec supplies approved task, revision, prompt and context only.
// Every model and resource setting comes from the explicit tier templates.
type Request struct {
	TrackOutcome bool               `json:"track_outcome,omitempty"` // Opt-in shared accounting; excluded from generation identity.
	Spec         inference.Spec     `json:"spec"`
	Policy       Policy             `json:"policy"`
	Checks       []evaluation.Check `json:"checks"`
}

type Policy struct {
	Local      Tier  `json:"local"`
	Escalation *Tier `json:"escalation,omitempty"`
}

// Tier.Spec must omit task, revision, instructions, input and context. Hosted
// escalation requires explicit prices and positive per-call and daily budgets.
type Tier struct {
	ID   string         `json:"id"`
	Spec inference.Spec `json:"spec"`
}

type Runner interface {
	Run(context.Context, inference.Spec) (inference.Result, error)
}

type RunnerFactory func(Tier) (Runner, error)

type Router struct {
	Factory   RunnerFactory
	AllowPaid bool // Caller permission, never read from request JSON or environment.
}

type Attempt struct {
	BudgetDiagnostic *inference.BudgetDiagnostic `json:"budget_diagnostic,omitempty"`
	TierID           string                      `json:"tier_id"`
	Result           inference.Result            `json:"result"` // Response.Text is always stripped.
	Checks           []evaluation.CheckResult    `json:"checks"`
	QualityPassed    bool                        `json:"quality_passed"`
	OutputSHA256     string                      `json:"output_sha256,omitempty"`
	ErrorCategory    string                      `json:"error_category,omitempty"`
}

type Result struct {
	BudgetDiagnostic *inference.BudgetDiagnostic `json:"budget_diagnostic,omitempty"`
	OutcomeTracking  *OutcomeTracking            `json:"outcome_tracking,omitempty"`
	RequestSHA256    string                      `json:"request_sha256"`
	Attempts         []Attempt                   `json:"attempts"`
	SelectedTier     string                      `json:"selected_tier,omitempty"`
	Response         *inference.Response         `json:"response,omitempty"` // Only a passing answer is exposed.
	Status           string                      `json:"status"`             // completed, quality_failed, stopped
}

// OutcomeTracking is separate from answer quality. Registration failure never
// changes a checked answer or authorizes another model call. Record is returned
// on failure so the exact metadata can be reconciled without regenerating text.
type OutcomeTracking struct {
	Status  string            `json:"status"` // recorded or failed
	RunID   string            `json:"run_id"`
	Task    outcomes.Task     `json:"task"`
	Receipt *outcomes.Receipt `json:"receipt,omitempty"`
	Record  *outcomes.Run     `json:"record,omitempty"`
}
