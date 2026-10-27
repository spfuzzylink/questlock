package semantic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const fixtureEmbeddingReply = `{"model":"fixture:local","embeddings":[[0.8,0.6]],"prompt_eval_count":4}`

type embeddingFixture struct {
	server   *httptest.Server
	embedded atomic.Int32
	digest   atomic.Value
	version  atomic.Value
}

func newEmbeddingFixture(t *testing.T, override func(http.ResponseWriter, *http.Request, *embeddingFixture) bool) *embeddingFixture {
	t.Helper()
	f := &embeddingFixture{}
	f.digest.Store(strings.Repeat("a", 64))
	f.version.Store("0.11.0-fixture")
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("embedding adapter sent credentials")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/embed" {
			f.embedded.Add(1)
			var request map[string]json.RawMessage
			if json.NewDecoder(r.Body).Decode(&request) != nil || string(request["truncate"]) != "false" || string(request["model"]) != `"fixture:local"` || request["input"] == nil {
				t.Error("embedding request must contain one input and explicitly disable truncation")
			}
		}
		if override != nil && override(w, r, f) {
			return
		}
		switch r.URL.Path {
		case "/api/version":
			fmt.Fprintf(w, `{"version":%q}`, f.version.Load())
		case "/api/tags":
			fmt.Fprintf(w, `{"models":[{"name":"fixture:local","model":"fixture:local","digest":%q}]}`, f.digest.Load())
		case "/api/show":
			io.WriteString(w, `{"capabilities":["embedding"]}`)
		case "/api/embed":
			io.WriteString(w, fixtureEmbeddingReply)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func fixtureEmbedder(t *testing.T, f *embeddingFixture) *OllamaEmbedder {
	t.Helper()
	before := f.embedded.Load()
	p, err := NewOllamaEmbedder(f.server.URL, "fixture:local", 8192)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if f.embedded.Load() != before {
		t.Fatal("discovery generated an embedding")
	}
	return p
}

func TestOllamaEmbeddingPinsLocalIdentityAndOptionalUsage(t *testing.T) {
	f := newEmbeddingFixture(t, nil)
	p := fixtureEmbedder(t, f)
	got, err := p.Embed(context.Background(), "A synthetic bounded question.")
	if err != nil || !validHash(got.ModelID) || len(got.Vector) != 2 || got.InputTokens == nil || *got.InputTokens != 4 || got.DurationMS < 0 || f.embedded.Load() != 1 {
		t.Fatalf("embedding = %+v, %v", got, err)
	}
	other := newEmbeddingFixture(t, func(w http.ResponseWriter, r *http.Request, _ *embeddingFixture) bool {
		if r.URL.Path == "/api/embed" {
			io.WriteString(w, `{"model":"fixture:local","embeddings":[[1,0]]}`)
			return true
		}
		return false
	})
	p2 := fixtureEmbedder(t, other)
	unknown, err := p2.Embed(context.Background(), "Same local weights at another origin.")
	if err != nil || unknown.ModelID != got.ModelID || unknown.InputTokens != nil {
		t.Fatalf("cross-worker identity or unreported usage = %+v, %v", unknown, err)
	}
	other.version.Store("0.12.0-fixture")
	p3 := fixtureEmbedder(t, other)
	if p3.modelID == got.ModelID {
		t.Fatal("runtime version not included in embedding identity")
	}
}

func TestOllamaEmbeddingRefusesDigestAndRuntimeDrift(t *testing.T) {
	for _, phase := range []string{"before", "during"} {
		for _, kind := range []string{"digest", "runtime"} {
			t.Run(phase+"/"+kind, func(t *testing.T) {
				mutate := func(f *embeddingFixture) {
					if kind == "digest" {
						f.digest.Store(strings.Repeat("b", 64))
					} else {
						f.version.Store("0.12.0-fixture")
					}
				}
				f := newEmbeddingFixture(t, func(_ http.ResponseWriter, r *http.Request, f *embeddingFixture) bool {
					if phase == "during" && r.URL.Path == "/api/embed" {
						mutate(f)
					}
					return false
				})
				p := fixtureEmbedder(t, f)
				if phase == "before" {
					mutate(f)
				}
				got, err := p.Embed(context.Background(), "Synthetic request.")
				if err == nil || got.Vector != nil || got.InputTokens != nil || phase == "before" && f.embedded.Load() != 0 {
					t.Fatalf("changed snapshot accepted: %+v, %v", got, err)
				}
			})
		}
	}
}

func TestOllamaEmbeddingRejectsMalformedTruncatedAndOversizeReplies(t *testing.T) {
	for name, reply := range map[string]string{
		"missing vector":   `{"model":"fixture:local"}`,
		"multiple vectors": `{"model":"fixture:local","embeddings":[[1],[2]]}`,
		"zero vector":      `{"model":"fixture:local","embeddings":[[0,0]]}`,
		"overflow":         `{"model":"fixture:local","embeddings":[[1e100]]}`,
		"NaN":              `{"model":"fixture:local","embeddings":[[NaN]]}`,
		"wrong model":      `{"model":"other:local","embeddings":[[1]]}`,
		"zero usage":       `{"model":"fixture:local","embeddings":[[1]],"prompt_eval_count":0}`,
		"negative usage":   `{"model":"fixture:local","embeddings":[[1]],"prompt_eval_count":-1}`,
		"fractional usage": `{"model":"fixture:local","embeddings":[[1]],"prompt_eval_count":1.5}`,
		"truncated":        `{"model":"fixture:local","embeddings":[[1]],"truncated":true}`,
		"error payload":    `{"error":"private synthetic upstream content"}`,
		"duplicate fields": `{"model":"fixture:local","embeddings":[[1]],"model":"other:local"}`,
		"trailing JSON":    fixtureEmbeddingReply + `{}`,
		"remote reply":     `{"model":"fixture:local","embeddings":[[1]],"remote_host":"https://example.invalid"}`,
		"large vector":     `{"model":"fixture:local","embeddings":[[` + strings.Repeat("1,", 4096) + `1]]}`,
		"large body":       `{"padding":"` + strings.Repeat("x", maxEmbeddingReplyBytes) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newEmbeddingFixture(t, func(w http.ResponseWriter, r *http.Request, _ *embeddingFixture) bool {
				if r.URL.Path == "/api/embed" {
					io.WriteString(w, reply)
					return true
				}
				return false
			})
			p := fixtureEmbedder(t, f)
			got, err := p.Embed(context.Background(), "Synthetic request.")
			if err == nil || got.Vector != nil || got.InputTokens != nil || strings.Contains(err.Error(), "private synthetic") {
				t.Fatalf("bad reply accepted or exposed: %+v, %v", got, err)
			}
		})
	}
}

