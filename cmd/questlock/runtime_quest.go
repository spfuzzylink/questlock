package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spfuzzylink/questlock/inference"
)

// runtime-quest runs the real adapters against deterministic native HTTP
// fixtures and a real broker. It does not emulate GPU execution or predict time.
func modelRuntimeQuest(output io.Writer) error {
	if _, err := fmt.Fprintln(output, "RUNTIME QUEST — protocol_fixture\nReal HTTP adapters + temporary SQLite; no model weights, predicted timing, GPU, or provider key."); err != nil {
		return err
	}
	for _, scenario := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"simple 1: dense native generation", runtimeQuestDense},
		{"simple 2: exact result reuse", runtimeQuestReuse},
		{"simple 3: separate cache namespaces", runtimeQuestNamespaces},
		{"complex 1: HiCache tier evidence versus billing", runtimeQuestTiers},
		{"complex 2: runtime change blocks stale reuse", runtimeQuestMutation},
		{"complex 3: unknown cache evidence and sparse boundary", runtimeQuestUnknown},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := scenario.run(ctx)
		cancel()
		if err != nil {
			return fmt.Errorf("%s: %w", scenario.name, err)
		}
		if _, err = fmt.Fprintln(output, "PASS  "+scenario.name); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(output, "QUEST CLEARED — six protocol scenarios passed. GPU performance and simulated latency were not measured.")
	return err
}

type runtimeQuestServer struct {
	server  *httptest.Server
	profile string
	unknown bool
	startup atomic.Int64
	calls   atomic.Int64
	mu      sync.Mutex
	salts   []string
}

func newRuntimeQuestServer(profile string, unknown bool) *runtimeQuestServer {
	f := &runtimeQuestServer{profile: profile, unknown: unknown}
	f.startup.Store(1)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var value any
		switch r.Method + " " + r.URL.Path {
		case "GET /model_info":
			value = map[string]any{"model_path": "/models/pinned", "served_model_name": "questlock-example", "tokenizer_path": "/models/pinned", "is_generation": true, "weight_version": "default"}
		case "GET /server_info":
			mode := "null"
			if profile == "hisparse" {
				mode = "decode"
			}
			value = map[string]any{"version": "0.5.20", "startup_time": f.startup.Load(), "enable_hierarchical_cache": profile == "hicache", "enable_hisparse": profile == "hisparse", "disable_radix_cache": profile == "hisparse", "disaggregation_mode": mode, "context_length": 4096}
		case "POST /tokenize":
			value = map[string]any{"tokens": []int64{101, 102, 103}, "count": 3, "max_model_len": 4096}
		case "POST /generate":
			var payload struct {
				InputIDs  []int64 `json:"input_ids"`
				CacheSalt string  `json:"cache_salt"`
			}
			if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload) != nil || !reflect.DeepEqual(payload.InputIDs, []int64{101, 102, 103}) || len(payload.CacheSalt) != 64 {
				http.Error(w, "invalid fixture request", 400)
				return
			}
			f.calls.Add(1)
			f.mu.Lock()
			f.salts = append(f.salts, payload.CacheSalt)
			f.mu.Unlock()
			meta := map[string]any{"id": "fixture-generation", "finish_reason": map[string]any{"type": "stop"}, "prompt_tokens": 3, "completion_tokens": 2, "weight_version": "default"}
			if !unknown {
				meta["cached_tokens"] = 0
				if profile == "hicache" {
					meta["cached_tokens"] = 2
					meta["cached_tokens_details"] = map[string]any{"device": 1, "host": 1, "storage": 0, "storage_backend": "file"}
				}
			}
			value = map[string]any{"text": "Checked fixture answer.", "meta_info": meta}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(value)
	}))
	return f
}

func runtimeQuestSpec(profile, namespace string) inference.Spec {
	return inference.Spec{Task: "runtime-quest", Revision: "fixture-1", Provider: "sglang", Model: "questlock-example", Instructions: "Return the supplied fixture answer.", Input: "Approved fixture task.", MaxInputTokens: 1024, MaxOutputTokens: 64, ReuseForSeconds: 3600, SGLang: &inference.SGLangConfig{DeploymentID: "pinned-fixture-1", CacheNamespace: namespace, Profile: profile, SelfHosted: true, MaxInputTokens: 1024}}
}

func runtimeQuestRun(ctx context.Context, profile string, unknown bool, run func(*modelQuestFixture, *runtimeQuestServer, inference.Spec, *inference.SGLangProvider) error) error {
	broker, err := newModelQuestFixture(ctx)
	if err != nil {
		return err
	}
	defer broker.close()
	server := newRuntimeQuestServer(profile, unknown)
	defer server.server.Close()
	spec := runtimeQuestSpec(profile, "fixture-scope-a")
	provider, err := inference.NewSGLangProviderContext(ctx, "", server.server.URL, spec.Model, *spec.SGLang)
	if err != nil {
		return err
	}
	return run(broker, server, spec, provider)
}

