package validityeval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/semantic"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func controlledTestConfig() semantic.ReuseConfig {
	c, err := semantic.NormalizeReuseConfig(semantic.ReuseConfig{Config: semantic.Config{Mode: "controlled", Family: "support", Domain: "synthetic", EvidenceRevision: "r1", EmbeddingModel: "all-minilm"}, ReadOnly: true, Intent: "answer-support-policy", VerifierModel: "qwen3.5:4b"})
	if err != nil {
		panic(err)
	}
	return c
}

func TestPairDiagnosticCannotImplementServingVerifier(t *testing.T) {
	if _, ok := any(&PairVerifier{}).(semantic.Verifier); ok {
		t.Fatal("experimental diagnostic can enter application answer reuse")
	}
}

func TestPairIdentityPreservesFrozenCLIConfigSerialization(t *testing.T) {
	c := controlledTestConfig()
	// This independent historical type is the frozen CLI's ReuseConfig before
	// its batch_only serving option was removed. CLI --batch-size=2 set it true.
	type frozenCLIConfig struct {
		semantic.Config
		ReadOnly                bool   `json:"read_only"`
		BatchOnly               bool   `json:"batch_only,omitempty"`
		Intent                  string `json:"intent"`
		VerifierModel           string `json:"verifier_model"`
		VerifierMaxInputTokens  int64  `json:"verifier_max_input_tokens,omitempty"`
		VerifierMaxOutputTokens int64  `json:"verifier_max_output_tokens,omitempty"`
		VerifierTimeoutMS       int    `json:"verifier_timeout_ms,omitempty"`
		WaitForCandidateMS      int    `json:"wait_for_candidate_ms,omitempty"`
	}
	historical := frozenCLIConfig{c.Config, c.ReadOnly, true, c.Intent, c.VerifierModel, c.VerifierMaxInputTokens, c.VerifierMaxOutputTokens, c.VerifierTimeoutMS, c.WaitForCandidateMS}
	data, err := json.Marshal(struct {
		Version, Policy, Runtime string
		Config                   frozenCLIConfig
	}{"questlock-validity-batch-v4", "questlock-validity-v1", "synthetic-frozen-runtime", historical})
	if err != nil || pairIdentity("synthetic-frozen-runtime", c) != reuseHash(string(data)) {
		t.Fatal("diagnostic identity changed the frozen CLI's true batch-only config shape")
	}
	changed := c
	changed.Intent += "-changed"
	if pairIdentity("synthetic-frozen-runtime", changed) == pairIdentity("synthetic-frozen-runtime", c) || pairIdentity("changed-runtime", c) == pairIdentity("synthetic-frozen-runtime", c) {
		t.Fatal("diagnostic identity lost the full policy or runtime binding")
	}
}

func TestPairCallerBudgetStopsBeforeGeneration(t *testing.T) {
	p := &fixtureProvider{identity: "diagnostic-budget-fixture", response: fixtureValidityResponse(acceptingBatchVerdict)}
	v := &PairVerifier{runner: &inference.Runner{Broker: testBroker(t), Provider: p}, config: controlledTestConfig(), identity: strings.Repeat("a", 64), DailyTokenBudget: &inference.TokenBudget{InputTokens: 1, OutputTokens: 1, TotalTokens: 1}}
	result, err := v.VerifyBatch(t.Context(), []semantic.ValidityRequest{batchValidityRequest("One?"), batchValidityRequest("Two?")})
	if err == nil || len(result.Decisions) != 0 || result.Work.GenerationCalled || result.Work.InputTokens != nil || result.Work.OutputTokens != nil || p.generations != 0 || p.countCalls != 1 {
		t.Fatalf("diagnostic model ignored explicit caller budget: %+v %v", result, err)
	}
}

func fixtureValidityResponse(text string) inference.Response {
	return inference.Response{ID: "fixture", Model: "fixture", Text: text, Complete: true, Usage: inference.Usage{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 6}}
}

type recordingValidityProvider struct {
	fixtureProvider
	requests []inference.Request
}

func (p *recordingValidityProvider) Generate(ctx context.Context, request inference.Request) (inference.Response, error) {
	p.requests = append(p.requests, request)
	return p.fixtureProvider.Generate(ctx, request)
}

