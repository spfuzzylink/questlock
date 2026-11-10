package delegation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/semantic"
)

func TestFactorizedPairProtocolPinsReviewedSufficiencyContract(t *testing.T) {
	h := sha256.Sum256([]byte(factorizedPairInstructions))
	if hex.EncodeToString(h[:]) != "a9a7ffc20ac2c08285fdcf3cc98a3565afeb4740f363375dedc9bc9d627ba8c8" {
		t.Fatal("reviewed pair protocol bytes changed")
	}
	if factorizedVerifierVersion != "questlock-factorized-validity-v20" {
		t.Fatal("reviewed pair protocol must retain its distinct policy identity")
	}
}

func mixedFactorizedFixture(t *testing.T) (*FactorizedVerifier, *independentFactorizedProvider, *independentFactorizedProvider) {
	t.Helper()
	v, _ := factorizedTestVerifier(t, nil)
	c := v.config
	c.VerifierPairModel, c.VerifierPairMaxInputTokens = "other:local", 500
	c, err := semantic.NormalizeReuseConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	source := &independentFactorizedProvider{identity: "source-runtime", support: factorizedTestSupport, pair: factorizedTestPair}
	pair := &independentFactorizedProvider{identity: "pair-runtime", support: factorizedTestSupport, pair: factorizedTestPair}
	v.config = c
	v.runner.Provider = source
	v.pairRunner = &inference.Runner{Broker: v.runner.Broker, Provider: pair}
	v.sourceRuntime, v.pairRuntime = v.runner.RuntimeIdentity(), v.pairRunner.RuntimeIdentity()
	v.identity = factorizedPolicyIdentity(c, v.sourceRuntime, v.pairRuntime)
	return v, source, pair
}

func TestFactorizedMixedModelsRetainCachedSourceKnownUsageAndStageCaps(t *testing.T) {
	v, source, pair := mixedFactorizedFixture(t)
	request := factorizedTestRequest()
	first, err := v.Verify(t.Context(), request)
	if err != nil || !first.Accepted() || !first.MixedModelUsage || semantic.ValidateVerifierWork(first, v.config) != nil || len(source.requests) != 1 || len(pair.requests) != 1 {
		t.Fatalf("mixed stages failed: %+v %v", first, err)
	}
	if first.Work[0].Model != v.config.VerifierModel || first.Work[1].Model != v.config.VerifierPairModel || first.Work[0].RuntimeIdentity != v.sourceRuntime || first.Work[1].RuntimeIdentity != v.pairRuntime || *first.InputTokens != 182 || *first.OutputTokens != 34 {
		t.Fatalf("mixed metering attribution lost: %+v", first)
	}
	if source.requests[0].Model != v.config.VerifierModel || pair.requests[0].Model != v.config.VerifierPairModel || !strings.HasPrefix(source.requests[0].Prefix, factorizedSupportInstructions) || !strings.HasPrefix(pair.requests[0].Prefix, factorizedPairInstructions) {
		t.Fatal("source/pair routing changed evidence or protocol")
	}
	if v.stageSpec("source", "x", "", "", "supported", "complete").MaxInputTokens != v.config.VerifierMaxInputTokens/2 || v.stageSpec("equivalence", "x", "", "", "a", "b").MaxInputTokens != 500 {
		t.Fatal("pair cap changed source allowance")
	}
	request.Question = "Which person is responsible for the remaining preview access review?"
	warm, err := v.Verify(t.Context(), request)
	if err != nil || !warm.Accepted() || semantic.ValidateVerifierWork(warm, v.config) != nil || !warm.Work[0].Reused || *warm.Work[0].InputTokens != 0 || *warm.InputTokens != 91 || len(source.requests) != 1 || len(pair.requests) != 2 {
		t.Fatalf("warm source cost was recounted: %+v %v", warm, err)
	}
	exact, err := v.Verify(t.Context(), request)
	if err != nil || !exact.Reused || exact.GenerationCalled || *exact.InputTokens != 0 || *exact.OutputTokens != 0 || semantic.ValidateVerifierWork(exact, v.config) != nil || len(source.requests) != 1 || len(pair.requests) != 2 {
		t.Fatalf("exact decision repeated inference: %+v %v", exact, err)
	}
}

