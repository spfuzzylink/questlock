package execution

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/observability"
)

type fixtureProvider struct {
	calls, counts atomic.Int64
	generate      func(context.Context, inference.Request) (inference.Response, error)
}

func (*fixtureProvider) Identity() string { return "synthetic-local-provider" }
func (*fixtureProvider) SelfHosted() bool { return true }
func (p *fixtureProvider) CountInputTokens(context.Context, inference.Request) (int64, error) {
	p.counts.Add(1)
	return 10, nil
}
func (p *fixtureProvider) Generate(ctx context.Context, req inference.Request) (inference.Response, error) {
	p.calls.Add(1)
	if p.generate != nil {
		return p.generate(ctx, req)
	}
	return fixtureResponse(), nil
}

type undeclaredProvider struct{ p *fixtureProvider }

func (p undeclaredProvider) Identity() string { return p.p.Identity() }
func (p undeclaredProvider) CountInputTokens(ctx context.Context, req inference.Request) (int64, error) {
	return p.p.CountInputTokens(ctx, req)
}
func (p undeclaredProvider) Generate(ctx context.Context, req inference.Request) (inference.Response, error) {
	return p.p.Generate(ctx, req)
}

func fixtureResponse() inference.Response {
	return inference.Response{Model: "fixture:local", Text: "checked fixture answer", Complete: true,
		Usage: inference.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 4}}
}

func fixtureSpec() inference.Spec {
	return inference.Spec{Task: "fixture", Revision: "snapshot-1", Provider: "ollama", Model: "fixture:local",
		Instructions: "Use only supplied facts.", Input: "Synthetic example.", MaxInputTokens: 1000,
		MaxOutputTokens: 32, MaxContextBytes: 1024, ReuseForSeconds: 3600}
}

func fixtureConfig(t *testing.T) (Config, *fixtureProvider) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	token, err := s.CreateToken(context.Background(), "execution-test", "synthetic-tests")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(s))
	t.Cleanup(server.Close)
	broker, err := client.New(server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	p := &fixtureProvider{}
	cfg := admission.DefaultConfig()
	cfg.RecoverySamples = 1
	cfg.SampleInterval, cfg.RampInterval = 10*time.Millisecond, 10*time.Millisecond
	return Config{Runner: inference.Runner{Broker: broker, Provider: p}, Admission: cfg,
		Source: admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
			return healthySnapshot(), nil
		})}, p
}

func healthySnapshot() admission.Snapshot {
	return admission.Snapshot{At: time.Now(), Known: true, Source: "synthetic-test", CPUCapacityMilli: 8000,
		LoadCapacityMilli: 8000, AvailableMemoryBytes: 8 << 30, MemoryPressure: admission.Normal}
}

func coordinator(t *testing.T, cfg Config) *Coordinator {
	t.Helper()
	c, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := c.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	waitFor(t, func() bool { return c.Status().Reason == "ready" })
	return c
}

func waitFor(t *testing.T, ready func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-ctx.Done():
			t.Fatal("fixture condition did not become ready")
		case <-ticker.C:
		}
	}
}

func TestReuseUsesStableTaskAndReleasesTransientRegistry(t *testing.T) {
	cfg, p := fixtureConfig(t)
	c := coordinator(t, cfg)
	first, err := c.Run(context.Background(), fixtureSpec())
	if err != nil {
		t.Fatal(err)
	}
	for range 8 {
		reused, err := c.Run(context.Background(), fixtureSpec())
		if err != nil || !reused.Reused || reused.Fingerprint != first.Fingerprint || reused.ThisRunUsage != (inference.Usage{}) {
			t.Fatalf("same task regenerated: %+v, %v", reused, err)
		}
	}
	if p.calls.Load() != 1 || p.counts.Load() != 1 || !first.Durable || first.Telemetry.Status != "disabled" {
		t.Fatalf("unexpected calls/result: %d, %d, %+v", p.calls.Load(), p.counts.Load(), first)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) != 0 || len(c.slots) != 0 {
		t.Fatal("completed task registry retained state")
	}
}

