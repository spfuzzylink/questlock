package inference

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func ollamaGenerationTimeout(value int64) *int64 { return &value }

func TestOllamaGenerationTimeoutValidationAndIdentity(t *testing.T) {
	s := ollamaOptionsSpec()
	baseline, err := BuildPlan(s)
	if err != nil || baseline.Fingerprint != "8432881ebf244e0bd4ae8e03eacaacdc946a9bbb30f278585ddcb2ed3fdccaf2" {
		t.Fatalf("legacy omitted timeout changed identity: %+v, %v", baseline, err)
	}
	seen := map[string]bool{baseline.Fingerprint: true}
	for _, seconds := range []int64{1, 120, 600} {
		s.Ollama = &OllamaOptions{GenerationTimeoutSeconds: &seconds}
		plan, err := BuildPlan(s)
		if err != nil || plan.Request.Ollama != s.Ollama || seen[plan.Fingerprint] || plan.Request.Prefix != baseline.Request.Prefix || plan.Request.Input != baseline.Request.Input {
			t.Fatalf("explicit timeout lost identity or changed prompt: %+v, %v", plan, err)
		}
		seen[plan.Fingerprint] = true
		encoded, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Spec
		decoder := json.NewDecoder(strings.NewReader(string(encoded)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&decoded); err != nil || !reflect.DeepEqual(decoded.Ollama, s.Ollama) {
			t.Fatalf("strict timeout round trip failed: %+v, %v", decoded.Ollama, err)
		}
	}
	p, calls := ollamaFixtureProvider(t, ollamaGenerateFixture)
	for _, seconds := range []int64{-1, 0, 601, math.MaxInt64} {
		options := &OllamaOptions{GenerationTimeoutSeconds: &seconds}
		s.Ollama = options
		if _, err := BuildPlan(s); err == nil {
			t.Fatalf("invalid generation timeout planned: %d", seconds)
		}
		for _, stream := range []bool{false, true} {
			req := ollamaTestRequest()
			req.Stream, req.Ollama = stream, options
			if _, err := p.CountInputTokens(context.Background(), req); err == nil {
				t.Fatalf("invalid generation timeout counted: %d", seconds)
			}
			if _, err := p.Generate(context.Background(), req); err == nil {
				t.Fatalf("invalid generation timeout generated: %d", seconds)
			}
		}
	}
	if calls.tags.Load() != 1 || calls.show.Load() != 1 || calls.generate.Load() != 0 {
		t.Fatal("invalid timeout reached HTTP after constructor discovery")
	}
}

func TestOllamaGenerationTimeoutOverridesOnlyPrivateGenerationClient(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, override := range []bool{false, true} {
			name := "nonstream"
			if stream {
				name = "stream"
			}
			if override {
				name += "/override"
			} else {
				name += "/default"
			}
			t.Run(name, func(t *testing.T) {
				recorded := make(chan map[string]any, 1)
				p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					recorded <- body
					time.Sleep(50 * time.Millisecond)
					if stream {
						w.Header().Set("Content-Type", "application/x-ndjson")
						writeStreamFrame(w, ollamaJSONGenerateFixture)
					} else {
						io.WriteString(w, ollamaJSONGenerateFixture)
					}
				}, nil)
				baseClient, baseTransport := p.client, p.client.Transport.(*http.Transport)
				if baseClient.Timeout != 120*time.Second || baseTransport.ResponseHeaderTimeout != 120*time.Second {
					t.Fatal("omitted timeout default changed")
				}
				baseClient.Timeout, baseTransport.ResponseHeaderTimeout = 20*time.Millisecond, 20*time.Millisecond
				req := ollamaTestRequest()
				req.Stream = stream
				if override {
					req.Ollama = &OllamaOptions{GenerationTimeoutSeconds: ollamaGenerationTimeout(1)}
				}
				got, err := p.Generate(context.Background(), req)
				if override {
					if err != nil || !got.Complete || got.Text != `{"message":"fixture output"}` {
						t.Fatalf("private timeout did not allow delayed generation: %+v, %v", got, err)
					}
				} else if err == nil || got.Complete || got.Text != "" {
					t.Fatalf("omitted timeout ignored original client deadline: %+v, %v", got, err)
				}
				if calls.generate.Load() != 1 || p.client != baseClient || p.client.Transport != baseTransport || baseClient.Timeout != 20*time.Millisecond || baseTransport.ResponseHeaderTimeout != 20*time.Millisecond {
					t.Fatal("generation retried or mutated shared client/transport")
				}
				wantBody := map[string]any{"model": req.Model, "system": req.Prefix, "prompt": req.Input, "stream": stream, "think": false, "options": map[string]any{"num_predict": float64(req.MaxOutputTokens), "num_ctx": float64(req.ContextWindow)}}
				if body := <-recorded; !reflect.DeepEqual(body, wantBody) {
					t.Fatalf("local timeout changed the upstream request: %+v", body)
				}
			})
		}
	}
}

