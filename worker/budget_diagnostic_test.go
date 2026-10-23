package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
)

func TestMCPBudgetStopReturnsSafeTypedEvidenceAndAdvertisesSchema(t *testing.T) {
	root := t.TempDir()
	requestFile(t, root, "approved.json", approvedRequest())
	diagnostic := inference.BudgetDiagnostic{LimitKind: "input_tokens", ConfiguredLimit: 4096, Unit: "tokens", Reason: "exceeds_limit", CountBasis: "conservative_byte_estimate", EstimatedInputTokens: 4269, Required: 4269}
	executions := 0
	session, _, _, _ := connectWorker(t, Options{Workspace: root, Execute: func(_ context.Context, request delegation.Request) (delegation.Result, error) {
		executions++
		return delegation.Result{Status: "stopped", BudgetDiagnostic: &diagnostic, Attempts: []delegation.Attempt{{
			TierID: "local", ErrorCategory: "budget_exceeded", BudgetDiagnostic: &diagnostic, Checks: evaluation.NotEvaluated(request.Checks),
			Result: inference.Result{BudgetDiagnostic: &diagnostic, Measurement: &inference.Measurement{Outcome: "budget_blocked"}},
		}}}, errors.Join(delegation.ErrStopped, fmt.Errorf("PRIVATE_PROVIDER_ERROR private-prompt https://private.invalid /private/config: %w", &inference.BudgetError{Diagnostic: diagnostic}))
	}}, "2026-07-28")
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	schema := listed.Tools[0].OutputSchema.(map[string]any)
	properties := schema["properties"].(map[string]any)
	if properties["budget_diagnostic"].(map[string]any)["type"] != "object" {
		t.Fatal("MCP output schema omits budget diagnostics")
	}
	result := call(t, session, map[string]any{"request_path": "approved.json"})
	output := decodeOutput(t, result)
	if !result.IsError || output.Status != "stopped" || output.ErrorCategory != "budget_exceeded" || output.Response != nil || output.SelectedTier != "" || len(output.Attempts) != 1 || executions != 1 {
		t.Fatalf("budget stop lost category or executed again: %+v", output)
	}
	attempt := output.Attempts[0]
	for _, observed := range []*inference.BudgetDiagnostic{output.BudgetDiagnostic, attempt.BudgetDiagnostic, attempt.Result.BudgetDiagnostic} {
		if !reflect.DeepEqual(observed, &diagnostic) {
			t.Fatalf("MCP lost safe numeric evidence: %+v", observed)
		}
	}
	if attempt.QualityPassed || attempt.Checks[0].Status != "not_evaluated" || attempt.Checks[0].Passed || attempt.Result.Measurement.GenerationCalled || attempt.Result.ThisRunUsage != (inference.Usage{}) {
		t.Fatalf("preflight looked like generated quality failure: %+v", attempt)
	}
	serialized, _ := json.Marshal(result)
	for _, secret := range []string{"PRIVATE_PROVIDER_ERROR", "private-prompt", "https://private.invalid", "/private/config"} {
		if strings.Contains(string(serialized), secret) {
			t.Fatalf("MCP returned unsafe provider text %q", secret)
		}
	}
}

func TestSafeOutputSanitizesAndCopiesEveryBudgetDiagnostic(t *testing.T) {
	diagnostic := inference.BudgetDiagnostic{LimitKind: "input_tokens", ConfiguredLimit: 4096, Unit: "tokens", Reason: "exceeds_limit", CountBasis: "conservative_byte_estimate", EstimatedInputTokens: 4269}
	result := delegation.Result{Status: "stopped", BudgetDiagnostic: &diagnostic, Attempts: []delegation.Attempt{{BudgetDiagnostic: &diagnostic, Result: inference.Result{BudgetDiagnostic: &diagnostic}}}}
	output := safeOutput(result, inference.ErrBudgetExceeded)
	for _, copied := range []*inference.BudgetDiagnostic{output.BudgetDiagnostic, output.Attempts[0].BudgetDiagnostic, output.Attempts[0].Result.BudgetDiagnostic} {
		if copied == &diagnostic || !reflect.DeepEqual(copied, &diagnostic) {
			t.Fatal("safe output shares caller-owned diagnostic")
		}
		copied.EstimatedInputTokens = 1
	}
	if diagnostic.EstimatedInputTokens != 4269 || result.Attempts[0].Result.BudgetDiagnostic.EstimatedInputTokens != 4269 {
		t.Fatal("safe output changed durable caller evidence")
	}
	diagnostic.Reason = "PRIVATE_PROVIDER_ERROR"
	output = safeOutput(result, &inference.BudgetError{Diagnostic: diagnostic})
	encoded, _ := json.Marshal(output)
	if output.BudgetDiagnostic != nil || output.Attempts[0].BudgetDiagnostic != nil || output.Attempts[0].Result.BudgetDiagnostic != nil || output.ErrorCategory != "budget_exceeded" || strings.Contains(string(encoded), "PRIVATE_PROVIDER_ERROR") {
		t.Fatalf("unsafe diagnostic survived MCP boundary: %s", encoded)
	}
}
