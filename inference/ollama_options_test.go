package inference

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func ollamaTemperature(value float64) *float64  { return &value }
func ollamaKeepAliveSeconds(value int64) *int64 { return &value }

func ollamaOptionsSpec() Spec {
	s := testSpec()
	s.Provider, s.Model = "ollama", "qwen3:4b"
	return s
}

func TestOllamaOmittedOptionsPreserveLegacyEncodingAndPlanIdentity(t *testing.T) {
	// Captured before adding generation options. Both nil fields must disappear
	// entirely so existing stored requests and their fingerprints remain valid.
	s := ollamaOptionsSpec()
	p, err := BuildPlan(s)
	if err != nil {
		t.Fatal(err)
	}
	wantSpec := `{"task":"handoff","revision":"commit-1","provider":"ollama","model":"qwen3:4b","instructions":"Keep it short.","input":"Summarize current changes.","max_context_bytes":0,"max_input_tokens":1000,"max_output_tokens":100,"reuse_for_seconds":3600}`
	wantRequest := `{"provider":"ollama","model":"qwen3:4b","context_window":1612,"prefix":"Keep it short.","input":"\n\nTask:\nSummarize current changes.","max_output_tokens":100,"cache_mode":"default"}`
	for _, tt := range []struct {
		name  string
		value any
		want  string
	}{{"spec", s, wantSpec}, {"request", p.Request, wantRequest}} {
		got, err := json.Marshal(tt.value)
		if err != nil || string(got) != tt.want {
			t.Fatalf("legacy %s encoding = %s, error = %v", tt.name, got, err)
		}
	}
	if p.Fingerprint != "8432881ebf244e0bd4ae8e03eacaacdc946a9bbb30f278585ddcb2ed3fdccaf2" {
		t.Fatal("legacy local request identity changed", p.Fingerprint)
	}
}

func TestOllamaOptionsPinPlanIdentityWithoutChangingPrompt(t *testing.T) {
	s := ollamaOptionsSpec()
	baseline, err := BuildPlan(s)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{baseline.Fingerprint: true}
	for _, options := range []*OllamaOptions{
		{},
		{Format: "json"},
		{Temperature: ollamaTemperature(0)},
		{Temperature: ollamaTemperature(2)},
		{Format: "json", Temperature: ollamaTemperature(0)},
		{KeepAliveSeconds: ollamaKeepAliveSeconds(0)},
		{KeepAliveSeconds: ollamaKeepAliveSeconds(300)},
		{Format: "json", Temperature: ollamaTemperature(0), KeepAliveSeconds: ollamaKeepAliveSeconds(0)},
	} {
		s.Ollama = options
		p, err := BuildPlan(s)
		if err != nil {
			t.Fatal(err)
		}
		if p.Request.Ollama != options || p.Request.Prefix != baseline.Request.Prefix || p.Request.Input != baseline.Request.Input {
			t.Fatal("options lost or planned prompt changed")
		}
		if seen[p.Fingerprint] {
			t.Fatal("distinct explicit generation controls share a request identity")
		}
		seen[p.Fingerprint] = true
		var decoded Spec
		encoded, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(strings.NewReader(string(encoded)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&decoded); err != nil || !reflect.DeepEqual(decoded.Ollama, options) {
			t.Fatalf("strict spec round trip lost options: %+v, %v", decoded.Ollama, err)
		}
		q, err := BuildPlan(decoded)
		if err != nil || q.Fingerprint != p.Fingerprint {
			t.Fatalf("options identity changed after decoding: %v", err)
		}
	}
}

func TestOllamaOptionsRejectUnsupportedProviderAndInvalidControls(t *testing.T) {
	for _, provider := range []string{"openai", "claude", "anthropic", "openrouter", "sglang", "codex"} {
		t.Run(provider, func(t *testing.T) {
			s := testSpec()
			s.Provider, s.Ollama = provider, &OllamaOptions{}
			if _, err := BuildPlan(s); err == nil || !strings.Contains(err.Error(), "ollama settings require") {
				t.Fatalf("Ollama controls accepted by %s: %v", provider, err)
			}
		})
	}
	p, calls := ollamaFixtureProvider(t, ollamaGenerateFixture)
	for _, tt := range []struct {
		name    string
		options OllamaOptions
	}{
		{"schema", OllamaOptions{Format: `{"type":"object"}`}},
		{"uppercase", OllamaOptions{Format: "JSON"}},
		{"whitespace", OllamaOptions{Format: " json"}},
		{"negative", OllamaOptions{Temperature: ollamaTemperature(-0.01)}},
		{"above maximum", OllamaOptions{Temperature: ollamaTemperature(2.01)}},
		{"nan", OllamaOptions{Temperature: ollamaTemperature(math.NaN())}},
		{"positive infinity", OllamaOptions{Temperature: ollamaTemperature(math.Inf(1))}},
		{"negative infinity", OllamaOptions{Temperature: ollamaTemperature(math.Inf(-1))}},
		{"negative keep alive", OllamaOptions{KeepAliveSeconds: ollamaKeepAliveSeconds(-1)}},
		{"keep alive above maximum", OllamaOptions{KeepAliveSeconds: ollamaKeepAliveSeconds(301)}},
		{"keep alive maximum integer", OllamaOptions{KeepAliveSeconds: ollamaKeepAliveSeconds(math.MaxInt64)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := ollamaOptionsSpec()
			s.Ollama = &tt.options
			if _, err := BuildPlan(s); err == nil {
				t.Fatal("invalid options planned")
			}
			req := ollamaTestRequest()
			req.Ollama = &tt.options
			if _, err := p.CountInputTokens(context.Background(), req); err == nil {
				t.Fatal("invalid options passed preflight")
			}
			if _, err := p.Generate(context.Background(), req); err == nil {
				t.Fatal("invalid options generated")
			}
		})
	}
	if calls.tags.Load() != 1 || calls.show.Load() != 1 || calls.generate.Load() != 0 {
		t.Fatal("invalid controls reached runtime after constructor discovery")
	}
}

func TestOllamaKeepAliveRejectsJSONIntegerOverflowAndNonIntegerValues(t *testing.T) {
	for _, value := range []string{"9223372036854775808", "-9223372036854775809", "0.5", `"0"`} {
		var s Spec
		decoder := json.NewDecoder(strings.NewReader(`{"ollama":{"keep_alive_seconds":` + value + `}}`))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&s); err == nil {
			t.Fatalf("non-integer or overflowing keep_alive_seconds decoded: %s", value)
		}
	}
}