func TestOllamaGenerationTimeoutPreservesEarlierCallerDeadline(t *testing.T) {
	for _, stream := range []bool{false, true} {
		p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond)
			if stream {
				w.Header().Set("Content-Type", "application/x-ndjson")
				writeStreamFrame(w, ollamaJSONGenerateFixture)
			} else {
				io.WriteString(w, ollamaJSONGenerateFixture)
			}
		}, nil)
		p.client.Timeout = 20 * time.Millisecond
		p.client.Transport.(*http.Transport).ResponseHeaderTimeout = 20 * time.Millisecond
		req := ollamaTestRequest()
		req.Stream = stream
		req.Ollama = &OllamaOptions{GenerationTimeoutSeconds: ollamaGenerationTimeout(1)}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		got, err := p.Generate(ctx, req)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != context.DeadlineExceeded || got.Complete || got.Text != "" || calls.generate.Load() != 1 {
			t.Fatalf("private timeout overrode caller deadline or retried: %+v, %v", got, err)
		}
	}
}

func TestOllamaGenerationTimeoutDoesNotExtendMetadataHTTP(t *testing.T) {
	var slow atomic.Bool
	var generated atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			if slow.Load() {
				time.Sleep(50 * time.Millisecond)
			}
			io.WriteString(w, ollamaTagsFixture)
		case "/api/show":
			io.WriteString(w, ollamaShowFixture)
		case "/api/generate":
			generated.Add(1)
			io.WriteString(w, ollamaJSONGenerateFixture)
		}
	}))
	defer srv.Close()
	p, err := NewOllamaProvider(srv.URL, "qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	defer p.client.CloseIdleConnections()
	p.client.Timeout = 20 * time.Millisecond
	p.client.Transport.(*http.Transport).ResponseHeaderTimeout = 20 * time.Millisecond
	slow.Store(true)
	req := ollamaTestRequest()
	req.Ollama = &OllamaOptions{GenerationTimeoutSeconds: ollamaGenerationTimeout(1)}
	if got, err := p.Generate(context.Background(), req); err == nil || got.Complete || generated.Load() != 0 {
		t.Fatalf("generation override changed metadata timeout: %+v, %v", got, err)
	}
}

func TestOllamaGenerationTimeoutIsIsolatedAcrossConcurrentRequests(t *testing.T) {
	p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		io.WriteString(w, ollamaJSONGenerateFixture)
	}, nil)
	baseClient, baseTransport := p.client, p.client.Transport.(*http.Transport)
	baseClient.Timeout, baseTransport.ResponseHeaderTimeout = 20*time.Millisecond, 20*time.Millisecond
	type outcome struct {
		override bool
		response Response
		err      error
	}
	outcomes := make(chan outcome, 3)
	for _, seconds := range []int64{0, 1, 2} {
		go func() {
			req := ollamaTestRequest()
			if seconds > 0 {
				req.Ollama = &OllamaOptions{GenerationTimeoutSeconds: &seconds}
			}
			response, err := p.Generate(context.Background(), req)
			outcomes <- outcome{seconds > 0, response, err}
		}()
	}
	for range 3 {
		got := <-outcomes
		if got.override && (got.err != nil || !got.response.Complete) || !got.override && (got.err == nil || got.response.Complete) {
			t.Fatalf("concurrent timeouts crossed requests: %+v", got)
		}
	}
	if calls.generate.Load() != 3 || p.client != baseClient || p.client.Transport != baseTransport || baseClient.Timeout != 20*time.Millisecond || baseTransport.ResponseHeaderTimeout != 20*time.Millisecond {
		t.Fatal("concurrent overrides retried or mutated shared client/transport")
	}
}

func TestOllamaGenerationTimeoutWithThinkingDiscardsTrace(t *testing.T) {
	fixture := ollamaThinkingFixture(t, nil)
	p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		io.WriteString(w, fixture)
	}, nil)
	p.client.Timeout = 20 * time.Millisecond
	p.client.Transport.(*http.Transport).ResponseHeaderTimeout = 20 * time.Millisecond
	req := ollamaTestRequest()
	req.Ollama = &OllamaOptions{Think: ollamaThink(true), GenerationTimeoutSeconds: ollamaGenerationTimeout(1)}
	got, err := p.Generate(context.Background(), req)
	if err != nil || !got.Complete || got.Text != `{"message":"fixture final answer"}` || got.Usage.OutputTokens != 20 || calls.generate.Load() != 1 {
		t.Fatalf("bounded thinking lost final text/usage or retried: %+v, %v", got, err)
	}
	assertNoOllamaThinkingText(t, got)
}
