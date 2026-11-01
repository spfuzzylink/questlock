package delegation

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/semantic"
)

func independentContractSetup(t *testing.T, answer string) *independentFactorizedFixture {
	t.Helper()
	f := independentFactorizedSetup(t)
	f.target.response = knownResult(answer).Response
	f.request.Spec.Input = "What is the recorded value?"
	f.request.Spec.Instructions = "Answer from the approved record; do not perform an action."
	f.request.Spec.Context[0].Text = "The recorded value is 7."
	// Deliberately do not encode presentation into the output checks: these
	// fixtures isolate the guard from a permissive model verdict.
	f.request.Checks = []evaluation.Check{{ID: "value", Kind: "contains", Text: "7"}}
	return f
}

func TestIndependentFactorizedPresentationMismatchCannotBeOverridden(t *testing.T) {
	for _, question := range []string{
		"Return the result as a three-column CSV.",
		"Return only the number.",
		"Return the result as a JSON object.",
		"Return the result as a Markdown table with two columns.",
	} {
		t.Run(question, func(t *testing.T) {
			f := independentContractSetup(t, "The recorded value is 7.")
			f.seed(t)
			f.request.Spec.Input = question
			out, err := f.router.Run(t.Context(), f.request)
			if err != nil || out.SemanticReuse == nil || out.SemanticReuse.GenerationAvoided || len(out.SemanticReuse.Verifications) != 0 || len(f.stages.requests) != 0 || len(out.SemanticReuse.Rejections) == 0 || f.target.generations != 2 || out.Attempts[0].Result.Resolution != nil {
				t.Fatalf("permissive verifier bypassed a concrete format mismatch: observation=%+v error=%v", out.SemanticReuse, err)
			}
		})
	}
}

func TestIndependentFactorizedValidPresentationStillRequiresModelDecision(t *testing.T) {
	for _, test := range []struct{ name, question, answer string }{
		{"csv", "Return the result as CSV with two columns.", "value,7\nlabel,7"},
		{"tsv", "Return the result as TSV with two columns.", "value\t7"},
		{"json", "Return the result as a JSON object.", `{"value":7}`},
		{"numeric", "Return only the number.", "7"},
		{"table", "Return the result as a Markdown table with two columns.", "| value | amount |\n| --- | --- |\n| x | 7 |"},
		{"subject only", "What should I know about exporting CSV records?", "The recorded value is 7."},
	} {
		for _, accept := range []bool{true, false} {
			name := test.name + "/rejecting verifier"
			if accept {
				name = test.name + "/accepting verifier"
			}
			t.Run(name, func(t *testing.T) {
				f := independentContractSetup(t, test.answer)
				if !accept {
					f.stages.pair = `{"verdict":"reject","answer_covers_current":false,"unchanged_facts":false,"same_output_contract":false}`
				}
				f.seed(t)
				f.request.Spec.Input = test.question
				out, err := f.router.Run(t.Context(), f.request)
				if err != nil || out.SemanticReuse == nil || out.SemanticReuse.GenerationAvoided != accept || len(out.SemanticReuse.Verifications) != 1 || len(f.stages.requests) != 2 {
					t.Fatalf("format guard became positive authorization or rejected valid syntax: observation=%+v error=%v", out.SemanticReuse, err)
				}
			})
		}
	}
}

func TestIndependentDirectFactorizedFormatRejectIsDurableAndSkipsPair(t *testing.T) {
	const question = "Return the result as a JSON object."
	for _, answer := range []string{`{"value":7}`, `[7]`} {
		v, p := factorizedTestVerifier(t, nil)
		request := factorizedTestRequest()
		request.Question, request.CachedAnswer = question, answer
		request.Source.OutputSHA256 = reuseHash(answer)
		out, err := v.Verify(t.Context(), request)
		accepted := answer[0] == '{'
		if err != nil || out.Accepted() != accepted || !out.Complete || !out.Durable || out.InputTokens == nil || out.OutputTokens == nil || out.ReceiptKey == "" || p.sourceCalls != 1 || p.pairCalls != map[bool]int{true: 1, false: 0}[accepted] || semantic.ValidateVerifierWork(out, v.config) != nil {
			t.Fatalf("direct presentation check lost durable known work or called an unnecessary pair: %+v %v", out, err)
		}
		if !accepted && (out.Verdict != "reject" || len(out.Work) != 1 || *out.InputTokens != 101 || *out.OutputTokens != 20) {
			t.Fatal("deterministic rejection was represented as unknown or extra inference")
		}
	}
}

func TestIndependentOutputContractDoesNotChangePublishedStandalone(t *testing.T) {
	f := independentContractSetup(t, "The recorded value is 7.")
	f.request.SemanticReuse.VerifierPolicy = ""
	c := *f.request.SemanticReuse
	p := &fixtureProvider{identity: "independent-standalone-format-fixture", response: knownResult(`{"verdict":"accept","same_intent":true,"same_facts":true,"same_constraints":true,"answer_supported":true}`).Response}
	f.router.Reuse.VerifierFactory = func(context.Context, semantic.ReuseConfig) (semantic.Verifier, error) {
		return &LocalVerifier{runner: &inference.Runner{Broker: f.broker, Provider: p}, config: c, identity: reuseHash("independent-standalone-format-policy")}, nil
	}
	f.seed(t)
	f.request.Spec.Input = "Return only the number for that value."
	if len(semantic.CheckReuseProtectedTerms("What is the recorded value?", f.request.Spec.Input, "")) != 0 {
		t.Fatal("standalone fixture unexpectedly failed its preexisting lexical guard")
	}
	out, err := f.router.Run(t.Context(), f.request)
	if err != nil || out.SemanticReuse == nil || !out.SemanticReuse.GenerationAvoided || p.generations != 1 || f.target.generations != 1 {
		t.Fatalf("factorized-only guard changed the published standalone path: observation=%+v error=%v", out.SemanticReuse, err)
	}
}

func TestIndependentFormatGuardPreservesExactReuseAndSourceExpiry(t *testing.T) {
	f := independentContractSetup(t, "7")
	f.request.Spec.Input = "Return only the number."
	original := f.request.Spec.Input
	f.seed(t)
	f.request.Spec.Input = "Provide only the number."
	alias, err := f.router.Run(t.Context(), f.request)
	if err != nil || alias.SemanticReuse == nil || !alias.SemanticReuse.GenerationAvoided || len(f.stages.requests) != 2 {
		t.Fatalf("valid numeric alias setup failed: %+v %v", alias.SemanticReuse, err)
	}
	for _, question := range []string{original, f.request.Spec.Input} {
		f.request.Spec.Input = question
		exact, err := f.router.Run(t.Context(), f.request)
		if err != nil || exact.Response == nil || len(f.stages.requests) != 2 || f.target.generations != 1 || !slices.Contains([]string{"skipped_exact", "reused"}, exact.SemanticReuse.Status) {
			t.Fatalf("format check changed exact reuse: %+v %v", exact.SemanticReuse, err)
		}
	}
	f.now = f.now.Add(2 * time.Hour)
	f.request.Spec.Input = "Give only the number."
	expired, err := f.router.Run(t.Context(), f.request)
	if err != nil || expired.SemanticReuse == nil || expired.SemanticReuse.GenerationAvoided || len(f.stages.requests) != 2 || f.target.generations != 2 {
		t.Fatalf("format pass extended the original source window: %+v %v", expired.SemanticReuse, err)
	}
}
