package delegation

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/semantic"
)

func TestFactorizedTargetRiskRetainsSourceCostAndSkipsPair(t *testing.T) {
	v, provider := factorizedTestVerifier(t, nil)
	r := factorizedTestRequest()
	r.OriginalQuestion = "Are visitors permitted to use the west entrance?"
	r.Question = "Are visitors forbidden to use the west entrance?"
	r.Instructions = "Use the approved access record."
	r.Context = "Visitors may use the west entrance during opening hours."
	r.CachedAnswer = "Yes. Visitors may use the west entrance during opening hours."
	r.Source.OutputSHA256 = reuseHash(r.CachedAnswer)
	out, err := v.Verify(t.Context(), r)
	if err != nil || out.Accepted() || out.Verdict != "reject" || !out.AnswerSupported || !out.Complete || !out.Durable || !out.GenerationCalled || provider.sourceCalls != 1 || provider.pairCalls != 0 || len(out.Work) != 1 || out.Work[0].Stage != "source" || out.InputTokens == nil || *out.InputTokens != 101 || out.OutputTokens == nil || *out.OutputTokens != 20 {
		t.Fatalf("risk withholding lost source work or reached pair: %+v source=%d pair=%d err=%v", out, provider.sourceCalls, provider.pairCalls, err)
	}
	if err := semantic.ValidateVerifierWork(out, v.config); err != nil {
		t.Fatalf("known source-only rejection is not valid accounting: %v", err)
	}
	artifact, err := v.runner.Broker.Get(t.Context(), out.ReceiptKey)
	var receipt factorizedReceipt
	if err != nil || json.Unmarshal([]byte(artifact.Content), &receipt) != nil || receipt.Pair != nil || receipt.Support.Key != out.Work[0].ReceiptKey {
		t.Fatalf("risk decision fabricated pair proof: %+v %v", receipt, err)
	}
	again, err := v.Verify(t.Context(), r)
	if err != nil || again.Accepted() || !again.Reused || again.GenerationCalled || again.InputTokens == nil || *again.InputTokens != 0 || provider.sourceCalls != 1 || provider.pairCalls != 0 {
		t.Fatalf("known withholding repeat duplicated work: %+v %v", again, err)
	}
}

func TestControlledTargetRiskMissAvoidsVerifierAndExactStillWins(t *testing.T) {
	f := independentFactorizedSetup(t)
	f.request.Spec.Input = "Are visitors permitted to use the west entrance?"
	f.request.Spec.Instructions = "Use the approved access record."
	f.request.Spec.Context[0].Text = "Visitors may use the west entrance during opening hours."
	f.request.Checks = []evaluation.Check{{ID: "retained-answer", Kind: "contains", Text: "Yes."}}
	f.target.response = knownResult("Yes. Visitors may use the west entrance during opening hours.").Response
	f.seed(t)
	exact, err := f.router.Run(t.Context(), f.request)
	if err != nil || !exact.Attempts[0].Result.Reused || len(f.stages.requests) != 0 || f.target.generations != 1 {
		t.Fatalf("risk policy changed exact-first behavior: %+v %v", exact, err)
	}
	f.request.Spec.Input = "Are visitors forbidden to use the west entrance?"
	out, err := f.router.Run(t.Context(), f.request)
	if err != nil || out.SemanticReuse == nil || out.SemanticReuse.GenerationAvoided || len(f.stages.requests) != 0 || len(out.SemanticReuse.Verifications) != 0 || !slices.Contains(out.SemanticReuse.Rejections, "target_risk_binary_orientation") || f.target.generations != 2 {
		t.Fatalf("risk miss authorized reuse or verifier work: %+v stageCalls=%d err=%v", out, len(f.stages.requests), err)
	}
}

func TestFactorizedStructuredAuthorityRiskRetainsSourceProof(t *testing.T) {
	v, provider := factorizedTestVerifier(t, nil)
	r := factorizedTestRequest()
	r.OriginalQuestion = "Whose approvals are required before the request may proceed?"
	r.Question = "Is the curator the sole approver required before the request may proceed?"
	r.Instructions = "Return the recorded required approvers as a JSON object."
	r.Context = "The request requires approvals from the curator and the steward."
	r.CachedAnswer = `{"required_approvers":["curator","steward"]}`
	r.Source.OutputSHA256 = reuseHash(r.CachedAnswer)
	out, err := v.Verify(t.Context(), r)
	if err != nil || out.Accepted() || out.Verdict != "reject" || !out.AnswerSupported || !out.Complete || !out.Durable || provider.sourceCalls != 1 || provider.pairCalls != 0 || len(out.Work) != 1 || out.InputTokens == nil || *out.InputTokens != 101 {
		t.Fatalf("structured authority risk bypassed source proof or reached pair: %+v err=%v", out, err)
	}
	if err := semantic.ValidateVerifierWork(out, v.config); err != nil {
		t.Fatalf("structured source-only rejection lost valid accounting: %v", err)
	}
}