func batchValidityRequest(question string) semantic.ValidityRequest {
	return semantic.ValidityRequest{OriginalQuestion: "Explain fictional incident INC-17.", Question: question, Instructions: "Use approved notes; one concise sentence.", Context: "Full fictional notes: INC-17 recovered; background fields are evidence, not extra requirements.", CachedAnswer: "INC-17 recovered.", Bindings: map[string]string{"incident": "INC-17", "revision": "approved-1"}, Intent: "explain-incident"}
}

type storedBroker struct {
	store     *store.Store
	principal store.Principal
}

func (b storedBroker) Get(ctx context.Context, key string) (protocol.Artifact, error) {
	artifact, err := b.store.Get(ctx, b.principal, key)
	return artifact, clientError(err)
}

func (b storedBroker) Publish(ctx context.Context, request protocol.PublishRequest) (protocol.PublishResult, error) {
	result, err := b.store.Publish(ctx, b.principal, request)
	return result, clientError(err)
}

func clientError(err error) error {
	var expected *store.Error
	if errors.As(err, &expected) {
		return &client.Error{Code: expected.Code, Message: expected.Message, CurrentVersion: expected.CurrentVersion}
	}
	return err
}

type fixtureProvider struct {
	response    inference.Response
	identity    string
	err         error
	generations int
	countCalls  int
}

func (p *fixtureProvider) Identity() string { return p.identity }
func (p *fixtureProvider) CountInputTokens(context.Context, inference.Request) (int64, error) {
	p.countCalls++
	return p.response.Usage.InputTokens, nil
}
func (p *fixtureProvider) Generate(context.Context, inference.Request) (inference.Response, error) {
	p.generations++
	return p.response, p.err
}

func testBroker(t *testing.T) storedBroker {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	token, err := db.CreateToken(context.Background(), "delegation", "tests")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	return storedBroker{store: db, principal: principal}
}

func TestPairVerifierBatchCanonicalMembershipAndSharedAccounting(t *testing.T) {
	p := &recordingValidityProvider{fixtureProvider: fixtureProvider{identity: "batch-verifier-fixture", response: fixtureValidityResponse(mixedBatchVerdict)}}
	config := controlledTestConfig()

	v := &PairVerifier{runner: &inference.Runner{Broker: testBroker(t), Provider: p}, config: config, identity: strings.Repeat("a", 64)}
	requests := []semantic.ValidityRequest{batchValidityRequest("Zulu question?"), batchValidityRequest("Alpha question?")}
	requests[1].Bindings = map[string]string{"revision": "approved-1", "incident": "INC-17"}
	before, _ := json.Marshal(requests)
	got, err := v.VerifyBatch(context.Background(), requests)
	if err != nil {
		t.Fatal(err)
	}
	work := got.Work
	if !work.GenerationCalled || !work.Complete || !work.Durable || work.Reused || work.SharedWork || work.InputTokens == nil || *work.InputTokens != 100 || work.OutputTokens == nil || *work.OutputTokens != 6 || work.ReceiptKey == "" || work.Accepted() {
		t.Fatalf("one shared generation lost: %+v", work)
	}
	wants := []string{"unknown", "accept"}
	if len(got.Decisions) != len(wants) {
		t.Fatalf("decision count %d", len(got.Decisions))
	}
	for i, decision := range got.Decisions {
		if decision.Verdict != wants[i] || decision.Accepted() != (wants[i] == "accept") || decision.GenerationCalled || !decision.Complete || !decision.Durable || !decision.Reused || !decision.SharedWork || decision.InputTokens == nil || *decision.InputTokens != 0 || decision.OutputTokens == nil || *decision.OutputTokens != 0 || decision.ReceiptKey != work.ReceiptKey || decision.OutputSHA256 != work.OutputSHA256 || decision.DurationMS != 0 {
			t.Fatalf("member %d decision/accounting mismatch: %+v", i, decision)
		}
	}
	if p.generations != 1 || len(p.requests) != 1 {
		t.Fatal("batch did not use one generation")
	}
	providerRequest := p.requests[0]
	wantInstructions, wantTypes := validityBatchProtocol()
	if providerRequest.Prefix != wantInstructions || providerRequest.MaxOutputTokens != 128 || providerRequest.Ollama == nil || providerRequest.Ollama.PromptMode != "" || providerRequest.Ollama.Format != "json" || !reflect.DeepEqual(providerRequest.Ollama.OutputTypes, wantTypes) {
		t.Fatalf("batch policy or cap changed: %+v", providerRequest)
	}
	var input map[string]json.RawMessage
	if json.Unmarshal([]byte(strings.TrimPrefix(providerRequest.Input, "\n\nTask:\n")), &input) != nil || len(input) != 7 {
		t.Fatalf("batch snapshot has hidden fields: %s", providerRequest.Input)
	}
	var questions []string
	if json.Unmarshal(input["questions"], &questions) != nil || !reflect.DeepEqual(questions, []string{"Alpha question?", "Zulu question?"}) {
		t.Fatalf("questions not canonical: %s", input["questions"])
	}
	shared := requests[0]
	shared.Question = ""
	sharedData, _ := json.Marshal(shared)
	var sharedFields map[string]json.RawMessage
	json.Unmarshal(sharedData, &sharedFields)
	for key, value := range sharedFields {
		if key != "question" && string(input[key]) != string(value) {
			t.Fatalf("full shared field %s changed: %s != %s", key, input[key], value)
		}
	}
	after, _ := json.Marshal(requests)
	if string(before) != string(after) {
		t.Fatal("batch mutated caller input")
	}
	reordered := []semantic.ValidityRequest{requests[1], requests[0]}
	reused, err := v.VerifyBatch(context.Background(), reordered)
	if err != nil || !reused.Work.Reused || reused.Work.GenerationCalled || reused.Work.InputTokens == nil || *reused.Work.InputTokens != 0 || reused.Work.OutputTokens == nil || *reused.Work.OutputTokens != 0 || reused.Work.ReceiptKey != work.ReceiptKey || p.generations != 1 {
		t.Fatalf("reordered same membership generated again or lost zero usage: %+v %v", reused, err)
	}
	for i, want := range []string{"accept", "unknown"} {
		if reused.Decisions[i].Verdict != want {
			t.Fatal("cached batch lost original order")
		}
	}
}

