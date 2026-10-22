package execution_test

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/execution"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/observability"
)

// These are real HTTPS/SQLite coordination checks with scripted generation.
// They call no model and establish neither two-host nor GPU performance.
type fixtureProvider struct {
	counts, generations atomic.Int64
	started             chan struct{}
	release             <-chan struct{}
	startOnce           sync.Once
	generateErr         error
}

func (*fixtureProvider) Identity() string { return "execution-fixture:immutable-model-1" }
func (*fixtureProvider) SelfHosted() bool { return true }
func (p *fixtureProvider) CountInputTokens(context.Context, inference.Request) (int64, error) {
	p.counts.Add(1)
	return 24, nil
}
func (p *fixtureProvider) Generate(ctx context.Context, _ inference.Request) (inference.Response, error) {
	p.generations.Add(1)
	if p.started != nil {
		p.startOnce.Do(func() { close(p.started) })
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return inference.Response{}, ctx.Err()
		}
	}
	if p.generateErr != nil {
		return inference.Response{}, p.generateErr
	}
	return inference.Response{ID: "scripted-1", Model: "cpu-fixture", Text: "private-response-fixture", Complete: true,
		Usage: inference.Usage{InputTokens: 24, UncachedInputTokens: 24, OutputTokens: 3}}, nil
}

type fixtureResources struct{ mode atomic.Int32 }

func (r *fixtureResources) Sample(context.Context) (admission.Snapshot, error) {
	x := admission.Snapshot{At: time.Now(), Known: true, Source: "execution_cpu_fixture", CPUCapacityMilli: 8000,
		LoadCapacityMilli: 8000, LoadMilli: 100, AvailableMemoryBytes: 4 << 30, MemoryPressure: admission.Normal}
	switch r.mode.Load() {
	case 1:
		x.MemoryPressure = admission.High
	case 2:
		x.At = x.At.Add(-time.Minute)
	case 3:
		x.Known = false
	}
	return x, nil
}

type brokerFixture struct {
	url, tokenA, tokenB string
	root                []byte
}

func newBrokerFixture(t testing.TB) brokerFixture {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a, err := s.CreateToken(context.Background(), "isolated-node-a", "execution-fixture")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateToken(context.Background(), "isolated-node-b", "execution-fixture")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(httpapi.New(s))
	t.Cleanup(srv.Close)
	return brokerFixture{srv.URL, a, b, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})}
}

func (b brokerFixture) client(t testing.TB, token string) *client.Client {
	t.Helper()
	c, err := client.NewWithTLSRoots(b.url, token, b.root)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newNode(t testing.TB, broker *client.Client, provider inference.Provider, source admission.Source, exporter observability.Exporter, allowHosted ...bool) *execution.Coordinator {
	t.Helper()
	cfg := admission.DefaultConfig()
	cfg.RecoverySamples, cfg.SampleInterval, cfg.RampInterval = 1, 10*time.Millisecond, 10*time.Millisecond
	cfg.MaxSampleAge = time.Second
	lifecycle, cancel := context.WithCancel(context.Background())
	c, err := execution.New(lifecycle, execution.Config{Runner: inference.Runner{Broker: broker, Provider: provider},
		Admission: cfg, Source: source, Exporter: exporter, ExportTimeout: time.Second,
		AllowHosted: len(allowHosted) > 0 && allowHosted[0]})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		ctx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if err := c.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return c
}

func waitStatus(t testing.TB, c *execution.Coordinator, reason string) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		s := c.Status()
		if s.Reason == reason && (reason != "ready" || s.ExpensiveWorkerLimit > 0) {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("coordinator did not reach %q: %+v", reason, s)
		case <-tick.C:
		}
	}
}

func fixtureSpec() inference.Spec {
	return inference.Spec{Task: "support-handoff", Revision: "ticket-1", Provider: "ollama", Model: "cpu-fixture",
		Instructions: "Use only the supplied facts.", Input: "private-prompt-fixture", MaxInputTokens: 256,
		MaxOutputTokens: 32, ReuseForSeconds: 3600}
}

