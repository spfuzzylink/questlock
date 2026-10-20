package inference

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const ollamaPrivateThinkingMarker = "fixture-reasoning-must-never-be-returned-or-stored"

func ollamaThinkingFixture(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	return mutatedFixture(t, ollamaGenerateFixture, func(frame map[string]any) {
		frame["thinking"] = ollamaPrivateThinkingMarker
		frame["response"] = `{"message":"fixture final answer"}`
		if mutate != nil {
			mutate(frame)
		}
	})
}

func assertNoOllamaThinkingText(t *testing.T, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), ollamaPrivateThinkingMarker) {
		t.Fatal("private reasoning entered returned or stored fields")
	}
}

func TestOllamaThinkingOptInReturnsOnlyFinalAnswerAndTotalOutputUsage(t *testing.T) {
	body := make(chan map[string]any, 1)
	fixture := ollamaThinkingFixture(t, nil)
	p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		body <- got
		io.WriteString(w, fixture)
	}, nil)
	req := ollamaTestRequest()
	req.Ollama = &OllamaOptions{Format: "json", Temperature: ollamaTemperature(0), KeepAliveSeconds: ollamaKeepAliveSeconds(0), Think: ollamaThink(true)}
	if _, err := p.CountInputTokens(context.Background(), req); err != nil || calls.generate.Load() != 0 {
		t.Fatalf("thinking preflight generated or failed: %v", err)
	}
	got, err := p.Generate(context.Background(), req)
	if err != nil || !got.Complete || got.Text != `{"message":"fixture final answer"}` || got.StopReason != "stop" {
		t.Fatalf("explicit thinking rejected valid final answer: %+v, %v", got, err)
	}
	wantUsage := Usage{InputTokens: 100, UncachedInputTokens: 60, CacheReadTokens: 40, OutputTokens: 20}
	if got.Usage != wantUsage || calls.generate.Load() != 1 {
		t.Fatalf("generated total usage split, lost or retried: %+v", got.Usage)
	}
	wantBody := map[string]any{"model": req.Model, "system": req.Prefix, "prompt": req.Input, "stream": false, "think": true, "format": "json", "keep_alive": float64(0), "options": map[string]any{"num_predict": float64(req.MaxOutputTokens), "num_ctx": float64(req.ContextWindow), "temperature": float64(0)}}
	if got := <-body; !reflect.DeepEqual(got, wantBody) {
		t.Fatalf("thinking changed prompts or other explicit controls: %+v", got)
	}
	assertNoOllamaThinkingText(t, got)
}

func TestOllamaThinkingStillRequiresExplicitOptIn(t *testing.T) {
	for _, options := range []*OllamaOptions{nil, {}, {Think: ollamaThink(false)}} {
		p, calls := ollamaFixtureProvider(t, ollamaThinkingFixture(t, nil))
		req := ollamaTestRequest()
		req.Ollama = options
		got, err := p.Generate(context.Background(), req)
		if err != nil || got.Complete || got.StopReason != "unexpected_thinking" || got.Usage.OutputTokens != 20 || calls.generate.Load() != 1 {
			t.Fatalf("unexpected thinking accepted or usage lost: %+v, %v", got, err)
		}
		assertNoOllamaThinkingText(t, got)
	}
}

func TestOllamaThinkingRequiresExplicitInstalledBooleanCapability(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing metadata", func(show map[string]any) { delete(show, "thinking") }},
		{"disabled only", func(show map[string]any) { show["thinking"] = map[string]any{"values": []any{false}} }},
		{"named levels", func(show map[string]any) { show["thinking"] = map[string]any{"values": []any{false, "true", "high"}} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			show := mutatedFixture(t, ollamaShowFixture, tt.mutate)
			endpoint, calls := ollamaFixtureServer(t, ollamaTagsFixture, show, ollamaThinkingFixture(t, nil))
			p, err := NewOllamaProvider(endpoint, "qwen3:4b")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(p.client.CloseIdleConnections)
			req := ollamaTestRequest()
			req.Ollama = &OllamaOptions{Think: ollamaThink(true)}
			if _, err := p.CountInputTokens(context.Background(), req); err == nil || !strings.Contains(err.Error(), "think:true") {
				t.Fatalf("unadvertised thinking passed preflight: %v", err)
			}
			if got, err := p.Generate(context.Background(), req); err == nil || !strings.Contains(err.Error(), "think:true") || got.Complete || got.Text != "" {
				t.Fatalf("unadvertised thinking generated: %+v, %v", got, err)
			}
			if calls.generate.Load() != 0 {
				t.Fatal("unadvertised thinking reached generation")
			}
		})
	}
}