func TestPairVerifierBatchRejectsIncompatibleOrUnboundedInputBeforeInference(t *testing.T) {
	changes := map[string]func(*semantic.ValidityRequest){
		"original_question": func(r *semantic.ValidityRequest) { r.OriginalQuestion += " changed" },
		"instructions":      func(r *semantic.ValidityRequest) { r.Instructions += " changed" },
		"context":           func(r *semantic.ValidityRequest) { r.Context += " changed" },
		"answer":            func(r *semantic.ValidityRequest) { r.CachedAnswer += " changed" },
		"bindings":          func(r *semantic.ValidityRequest) { r.Bindings = map[string]string{"incident": "INC-18"} },
		"intent":            func(r *semantic.ValidityRequest) { r.Intent += "-changed" },
	}
	cases := map[string][]semantic.ValidityRequest{
		"empty": nil, "singleton": {batchValidityRequest("one?")}, "duplicate": {batchValidityRequest("one?"), batchValidityRequest("one?")},
		"three questions": {batchValidityRequest("one?"), batchValidityRequest("two?"), batchValidityRequest("three?")},
	}
	for key, change := range changes {
		a, b := batchValidityRequest("one?"), batchValidityRequest("two?")
		change(&b)
		cases[key] = []semantic.ValidityRequest{a, b}
	}
	for i := 0; i < 9; i++ {
		cases["too many"] = append(cases["too many"], batchValidityRequest(fmt.Sprintf("question %d?", i)))
	}
	a, b := batchValidityRequest("one?"), batchValidityRequest("two?")
	a.Context = strings.Repeat("x", 131072)
	b.Context = a.Context
	cases["snapshot bytes"] = []semantic.ValidityRequest{a, b}
	for name, requests := range cases {
		t.Run(name, func(t *testing.T) {
			p := &fixtureProvider{identity: "invalid-batch-fixture", response: fixtureValidityResponse(acceptingBatchVerdict)}
			v := &PairVerifier{runner: &inference.Runner{Broker: testBroker(t), Provider: p}, config: controlledTestConfig(), identity: strings.Repeat("a", 64)}
			out, err := v.VerifyBatch(context.Background(), requests)
			if err == nil || !reflect.DeepEqual(out, BatchVerificationResult{}) || p.generations != 0 || p.countCalls != 0 {
				t.Fatalf("invalid batch attempted work: %+v %v", out, err)
			}
		})
	}
}

