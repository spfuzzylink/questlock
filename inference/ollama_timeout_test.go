package inference

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
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

// Metadata uses the original transport and must not depend on the scheduling of
// a short real-network timeout. A cloned generation transport falls through to
// the HTTP fixture because Transport.Clone copies exported fields, not protocols
// installed with RegisterProtocol.
type ollamaTimeoutMetadataTransport struct{}

func (ollamaTimeoutMetadataTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body string
	switch req.URL.Path {
	case "/api/tags":
		body = ollamaTagsFixture
	case "/api/show":
		body = ollamaShowFixture
	default:
		return nil, http.ErrSkipAltProtocol
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Request: req}, nil
}

func isolateOllamaTimeoutMetadata(p *OllamaProvider) {
	p.client.Transport.(*http.Transport).RegisterProtocol("http", ollamaTimeoutMetadataTransport{})
	installOllamaTimeoutHandshake(p)
}

type ollamaTimeoutStartedKey struct{}
type ollamaTimeoutIndexKey struct{}

type ollamaTimeoutHandshakeConn struct {
	net.Conn
	started <-chan struct{}
}

func (c *ollamaTimeoutHandshakeConn) Write(data []byte) (int, error) {
	n, err := c.Conn.Write(data)
	if err == nil {
		// The bytes are on the wire, but the transport cannot start its header
		// timer until this write returns. The handler acknowledges receipt first.
		select {
		case <-c.started:
		case <-time.After(10 * time.Second):
		}
	}
	return n, err
}

func installOllamaTimeoutHandshake(p *OllamaProvider) {
	transport := p.client.Transport.(*http.Transport)
	transport.CloseIdleConnections()
	transport.DisableKeepAlives = true
	dial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		started, _ := ctx.Value(ollamaTimeoutStartedKey{}).(<-chan struct{})
		if err != nil || started == nil {
			return conn, err
		}
		return &ollamaTimeoutHandshakeConn{Conn: conn, started: started}, nil
	}
}

func ollamaGenerationStartedContext(started <-chan struct{}) context.Context {
	return context.WithValue(context.Background(), ollamaTimeoutStartedKey{}, started)
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
				started := make(chan struct{})
				var once sync.Once
				p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
					once.Do(func() { close(started) })
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					recorded <- body
					if !override {
						<-r.Context().Done()
						return
					}
					// This exceeds the base header timeout, while the override has
					// ample room even on a loaded race-detector worker.
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
				isolateOllamaTimeoutMetadata(p)
				baseTransport.ResponseHeaderTimeout = 20 * time.Millisecond
				deadlines := make(chan time.Duration, 1)
				baseTransport.Proxy = func(r *http.Request) (*url.URL, error) {
					if r.URL.Path == "/api/generate" {
						deadline, ok := r.Context().Deadline()
						if !ok {
							t.Error("generation client has no deadline")
						}
						deadlines <- time.Until(deadline)
					}
					return nil, nil
				}
				req := ollamaTestRequest()
				req.Stream = stream
				wantDeadline := 120 * time.Second
				if override {
					req.Ollama = &OllamaOptions{GenerationTimeoutSeconds: ollamaGenerationTimeout(60)}
					wantDeadline = 60 * time.Second
				}
				got, err := p.Generate(ollamaGenerationStartedContext(started), req)
				if override {
					if err != nil || !got.Complete || got.Text != `{"message":"fixture output"}` {
						t.Fatalf("private timeout did not allow delayed generation: %+v, %v", got, err)
					}
				} else if err == nil || got.Complete || got.Text != "" {
					t.Fatalf("omitted timeout ignored original header deadline: %+v, %v", got, err)
				}
				if remaining := <-deadlines; remaining <= wantDeadline/2 || remaining > wantDeadline {
					t.Fatalf("generation client deadline = %v, want near %v", remaining, wantDeadline)
				}
				if calls.generate.Load() != 1 || p.client != baseClient || p.client.Transport != baseTransport || baseClient.Timeout != 120*time.Second || baseTransport.ResponseHeaderTimeout != 20*time.Millisecond {
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

// The fixture controls when the caller deadline expires after generation has
// started. It exercises deadline propagation without racing metadata scheduling
// against a narrow wall-clock deadline.
type ollamaControlledDeadline struct {
	context.Context
	deadline time.Time
	done     chan struct{}
}

func (c *ollamaControlledDeadline) Deadline() (time.Time, bool) { return c.deadline, true }
func (c *ollamaControlledDeadline) Done() <-chan struct{}       { return c.done }
func (c *ollamaControlledDeadline) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func TestOllamaGenerationTimeoutPreservesEarlierCallerDeadline(t *testing.T) {
	for _, stream := range []bool{false, true} {
		started := make(chan struct{}, 1)
		p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			started <- struct{}{}
			<-r.Context().Done()
		}, nil)
		isolateOllamaTimeoutMetadata(p)
		req := ollamaTestRequest()
		req.Stream = stream
		req.Ollama = &OllamaOptions{GenerationTimeoutSeconds: ollamaGenerationTimeout(300)}
		ctx := &ollamaControlledDeadline{Context: context.Background(), deadline: time.Now().Add(time.Minute), done: make(chan struct{})}
		type outcome struct {
			response Response
			err      error
		}
		finished := make(chan outcome, 1)
		go func() {
			response, err := p.Generate(ctx, req)
			finished <- outcome{response, err}
		}()
		select {
		case <-started:
		case <-time.After(10 * time.Second):
			close(ctx.done)
			t.Fatal("generation did not reach deadline fixture")
		}
		close(ctx.done)
		got := <-finished
		if !errors.Is(got.err, context.DeadlineExceeded) || ctx.Err() != context.DeadlineExceeded || got.response.Complete || got.response.Text != "" || calls.generate.Load() != 1 {
			t.Fatalf("private timeout overrode caller deadline or retried: %+v, %v", got.response, got.err)
		}
	}
}

