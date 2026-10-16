package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// Field names and inclusive token semantics follow OpenRouter's official
// usage-accounting example and Chat Completions schema. This is a deterministic
// protocol fixture, not evidence of a live paid model or account's capabilities.
const openRouterResponseFixture = `{"id":"gen_fixture","object":"chat.completion","model":"openai/test-model","provider":"OpenAI","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Useful answer."}}],"usage":{"prompt_tokens":1000,"completion_tokens":20,"total_tokens":1020,"prompt_tokens_details":{"cached_tokens":600,"cache_write_tokens":300,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":5,"audio_tokens":0},"cost":0.0012}}`

func openRouterTestConfig() OpenRouterConfig {
	return OpenRouterConfig{UpstreamProvider: "openai", InputTokenUpperBound: 1200, CacheWriteUsage: "required"}
}

func openRouterTestRequest() Request {
	c := openRouterTestConfig()
	return Request{Provider: "openrouter", Model: "openai/test-model", Prefix: "Stable instructions.", Input: "Summarize this change.", MaxOutputTokens: 64, CacheMode: "default", OpenRouter: &c}
}

func newOpenRouterFixture(t *testing.T, h http.HandlerFunc) *OpenRouterProvider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := NewOpenRouterProvider("unit-test-key", srv.URL+"/api/v1/", openRouterTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.client.CloseIdleConnections)
	return p
}

func TestOpenRouterPinnedTextRequestAndNativePartitions(t *testing.T) {
	var calls atomic.Int64
	p := newOpenRouterFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/api/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer unit-test-key" || r.Header.Get("x-api-key") != "" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("incorrect OpenRouter request method, path, or headers")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{
			"model": "openai/test-model", "max_completion_tokens": float64(64), "stream": false,
			"messages": []any{map[string]any{"role": "system", "content": "Stable instructions."}, map[string]any{"role": "user", "content": "Summarize this change."}},
			"provider": map[string]any{"order": []any{"openai"}, "only": []any{"openai"}, "allow_fallbacks": false, "require_parameters": true, "max_price": map[string]any{"request": float64(0)}},
		}
		if !reflect.DeepEqual(body, want) {
			t.Errorf("unexpected gateway payload: %#v", body)
		}
		io.WriteString(w, openRouterResponseFixture)
	})
	req := openRouterTestRequest()
	n, err := p.CountInputTokens(context.Background(), req)
	if err != nil || n != 1200 || calls.Load() != 0 {
		t.Fatalf("explicit reservation bound = %d, %v; network calls = %d", n, err, calls.Load())
	}
	got, err := p.Generate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Complete || got.Text != "Useful answer." || got.StopReason != "stop" || calls.Load() != 1 {
		t.Fatalf("unexpected result: %+v; calls=%d", got, calls.Load())
	}
	wantUsage := Usage{InputTokens: 1000, UncachedInputTokens: 100, CacheReadTokens: 600, CacheWriteTokens: 300, OutputTokens: 20, ReasoningTokens: 5}
	if got.Usage != wantUsage {
		t.Fatalf("usage=%+v; want %+v (reasoning is already in output)", got.Usage, wantUsage)
	}
}

