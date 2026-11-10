package delegation

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/semantic"
)

func TestIndependentSourceMeasurementRejectRetainsDurableWorkAndSkipsPair(t *testing.T) {
	for _, test := range []struct{ name, evidence, answer, original, instructions string }{
		{"average introduced", "The p95 latency limit is 240 ms.", "The average latency limit is 240 ms.", "What is the latency limit?", "Explain only the record."},
		{"request is not evidence", "The p95 latency limit is 240 ms.", "The average latency limit is 240 ms.", "What is the average latency limit?", "Report average latency."},
		{"percentile changed", "The p95 latency limit is 240 ms.", "The p99 latency limit is 240 ms.", "What is the latency limit?", "Explain only the record."},
		{"tail percentile changed", "The p99.9 latency limit is 240 ms.", "The p99.99 latency limit is 240 ms.", "What is the latency limit?", "Explain only the record."},
	} {
		t.Run(test.name, func(t *testing.T) {
			v, p := factorizedTestVerifier(t, nil)
			r := factorizedTestRequest()
			r.Context, r.CachedAnswer, r.OriginalQuestion, r.Instructions = test.evidence, test.answer, test.original, test.instructions
			r.Source.OutputSHA256 = reuseHash(r.CachedAnswer)
			r.Question = "Explain that recorded latency limit."
			out, err := v.Verify(t.Context(), r)
			if err != nil || out.Accepted() || out.AnswerSupported || out.Verdict != "reject" || !out.Complete || !out.Durable || len(out.Work) != 1 || p.sourceCalls != 1 || p.pairCalls != 0 || !out.GenerationCalled || out.InputTokens == nil || *out.InputTokens != 101 || out.OutputTokens == nil || *out.OutputTokens != 20 || semantic.ValidateVerifierWork(out, v.config) != nil {
				t.Fatalf("unsupported measurement bypassed guard or lost known work: %+v %v", out, err)
			}
			artifact, err := v.runner.Broker.Get(t.Context(), out.ReceiptKey)
			var receipt factorizedReceipt
			if err != nil || json.Unmarshal([]byte(artifact.Content), &receipt) != nil || receipt.Verdict != "reject" || receipt.Pair != nil || receipt.Support.Key != out.Work[0].ReceiptKey || receipt.Support.OutputSHA256 != out.Work[0].OutputSHA256 || reuseHash(artifact.Content) != out.OutputSHA256 {
				t.Fatalf("reject did not pin exactly its generated source proof: %+v %v", receipt, err)
			}
			repeat, err := v.Verify(t.Context(), r)
			if err != nil || repeat.Verdict != "reject" || !repeat.Reused || repeat.GenerationCalled || repeat.InputTokens == nil || *repeat.InputTokens != 0 || repeat.OutputTokens == nil || *repeat.OutputTokens != 0 || p.sourceCalls != 1 || p.pairCalls != 0 || repeat.ReceiptKey != out.ReceiptKey {
				t.Fatalf("same reject repeated inference or lost immutable proof: %+v %v", repeat, err)
			}
		})
	}
}

func TestIndependentSourceMeasurementPassNeverAuthorizesReuse(t *testing.T) {
	for _, sourceAccept := range []bool{true, false} {
		for _, pairAccept := range []bool{true, false} {
			v, p := factorizedTestVerifier(t, nil)
			r := factorizedTestRequest()
			r.Context, r.CachedAnswer = "Mean latency was 240 ms.", "Average latency was 240 ms."
			r.Source.OutputSHA256 = reuseHash(r.CachedAnswer)
			if !sourceAccept {
				p.support = `{"verdict":"reject","supported":false,"complete":false}`
			}
			if !pairAccept {
				p.pair = `{"verdict":"reject","answer_covers_current":false,"unchanged_facts":false,"same_output_contract":false}`
			}
			out, err := v.Verify(t.Context(), r)
			if err != nil || out.Accepted() != (sourceAccept && pairAccept) || p.sourceCalls != 1 || p.pairCalls != map[bool]int{true: 1, false: 0}[sourceAccept] {
				t.Fatalf("matching statistic overrode mandatory model checks: %+v %v", out, err)
			}
		}
	}
}

func TestIndependentSourceMeasurementFactsMayComeFromApprovedBindings(t *testing.T) {
	v, p := factorizedTestVerifier(t, nil)
	r := factorizedTestRequest()
	r.Context, r.CachedAnswer = "Recorded latency is 240 ms.", "Mean latency is 240 ms."
	r.Bindings["aggregation"] = "average"
	r.Source.OutputSHA256 = reuseHash(r.CachedAnswer)
	out, err := v.Verify(t.Context(), r)
	if err != nil || !out.Accepted() || p.sourceCalls != 1 || p.pairCalls != 1 {
		t.Fatalf("approved binding was not available to source screen: %+v %v", out, err)
	}
}

func TestIndependentSourceMeasurementDoesNotChangePublishedStandalone(t *testing.T) {
	f := independentContractSetup(t, "Average latency is 240 ms.")
	f.request.Spec.Input = "What is the recorded latency limit?"
	f.request.Spec.Context[0].Text = "The p95 latency limit is 240 ms."
	f.request.Checks[0].Text = "240"
	f.request.SemanticReuse.VerifierPolicy = ""
	c := *f.request.SemanticReuse
	p := &fixtureProvider{identity: "independent-standalone-measurement-fixture", response: knownResult(`{"verdict":"accept","same_intent":true,"same_facts":true,"same_constraints":true,"answer_supported":true}`).Response}
	f.router.Reuse.VerifierFactory = func(context.Context, semantic.ReuseConfig) (semantic.Verifier, error) {
		return &LocalVerifier{runner: &inference.Runner{Broker: f.broker, Provider: p}, config: c, identity: reuseHash("independent-standalone-measurement-policy")}, nil
	}
	f.seed(t)
	f.request.Spec.Input = "Explain that recorded latency limit."
	out, err := f.router.Run(t.Context(), f.request)
	if err != nil || out.SemanticReuse == nil || !out.SemanticReuse.GenerationAvoided || p.generations != 1 || f.target.generations != 1 {
		t.Fatalf("factorized-only guard changed the standalone policy: %+v %v", out.SemanticReuse, err)
	}
}

func TestIndependentSourceMeasurementGuardLeavesExactLookupFirst(t *testing.T) {
	f := independentContractSetup(t, "Average latency is 240 ms.")
	f.request.Spec.Input = "What is the recorded latency limit?"
	f.request.Spec.Context[0].Text = "The p95 latency limit is 240 ms."
	f.request.Checks[0].Text = "240"
	f.seed(t)
	original := f.request.Spec.Input
	f.request.Spec.Input = "Explain that recorded latency limit."
	paraphrase, err := f.router.Run(t.Context(), f.request)
	if err != nil || paraphrase.SemanticReuse == nil || paraphrase.SemanticReuse.GenerationAvoided || len(f.stages.requests) != 1 || f.target.generations != 2 {
		t.Fatalf("unsupported source not withheld from semantic reuse: %+v %v", paraphrase.SemanticReuse, err)
	}
	f.request.Spec.Input = original
	exact, err := f.router.Run(t.Context(), f.request)
	if err != nil || exact.SemanticReuse == nil || exact.SemanticReuse.Status != "skipped_exact" || f.target.generations != 2 || len(f.stages.requests) != 1 || !exact.Attempts[0].Result.Reused {
		t.Fatalf("new guard changed existing exact lookup: %+v %v", exact.SemanticReuse, err)
	}
}
