package inference

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func outputTypesFixture() map[string]string {
	return map[string]string{"count": "integer", "label": "string", "ratio": "number", "ready": "boolean"}
}

func TestOllamaOutputTypesWireGroundingAndEstimate(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, mode := range []string{"", "bounded_text"} {
			t.Run(strconv.FormatBool(stream)+"/"+mode, func(t *testing.T) {
				text := " \n" + `{"ready":false,"ratio":1e-99999,"label":"fixture value","count":9007199254740993}` + "\n"
				fixture := mutatedFixture(t, ollamaGenerateFixture, func(frame map[string]any) { frame["response"] = text })
				recorded := make(chan map[string]any, 1)
				p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					recorded <- body
					if stream {
						w.Header().Set("Content-Type", "application/x-ndjson")
						writeStreamFrame(w, fixture)
					} else {
						io.WriteString(w, fixture)
					}
				}, nil)
				req := ollamaTestRequest()
				req.Stream, req.ContextWindow = stream, 8192
				req.Ollama = &OllamaOptions{Format: "json", PromptMode: mode, OutputTypes: outputTypesFixture()}
				before, _ := json.Marshal(req)
				count, err := p.CountInputTokens(context.Background(), req)
				if err != nil || calls.generate.Load() != 0 {
					t.Fatalf("output contract preflight failed or ran inference: %d, %v", count, err)
				}
				got, err := p.Generate(context.Background(), req)
				if err != nil || !got.Complete || got.Text != text || got.Usage.OutputTokens != 20 || calls.generate.Load() != 1 {
					t.Fatalf("valid typed text was changed, discarded or retried: %+v, %v", got, err)
				}
				wantSchema := map[string]any{"type": "object", "properties": map[string]any{"count": map[string]any{"type": "integer"}, "label": map[string]any{"type": "string"}, "ratio": map[string]any{"type": "number"}, "ready": map[string]any{"type": "boolean"}}, "required": []any{"count", "label", "ratio", "ready"}, "additionalProperties": false}
				declared, _ := json.Marshal(req.Ollama.OutputTypes)
				system := req.Prefix
				prompt := req.Input
				if mode == "bounded_text" {
					prompt = BoundedTextPrompt(req)
					system = "Model default instructions."
				}
				system += "\n\nOutput field types (JSON object):\n" + string(declared) + "\n\n" + ollamaOutputTypesGuidance
				want := map[string]any{"model": req.Model, "system": system, "prompt": prompt, "stream": stream, "think": false, "format": wantSchema, "options": map[string]any{"num_predict": float64(req.MaxOutputTokens), "num_ctx": float64(req.ContextWindow)}}
				body := <-recorded
				if !reflect.DeepEqual(body, want) {
					t.Fatalf("wire schema added answer constraints, lost defaults or changed user prompt: %+v", body)
				}
				schema, err := ollamaOutputTypesSchema(req.Ollama)
				if err != nil || count != int64(len(system)+len(prompt)+len(schema)+len("{{ .System }} {{ .Prompt }}"))+512 {
					t.Fatalf("actual grounding/schema/default system not counted once: %d, %v", count, err)
				}
				after, _ := json.Marshal(req)
				if string(before) != string(after) {
					t.Fatal("manual contract rendering mutated caller request")
				}
			})
		}
	}
}

func TestOllamaOutputTypesPreserveEffectiveSystem(t *testing.T) {
	for _, mode := range []string{"", "bounded_text"} {
		for _, explicit := range []string{"", "explicit system replaces installed default"} {
			req := ollamaTestRequest()
			req.Prefix = explicit
			req.Ollama = &OllamaOptions{Format: "json", PromptMode: mode, OutputTypes: map[string]string{"value": "string"}}
			rendered, err := renderOllamaRequest(req, "installed default system")
			if err != nil {
				t.Fatal(err)
			}
			want := explicit
			if explicit == "" || mode == "bounded_text" {
				want = "installed default system"
			}
			want += "\n\nOutput field types (JSON object):\n" + `{"value":"string"}` + "\n\n" + ollamaOutputTypesGuidance
			if rendered.system != want {
				t.Fatalf("type grounding changed effective system: %q, want %q", rendered.system, want)
			}
			if mode == "bounded_text" && rendered.prompt != BoundedTextPrompt(req) {
				t.Fatal("type grounding changed canonical user prompt")
			}
		}
	}
}

