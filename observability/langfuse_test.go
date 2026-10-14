package observability

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func sampleExecution() Execution {
	start := time.Unix(1_800_000_000, 12345).UTC()
	usage := Usage{InputTokens: 100, UncachedInputTokens: 60, CacheReadTokens: 30, CacheWriteTokens: 10, OutputTokens: 40, ReasoningTokens: 8}
	cost := 0.0042
	return Execution{
		StartedAt: start, EndedAt: start.Add(250 * time.Millisecond), Fingerprint: strings.Repeat("a", 64),
		Provider: "anthropic", Model: "fixture-model", Outcome: Complete, Durable: true,
		ContextBytes: 300, TotalContextBytes: 500, SelectedContext: 2, OmittedContext: 1,
		ThisRunUsage: usage, OriginalUsage: usage, CostUSD: &cost,
		Generation: &Generation{StartedAt: start.Add(20 * time.Millisecond), EndedAt: start.Add(220 * time.Millisecond), Model: "fixture-model", Outcome: Complete, KnownUsage: true, Usage: usage, CostUSD: &cost},
		PhaseMS:    map[string]float64{"provider_setup": 4.5, "plan": 1.25, "generation": 200, "publication": 2.0},
	}
}

func exporterForServer(t *testing.T, s *httptest.Server, timeout time.Duration) *Langfuse {
	t.Helper()
	exporter, err := NewLangfuse(Config{Endpoint: s.URL + "/api/public/otel/v1/traces", PublicKey: "fixture-public", SecretKey: "fixture-secret", Release: "0.3.0-test", Timeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	return exporter
}

// Decode independently of the package's OTLP structs, so field encodings and
// numeric enums are checked on the bytes actually accepted by an HTTP server.
func capturedSpans(t *testing.T, data []byte) ([]map[string]any, map[string]any) {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	resources := envelope["resourceSpans"].([]any)
	if len(resources) != 1 {
		t.Fatalf("resource count: %d", len(resources))
	}
	resource := resources[0].(map[string]any)
	scopes := resource["scopeSpans"].([]any)
	if len(scopes) != 1 {
		t.Fatalf("scope count: %d", len(scopes))
	}
	rawSpans := scopes[0].(map[string]any)["spans"].([]any)
	spans := make([]map[string]any, len(rawSpans))
	for i, s := range rawSpans {
		spans[i] = s.(map[string]any)
	}
	return spans, resource["resource"].(map[string]any)
}

func attributes(t *testing.T, obj map[string]any) map[string]any {
	t.Helper()
	result := make(map[string]any)
	for _, raw := range obj["attributes"].([]any) {
		a := raw.(map[string]any)
		value := a["value"].(map[string]any)
		if len(value) != 1 {
			t.Fatalf("OTLP AnyValue contains more than one field: %v", value)
		}
		for _, val := range value {
			result[a["key"].(string)] = val
		}
	}
	return result
}

func TestGenerationHTTPContractAndExclusiveUsage(t *testing.T) {
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "host.name=private-host,user.id=private-user")
	t.Setenv("OPENAI_API_KEY", "private-provider-marker")
	t.Setenv("LANGFUSE_SESSION_ID", "private-session-marker")
	var captured []byte
	var calls atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/api/public/otel/v1/traces" || r.URL.RawQuery != "" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		public, secret, ok := r.BasicAuth()
		if !ok || public != "fixture-public" || secret != "fixture-secret" {
			t.Error("project Basic auth missing")
		}
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("x-langfuse-ingestion-version") != "4" {
			t.Error("OTLP encoding/version headers missing")
		}
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "{}")
	}))
	defer s.Close()
	e := sampleExecution()
	receipt, err := exporterForServer(t, s, 0).Export(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || len(captured) >= maxPayloadBytes {
		t.Fatalf("calls=%d payload=%d", calls.Load(), len(captured))
	}
	for _, marker := range []string{"fixture-public", "fixture-secret", "private-host", "private-user", "private-provider-marker", "private-session-marker"} {
		if strings.Contains(string(captured), marker) {
			t.Fatalf("credential/environment content leaked into spans: %s", marker)
		}
	}
	spans, resource := capturedSpans(t, captured)
	if len(spans) != 2 {
		t.Fatalf("want root + actual generation, got %d spans", len(spans))
	}
	root, generation := spans[0], spans[1]
	if root["spanId"] != receipt.RootSpanID || generation["spanId"] != receipt.GenerationSpanID || root["traceId"] != receipt.TraceID || generation["traceId"] != receipt.TraceID || generation["parentSpanId"] != receipt.RootSpanID {
		t.Fatal("trace hierarchy did not match receipt")
	}
	if _, exists := root["parentSpanId"]; exists || !validID(receipt.TraceID, 16) || !validID(receipt.RootSpanID, 8) || !validID(receipt.GenerationSpanID, 8) {
		t.Fatal("invalid root/IDs")
	}
	if root["kind"] != float64(1) || generation["kind"] != float64(3) || generation["status"].(map[string]any)["code"] != float64(1) {
		t.Fatal("OTLP enum fields must be numeric and represent internal/client/ok")
	}
	if root["startTimeUnixNano"] != strconv.FormatInt(e.StartedAt.UnixNano(), 10) || generation["endTimeUnixNano"] != strconv.FormatInt(e.Generation.EndedAt.UnixNano(), 10) {
		t.Fatal("nanosecond timestamps must be decimal strings")
	}
	ra, ga := attributes(t, root), attributes(t, generation)
	if ra["langfuse.observation.type"] != "span" || ga["langfuse.observation.type"] != "generation" || ra["langfuse.trace.public"] != false {
		t.Fatal("explicit private root/generation type missing")
	}
	if ra[metadataPrefix+"this_run_input_tokens"] != "100" || ra[metadataPrefix+"execution_ms"] != float64(250) || ra[metadataPrefix+"phase_provider_setup_ms"] != 4.5 || ga[metadataPrefix+"generation_ms"] != float64(200) {
		t.Fatal("usage/timing metadata inaccurate")
	}
	for _, attr := range []string{"langfuse.observation.input", "langfuse.observation.output", "langfuse.trace.input", "langfuse.trace.output", "user.id", "langfuse.user.id", "langfuse.session.id", "host.name", "langfuse.observation.usage_details", "langfuse.observation.cost_details"} {
		if _, exists := ra[attr]; exists {
			t.Fatalf("forbidden root attribute exported: %s", attr)
		}
	}
	var usage map[string]int64
	if err := json.Unmarshal([]byte(ga["langfuse.observation.usage_details"].(string)), &usage); err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"input": 60, "input_cached_tokens": 30, "input_cache_creation": 10, "output": 32, "output_reasoning_tokens": 8, "total": 140}
	if !reflect.DeepEqual(usage, want) {
		t.Fatalf("inclusive tokens were not correctly partitioned: %v", usage)
	}
	var cost map[string]float64
	if err := json.Unmarshal([]byte(ga["langfuse.observation.cost_details"].(string)), &cost); err != nil || cost["total"] != 0.0042 || ga[metadataPrefix+"cost_source"] != "caller_estimate" {
		t.Fatal("explicit cost/source inaccurate")
	}
	resourceAttrs := attributes(t, resource)
	if len(resourceAttrs) != 2 || resourceAttrs["service.name"] != "questlock" || resourceAttrs["service.version"] != "0.3.0-test" {
		t.Fatalf("unapproved resource attributes: %v", resourceAttrs)
	}
}

