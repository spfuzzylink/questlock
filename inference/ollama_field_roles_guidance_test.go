package inference_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
)

func TestFieldRoleGuidancePreservesCodesDecimalsAndOriginalSemanticChecks(t *testing.T) {
	const guidance = "The type contract specifies representation only. Derive values from the approved task and supplied input. Preserve copied strings exactly, including numeric-looking codes. Use the task's stated action or policy when a field requests one; do not substitute an unrelated computed quantity. Compute numeric quantities only where requested. Recheck every requested field against its source before returning the object."
	const definitions = "Field role definitions: quantity is a count or amount requested by the task; policy_action is the task's stated handling behavior or action rather than an amount; identifier is a source identifier copied exactly, including numeric-looking codes."
	for _, scenario := range []struct {
		name, instructions, input string
		types, roles              map[string]string
		correct                   string
		wrong                     []string
	}{
		{
			name:         "numeric policy code and decimal amount",
			instructions: "Copy the stated handling code into handling_code and the source reference into reference_code. Return total_amount as the sum of the two stated amounts.",
			input:        "Handling code: 009. Reference: 00042. Amounts: 1.25 and 1.5.",
			types:        map[string]string{"handling_code": "string", "reference_code": "string", "total_amount": "number"},
			roles:        map[string]string{"handling_code": "policy_action", "reference_code": "identifier", "total_amount": "quantity"},
			correct:      `{"handling_code":"009","reference_code":"00042","total_amount":2.7500}`,
			wrong: []string{
				`{"handling_code":"2.75","reference_code":"00042","total_amount":2.7500}`,
				`{"handling_code":"009","reference_code":"42","total_amount":2.7500}`,
				`{"handling_code":"009","reference_code":"00042","total_amount":2.5}`,
			},
		},
		{
			name:         "named policy and item count",
			instructions: "Return the manifest's stated processing action as next_step and its shipment identifier as shipment_ref. Compute carton_count by adding the two pallet counts.",
			input:        "Manifest action: route-to-desk. Shipment identifier: ZX-008. Pallet counts: 6 and 7.",
			types:        map[string]string{"next_step": "string", "shipment_ref": "string", "carton_count": "integer"},
			roles:        map[string]string{"next_step": "policy_action", "shipment_ref": "identifier", "carton_count": "quantity"},
			correct:      `{"next_step":"route-to-desk","shipment_ref":"ZX-008","carton_count":13}`,
			wrong:        []string{`{"next_step":"13","shipment_ref":"ZX-008","carton_count":13}`},
		},
	} {
		for _, stream := range []bool{false, true} {
			responses := append([]string{" \n" + scenario.correct + "\n"}, scenario.wrong...)
			for index, responseText := range responses {
				t.Run(scenario.name+"/stream="+strconv.FormatBool(stream)+"/response="+strconv.Itoa(index), func(t *testing.T) {
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
							if stream {
								w.Header().Set("Content-Type", "application/x-ndjson")
							}
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
					req := inference.Request{Provider: "ollama", Model: "fixture:local", Prefix: scenario.instructions, Input: scenario.input, Stream: stream, ContextWindow: 8192, MaxOutputTokens: 64, CacheMode: "default", Ollama: &inference.OllamaOptions{Format: "json", PromptMode: "bounded_text", OutputTypes: scenario.types, FieldRoles: scenario.roles}}
					checks := []evaluation.Check{{ID: "original-task-semantics", Kind: "json_equals", Expected: json.RawMessage(scenario.correct)}}
					beforeRequest, _ := json.Marshal(req)
					beforeChecks, _ := json.Marshal(checks)
					got, err := provider.Generate(context.Background(), req)
					if err != nil || !got.Complete || got.Text != responseText || got.Usage.InputTokens != 100 || got.Usage.OutputTokens != 20 || generations.Load() != 1 {
						t.Fatalf("field roles rewrote output, imposed semantic constraints or retried: %+v, %v", got, err)
					}
					body := <-recorded
					declaredTypes, _ := json.Marshal(scenario.types)
					declaredRoles, _ := json.Marshal(scenario.roles)
					wantSystem := "fixture installed system\n\nOutput field types (JSON object):\n" + string(declaredTypes) + "\n\n" + guidance + "\n\nField roles:\n" + string(declaredRoles) + "\n\n" + definitions
					if body.system != wantSystem || body.prompt != inference.BoundedTextPrompt(req) {
						t.Fatalf("roles changed source prompt or supplied task-specific values: system=%q prompt=%q", body.system, body.prompt)
					}
					_, passed := evaluation.CheckOutput(got.Text, checks)
					if passed != (index == 0) {
						t.Fatal("role guidance bypassed the original semantic check", got.Text)
					}
					afterRequest, _ := json.Marshal(req)
					afterChecks, _ := json.Marshal(checks)
					if string(beforeRequest) != string(afterRequest) || string(beforeChecks) != string(afterChecks) {
						t.Fatal("role guidance mutated the approved request or original checks")
					}
				})
			}
		}
	}
}