func runtimeQuestDense(ctx context.Context) error {
	return runtimeQuestRun(ctx, "dense", false, func(b *modelQuestFixture, s *runtimeQuestServer, spec inference.Spec, p *inference.SGLangProvider) error {
		r, err := b.runner(0, p).Run(ctx, spec)
		if err != nil {
			return err
		}
		if !r.Durable || !r.Response.Complete || r.ThisRunUsage.InputTokens != 3 || r.ThisRunUsage.OutputTokens != 2 || s.calls.Load() != 1 {
			return errors.New("native request did not complete once")
		}
		return nil
	})
}
func runtimeQuestReuse(ctx context.Context) error {
	return runtimeQuestRun(ctx, "dense", false, func(b *modelQuestFixture, s *runtimeQuestServer, spec inference.Spec, p *inference.SGLangProvider) error {
		if _, err := b.runner(0, p).Run(ctx, spec); err != nil {
			return err
		}
		r, err := b.runner(1, p).Run(ctx, spec)
		if err != nil {
			return err
		}
		if !r.Reused || r.ThisRunUsage != (inference.Usage{}) || r.Response.RuntimeCache != nil || r.GenerationRuntimeCache == nil || s.calls.Load() != 1 {
			return errors.New("reuse created generation or current cache evidence")
		}
		return nil
	})
}
func runtimeQuestNamespaces(ctx context.Context) error {
	return runtimeQuestRun(ctx, "dense", false, func(b *modelQuestFixture, s *runtimeQuestServer, spec inference.Spec, p *inference.SGLangProvider) error {
		a, err := b.runner(0, p).Run(ctx, spec)
		if err != nil {
			return err
		}
		cfg := *spec.SGLang
		cfg.CacheNamespace = "fixture-scope-b"
		spec.SGLang = &cfg
		p2, err := inference.NewSGLangProviderContext(ctx, "", s.server.URL, spec.Model, cfg)
		if err != nil {
			return err
		}
		z, err := b.runner(1, p2).Run(ctx, spec)
		if err != nil {
			return err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if z.Reused || a.Fingerprint == z.Fingerprint || len(s.salts) != 2 || s.salts[0] == s.salts[1] {
			return errors.New("cache namespaces were merged")
		}
		return nil
	})
}
func runtimeQuestTiers(ctx context.Context) error {
	return runtimeQuestRun(ctx, "hicache", false, func(b *modelQuestFixture, s *runtimeQuestServer, spec inference.Spec, p *inference.SGLangProvider) error {
		r, err := b.runner(0, p).Run(ctx, spec)
		if err != nil {
			return err
		}
		c := r.Response.RuntimeCache
		if c == nil || c.CachedTokens == nil || *c.CachedTokens != 2 || c.HostTokens == nil || *c.HostTokens != 1 || r.ThisRunUsage.CacheReadTokens != 0 || r.CostUSD != nil || r.AvoidedCostUSD != nil {
			return errors.New("compute cache evidence was lost or counted as billing savings")
		}
		return nil
	})
}
func runtimeQuestMutation(ctx context.Context) error {
	return runtimeQuestRun(ctx, "dense", false, func(b *modelQuestFixture, s *runtimeQuestServer, spec inference.Spec, p *inference.SGLangProvider) error {
		if _, err := b.runner(0, p).Run(ctx, spec); err != nil {
			return err
		}
		s.startup.Add(1)
		r, err := b.runner(1, p).Run(ctx, spec)
		if err == nil || r.Reused || s.calls.Load() != 1 {
			return errors.New("changed runtime identity allowed stale reuse")
		}
		return nil
	})
}
func runtimeQuestUnknown(ctx context.Context) error {
	err := runtimeQuestRun(ctx, "dense", true, func(b *modelQuestFixture, s *runtimeQuestServer, spec inference.Spec, p *inference.SGLangProvider) error {
		r, err := b.runner(0, p).Run(ctx, spec)
		if err != nil {
			return err
		}
		c := r.Response.RuntimeCache
		if c == nil || c.CachedTokens != nil || c.HostTokens != nil {
			return errors.New("unreported cache counter became zero")
		}
		return nil
	})
	if err != nil {
		return err
	}
	return runtimeQuestRun(ctx, "hisparse", false, func(b *modelQuestFixture, s *runtimeQuestServer, spec inference.Spec, p *inference.SGLangProvider) error {
		if p.Capabilities().GenerationSupported {
			return errors.New("direct sparse decode advertised generation")
		}
		_, err := b.runner(0, p).Run(ctx, spec)
		if err == nil || s.calls.Load() != 0 {
			return errors.New("unsupported sparse decode generated")
		}
		return nil
	})
}