func TestOllamaOutputTypesNilPreservesLegacyBodyAndIdentity(t *testing.T) {
	s := ollamaOptionsSpec()
	plan, err := BuildPlan(s)
	if err != nil || plan.Fingerprint != "8432881ebf244e0bd4ae8e03eacaacdc946a9bbb30f278585ddcb2ed3fdccaf2" {
		t.Fatalf("nil contract changed legacy identity: %s, %v", plan.Fingerprint, err)
	}
	recorded := make(chan string, 1)
	p, _ := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		recorded <- string(body)
		io.WriteString(w, ollamaGenerateFixture)
	}, nil)
	if _, err := p.Generate(context.Background(), ollamaTestRequest()); err != nil {
		t.Fatal(err)
	}
	const legacy = `{"model":"qwen3:4b","system":"Approved concise memory.","prompt":"Draft a handoff.","stream":false,"think":false,"options":{"num_predict":64,"num_ctx":1024}}`
	if got := <-recorded; got != legacy {
		t.Fatalf("nil contract changed legacy bytes or emitted format:null: %s", got)
	}
	s.Ollama = &OllamaOptions{Format: "json", OutputTypes: map[string]string{"value": "string"}}
	a, err := BuildPlan(s)
	if err != nil {
		t.Fatal(err)
	}
	s.Ollama = &OllamaOptions{Format: "json", OutputTypes: map[string]string{"value": "integer"}}
	b, err := BuildPlan(s)
	if err != nil || a.Fingerprint == b.Fingerprint || a.Fingerprint == plan.Fingerprint {
		t.Fatal("declared types did not isolate request identity", err)
	}
	s.Ollama.OutputTypes = map[string]string{"second": "boolean", "first": "string"}
	a, err = BuildPlan(s)
	if err != nil {
		t.Fatal(err)
	}
	s.Ollama.OutputTypes = map[string]string{"first": "string", "second": "boolean"}
	b, err = BuildPlan(s)
	if err != nil || a.Fingerprint != b.Fingerprint {
		t.Fatal("map insertion order changed the declared contract identity", err)
	}
}

func TestOllamaOutputTypesAcceptBoundedPrimitiveFieldLimits(t *testing.T) {
	fields := map[string]string{}
	for i := range 32 {
		fields["field_"+strconv.Itoa(i)] = "string"
	}
	for _, types := range []map[string]string{fields, {strings.Repeat("a", 64): "integer"}} {
		schema, err := ollamaOutputTypesSchema(&OllamaOptions{Format: "json", OutputTypes: types})
		if err != nil || len(schema) > maxOllamaOutputTypesSchemaBytes {
			t.Fatalf("valid bounded primitive declaration rejected: %v", err)
		}
	}
}