func TestOpenRouterConstructorAndIdentity(t *testing.T) {
	c := openRouterTestConfig()
	p, err := NewOpenRouterProvider("unit-test-key", "", c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.client.CloseIdleConnections)
	if p.baseURL != "https://openrouter.ai/api/v1" || p.client.Transport.(*http.Transport).Proxy != nil || p.client.Timeout != providerTimeout {
		t.Fatal("default endpoint or transport policy differs")
	}
	if strings.Contains(p.Identity(), "unit-test-key") || !strings.Contains(p.Identity(), "openrouter.ai/api/v1:openai:1200:required") {
		t.Fatal("identity must contain route policy and exclude credentials")
	}
	for _, mutate := range []func(*OpenRouterConfig){
		func(c *OpenRouterConfig) { c.UpstreamProvider = "anthropic" },
		func(c *OpenRouterConfig) { c.InputTokenUpperBound++ },
		func(c *OpenRouterConfig) { c.CacheWriteUsage = "not_applicable" },
	} {
		other := c
		mutate(&other)
		q, err := NewOpenRouterProvider("different-key", "", other)
		if err != nil || q.Identity() == p.Identity() {
			t.Fatalf("route policy must isolate identity: %+v %v", other, err)
		}
		q.client.CloseIdleConnections()
	}
	for _, invalid := range []OpenRouterConfig{
		{}, {"openai", 0, "required"}, {"openai", maxProviderTokens + 1, "required"}, {"openai", 1, ""}, {"openai", 1, "unknown"},
		{"OpenAI", 1, "required"}, {"openai,anthropic", 1, "required"}, {"google-vertex/us-east5", 1, "required"}, {"openai:floor", 1, "required"},
	} {
		if _, err := NewOpenRouterProvider("key", "", invalid); err == nil {
			t.Errorf("accepted invalid configuration %+v", invalid)
		}
	}
	for _, endpoint := range []string{"http://example.com/api/v1", "https://user:pass@example.com", "https://example.com?key=bad", "https://example.com/#frag", "https://example.com/a/../b"} {
		if _, err := NewOpenRouterProvider("key", endpoint, c); err == nil {
			t.Errorf("accepted unsafe endpoint %q", endpoint)
		}
	}
	for _, key := range []string{"", "a\nb", "a b"} {
		if _, err := NewOpenRouterProvider(key, "", c); err == nil {
			t.Error("accepted invalid API key")
		}
	}
}

func TestOpenRouterRejectsUnsupportedRequestsBeforeNetwork(t *testing.T) {
	var calls atomic.Int64
	p := newOpenRouterFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	cases := map[string]func(*Request){
		"no route":                func(r *Request) { r.OpenRouter = nil },
		"mismatched route":        func(r *Request) { r.OpenRouter.UpstreamProvider = "anthropic" },
		"mismatched bound":        func(r *Request) { r.OpenRouter.InputTokenUpperBound++ },
		"mismatched cache policy": func(r *Request) { r.OpenRouter.CacheWriteUsage = "not_applicable" },
		"provider":                func(r *Request) { r.Provider = "openai" },
		"stream":                  func(r *Request) { r.Stream = true },
		"reasoning":               func(r *Request) { r.ReasoningEffort = "high" },
		"context window":          func(r *Request) { r.ContextWindow = 8192 },
		"cache breakpoints":       func(r *Request) { r.CacheMode = "prefix" },
		"auto model":              func(r *Request) { r.Model = "openrouter/auto" },
		"model variant":           func(r *Request) { r.Model += ":floor" },
		"preset":                  func(r *Request) { r.Model = "@preset/mine" },
		"empty input":             func(r *Request) { r.Input = "" },
		"invalid utf8":            func(r *Request) { r.Input = string([]byte{255}) },
		"large input":             func(r *Request) { r.Input = strings.Repeat("x", maxProviderRequestBytes+1) },
		"unbounded output":        func(r *Request) { r.MaxOutputTokens = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := openRouterTestRequest()
			mutate(&r)
			if _, err := p.CountInputTokens(context.Background(), r); err == nil {
				t.Fatal("unsupported request passed preflight")
			}
			if _, err := p.Generate(context.Background(), r); err == nil {
				t.Fatal("unsupported request generated")
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("invalid requests reached a provider")
	}
}

func TestOpenRouterUnknownUsageNeverBecomesKnownZero(t *testing.T) {
	cases := map[string][2]string{
		"missing usage":            {`,"usage":`, `,"other_usage":`},
		"missing prompt":           {`"prompt_tokens":1000`, `"other_prompt":1000`},
		"null prompt":              {`"prompt_tokens":1000`, `"prompt_tokens":null`},
		"fractional prompt":        {`"prompt_tokens":1000`, `"prompt_tokens":1.1`},
		"missing output":           {`"completion_tokens":20`, `"other_output":20`},
		"missing total":            {`"total_tokens":1020`, `"other_total":1020`},
		"wrong total":              {`"total_tokens":1020`, `"total_tokens":1021`},
		"missing cache reads":      {`"cached_tokens":600`, `"other_reads":600`},
		"null cache reads":         {`"cached_tokens":600`, `"cached_tokens":null`},
		"missing cache writes":     {`"cache_write_tokens":300`, `"other_writes":300`},
		"negative cache writes":    {`"cache_write_tokens":300`, `"cache_write_tokens":-1`},
		"disjoint replay counters": {`"cached_tokens":600`, `"cached_tokens":2000`},
		"double counted partition": {`"cache_write_tokens":300`, `"cache_write_tokens":600`},
		"reasoning exceeds output": {`"reasoning_tokens":5`, `"reasoning_tokens":21`},
		"negative output":          {`"completion_tokens":20`, `"completion_tokens":-20`},
		"negative cost":            {`"cost":0.0012`, `"cost":-0.0012`},
		"audio input":              {`"audio_tokens":0`, `"audio_tokens":1`},
		"tool usage":               {`"cost":0.0012`, `"cost":0.0012,"server_tool_use_details":{"tool_calls_executed":1}`},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			var raw openRouterResponse
			body := strings.Replace(openRouterResponseFixture, edit[0], edit[1], 1)
			err := json.Unmarshal([]byte(body), &raw)
			if err == nil {
				_, err = raw.normalize(openRouterTestRequest(), openRouterTestConfig())
			}
			if err == nil {
				t.Fatal("unknown or invalid usage accepted")
			}
		})
	}
}