func TestReuseDoesNotCreateGenerationOrBillHistoricalUsage(t *testing.T) {
	var captured []byte
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "{}")
	}))
	defer s.Close()
	e := sampleExecution()
	zero, originalCost := 0.0, *e.CostUSD
	e.Reused, e.Outcome, e.Generation = true, Reused, nil
	e.AvoidedUsage, e.ThisRunUsage, e.CostUSD, e.AvoidedCostUSD = e.OriginalUsage, Usage{}, &zero, &originalCost
	receipt, err := exporterForServer(t, s, 0).Export(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	spans, _ := capturedSpans(t, captured)
	if len(spans) != 1 || receipt.GenerationSpanID != "" {
		t.Fatal("reuse fabricated a generation")
	}
	a := attributes(t, spans[0])
	if a["langfuse.observation.type"] != "span" || a[metadataPrefix+"reused"] != true || a[metadataPrefix+"this_run_input_tokens"] != "0" || a[metadataPrefix+"this_run_output_tokens"] != "0" || a[metadataPrefix+"this_run_cost_usd"] != 0.0 || a[metadataPrefix+"original_input_tokens"] != "100" || a[metadataPrefix+"avoided_input_tokens"] != "100" || a[metadataPrefix+"avoided_cost_usd"] != originalCost {
		t.Fatalf("wrong reuse accounting: %v", a)
	}
	for _, key := range []string{"langfuse.observation.model.name", "langfuse.observation.usage_details", "langfuse.observation.cost_details", "gen_ai.request.model"} {
		if _, exists := a[key]; exists {
			t.Fatalf("reuse can be inferred/billed as generation from %s", key)
		}
	}
}

