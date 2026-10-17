package admission

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type testClock struct{ n atomic.Int64 }

func (c *testClock) now() time.Time { return time.Unix(0, c.n.Load()) }
func (c *testClock) advance()       { c.n.Add(int64(10 * time.Millisecond)) }
func testConfig() Config {
	c := DefaultConfig()
	c.RecoverySamples = 2
	c.SampleInterval = 10 * time.Millisecond
	c.RampInterval = 10 * time.Millisecond
	c.MaxSampleAge = time.Second
	c.ProbeTimeout = 100 * time.Millisecond
	c.QueueRowsPerWorker = 2
	return c
}
func healthy(at time.Time) Snapshot {
	return Snapshot{At: at, Known: true, Source: "fixture", CPUCapacityMilli: 8000, LoadCapacityMilli: 8000, LoadMilli: 1000, AvailableMemoryBytes: 2 << 30, MemoryPressure: Normal}
}
func fixtureScheduler(t *testing.T, execute Executor) (*Scheduler, *testClock) {
	t.Helper()
	s, e := New(testConfig(), execute)
	if e != nil {
		t.Fatal(e)
	}
	clock := &testClock{}
	clock.n.Store(int64(time.Second))
	s.now = clock.now
	return s, clock
}
func tick(s *Scheduler, c *testClock)  { c.advance(); s.Update(healthy(c.now())) }
func ready(s *Scheduler, c *testClock) { tick(s, c); tick(s, c) }
func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("bounded wait expired")
		var v T
		return v
	}
}

func TestPressurePreservesPendingAndInflightAndProtectsBroker(t *testing.T) {
	started := make(chan string, 8)
	release := make(chan struct{})
	s, c := fixtureScheduler(t, func(ctx context.Context, j Job) error {
		started <- j.ID
		if j.ID == "running" {
			<-release
		}
		return nil
	})
	ready(s, c)
	first, e := s.Submit(context.Background(), Job{"running", LaneInference, []byte("approved")})
	if e != nil {
		t.Fatal(e)
	}
	s.dispatch(context.Background())
	if receive(t, started) != "running" {
		t.Fatal("wrong job")
	}
	pending, e := s.Submit(context.Background(), Job{"waiting", LaneInference, nil})
	if e != nil {
		t.Fatal(e)
	}
	c.advance()
	x := healthy(c.now())
	x.MemoryPressure = High
	s.Update(x)
	s.dispatch(context.Background())
	if s.Status().ExpensiveWorkerLimit != 0 || s.Status().Active != 1 || len(s.Pending()) != 1 {
		t.Fatal(s.Status())
	}
	if _, e = s.Submit(context.Background(), Job{"no-admission", LaneInference, nil}); !errors.Is(e, ErrPaused) {
		t.Fatal(e)
	}
	broker, e := s.Submit(context.Background(), Job{"health", LaneBroker, nil})
	if e != nil {
		t.Fatal(e)
	}
	s.dispatch(context.Background())
	if receive(t, started) != "health" {
		t.Fatal("protected lane blocked")
	}
	receive(t, broker)
	close(release)
	if receive(t, first).Status != "completed" {
		t.Fatal("pressure killed in-flight work")
	}
	s.dispatch(context.Background())
	select {
	case <-pending:
		t.Fatal("pressure discarded queued work")
	default:
	}
	tick(s, c)
	if s.Status().ExpensiveWorkerLimit != 0 {
		t.Fatal("recovered without consecutive samples")
	}
	tick(s, c)
	s.dispatch(context.Background())
	if receive(t, started) != "waiting" {
		t.Fatal("queued work lost")
	}
	receive(t, pending)
}

