package inference

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"
)

func ollamaSeed(value int64) *int64 { return &value }

func TestOllamaSeedBoundsIdentityAndOwnedPlan(t *testing.T) {
	s := ollamaOptionsSpec()
	base, _ := BuildPlan(s)
	seen := map[string]bool{base.Fingerprint: true}
	for _, seed := range []int64{0, 42, 2147483647, -1, 2147483648} {
		s.Ollama = &OllamaOptions{Seed: ollamaSeed(seed)}
		p, err := BuildPlan(s)
		if (err == nil) != (seed >= 0 && seed <= 2147483647) {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if err != nil {
			continue
		}
		if seen[p.Fingerprint] {
			t.Fatal("seed absent/zero/value identities collided")
		}
		seen[p.Fingerprint] = true
		if p.Request.Ollama.Seed == s.Ollama.Seed {
			t.Fatal("plan aliased seed")
		}
		*s.Ollama.Seed = 99
		if *p.Request.Ollama.Seed != seed {
			t.Fatal("plan drifted after mutation")
		}
	}
	s.Ollama = &OllamaOptions{Temperature: ollamaTemperature(1), Seed: ollamaSeed(42), Think: ollamaThink(true), KeepAliveSeconds: ollamaKeepAliveSeconds(0), GenerationTimeoutSeconds: ollamaSeed(120), Format: "json", OutputTypes: map[string]string{"message": "string"}, FieldRoles: map[string]string{"message": "identifier"}}
	p, err := BuildPlan(s)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(p.Request)
	*s.Ollama.Temperature = 2
	*s.Ollama.Seed = 7
	*s.Ollama.Think = false
	*s.Ollama.KeepAliveSeconds = 2
	*s.Ollama.GenerationTimeoutSeconds = 1
	s.Ollama.OutputTypes["message"] = "integer"
	s.Ollama.FieldRoles["message"] = "policy_action"
	after, _ := json.Marshal(p.Request)
	if string(before) != string(after) {
		t.Fatal("caller mutated fingerprinted native options")
	}
}

func TestOllamaSeedTemperatureNativeAndWrappedWire(t *testing.T) {
	for _, mode := range []string{"", "bounded_text"} {
		for _, stream := range []bool{false, true} {
			for _, seed := range []*int64{nil, ollamaSeed(0), ollamaSeed(42)} {
				recorded := make(chan map[string]any, 1)
				p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if json.NewDecoder(r.Body).Decode(&body) != nil {
						t.Error("bad request")
					}
					recorded <- body
					if stream {
						w.Header().Set("Content-Type", "application/x-ndjson")
						writeStreamFrame(w, ollamaJSONGenerateFixture)
					} else {
						io.WriteString(w, ollamaJSONGenerateFixture)
					}
				}, nil)
				req := ollamaTestRequest()
				req.Stream = stream
				req.Ollama = &OllamaOptions{PromptMode: mode, Temperature: ollamaTemperature(1), Seed: seed}
				if _, err := p.CountInputTokens(context.Background(), req); err != nil || calls.generate.Load() != 0 {
					t.Fatalf("count failed or generated: %v", err)
				}
				got, err := p.Generate(context.Background(), req)
				if err != nil || !got.Complete {
					t.Fatalf("generate: %+v %v", got, err)
				}
				body := <-recorded
				options := body["options"].(map[string]any)
				want := map[string]any{"num_predict": float64(req.MaxOutputTokens), "num_ctx": float64(req.ContextWindow), "temperature": float64(1)}
				if seed != nil {
					want["seed"] = float64(*seed)
				}
				if !reflect.DeepEqual(options, want) || body["stream"] != stream {
					t.Fatalf("wrong sampling wire: %+v", body)
				}
			}
		}
	}
}

func TestOllamaInvalidSeedNeverCountsOrGenerates(t *testing.T) {
	p, calls := ollamaFixtureProvider(t, ollamaGenerateFixture)
	for _, seed := range []int64{-1, 2147483648} {
		req := ollamaTestRequest()
		req.Ollama = &OllamaOptions{Seed: ollamaSeed(seed)}
		if _, err := p.CountInputTokens(context.Background(), req); err == nil {
			t.Fatal("invalid count")
		}
		if _, err := p.Generate(context.Background(), req); err == nil {
			t.Fatal("invalid generate")
		}
	}
	if calls.generate.Load() != 0 {
		t.Fatal("invalid seed generated")
	}
	for _, raw := range []string{`0.5`, `"42"`, `9223372036854775808`} {
		var s Spec
		if json.Unmarshal([]byte(`{"ollama":{"seed":`+raw+`}}`), &s) == nil {
			t.Fatal("noninteger or overflow seed decoded")
		}
	}
}
