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
	"sync"
	"testing"
)

// Deterministic native-protocol fixtures follow SGLang v0.5.20 (94602c9).
// They do not execute a model, KV transfer kernel, or GPU performance test.
type sglangFixture struct {
	mu         sync.Mutex
	server     *httptest.Server
	model      map[string]any
	info       map[string]any
	tokenize   map[string]any
	generation map[string]any
	paths      []string
	bodies     map[string][]map[string]any
	auth       []string
	hook       func(*sglangFixture, *http.Request)
	failedPath string
	failedCode int
	failedBody string
}

func sglangTestConfig(profile string) SGLangConfig {
	return SGLangConfig{DeploymentID: "weights-sha256-fixture-configuration-1", CacheNamespace: "engineering-fixture", Profile: profile, SelfHosted: true, MaxInputTokens: 128}
}

func sglangTestRequest(profile string) Request {
	cfg := sglangTestConfig(profile)
	return Request{Provider: "sglang", Model: "questlock-example", Prefix: "Stable instructions.", Input: "Summarize this change.", MaxOutputTokens: 16, CacheMode: "default", SGLang: &cfg}
}

func newSGLangFixture(t *testing.T, profile string) *sglangFixture {
	t.Helper()
	f := &sglangFixture{
		model:      map[string]any{"model_path": "/models/pinned", "served_model_name": "questlock-example", "tokenizer_path": "/models/pinned", "is_generation": true, "weight_version": "default"},
		info:       map[string]any{"version": SGLangRuntimeVersion, "startup_time": 12345, "enable_hierarchical_cache": profile == "hicache", "enable_hisparse": profile == "hisparse", "disable_radix_cache": profile == "hisparse", "disaggregation_mode": "null", "context_length": 4096},
		tokenize:   map[string]any{"tokens": []int{1, 2, 3, 7}, "count": 4, "max_model_len": 4096},
		generation: map[string]any{"text": "Checked answer.", "meta_info": map[string]any{"id": "sg-fixture-1", "finish_reason": map[string]any{"type": "stop"}, "prompt_tokens": 4, "completion_tokens": 2, "reasoning_tokens": 0, "cached_tokens": 2, "weight_version": "default", "cached_tokens_details": map[string]any{"device": 1, "host": 1, "storage": 0, "storage_backend": "HiCacheFile"}}},
		bodies:     map[string][]map[string]any{},
	}
	if profile == "hisparse" {
		f.info["disaggregation_mode"] = "decode"
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		if r.Method == http.MethodPost {
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("invalid fixture request JSON")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.bodies[r.URL.Path] = append(f.bodies[r.URL.Path], body)
		}
		if f.hook != nil {
			f.hook(f, r)
		}
		if f.failedPath == r.URL.Path {
			w.WriteHeader(f.failedCode)
			io.WriteString(w, f.failedBody)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/model_info":
			json.NewEncoder(w).Encode(f.model)
		case "/server_info":
			json.NewEncoder(w).Encode(f.info)
		case "/tokenize":
			json.NewEncoder(w).Encode(f.tokenize)
		case "/generate":
			json.NewEncoder(w).Encode(f.generation)
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *sglangFixture) provider(t *testing.T, profile string) *SGLangProvider {
	t.Helper()
	p, err := NewSGLangProviderContext(context.Background(), "", f.server.URL, "questlock-example", sglangTestConfig(profile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.client.CloseIdleConnections)
	return p
}

func (f *sglangFixture) change(fn func(*sglangFixture)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *sglangFixture) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paths)
}

func TestSGLangExactTokenizedRequestAndComputeCache(t *testing.T) {
	f := newSGLangFixture(t, "hicache")
	p := f.provider(t, "hicache")
	req := sglangTestRequest("hicache")
	count, err := p.CountInputTokens(context.Background(), req)
	if err != nil || count != 4 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	got, err := p.Generate(context.Background(), req)
	if err != nil || !got.Complete || got.Text != "Checked answer." || got.Model != req.Model {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	// Runtime cache hits never become provider billing discounts.
	if got.Usage != (Usage{InputTokens: 4, UncachedInputTokens: 4, OutputTokens: 2}) || got.RuntimeCache == nil || *got.RuntimeCache.CachedTokens != 2 || *got.RuntimeCache.HostTokens != 1 || got.RuntimeCache.Evidence != "runtime_reported" {
		t.Fatalf("usage=%+v cache=%+v", got.Usage, got.RuntimeCache)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies["/tokenize"]) != 1 || len(f.bodies["/generate"]) != 1 {
		t.Fatal("counted IDs must be reused for the same exact generation request")
	}
	wantTokenize := map[string]any{"model": req.Model, "messages": []any{map[string]any{"role": "system", "content": req.Prefix}, map[string]any{"role": "user", "content": req.Input}}}
	if !reflect.DeepEqual(f.bodies["/tokenize"][0], wantTokenize) {
		t.Fatalf("tokenize payload=%#v", f.bodies["/tokenize"][0])
	}
	gen := f.bodies["/generate"][0]
	wantGen := map[string]any{"input_ids": []any{float64(1), float64(2), float64(3), float64(7)}, "cache_salt": p.cacheSalt, "stream": false, "sampling_params": map[string]any{"temperature": float64(0), "max_new_tokens": float64(16)}}
	if !reflect.DeepEqual(gen, wantGen) || len(p.cacheSalt) != 64 {
		t.Fatalf("generation must use exact IDs with an explicit namespace: %#v", gen)
	}
	for _, auth := range f.auth {
		if auth != "" {
			t.Fatal("no placeholder authorization may be sent to local SGLang")
		}
	}
}

func TestSGLangInspectionAndPDGenerationBoundary(t *testing.T) {
	for _, profile := range []string{"dense", "hicache", "hisparse"} {
		t.Run(profile, func(t *testing.T) {
			f := newSGLangFixture(t, profile)
			if profile != "hisparse" {
				f.change(func(f *sglangFixture) { f.info["disaggregation_mode"] = "prefill" })
			}
			p := f.provider(t, profile)
			caps := p.Capabilities()
			if !caps.Observed || caps.GenerationSupported || caps.RuntimeVersion != SGLangRuntimeVersion || len(caps.RuntimeIdentitySHA256) != 64 || caps.Reason == "" {
				t.Fatalf("incorrect inspection: %+v", caps)
			}
			before := f.callCount()
			if _, err := p.CountInputTokens(context.Background(), sglangTestRequest(profile)); err == nil {
				t.Fatal("unvalidated PD ingress must fail before tokenization")
			}
			if _, err := p.Generate(context.Background(), sglangTestRequest(profile)); err == nil {
				t.Fatal("direct PD generation must not be attempted")
			}
			if f.callCount() != before {
				t.Fatal("PD generation boundary performed network calls")
			}
		})
	}
}

func TestSGLangConfigAndEndpointValidation(t *testing.T) {
	cfg := sglangTestConfig("dense")
	for name, mutate := range map[string]func(*SGLangConfig){
		"self-hosted":   func(c *SGLangConfig) { c.SelfHosted = false },
		"deployment":    func(c *SGLangConfig) { c.DeploymentID = "" },
		"namespace":     func(c *SGLangConfig) { c.CacheNamespace = "tenant space" },
		"profile":       func(c *SGLangConfig) { c.Profile = "auto" },
		"input-cap":     func(c *SGLangConfig) { c.MaxInputTokens = 0 },
		"excessive-cap": func(c *SGLangConfig) { c.MaxInputTokens = 1_000_001 },
	} {
		t.Run(name, func(t *testing.T) {
			c := cfg
			mutate(&c)
			if ValidateSGLangConfig(c) == nil {
				t.Fatal("accepted invalid deployment declaration")
			}
		})
	}
	for _, endpoint := range []string{"http://localhost:30000", "http://example.com", "ftp://example.com", "https://example.com", "http://127.0.0.1/?key=secret", "http://user:pass@127.0.0.1", "http://127.0.0.1/a/../b", "http://127.0.0.1/#fragment"} {
		if _, err := NewSGLangProviderContext(context.Background(), "", endpoint, "questlock-example", cfg); err == nil {
			t.Errorf("accepted unsafe/uncredentialed endpoint %q", endpoint)
		}
	}
	f := newSGLangFixture(t, "dense")
	for _, key := range []string{"bad\nkey", "space key", strings.Repeat("a", 4097)} {
		if _, err := NewSGLangProviderContext(context.Background(), key, f.server.URL, "questlock-example", cfg); err == nil {
			t.Fatal("accepted invalid explicit credentials")
		}
	}
	if f.callCount() != 0 {
		t.Fatal("static validation performed discovery")
	}
}

func TestSGLangRejectsInvalidReadback(t *testing.T) {
	for name, mutate := range map[string]func(*sglangFixture){
		"version":          func(f *sglangFixture) { f.info["version"] = "0.5.21" },
		"model":            func(f *sglangFixture) { f.model["served_model_name"] = "different-model" },
		"embedding":        func(f *sglangFixture) { f.model["is_generation"] = false },
		"model-path":       func(f *sglangFixture) { delete(f.model, "model_path") },
		"weight-version":   func(f *sglangFixture) { delete(f.model, "weight_version") },
		"startup":          func(f *sglangFixture) { delete(f.info, "startup_time") },
		"startup-zero":     func(f *sglangFixture) { f.info["startup_time"] = 0 },
		"cache-unknown":    func(f *sglangFixture) { delete(f.info, "enable_hierarchical_cache") },
		"profile":          func(f *sglangFixture) { f.info["enable_hierarchical_cache"] = true },
		"mode":             func(f *sglangFixture) { f.info["disaggregation_mode"] = "automatic" },
		"context-fraction": func(f *sglangFixture) { f.info["context_length"] = 5.5 },
		"incompatible-cache": func(f *sglangFixture) {
			f.info["enable_hierarchical_cache"], f.info["enable_hisparse"], f.info["disable_radix_cache"] = true, true, true
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSGLangFixture(t, "dense")
			f.change(mutate)
			if _, err := NewSGLangProviderContext(context.Background(), "", f.server.URL, "questlock-example", sglangTestConfig("dense")); err == nil {
				t.Fatal("invalid readback accepted")
			}
		})
	}
}

func TestSGLangRejectsUnsupportedRequestBeforeNetwork(t *testing.T) {
	f := newSGLangFixture(t, "dense")
	p := f.provider(t, "dense")
	for name, mutate := range map[string]func(*Request){
		"stream":           func(r *Request) { r.Stream = true },
		"reasoning":        func(r *Request) { r.ReasoningEffort = "low" },
		"prefix-mode":      func(r *Request) { r.CacheMode = "prefix" },
		"provider":         func(r *Request) { r.Provider = "openai" },
		"model":            func(r *Request) { r.Model = "other" },
		"no-config":        func(r *Request) { r.SGLang = nil },
		"config-mismatch":  func(r *Request) { r.SGLang.CacheNamespace = "other" },
		"empty-input":      func(r *Request) { r.Input = " " },
		"invalid-utf8":     func(r *Request) { r.Input = string([]byte{255}) },
		"unbounded-output": func(r *Request) { r.MaxOutputTokens = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			r := sglangTestRequest("dense")
			mutate(&r)
			before := f.callCount()
			if _, err := p.CountInputTokens(context.Background(), r); err == nil {
				t.Fatal("unsupported counting accepted")
			}
			if _, err := p.Generate(context.Background(), r); err == nil {
				t.Fatal("unsupported generation accepted")
			}
			if f.callCount() != before {
				t.Fatal("unsupported request reached runtime")
			}
		})
	}
}