func TestOpenRouterCacheWriteDeclaration(t *testing.T) {
	var raw openRouterResponse
	if err := json.Unmarshal([]byte(strings.Replace(openRouterResponseFixture, `,"cache_write_tokens":300`, "", 1)), &raw); err != nil {
		t.Fatal(err)
	}
	c := openRouterTestConfig()
	c.CacheWriteUsage = "not_applicable"
	got, err := raw.normalize(openRouterTestRequest(), c)
	if err != nil || !got.Complete || got.Usage.CacheWriteTokens != 0 || got.Usage.UncachedInputTokens != 400 {
		t.Fatalf("explicit no-write-pricing route: %+v %v", got, err)
	}
	if err := json.Unmarshal([]byte(openRouterResponseFixture), &raw); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.normalize(openRouterTestRequest(), c); err == nil {
		t.Fatal("positive cache writes contradicted not_applicable policy")
	}
}

func TestOpenRouterIncompleteResponsesRetainKnownUsage(t *testing.T) {
	cases := map[string][2]string{
		"different model":     {`"model":"openai/test-model"`, `"model":"openai/other-model"`},
		"different upstream":  {`"provider":"OpenAI"`, `"provider":"Anthropic"`},
		"unreported upstream": {`"provider":"OpenAI"`, `"provider":""`},
		"length":              {`"finish_reason":"stop"`, `"finish_reason":"length"`},
		"refusal":             {`"role":"assistant"`, `"refusal":"Refused","role":"assistant"`},
		"tool call":           {`"role":"assistant"`, `"tool_calls":[{"id":"call_1"}],"role":"assistant"`},
		"wrong role":          {`"role":"assistant"`, `"role":"user"`},
		"wrong index":         {`"index":0`, `"index":1`},
		"empty text":          {`"content":"Useful answer."`, `"content":""`},
		"error":               {`"object":"chat.completion"`, `"error":{"message":"sensitive"},"object":"chat.completion"`},
		"output cap":          {`"completion_tokens":20,"total_tokens":1020`, `"completion_tokens":65,"total_tokens":1065`},
		"input bound":         {`"prompt_tokens":1000,"completion_tokens":20,"total_tokens":1020`, `"prompt_tokens":1201,"completion_tokens":20,"total_tokens":1221`},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			var raw openRouterResponse
			if err := json.Unmarshal([]byte(strings.Replace(openRouterResponseFixture, edit[0], edit[1], 1)), &raw); err != nil {
				t.Fatal(err)
			}
			got, err := raw.normalize(openRouterTestRequest(), openRouterTestConfig())
			if err != nil || got.Complete || got.Usage.InputTokens < 1000 || got.Usage.OutputTokens < 20 {
				t.Fatalf("incomplete generation must retain actual usage: %+v %v", got, err)
			}
		})
	}
}

func TestOpenRouterNoRedirectRetryOrErrorEcho(t *testing.T) {
	var targetCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	for _, status := range []int{307, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int64
			p := newOpenRouterFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", target.URL+"/unit-test-key")
				w.WriteHeader(status)
				io.WriteString(w, "unit-test-key sensitive-user-input")
			})
			_, err := p.Generate(context.Background(), openRouterTestRequest())
			if err == nil || strings.Contains(err.Error(), "unit-test-key") || strings.Contains(err.Error(), "sensitive-user-input") || strings.Contains(err.Error(), target.URL) {
				t.Fatalf("unsafe error: %v", err)
			}
			if calls.Load() != 1 || targetCalls.Load() != 0 {
				t.Fatal("request retried or followed redirect")
			}
		})
	}
}

