package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type estimatedProvider struct{ *testProvider }

func (*estimatedProvider) InputTokenCountBasis() string { return "conservative_byte_estimate" }

func requireBudgetDiagnostic(t *testing.T, err error, kind string) *BudgetDiagnostic {
	t.Helper()
	var typed *BudgetError
	if !errors.Is(err, ErrBudgetExceeded) || !errors.As(err, &typed) {
		t.Fatalf("missing typed budget cause: %v", err)
	}
	diagnostic := BudgetDiagnosticFromError(err)
	if diagnostic == nil || diagnostic.LimitKind != kind {
		t.Fatalf("missing %s evidence: %+v", kind, diagnostic)
	}
	return diagnostic
}

func TestInputCapDiagnosticDistinguishesEstimateWithoutGenerating(t *testing.T) {
	r, p := testRunner(t)
	r.Provider = &estimatedProvider{p}
	p.input = 4269
	spec := testSpec()
	spec.Instructions, spec.Input = "synthetic-private-instructions", "synthetic-private-input"
	spec.MaxInputTokens = 4096
	result, err := r.Run(context.Background(), spec)
	diagnostic := requireBudgetDiagnostic(t, err, "input_tokens")
	if diagnostic.ConfiguredLimit != 4096 || diagnostic.EstimatedInputTokens != 4269 || diagnostic.CountedInputTokens != 0 || diagnostic.Required != 4269 || diagnostic.CountBasis != "conservative_byte_estimate" || diagnostic.GenerationCalled || diagnostic.ReportedInputTokens != 0 || diagnostic.Unit != "tokens" {
		t.Fatalf("unactionable estimate: %+v", diagnostic)
	}
	if p.calls != 0 || p.counts != 1 || result.Measurement == nil || result.Measurement.GenerationCalled || result.Measurement.Outcome != "budget_blocked" || result.ThisRunUsage != (Usage{}) || !reflect.DeepEqual(result.BudgetDiagnostic, diagnostic) {
		t.Fatalf("preflight performed generation or lost evidence: %+v", result)
	}
	encoded, _ := json.Marshal(result)
	for _, forbidden := range []string{spec.Instructions, spec.Input, "test-provider", "http://", "fixture"} {
		if strings.Contains(string(encoded), forbidden) || strings.Contains(err.Error(), forbidden) {
			t.Fatalf("diagnostic exposed %q", forbidden)
		}
	}
	// The caller explicitly changes its limit. The rejection changes neither
	// the requested cap nor revision, and introduces no retry or model switch.
	if spec.MaxInputTokens != 4096 || spec.Revision != "commit-1" {
		t.Fatal("preflight changed policy")
	}
	spec.MaxInputTokens = 8192
	if _, err := r.Run(context.Background(), spec); err != nil || p.calls != 1 {
		t.Fatalf("explicit correction failed: %v", err)
	}
}

func TestPreflightCostDiagnostic(t *testing.T) {
	r, p := testRunner(t)
	spec := testSpec()
	spec.Prices = &Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
	spec.MaxCostUSD = .0001
	result, err := r.Run(context.Background(), spec)
	diagnostic := requireBudgetDiagnostic(t, err, "per_call_cost")
	if diagnostic.ConfiguredLimit != 100 || diagnostic.Required != 1100 || diagnostic.CountedInputTokens != 100 || diagnostic.CountBasis != "configured_reservation" || diagnostic.Unit != "usd_micros" || diagnostic.GenerationCalled || p.calls != 0 || result.BudgetDiagnostic == nil {
		t.Fatalf("missing cost bounds: %+v", diagnostic)
	}
}

func TestGenerationBoundaryUpdatesSafeTypedDiagnostic(t *testing.T) {
	r, p := testRunner(t)
	providerDiagnostic := BudgetDiagnostic{LimitKind: "model_context_tokens", ConfiguredLimit: 100, Unit: "tokens", Reason: "exceeds_limit", CountBasis: "conservative_byte_estimate", EstimatedInputTokens: 120}
	p.err = &BudgetError{Diagnostic: providerDiagnostic}
	result, err := r.Run(context.Background(), testSpec())
	diagnostic := requireBudgetDiagnostic(t, err, "model_context_tokens")
	if p.calls != 1 || !diagnostic.GenerationCalled || result.BudgetDiagnostic == nil || !result.BudgetDiagnostic.GenerationCalled || !result.Measurement.GenerationCalled || result.Measurement.Outcome != "uncertain" || providerDiagnostic.GenerationCalled {
		t.Fatalf("generation boundary or provider-owned evidence changed: %+v %+v", result, diagnostic)
	}
}