func TestFactorizedPairPolicyBindsBothRuntimesAndEffectiveCaps(t *testing.T) {
	v, _, _ := mixedFactorizedFixture(t)
	for name, mutate := range map[string]func(*semantic.ReuseConfig, *string, *string){
		"source runtime": func(_ *semantic.ReuseConfig, source, _ *string) { *source = reuseHash("different-source") },
		"pair runtime":   func(_ *semantic.ReuseConfig, _, pair *string) { *pair = reuseHash("different-pair") },
		"pair model":     func(c *semantic.ReuseConfig, _, _ *string) { c.VerifierPairModel = "third:local" },
		"pair cap":       func(c *semantic.ReuseConfig, _, _ *string) { c.VerifierPairMaxInputTokens-- },
	} {
		t.Run(name, func(t *testing.T) {
			c, source, pair := v.config, v.sourceRuntime, v.pairRuntime
			mutate(&c, &source, &pair)
			if factorizedPolicyIdentity(c, source, pair) == v.Identity() {
				t.Fatal("policy drift retained identity")
			}
		})
	}
	for _, stage := range []string{"source", "pair"} {
		t.Run("live "+stage+" drift", func(t *testing.T) {
			v, source, pair := mixedFactorizedFixture(t)
			if stage == "source" {
				source.identity += "-changed"
			} else {
				pair.identity += "-changed"
			}
			out, err := v.Verify(t.Context(), factorizedTestRequest())
			if err == nil || out.Accepted() || len(out.Work) != 0 || len(source.requests)+len(pair.requests) != 0 {
				t.Fatalf("drift reached inference: %+v %v", out, err)
			}
		})
	}
}

func TestFactorizedMixedPolicyCannotExtendSourceExpiryOrChangeExactFirst(t *testing.T) {
	f := independentFactorizedSetup(t)
	f.request.SemanticReuse.VerifierPairModel = "other:local"
	f.request.SemanticReuse.VerifierPairMaxInputTokens = 500
	f.verifier.config = *f.request.SemanticReuse
	pair := &independentFactorizedProvider{identity: "separate-pair", pair: factorizedTestPair}
	f.verifier.pairRunner = &inference.Runner{Broker: f.broker, Provider: pair, Now: func() time.Time { return f.now }}
	f.verifier.sourceRuntime, f.verifier.pairRuntime = f.verifier.runner.RuntimeIdentity(), f.verifier.pairRunner.RuntimeIdentity()
	f.verifier.identity = factorizedPolicyIdentity(f.verifier.config, f.verifier.sourceRuntime, f.verifier.pairRuntime)
	original := f.request.Spec.Input
	f.seed(t)
	f.request.Spec.Input = "Before the preview can proceed, what is still required?"
	alias, err := f.router.Run(t.Context(), f.request)
	if err != nil || alias.SemanticReuse == nil || !alias.SemanticReuse.GenerationAvoided || len(f.stages.requests) != 1 || len(pair.requests) != 1 {
		t.Fatalf("alias setup: %+v %v", alias, err)
	}
	for _, input := range []string{original, f.request.Spec.Input} {
		f.request.Spec.Input = input
		exact, err := f.router.Run(t.Context(), f.request)
		if err != nil || len(exact.Attempts) != 1 || !exact.Attempts[0].Result.Reused || len(f.stages.requests) != 1 || len(pair.requests) != 1 {
			t.Fatalf("exact bypass changed: %+v %v", exact, err)
		}
	}
	f.now = f.now.Add(2 * time.Hour)
	f.request.Spec.Input = "Which requirements remain before the preview proceeds?"
	expired, err := f.router.Run(t.Context(), f.request)
	if err != nil || expired.SemanticReuse == nil || expired.SemanticReuse.GenerationAvoided || f.target.generations != 2 || len(pair.requests) != 1 || len(f.stages.requests) != 1 {
		t.Fatalf("source window extended: %+v %v", expired, err)
	}
}

