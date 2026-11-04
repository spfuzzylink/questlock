package delegation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
)

// Scripted decisions test transport, accounting and serving gates. They do not
// measure a model's ability to recognize answer applicability or output changes.
type independentApplicabilityProvider struct {
	decision string
	requests []inference.Request
	sources  int
	pairs    int
}

func (*independentApplicabilityProvider) Identity() string {
	return "independent-answer-applicability-fixture-v1"
}

func (*independentApplicabilityProvider) CountInputTokens(context.Context, inference.Request) (int64, error) {
	return 89, nil
}

func (p *independentApplicabilityProvider) Generate(_ context.Context, request inference.Request) (inference.Response, error) {
	p.requests = append(p.requests, request)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimPrefix(request.Input, "\n\nTask:\n")), &fields); err != nil {
		return inference.Response{}, err
	}
	text := `{"verdict":"accept","supported":true,"complete":true}`
	if fields["question"] == nil {
		p.sources++
	} else {
		p.pairs++
		text = p.decision
	}
	return inference.Response{Text: text, Complete: true, Model: "offline", Usage: inference.Usage{InputTokens: 89, UncachedInputTokens: 89, OutputTokens: 19}}, nil
}

func independentApplicabilitySetup(t *testing.T, answer, decision string) (*independentFactorizedFixture, *independentApplicabilityProvider) {
	t.Helper()
	f := independentFactorizedSetup(t)
	p := &independentApplicabilityProvider{decision: decision}
	f.verifier.runner.Provider = p
	f.target.response = knownResult(answer).Response
	f.request.Spec.Instructions = "Return the requested evidence as JSON; do not perform an action."
	f.request.Checks = []evaluation.Check{{ID: "json", Kind: "json_valid"}}
	f.request.SemanticReuse.Bindings = map[string]string{"channel": "preview"}
	f.verifier.config.Bindings = f.request.SemanticReuse.Bindings
	return f, p
}

func TestIndependentApplicabilityReceivesExactAnswerAndPreservesExactFirst(t *testing.T) {
	const answer = `{"owner":"Mira","deadline":"Friday","checks":{"container":"remaining"}}`
	const accepted = `{"verdict":"accept","answer_covers_current":true,"unchanged_facts":true,"same_output_contract":true}`
	f, p := independentApplicabilitySetup(t, answer, accepted)
	f.request.Spec.Context[0].Text = "For the preview, Mira owns the review, its deadline is Friday, and container checks remain."
	f.request.Spec.Input = "Report the preview review owner, deadline and remaining checks as JSON."
	originalQuestion := f.request.Spec.Input
	f.seed(t)
	questions := []string{
		"Provide JSON describing who owns the preview review, when it is due, and which checks remain.",
		"For the preview review, return its responsible person, deadline, and unfinished checks in JSON.",
	}
	var sourceReceipt string
	for i, question := range questions {
		f.request.Spec.Input = question
		out, err := f.router.Run(t.Context(), f.request)
		if err != nil || out.SemanticReuse == nil || !out.SemanticReuse.GenerationAvoided || len(out.SemanticReuse.Verifications) != 1 || out.Response == nil || out.Response.Text != answer || f.target.generations != 1 || p.sources != 1 || p.pairs != i+1 {
			t.Fatalf("applicable answer did not reuse its pinned source: observation=%+v source=%d pair=%d target=%d error=%v", out.SemanticReuse, p.sources, p.pairs, f.target.generations, err)
		}
		work := out.SemanticReuse.Verifications[0].Work
		if len(work) != 2 || work[0].Reused != (i > 0) || work[1].Reused {
			t.Fatalf("source certificate and new answer check were not accounted independently: %+v", work)
		}
		if i == 0 {
			sourceReceipt = work[0].ReceiptKey
			var source struct {
				Answer   string `json:"cached_answer"`
				Original string `json:"original_question"`
				Current  string `json:"question"`
			}
			if json.Unmarshal([]byte(strings.TrimPrefix(p.requests[0].Input, "\n\nTask:\n")), &source) != nil || source.Answer != answer || source.Original != originalQuestion || source.Current != "" {
				t.Fatal("reusable source certificate lost its original answer/request or included the new question")
			}
		} else if sourceReceipt != work[0].ReceiptKey {
			t.Fatal("new wording changed the unchanged source certificate")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(strings.TrimPrefix(p.requests[len(p.requests)-1].Input, "\n\nTask:\n")), &fields); err != nil {
			t.Fatal(err)
		}
		for key, expected := range map[string]string{"cached_answer": answer, "original_question": originalQuestion, "question": question, "instructions": f.request.Spec.Instructions} {
			var actual string
			if json.Unmarshal(fields[key], &actual) != nil || actual != expected {
				t.Fatalf("applicability lost exact %s: %s", key, fields[key])
			}
		}
		var background string
		var bindings map[string]string
		if json.Unmarshal(fields["context"], &background) != nil || !strings.Contains(background, f.request.Spec.Context[0].Text) || json.Unmarshal(fields["bindings"], &bindings) != nil || bindings["channel"] != "preview" {
			t.Fatal("answer applicability lost approved background or bindings")
		}
	}
	for _, request := range p.requests {
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(strings.TrimPrefix(request.Input, "\n\nTask:\n")), &fields) != nil || fields["intent"] != nil || strings.Contains(request.Input, f.request.SemanticReuse.Intent) {
			t.Fatal("source intent metadata reached a model stage")
		}
	}
	beforeCalls := len(p.requests)
	exact, err := f.router.Run(t.Context(), f.request)
	if err != nil || exact.Response == nil || exact.Response.Text != answer || len(exact.Attempts) != 1 || !exact.Attempts[0].Result.Reused || len(p.requests) != beforeCalls || f.target.generations != 1 {
		t.Fatalf("exact alias repeat performed verification or generation: observation=%+v error=%v", exact.SemanticReuse, err)
	}
	f.request.Spec.Input = originalQuestion
	original, err := f.router.Run(t.Context(), f.request)
	if err != nil || original.SemanticReuse == nil || original.SemanticReuse.Status != "skipped_exact" || len(p.requests) != beforeCalls || f.target.generations != 1 {
		t.Fatalf("exact original no longer bypasses answer verification: observation=%+v error=%v", original.SemanticReuse, err)
	}
}

