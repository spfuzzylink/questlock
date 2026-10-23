package delegation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
)

func TestBudgetStopRetainsSafeTypedCauseAndSkipsQuality(t *testing.T) {
	diagnostic := inference.BudgetDiagnostic{LimitKind: "input_tokens", ConfiguredLimit: 4096, Unit: "tokens", Reason: "exceeds_limit", CountBasis: "conservative_byte_estimate", EstimatedInputTokens: 4269, Required: 4269}
	local := &scriptedRunner{result: inference.Result{Measurement: &inference.Measurement{Outcome: "budget_blocked"}}, err: fmt.Errorf("provider-secret-token private-prompt https://private.invalid /private/config: %w", &inference.BudgetError{Diagnostic: diagnostic})}
	factories := 0
	result, err := (Router{Factory: func(Tier) (Runner, error) { factories++; return local, nil }}).Run(context.Background(), testRequest())
	var typed *inference.BudgetError
	if !errors.Is(err, ErrStopped) || !errors.Is(err, inference.ErrBudgetExceeded) || !errors.As(err, &typed) || typed.Diagnostic != diagnostic || factories != 1 || local.calls != 1 || result.Status != "stopped" || len(result.Attempts) != 1 || result.Response != nil {
		t.Fatalf("budget cause lost or escalation called: %v %+v", err, result)
	}
	attempt := result.Attempts[0]
	if !reflect.DeepEqual(result.BudgetDiagnostic, &diagnostic) || !reflect.DeepEqual(attempt.BudgetDiagnostic, &diagnostic) || !reflect.DeepEqual(attempt.Result.BudgetDiagnostic, &diagnostic) || attempt.ErrorCategory != "budget_exceeded" || attempt.QualityPassed {
		t.Fatalf("structured evidence lost: %+v", attempt)
	}
	for _, check := range attempt.Checks {
		if check.Status != "not_evaluated" || check.Passed {
			t.Fatalf("budget rejection appeared as model quality: %+v", check)
		}
	}
	encoded, _ := json.Marshal(result)
	for _, forbidden := range []string{"provider-secret-token", "private-prompt", "https://private.invalid", "/private/config"} {
		if strings.Contains(err.Error(), forbidden) || strings.Contains(string(encoded), forbidden) {
			t.Fatalf("sensitive provider error exposed %q", forbidden)
		}
	}
	// The returned error owns a safe copy rather than the provider's pointer.
	typed.Diagnostic.EstimatedInputTokens = 1
	if result.BudgetDiagnostic.EstimatedInputTokens != 4269 {
		t.Fatal("typed error mutation changed result evidence")
	}
}

func TestBudgetStopDropsUntrustedDiagnosticText(t *testing.T) {
	for _, field := range []string{"limit_kind", "unit", "reason", "count_basis"} {
		diagnostic := inference.BudgetDiagnostic{LimitKind: "input_tokens", ConfiguredLimit: 4096, Unit: "tokens", Reason: "exceeds_limit", CountBasis: "conservative_byte_estimate"}
		switch field {
		case "limit_kind":
			diagnostic.LimitKind = "sensitive-provider-body"
		case "unit":
			diagnostic.Unit = "sensitive-provider-body"
		case "reason":
			diagnostic.Reason = "sensitive-provider-body"
		case "count_basis":
			diagnostic.CountBasis = "sensitive-provider-body"
		}
		local := &scriptedRunner{result: inference.Result{BudgetDiagnostic: &diagnostic}, err: &inference.BudgetError{Diagnostic: diagnostic}}
		result, err := (Router{Factory: func(Tier) (Runner, error) { return local, nil }}).Run(context.Background(), testRequest())
		var typed *inference.BudgetError
		encoded, _ := json.Marshal(result)
		if !errors.Is(err, inference.ErrBudgetExceeded) || errors.As(err, &typed) || result.BudgetDiagnostic != nil || strings.Contains(string(encoded), "sensitive-provider-body") || strings.Contains(err.Error(), "sensitive-provider-body") {
			t.Fatalf("untrusted %s leaked through typed cause: %v %s", field, err, encoded)
		}
	}
}

