package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spfuzzylink/questlock/inference"
)

func localModelSpec() inference.Spec {
	spec := validModelSpec()
	spec.Provider = "ollama"
	spec.Model = "qwen3.5:4b"
	spec.MaxInputTokens = 12000
	spec.MaxOutputTokens = 512
	spec.ReuseForSeconds = 3600
	spec.Context[0].Text = "Approved memory: prefer Go; use spfuzzylink/ branches; preserve published versions and working features."
	return spec
}

func TestModelRunLocalWithoutAPIKeysAndSeparatesChangedWeights(t *testing.T) {
	broker, err := newModelQuestFixture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer broker.close()
	t.Setenv("QUESTLOCK_TOKEN", broker.tokens[0])
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OLLAMA_API_KEY", "")
	var generations atomic.Int64
	var digest atomic.Value
	digest.Store(strings.Repeat("a", 64))
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" {
			t.Error("local mode sent a provider credential")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			if r.Method != http.MethodGet {
				t.Error("installed model discovery must be GET")
			}
			json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": "qwen3.5:4b", "model": "qwen3.5:4b", "digest": digest.Load().(string)}}})
		case "/api/show":
			json.NewEncoder(w).Encode(map[string]any{
				"template": "{{ .System }} {{ .Prompt }}", "system": "",
				"capabilities": []string{"completion", "thinking"},
				"thinking":     map[string]any{"values": []bool{true, false}},
				"model_info":   map[string]any{"general.architecture": "qwen3_5", "qwen3_5.context_length": 262144},
			})
		case "/api/generate":
			generations.Add(1)
			var input struct {
				Model   string `json:"model"`
				System  string `json:"system"`
				Prompt  string `json:"prompt"`
				Stream  *bool  `json:"stream"`
				Think   *bool  `json:"think"`
				Options struct {
					NumPredict int64 `json:"num_predict"`
					NumCtx     int64 `json:"num_ctx"`
				} `json:"options"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.Model != "qwen3.5:4b" || input.Stream == nil || *input.Stream || input.Think == nil || *input.Think || input.Options.NumPredict != 512 || input.Options.NumCtx != 13024 {
				t.Errorf("local generation did not preserve its bounded native contract: %+v", input)
			}
			if !strings.Contains(input.System, "spfuzzylink/") || !strings.Contains(input.Prompt, "Fixed a failing request.") {
				t.Errorf("approved memory or current facts missing: %+v", input)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"model": "qwen3.5:4b", "created_at": "2030-01-02T12:00:00Z", "response": "A verified local fixture handoff.",
				"done": true, "done_reason": "stop", "prompt_eval_count": 256, "eval_count": 64,
			})
		default:
			t.Errorf("unexpected local model route %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	spec, err := json.Marshal(localModelSpec())
	if err != nil {
		t.Fatal(err)
	}
	path := saveModelSpec(t, spec)
	invoke := func() inference.Result {
		t.Helper()
		var output, help bytes.Buffer
		if err := modelCommandIO([]string{"run", "--spec", path, "--broker", broker.server.URL, "--api-base-url", provider.URL, "--timeout", "5s"}, &output, &help); err != nil {
			t.Fatal(err)
		}
		var result inference.Result
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !result.Response.Complete || !result.Durable || result.CostUSD != nil || result.AvoidedCostUSD != nil {
			t.Fatalf("local result lacks completion/persistence or invents token pricing: %+v", result)
		}
		return result
	}
	first, second := invoke(), invoke()
	if first.Reused || !second.Reused || generations.Load() != 1 || second.ThisRunUsage != (inference.Usage{}) || second.AvoidedUsage.InputTokens != 256 || second.AvoidedUsage.OutputTokens != 64 {
		t.Fatalf("local CLI did not generate once then reuse: first=%+v second=%+v generations=%d", first, second, generations.Load())
	}
	digest.Store(strings.Repeat("b", 64))
	third := invoke()
	if third.Reused || third.Fingerprint == first.Fingerprint || generations.Load() != 2 {
		t.Fatalf("new installed weights reused an older answer: third=%+v generations=%d", third, generations.Load())
	}
}

func TestModelLocalDiscoveryHonorsCommandTimeout(t *testing.T) {
	t.Setenv("QUESTLOCK_TOKEN", "fixture-token")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer provider.Close()
	spec, err := json.Marshal(localModelSpec())
	if err != nil {
		t.Fatal(err)
	}
	var output, help bytes.Buffer
	err = modelCommandIO([]string{"run", "--spec", saveModelSpec(t, spec), "--api-base-url", provider.URL, "--timeout", "50ms"}, &output, &help)
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") || output.Len() != 0 {
		t.Fatalf("local discovery escaped command deadline or required an API key: error=%v output=%q", err, output.String())
	}
}
