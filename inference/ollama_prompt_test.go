package inference

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestOllamaBoundedTextPromptPinsModeWithoutChangingApprovedRequest(t *testing.T) {
	s := ollamaOptionsSpec()
	baseline, err := BuildPlan(s)
	if err != nil || baseline.Fingerprint != "8432881ebf244e0bd4ae8e03eacaacdc946a9bbb30f278585ddcb2ed3fdccaf2" {
		t.Fatalf("legacy prompt identity changed: %+v, %v", baseline, err)
	}
	s.Ollama = &OllamaOptions{PromptMode: "bounded_text"}
	plan, err := BuildPlan(s)
	if err != nil || plan.Fingerprint == baseline.Fingerprint || plan.Request.Prefix != baseline.Request.Prefix || plan.Request.Input != baseline.Request.Input {
		t.Fatalf("prompt mode did not isolate identity or changed approved content: %+v, %v", plan, err)
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Spec
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil || !reflect.DeepEqual(decoded.Ollama, s.Ollama) {
		t.Fatalf("strict prompt mode round trip failed: %+v, %v", decoded.Ollama, err)
	}
	decodedPlan, err := BuildPlan(decoded)
	if err != nil || decodedPlan.Fingerprint != plan.Fingerprint {
		t.Fatalf("decoded prompt mode identity changed: %+v, %v", decodedPlan, err)
	}
}

func TestOllamaBoundedTextPromptMatchesCanonicalWireAndEstimate(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
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
			req.Stream, req.ContextWindow = stream, 4096
			req.Prefix = "Approved \"instructions\".\nKeep <data> intact."
			req.Input = "Reference data:\n\t\"quoted\" \\path \x01 & <literal>.\nTask: use only this data."
			legacy, err := p.CountInputTokens(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			req.Ollama = &OllamaOptions{PromptMode: "bounded_text"}
			before, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			canonical := BoundedTextPrompt(req)
			estimate, err := p.CountInputTokens(context.Background(), req)
			want := int64(len(canonical)+len("{{ .System }} {{ .Prompt }}")+len("Model default instructions.")) + 512
			if err != nil || estimate != want || estimate <= legacy || calls.generate.Load() != 0 {
				t.Fatalf("canonical wrapper/escaping or installed system not counted once: %d, %v; want %d, legacy %d", estimate, err, want, legacy)
			}
			got, err := p.Generate(context.Background(), req)
			if err != nil || !got.Complete || got.Text != `{"message":"fixture output"}` || calls.generate.Load() != 1 {
				t.Fatalf("bounded generation failed, changed final output or repeated: %+v, %v", got, err)
			}
			wantBody := map[string]any{"model": req.Model, "system": "", "prompt": canonical, "stream": stream, "think": false, "options": map[string]any{"num_predict": float64(req.MaxOutputTokens), "num_ctx": float64(req.ContextWindow)}}
			if body := <-recorded; !reflect.DeepEqual(body, wantBody) {
				t.Fatalf("wire prompt differs from exact canonical request or caps: %+v", body)
			}
			after, err := json.Marshal(req)
			if err != nil || string(before) != string(after) {
				t.Fatal("rendering mutated the caller request")
			}
		})
	}
}