func TestOllamaOutputTypesRejectInvalidDeclarationsBeforeHTTP(t *testing.T) {
	tooMany, tooLarge := map[string]string{}, map[string]string{}
	for i := range 33 {
		tooMany["field_"+strconv.Itoa(i)] = "integer"
		if i < 32 {
			name := "field_" + strconv.Itoa(i) + "_" + strings.Repeat("a", 64-len("field_"+strconv.Itoa(i)+"_"))
			tooLarge[name] = "integer"
		}
	}
	cases := []OllamaOptions{{Format: "json", OutputTypes: map[string]string{}}, {OutputTypes: outputTypesFixture()}, {Format: "json", OutputTypes: tooMany}, {Format: "json", OutputTypes: tooLarge}}
	for _, key := range []string{"", "Answer", "_answer", "1_answer", "answer_", "answer__value", "answer-value", "résultat", strings.Repeat("a", 65)} {
		cases = append(cases, OllamaOptions{Format: "json", OutputTypes: map[string]string{key: "string"}})
	}
	for _, typ := range []string{"object", "array", "null", "17", `enum:["answer"]`, `{"type":"string","const":"answer"}`, "string ", "boolean|true"} {
		cases = append(cases, OllamaOptions{Format: "json", OutputTypes: map[string]string{"answer": typ}})
	}
	p, calls := ollamaFixtureProvider(t, ollamaGenerateFixture)
	for _, options := range cases {
		s := ollamaOptionsSpec()
		s.Ollama = &options
		if _, err := BuildPlan(s); err == nil {
			t.Fatal("invalid manual type contract planned", options)
		}
		req := ollamaTestRequest()
		req.Ollama = &options
		if _, err := p.CountInputTokens(context.Background(), req); err == nil {
			t.Fatal("invalid manual type contract counted", options)
		}
		if _, err := p.Generate(context.Background(), req); err == nil {
			t.Fatal("invalid manual type contract generated", options)
		}
	}
	if calls.tags.Load() != 1 || calls.show.Load() != 1 || calls.generate.Load() != 0 {
		t.Fatal("invalid manual contract reached HTTP after constructor discovery")
	}
}

func TestOllamaOutputTypesWithholdInvalidTextButRetainUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, text := range []string{
			`{"count":1.5,"label":"ok","ratio":1,"ready":true}`,
			`{"count":1,"label":7,"ratio":1,"ready":true}`,
			`{"count":1,"label":"ok","ratio":"1","ready":true}`,
			`{"count":1,"label":"ok","ratio":1,"ready":"true"}`,
			`{"count":1,"label":"ok","ratio":1}`,
			`{"count":1,"label":"ok","ratio":1,"ready":true,"extra":false}`,
			`{"count":1,"count":2,"label":"ok","ratio":1,"ready":true}`,
			`{"count":1,"\u0063ount":2,"label":"ok","ratio":1,"ready":true}`,
			`{"Count":1,"label":"ok","ratio":1,"ready":true}`,
			`{"count":1,"label":null,"ratio":1,"ready":true}`,
			`{"count":1,"label":["ok"],"ratio":1,"ready":true}`,
			`{"count":1,"label":"\ud800","ratio":1,"ready":true}`,
			`[]`, `null`, `"text"`, `{}`, `{"count":1} {"count":2}`, "```json\n{}\n```",
		} {
			t.Run(strconv.FormatBool(stream)+"/"+text, func(t *testing.T) {
				fixture := mutatedFixture(t, ollamaGenerateFixture, func(frame map[string]any) { frame["response"] = text })
				p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
					if stream {
						w.Header().Set("Content-Type", "application/x-ndjson")
						writeStreamFrame(w, fixture)
					} else {
						io.WriteString(w, fixture)
					}
				}, nil)
				req := ollamaTestRequest()
				req.Stream, req.ContextWindow = stream, 8192
				req.Ollama = &OllamaOptions{Format: "json", OutputTypes: outputTypesFixture()}
				got, err := p.Generate(context.Background(), req)
				if err != nil || got.Complete || got.Text != "" || got.StopReason != "output_types_mismatch" || got.Usage.InputTokens != 100 || got.Usage.OutputTokens != 20 || calls.generate.Load() != 1 {
					t.Fatalf("bad typed output escaped, lost known usage or retried: %+v, %v", got, err)
				}
			})
		}
	}
}