func TestHTTPSIsolatedNodesFenceDuplicateAndReuseCompletedSnapshot(t *testing.T) {
	b := newBrokerFixture(t)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	p := &fixtureProvider{started: make(chan struct{}), release: release}
	a := newNode(t, b.client(t, b.tokenA), p, &fixtureResources{}, nil)
	z := newNode(t, b.client(t, b.tokenB), p, &fixtureResources{}, nil)
	waitStatus(t, a, "ready")
	waitStatus(t, z, "ready")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type answer struct {
		result execution.Result
		err    error
	}
	done := make(chan answer, 1)
	go func() { result, err := a.Run(ctx, fixtureSpec()); done <- answer{result, err} }()
	select {
	case <-p.started:
	case <-ctx.Done():
		t.Fatal("first node did not reach scripted generation")
	}
	if _, err := z.Run(ctx, fixtureSpec()); !errors.Is(err, inference.ErrInFlight) {
		t.Fatalf("second node bypassed the pending claim: %v", err)
	}
	if p.counts.Load() != 1 || p.generations.Load() != 1 {
		t.Fatal("duplicate reached provider preflight or generation")
	}
	unblock()
	first := <-done
	if first.err != nil || !first.result.Durable || first.result.Reused {
		t.Fatalf("first result: %+v, %v", first.result, first.err)
	}
	again, err := z.Run(ctx, fixtureSpec())
	if err != nil || !again.Durable || !again.Reused || again.ThisRunUsage != (inference.Usage{}) ||
		again.AvoidedUsage != first.result.GenerationUsage || p.counts.Load() != 1 || p.generations.Load() != 1 {
		t.Fatalf("completed snapshot was not reused without generation: %+v, %v", again, err)
	}
	changed := fixtureSpec()
	changed.Revision = "ticket-2"
	fresh, err := z.Run(ctx, changed)
	if err != nil || !fresh.Durable || fresh.Reused || p.counts.Load() != 2 || p.generations.Load() != 2 {
		t.Fatalf("changed snapshot reused stale work: %+v, %v", fresh, err)
	}
}

func TestAdmissionAndValidationRejectBeforeDurableClaimOrProviderCall(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   int32
		reason string
	}{
		{"memory_pressure", 1, "resource_pressure"},
		{"stale_cpu_memory", 2, "signals_unavailable_or_stale"},
		{"unknown_cpu_memory", 3, "signals_unavailable_or_stale"},
		{"oversized_snapshot", 0, "ready"},
		{"canceled_context", 0, "ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBrokerFixture(t)
			broker := b.client(t, b.tokenA)
			p := &fixtureProvider{}
			source := &fixtureResources{}
			source.mode.Store(tc.mode)
			c := newNode(t, broker, p, source, nil)
			waitStatus(t, c, tc.reason)
			spec := fixtureSpec()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if tc.name == "oversized_snapshot" {
				spec.Input = strings.Repeat("x", 513<<10)
			}
			if tc.name == "canceled_context" {
				cancel()
			}
			_, err := c.Run(ctx, spec)
			if err == nil || (tc.mode > 0 && !errors.Is(err, admission.ErrPaused)) {
				t.Fatalf("expected safe rejection: %v", err)
			}
			artifacts, err := broker.List(context.Background())
			if err != nil || len(artifacts) != 0 || p.counts.Load() != 0 || p.generations.Load() != 0 {
				t.Fatalf("rejection reached durable claim or provider: artifacts=%d counts=%d generations=%d err=%v",
					len(artifacts), p.counts.Load(), p.generations.Load(), err)
			}
		})
	}
}