func TestOllamaGenerationTimeoutDoesNotExtendMetadataHTTP(t *testing.T) {
	var slow atomic.Bool
	var generated atomic.Int32
	started := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			if slow.Load() {
				once.Do(func() { close(started) })
				<-r.Context().Done()
				return
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
	installOllamaTimeoutHandshake(p)
	p.client.Transport.(*http.Transport).ResponseHeaderTimeout = 20 * time.Millisecond
	slow.Store(true)
	req := ollamaTestRequest()
	req.Ollama = &OllamaOptions{GenerationTimeoutSeconds: ollamaGenerationTimeout(60)}
	ctx, cancel := context.WithTimeout(ollamaGenerationStartedContext(started), 10*time.Second)
	defer cancel()
	if got, err := p.Generate(ctx, req); err == nil || ctx.Err() != nil || got.Complete || generated.Load() != 0 || p.client.Timeout != 120*time.Second || p.client.Transport.(*http.Transport).ResponseHeaderTimeout != 20*time.Millisecond {
		t.Fatalf("generation override changed metadata timeout: %+v, %v", got, err)
	}
}

func TestOllamaGenerationTimeoutIsIsolatedAcrossConcurrentRequests(t *testing.T) {
	started := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	release := make(chan struct{})
	p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
		index := int(r.Header.Get("X-Timeout-Fixture-Index")[0] - '0')
		close(started[index])
		io.Copy(io.Discard, r.Body)
		if index == 0 {
			<-r.Context().Done()
			return
		}
		<-release
		io.WriteString(w, ollamaJSONGenerateFixture)
	}, nil)
	baseClient, baseTransport := p.client, p.client.Transport.(*http.Transport)
	isolateOllamaTimeoutMetadata(p)
	baseTransport.ResponseHeaderTimeout = 20 * time.Millisecond
	baseTransport.Proxy = func(r *http.Request) (*url.URL, error) {
		if r.URL.Path == "/api/generate" {
			index := r.Context().Value(ollamaTimeoutIndexKey{}).(int)
			r.Header.Set("X-Timeout-Fixture-Index", string(rune('0'+index)))
		}
		return nil, nil
	}
	type outcome struct {
		override bool
		response Response
		err      error
	}
	outcomes := make(chan outcome, 3)
	for index, seconds := range []int64{0, 60, 90} {
		go func() {
			req := ollamaTestRequest()
			req.Input = string(rune('0' + index))
			if seconds > 0 {
				req.Ollama = &OllamaOptions{GenerationTimeoutSeconds: &seconds}
			}
			ctx := context.WithValue(ollamaGenerationStartedContext(started[index]), ollamaTimeoutIndexKey{}, index)
			response, err := p.Generate(ctx, req)
			outcomes <- outcome{seconds > 0, response, err}
		}()
	}
	for _, ready := range started {
		select {
		case <-ready:
		case <-time.After(10 * time.Second):
			close(release)
			t.Fatal("concurrent generation did not reach fixture")
		}
	}
	close(release)
	for range 3 {
		got := <-outcomes
		if got.override && (got.err != nil || !got.response.Complete) || !got.override && (got.err == nil || got.response.Complete) {
			t.Fatalf("concurrent timeouts crossed requests: %+v", got)
		}
	}
	if calls.generate.Load() != 3 || p.client != baseClient || p.client.Transport != baseTransport || baseClient.Timeout != 120*time.Second || baseTransport.ResponseHeaderTimeout != 20*time.Millisecond {
		t.Fatal("concurrent overrides retried or mutated shared client/transport")
	}
}

func TestOllamaGenerationTimeoutWithThinkingDiscardsTrace(t *testing.T) {
	fixture := ollamaThinkingFixture(t, nil)
	p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, fixture)
	}, nil)
	isolateOllamaTimeoutMetadata(p)
	p.client.Transport.(*http.Transport).ResponseHeaderTimeout = 20 * time.Millisecond
	req := ollamaTestRequest()
	req.Ollama = &OllamaOptions{Think: ollamaThink(true), GenerationTimeoutSeconds: ollamaGenerationTimeout(60)}
	got, err := p.Generate(context.Background(), req)
	if err != nil || !got.Complete || got.Text != `{"message":"fixture final answer"}` || got.Usage.OutputTokens != 20 || calls.generate.Load() != 1 {
		t.Fatalf("bounded thinking lost final text/usage or retried: %+v, %v", got, err)
	}
	assertNoOllamaThinkingText(t, got)
}
