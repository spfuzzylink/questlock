package inference_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
)

func TestOutputTypeGuidancePreservesCopiedCodesAndIndependentSemanticChecks(t *testing.T) {
	const guidance = "The type contract specifies representation only. Derive values from the approved task and supplied input. Preserve copied strings exactly, including numeric-looking codes. Use the task's stated action or policy when a field requests one; do not substitute an unrelated computed quantity. Compute numeric quantities only where requested. Recheck every requested field against its source before returning the object."
	for _, scenario := range []struct {
		name, instructions, input string
		types                     map[string]string
		correct, wrong            string
	}{
		{"copied numeric code", "Copy the batch code exactly into batch_code as a string. Return package_count as the stated number of boxes.", "Batch code 007 contains four boxes.", map[string]string{"batch_code": "string", "package_count": "integer"}, `{"batch_code":"007","package_count":4}`, `{"batch_code":"7","package_count":4}`},
		{"policy and quantity", "Use the input's stated handling policy as disposition. Compute slot_total as the sum of the listed aisle counts.", "Handling policy: hold. Aisle counts: 2 and 3.", map[string]string{"disposition": "string", "slot_total": "integer"}, `{"disposition":"hold","slot_total":5}`, `{"disposition":"5","slot_total":5}`},
	} {
		for _, correct := range []bool{true, false} {
			name := scenario.name + "/wrong semantics"
			responseText := scenario.wrong
			if correct {
				name, responseText = scenario.name+"/correct", " \n"+scenario.correct+"\n"
			}
			t.Run(name, func(t *testing.T) {
				var generations atomic.Int32
				recorded := make(chan struct{ system, prompt string }, 1)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/tags":
						io.WriteString(w, `{"models":[{"name":"fixture:local","digest":"`+strings.Repeat("a", 64)+`"}]}`)
					case "/api/show":
						io.WriteString(w, `{"template":"fixture template","system":"fixture installed system","capabilities":["completion"],"model_info":{"general.architecture":"fixture","fixture.context_length":8192}}`)
					case "/api/generate":
						generations.Add(1)
						var body struct {
							System string `json:"system"`
							Prompt string `json:"prompt"`
						}
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						recorded <- struct{ system, prompt string }{body.System, body.Prompt}
						json.NewEncoder(w).Encode(map[string]any{"model": "fixture:local", "response": responseText, "done": true, "done_reason": "stop", "prompt_eval_count": 100, "eval_count": 20})
					default:
						t.Error("unexpected fixture endpoint", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer srv.Close()
				provider, err := inference.NewOllamaProvider(srv.URL, "fixture:local")
				if err != nil {
					t.Fatal(err)
				}
				req := inference.Request{Provider: "ollama", Model: "fixture:local", Prefix: scenario.instructions, Input: scenario.input, ContextWindow: 8192, MaxOutputTokens: 64, CacheMode: "default", Ollama: &inference.OllamaOptions{Format: "json", PromptMode: "bounded_text", OutputTypes: scenario.types}}
				checks := []evaluation.Check{{ID: "original-task-semantics", Kind: "json_equals", Expected: json.RawMessage(scenario.correct)}}
				beforeRequest, _ := json.Marshal(req)
				beforeChecks, _ := json.Marshal(checks)
				got, err := provider.Generate(context.Background(), req)
				if err != nil || !got.Complete || got.Text != responseText || generations.Load() != 1 {
					t.Fatalf("representation contract rewrote output, imposed semantics or retried: %+v, %v", got, err)
				}
				body := <-recorded
				declared, _ := json.Marshal(scenario.types)
				wantSystem := "fixture installed system\n\nOutput field types (JSON object):\n" + string(declared) + "\n\n" + guidance
				if body.system != wantSystem || body.prompt != inference.BoundedTextPrompt(req) {
					t.Fatalf("generic guidance changed source request or included task-specific hints: system=%q prompt=%q", body.system, body.prompt)
				}
				_, passed := evaluation.CheckOutput(got.Text, checks)
				if passed != correct {
					t.Fatal("representation guidance bypassed the original semantic check", got.Text)
				}
				afterRequest, _ := json.Marshal(req)
				afterChecks, _ := json.Marshal(checks)
				if string(beforeRequest) != string(afterRequest) || string(beforeChecks) != string(afterChecks) {
					t.Fatal("guidance mutated the approved source or original checks")
				}
			})
		}
	}
}