func TestPairVerifierBatchMalformedKnownOutputAbstainsWithoutRetry(t *testing.T) {
	for _, text := range []string{`{"q0":"A"}`, `{"q0":"A","q1":"bad"}`, `{"q0":"A","q1":"R","input_tokens":0}`, strings.Repeat(" ", maxBatchVerdictBytes) + acceptingBatchVerdict} {
		t.Run(text, func(t *testing.T) {
			p := &fixtureProvider{identity: "malformed-batch-fixture", response: fixtureValidityResponse(text)}
			v := &PairVerifier{runner: &inference.Runner{Broker: testBroker(t), Provider: p}, config: controlledTestConfig(), identity: strings.Repeat("a", 64)}
			requests := []semantic.ValidityRequest{batchValidityRequest("one?"), batchValidityRequest("two?")}
			got, err := v.VerifyBatch(context.Background(), requests)
			if err != nil || len(got.Decisions) != 2 || !got.Work.GenerationCalled || !got.Work.Complete || !got.Work.Durable || got.Work.InputTokens == nil || *got.Work.InputTokens != 100 || got.Work.OutputTokens == nil || *got.Work.OutputTokens != 6 || got.Work.ReceiptKey == "" {
				t.Fatalf("known malformed work lost: %+v %v", got, err)
			}
			for _, decision := range got.Decisions {
				if decision.Verdict != "unknown" || decision.Accepted() || !decision.SharedWork || decision.ReceiptKey != got.Work.ReceiptKey || decision.InputTokens == nil || *decision.InputTokens != 0 {
					t.Fatalf("partial malformed decision accepted: %+v", decision)
				}
			}
			second, err := v.VerifyBatch(context.Background(), requests)
			if err != nil || !second.Work.Reused || second.Work.GenerationCalled || p.generations != 1 {
				t.Fatalf("known malformed batch retried: %+v %v", second, err)
			}
		})
	}
}

func TestPairVerifierBatchUnknownWorkStopsAndKeepsOnePendingClaim(t *testing.T) {
	for _, kind := range []string{"generation_error", "invalid_usage"} {
		t.Run(kind, func(t *testing.T) {
			p := &fixtureProvider{identity: "unknown-batch-fixture", response: fixtureValidityResponse(acceptingBatchVerdict)}
			if kind == "generation_error" {
				p.err = errors.New("synthetic lost batch outcome")
			} else {
				p.response.Usage.UncachedInputTokens = 0
			}
			v := &PairVerifier{runner: &inference.Runner{Broker: testBroker(t), Provider: p}, config: controlledTestConfig(), identity: strings.Repeat("a", 64)}
			requests := []semantic.ValidityRequest{batchValidityRequest("one?"), batchValidityRequest("two?")}
			got, err := v.VerifyBatch(context.Background(), requests)
			if err == nil || len(got.Decisions) != 0 || !got.Work.GenerationCalled || got.Work.Complete || got.Work.Durable || got.Work.InputTokens != nil || got.Work.OutputTokens != nil || got.Work.ReceiptKey == "" {
				t.Fatalf("unknown batch promoted or lost: %+v %v", got, err)
			}
			second, err := v.VerifyBatch(context.Background(), []semantic.ValidityRequest{requests[1], requests[0]})
			if !errors.Is(err, inference.ErrInFlight) || len(second.Decisions) != 0 || second.Work.GenerationCalled || second.Work.InputTokens != nil || second.Work.ReceiptKey != got.Work.ReceiptKey || p.generations != 1 {
				t.Fatalf("same pending membership retried or replaced: %+v %v", second, err)
			}
		})
	}
}

func TestPairVerifierBatchCapsOutputWithoutIncreasingOperatorLimit(t *testing.T) {
	for _, test := range []struct {
		members          int
		configured, want int64
	}{{2, 512, 128}, {2, 128, 128}, {2, 64, 64}, {2, 32, 32}, {2, 16, 16}} {
		t.Run(fmt.Sprintf("%d/%d", test.members, test.configured), func(t *testing.T) {
			requests := make([]semantic.ValidityRequest, test.members)
			for i := range requests {
				requests[i] = batchValidityRequest(fmt.Sprintf("question %d?", i))
			}
			p := &recordingValidityProvider{fixtureProvider: fixtureProvider{identity: "batch-cap-fixture", response: fixtureValidityResponse(acceptingBatchVerdict)}}
			config := controlledTestConfig()
			config.VerifierMaxOutputTokens = test.configured
			v := &PairVerifier{runner: &inference.Runner{Broker: testBroker(t), Provider: p}, config: config, identity: strings.Repeat("a", 64)}
			if _, err := v.VerifyBatch(context.Background(), requests); err != nil {
				t.Fatal(err)
			}
			if len(p.requests) != 1 || p.requests[0].MaxOutputTokens != test.want {
				t.Fatalf("batch raised or lost output bound: %+v", p.requests)
			}
		})
	}
}