func TestOllamaOutputTypesCountSchemaAgainstCaps(t *testing.T) {
	p, calls := ollamaFixtureProvider(t, ollamaGenerateFixture)
	req := ollamaTestRequest()
	req.ContextWindow = 8192
	req.Ollama = &OllamaOptions{Format: "json", OutputTypes: outputTypesFixture()}
	count, err := p.CountInputTokens(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	schema, _ := ollamaOutputTypesSchema(req.Ollama)
	req.ContextWindow = count - int64(len(schema)) + req.MaxOutputTokens
	if _, err := p.CountInputTokens(context.Background(), req); err == nil {
		t.Fatal("schema bytes bypassed context cap")
	}
	if _, err := p.Generate(context.Background(), req); err == nil || calls.generate.Load() != 0 {
		t.Fatal("schema overrun reached generation")
	}
	req.ContextWindow = 65536
	req.Input = strings.Repeat("x", maxProviderRequestBytes-len(req.Prefix)-1)
	beforeTags := calls.tags.Load()
	if _, err := p.CountInputTokens(context.Background(), req); err == nil || calls.tags.Load() != beforeTags {
		t.Fatal("grounding/schema overrun passed rendered byte check before metadata")
	}
	s := ollamaOptionsSpec()
	s.Input = strings.Repeat("x", (512<<10)-len(s.Instructions)-len("\n\nTask:\n")-1)
	if _, err := BuildPlan(s); err != nil {
		t.Fatal("raw byte fixture should fit the legacy plan", err)
	}
	s.Ollama = req.Ollama
	if _, err := BuildPlan(s); err == nil {
		t.Fatal("grounding/schema bytes bypassed planned prompt cap")
	}
}

func TestOllamaOutputTypesIntegerClassificationHasNoFloatOrBigNumberCoercion(t *testing.T) {
	for _, number := range []string{"0", "-0", "1", "-1", "1.0", "10e-1", "1000e-3", "9007199254740993", "1e1000000000", "-0e-999999999999999999999", "1e" + strings.Repeat("9", 1000)} {
		if !validOllamaOutputTypes(`{"v":`+number+`}`, map[string]string{"v": "integer"}) {
			t.Fatal("mathematically integral JSON number rejected", number)
		}
	}
	for _, number := range []string{"1.5", "0.001", "10e-2", "1e-1000000000", "9007199254740993.1", "1.000000000000000000000001", "1e-" + strings.Repeat("9", 1000)} {
		if validOllamaOutputTypes(`{"v":`+number+`}`, map[string]string{"v": "integer"}) {
			t.Fatal("fractional JSON number rounded or coerced to integer", number)
		}
	}
}

func TestRunnerOllamaOutputTypesStoresFailureWithoutBadTextOrRetry(t *testing.T) {
	const bad = `{"value":"private-bad-output-marker"}`
	fixture := mutatedFixture(t, ollamaGenerateFixture, func(frame map[string]any) { frame["response"] = bad })
	p, calls := ollamaFixtureProvider(t, fixture)
	r, _ := testRunner(t)
	r.Provider = p
	s := ollamaOptionsSpec()
	s.Ollama = &OllamaOptions{Format: "json", OutputTypes: map[string]string{"value": "integer"}}
	s.MaxInputTokens = 2048
	got, err := r.Run(context.Background(), s)
	if !errors.Is(err, ErrIncomplete) || got.Response.Text != "" || got.GenerationUsage.OutputTokens != 20 || !got.Durable || calls.generate.Load() != 1 {
		t.Fatalf("type failure lost usage or escaped as approved text: %+v, %v", got, err)
	}
	stored, err := r.Broker.Get(context.Background(), "inference/v1/jobs/"+got.Fingerprint)
	if err != nil || strings.Contains(stored.Content, "private-bad-output-marker") {
		t.Fatal("bad typed text entered durable record", err)
	}
	if _, err := r.Run(context.Background(), s); !errors.Is(err, ErrIncomplete) || calls.generate.Load() != 1 {
		t.Fatal("typed failure triggered automatic retry", err)
	}
}
