package delegation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"

	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
)

var (
	ErrQualityFailed = errors.New("delegated task did not satisfy every declared quality check")
	ErrStopped       = errors.New("delegated task stopped without escalating; inspect its recorded outcome")
)

// Run executes at most two explicitly configured tiers. A second tier is
// reached only after error-free, known complete and durable output fails the
// declared checks. Every provider/accounting error stops the whole policy.
// Repeated calls retain the same request/tier identity and the Runner's reuse
// behavior; the Router never changes a revision to force another generation.
func (router Router) Run(ctx context.Context, request Request) (Result, error) {
	if err := Validate(request, router.AllowPaid); err != nil {
		return Result{Status: "stopped"}, err
	}
	if router.Factory == nil {
		return Result{Status: "stopped"}, errors.New("delegation runner factory is required")
	}
	result := Result{RequestSHA256: requestDigest(request), Status: "stopped"}
	for _, tier := range tiers(request.Policy) {
		result.Status = "stopped"
		if err := ctx.Err(); err != nil {
			return result, errors.Join(ErrStopped, err)
		}
		spec := tierSpec(request, tier, result.RequestSHA256)
		runner, err := router.Factory(tier)
		if err != nil || runner == nil {
			result.Attempts = append(result.Attempts, Attempt{TierID: tier.ID, ErrorCategory: "runner_initialization_failed", Checks: evaluation.NotEvaluated(request.Checks)})
			return result, ErrStopped
		}
		answer, runErr := runner.Run(ctx, spec)
		checks, quality := evaluation.NotEvaluated(request.Checks), false
		complete := runErr == nil && knownComplete(answer, spec)
		if complete {
			checks, quality = evaluation.CheckOutput(answer.Response.Text, request.Checks)
		}
		diagnostic := inference.BudgetDiagnosticFromError(runErr)
		answer.BudgetDiagnostic = diagnostic
		attempt := Attempt{TierID: tier.ID, Result: answer, Checks: checks, QualityPassed: quality, BudgetDiagnostic: diagnostic}
		if answer.Response.Text != "" {
			digest := sha256.Sum256([]byte(answer.Response.Text))
			attempt.OutputSHA256 = hex.EncodeToString(digest[:])
		}
		attempt.Result.Response.Text = ""
		if runErr != nil {
			result.BudgetDiagnostic = diagnostic
			attempt.ErrorCategory = errorCategory(runErr)
			result.Attempts = append(result.Attempts, attempt)
			return result, stoppedError(runErr)
		}
		if !complete {
			attempt.ErrorCategory = "unconfirmed_complete_outcome"
			result.Attempts = append(result.Attempts, attempt)
			return result, ErrStopped
		}
		result.Attempts = append(result.Attempts, attempt)
		if quality {
			response := answer.Response
			result.SelectedTier, result.Response, result.Status = tier.ID, &response, "completed"
			if request.SemanticShadow != nil {
				result.SemanticShadow = router.Shadow.observe(ctx, request, tier, runner, answer)
			}
			return result, nil
		}
		// Known bad answers are distinct from an incomplete/uncertain call.
		// An explicit next tier is the only allowed further generation.
		result.Status = "quality_failed"
	}
	return result, ErrQualityFailed
}

func knownComplete(result inference.Result, spec inference.Spec) bool {
	if !result.Durable || !result.Response.Complete || result.Response.Text == "" || result.Fingerprint == "" || !validUsage(result.GenerationUsage) {
		return false
	}
	if result.GenerationUsage.InputTokens > spec.MaxInputTokens || result.GenerationUsage.OutputTokens > spec.MaxOutputTokens {
		return false
	}
	if result.Reused {
		if result.ThisRunUsage != (inference.Usage{}) || result.Response.Usage != (inference.Usage{}) || result.AvoidedUsage != result.GenerationUsage {
			return false
		}
		if m := result.Measurement; m != nil && (m.GenerationCalled || m.KnownGenerationUsage || m.GenerationUsage != nil || m.Outcome != "reused") {
			return false
		}
	} else {
		if result.ThisRunUsage != result.GenerationUsage || result.Response.Usage != result.GenerationUsage || result.AvoidedUsage != (inference.Usage{}) {
			return false
		}
		if m := result.Measurement; m != nil && (!m.GenerationCalled || !m.KnownGenerationUsage || m.GenerationUsage == nil || *m.GenerationUsage != result.GenerationUsage || m.Outcome != "complete") {
			return false
		}
	}
	if spec.Prices != nil {
		if result.CostUSD == nil || !finiteNonnegative(*result.CostUSD) || *result.CostUSD > spec.MaxCostUSD {
			return false
		}
		if result.Reused && (*result.CostUSD != 0 || result.AvoidedCostUSD == nil || !finiteNonnegative(*result.AvoidedCostUSD) || *result.AvoidedCostUSD > spec.MaxCostUSD) {
			return false
		}
	}
	return true
}

func validUsage(usage inference.Usage) bool {
	for _, n := range []int64{usage.InputTokens, usage.UncachedInputTokens, usage.CacheReadTokens, usage.CacheWriteTokens, usage.OutputTokens, usage.ReasoningTokens} {
		if n < 0 || n > 1000000000 {
			return false
		}
	}
	return usage.InputTokens > 0 && usage.InputTokens == usage.UncachedInputTokens+usage.CacheReadTokens+usage.CacheWriteTokens && usage.ReasoningTokens <= usage.OutputTokens
}

func finiteNonnegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

// Preserve safe sentinel causes for callers without returning provider errors
// that might embed secrets, raw response bodies, or request text.
func stoppedError(err error) error {
	causes := []error{ErrStopped}
	for _, safe := range []error{context.Canceled, context.DeadlineExceeded, inference.ErrInFlight, inference.ErrIncomplete, inference.ErrBudgetExceeded} {
		if errors.Is(err, safe) {
			if safe == inference.ErrBudgetExceeded {
				if diagnostic := inference.BudgetDiagnosticFromError(err); diagnostic != nil {
					causes = append(causes, &inference.BudgetError{Diagnostic: *diagnostic})
					continue
				}
			}
			causes = append(causes, safe)
		}
	}
	if len(causes) == 1 {
		return ErrStopped
	}
	return errors.Join(causes...)
}

func errorCategory(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, inference.ErrInFlight):
		return "pending_or_uncertain"
	case errors.Is(err, inference.ErrIncomplete):
		return "incomplete"
	case errors.Is(err, inference.ErrBudgetExceeded):
		return "budget_exceeded"
	default:
		return "run_failed"
	}
}