func TestPairVerifierBatchIncompleteAndOverrunRetainKnownWorkAndNeverRetry(t *testing.T) {
	for _, kind := range []string{"incomplete", "output_overrun"} {
		t.Run(kind, func(t *testing.T) {
			p := &fixtureProvider{identity: "bounded-batch-fixture", response: fixtureValidityResponse(acceptingBatchVerdict)}
			if kind == "incomplete" {
				p.response.Complete = false
			} else {
				p.response.Usage.OutputTokens = 129
			}
			v := &PairVerifier{runner: &inference.Runner{Broker: testBroker(t), Provider: p}, config: controlledTestConfig(), identity: strings.Repeat("a", 64)}
			requests := []semantic.ValidityRequest{batchValidityRequest("one?"), batchValidityRequest("two?")}
			got, err := v.VerifyBatch(context.Background(), requests)
			if err == nil || len(got.Decisions) != 0 || !got.Work.GenerationCalled || got.Work.Complete || got.Work.ReceiptKey == "" || got.Work.InputTokens == nil || *got.Work.InputTokens != 100 || got.Work.OutputTokens == nil || *got.Work.OutputTokens != p.response.Usage.OutputTokens {
				t.Fatalf("bounded failed work promoted or lost: %+v %v", got, err)
			}
			second, err := v.VerifyBatch(context.Background(), requests)
			if err == nil || len(second.Decisions) != 0 || p.generations != 1 {
				t.Fatalf("failed batch automatically retried: %+v %v", second, err)
			}
		})
	}
}

func TestPairVerifierPairSmallOperatorCapTruncationWithholdsBothDecisions(t *testing.T) {
	p := &recordingValidityProvider{fixtureProvider: fixtureProvider{identity: "small-cap-verifier-fixture", response: fixtureValidityResponse(strings.TrimSuffix(acceptingBatchVerdict, "}"))}}
	p.response.Complete = false
	p.response.Usage.OutputTokens = 16
	config := controlledTestConfig()
	config.VerifierMaxOutputTokens = 16
	v := &PairVerifier{runner: &inference.Runner{Broker: testBroker(t), Provider: p}, config: config, identity: strings.Repeat("a", 64)}
	requests := []semantic.ValidityRequest{batchValidityRequest("One?"), batchValidityRequest("Two?")}
	got, err := v.VerifyBatch(t.Context(), requests)
	if err == nil || len(got.Decisions) != 0 || got.Work.Accepted() || !got.Work.GenerationCalled || got.Work.Complete || got.Work.InputTokens == nil || *got.Work.InputTokens != 100 || got.Work.OutputTokens == nil || *got.Work.OutputTokens != 16 || len(p.requests) != 1 || p.requests[0].MaxOutputTokens != 16 {
		t.Fatalf("small operator cap was raised or incomplete factors authorized reuse: %+v %v", got, err)
	}
	if second, err := v.VerifyBatch(t.Context(), requests); err == nil || len(second.Decisions) != 0 || p.generations != 1 {
		t.Fatalf("small-cap incomplete output retried: %+v %v", second, err)
	}
}