func TestUnknownBillingOmittedAndLocalZeroExplicit(t *testing.T) {
	for _, mode := range []string{"uncertain", "local", "unpriced"} {
		t.Run(mode, func(t *testing.T) {
			var captured []byte
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, "{}")
			}))
			defer s.Close()
			e := sampleExecution()
			if mode == "uncertain" {
				e.Outcome, e.Generation.Outcome, e.Generation.KnownUsage = Uncertain, Uncertain, false
				e.ThisRunUsage, e.Generation.Usage, e.OriginalUsage = Usage{}, Usage{}, Usage{}
				e.CostUSD, e.Generation.CostUSD = nil, nil
			} else if mode == "local" {
				zero := 0.0
				e.Provider, e.CostUSD, e.Generation.CostUSD = "ollama", &zero, &zero
			} else {
				e.CostUSD, e.Generation.CostUSD = nil, nil
			}
			if _, err := exporterForServer(t, s, 0).Export(context.Background(), e); err != nil {
				t.Fatal(err)
			}
			spans, _ := capturedSpans(t, captured)
			ra, ga := attributes(t, spans[0]), attributes(t, spans[1])
			if mode == "uncertain" {
				if ga[metadataPrefix+"usage_known"] != false || ra[metadataPrefix+"usage_known"] != false {
					t.Fatal("uncertain usage flagged known")
				}
				if _, exists := ga["langfuse.observation.usage_details"]; exists {
					t.Fatal("unknown generation usage fabricated as zero")
				}
				if _, exists := ra[metadataPrefix+"this_run_input_tokens"]; exists {
					t.Fatal("unknown root usage fabricated as zero")
				}
			}
			if mode == "local" {
				if ga["langfuse.observation.cost_details"] != "{\"total\":0}" || ga[metadataPrefix+"cost_source"] != "local_zero" || ra[metadataPrefix+"cost_source"] != "local_zero" {
					t.Fatal("local provider zero not explicitly acknowledged")
				}
			} else if _, exists := ga["langfuse.observation.cost_details"]; exists {
				t.Fatal("unpriced/unknown cost fabricated")
			}
		})
	}
}

func TestExporterDoesNotRetryOrLeakServerErrors(t *testing.T) {
	cases := []struct {
		name, body, contentType string
		status                  int
		want                    error
	}{
		{"unauthorized", "fixture-private-error", "text/plain", 401, ErrUnavailable},
		{"throttled", "fixture-private-error", "text/plain", 429, ErrUnavailable},
		{"server_failure", "fixture-private-error", "text/plain", 503, ErrUnavailable},
		{"html_success", "fixture-private-error", "text/html", 200, ErrInvalidResponse},
		{"malformed", "fixture-private-error", "application/json", 200, ErrInvalidResponse},
		{"null", "null", "application/json", 200, ErrInvalidResponse},
		{"oversized", strings.Repeat("x", maxResponseBytes+1), "application/json", 200, ErrInvalidResponse},
		{"partial", `{"partialSuccess":{"rejectedSpans":"1","errorMessage":"fixture-private-error"}}`, "application/json", 200, ErrRejected},
		{"partial_numeric", `{"partialSuccess":{"rejectedSpans":1}}`, "application/json", 200, ErrRejected},
		{"bad_count", `{"partialSuccess":{"rejectedSpans":"-1"}}`, "application/json", 200, ErrInvalidResponse},
		{"warning", `{"partialSuccess":{"rejectedSpans":"0","errorMessage":"fixture-private-error"}}`, "application/json", 200, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var count atomic.Int64
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				w.Header().Set("Content-Type", c.contentType)
				w.WriteHeader(c.status)
				io.WriteString(w, c.body)
			}))
			defer s.Close()
			r, err := exporterForServer(t, s, 0).Export(context.Background(), sampleExecution())
			if !errors.Is(err, c.want) || count.Load() != 1 || r.TraceID == "" {
				t.Fatalf("err=%v want=%v attempts=%d receipt=%v", err, c.want, count.Load(), r)
			}
			if err != nil && strings.Contains(err.Error(), "fixture-private-error") {
				t.Fatal("raw server error leaked")
			}
		})
	}
}