func TestOpenRouterMalformedResponseAndCancellation(t *testing.T) {
	for _, body := range []string{`not JSON unit-test-key`, `{"usage":{"prompt_tokens":1e100}}`, strings.Repeat("x", maxProviderResponseBytes+1)} {
		p := newOpenRouterFixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
		if _, err := p.Generate(context.Background(), openRouterTestRequest()); err == nil || strings.Contains(err.Error(), "unit-test-key") {
			t.Fatal("malformed response accepted or echoed")
		}
	}
	p := newOpenRouterFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("cancelled request reached server") })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.CountInputTokens(ctx, openRouterTestRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("preflight cancellation lost: %v", err)
	}
	if _, err := p.Generate(ctx, openRouterTestRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("generation cancellation lost: %v", err)
	}
}

func openRouterRunnerSpec() Spec {
	s := testSpec()
	s.Provider, s.Model = "openrouter", "openai/test-model"
	c := openRouterTestConfig()
	s.OpenRouter = &c
	s.MaxInputTokens, s.MaxOutputTokens = 1200, 64
	s.Prices = &Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
	s.MaxCostUSD, s.DailyBudgetUSD = .001264, .0023
	return s
}

func TestOpenRouterRunnerUnknownUsageRetainsReservationAndNeverRetries(t *testing.T) {
	var calls atomic.Int64
	p := newOpenRouterFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, strings.Replace(openRouterResponseFixture, `"cached_tokens":600`, `"unknown_cache":600`, 1))
	})
	r, _ := testRunner(t)
	r.Provider = p
	s := openRouterRunnerSpec()
	first, err := r.Run(context.Background(), s)
	if err == nil || first.Measurement.Outcome != "uncertain" || !first.Measurement.GenerationCalled || first.Measurement.KnownGenerationUsage {
		t.Fatalf("unknown usage must stop as uncertain: %+v %v", first, err)
	}
	if _, err := r.Run(context.Background(), s); !errors.Is(err, ErrInFlight) {
		t.Fatalf("unknown generation automatically retried: %v", err)
	}
	s.Revision = "second-job"
	if _, err := r.Run(context.Background(), s); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("unknown charge reservation released: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("made %d generation calls despite uncertainty", calls.Load())
	}
}

func TestOpenRouterRunnerRouteMismatchSettlesKnownUsageWithoutReuse(t *testing.T) {
	var calls atomic.Int64
	p := newOpenRouterFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, strings.Replace(openRouterResponseFixture, `"provider":"OpenAI"`, `"provider":"Other"`, 1))
	})
	r, _ := testRunner(t)
	r.Provider = p
	s := openRouterRunnerSpec()
	first, err := r.Run(context.Background(), s)
	if !errors.Is(err, ErrIncomplete) || !first.Durable || !first.Measurement.KnownGenerationUsage || first.GenerationUsage.InputTokens != 1000 {
		t.Fatalf("known mismatch must remain charged and incomplete: %+v %v", first, err)
	}
	if _, err := r.Run(context.Background(), s); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("incomplete answer reused or retried: %v", err)
	}
	s.Revision = "second-job"
	if _, err := r.Run(context.Background(), s); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("known usage was not reconciled against the reservation: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("unexpected generation calls: %d", calls.Load())
	}
}

func TestOpenRouterRunnerExactReuseHasZeroNewGeneration(t *testing.T) {
	var calls atomic.Int64
	p := newOpenRouterFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, openRouterResponseFixture)
	})
	r, _ := testRunner(t)
	r.Provider = p
	s := openRouterRunnerSpec()
	first, err := r.Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	reused, err := r.Run(context.Background(), s)
	if err != nil || !reused.Reused || reused.ThisRunUsage != (Usage{}) || reused.Response.Usage != (Usage{}) || reused.AvoidedUsage != first.GenerationUsage || reused.Measurement.GenerationCalled || calls.Load() != 1 {
		t.Fatalf("exact reuse regenerated or charged usage: %+v %v; calls=%d", reused, err, calls.Load())
	}
}