func TestPairVerifierTypedBatchWireCountsRealGuidanceAndFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name, text   string
		wantError    bool
		wantVerdicts []string
	}{
		{"typed", mixedBatchVerdict, false, []string{"accept", "unknown"}},
		{"string instead of boolean", strings.Replace(acceptingBatchVerdict, `"i0":true`, `"i0":"true"`, 1), true, nil},
		{"old ARU drift", `{"q0":"A","q1":"A"}`, true, nil},
		{"old array drift", `{"v":["A","A"]}`, true, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := make(chan map[string]any, 1)
			const template = "{{ .System }} {{ .Prompt }}"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/tags":
					io.WriteString(w, `{"models":[{"name":"qwen3.5:4b","digest":"`+strings.Repeat("a", 64)+`"}]}`)
				case "/api/show":
					json.NewEncoder(w).Encode(map[string]any{"template": template, "system": "Installed default must not replace explicit policy.", "capabilities": []string{"completion"}, "model_info": map[string]any{"general.architecture": "fixture", "fixture.context_length": 32768}})
				case "/api/generate":
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					select {
					case wire <- body:
					default:
						t.Error("typed batch repeated generation")
					}
					json.NewEncoder(w).Encode(map[string]any{"model": "qwen3.5:4b", "response": test.text, "done": true, "done_reason": "stop", "prompt_eval_count": 100, "eval_count": 6})
				default:
					t.Errorf("unexpected model fixture endpoint %s", r.URL.Path)
					http.Error(w, "unexpected", http.StatusNotFound)
				}
			}))
			defer server.Close()
			config := controlledTestConfig()

			v, err := NewPairVerifier(t.Context(), testBroker(t), server.URL, config)
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			result, err := v.VerifyBatch(t.Context(), []semantic.ValidityRequest{batchValidityRequest("Alpha question?"), batchValidityRequest("Zulu question?")})
			if (err != nil) != test.wantError || len(result.Decisions) != len(test.wantVerdicts) || !result.Work.GenerationCalled || result.Work.InputTokens == nil || *result.Work.InputTokens != 100 || result.Work.OutputTokens == nil || *result.Work.OutputTokens != 6 || result.Work.ReceiptKey == "" {
				t.Fatalf("typed batch accounting or decision count lost: %+v %v", result, err)
			}
			for i, want := range test.wantVerdicts {
				if result.Decisions[i].Verdict != want {
					t.Fatalf("unexpected typed decision: %+v", result.Decisions[i])
				}
			}
			body := <-wire
			instructions, types := validityBatchProtocol()
			system, ok := body["system"].(string)
			if !ok || !strings.HasPrefix(system, instructions+"\n\nOutput field types (JSON object):\n") || !strings.Contains(system, `{"c0":"boolean","c1":"boolean","f0":"boolean","f1":"boolean","i0":"boolean","i1":"boolean","s0":"boolean","s1":"boolean"}`) || !strings.Contains(system, "Recheck every requested field against its source before returning the object.") {
				t.Fatalf("existing typed guidance was hidden or lost: %+v", body)
			}
			format, ok := body["format"].(map[string]any)
			if !ok || format["type"] != "object" || format["additionalProperties"] != false {
				t.Fatalf("existing primitive schema was not used: %+v", body["format"])
			}
			properties, ok := format["properties"].(map[string]any)
			wantProperty := map[string]any{"type": "boolean"}
			if !ok || len(properties) != 8 || !reflect.DeepEqual(format["required"], []any{"c0", "c1", "f0", "f1", "i0", "i1", "s0", "s1"}) {
				t.Fatalf("required decoder fields changed: %+v", format)
			}
			for field := range types {
				if !reflect.DeepEqual(properties[field], wantProperty) {
					t.Fatalf("Boolean decoder field changed: %s: %+v", field, properties[field])
				}
			}
			options := body["options"].(map[string]any)
			if options["num_predict"] != float64(128) {
				t.Fatalf("typed batch cap changed: %+v", options)
			}
			prompt, ok := body["prompt"].(string)
			if !ok || !strings.HasPrefix(prompt, "\n\nTask:\n{") {
				t.Fatalf("batch input stopped using raw native JSON: %+v", body["prompt"])
			}
			schema, _ := json.Marshal(format)
			count, err := v.runner.Provider.CountInputTokens(t.Context(), inference.Request{Provider: "ollama", Model: config.VerifierModel, Prefix: instructions, Input: prompt, MaxOutputTokens: 128, ContextWindow: config.VerifierMaxInputTokens + 128 + 512, CacheMode: "default", Ollama: &inference.OllamaOptions{Format: "json", OutputTypes: types}})
			wantBytes := len(system) + len(prompt) + len(schema)
			if err != nil || count != int64(wantBytes+len(template)+512) {
				t.Fatalf("schema/guidance absent from actual adapter estimate: got %d want %d error %v", count, wantBytes+len(template)+512, err)
			}
			t.Logf("rendered_system_prompt_schema_bytes=%d typed_guidance_and_schema_bytes=%d adapter_conservative_byte_estimate=%d", wantBytes, len(system)-len(instructions)+len(schema), count)
		})
	}
}