func TestLangfuseFailurePreservesDurableResultAndDoesNotRetryGeneration(t *testing.T) {
	b := newBrokerFixture(t)
	var exports atomic.Int64
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exports.Add(1)
		body, err := io.ReadAll(io.LimitReader(r.Body, 65<<10))
		if err != nil || r.URL.Path != "/api/public/otel/v1/traces" ||
			strings.Contains(string(body), "private-prompt-fixture") || strings.Contains(string(body), "private-response-fixture") {
			t.Error("telemetry included task content or invalid endpoint")
		}
		http.Error(w, "scripted unavailable", http.StatusServiceUnavailable)
	}))
	defer collector.Close()
	exporter, err := observability.NewLangfuse(observability.Config{Endpoint: collector.URL + "/api/public/otel/v1/traces",
		PublicKey: "fixture-public", SecretKey: "fixture-secret", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	p := &fixtureProvider{}
	c := newNode(t, b.client(t, b.tokenA), p, &fixtureResources{}, exporter)
	waitStatus(t, c, "ready")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	first, err := c.Run(ctx, fixtureSpec())
	if err != nil || !first.Durable || first.Reused || first.Telemetry.Status != "unconfirmed" || first.Telemetry.Error == nil {
		t.Fatalf("telemetry failure changed generation result: %+v, %v", first, err)
	}
	again, err := c.Run(ctx, fixtureSpec())
	if err != nil || !again.Durable || !again.Reused || again.Response.Text != first.Response.Text ||
		again.Telemetry.Status != "unconfirmed" || p.generations.Load() != 1 || p.counts.Load() != 1 || exports.Load() != 2 {
		t.Fatalf("telemetry failure triggered regeneration or export retry: %+v, %v", again, err)
	}
}

func TestUnknownGenerationRemainsPendingAcrossHTTPSNodes(t *testing.T) {
	b := newBrokerFixture(t)
	p := &fixtureProvider{generateErr: errors.New("scripted connection lost")}
	a := newNode(t, b.client(t, b.tokenA), p, &fixtureResources{}, nil)
	z := newNode(t, b.client(t, b.tokenB), p, &fixtureResources{}, nil)
	waitStatus(t, a, "ready")
	waitStatus(t, z, "ready")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := a.Run(ctx, fixtureSpec()); err == nil {
		t.Fatal("unknown generation was accepted")
	}
	if _, err := z.Run(ctx, fixtureSpec()); !errors.Is(err, inference.ErrInFlight) || p.generations.Load() != 1 || p.counts.Load() != 1 {
		t.Fatalf("other node replayed an uncertain generation: %v", err)
	}
}

type scriptedPaidProvider struct{ *fixtureProvider }

func (*scriptedPaidProvider) SelfHosted() bool { return false }

func TestHTTPSNodesShareDailyDollarReservationBeforeScriptedGeneration(t *testing.T) {
	b := newBrokerFixture(t)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	p := &scriptedPaidProvider{&fixtureProvider{release: release}}
	a := newNode(t, b.client(t, b.tokenA), p, &fixtureResources{}, nil, true)
	z := newNode(t, b.client(t, b.tokenB), p, &fixtureResources{}, nil, true)
	waitStatus(t, a, "ready")
	waitStatus(t, z, "ready")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	first, second := fixtureSpec(), fixtureSpec()
	first.Task, second.Task = "independent-task-a", "independent-task-b"
	for _, spec := range []*inference.Spec{&first, &second} {
		spec.Provider = "openai" // Scripted provider only; no API endpoint or key.
		spec.MaxInputTokens = 24
		spec.Prices = &inference.Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
		spec.MaxCostUSD, spec.DailyBudgetUSD = .000056, .000056
	}
	type answer struct {
		result execution.Result
		err    error
	}
	done := make(chan answer, 2)
	go func() { result, err := a.Run(ctx, first); done <- answer{result, err} }()
	go func() { result, err := z.Run(ctx, second); done <- answer{result, err} }()
	// The winning generation is held open, so the other node must see the
	// outstanding reservation rather than a conveniently settled fixture bill.
	var blocked answer
	select {
	case blocked = <-done:
	case <-ctx.Done():
		t.Fatal("neither node resolved the shared reservation")
	}
	if !errors.Is(blocked.err, inference.ErrBudgetExceeded) {
		t.Fatalf("daily cap did not reject the second fresh task: %v", blocked.err)
	}
	unblock()
	var completed answer
	select {
	case completed = <-done:
	case <-ctx.Done():
		t.Fatal("winning scripted generation did not complete")
	}
	if completed.err != nil || !completed.result.Durable || completed.result.Reused || p.generations.Load() != 1 || p.counts.Load() != 2 {
		t.Fatalf("shared cap generated twice or lost the completed result: %+v, %v counts=%d generations=%d",
			completed.result, completed.err, p.counts.Load(), p.generations.Load())
	}
}

func TestHTTPSNodesShareTokenQuotaWithLocalOrBothBudgetPolicies(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hosted     bool
		dollarCap  float64
		tokenScale int64
	}{
		{"local_tokens", false, 0, 1},
		{"hosted_token_limit_first", true, .000112, 1},
		{"hosted_dollar_limit_first", true, .000056, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBrokerFixture(t)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			p := &fixtureProvider{release: release}
			var provider inference.Provider = p
			if tc.hosted {
				provider = &scriptedPaidProvider{p}
			}
			a := newNode(t, b.client(t, b.tokenA), provider, &fixtureResources{}, nil, tc.hosted)
			z := newNode(t, b.client(t, b.tokenB), provider, &fixtureResources{}, nil, tc.hosted)
			waitStatus(t, a, "ready")
			waitStatus(t, z, "ready")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			first, second := fixtureSpec(), fixtureSpec()
			first.Task, second.Task = "budgeted-task-a", "budgeted-task-b"
			first.Model, second.Model = "scripted-model-a", "scripted-model-b"
			for _, spec := range []*inference.Spec{&first, &second} {
				spec.MaxInputTokens = 24
				spec.DailyTokenBudget = &inference.TokenBudget{InputTokens: 24 * tc.tokenScale,
					OutputTokens: 32 * tc.tokenScale, TotalTokens: 56 * tc.tokenScale}
				if tc.hosted {
					spec.Provider = "openai" // Scripted; no endpoint or key exists.
					spec.Prices = &inference.Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
					spec.MaxCostUSD, spec.DailyBudgetUSD = .000056, tc.dollarCap
				}
			}
			type answer struct {
				result execution.Result
				err    error
				spec   inference.Spec
			}
			done := make(chan answer, 2)
			go func() { result, err := a.Run(ctx, first); done <- answer{result, err, first} }()
			go func() { result, err := z.Run(ctx, second); done <- answer{result, err, second} }()
			var blocked answer
			select {
			case blocked = <-done:
			case <-ctx.Done():
				t.Fatal("budget contention did not reject one task before generation")
			}
			if !errors.Is(blocked.err, inference.ErrBudgetExceeded) {
				t.Fatalf("fresh task bypassed token/dollar reservation: %v", blocked.err)
			}
			unblock()
			var completed answer
			select {
			case completed = <-done:
			case <-ctx.Done():
				t.Fatal("winning budgeted task did not complete")
			}
			if completed.err != nil || !completed.result.Durable || p.generations.Load() != 1 || p.counts.Load() != 2 {
				t.Fatalf("unexpected budgeted result: %+v %v counts=%d generations=%d", completed.result, completed.err, p.counts.Load(), p.generations.Load())
			}
			// Completed reuse is still allowed and consumes no new quota.
			again, err := z.Run(ctx, completed.spec)
			if err != nil || !again.Reused || again.ThisRunUsage != (inference.Usage{}) || p.generations.Load() != 1 || p.counts.Load() != 2 {
				t.Fatalf("quota blocked reuse or charged it as generation: %+v %v", again, err)
			}
			key := "inference/v1/token-budget/" + time.Now().UTC().Format("2006-01-02")
			artifact, err := b.client(t, b.tokenA).Get(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			var ledger struct {
				Entries map[string]struct {
					Actual *inference.TokenBudget `json:"actual"`
				} `json:"entries"`
			}
			if json.Unmarshal([]byte(artifact.Content), &ledger) != nil {
				t.Fatal("invalid token-ledger fixture readback")
			}
			var total inference.TokenBudget
			for _, entry := range ledger.Entries {
				if entry.Actual == nil {
					t.Fatal("known result left its token reservation unsettled")
				}
				total.InputTokens += entry.Actual.InputTokens
				total.OutputTokens += entry.Actual.OutputTokens
				total.TotalTokens += entry.Actual.TotalTokens
			}
			if total != (inference.TokenBudget{InputTokens: 24, OutputTokens: 3, TotalTokens: 27}) {
				t.Fatalf("quota did not settle to inclusive actual usage: %+v", total)
			}
		})
	}
}