func TestSGLangTokenizerBoundsAndContext(t *testing.T) {
	for name, mutate := range map[string]func(*sglangFixture){
		"missing-count":             func(f *sglangFixture) { delete(f.tokenize, "count") },
		"count-mismatch":            func(f *sglangFixture) { f.tokenize["count"] = 3 },
		"token-fraction":            func(f *sglangFixture) { f.tokenize["tokens"] = []float64{1, 2, 3, 7.5} },
		"token-negative":            func(f *sglangFixture) { f.tokenize["tokens"] = []int{1, 2, 3, -1} },
		"batch":                     func(f *sglangFixture) { f.tokenize["tokens"] = [][]int{{1, 2, 3, 7}} },
		"max-model-len":             func(f *sglangFixture) { delete(f.tokenize, "max_model_len") },
		"context-overflow":          func(f *sglangFixture) { f.tokenize["max_model_len"] = 19 },
		"configured-input-overflow": func(f *sglangFixture) { f.tokenize["tokens"], f.tokenize["count"] = make([]int, 129), 129 },
	} {
		t.Run(name, func(t *testing.T) {
			f := newSGLangFixture(t, "dense")
			p := f.provider(t, "dense")
			f.change(mutate)
			if _, err := p.CountInputTokens(context.Background(), sglangTestRequest("dense")); err == nil {
				t.Fatal("invalid tokenization was accepted")
			}
			if _, err := p.Generate(context.Background(), sglangTestRequest("dense")); err == nil {
				t.Fatal("generation accepted invalid tokenization")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.bodies["/generate"]) != 0 {
				t.Fatal("invalid tokenization reached generation")
			}
		})
	}
	t.Run("server-context-override", func(t *testing.T) {
		f := newSGLangFixture(t, "dense")
		f.change(func(f *sglangFixture) { f.info["context_length"] = 19 })
		p := f.provider(t, "dense")
		if _, err := p.CountInputTokens(context.Background(), sglangTestRequest("dense")); err == nil {
			t.Fatal("server context override must constrain tokenizer capacity")
		}
	})
}