func TestHysteresisRampQuotasAndStaleReadings(t *testing.T) {
	s, c := fixtureScheduler(t, func(context.Context, Job) error { return nil })
	ready(s, c)
	if s.Status().ExpensiveWorkerLimit != 1 {
		t.Fatal(s.Status())
	}
	tick(s, c)
	if s.Status().ExpensiveWorkerLimit != 2 {
		t.Fatal("did not ramp one step")
	}
	// A replayed observation cannot count as a new recovery observation.
	s.Update(s.Status().Snapshot)
	if s.Status().ExpensiveWorkerLimit != 2 {
		t.Fatal("replayed observation ramped")
	}
	c.advance()
	x := healthy(c.now())
	x.LoadMilli = 8000
	s.Update(x)
	if s.Status().ExpensiveWorkerLimit != 2 {
		t.Fatal("hysteresis band oscillated")
	}
	c.advance()
	x = healthy(c.now())
	x.CPUCapacityMilli = 450
	s.Update(x)
	if s.Status().ExpensiveWorkerLimit != 1 {
		t.Fatal("quota shrink ignored")
	}
	c.n.Add(int64(2 * time.Second))
	if s.Status().ExpensiveWorkerLimit != 0 {
		t.Fatal("stale readings admitted work")
	}
	tick(s, c)
	if s.Status().ExpensiveWorkerLimit != 0 {
		t.Fatal("stale recovery bypassed samples")
	}
	tick(s, c)
	if s.Status().ExpensiveWorkerLimit != 1 {
		t.Fatal("fresh recovery failed")
	}
	c.advance()
	x = healthy(c.now())
	x.MemoryPressure = Critical
	s.Update(x)
	if s.Status().ExpensiveWorkerLimit != 0 {
		t.Fatal("minimum workers overrode pressure")
	}
}