func TestRedirectProxyAndTimeoutBoundaries(t *testing.T) {
	var redirected atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/api/public/otel/v1/traces", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	exporter := exporterForServer(t, source, 0)
	if _, err := exporter.Export(context.Background(), sampleExecution()); !errors.Is(err, ErrUnavailable) || redirected.Load() != 0 {
		t.Fatal("redirect was followed or falsely acknowledged")
	}
	transport := exporter.client.Transport.(*http.Transport)
	if transport.Proxy != nil || !transport.DisableKeepAlives || transport.MaxResponseHeaderBytes > 64<<10 {
		t.Fatal("proxy/reused connection/unbounded response headers enabled")
	}
	var slowCalls atomic.Int64
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slowCalls.Add(1)
		select {
		case <-r.Context().Done():
		case <-time.After(250 * time.Millisecond):
		}
	}))
	defer slow.Close()
	bounded := exporterForServer(t, slow, 20*time.Millisecond)
	start := time.Now()
	if _, err := bounded.Export(context.Background(), sampleExecution()); !errors.Is(err, ErrUnavailable) || time.Since(start) > 200*time.Millisecond || slowCalls.Load() != 1 {
		t.Fatalf("timeout unbounded/retried: err=%v calls=%d duration=%s", err, slowCalls.Load(), time.Since(start))
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := bounded.Export(cancelled, sampleExecution()); !errors.Is(err, ErrUnavailable) || slowCalls.Load() != 1 {
		t.Fatal("cancelled export sent a request")
	}
}

func TestConfigurationRejectsUnsafeEndpointsWithoutNetwork(t *testing.T) {
	for _, endpoint := range []string{
		"", "http://localhost:3000/api/public/otel/v1/traces", "http://192.0.2.1/api/public/otel/v1/traces", "http://127.0.0.1.example.test/api/public/otel/v1/traces",
		"https://example.test/api/public/otel/v1/traces?secret=value", "https://user:pass@example.com/api/public/otel/v1/traces", "https://example.test/api/public/otel/v1/traces#marker",
		"https://example.test/api/public/otel/v1/traces?", "https://example.test/a/../api/public/otel/v1/traces", "https://example.test/api/public/otel/v1/%74races", "https://example.test/api/public/otel", "file:///api/public/otel/v1/traces",
	} {
		if _, err := NewLangfuse(Config{Endpoint: endpoint, PublicKey: "fixture-public", SecretKey: "fixture-secret"}); err == nil {
			t.Fatalf("unsafe endpoint allowed: %s", endpoint)
		}
	}
	for _, endpoint := range []string{"https://example.test/api/public/otel/v1/traces", "https://example.test/hosted/api/public/otel/v1/traces", "http://127.0.0.1:3000/api/public/otel/v1/traces", "http://[::1]:3000/api/public/otel/v1/traces"} {
		if _, err := NewLangfuse(Config{Endpoint: endpoint, PublicKey: "fixture-public", SecretKey: "fixture-secret"}); err != nil {
			t.Fatalf("valid endpoint rejected: %s (%v)", endpoint, err)
		}
	}
	for _, c := range []Config{
		{PublicKey: "", SecretKey: "fixture-secret"}, {PublicKey: "with:colon", SecretKey: "fixture-secret"}, {PublicKey: "fixture-public", SecretKey: "fixture-secret\n"},
		{PublicKey: "fixture-public", SecretKey: "fixture-secret", Timeout: -1}, {PublicKey: "fixture-public", SecretKey: "fixture-secret", Timeout: maxTimeout + 1}, {PublicKey: "fixture-public", SecretKey: "fixture-secret", Release: "private label with spaces"},
	} {
		c.Endpoint = "https://example.test/api/public/otel/v1/traces"
		if _, err := NewLangfuse(c); err == nil {
			t.Fatal("invalid key/timeout/release accepted")
		}
	}
}