func TestOllamaEmbeddingLocalCloudProxyAndRedirectPolicy(t *testing.T) {
	for _, origin := range []string{"https://127.0.0.1:11434", "http://localhost:11434", "http://example.invalid", "http://192.0.2.1", "http://user:password@127.0.0.1", "http://127.0.0.1/path", "http://127.0.0.1?query=secret", "http://127.0.0.1#secret", "http://127.0.0.1:0", "http://127.0.0.1:65536", "http://127.0.0.1:"} {
		if _, err := NewOllamaEmbedder(origin, "fixture:local", 8192); err == nil || strings.Contains(err.Error(), origin) {
			t.Fatalf("unsafe origin accepted or reflected: %s, %v", origin, err)
		}
	}
	for _, model := range []string{"fixture:cloud", "fixture-cloud:latest", "fixture\nprivate", "https://example.invalid/fixture", "../fixture", ""} {
		if _, err := NewOllamaEmbedder("http://127.0.0.1:1", model, 8192); err == nil || model != "" && strings.Contains(err.Error(), model) {
			t.Fatalf("unsafe model accepted or reflected: %q, %v", model, err)
		}
	}
	var proxyCalls, redirectCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyCalls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(proxy.Close)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	f := newEmbeddingFixture(t, nil)
	p := fixtureEmbedder(t, f)
	if transport, ok := p.client.Transport.(*http.Transport); !ok || transport.Proxy != nil {
		t.Fatal("embedding adapter must disable environment proxy routing")
	}
	if _, err := p.Embed(context.Background(), "Synthetic request."); err != nil || proxyCalls.Load() != 0 {
		t.Fatalf("proxy contacted: %d, %v", proxyCalls.Load(), err)
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectCalls.Add(1)
		io.WriteString(w, fixtureEmbeddingReply)
	}))
	t.Cleanup(target.Close)
	redirect := newEmbeddingFixture(t, func(w http.ResponseWriter, r *http.Request, _ *embeddingFixture) bool {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
		return true
	})
	if _, err := NewOllamaEmbedder(redirect.server.URL, "fixture:local", 8192); err == nil || redirectCalls.Load() != 0 {
		t.Fatalf("discovery redirect followed: %v", err)
	}
	redirectEmbed := newEmbeddingFixture(t, func(w http.ResponseWriter, r *http.Request, _ *embeddingFixture) bool {
		if r.URL.Path == "/api/embed" {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			return true
		}
		return false
	})
	if _, err := fixtureEmbedder(t, redirectEmbed).Embed(context.Background(), "Synthetic request."); err == nil || redirectCalls.Load() != 0 {
		t.Fatalf("embedding redirect followed: %v", err)
	}
}