func TestDynamicAndAbsoluteQueueLimitsProtectReservedSlots(t *testing.T) {
	s, c := fixtureScheduler(t, func(context.Context, Job) error { t.Fatal("executor invoked before Run"); return nil })
	ready(s, c)
	for _, id := range []string{"a", "b"} {
		if _, e := s.Submit(context.Background(), Job{id, LaneRevision, nil}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Submit(context.Background(), Job{"full", LaneRevision, nil}); !errors.Is(e, ErrQueueFull) {
		t.Fatal(e)
	}
	for _, id := range []string{"p1", "p2", "p3", "p4"} {
		if _, e := s.Submit(context.Background(), Job{id, LaneBroker, nil}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Submit(context.Background(), Job{"p5", LaneBroker, nil}); !errors.Is(e, ErrQueueFull) {
		t.Fatal(e)
	}
	if len(s.Pending()) != 6 || s.Status().QueuedBytes > s.config.MaxQueueBytes {
		t.Fatal(s.Status())
	}
	// Lower dynamic headroom never evicts an accepted task or expands the hard ceiling.
	c.advance()
	x := healthy(c.now())
	x.AvailableMemoryBytes = 0
	s.Update(x)
	if len(s.Pending()) != 6 {
		t.Fatal("headroom shrink discarded work")
	}
	config := testConfig()
	config.MaxQueueBytes = 2048
	config.ProtectedQueueBytes = 1024
	s2, e := New(config, func(context.Context, Job) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	s2.now = c.now
	ready(s2, c)
	if _, e = s2.Submit(context.Background(), Job{"bytes", LaneRevision, make([]byte, 1024)}); !errors.Is(e, ErrQueueFull) {
		t.Fatal("copied payload exceeded byte cap", e)
	}
}

func TestReservationsLaneContentionCancellationAndPayloadOwnership(t *testing.T) {
	started := make(chan string, 4)
	release := make(chan struct{})
	s, c := fixtureScheduler(t, func(ctx context.Context, j Job) error { started <- string(j.Payload); <-release; return ctx.Err() })
	ready(s, c)
	tick(s, c)
	tick(s, c)
	ctx, cancel := context.WithCancel(context.Background())
	payload := []byte("first")
	future, e := s.Submit(ctx, Job{"one", LaneInference, payload})
	if e != nil {
		t.Fatal(e)
	}
	payload[0] = 'X'
	s.dispatch(context.Background())
	if receive(t, started) != "first" {
		t.Fatal("caller mutated copied payload")
	}
	second, e := s.Submit(context.Background(), Job{"two", LaneInference, []byte("second")})
	if e != nil {
		t.Fatal(e)
	}
	s.dispatch(context.Background())
	cancel()
	if s.Status().ActiveByLane[LaneInference] != 1 {
		t.Fatal("cancellation released active reservation early")
	}
	select {
	case <-started:
		t.Fatal("shared model concurrency exceeded one")
	default:
	}
	close(release)
	if receive(t, future).Status != "cancelled" {
		t.Fatal("cancel not propagated")
	}
	s.dispatch(context.Background())
	if receive(t, started) != "second" {
		t.Fatal("second job not dispatched")
	}
	receive(t, second)
	ctx, cancel = context.WithCancel(context.Background())
	queued, e := s.Submit(ctx, Job{"cancel-queued", LaneRevision, nil})
	if e != nil {
		t.Fatal(e)
	}
	cancel()
	if receive(t, queued).Status != "cancelled" || len(s.Pending()) != 0 {
		t.Fatal("queued cancellation lost outcome")
	}
}

func TestUnknownSignalsDoNotBlockProtectedWorkOrPretendToFit(t *testing.T) {
	s, c := fixtureScheduler(t, func(context.Context, Job) error { return nil })
	if _, e := s.Submit(context.Background(), Job{"unknown", LaneInference, nil}); !errors.Is(e, ErrPaused) {
		t.Fatal(e)
	}
	cheap, e := s.Submit(context.Background(), Job{"persist", LaneBroker, nil})
	if e != nil {
		t.Fatal(e)
	}
	s.dispatch(context.Background())
	receive(t, cheap)
	ready(s, c)
	c.advance()
	x := healthy(c.now())
	x.CPUCapacityMilli = 500
	s.Update(x)
	if _, e = s.Submit(context.Background(), Job{"large", LaneInference, nil}); !errors.Is(e, ErrDoesNotFit) {
		t.Fatal(e)
	}
	if _, e = s.Submit(context.Background(), Job{"small", LaneRevision, nil}); e != nil {
		t.Fatal(e)
	}
	c.advance()
	x = healthy(c.now())
	x.Known = false
	s.Update(x)
	if s.Status().ExpensiveWorkerLimit != 0 {
		t.Fatal("unavailable signals allowed work")
	}
}

func TestRunCancellationRetainsQueueAndSingleOwner(t *testing.T) {
	s, c := fixtureScheduler(t, func(context.Context, Job) error { t.Fatal("cancelled Run executed pending job"); return nil })
	ready(s, c)
	if _, e := s.Submit(context.Background(), Job{"pending", LaneRevision, nil}); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := s.Run(ctx, SourceFunc(func(ctx context.Context) (Snapshot, error) { return Snapshot{}, ctx.Err() }))
	if !errors.Is(e, context.Canceled) || len(s.Pending()) != 1 {
		t.Fatal(e, s.Status())
	}
	ctx, cancel = context.WithCancel(context.Background())
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.Run(ctx, SourceFunc(func(ctx context.Context) (Snapshot, error) {
			close(entered)
			<-ctx.Done()
			return Snapshot{}, ctx.Err()
		}))
	}()
	receive(t, entered)
	if e = s.Run(context.Background(), SystemSource()); !errors.Is(e, ErrRunning) {
		t.Fatal(e)
	}
	cancel()
	receive(t, done)
}

func TestInvalidConfigurationNumericsAndInput(t *testing.T) {
	for _, edit := range []func(*Config){func(c *Config) { c.MaxWorkers = 1025 }, func(c *Config) { c.ProtectedMemoryBytes = math.MaxInt64; c.MemoryHeadroomBytes = 1 }, func(c *Config) { c.Lanes[0].MemoryBytes = math.MaxInt64 }, func(c *Config) { c.MinWorkers = c.MaxWorkers }, func(c *Config) { c.QueueMemoryPercent = 101 }, func(c *Config) { c.CPUStallHighMilliPercent = 100001 }, func(c *Config) { c.Lanes[1].Name = LaneBroker }} {
		c := testConfig()
		edit(&c)
		if _, e := New(c, func(context.Context, Job) error { return nil }); !errors.Is(e, ErrConfiguration) {
			t.Fatal("unsafe configuration accepted")
		}
	}
	for _, data := range []string{`{"max_workers":1e309}`, `{"max_workers":NaN}`, `{"unknown":1}`, strings.Repeat(" ", 65537)} {
		if _, e := LoadConfig(strings.NewReader(data)); e == nil {
			t.Fatal("bad config accepted")
		}
	}
	b, _ := json.Marshal(testConfig())
	if _, e := LoadConfig(strings.NewReader(string(b))); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{strings.Replace(string(b), `"max_workers":5`, `"max_workers":5,"max_workers":5`, 1), strings.Replace(string(b), `"min_workers":1`, `"min_workers":null`, 1), strings.Replace(string(b), `"protected":false`, `"protected":false,"protected":false`, 1), strings.Replace(string(b), `"min_workers":1,`, "", 1)} {
		if _, e := LoadConfig(strings.NewReader(bad)); e == nil {
			t.Fatal("ambiguous or partial runtime policy accepted")
		}
	}
}

func TestKnownPayloadMustFitWorkerReservation(t *testing.T) {
	c := testConfig()
	c.Lanes[3].MemoryBytes = 1
	s, e := New(c, func(context.Context, Job) error { t.Fatal("underreserved payload executed"); return nil })
	if e != nil {
		t.Fatal(e)
	}
	clock := &testClock{}
	clock.n.Store(int64(time.Second))
	s.now = clock.now
	ready(s, clock)
	if _, e = s.Submit(context.Background(), Job{"payload", LaneRevision, make([]byte, 1<<20)}); !errors.Is(e, ErrDoesNotFit) {
		t.Fatal(e)
	}
	if s.Status().QueuedRows != 0 {
		t.Fatal("underreserved payload queued")
	}
}

func TestRunningReservationsLeaveQueueMemoryAndRecoveryRamps(t *testing.T) {
	c := testConfig()
	c.MinWorkers = 4
	started, release := make(chan struct{}), make(chan struct{})
	s, e := New(c, func(context.Context, Job) error { close(started); <-release; return nil })
	if e != nil {
		t.Fatal(e)
	}
	clock := &testClock{}
	clock.n.Store(int64(time.Second))
	s.now = clock.now
	ready(s, clock)
	if s.Status().ExpensiveWorkerLimit != 1 {
		t.Fatal("minimum goal bypassed safe ramp")
	}
	x := healthy(clock.now())
	x.AvailableMemoryBytes = c.MemoryHeadroomBytes + c.ProtectedMemoryBytes + 32*MiB
	clock.advance()
	x.At = clock.now()
	s.Update(x)
	if s.Status().PotentialWorkers != 1 {
		t.Fatal("queue allowance not deducted before worker capacity", s.Status())
	}
	future, e := s.Submit(context.Background(), Job{"one", LaneRevision, nil})
	if e != nil {
		t.Fatal(e)
	}
	s.dispatch(context.Background())
	receive(t, started)
	defer func() { close(release); receive(t, future) }()
	clock.advance()
	x.At = clock.now()
	x.AvailableMemoryBytes = c.MemoryHeadroomBytes + c.ProtectedMemoryBytes + 8*MiB
	s.Update(x)
	if s.Status().DynamicQueueBytes != 0 || s.Status().Active != 1 {
		t.Fatal("new queue memory admitted while running estimate exceeds shrunken budget", s.Status())
	}
	if _, e = s.Submit(context.Background(), Job{"blocked", LaneRevision, nil}); !errors.Is(e, ErrPaused) {
		t.Fatal(e)
	}
	c.ProtectedMemoryBytes = c.Lanes[0].MemoryBytes
	if c.Validate() == nil {
		t.Fatal("protected allowance omitted its queue bytes")
	}
}

func TestPanicIsReportedOnceWithoutRetry(t *testing.T) {
	var calls atomic.Int64
	s, c := fixtureScheduler(t, func(context.Context, Job) error { calls.Add(1); panic("private payload must not become an error") })
	ready(s, c)
	future, e := s.Submit(context.Background(), Job{"panic", LaneRevision, nil})
	if e != nil {
		t.Fatal(e)
	}
	s.dispatch(context.Background())
	o := receive(t, future)
	if !errors.Is(o.Err, ErrExecutorPanic) || calls.Load() != 1 || s.Status().Active != 0 {
		t.Fatal(o)
	}
}

func TestDelayedCancellationCannotCancelAReusedID(t *testing.T) {
	s, c := fixtureScheduler(t, func(context.Context, Job) error { return nil })
	ready(s, c)
	first, e := s.Submit(context.Background(), Job{"same-id", LaneRevision, nil})
	if e != nil {
		t.Fatal(e)
	}
	oldEntry := s.queue[0]
	s.dispatch(context.Background())
	receive(t, first)
	second, e := s.Submit(context.Background(), Job{"same-id", LaneRevision, nil})
	if e != nil {
		t.Fatal(e)
	}
	// A cancellation callback can already be waiting for the mutex when the
	// prior task completes. Match the entry, never merely its reusable ID.
	s.cancelQueued(oldEntry)
	if len(s.Pending()) != 1 {
		t.Fatal("old cancellation removed a different task with the same ID")
	}
	s.dispatch(context.Background())
	if receive(t, second).Status != "completed" {
		t.Fatal("replacement task lost")
	}
}