func TestInvalidMetadataNeverLeavesWorker(t *testing.T) {
	var calls atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer s.Close()
	cases := []func(*Execution){
		func(e *Execution) { e.Fingerprint = "raw-private-task-marker" },
		func(e *Execution) { e.Provider = "raw-private-provider-marker" },
		func(e *Execution) { e.Model = "https://private.example.test/model" },
		func(e *Execution) { e.Outcome = "raw-private-error-marker" },
		func(e *Execution) { e.StartedAt = time.Time{} },
		func(e *Execution) { e.EndedAt = e.StartedAt.Add(-1) },
		func(e *Execution) { e.Generation.StartedAt = e.StartedAt.Add(-1) },
		func(e *Execution) { e.ThisRunUsage.InputTokens++ },
		func(e *Execution) { e.Generation.Usage.ReasoningTokens = 1000 },
		func(e *Execution) { e.Generation.KnownUsage = false },
		func(e *Execution) { e.Reused = true },
		func(e *Execution) { e.PhaseMS["raw-private-stage-marker"] = 1 },
		func(e *Execution) { e.PhaseMS["plan"] = math.Inf(1) },
		func(e *Execution) { bad := math.NaN(); e.CostUSD = &bad },
	}
	l := exporterForServer(t, s, 0)
	for _, mutate := range cases {
		e := sampleExecution()
		mutate(&e)
		if _, err := l.Export(context.Background(), e); !errors.Is(err, ErrInvalidExecution) {
			t.Fatalf("invalid metadata accepted: %v", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid metadata left worker in %d requests", calls.Load())
	}
}

func TestConcurrentInvocationsHaveDistinctTraceIDs(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "{}")
	}))
	defer s.Close()
	l := exporterForServer(t, s, 0)
	var wg sync.WaitGroup
	traces := sync.Map{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := l.Export(context.Background(), sampleExecution())
			if err != nil {
				t.Error(err)
				return
			}
			if _, existed := traces.LoadOrStore(r.TraceID, true); existed {
				t.Error("invocations shared a trace ID")
			}
		}()
	}
	wg.Wait()
}

func TestOptionalStreamingContentTimingUsesDocumentedCompletionTimestamp(t *testing.T) {
	var captured []byte
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "{}")
	}))
	defer s.Close()
	e := sampleExecution()
	first, output := 30.0, 150.0
	e.Generation.FirstTokenMS, e.Generation.OutputMS, e.Generation.TimingBasis = &first, &output, "client_observed_content_chunks"
	if _, err := exporterForServer(t, s, 0).Export(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	spans, _ := capturedSpans(t, captured)
	root, generation := attributes(t, spans[0]), attributes(t, spans[1])
	wantFirst := e.Generation.StartedAt.Add(30 * time.Millisecond).UTC().Format(time.RFC3339Nano)
	if generation["langfuse.observation.completion_start_time"] != wantFirst || generation[metadataPrefix+"first_content_ms"] != first || generation[metadataPrefix+"output_content_ms"] != output || generation[metadataPrefix+"timing_basis"] != "client_observed_content_chunks" {
		t.Fatalf("client observation timestamp/basis inaccurate: %v", generation)
	}
	if _, exists := root["langfuse.observation.completion_start_time"]; exists {
		t.Fatal("execution root invented provider first-token latency")
	}
}

func TestInvalidOptionalContentTimingNeverLeavesWorker(t *testing.T) {
	var calls atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer s.Close()
	l := exporterForServer(t, s, 0)
	for _, mutate := range []func(*Generation){
		func(g *Generation) {
			ms := 201.0
			g.FirstTokenMS, g.TimingBasis = &ms, "client_observed_content_chunks"
		},
		func(g *Generation) { ms := -1.0; g.FirstTokenMS, g.TimingBasis = &ms, "client_observed_content_chunks" },
		func(g *Generation) {
			ms := math.NaN()
			g.FirstTokenMS, g.TimingBasis = &ms, "client_observed_content_chunks"
		},
		func(g *Generation) { ms := 1.0; g.OutputMS, g.TimingBasis = &ms, "client_observed_content_chunks" },
		func(g *Generation) { ms := 1.0; g.FirstTokenMS, g.TimingBasis = &ms, "private-raw-timing-label" },
		func(g *Generation) {
			first, out := 100.0, 101.0
			g.FirstTokenMS, g.OutputMS, g.TimingBasis = &first, &out, "client_observed_content_chunks"
		},
	} {
		e := sampleExecution()
		mutate(e.Generation)
		if _, err := l.Export(context.Background(), e); !errors.Is(err, ErrInvalidExecution) {
			t.Fatalf("invalid timing accepted: %v", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid optional timing exported")
	}
}