func TestNoHostedExecutionWithoutBothExplicitAssertions(t *testing.T) {
	for _, tc := range []struct {
		name                                  string
		specHosted, providerUndeclared, allow bool
		want                                  error
	}{
		{"hosted-spec", true, false, false, ErrHostedDisabled},
		{"undeclared-provider", false, true, false, ErrHostedDisabled},
		{"explicit-hosted-opt-in", true, true, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, p := fixtureConfig(t)
			cfg.AllowHosted = tc.allow
			if tc.providerUndeclared {
				cfg.Runner.Provider = undeclaredProvider{p}
			}
			c := coordinator(t, cfg)
			spec := fixtureSpec()
			if tc.specHosted {
				spec.Provider = "openai"
			}
			_, err := c.Run(context.Background(), spec)
			if !errors.Is(err, tc.want) {
				t.Fatalf("wanted %v, got %v", tc.want, err)
			}
			if tc.want != nil && (p.calls.Load() != 0 || p.counts.Load() != 0) {
				t.Fatal("denied task reached provider")
			}
		})
	}
}

func TestPressureRejectsBeforeAnyProviderOrBrokerClaim(t *testing.T) {
	cfg, p := fixtureConfig(t)
	cfg.Source = admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		s := healthySnapshot()
		s.MemoryPressure = admission.Critical
		return s, nil
	})
	c, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	waitFor(t, func() bool { return c.Status().Reason == "resource_pressure" })
	_, err = c.Run(context.Background(), fixtureSpec())
	if !errors.Is(err, admission.ErrPaused) || p.calls.Load() != 0 || p.counts.Load() != 0 {
		t.Fatalf("paused work escaped: %v", err)
	}
	plan, _ := inference.BuildPlan(fixtureSpec())
	if plan.Fingerprint == "" {
		t.Fatal("invalid fixture")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) != 0 {
		t.Fatal("rejected task retained registry entry")
	}
}

func TestQueuedSpecIsFrozenAndCancellationCallsNoProvider(t *testing.T) {
	cfg, p := fixtureConfig(t)
	entered, release := make(chan inference.Request, 3), make(chan struct{})
	p.generate = func(ctx context.Context, req inference.Request) (inference.Response, error) {
		entered <- req
		select {
		case <-release:
			return fixtureResponse(), nil
		case <-ctx.Done():
			return inference.Response{}, ctx.Err()
		}
	}
	c := coordinator(t, cfg)
	firstDone := make(chan error, 1)
	go func() { _, err := c.Run(context.Background(), fixtureSpec()); firstDone <- err }()
	<-entered
	spec := fixtureSpec()
	spec.Revision = "queued-snapshot"
	spec.Context = []inference.ContextItem{{ID: "fixed", Text: "original fact", Required: true}}
	spec.Ollama = &inference.OllamaOptions{Format: "json", OutputTypes: map[string]string{"quantity": "integer"}}
	secondDone := make(chan error, 1)
	go func() { _, err := c.Run(context.Background(), spec); secondDone <- err }()
	waitFor(t, func() bool { return c.Status().QueuedRows == 1 })
	spec.Context[0].Text = "mutated fact"
	spec.Ollama.OutputTypes["quantity"] = "string"
	queuedCtx, cancel := context.WithCancel(context.Background())
	thirdDone := make(chan error, 1)
	third := fixtureSpec()
	third.Revision = "cancelled-before-claim"
	go func() { _, err := c.Run(queuedCtx, third); thirdDone <- err }()
	waitFor(t, func() bool { return c.Status().QueuedRows == 2 })
	cancel()
	if err := <-thirdDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancellation: %v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	req := <-entered
	if !strings.Contains(req.Input, "original fact") || strings.Contains(req.Input, "mutated fact") || req.Ollama.OutputTypes["quantity"] != "integer" {
		t.Fatalf("queued task used caller mutation: %+v", req)
	}
	if p.calls.Load() != 2 {
		t.Fatalf("queued cancellation generated: %d", p.calls.Load())
	}
}

