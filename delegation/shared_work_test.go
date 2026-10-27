package delegation

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/outcomes"
	"github.com/spfuzzylink/questlock/semantic"
)

func TestExperimentalPairServingAPIIsUnavailable(t *testing.T) {
	for _, test := range []struct {
		typ    reflect.Type
		method string
	}{
		{reflect.TypeOf(&Router{}), "RunReuseBatch"},
		{reflect.TypeOf(&LocalVerifier{}), "VerifyBatch"},
		{reflect.TypeOf(&LocalVerifier{}), "BatchIdentity"},
	} {
		if _, ok := test.typ.MethodByName(test.method); ok {
			t.Fatalf("withdrawn pair serving method remains: %s", test.method)
		}
	}
	if _, ok := reflect.TypeOf(semantic.ReuseConfig{}).FieldByName("BatchOnly"); ok {
		t.Fatal("withdrawn serving policy remains configurable")
	}
}

type diagnosticSharedVerifier struct{ calls int }

func (*diagnosticSharedVerifier) Identity() string { return strings.Repeat("a", 64) }
func (v *diagnosticSharedVerifier) Verify(context.Context, semantic.ValidityRequest) (semantic.ValidityResult, error) {
	v.calls++
	zero := int64(0)
	return semantic.ValidityResult{Verdict: "accept", SameIntent: true, SameFacts: true, SameConstraints: true, AnswerSupported: true, SharedWork: true, Reused: true, Complete: true, Durable: true, InputTokens: &zero, OutputTokens: &zero, ReceiptKey: "inference/v1/jobs/" + strings.Repeat("b", 64)}, nil
}

func TestControlledReuseAlwaysRefusesSharedDiagnosticDecision(t *testing.T) {
	b := testBroker(t)
	p := &fixtureProvider{identity: "diagnostic-refusal-target", response: knownResult(`{"ship":false}`).Response}
	v := &diagnosticSharedVerifier{}
	cfg := controlledTestConfig()
	router := Router{Reuse: &ControlledReuser{Broker: b, Index: controlledStoredIndex{b}, EmbedderFactory: func(context.Context, semantic.Config) (semantic.Embedder, error) {
		return &shadowFailureEmbedder{}, nil
	}, VerifierFactory: func(context.Context, semantic.ReuseConfig) (semantic.Verifier, error) { return v, nil }}, Factory: func(Tier) (Runner, error) { return &inference.Runner{Broker: b, Provider: p}, nil }}
	req := testRequest()
	req.Policy.Escalation = nil
	req.SemanticReuse = &cfg
	req.Spec.Input = "How do I reset an account password?"
	if source, err := router.Run(t.Context(), req); err != nil || source.SemanticReuse == nil || !source.SemanticReuse.Indexed {
		t.Fatalf("synthetic source: %+v %v", source, err)
	}
	p.countCalls, p.generations = 0, 0
	req.Spec.Input = "How can I reset an account password?"
	result, err := router.Run(t.Context(), req)
	if err == nil || result.Status != "stopped" || result.Response != nil || result.SemanticReuse == nil || result.SemanticReuse.Status != "verification_unconfirmed" || p.countCalls != 0 || p.generations != 0 || v.calls != 1 {
		t.Fatalf("shared diagnostic result entered serving: %+v %v", result, err)
	}
	if _, err := router.Run(t.Context(), req); !errors.Is(err, inference.ErrInFlight) || p.generations != 0 || v.calls != 1 {
		t.Fatalf("shared diagnostic uncertainty retried: %v", err)
	}
}

func TestSharedMemberCannotPublishZeroWorkOutcome(t *testing.T) {
	for _, receipt := range []string{"inference/v1/jobs/" + strings.Repeat("b", 64), ""} {
		t.Run(receipt, func(t *testing.T) {
			b := testBroker(t)
			zero := int64(0)
			req := testRequest()
			result := Result{RequestSHA256: strings.Repeat("a", 64), Status: "stopped", SemanticReuse: &ReuseObservation{Verifications: []semantic.ValidityResult{{SharedWork: true, Reused: true, Complete: true, Durable: true, InputTokens: &zero, OutputTokens: &zero, ReceiptKey: receipt, OutputSHA256: strings.Repeat("c", 64)}}}}
			RecordOutcome(t.Context(), b, req, &result, "member")
			if result.OutcomeTracking == nil || result.OutcomeTracking.Status != "failed" || result.OutcomeTracking.ErrorCategory != "shared_work_requires_batch_accounting" || result.OutcomeTracking.Record != nil {
				t.Fatalf("shared member became free ledger: %+v", result.OutcomeTracking)
			}
			_, err := (outcomes.Store{Broker: b}).Record(t.Context(), outcomes.Task{ID: req.Spec.Task, Revision: req.Spec.Revision}, OutcomeRecord(req, result, "manual-member"))
			if err == nil || errors.Is(err, inference.ErrInFlight) {
				t.Fatalf("manual helper published omitted parent work: %v", err)
			}
		})
	}
}