func TestSGLangMutationStopsLookupCountingAndGeneration(t *testing.T) {
	for _, stage := range []string{"refresh", "count", "during-count", "generate", "during-generate"} {
		t.Run(stage, func(t *testing.T) {
			f := newSGLangFixture(t, "dense")
			p := f.provider(t, "dense")
			f.change(func(f *sglangFixture) {
				if strings.HasPrefix(stage, "during-") {
					path := "/tokenize"
					if stage == "during-generate" {
						path = "/generate"
					}
					f.hook = func(f *sglangFixture, r *http.Request) {
						if r.URL.Path == path {
							f.model["weight_version"] = "new-version"
						}
					}
				} else {
					f.info["startup_time"] = 67890
				}
			})
			var err error
			switch stage {
			case "refresh":
				err = p.RefreshIdentity(context.Background())
			case "count", "during-count":
				_, err = p.CountInputTokens(context.Background(), sglangTestRequest("dense"))
			default:
				var answer Response
				answer, err = p.Generate(context.Background(), sglangTestRequest("dense"))
				if answer.Text != "" || answer.Complete {
					t.Fatal("mutated deployment must withhold the answer")
				}
			}
			if err == nil {
				t.Fatal("runtime identity mutation was ignored")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			want := 0
			if stage == "during-generate" {
				want = 1
			}
			if len(f.bodies["/generate"]) != want {
				t.Fatal("identity failure retried or called generation unexpectedly")
			}
		})
	}
}

func TestSGLangIdentityAndNamespaceExcludeSecrets(t *testing.T) {
	f := newSGLangFixture(t, "dense")
	p := f.provider(t, "dense")
	f.change(func(f *sglangFixture) {
		f.info["api_key"], f.info["admin_api_key"], f.info["launch_command"] = "rotated-provider-secret", "admin-secret", "secret-command"
		f.info["internal_states"] = []any{map[string]any{"num_requests": 3}}
	})
	if err := p.RefreshIdentity(context.Background()); err != nil {
		t.Fatal("secret rotation and load counters must not enter request identity", err)
	}
	other, err := NewSGLangProviderContext(context.Background(), "different-key", f.server.URL, "questlock-example", sglangTestConfig("dense"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.client.CloseIdleConnections)
	if other.Identity() != p.Identity() || other.cacheSalt != p.cacheSalt {
		t.Fatal("authorization key affected task/cache identity")
	}
	for _, field := range []string{"namespace", "deployment"} {
		cfg := sglangTestConfig("dense")
		if field == "namespace" {
			cfg.CacheNamespace = "different-tenant"
		} else {
			cfg.DeploymentID = "new-pinned-model"
		}
		other, err := NewSGLangProviderContext(context.Background(), "", f.server.URL, "questlock-example", cfg)
		if err != nil {
			t.Fatal(err)
		}
		other.client.CloseIdleConnections()
		if other.Identity() == p.Identity() || other.cacheSalt == p.cacheSalt {
			t.Fatal("deployment/namespace must isolate request and KV identity")
		}
	}
	serialized, _ := json.Marshal(p.Capabilities())
	for _, secret := range []string{"secret", f.server.URL, "/models/", "Stable instructions"} {
		if strings.Contains(p.Identity()+string(serialized), secret) {
			t.Fatal("identity/capabilities exposed a private readback")
		}
	}
}

func TestSGLangRejectsInvalidGenerationMetadata(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"missing-usage":    func(m map[string]any) { delete(m, "completion_tokens") },
		"prompt-mismatch":  func(m map[string]any) { m["prompt_tokens"] = 3 },
		"negative-output":  func(m map[string]any) { m["completion_tokens"] = -1 },
		"reasoning-subset": func(m map[string]any) { m["reasoning_tokens"] = 3 },
		"no-finish":        func(m map[string]any) { delete(m, "finish_reason") },
		"weight-version":   func(m map[string]any) { m["weight_version"] = "unrelated" },
		"mixed-weights": func(m map[string]any) {
			m["weight_versions"] = []any{map[string]any{"version": "other", "start": 0, "end": 2}}
		},
		"weight-range": func(m map[string]any) {
			m["weight_versions"] = []any{map[string]any{"version": "default", "start": 0, "end": 3}}
		},
		"negative-cache":   func(m map[string]any) { m["cached_tokens"] = -1 },
		"cache-over-input": func(m map[string]any) { m["cached_tokens"] = 5 },
		"tier-negative":    func(m map[string]any) { m["cached_tokens_details"].(map[string]any)["host"] = -1 },
		"tier-sum":         func(m map[string]any) { m["cached_tokens_details"].(map[string]any)["host"] = 3 },
		"tier-disagrees":   func(m map[string]any) { m["cached_tokens_details"].(map[string]any)["storage"] = 1 },
		"backend-private": func(m map[string]any) {
			m["cached_tokens_details"].(map[string]any)["storage_backend"] = "https://secret@example.com"
		},
		"id-control": func(m map[string]any) { m["id"] = "bad\nrequest" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newSGLangFixture(t, "dense")
			p := f.provider(t, "dense")
			f.change(func(f *sglangFixture) { mutate(f.generation["meta_info"].(map[string]any)) })
			answer, err := p.Generate(context.Background(), sglangTestRequest("dense"))
			if err == nil || answer.Text != "" || answer.Complete {
				t.Fatalf("unvalidated answer escaped: %+v err=%v", answer, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.bodies["/generate"]) != 1 {
				t.Fatal("uncertain response must not trigger generation retry")
			}
		})
	}
}

func TestSGLangKnownIncompleteAndUnknownCache(t *testing.T) {
	for _, reason := range []string{"length", "abort", "unexpected"} {
		t.Run(reason, func(t *testing.T) {
			f := newSGLangFixture(t, "dense")
			p := f.provider(t, "dense")
			f.change(func(f *sglangFixture) {
				f.generation["meta_info"].(map[string]any)["finish_reason"] = map[string]any{"type": reason, "message": "private upstream error"}
			})
			got, err := p.Generate(context.Background(), sglangTestRequest("dense"))
			if err != nil || got.Complete || got.Usage.OutputTokens != 2 || strings.Contains(got.StopReason, "private") {
				t.Fatalf("known incomplete accounting=%+v err=%v", got, err)
			}
		})
	}
	t.Run("known-overrun", func(t *testing.T) {
		f := newSGLangFixture(t, "dense")
		p := f.provider(t, "dense")
		f.change(func(f *sglangFixture) { f.generation["meta_info"].(map[string]any)["completion_tokens"] = 17 })
		got, err := p.Generate(context.Background(), sglangTestRequest("dense"))
		if err != nil || got.Complete || got.StopReason != "usage_limit_exceeded" || got.Usage.OutputTokens != 17 {
			t.Fatalf("overrun was hidden: %+v err=%v", got, err)
		}
	})
	for _, knownZero := range []bool{false, true} {
		t.Run(fmt.Sprint("cache-zero-", knownZero), func(t *testing.T) {
			f := newSGLangFixture(t, "dense")
			p := f.provider(t, "dense")
			f.change(func(f *sglangFixture) {
				m := f.generation["meta_info"].(map[string]any)
				delete(m, "cached_tokens_details")
				if knownZero {
					m["cached_tokens"] = 0
				} else {
					delete(m, "cached_tokens")
				}
			})
			got, err := p.Generate(context.Background(), sglangTestRequest("dense"))
			if err != nil || !got.Complete || got.RuntimeCache == nil {
				t.Fatalf("valid missing cache metadata=%+v %v", got, err)
			}
			if (got.RuntimeCache.CachedTokens != nil) != knownZero || got.RuntimeCache.DeviceTokens != nil || got.RuntimeCache.HostTokens != nil || got.RuntimeCache.StorageTokens != nil || got.Usage.CacheReadTokens != 0 {
				t.Fatal("unknown cache tiers or cache billing must not be invented")
			}
		})
	}
}

func TestSGLangErrorsAreRedactedAndNotRetried(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := newSGLangFixture(t, "dense")
			p := f.provider(t, "dense")
			f.change(func(f *sglangFixture) {
				f.failedPath, f.failedCode, f.failedBody = "/generate", status, "private prompt provider-key https://secret.example"
			})
			_, err := p.Generate(context.Background(), sglangTestRequest("dense"))
			if err == nil || !strings.Contains(err.Error(), fmt.Sprint(status)) || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), f.server.URL) {
				t.Fatalf("error must be redacted: %v", err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.bodies["/generate"]) != 1 {
				t.Fatal("provider failure retried generation")
			}
		})
	}
	t.Run("cancelled-discovery", func(t *testing.T) {
		f := newSGLangFixture(t, "dense")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := NewSGLangProviderContext(ctx, "", f.server.URL, "questlock-example", sglangTestConfig("dense"))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	})
	t.Run("oversized-generation", func(t *testing.T) {
		f := newSGLangFixture(t, "dense")
		p := f.provider(t, "dense")
		f.change(func(f *sglangFixture) {
			f.failedPath, f.failedCode, f.failedBody = "/generate", 200, strings.Repeat("x", maxProviderResponseBytes+1)
		})
		if _, err := p.Generate(context.Background(), sglangTestRequest("dense")); err == nil {
			t.Fatal("unbounded response accepted")
		}
	})
}