func TestLifecycleCancelsActiveAndQueuedCalls(t *testing.T) {
	cfg, p := fixtureConfig(t)
	entered := make(chan struct{})
	p.generate = func(ctx context.Context, _ inference.Request) (inference.Response, error) {
		close(entered)
		<-ctx.Done()
		return inference.Response{}, ctx.Err()
	}
	life, stop := context.WithCancel(context.Background())
	c, err := New(life, cfg)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return c.Status().Reason == "ready" })
	activeDone, queuedDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := c.Run(context.Background(), fixtureSpec()); activeDone <- err }()
	<-entered
	queued := fixtureSpec()
	queued.Revision = "never-started"
	go func() { _, err := c.Run(context.Background(), queued); queuedDone <- err }()
	waitFor(t, func() bool { return c.Status().QueuedRows == 1 })
	stop()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-activeDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("active cancellation lost runner error: %v", err)
	}
	if err := <-queuedDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancellation: %v", err)
	}
	if p.calls.Load() != 1 {
		t.Fatal("shutdown generated queued task")
	}
	if _, err := c.Run(context.Background(), fixtureSpec()); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed accepted task: %v", err)
	}
}

func TestCloseDeadlineRetainsActiveCapacityAndPartialAnswer(t *testing.T) {
	cfg, p := fixtureConfig(t)
	entered, release := make(chan struct{}), make(chan struct{})
	p.generate = func(context.Context, inference.Request) (inference.Response, error) {
		close(entered)
		<-release
		return fixtureResponse(), nil
	}
	c := coordinator(t, cfg)
	done := make(chan struct {
		result Result
		err    error
	}, 1)
	go func() {
		result, err := c.Run(context.Background(), fixtureSpec())
		done <- struct {
			result Result
			err    error
		}{result, err}
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := c.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close ignored deadline: %v", err)
	}
	if c.Status().Active != 1 {
		t.Fatal("cancelled but running callback released its slot")
	}
	close(release)
	out := <-done
	if out.err == nil || out.result.Response.Text != "checked fixture answer" || out.result.Durable {
		t.Fatalf("partial answer lost: %+v, %v", out.result, out.err)
	}
}

func TestIncompleteAndUncertainDoNotAutomaticallyRetry(t *testing.T) {
	for _, incomplete := range []bool{true, false} {
		t.Run(strconvBool(incomplete), func(t *testing.T) {
			cfg, p := fixtureConfig(t)
			p.generate = func(context.Context, inference.Request) (inference.Response, error) {
				if !incomplete {
					return inference.Response{}, errors.New("synthetic uncertain transport")
				}
				r := fixtureResponse()
				r.Complete = false
				return r, nil
			}
			c := coordinator(t, cfg)
			first, err := c.Run(context.Background(), fixtureSpec())
			if err == nil {
				t.Fatal("invalid generation accepted")
			}
			want := inference.ErrInFlight
			if incomplete {
				want = inference.ErrIncomplete
				if !errors.Is(err, want) || !first.Durable || first.Response.Text == "" {
					t.Fatalf("incomplete result lost: %+v, %v", first, err)
				}
			}
			if _, err := c.Run(context.Background(), fixtureSpec()); !errors.Is(err, want) {
				t.Fatalf("wanted %v, got %v", want, err)
			}
			if p.calls.Load() != 1 {
				t.Fatal("generation automatically retried")
			}
		})
	}
}

func strconvBool(v bool) string {
	if v {
		return "incomplete"
	}
	return "uncertain"
}

type exporterFunc func(context.Context, observability.Execution) (observability.Receipt, error)

func (f exporterFunc) Export(ctx context.Context, e observability.Execution) (observability.Receipt, error) {
	return f(ctx, e)
}

func TestOptionalExportTimeoutAndPanicNeverChangeTaskSuccess(t *testing.T) {
	for _, panicExport := range []bool{false, true} {
		t.Run(strconvBool(panicExport), func(t *testing.T) {
			cfg, p := fixtureConfig(t)
			var exports atomic.Int64
			cfg.ExportTimeout = 5 * time.Millisecond
			cfg.Exporter = exporterFunc(func(ctx context.Context, e observability.Execution) (observability.Receipt, error) {
				exports.Add(1)
				data, _ := json.Marshal(e)
				if strings.Contains(string(data), "Synthetic example") {
					t.Error("prompt leaked to exporter")
				}
				if panicExport {
					panic("synthetic exporter panic")
				}
				<-ctx.Done()
				return observability.Receipt{TraceID: "synthetic-trace"}, ctx.Err()
			})
			c := coordinator(t, cfg)
			result, err := c.Run(context.Background(), fixtureSpec())
			if err != nil || !result.Durable || !result.Response.Complete || result.Telemetry.Status != "unconfirmed" || result.Telemetry.Error == nil {
				t.Fatalf("telemetry altered result: %+v, %v", result, err)
			}
			if exports.Load() != 1 || p.calls.Load() != 1 {
				t.Fatal("export failure retried execution")
			}
		})
	}
}