func TestFactorizedNativePairModelConstructionWireAndMetadataDrift(t *testing.T) {
	// Only this in-process HTTP fixture receives requests. Scripted verdicts
	// establish adapter/provenance behavior, never semantic model quality.
	const sourceModel, pairModel = "source:local", "pair:local"
	var mu sync.Mutex
	pairDigest := strings.Repeat("b", 64)
	var calls []independentNativeVerifierBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/tags":
			io.WriteString(w, `{"models":[{"name":"`+sourceModel+`","digest":"`+strings.Repeat("a", 64)+`"},{"name":"`+pairModel+`","digest":"`+pairDigest+`"}]}`)
		case "/api/show":
			io.WriteString(w, `{"template":"{{ .System }} {{ .Prompt }}","capabilities":["completion"],"model_info":{"general.architecture":"fixture","fixture.context_length":65536}}`)
		case "/api/generate":
			var body independentNativeVerifierBody
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("bad native body")
				w.WriteHeader(400)
				return
			}
			calls = append(calls, body)
			text := factorizedTestSupport
			if body.Model == pairModel {
				text = factorizedTestPair
			}
			json.NewEncoder(w).Encode(map[string]any{"model": body.Model, "response": text, "done": true, "done_reason": "stop", "prompt_eval_count": 100, "eval_count": 20})
		default:
			t.Error("unexpected native fixture path", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	c := controlledTestConfig()
	c.VerifierPolicy, c.VerifierModel, c.VerifierPairModel = "factorized-v1", sourceModel, pairModel
	c.VerifierMaxInputTokens, c.VerifierPairMaxInputTokens = 16384, 6400
	v, err := NewFactorizedVerifier(t.Context(), testBroker(t), srv.URL, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(v.Close)
	if v.pairRunner == nil {
		t.Fatal("alternate model did not get a runner")
	}
	request := factorizedTestRequest()
	out, err := v.Verify(t.Context(), request)
	if err != nil || !out.Accepted() || semantic.ValidateVerifierWork(out, v.config) != nil {
		t.Fatalf("native mixed call: %+v %v", out, err)
	}
	mu.Lock()
	bodies := append([]independentNativeVerifierBody(nil), calls...)
	mu.Unlock()
	if len(bodies) != 2 || bodies[0].Model != sourceModel || bodies[1].Model != pairModel || !strings.HasPrefix(bodies[0].System, factorizedSupportInstructions+"\n\n") || !strings.HasPrefix(bodies[1].System, factorizedPairInstructions+"\n\n") || bodies[0].Options.Context != v.config.VerifierMaxInputTokens/2+v.config.VerifierMaxOutputTokens/2+512 || bodies[1].Options.Context != 6400+v.config.VerifierMaxOutputTokens/2+512 {
		t.Fatalf("native stages lost policy/caps: %+v", bodies)
	}
	for _, body := range bodies {
		if body.Think == nil || *body.Think || body.KeepAlive == nil || *body.KeepAlive != 0 || body.Stream || body.Options.Temperature != 0 {
			t.Fatalf("default pair enabled thinking/streaming: %+v", body)
		}
	}
	mu.Lock()
	pairDigest = strings.Repeat("c", 64)
	mu.Unlock()
	request.Question = "Which person is responsible for the remaining access review?"
	drift, err := v.Verify(t.Context(), request)
	mu.Lock()
	count := len(calls)
	mu.Unlock()
	if err == nil || drift.Accepted() || len(drift.Work) != 2 || !drift.Work[0].Reused || drift.Work[1].GenerationCalled || count != 2 {
		t.Fatalf("metadata drift generated or accepted: %+v %v calls=%d", drift, err, count)
	}
	changed, err := NewFactorizedVerifier(t.Context(), v.runner.Broker, srv.URL, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(changed.Close)
	if changed.Identity() == v.Identity() {
		t.Fatal("new pair digest retained old policy")
	}
	same := c
	same.VerifierPairModel = sourceModel
	sameV, err := NewFactorizedVerifier(t.Context(), v.runner.Broker, srv.URL, same)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sameV.Close)
	if sameV.pairRunner != nil {
		t.Fatal("same explicit model created a duplicate provider")
	}
}