func TestOllamaEmbeddingDiscoveryRefusesCloudAndMissingCapability(t *testing.T) {
	for name, routeReply := range map[string][2]string{
		"cloud show":         {"/api/show", `{"capabilities":["embedding"],"remote_host":"https://example.invalid"}`},
		"cloud tags":         {"/api/tags", `{"models":[{"name":"fixture:local","digest":"` + strings.Repeat("a", 64) + `","remote_model":"remote:cloud"}]}`},
		"no embeddings":      {"/api/show", `{"capabilities":["completion"]}`},
		"invalid capability": {"/api/show", `{"capabilities":"embedding"}`},
		"missing version":    {"/api/version", `{}`},
		"missing digest":     {"/api/tags", `{"models":[{"name":"fixture:local"}]}`},
		"missing model":      {"/api/tags", `{"models":[]}`},
		"ambiguous model":    {"/api/tags", `{"models":[{"name":"fixture:local","digest":"` + strings.Repeat("a", 64) + `"},{"name":"fixture:local","digest":"` + strings.Repeat("b", 64) + `"}]}`},
	} {
		t.Run(name, func(t *testing.T) {
			f := newEmbeddingFixture(t, func(w http.ResponseWriter, r *http.Request, _ *embeddingFixture) bool {
				if r.URL.Path == routeReply[0] {
					io.WriteString(w, routeReply[1])
					return true
				}
				return false
			})
			if _, err := NewOllamaEmbedder(f.server.URL, "fixture:local", 8192); err == nil || f.embedded.Load() != 0 {
				t.Fatalf("unsafe metadata accepted: %v", err)
			}
		})
	}
}

func TestOllamaEmbeddingInputLimitsAndCancellation(t *testing.T) {
	f := newEmbeddingFixture(t, nil)
	p := fixtureEmbedder(t, f)
	for _, input := range []string{"", " \n", string([]byte{0xff}), strings.Repeat("x", 8193)} {
		if _, err := p.Embed(context.Background(), input); err == nil || f.embedded.Load() != 0 {
			t.Fatal("invalid input reached embedding")
		}
	}
	for _, limit := range []int{-1, 32769} {
		if _, err := NewOllamaEmbedder(f.server.URL, "fixture:local", limit); err == nil {
			t.Fatal("invalid input limit accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Embed(ctx, "Synthetic request."); !errors.Is(err, context.Canceled) || f.embedded.Load() != 0 {
		t.Fatalf("pre-cancellation ignored: %v", err)
	}
	if _, err := NewOllamaEmbedderContext(ctx, f.server.URL, "fixture:local", 8192); !errors.Is(err, context.Canceled) {
		t.Fatalf("discovery cancellation ignored: %v", err)
	}
	started := make(chan struct{})
	stalled := newEmbeddingFixture(t, func(w http.ResponseWriter, r *http.Request, _ *embeddingFixture) bool {
		if r.URL.Path == "/api/embed" {
			close(started)
			<-r.Context().Done()
			return true
		}
		return false
	})
	p2 := fixtureEmbedder(t, stalled)
	ctx, cancel = context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := p2.Embed(ctx, "Synthetic request."); done <- err }()
	select {
	case <-started:
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("embedding did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("embedding cancellation ignored: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("embedding cancellation stalled")
	}
}

func TestOllamaEmbeddingDeadlineBoundsStalledRuntime(t *testing.T) {
	f := newEmbeddingFixture(t, func(_ http.ResponseWriter, r *http.Request, _ *embeddingFixture) bool {
		if r.URL.Path == "/api/embed" {
			<-r.Context().Done()
			return true
		}
		return false
	})
	p := fixtureEmbedder(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	got, err := p.Embed(ctx, "Synthetic request.")
	if !errors.Is(err, context.DeadlineExceeded) || got.Vector != nil || got.InputTokens != nil || time.Since(start) > time.Second {
		t.Fatalf("runtime deadline not honored: %+v, %v", got, err)
	}
}

func TestOllamaEmbeddingAcceptsExactLimitsAndIgnoresUnselectedCloudAlias(t *testing.T) {
	f := newEmbeddingFixture(t, func(w http.ResponseWriter, r *http.Request, _ *embeddingFixture) bool {
		switch r.URL.Path {
		case "/api/tags":
			io.WriteString(w, `{"models":[{"name":"fixture:local","digest":"`+strings.Repeat("a", 64)+`"},{"name":"unselected:cloud","remote_host":"https://example.invalid"}]}`)
			return true
		case "/api/embed":
			io.WriteString(w, `{"model":"fixture:local","embeddings":[[`+strings.Repeat("1,", 4095)+`1]],"prompt_eval_count":null}`)
			return true
		}
		return false
	})
	p, err := NewOllamaEmbedder(f.server.URL, "fixture:local", MaxInputBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	got, err := p.Embed(context.Background(), strings.Repeat("x", MaxInputBytes))
	if err != nil || len(got.Vector) != MaxDimensions || got.InputTokens != nil {
		t.Fatalf("exact supported limits refused: %d, %v", len(got.Vector), err)
	}
}