func TestSGLangConcurrentRequestsKeepTheirExactInputIDs(t *testing.T) {
	f := newSGLangFixture(t, "dense")
	p := f.provider(t, "dense")
	f.change(func(f *sglangFixture) {
		f.hook = func(f *sglangFixture, r *http.Request) {
			switch r.URL.Path {
			case "/tokenize":
				requests := f.bodies["/tokenize"]
				messages := requests[len(requests)-1]["messages"].([]any)
				input := messages[len(messages)-1].(map[string]any)["content"].(string)
				if input == "task-b" {
					f.tokenize["tokens"] = []int{8, 9, 10, 11}
				} else {
					f.tokenize["tokens"] = []int{1, 2, 3, 7}
				}
			case "/generate":
				requests := f.bodies["/generate"]
				tokens := requests[len(requests)-1]["input_ids"].([]any)
				if tokens[0].(float64) == 8 {
					f.generation["text"] = "answer-b"
				} else {
					f.generation["text"] = "answer-a"
				}
			}
		}
	})
	var counted sync.WaitGroup
	counted.Add(2)
	var done sync.WaitGroup
	for _, suffix := range []string{"a", "b"} {
		done.Add(1)
		go func(suffix string) {
			defer done.Done()
			req := sglangTestRequest("dense")
			req.Input = "task-" + suffix
			_, countErr := p.CountInputTokens(context.Background(), req)
			counted.Done()
			counted.Wait()
			if countErr != nil {
				t.Error(countErr)
				return
			}
			answer, err := p.Generate(context.Background(), req)
			if err != nil || !answer.Complete || answer.Text != "answer-"+suffix {
				t.Errorf("request %s used another request's counted IDs: %+v err=%v", suffix, answer, err)
			}
		}(suffix)
	}
	done.Wait()
}
