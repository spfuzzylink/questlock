package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/semantic"
)

func controlledTestConfig() semantic.ReuseConfig {
	c, err := semantic.NormalizeReuseConfig(semantic.ReuseConfig{Config: semantic.Config{Mode: "controlled", Family: "support", Domain: "synthetic", EvidenceRevision: "r1", EmbeddingModel: "all-minilm"}, ReadOnly: true, Intent: "answer-support-policy", VerifierModel: "qwen3.5:4b"})
	if err != nil {
		panic(err)
	}
	return c
}

func TestLocalVerifierAccountsKnownFreshAndExactDecision(t *testing.T) {
	p := &fixtureProvider{identity: "validity-fixture", response: knownResult(`{"verdict":"accept","same_intent":true,"same_facts":true,"same_constraints":true,"answer_supported":true}`).Response}
	v := &LocalVerifier{runner: &inference.Runner{Broker: testBroker(t), Provider: p}, config: controlledTestConfig(), identity: strings.Repeat("a", 64)}
	input := semantic.ValidityRequest{OriginalQuestion: "How do I reset a password?", Question: "How can I reset a password?", CachedAnswer: "Use reset.", Intent: "support"}
	first, err := v.Verify(context.Background(), input)
	if err != nil || !first.Accepted() || !first.Complete || !first.Durable || !first.GenerationCalled || first.InputTokens == nil || *first.InputTokens != 100 || first.OutputTokens == nil || *first.OutputTokens != 20 || first.ReceiptKey == "" {
		t.Fatalf("fresh verification evidence: %+v %v", first, err)
	}
	second, err := v.Verify(context.Background(), input)
	if err != nil || !second.Accepted() || !second.Reused || second.GenerationCalled || second.InputTokens == nil || *second.InputTokens != 0 || second.OutputTokens == nil || *second.OutputTokens != 0 || first.ReceiptKey != second.ReceiptKey {
		t.Fatalf("reused verification evidence: %+v %v", second, err)
	}
}

func TestLocalVerifierMalformedCompleteVerdictAbstainsWithUsage(t *testing.T) {
	for _, text := range []string{`{"verdict":"accept"}`, `{"verdict":"accept","same_intent":true,"same_facts":true,"same_constraints":true,"answer_supported":true,"input_tokens":0}`, `not JSON`} {
		t.Run(text, func(t *testing.T) {
			p := &fixtureProvider{identity: "validity-fixture", response: knownResult(text).Response}
			v := &LocalVerifier{runner: &inference.Runner{Broker: testBroker(t), Provider: p}, config: controlledTestConfig(), identity: strings.Repeat("a", 64)}
			result, err := v.Verify(context.Background(), semantic.ValidityRequest{Question: "Same question?"})
			if err != nil || result.Accepted() || result.Verdict != "unknown" || !result.GenerationCalled || result.InputTokens == nil || *result.InputTokens != 100 || !result.Complete || !result.Durable {
				t.Fatalf("malformed answer authorized reuse or lost accounting: %+v %v", result, err)
			}
		})
	}
}

func TestControlledPolicyDoesNotChangeExistingGenerationIdentity(t *testing.T) {
	r := testRequest()
	original := requestDigest(r)
	c := controlledTestConfig()
	r.SemanticReuse = &c
	if requestDigest(r) != original {
		t.Fatal("controlled policy changed existing exact fingerprint")
	}
	r.Policy.Escalation = nil
	if err := Validate(r, false); err != nil {
		t.Fatal(err)
	}
	r.SemanticShadow = &c.Config
	if Validate(r, false) == nil {
		t.Fatal("combined shadow and serving policy accepted")
	}
}

func TestVerifierCostIsIncludedInOutcomeAccounting(t *testing.T) {
	input, output := int64(120), int64(30)
	request := testRequest()
	result := Result{RequestSHA256: strings.Repeat("a", 64), Status: "completed", SemanticReuse: &ReuseObservation{Verifications: []semantic.ValidityResult{{Verdict: "reject", InputTokens: &input, OutputTokens: &output, ReceiptKey: "inference/v1/jobs/" + strings.Repeat("b", 64), GenerationCalled: true, Complete: true, Durable: true}}}}
	record := OutcomeRecord(request, result, "test-run")
	if len(record.VerificationWork) != 1 || record.VerificationWork[0].Usage.InputTokens != input || record.VerificationWork[0].Usage.OutputTokens != output || !record.VerificationWork[0].GenerationCalled || record.VerificationWork[0].CostBasis != "local_token_charge" {
		data, _ := json.Marshal(record)
		t.Fatalf("verification hidden from accounting: %s", data)
	}
}

func TestLocalVerifierUnknownGenerationRemainsInOutcomeAccounting(t *testing.T) {
	for _, kind := range []string{"generation_error", "invalid_usage"} {
		t.Run(kind, func(t *testing.T) {
			provider := &fixtureProvider{identity: "unknown-verification-fixture", response: knownResult(`{"verdict":"reject","same_intent":false,"same_facts":false,"same_constraints":false,"answer_supported":false}`).Response}
			if kind == "generation_error" {
				provider.err = errors.New("synthetic broken generation")
			} else {
				provider.response.Usage.UncachedInputTokens = 0
			}
			v := &LocalVerifier{runner: &inference.Runner{Broker: testBroker(t), Provider: provider}, config: controlledTestConfig(), identity: strings.Repeat("a", 64)}
			verdict, err := v.Verify(context.Background(), semantic.ValidityRequest{Question: "Same question?"})
			if err == nil || !verdict.GenerationCalled || verdict.ReceiptKey == "" || verdict.InputTokens != nil || verdict.Complete || verdict.Durable {
				t.Fatalf("unknown generation lost: %+v %v", verdict, err)
			}
			run := OutcomeRecord(testRequest(), Result{Status: "stopped", SemanticReuse: &ReuseObservation{Verifications: []semantic.ValidityResult{verdict}}}, "unknown-run")
			if len(run.VerificationWork) != 1 || !run.VerificationWork[0].GenerationCalled || run.VerificationWork[0].UsageKnown || run.VerificationWork[0].Complete || run.VerificationWork[0].Durable {
				t.Fatalf("unknown verification disappeared from outcome: %+v", run)
			}
		})
	}
}