func TestIndependentApplicabilityOmitsSourceIntentButBindsFullRequestReceipt(t *testing.T) {
	// Labels intentionally suggest different actors. This checks only metadata
	// transport and receipt identity, not whether a model understands either role.
	intents := []string{"source-employee-answer-hint", "source-manager-answer-hint"}
	const accepted = `{"verdict":"accept","answer_covers_current":true,"unchanged_facts":true,"same_output_contract":true}`
	var previousPayloads []string
	var previousStageReceipts []string
	var previousReceipt string
	for _, intent := range intents {
		v, _ := factorizedTestVerifier(t, nil)
		p := &independentApplicabilityProvider{decision: accepted}
		v.runner.Provider = p
		v.config.Intent = intent
		request := factorizedTestRequest()
		request.Intent = intent
		out, err := v.Verify(t.Context(), request)
		if err != nil || !out.Accepted() || len(p.requests) != 2 || len(out.Work) != 2 {
			t.Fatalf("scripted metadata isolation setup failed: %+v %v", out, err)
		}
		payloads := make([]string, len(p.requests))
		stageReceipts := make([]string, len(out.Work))
		for i, stage := range p.requests {
			payloads[i] = stage.Prefix + "\n" + stage.Input
			stageReceipts[i] = out.Work[i].ReceiptKey
			var fields map[string]json.RawMessage
			if json.Unmarshal([]byte(strings.TrimPrefix(stage.Input, "\n\nTask:\n")), &fields) != nil || fields["intent"] != nil {
				t.Fatal("source metadata intent field reached a model stage")
			}
			for _, label := range intents {
				if strings.Contains(stage.Input, label) || strings.Contains(stage.Prefix, label) {
					t.Fatal("actor-bearing source label leaked into verifier task data or instructions")
				}
			}
			if previousPayloads != nil && payloads[i] != previousPayloads[i] {
				t.Fatal("changing only source intent metadata changed a model payload")
			}
			if previousStageReceipts != nil && stageReceipts[i] == previousStageReceipts[i] {
				t.Fatal("source metadata intent no longer binds the stage certificate identity")
			}
		}
		artifact, err := v.runner.Broker.Get(t.Context(), out.ReceiptKey)
		var receipt factorizedReceipt
		encodedRequest, _ := json.Marshal(request)
		if err != nil || json.Unmarshal([]byte(artifact.Content), &receipt) != nil || receipt.RequestSHA256 != reuseHash(string(encodedRequest)) || previousReceipt != "" && out.ReceiptKey == previousReceipt {
			t.Fatal("omitting source metadata from model input lost its full-request receipt binding")
		}
		previousPayloads, previousStageReceipts, previousReceipt = payloads, stageReceipts, out.ReceiptKey
	}
}