func TestOllamaBoundedTextPromptRetainsTotalContextAndOutputCaps(t *testing.T) {
	for _, stream := range []bool{false, true} {
		p, calls := ollamaFixtureProvider(t, ollamaGenerateFixture)
		req := ollamaTestRequest()
		req.Stream, req.ContextWindow = stream, 4096
		legacy, err := p.CountInputTokens(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		req.ContextWindow = legacy + req.MaxOutputTokens
		req.Ollama = &OllamaOptions{PromptMode: "bounded_text"}
		if _, err := p.CountInputTokens(context.Background(), req); err == nil {
			t.Fatal("wrapped prompt bypassed context estimate cap")
		}
		if _, err := p.Generate(context.Background(), req); err == nil || calls.generate.Load() != 0 {
			t.Fatal("wrapped prompt bypassed generation context cap")
		}
	}
	p, calls := ollamaFixtureProvider(t, ollamaThinkingFixture(t, func(frame map[string]any) { frame["eval_count"] = 65 }))
	req := ollamaTestRequest()
	req.ContextWindow = 4096
	req.Ollama = &OllamaOptions{PromptMode: "bounded_text", Think: ollamaThink(true)}
	got, err := p.Generate(context.Background(), req)
	if err != nil || got.Complete || got.StopReason != "usage_limit_exceeded" || got.Usage.OutputTokens != 65 || got.Usage.ReasoningTokens != 0 || calls.generate.Load() != 1 {
		t.Fatalf("framing lost total generated usage or retried: %+v, %v", got, err)
	}
	assertNoOllamaThinkingText(t, got)
}

func TestOllamaPromptModeAndRenderedByteLimitsFailBeforeHTTP(t *testing.T) {
	p, calls := ollamaFixtureProvider(t, ollamaGenerateFixture)
	for _, mode := range []string{"legacy", "bounded-text", "BOUNDED_TEXT", " bounded_text", "schema"} {
		s := ollamaOptionsSpec()
		s.Ollama = &OllamaOptions{PromptMode: mode}
		if _, err := BuildPlan(s); err == nil {
			t.Fatalf("unsupported prompt mode planned: %s", mode)
		}
		req := ollamaTestRequest()
		req.Ollama = s.Ollama
		if _, err := p.CountInputTokens(context.Background(), req); err == nil {
			t.Fatalf("unsupported prompt mode counted: %s", mode)
		}
		if _, err := p.Generate(context.Background(), req); err == nil {
			t.Fatalf("unsupported prompt mode generated: %s", mode)
		}
	}
	s := ollamaOptionsSpec()
	s.Input = strings.Repeat("\x01", 100000)
	if _, err := BuildPlan(s); err != nil {
		t.Fatalf("legacy raw prompt fixture exceeds existing planned byte bound: %v", err)
	}
	s.Ollama = &OllamaOptions{PromptMode: "bounded_text"}
	if _, err := BuildPlan(s); err == nil || !strings.Contains(err.Error(), "rendered Ollama prompt exceeds") {
		t.Fatalf("escaped canonical prompt exceeded the planned byte cap: %v", err)
	}
	req := ollamaTestRequest()
	req.ContextWindow = 65536
	req.Input = strings.Repeat("\x01", maxProviderRequestBytes/6)
	req.Ollama = &OllamaOptions{PromptMode: "bounded_text"}
	if len(req.Prefix)+len(req.Input) >= maxProviderRequestBytes || len(BoundedTextPrompt(req)) <= maxProviderRequestBytes {
		t.Fatal("fixture must fit raw bytes and exceed the rendered byte cap")
	}
	if _, err := p.CountInputTokens(context.Background(), req); err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("oversized rendered prompt passed preflight: %v", err)
	}
	if _, err := p.Generate(context.Background(), req); err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("oversized rendered prompt generated: %v", err)
	}
	for _, raw := range []struct{ prefix, input string }{
		{strings.Repeat("x", maxProviderRequestBytes+1), "fixture input"},
		{"fixture instructions", strings.Repeat("\x01", maxProviderRequestBytes+1)},
		{strings.Repeat("x", maxProviderRequestBytes/2), strings.Repeat("\x01", maxProviderRequestBytes/2+1)},
	} {
		req.Prefix, req.Input = raw.prefix, raw.input
		if _, err := p.CountInputTokens(context.Background(), req); err == nil || !strings.Contains(err.Error(), "byte limit") {
			t.Fatalf("oversized raw prompt passed preflight: %v", err)
		}
		if _, err := p.Generate(context.Background(), req); err == nil || !strings.Contains(err.Error(), "byte limit") {
			t.Fatalf("oversized raw prompt generated: %v", err)
		}
	}
	if calls.tags.Load() != 1 || calls.show.Load() != 1 || calls.generate.Load() != 0 {
		t.Fatal("invalid framing reached HTTP after constructor discovery")
	}
}