func TestOllamaThinkingRejectsStreamingBeforeHTTP(t *testing.T) {
	s := ollamaOptionsSpec()
	s.Stream, s.Ollama = true, &OllamaOptions{Think: ollamaThink(true)}
	if _, err := BuildPlan(s); err == nil || !strings.Contains(err.Error(), "nonstream") {
		t.Fatalf("streaming thinking was planned: %v", err)
	}
	p, calls := ollamaFixtureProvider(t, ollamaThinkingFixture(t, nil))
	req := streamRequest()
	req.Ollama = s.Ollama
	if _, err := p.CountInputTokens(context.Background(), req); err == nil || !strings.Contains(err.Error(), "nonstream") {
		t.Fatalf("streaming thinking passed preflight: %v", err)
	}
	if got, err := p.Generate(context.Background(), req); err == nil || !strings.Contains(err.Error(), "nonstream") || got.Complete || got.Text != "" {
		t.Fatalf("streaming thinking generated: %+v, %v", got, err)
	}
	if calls.tags.Load() != 1 || calls.show.Load() != 1 || calls.generate.Load() != 0 {
		t.Fatal("streaming thinking reached HTTP after constructor discovery")
	}
}

func TestOllamaThinkingCannotApproveIncompleteUnsupportedOrOverLimitOutput(t *testing.T) {
	for _, tt := range []struct {
		name       string
		mutate     func(map[string]any)
		stopReason string
		wantError  bool
	}{
		{"unfinished", func(m map[string]any) { m["done"] = false }, "incomplete", false},
		{"missing done", func(m map[string]any) { delete(m, "done") }, "incomplete", false},
		{"missing final answer", func(m map[string]any) { m["response"] = "" }, "stop", false},
		{"length", func(m map[string]any) { m["done_reason"] = "length" }, "length", false},
		{"tools", func(m map[string]any) { m["tool_calls"] = []any{map[string]any{"name": "fixture-tool"}} }, "unsupported_output", false},
		{"runtime error", func(m map[string]any) { m["error"] = "fixture failure" }, "stop", false},
		{"output cap", func(m map[string]any) { m["eval_count"] = 65 }, "usage_limit_exceeded", false},
		{"context cap", func(m map[string]any) { m["prompt_eval_count"] = 1010 }, "usage_limit_exceeded", false},
		{"count overflow", func(m map[string]any) { m["eval_count"] = int64(math.MaxInt64) }, "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, calls := ollamaFixtureProvider(t, ollamaThinkingFixture(t, tt.mutate))
			req := ollamaTestRequest()
			req.Ollama = &OllamaOptions{Think: ollamaThink(true)}
			got, err := p.Generate(context.Background(), req)
			if (err != nil) != tt.wantError || got.Complete || got.StopReason != tt.stopReason || calls.generate.Load() != 1 {
				t.Fatalf("thinking bypassed failure or retried: %+v, %v", got, err)
			}
			if !tt.wantError && (got.Usage.InputTokens < 1 || got.Usage.OutputTokens < 1 || got.Usage.ReasoningTokens != 0) {
				t.Fatal("known total usage was discarded or an unsupported reasoning split invented")
			}
			assertNoOllamaThinkingText(t, got)
		})
	}
}

func TestRunnerOllamaThinkingStoresOnlyFinalTextAndKnownTotalUsage(t *testing.T) {
	for _, tt := range []struct {
		name    string
		mutate  func(map[string]any)
		wantErr error
		output  int64
		state   string
	}{
		{"complete", nil, nil, 20, "complete"},
		{"reasoning exhausts output cap", func(m map[string]any) { m["eval_count"] = 101 }, ErrBudgetExceeded, 101, "limit_exceeded"},
		{"missing final", func(m map[string]any) { m["response"] = "" }, ErrIncomplete, 20, "incomplete"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, calls := ollamaFixtureProvider(t, ollamaThinkingFixture(t, tt.mutate))
			r, _ := testRunner(t)
			r.Provider = p
			s := ollamaOptionsSpec()
			s.Ollama = &OllamaOptions{Think: ollamaThink(true)}
			got, err := r.Run(context.Background(), s)
			if !errors.Is(err, tt.wantErr) || got.ThisRunUsage.OutputTokens != tt.output || got.ThisRunUsage.ReasoningTokens != 0 || !got.Durable || calls.generate.Load() != 1 {
				t.Fatalf("thinking outcome or known usage lost: %+v, %v", got, err)
			}
			assertNoOllamaThinkingText(t, got)
			artifact, err := r.Broker.Get(context.Background(), "inference/v1/jobs/"+got.Fingerprint)
			if err != nil {
				t.Fatal(err)
			}
			assertNoOllamaThinkingText(t, artifact)
			var record jobRecord
			if err := json.Unmarshal([]byte(artifact.Content), &record); err != nil || record.State != tt.state || record.Response == nil || record.Response.Usage.OutputTokens != tt.output {
				t.Fatalf("stored outcome or usage lost: %+v, %v", record, err)
			}
			second, err := r.Run(context.Background(), s)
			if !errors.Is(err, tt.wantErr) || calls.generate.Load() != 1 || (tt.wantErr == nil && !second.Reused) {
				t.Fatalf("reasoning response retried: %+v, %v", second, err)
			}
			assertNoOllamaThinkingText(t, second)
		})
	}
}
