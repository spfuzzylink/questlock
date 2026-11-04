package delegation

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/semantic"
)

func TestIndependentLanguageAndProseGuardsStopBeforePermissiveRouterVerifier(t *testing.T) {
	for _, test := range []struct{ name, answer, question, reason string }{
		{"changed language", "The recorded value is 7.", "Explain the recorded value. Reply in spanish.", "output_language_changed"},
		{"json cannot become prose", `{"value":7}`, "Explain the recorded value in plain prose.", "output_prose_contract_mismatch"},
		{"explicit no json", `{"value":7}`, "Explain the recorded value without JSON.", "output_no_json_contract_mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := independentContractSetup(t, test.answer)
			f.request.Spec.Instructions = "Use only the approved record. Answer in english."
			f.seed(t)
			f.request.Spec.Input = test.question
			out, err := f.router.Run(t.Context(), f.request)
			if err != nil || out.SemanticReuse == nil || out.SemanticReuse.GenerationAvoided || !slices.Contains(out.SemanticReuse.Rejections, test.reason) || len(out.SemanticReuse.Verifications) != 0 || len(f.stages.requests) != 0 || f.target.generations != 2 || out.Attempts[0].Result.Resolution != nil {
				t.Fatalf("a model could override the known contract mismatch: observation=%+v err=%v", out.SemanticReuse, err)
			}
		})
	}
}

func TestIndependentDirectLanguageAndProseRejectPinsOneSourceAndNoPair(t *testing.T) {
	for _, test := range []struct{ answer, question string }{
		{"The recorded value is 7.", "Explain the recorded value. Reply in spanish."},
		{`{"value":7}`, "Explain the recorded value in plain prose."},
	} {
		v, p := factorizedTestVerifier(t, nil)
		r := factorizedTestRequest()
		r.Context = "The recorded value is 7."
		r.Instructions = "Use only the approved record. Answer in english."
		r.OriginalQuestion = "What is the recorded value?"
		r.Question, r.CachedAnswer = test.question, test.answer
		r.Source.OutputSHA256 = reuseHash(r.CachedAnswer)
		out, err := v.Verify(t.Context(), r)
		if err != nil || out.Accepted() || !out.AnswerSupported || out.Verdict != "reject" || !out.Complete || !out.Durable || len(out.Work) != 1 || !out.Work[0].GenerationCalled || p.sourceCalls != 1 || p.pairCalls != 0 || out.InputTokens == nil || *out.InputTokens != 101 || out.OutputTokens == nil || *out.OutputTokens != 20 || semantic.ValidateVerifierWork(out, v.config) != nil {
			t.Fatalf("direct guard lost its bounded known source work: %+v %v", out, err)
		}
		artifact, err := v.runner.Broker.Get(t.Context(), out.ReceiptKey)
		var receipt factorizedReceipt
		if err != nil || json.Unmarshal([]byte(artifact.Content), &receipt) != nil || receipt.Pair != nil || receipt.Support.Key != out.Work[0].ReceiptKey || receipt.Verdict != "reject" || reuseHash(artifact.Content) != out.OutputSHA256 {
			t.Fatalf("reject did not durably pin exactly its source job: %+v %v", receipt, err)
		}
	}
}

func TestIndependentMatchingEnglishContractStillRequiresBothValidityStages(t *testing.T) {
	for _, rejectingStage := range []string{"none", "source", "pair"} {
		t.Run(rejectingStage, func(t *testing.T) {
			f := independentContractSetup(t, "The recorded value is 7.")
			f.request.Spec.Instructions = "Use only the approved record. English is the default output language."
			if rejectingStage == "source" {
				f.stages.support = `{"verdict":"reject","supported":false,"complete":false}`
			}
			if rejectingStage == "pair" {
				f.stages.pair = `{"verdict":"reject","answer_covers_current":false,"unchanged_facts":false,"same_output_contract":false}`
			}
			f.seed(t)
			f.request.Spec.Input = "Explain the recorded value. Reply in english."
			out, err := f.router.Run(t.Context(), f.request)
			calls := 2
			if rejectingStage == "source" {
				calls = 1
			}
			if err != nil || out.SemanticReuse == nil || out.SemanticReuse.GenerationAvoided != (rejectingStage == "none") || len(f.stages.requests) != calls || len(out.SemanticReuse.Verifications) != 1 {
				t.Fatalf("matching language became positive authorization or blocked a valid check: %+v %v", out.SemanticReuse, err)
			}
		})
	}
}

func TestIndependentLanguageGuardDoesNotChangePublishedStandalone(t *testing.T) {
	f := independentContractSetup(t, "The recorded value is 7.")
	f.request.Spec.Instructions = "Use only the approved record. Answer in english."
	f.request.SemanticReuse.VerifierPolicy = ""
	c := *f.request.SemanticReuse
	p := &fixtureProvider{identity: "independent-standalone-language-fixture", response: knownResult(`{"verdict":"accept","same_intent":true,"same_facts":true,"same_constraints":true,"answer_supported":true}`).Response}
	f.router.Reuse.VerifierFactory = func(context.Context, semantic.ReuseConfig) (semantic.Verifier, error) {
		return &LocalVerifier{runner: &inference.Runner{Broker: f.broker, Provider: p}, config: c, identity: reuseHash("independent-standalone-language-policy")}, nil
	}
	f.seed(t)
	f.request.Spec.Input = "Explain the recorded value. Reply in spanish."
	out, err := f.router.Run(t.Context(), f.request)
	if err != nil || out.SemanticReuse == nil || !out.SemanticReuse.GenerationAvoided || p.generations != 1 || f.target.generations != 1 {
		t.Fatalf("new factorized language policy changed the published standalone path: %+v %v", out.SemanticReuse, err)
	}
}
