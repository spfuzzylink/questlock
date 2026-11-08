package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/spfuzzylink/questlock/inference"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/semantic"
)

func TestIndependentFactorizedPairSamplingNativeWireAndTotalThinkingOutputAccounting(t *testing.T) {
	const model = "fixture:local"
	const privateTrace = "private-pair-reasoning-must-not-be-persisted"
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
			text, output := factorizedTestSupport, 20
			frame := map[string]any{"model": model, "done": true, "done_reason": "stop", "prompt_eval_count": 100}
			if fields["question"] != nil {
				text, output = factorizedTestPair, 1000
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
	c.VerifierPolicy, c.VerifierModel, c.VerifierPairThink = "factorized-v1", model, true
	c.VerifierMaxInputTokens, c.VerifierMaxOutputTokens = 16384, 4096
	temperature, seed := float64(1), int64(42)
	c.VerifierPairTemperature, c.VerifierPairSeed = &temperature, &seed
	broker := testBroker(t)
	v, err := NewFactorizedVerifier(t.Context(), broker, srv.URL, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(v.Close)
	temperature, seed = 2, 7 // Caller mutation must not alter normalized policy or wire.
	request := factorizedTestRequest()
	first, err := v.Verify(t.Context(), request)
	if err != nil || !first.Accepted() || semantic.ValidateVerifierWork(first, v.config) != nil || len(first.Work) != 2 || first.OutputTokens == nil || *first.OutputTokens != 1020 || first.Work[1].OutputTokens == nil || *first.Work[1].OutputTokens != 1000 {
		t.Fatalf("pair reasoning output was omitted from usage or native verification failed: %+v %v", first, err)
	}
	wire := snapshot()
	if len(wire) != 2 {
		t.Fatalf("expected two explicit stages, got %d", len(wire))
	}
	for i, body := range wire {
		if body.Think == nil || *body.Think != (i == 1) || body.Stream || body.Options.Predict != 2048 || body.KeepAlive == nil || *body.KeepAlive != 0 || body.Options.Temperature != float64(i) || (i == 0 && body.Options.Seed != nil) || (i == 1 && (body.Options.Seed == nil || *body.Options.Seed != 42)) {
			t.Fatalf("pair-only thinking altered stage or exceeded bounded wire policy: %+v", body)
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
	if err != nil || !novel.Accepted() || len(novel.Work) != 2 || !novel.Work[0].Reused || novel.Work[0].GenerationCalled || *novel.Work[0].OutputTokens != 0 || novel.OutputTokens == nil || *novel.OutputTokens != 1000 || len(snapshot()) != 3 {
		t.Fatalf("cached source was not independently reused with zero current usage: %+v %v", novel, err)
	}
	repeat, err := v.Verify(t.Context(), request)
	if err != nil || !repeat.Accepted() || !repeat.Reused || repeat.GenerationCalled || repeat.OutputTokens == nil || *repeat.OutputTokens != 0 || len(snapshot()) != 3 {
		t.Fatalf("exact decision repeated source reasoning or applicability inference: %+v %v", repeat, err)
	}
	noThinking := c
	noThinking.VerifierPairThink, noThinking.VerifierMaxOutputTokens = false, 128
	old, err := NewFactorizedVerifier(t.Context(), broker, srv.URL, noThinking)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(old.Close)
	if old.Identity() == v.Identity() || len(snapshot()) != 3 {
		t.Fatal("pair-thinking policy did not isolate decisions or constructor generated")
	}
}

func TestIndependentPairThinkingDoesNotChangeExactAliasesOrSourceExpiry(t *testing.T) {
	f := independentFactorizedSetup(t)
	f.request.SemanticReuse.VerifierPairThink = true
	f.request.SemanticReuse.VerifierMaxOutputTokens = 4096
	f.verifier.config.VerifierPairThink = true
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

func TestPairThinkingFalsePreservesV17IdentityAndExplicitTrueBindsRequest(t *testing.T) {
	v, p := factorizedTestVerifier(t, nil)
	c := v.config
	c.VerifierPolicy = "factorized-v1"
	base := factorizedPolicyIdentity(c, strings.Repeat("a", 64), strings.Repeat("b", 64))
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), "verifier_pair_think") {
		t.Fatal("false option changedlegacyidentitybytes")
	}
	c.VerifierPairThink = true
	if factorizedPolicyIdentity(c, strings.Repeat("a", 64), strings.Repeat("b", 64)) == base {
		t.Fatal("true flag didnotbindpolicy")
	}
	c.VerifierPairThink = false
	if factorizedPolicyIdentity(c, strings.Repeat("a", 64), strings.Repeat("b", 64)) != base {
		t.Fatal("false option changedlegacyv17identity")
	}
	v.config.VerifierPairThink = true
	first, err := v.Verify(t.Context(), factorizedTestRequest())
	if err != nil || !first.Accepted() || len(p.requests) != 2 {
		t.Fatal("pair fixture failed", err)
	}
	if p.requests[0].Ollama.Think == nil || *p.requests[0].Ollama.Think || p.requests[1].Ollama.Think == nil || !*p.requests[1].Ollama.Think {
		t.Fatal("source/pairreasoningnotindependent")
	}
	before, err := inference.BuildPlan(v.stageSpec("equivalence", "same", factorizedPairInstructions, `{"context":"complete"}`, "answer_covers_current", "unchanged_facts"))
	if err != nil {
		t.Fatal(err)
	}
	v.config.VerifierPairThink = false
	after, err := inference.BuildPlan(v.stageSpec("equivalence", "same", factorizedPairInstructions, `{"context":"complete"}`, "answer_covers_current", "unchanged_facts"))
	if err != nil || after.Fingerprint == before.Fingerprint {
		t.Fatal("pairnativeflagdidnotbindrequestidentity", err)
	}
}

func TestPairThinkingUnknownPairRetainsSourceCostAndFencesRepeat(t *testing.T) {
	v, p := factorizedTestVerifier(t, nil)
	v.config.VerifierPairThink = true
	temperature, seed := float64(1), int64(42)
	v.config.VerifierPairTemperature, v.config.VerifierPairSeed = &temperature, &seed
	p.failStage = "equivalence"
	out, err := v.Verify(t.Context(), factorizedTestRequest())
	if err == nil || out.Accepted() || len(out.Work) != 2 || out.Work[0].InputTokens == nil || *out.Work[0].InputTokens != 101 || out.Work[1].InputTokens != nil || out.Work[1].OutputTokens != nil || !out.Work[1].GenerationCalled || out.Work[1].ReceiptKey == "" {
		t.Fatalf("unknownpairlostsourcecostorfence: %+v %v", out, err)
	}
	calls := p.sourceCalls + p.pairCalls
	p.failStage = ""
	again, err := v.Verify(t.Context(), factorizedTestRequest())
	if err == nil || again.Accepted() || p.sourceCalls+p.pairCalls != calls {
		t.Fatal("uncertainpairreasoningwasregenerated")
	}
}

type canceledPairThinkingProvider struct {
	*factorizedTestProvider
	started    chan struct{}
	pairStarts atomic.Int32
}

func (p *canceledPairThinkingProvider) Generate(ctx context.Context, r inference.Request) (inference.Response, error) {
	if p.stage(r) != "equivalence" {
		return p.factorizedTestProvider.Generate(ctx, r)
	}
	if p.pairStarts.Add(1) == 1 {
		close(p.started)
		<-ctx.Done()
		return inference.Response{}, ctx.Err()
	}
	return inference.Response{}, errors.New("unexpectedpairretry")
}
func TestPairThinkingCallerCancellationPreservesUncertainFence(t *testing.T) {
	v, base := factorizedTestVerifier(t, nil)
	v.config.VerifierPairThink = true
	temperature, seed := float64(1), int64(42)
	v.config.VerifierPairTemperature, v.config.VerifierPairSeed = &temperature, &seed
	p := &canceledPairThinkingProvider{factorizedTestProvider: base, started: make(chan struct{})}
	v.runner.Provider = p
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan semantic.ValidityResult, 1)
	errs := make(chan error, 1)
	go func() { out, err := v.Verify(ctx, factorizedTestRequest()); done <- out; errs <- err }()
	select {
	case <-p.started:
	case <-time.After(3 * time.Second):
		t.Fatal("pairdidnotstart")
	}
	cancel()
	out := <-done
	err := <-errs
	if err == nil || out.Accepted() || len(out.Work) != 2 || out.Work[0].InputTokens == nil || out.Work[1].InputTokens != nil || !out.Work[1].GenerationCalled {
		t.Fatalf("cancellationlostknownsourceoruncertainpair: %+v %v", out, err)
	}
	_, err = v.Verify(t.Context(), factorizedTestRequest())
	if err == nil || p.pairStarts.Load() != 1 || base.sourceCalls != 1 {
		t.Fatal("canceledpairreasoningwasretried")
	}
}

type pairThinkingCountProbe struct {
	*factorizedTestProvider
	counted []inference.Request
}

func (p *pairThinkingCountProbe) CountInputTokens(ctx context.Context, r inference.Request) (int64, error) {
	p.counted = append(p.counted, r)
	return p.factorizedTestProvider.CountInputTokens(ctx, r)
}
func TestPairThinkingNativeCountReceivesIndependentStageOptions(t *testing.T) {
	v, base := factorizedTestVerifier(t, nil)
	v.config.VerifierPairThink = true
	temperature, seed := float64(1), int64(42)
	v.config.VerifierPairTemperature, v.config.VerifierPairSeed = &temperature, &seed
	p := &pairThinkingCountProbe{factorizedTestProvider: base}
	v.runner.Provider = p
	out, err := v.Verify(t.Context(), factorizedTestRequest())
	if err != nil || !out.Accepted() || len(p.counted) != 2 {
		t.Fatal("stage counting didnot precede boundednativefixture", err)
	}
	if p.counted[0].Ollama.Think == nil || *p.counted[0].Ollama.Think || p.counted[1].Ollama.Think == nil || !*p.counted[1].Ollama.Think {
		t.Fatal("native count useddifferent reasoningoption than stage generation")
	}
	for i := range p.counted {
		if p.counted[i].Ollama.Temperature == nil || *p.counted[i].Ollama.Temperature != float64(i) || (i == 0 && p.counted[i].Ollama.Seed != nil) || (i == 1 && (p.counted[i].Ollama.Seed == nil || *p.counted[i].Ollama.Seed != 42)) {
			t.Fatal("counting lost explicit pair sampling or altered source")
		}
		if p.counted[i].Input != base.requests[i].Input || p.counted[i].Prefix != base.requests[i].Prefix || p.counted[i].ContextWindow != base.requests[i].ContextWindow {
			t.Fatal("counting lostfullrenderedstageinput")
		}
	}
}