func TestRuntimeLimitDiagnosticsPersistWithoutRegeneration(t *testing.T) {
	for _, kind := range []string{"input_tokens", "output_tokens"} {
		t.Run(kind, func(t *testing.T) {
			r, p := testRunner(t)
			spec := testSpec()
			switch kind {
			case "input_tokens":
				spec.MaxInputTokens = 100
				p.response.Usage = Usage{InputTokens: 101, UncachedInputTokens: 101, OutputTokens: 25}
			case "output_tokens":
				p.response.Usage.OutputTokens = 101
			}
			result, err := r.Run(context.Background(), spec)
			diagnostic := requireBudgetDiagnostic(t, err, kind)
			if diagnostic.CountBasis != "runtime_reported_usage" || !diagnostic.GenerationCalled || diagnostic.EstimatedInputTokens != 0 || diagnostic.ReportedInputTokens != p.response.Usage.InputTokens || diagnostic.ReportedOutputTokens != p.response.Usage.OutputTokens || result.Response.Complete || result.Measurement.Outcome != "limit_exceeded" || !reflect.DeepEqual(result.BudgetDiagnostic, diagnostic) {
				t.Fatalf("runtime evidence confused with estimate: %+v %+v", result, diagnostic)
			}
			// Even after the reuse TTL, a recorded limit breach cannot generate.
			r.Now = func() time.Time { return result.ExpiresAt.Add(time.Hour) }
			again, replayErr := r.Run(context.Background(), spec)
			replayed := requireBudgetDiagnostic(t, replayErr, kind)
			if p.calls != 1 || again.Measurement.GenerationCalled || !replayed.GenerationCalled || !reflect.DeepEqual(replayed, diagnostic) {
				t.Fatalf("saved limit breach lost cause or regenerated: %+v %+v calls=%d", again, replayed, p.calls)
			}
		})
	}
}

func TestModelContextDiagnosticKeepsConservativeOllamaEstimate(t *testing.T) {
	req := ollamaTestRequest()
	metadata := ollamaMetadata{contextLength: req.ContextWindow, template: "synthetic-template", system: "synthetic-system"}
	estimate, err := ollamaInputEstimate(req, metadata)
	if err != nil {
		t.Fatal(err)
	}
	want := int64(len(req.Prefix)+len(req.Input)+len(metadata.template)+len(metadata.system)) + 512
	if estimate != want {
		t.Fatalf("conservative estimate changed: got %d want %d", estimate, want)
	}
	for _, capacity := range []bool{false, true} {
		changed, model := req, metadata
		if capacity {
			model.contextLength--
		} else {
			changed.ContextWindow = estimate + req.MaxOutputTokens - 1
		}
		_, err := ollamaInputEstimate(changed, model)
		diagnostic := requireBudgetDiagnostic(t, err, "model_context_tokens")
		if diagnostic.EstimatedInputTokens != want || diagnostic.CountBasis != "conservative_byte_estimate" || diagnostic.GenerationCalled || diagnostic.ContextWindow != changed.ContextWindow || diagnostic.Required <= diagnostic.ConfiguredLimit {
			t.Fatalf("context bound missing: %+v", diagnostic)
		}
	}
}

func TestSharedQuotaDiagnostics(t *testing.T) {
	for _, kind := range []string{"daily_input_tokens", "daily_output_tokens", "daily_total_tokens", "daily_cost"} {
		t.Run(kind, func(t *testing.T) {
			r, p := testRunner(t)
			spec := quotaSpec()
			spec.DailyTokenBudget = &TokenBudget{InputTokens: 1000, OutputTokens: 1000, TotalTokens: 2000}
			var err error
			switch kind {
			case "daily_input_tokens":
				spec.DailyTokenBudget.InputTokens = 99
			case "daily_output_tokens":
				spec.DailyTokenBudget.OutputTokens = 99
			case "daily_total_tokens":
				spec.DailyTokenBudget.TotalTokens = 199
			case "daily_cost":
				spec.DailyTokenBudget = nil
				spec.Prices = &Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
				spec.DailyBudgetUSD = .000199
			}
			result, err := r.Run(context.Background(), spec)
			diagnostic := requireBudgetDiagnostic(t, err, kind)
			if p.calls != 0 || diagnostic.GenerationCalled || diagnostic.CountBasis != "configured_reservation" || diagnostic.Required <= diagnostic.ConfiguredLimit || diagnostic.RequestedReservation != diagnostic.Required || result.BudgetDiagnostic == nil {
				t.Fatalf("quota bound missing: %+v", diagnostic)
			}
		})
	}
}