func TestChecksAreNotEvaluatedWithoutKnownCompleteAnswer(t *testing.T) {
	request := testRequest()
	request.Checks = []evaluation.Check{{ID: "absent", Kind: "not_contains", Text: "BAD"}}
	for _, kind := range []string{"empty", "incomplete", "initialization", "error_with_text"} {
		t.Run(kind, func(t *testing.T) {
			answer := knownResult("good text")
			var runErr error
			switch kind {
			case "empty":
				answer.Response.Text = ""
			case "incomplete":
				answer.Response.Complete = false
			case "error_with_text":
				runErr = inference.ErrIncomplete
			}
			result, err := (Router{Factory: func(Tier) (Runner, error) {
				if kind == "initialization" {
					return nil, errors.New("synthetic init failure")
				}
				return &scriptedRunner{result: answer, err: runErr}, nil
			}}).Run(context.Background(), request)
			if !errors.Is(err, ErrStopped) || len(result.Attempts) != 1 || result.Attempts[0].QualityPassed || len(result.Attempts[0].Checks) != 1 || result.Attempts[0].Checks[0].Status != "not_evaluated" || result.Attempts[0].Checks[0].Passed {
				t.Fatalf("non-answer appeared quality checked: %v %+v", err, result)
			}
		})
	}
	answer := knownResult("good reused text")
	answer.Reused, answer.AvoidedUsage = true, answer.GenerationUsage
	answer.ThisRunUsage, answer.Response.Usage = inference.Usage{}, inference.Usage{}
	answer.Measurement = &inference.Measurement{Outcome: "reused"}
	result, err := (Router{Factory: func(Tier) (Runner, error) { return &scriptedRunner{result: answer}, nil }}).Run(context.Background(), request)
	if err != nil || !result.Attempts[0].QualityPassed || !result.Attempts[0].Checks[0].Passed || result.Attempts[0].Checks[0].Status != "" {
		t.Fatalf("known complete exact reuse skipped quality: %v %+v", err, result)
	}
}

func TestExportedRequestDigestPreservesCanonicalIdentity(t *testing.T) {
	request := testRequest()
	if err := Validate(request, false); err != nil {
		t.Fatal(err)
	}
	digest := RequestDigest(request)
	// This pre-existing literal has no optional counting or diagnostic fields.
	const legacyRequest = `{"spec":{"task":"release-review","revision":"snapshot-1","provider":"","model":"","instructions":"Return only JSON with ship false when a required gate is missing.","input":"Unit and race passed; container has not run.","max_context_bytes":0,"max_input_tokens":0,"max_output_tokens":0},"policy":{"local":{"id":"compact","spec":{"task":"","revision":"","provider":"ollama","model":"qwen3.5:4b","instructions":"","input":"","max_context_bytes":8192,"max_input_tokens":8192,"max_output_tokens":128,"reuse_for_seconds":3600}}},"checks":[{"id":"expected","kind":"json_equals","expected":{"ship":false}}]}`
	var legacy Request
	if err := json.Unmarshal([]byte(legacyRequest), &legacy); err != nil {
		t.Fatal(err)
	}
	legacyDigest := sha256.Sum256([]byte(legacyRequest))
	if RequestDigest(legacy) != fmt.Sprintf("%x", legacyDigest) {
		t.Fatal("export changed old nil-field identity")
	}
	request.TrackOutcome = true
	if RequestDigest(request) != digest {
		t.Fatal("outcome tracking changed request identity")
	}
	result, err := (Router{Factory: func(Tier) (Runner, error) { return &scriptedRunner{result: knownResult(`{"ship":false}`)}, nil }}).Run(context.Background(), request)
	if err != nil || result.RequestSHA256 != digest {
		t.Fatalf("exported digest disagrees with router: %v %+v", err, result)
	}
	for _, value := range []any{Result{}, Attempt{}} {
		encoded, _ := json.Marshal(value)
		if strings.Contains(string(encoded), "budget_diagnostic") {
			t.Fatalf("nil diagnostic changed old result JSON: %s", encoded)
		}
	}
}