const ollamaJSONGenerateFixture = `{"model":"qwen3:4b","response":"{\"message\":\"fixture output\"}","done":true,"done_reason":"stop","prompt_eval_count":100,"eval_count":20}`

func TestOllamaExplicitOptionsSerializeForBothGenerationModes(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tt := range []struct {
			name    string
			options *OllamaOptions
		}{
			{"omitted", nil},
			{"empty", &OllamaOptions{}},
			{"json", &OllamaOptions{Format: "json"}},
			{"zero", &OllamaOptions{Temperature: ollamaTemperature(0)}},
			{"maximum", &OllamaOptions{Temperature: ollamaTemperature(2)}},
			{"json and zero", &OllamaOptions{Format: "json", Temperature: ollamaTemperature(0)}},
			{"unload after request", &OllamaOptions{KeepAliveSeconds: ollamaKeepAliveSeconds(0)}},
			{"retain maximum", &OllamaOptions{KeepAliveSeconds: ollamaKeepAliveSeconds(300)}},
			{"json zero and unload", &OllamaOptions{Format: "json", Temperature: ollamaTemperature(0), KeepAliveSeconds: ollamaKeepAliveSeconds(0)}},
		} {
			name := "nonstream/" + tt.name
			if stream {
				name = "stream/" + tt.name
			}
			t.Run(name, func(t *testing.T) {
				recorded := make(chan map[string]any, 1)
				p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
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
				req.Stream, req.Ollama = stream, tt.options
				if _, err := p.CountInputTokens(context.Background(), req); err != nil {
					t.Fatal(err)
				}
				if calls.generate.Load() != 0 {
					t.Fatal("options made preflight run inference")
				}
				got, err := p.Generate(context.Background(), req)
				if err != nil || !got.Complete || got.Text != `{"message":"fixture output"}` {
					t.Fatalf("generation = %+v, %v", got, err)
				}
				options := map[string]any{"num_predict": float64(req.MaxOutputTokens), "num_ctx": float64(req.ContextWindow)}
				want := map[string]any{"model": "qwen3:4b", "system": req.Prefix, "prompt": req.Input, "stream": stream, "think": false, "options": options}
				if tt.options != nil {
					if tt.options.Format != "" {
						want["format"] = tt.options.Format
					}
					if tt.options.Temperature != nil {
						options["temperature"] = *tt.options.Temperature
					}
					if tt.options.KeepAliveSeconds != nil {
						want["keep_alive"] = float64(*tt.options.KeepAliveSeconds)
					}
				}
				if body := <-recorded; !reflect.DeepEqual(body, want) {
					t.Fatalf("generation controls, prompt, or caps changed: %+v", body)
				}
				if calls.generate.Load() != 1 {
					t.Fatal("generation repeated")
				}
			})
		}
	}
}

func TestOllamaOptionsRetainContextAndOutputCaps(t *testing.T) {
	p, calls := ollamaFixtureProvider(t, strings.Replace(ollamaJSONGenerateFixture, `"eval_count":20`, `"eval_count":65`, 1))
	req := ollamaTestRequest()
	req.Ollama = &OllamaOptions{Format: "json", Temperature: ollamaTemperature(0), KeepAliveSeconds: ollamaKeepAliveSeconds(0)}
	tooSmall := req
	tooSmall.ContextWindow = 600
	if _, err := p.CountInputTokens(context.Background(), tooSmall); err == nil {
		t.Fatal("options bypassed context preflight cap")
	}
	if _, err := p.Generate(context.Background(), tooSmall); err == nil || calls.generate.Load() != 0 {
		t.Fatal("options bypassed generation context cap")
	}
	result, err := p.Generate(context.Background(), req)
	if err != nil || result.Complete || result.StopReason != "usage_limit_exceeded" || result.Usage.OutputTokens != 65 || calls.generate.Load() != 1 {
		t.Fatalf("options lost reported overrun without retry: %+v, %v", result, err)
	}
}

func TestOllamaRejectedOptionsNeverFallBackOrRetry(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":"private runtime detail"}`)
			}, nil)
			req := ollamaTestRequest()
			req.Stream = stream
			req.Ollama = &OllamaOptions{Format: "json", Temperature: ollamaTemperature(0), KeepAliveSeconds: ollamaKeepAliveSeconds(0)}
			got, err := p.Generate(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), "HTTP 400") || strings.Contains(err.Error(), "private runtime detail") || got.Complete || got.Text != "" || calls.generate.Load() != 1 {
				t.Fatalf("rejected controls retried, leaked detail or approved output: %+v, %v", got, err)
			}
		})
	}
}