func TestIndependentApplicabilityHonorsEveryDecisionBeforeServing(t *testing.T) {
	for _, test := range []struct {
		name, decision string
	}{
		{"missing detail", `{"verdict":"accept","answer_covers_current":false,"unchanged_facts":true,"same_output_contract":true}`},
		{"changed facts", `{"verdict":"accept","answer_covers_current":true,"unchanged_facts":false,"same_output_contract":true}`},
		{"changed contract", `{"verdict":"accept","answer_covers_current":true,"unchanged_facts":true,"same_output_contract":false}`},
		{"unknown", `{"verdict":"unknown","answer_covers_current":true,"unchanged_facts":true,"same_output_contract":true}`},
		{"legacy equivalence", `{"verdict":"accept","original_covers_current":true,"current_covers_original":true,"same_output_contract":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, p := independentApplicabilitySetup(t, `{"status":"pending"}`, test.decision)
			f.request.Spec.Context[0].Text = "The preview status is pending."
			f.request.Spec.Input = "Report preview readiness as JSON."
			f.seed(t)
			f.request.Spec.Input = "Return preview readiness using the JSON key status."
			out, err := f.router.Run(t.Context(), f.request)
			if err != nil || out.SemanticReuse == nil || out.SemanticReuse.GenerationAvoided || len(out.SemanticReuse.Verifications) != 1 || out.SemanticReuse.Verifications[0].Accepted() || p.sources != 1 || p.pairs != 1 || f.target.generations != 2 {
				t.Fatalf("nonaccepting applicability authorized serving: observation=%+v error=%v", out.SemanticReuse, err)
			}
		})
	}
}

func TestIndependentApplicabilityCanDistinguishSavedAnswersWithSameJSONShape(t *testing.T) {
	for _, test := range []struct {
		name, answer, decision string
		serve                  bool
	}{
		{"matching key", `{"status":"pending"}`, `{"verdict":"accept","answer_covers_current":true,"unchanged_facts":true,"same_output_contract":true}`, true},
		{"different key", `{"state":"pending"}`, `{"verdict":"reject","answer_covers_current":true,"unchanged_facts":true,"same_output_contract":false}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, p := independentApplicabilitySetup(t, test.answer, test.decision)
			f.request.Spec.Context[0].Text = "The preview status is pending."
			f.request.Spec.Input = "Report preview readiness as JSON."
			f.seed(t)
			f.target.response = knownResult(`{"status":"pending"}`).Response
			f.request.Spec.Input = "Return preview readiness using the JSON key status."
			out, err := f.router.Run(t.Context(), f.request)
			wantGenerations := 2
			if test.serve {
				wantGenerations = 1
			}
			if err != nil || out.SemanticReuse == nil || out.SemanticReuse.GenerationAvoided != test.serve || p.sources != 1 || p.pairs != 1 || f.target.generations != wantGenerations {
				t.Fatalf("scripted output-contract decision was not honored: observation=%+v error=%v", out.SemanticReuse, err)
			}
			var data struct {
				Answer string `json:"cached_answer"`
			}
			if json.Unmarshal([]byte(strings.TrimPrefix(p.requests[1].Input, "\n\nTask:\n")), &data) != nil || data.Answer != test.answer {
				t.Fatal("same JSON category erased the saved key before the applicability check")
			}
		})
	}
}