func TestConfigurationAndBoundedSnapshots(t *testing.T) {
	cfg, _ := fixtureConfig(t)
	if _, err := New(nil, cfg); !errors.Is(err, ErrConfiguration) {
		t.Fatal("nil lifecycle accepted")
	}
	bad := cfg
	bad.Runner.Provider = (*fixtureProvider)(nil)
	if _, err := New(context.Background(), bad); !errors.Is(err, ErrConfiguration) {
		t.Fatal("nil provider accepted")
	}
	bad = cfg
	bad.ExportTimeout = 6 * time.Second
	if _, err := New(context.Background(), bad); !errors.Is(err, ErrConfiguration) {
		t.Fatal("unbounded export accepted")
	}
	bad = cfg
	bad.Admission.Lanes = append([]admission.Lane(nil), cfg.Admission.Lanes...)
	for i := range bad.Admission.Lanes {
		if bad.Admission.Lanes[i].Name == admission.LaneInference {
			bad.Admission.Lanes[i].Name = "not-inference"
		}
	}
	if _, err := New(context.Background(), bad); !errors.Is(err, ErrConfiguration) {
		t.Fatal("missing inference lane accepted")
	}
	c := coordinator(t, cfg)
	if _, err := c.Run(nil, fixtureSpec()); !errors.Is(err, ErrConfiguration) {
		t.Fatal("nil task context accepted")
	}
	spec := fixtureSpec()
	spec.Input = strings.Repeat("x", MaxSpecBytes+1)
	if _, err := c.Run(context.Background(), spec); err == nil {
		t.Fatal("oversized snapshot accepted")
	}
	// Valid raw prompt size can still exceed the queue's encoded-byte limit.
	spec = fixtureSpec()
	spec.Input = strings.Repeat("\x00", 200000)
	if _, err := inference.BuildPlan(spec); err != nil {
		t.Fatalf("invalid size fixture: %v", err)
	}
	if _, err := c.Run(context.Background(), spec); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("oversized encoded snapshot accepted: %v", err)
	}
}

func TestQueueChargesWholeSnapshotAndRejectsExcessWithoutRetention(t *testing.T) {
	cfg, p := fixtureConfig(t)
	cfg.Admission.MaxWorkers = 2
	cfg.Admission.QueueRowsPerWorker = 1
	for i := range cfg.Admission.Lanes {
		cfg.Admission.Lanes[i].MaxWorkers = 1
	}
	entered, release := make(chan struct{}, 2), make(chan struct{})
	p.generate = func(ctx context.Context, _ inference.Request) (inference.Response, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return fixtureResponse(), nil
		case <-ctx.Done():
			return inference.Response{}, ctx.Err()
		}
	}
	c := coordinator(t, cfg)
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := c.Run(context.Background(), fixtureSpec()); firstDone <- err }()
	<-entered
	second := fixtureSpec()
	second.Revision = "bounded-queue"
	second.Input = strings.Repeat("synthetic context ", 128)
	go func() { _, err := c.Run(context.Background(), second); secondDone <- err }()
	waitFor(t, func() bool { return c.Status().QueuedRows == 1 })
	payload, _ := json.Marshal(second)
	if c.Status().QueuedBytes < int64(len(payload))+admission.QueueEntryBytes {
		t.Fatal("queue charged only a small operation ID")
	}
	third := fixtureSpec()
	third.Revision = "excess-call"
	if _, err := c.Run(context.Background(), third); !errors.Is(err, admission.ErrQueueFull) {
		t.Fatalf("excess queued: %v", err)
	}
	c.mu.Lock()
	retained := len(c.calls)
	c.mu.Unlock()
	if retained != 2 {
		t.Fatalf("registry retained rejected call: %d", retained)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 2 {
		t.Fatal("queue overflow reached provider")
	}
}
