package delegation

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/semantic"
)

func TestIndependentFactorizedSourceThinkingNativeWireAndTotalOutputAccounting(t *testing.T) {
	const model = "fixture:local"
	const privateTrace = "private-reasoning-must-not-be-persisted"
	var mu sync.Mutex
	var bodies []independentNativeVerifierBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			io.WriteString(w, `{"models":[{"name":"`+model+`","digest":"`+strings.Repeat("a", 64)+`"}]}`)
		case "/api/show":
			json.NewEncoder(w).Encode(map[string]any{"template": "{{ .System }} {{ .Prompt }}", "capabilities": []string{"completion"}, "thinking": map[string]any{"values": []bool{true, false}}, "model_info": map[string]any{"general.architecture": "fixture", "fixture.context_length": 65536}})
		case "/api/generate":
			var body independentNativeVerifierBody
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("invalid native request")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			bodies = append(bodies, body)
			mu.Unlock()
			var fields map[string]json.RawMessage
			if json.Unmarshal([]byte(strings.TrimPrefix(body.Prompt, "\n\nTask:\n")), &fields) != nil {
				t.Error("native prompt lost its task data record")
			}
			text, output := factorizedTestSupport, 1000
			frame := map[string]any{"model": model, "done": true, "done_reason": "stop", "prompt_eval_count": 100}
			if fields["question"] != nil {
				text, output = factorizedTestPair, 20
			} else {
				frame["thinking"] = privateTrace
			}
			frame["response"], frame["eval_count"] = text, output
			json.NewEncoder(w).Encode(frame)
		default:
			t.Error("unexpected local fixture endpoint", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	snapshot := func() []independentNativeVerifierBody {
		mu.Lock()
		defer mu.Unlock()
		return append([]independentNativeVerifierBody(nil), bodies...)
	}
	c := controlledTestConfig()
	c.VerifierPolicy, c.VerifierModel, c.VerifierSourceThink = "factorized-v1", model, true
	c.VerifierMaxInputTokens, c.VerifierMaxOutputTokens = 16384, 4096
	broker := testBroker(t)
	v, err := NewFactorizedVerifier(t.Context(), broker, srv.URL, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(v.Close)
	request := factorizedTestRequest()
	first, err := v.Verify(t.Context(), request)
	if err != nil || !first.Accepted() || semantic.ValidateVerifierWork(first, v.config) != nil || len(first.Work) != 2 || first.OutputTokens == nil || *first.OutputTokens != 1020 || first.Work[0].OutputTokens == nil || *first.Work[0].OutputTokens != 1000 {
		t.Fatalf("source reasoning output was omitted from usage or native verification failed: %+v %v", first, err)
	}
	wire := snapshot()
	if len(wire) != 2 {
		t.Fatalf("expected two explicit stages, got %d", len(wire))
	}
	for i, body := range wire {
		if body.Think == nil || *body.Think != (i == 0) || body.Stream || body.Options.Predict != 2048 || body.KeepAlive == nil || *body.KeepAlive != 0 || body.Options.Temperature != 0 {
			t.Fatalf("source-only thinking altered stage or exceeded bounded wire policy: %+v", body)
		}
	}
	for _, key := range []string{first.ReceiptKey, first.Work[0].ReceiptKey, first.Work[1].ReceiptKey} {
		artifact, err := broker.Get(t.Context(), key)
		if err != nil || strings.Contains(artifact.Content, privateTrace) {
			t.Fatalf("private reasoning leaked or proof unavailable: %v", err)
		}
	}
	request.Question = "Which person is responsible for that remaining preview access review?"
	novel, err := v.Verify(t.Context(), request)
	if err != nil || !novel.Accepted() || len(novel.Work) != 2 || !novel.Work[0].Reused || novel.Work[0].GenerationCalled || *novel.Work[0].OutputTokens != 0 || novel.OutputTokens == nil || *novel.OutputTokens != 20 || len(snapshot()) != 3 {
		t.Fatalf("thinking certificate was not independently reused with zero current usage: %+v %v", novel, err)
	}
	repeat, err := v.Verify(t.Context(), request)
	if err != nil || !repeat.Accepted() || !repeat.Reused || repeat.GenerationCalled || repeat.OutputTokens == nil || *repeat.OutputTokens != 0 || len(snapshot()) != 3 {
		t.Fatalf("exact decision repeated source reasoning or applicability inference: %+v %v", repeat, err)
	}
	noThinking := c
	noThinking.VerifierSourceThink, noThinking.VerifierMaxOutputTokens = false, 128
	old, err := NewFactorizedVerifier(t.Context(), broker, srv.URL, noThinking)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(old.Close)
	if old.Identity() == v.Identity() || len(snapshot()) != 3 {
		t.Fatal("source-thinking policy did not isolate certificates or constructor generated")
	}
}

func TestIndependentSourceThinkingDoesNotChangeExactAliasesOrSourceExpiry(t *testing.T) {
	f := independentFactorizedSetup(t)
	f.request.SemanticReuse.VerifierSourceThink = true
	f.request.SemanticReuse.VerifierMaxOutputTokens = 4096
	f.verifier.config.VerifierSourceThink = true
	f.verifier.config.VerifierMaxOutputTokens = 4096
	original := f.request.Spec.Input
	f.seed(t)
	f.request.Spec.Input = "What must still happen before the preview proceeds?"
	alias, err := f.router.Run(t.Context(), f.request)
	if err != nil || alias.SemanticReuse == nil || !alias.SemanticReuse.GenerationAvoided || len(f.stages.requests) != 2 {
		t.Fatalf("thinking-policy alias setup failed: %+v %v", alias.SemanticReuse, err)
	}
	for _, input := range []string{original, f.request.Spec.Input} {
		f.request.Spec.Input = input
		exact, err := f.router.Run(t.Context(), f.request)
		if err != nil || len(exact.Attempts) != 1 || !exact.Attempts[0].Result.Reused || len(f.stages.requests) != 2 || f.target.generations != 1 || len(exact.SemanticReuse.Verifications) != 0 || exact.SemanticReuse.EmbeddingInputTokens != nil {
			t.Fatalf("thinking policy altered exact lookup: %+v %v", exact.SemanticReuse, err)
		}
	}
	f.now = f.now.Add(2 * time.Hour)
	f.request.Spec.Input = "Which requirements remain for that preview?"
	expired, err := f.router.Run(t.Context(), f.request)
	if err != nil || expired.SemanticReuse == nil || expired.SemanticReuse.GenerationAvoided || f.target.generations != 2 || len(f.stages.requests) != 2 {
		t.Fatalf("thinking certificate extended original source expiry: %+v %v", expired.SemanticReuse, err)
	}
}
