package inference

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const ollamaDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const ollamaTagsFixture = `{"models":[{"name":"qwen3:4b","model":"qwen3:4b","digest":"` + ollamaDigest + `"}]}`
const ollamaShowFixture = `{"template":"{{ .System }} {{ .Prompt }}","system":"Model default instructions.","capabilities":["completion","thinking"],"thinking":{"values":[false,true],"default":true},"model_info":{"general.architecture":"qwen3","qwen3.context_length":40960}}`
const ollamaGenerateFixture = `{"model":"qwen3:4b","created_at":"2026-10-12T22:00:00Z","response":"Useful local answer.","done":true,"done_reason":"stop","prompt_eval_count":100,"prompt_eval_cached_count":40,"eval_count":20}`

type ollamaTestCalls struct{ tags, show, generate atomic.Int32 }

func ollamaFixtureServer(t *testing.T, tags, show, generation string) (string, *ollamaTestCalls) {
	t.Helper()
	calls := &ollamaTestCalls{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" {
			t.Error("local request sent credentials")
		}
		switch r.URL.Path {
		case "/api/tags":
			calls.tags.Add(1)
			if r.Method != http.MethodGet {
				t.Error("tags must use GET")
			}
			io.WriteString(w, tags)
		case "/api/show":
			calls.show.Add(1)
			if r.Method != http.MethodPost {
				t.Error("show must use POST")
			}
			io.WriteString(w, show)
		case "/api/generate":
			calls.generate.Add(1)
			io.WriteString(w, generation)
		default:
			t.Error("unexpected local endpoint")
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, calls
}

func ollamaFixtureProvider(t *testing.T, generation string) (*OllamaProvider, *ollamaTestCalls) {
	t.Helper()
	baseURL, calls := ollamaFixtureServer(t, ollamaTagsFixture, ollamaShowFixture, generation)
	p, err := NewOllamaProvider(baseURL, "qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.client.CloseIdleConnections)
	return p, calls
}

func ollamaTestRequest() Request {
	return Request{Provider: "ollama", Model: "qwen3:4b", Prefix: "Approved concise memory.", Input: "Draft a handoff.", MaxOutputTokens: 64, ContextWindow: 1024, CacheMode: "default"}
}

func TestOllamaPreflightOnlyReadsMetadataAndGenerationIsBounded(t *testing.T) {
	var generated atomic.Int32
	recorded := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" {
			t.Error("Ollama request includes credentials")
		}
		switch r.URL.Path {
		case "/api/tags":
			io.WriteString(w, ollamaTagsFixture)
		case "/api/show":
			var show map[string]any
			if err := json.NewDecoder(r.Body).Decode(&show); err != nil {
				t.Error(err)
			}
			if !reflect.DeepEqual(show, map[string]any{"model": "qwen3:4b"}) {
				t.Error("show should inspect exactly one installed model")
			}
			io.WriteString(w, ollamaShowFixture)
		case "/api/generate":
			generated.Add(1)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			recorded <- body
			io.WriteString(w, ollamaGenerateFixture)
		default:
			t.Error("unexpected endpoint")
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	p, err := NewOllamaProvider(srv.URL+"/", "qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	defer p.client.CloseIdleConnections()
	if !strings.Contains(p.Identity(), "qwen3:4b@"+ollamaDigest) || !strings.HasPrefix(p.Identity(), "ollama:"+srv.URL) {
		t.Fatal("installed model digest missing from identity")
	}
	if p.client.Timeout != providerTimeout || p.client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("local HTTP safeguards missing")
	}
	req := ollamaTestRequest()
	estimate, err := p.CountInputTokens(context.Background(), req)
	wantEstimate := int64(len(req.Prefix)+len(req.Input)+len("{{ .System }} {{ .Prompt }}")+len("Model default instructions.")) + 512
	if err != nil || estimate != wantEstimate {
		t.Fatalf("estimate = %d, error = %v", estimate, err)
	}
	if generated.Load() != 0 {
		t.Fatal("preflight generated output")
	}
	result, err := p.Generate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.Text != "Useful local answer." || result.Usage != (Usage{InputTokens: 100, UncachedInputTokens: 60, CacheReadTokens: 40, OutputTokens: 20}) {
		t.Fatalf("local result: %+v", result)
	}
	body := <-recorded
	want := map[string]any{"model": "qwen3:4b", "system": req.Prefix, "prompt": req.Input, "stream": false, "think": false, "options": map[string]any{"num_predict": float64(64), "num_ctx": float64(1024)}}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("generation request = %+v", body)
	}
	if generated.Load() != 1 {
		t.Fatal("local generation was repeated")
	}
}

func TestOllamaRejectsRemoteEndpointsAndCloudModelNames(t *testing.T) {
	for _, endpoint := range []string{"http://localhost:11434", "https://127.0.0.1:11434", "https://ollama.com", "http://192.168.1.10:11434", "http://127.0.0.1.example.com:11434", "http://user:secret@127.0.0.1:11434", "http://127.0.0.1:11434/api", "http://127.0.0.1:11434?key=secret", "http://127.0.0.1:11434#secret"} {
		if _, err := NewOllamaProvider(endpoint, "qwen3:4b"); err == nil {
			t.Fatalf("unsafe local endpoint accepted: %s", endpoint)
		} else if strings.Contains(err.Error(), "secret") {
			t.Fatal("endpoint secret leaked")
		}
	}
	endpoint, calls := ollamaFixtureServer(t, ollamaTagsFixture, ollamaShowFixture, ollamaGenerateFixture)
	for _, model := range []string{"qwen3:cloud", "renamed-CLOUD-model", "", "../model", "invalid model"} {
		if _, err := NewOllamaProvider(endpoint, model); err == nil {
			t.Fatalf("invalid/cloud model accepted: %s", model)
		}
	}
	if calls.tags.Load() != 0 || calls.show.Load() != 0 || calls.generate.Load() != 0 {
		t.Fatal("invalid configuration reached Ollama")
	}
}

func TestOllamaRejectsRemoteAndIncompleteInstalledMetadata(t *testing.T) {
	tests := []struct{ name, tags, show string }{
		{"not installed", `{"models":[]}`, ollamaShowFixture},
		{"missing digest", `{"models":[{"name":"qwen3:4b"}]}`, ollamaShowFixture},
		{"invalid digest", `{"models":[{"name":"qwen3:4b","digest":"short"}]}`, ollamaShowFixture},
		{"remote tag", `{"models":[{"name":"qwen3:4b","digest":"` + ollamaDigest + `","remote_host":"https://ollama.com"}]}`, ollamaShowFixture},
		{"nested remote tag", `{"models":[{"name":"qwen3:4b","digest":"` + ollamaDigest + `","details":{"remote_model":"cloud-model"}}]}`, ollamaShowFixture},
		{"remote show", ollamaTagsFixture, `{"remote_model":"hosted-model"}`},
		{"nested remote show", ollamaTagsFixture, `{"details":{"remote_host":"https://ollama.com"}}`},
		{"no completion", ollamaTagsFixture, `{"capabilities":["embedding"]}`},
		{"missing capacity", ollamaTagsFixture, `{"capabilities":["completion"],"model_info":{}}`},
		{"fractional capacity", ollamaTagsFixture, `{"capabilities":["completion"],"model_info":{"general.architecture":"qwen3","qwen3.context_length":10.5}}`},
		{"thinking cannot disable", ollamaTagsFixture, `{"capabilities":["completion"],"thinking":{"values":["high","low"]},"model_info":{"general.architecture":"qwen3","qwen3.context_length":40960}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseURL, calls := ollamaFixtureServer(t, tt.tags, tt.show, ollamaGenerateFixture)
			if _, err := NewOllamaProvider(baseURL, "qwen3:4b"); err == nil {
				t.Fatal("unsupported installed metadata accepted")
			}
			if calls.generate.Load() != 0 {
				t.Fatal("constructor ran inference")
			}
		})
	}
}

func TestOllamaDigestChangesRefuseGenerationAndChangeIdentity(t *testing.T) {
	var changed atomic.Bool
	var generated atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			body := ollamaTagsFixture
			if changed.Load() {
				body = strings.ReplaceAll(body, ollamaDigest, strings.Repeat("b", 64))
			}
			io.WriteString(w, body)
		case "/api/show":
			io.WriteString(w, ollamaShowFixture)
		case "/api/generate":
			generated.Add(1)
			io.WriteString(w, ollamaGenerateFixture)
		}
	}))
	defer srv.Close()
	p1, err := NewOllamaProvider(srv.URL, "qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	defer p1.client.CloseIdleConnections()
	changed.Store(true)
	if _, err = p1.CountInputTokens(context.Background(), ollamaTestRequest()); err == nil {
		t.Fatal("changed model counted under old snapshot")
	}
	if _, err = p1.Generate(context.Background(), ollamaTestRequest()); err == nil {
		t.Fatal("changed model generated under old snapshot")
	}
	if generated.Load() != 0 {
		t.Fatal("stale provider ran inference")
	}
	p2, err := NewOllamaProvider(srv.URL, "qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	defer p2.client.CloseIdleConnections()
	if p1.Identity() == p2.Identity() {
		t.Fatal("changed model reused previous digest identity")
	}
}

func TestOllamaDigestChangedDuringGenerationDoesNotPublishOldSnapshot(t *testing.T) {
	var changed atomic.Bool
	var generated atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			body := ollamaTagsFixture
			if changed.Load() {
				body = strings.ReplaceAll(body, ollamaDigest, strings.Repeat("b", 64))
			}
			io.WriteString(w, body)
		case "/api/show":
			io.WriteString(w, ollamaShowFixture)
		case "/api/generate":
			generated.Add(1)
			changed.Store(true)
			io.WriteString(w, ollamaGenerateFixture)
		}
	}))
	defer srv.Close()
	p, err := NewOllamaProvider(srv.URL, "qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	defer p.client.CloseIdleConnections()
	if _, err = p.Generate(context.Background(), ollamaTestRequest()); err == nil || !strings.Contains(err.Error(), "digest changed") {
		t.Fatalf("model changed during inference was accepted: %v", err)
	}
	if generated.Load() != 1 {
		t.Fatal("local provider regenerated after detecting drift")
	}
}

func TestOllamaValidatesContextAndRequestWithoutInference(t *testing.T) {
	p, calls := ollamaFixtureProvider(t, ollamaGenerateFixture)
	for _, mutate := range []func(*Request){
		func(r *Request) { r.Provider = "claude" }, func(r *Request) { r.Model = "qwen3:8b" },
		func(r *Request) { r.CacheMode = "prefix" }, func(r *Request) { r.ReasoningEffort = "none" },
		func(r *Request) { r.Input = "" }, func(r *Request) { r.Input = string([]byte{0xff}) },
		func(r *Request) { r.MaxOutputTokens = 0 }, func(r *Request) { r.MaxOutputTokens = 1_000_001 },
		func(r *Request) { r.ContextWindow = 0 }, func(r *Request) { r.ContextWindow = 65537 },
		func(r *Request) { r.ContextWindow = 45000 }, // Larger than this model's capacity.
		func(r *Request) { r.ContextWindow = 600 },   // Estimate plus output allowance will not fit.
	} {
		req := ollamaTestRequest()
		mutate(&req)
		if _, err := p.CountInputTokens(context.Background(), req); err == nil {
			t.Fatal("invalid local request counted")
		}
		if _, err := p.Generate(context.Background(), req); err == nil {
			t.Fatal("invalid local request generated")
		}
	}
	if calls.generate.Load() != 0 {
		t.Fatal("invalid local request reached generation")
	}
}

func TestOllamaIncompleteOutputsRemainNonReusable(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"unfinished", func(m map[string]any) { m["done"] = false }},
		{"missing done", func(m map[string]any) { delete(m, "done") }},
		{"length", func(m map[string]any) { m["done_reason"] = "length" }},
		{"unknown stop", func(m map[string]any) { m["done_reason"] = "new_reason" }},
		{"unexpected thinking", func(m map[string]any) { m["thinking"] = "internal thought" }},
		{"tools", func(m map[string]any) { m["tool_calls"] = []any{map[string]any{"name": "shell"}} }},
		{"empty answer", func(m map[string]any) { m["response"] = "" }},
		{"model mismatch", func(m map[string]any) { m["model"] = "other:4b" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, _ := ollamaFixtureProvider(t, mutatedFixture(t, ollamaGenerateFixture, tt.mutate))
			result, err := p.Generate(context.Background(), ollamaTestRequest())
			if err != nil {
				t.Fatal(err)
			}
			if result.Complete {
				t.Fatal("unfinished/unsupported local output was marked complete")
			}
			if result.Usage.InputTokens != 100 || result.Usage.OutputTokens != 20 {
				t.Fatal("incomplete local usage was lost")
			}
		})
	}
}

func TestOllamaRequiresValidReportedUsage(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing input", func(m map[string]any) { delete(m, "prompt_eval_count") }},
		{"missing output", func(m map[string]any) { delete(m, "eval_count") }},
		{"negative input", func(m map[string]any) { m["prompt_eval_count"] = -1 }},
		{"negative output", func(m map[string]any) { m["eval_count"] = -1 }},
		{"negative cache", func(m map[string]any) { m["prompt_eval_cached_count"] = -1 }},
		{"cache exceeds input", func(m map[string]any) { m["prompt_eval_cached_count"] = 101 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, _ := ollamaFixtureProvider(t, mutatedFixture(t, ollamaGenerateFixture, tt.mutate))
			if _, err := p.Generate(context.Background(), ollamaTestRequest()); err == nil {
				t.Fatal("invalid local usage accepted")
			}
		})
	}
	body := mutatedFixture(t, ollamaGenerateFixture, func(m map[string]any) { delete(m, "prompt_eval_cached_count") })
	p, _ := ollamaFixtureProvider(t, body)
	result, err := p.Generate(context.Background(), ollamaTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.CacheReadTokens != 0 || result.Usage.UncachedInputTokens != 100 {
		t.Fatal("optional absent cached count was not treated as zero")
	}
}

func TestOllamaRetainsKnownPolicyOverruns(t *testing.T) {
	for _, tt := range []struct {
		name          string
		mutate        func(map[string]any)
		input, output int64
	}{
		{"output", func(m map[string]any) { m["eval_count"] = 65 }, 100, 65},
		{"context", func(m map[string]any) { m["prompt_eval_count"] = 1010 }, 1010, 20},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, _ := ollamaFixtureProvider(t, mutatedFixture(t, ollamaGenerateFixture, tt.mutate))
			got, err := p.Generate(context.Background(), ollamaTestRequest())
			if err != nil || got.Complete || got.Usage.InputTokens != tt.input || got.Usage.OutputTokens != tt.output || got.StopReason != "usage_limit_exceeded" {
				t.Fatalf("known local overrun lost: %+v %v", got, err)
			}
		})
	}
}

func TestOllamaDoesNotRetryRedirectOrLeakUpstreamErrors(t *testing.T) {
	var forwarded atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL+"/secret")
		w.WriteHeader(http.StatusTemporaryRedirect)
		io.WriteString(w, "private secret")
	}))
	defer srv.Close()
	_, err := NewOllamaProvider(srv.URL, "qwen3:4b")
	if err == nil || strings.Contains(err.Error(), "secret") || forwarded.Load() != 0 {
		t.Fatal("Ollama followed a redirect or leaked upstream error")
	}
	var generated atomic.Int32
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			io.WriteString(w, ollamaTagsFixture)
		case "/api/show":
			io.WriteString(w, ollamaShowFixture)
		case "/api/generate":
			generated.Add(1)
			w.WriteHeader(429)
			io.WriteString(w, "private secret")
		}
	}))
	defer srv2.Close()
	p, err := NewOllamaProvider(srv2.URL, "qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	defer p.client.CloseIdleConnections()
	_, err = p.Generate(context.Background(), ollamaTestRequest())
	if err == nil || strings.Contains(err.Error(), "secret") || generated.Load() != 1 {
		t.Fatal("Ollama retried or leaked upstream error")
	}
}

func TestOllamaHonorsCancellation(t *testing.T) {
	var hold atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hold.Load() {
			io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
			return
		}
		if r.URL.Path == "/api/tags" {
			io.WriteString(w, ollamaTagsFixture)
		} else {
			io.WriteString(w, ollamaShowFixture)
		}
	}))
	defer srv.Close()
	p, err := NewOllamaProvider(srv.URL, "qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	defer p.client.CloseIdleConnections()
	hold.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = p.CountInputTokens(ctx, ollamaTestRequest()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("local preflight cancellation lost: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err = p.Generate(ctx, ollamaTestRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("local generation cancellation lost: %v", err)
	}
	if _, err = NewOllamaProviderContext(ctx, srv.URL, "qwen3:4b"); !errors.Is(err, context.Canceled) {
		t.Fatalf("local discovery cancellation lost: %v", err)
	}
}