func TestSharedQuotaPolicyAndFreezeDiagnostics(t *testing.T) {
	for _, mode := range []string{"token_policy_required", "token_frozen", "cost_frozen"} {
		t.Run(mode, func(t *testing.T) {
			r, p := testRunner(t)
			spec := quotaSpec()
			kind, reason := "daily_total_tokens", "policy_required"
			if mode == "token_frozen" {
				p.response.Usage = Usage{InputTokens: 101, UncachedInputTokens: 101, OutputTokens: 25}
				kind, reason = "daily_input_tokens", "frozen_after_overrun"
			} else if mode == "cost_frozen" {
				spec.DailyTokenBudget = nil
				spec.Prices = &Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
				spec.DailyBudgetUSD = .01
				p.response.Usage = Usage{InputTokens: 101, UncachedInputTokens: 101, OutputTokens: 100}
				kind, reason = "daily_cost", "frozen_after_overrun"
			}
			_, firstErr := r.Run(context.Background(), spec)
			if mode == "token_policy_required" {
				if firstErr != nil {
					t.Fatal(firstErr)
				}
				spec.DailyTokenBudget = nil
			} else if !errors.Is(firstErr, ErrBudgetExceeded) {
				t.Fatalf("original overrun missing: %v", firstErr)
			}
			spec.Revision = "new-synthetic-task"
			result, err := r.Run(context.Background(), spec)
			diagnostic := requireBudgetDiagnostic(t, err, kind)
			if p.calls != 1 || diagnostic.GenerationCalled || result.Measurement.GenerationCalled || diagnostic.Reason != reason || diagnostic.CurrentUsage <= 0 || diagnostic.RequestedReservation <= 0 {
				t.Fatalf("shared-policy rejection lost evidence or generated: %+v %+v", result, diagnostic)
			}
		})
	}
}

func TestBudgetDiagnosticsRejectUntrustedStringFieldsAndPreserveNilJSON(t *testing.T) {
	base := BudgetDiagnostic{LimitKind: "input_tokens", ConfiguredLimit: 4096, Unit: "tokens", Reason: "exceeds_limit", CountBasis: "conservative_byte_estimate", EstimatedInputTokens: 4269}
	for _, mutate := range []func(*BudgetDiagnostic){
		func(d *BudgetDiagnostic) { d.LimitKind = "secret-provider-body" },
		func(d *BudgetDiagnostic) { d.Unit = "https://secret.invalid" },
		func(d *BudgetDiagnostic) { d.Reason = "private prompt" },
		func(d *BudgetDiagnostic) { d.CountBasis = "/private/config" },
		func(d *BudgetDiagnostic) { d.ConfiguredLimit = -1 },
	} {
		diagnostic := base
		mutate(&diagnostic)
		if got := BudgetDiagnosticFromError(&BudgetError{Diagnostic: diagnostic}); got != nil {
			t.Fatalf("unsafe provider diagnostic accepted: %+v", got)
		}
	}
	wrapped := fmt.Errorf("private provider message: %w", &BudgetError{Diagnostic: base})
	if got := BudgetDiagnosticFromError(wrapped); got == nil || *got != base {
		t.Fatal("safe numeric evidence lost through wrapped API error")
	}
	for _, value := range []any{Result{}, jobRecord{}} {
		encoded, err := json.Marshal(value)
		if err != nil || strings.Contains(string(encoded), "budget_diagnostic") {
			t.Fatalf("nil diagnostic changed old JSON: %s %v", encoded, err)
		}
	}
	r, p := testRunner(t)
	first, err := r.Run(context.Background(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.Run(context.Background(), testSpec())
	if err != nil || !again.Reused || again.Fingerprint != first.Fingerprint || again.BudgetDiagnostic != nil || first.BudgetDiagnostic != nil || p.calls != 1 {
		t.Fatalf("ordinary reuse changed: %+v %v", again, err)
	}
}
