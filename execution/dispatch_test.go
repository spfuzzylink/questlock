package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/protocol"
)

type dispatchAudit struct {
	inference.Broker
	writes atomic.Int64
}

func (b *dispatchAudit) Publish(ctx context.Context, r protocol.PublishRequest) (protocol.PublishResult, error) {
	b.writes.Add(1)
	return b.Broker.Publish(ctx, r)
}

func dispatchGate(t *testing.T, source admission.Source) inference.AdmissionFunc {
	t.Helper()
	a, err := delegation.NewResourceAdmission(delegation.ReuseAdmissionPolicy{}, source)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func readyDispatch(context.Context, inference.Spec) (func(), error) { return nil, nil }
func denyDispatch(context.Context, inference.Spec) (func(), error) {
	return nil, inference.ErrAdmissionDeferred
}

func newDispatch(t *testing.T, b inference.Broker, nodes []Node, opts DispatchOptions) *Dispatcher {
	t.Helper()
	d, err := NewDispatcher(b, nodes, opts)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDispatchDefersPressureToIdenticalPeerAndExactHitBypassesResources(t *testing.T) {
	cfg, first := fixtureConfig(t)
	second := &fixtureProvider{}
	var probes atomic.Int64
	a := dispatchGate(t, admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		probes.Add(1)
		s := healthySnapshot()
		s.MemoryPressure = admission.High
		return s, nil
	}))
	d := newDispatch(t, cfg.Runner.Broker, []Node{{"pressured", first, a}, {"ready", second, readyDispatch}}, DispatchOptions{})
	out, err := d.Run(t.Context(), fixtureSpec())
	if err != nil || out.SelectedNode != "ready" || !out.Durable || first.calls.Load() != 0 || second.calls.Load() != 1 || out.Nodes[0].Deferrals != 1 || out.Nodes[1].GenerationCalls != 1 {
		t.Fatalf("preclaim dispatch failed: %+v %v", out, err)
	}
	exact, err := d.Run(t.Context(), fixtureSpec())
	if err != nil || !exact.Reused || exact.SelectedNode != "pressured" || exact.Fingerprint != out.Fingerprint || probes.Load() != 1 || second.calls.Load() != 1 || exact.Nodes[1].Runs != 0 {
		t.Fatalf("exact reuse changed under pressure: %+v %v", exact, err)
	}
}

func TestDispatchWaitsForRecoveryAndBoundsAllDeferredWork(t *testing.T) {
	for _, recover := range []bool{true, false} {
		t.Run(strconvBool(recover), func(t *testing.T) {
			cfg, p := fixtureConfig(t)
			audit := &dispatchAudit{Broker: cfg.Runner.Broker}
			var probes atomic.Int64
			a := dispatchGate(t, admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
				s := healthySnapshot()
				if n := probes.Add(1); !recover || n < 2 {
					s.MemoryPressure = admission.Critical
				}
				return s, nil
			}))
			d := newDispatch(t, audit, []Node{{"local", p, a}}, DispatchOptions{MaxWait: 50 * time.Millisecond, PollInterval: 10 * time.Millisecond})
			out, err := d.Run(t.Context(), fixtureSpec())
			if recover {
				if err != nil || p.calls.Load() != 1 || out.Nodes[0].Runs < 2 || out.Nodes[0].Deferrals != 1 {
					t.Fatalf("recovery did not execute once: %+v %v", out, err)
				}
			} else if !errors.Is(err, inference.ErrAdmissionDeferred) || !errors.Is(err, context.DeadlineExceeded) || p.calls.Load() != 0 || p.counts.Load() != 0 || audit.writes.Load() != 0 || len(out.Nodes) != 1 || out.Nodes[0].Deferrals < 2 {
				t.Fatalf("bounded deferral created work: %+v %v", out, err)
			}
		})
	}
}

func TestDispatchCapacityDeadlineCannotCancelAdmittedGeneration(t *testing.T) {
	cfg, p := fixtureConfig(t)
	p.generate = func(ctx context.Context, _ inference.Request) (inference.Response, error) {
		timer := time.NewTimer(40 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return inference.Response{}, ctx.Err()
		case <-timer.C:
			return fixtureResponse(), nil
		}
	}
	d := newDispatch(t, cfg.Runner.Broker, []Node{{"local", p, readyDispatch}}, DispatchOptions{MaxWait: 10 * time.Millisecond})
	out, err := d.Run(t.Context(), fixtureSpec())
	if err != nil || !out.Durable || p.calls.Load() != 1 {
		t.Fatalf("capacity deadline cancelled generation: %+v %v", out, err)
	}
}

func TestDispatchRechecksCancellationAfterReadyResourceProbe(t *testing.T) {
	cfg, p := fixtureConfig(t)
	audit := &dispatchAudit{Broker: cfg.Runner.Broker}
	ctx, cancel := context.WithCancel(t.Context())
	a := dispatchGate(t, admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		cancel()
		return healthySnapshot(), nil
	}))
	d := newDispatch(t, audit, []Node{{"local", p, a}, {"peer", &fixtureProvider{}, readyDispatch}}, DispatchOptions{MaxWait: time.Second})
	_, err := d.Run(ctx, fixtureSpec())
	if !errors.Is(err, context.Canceled) || audit.writes.Load() != 0 || p.counts.Load() != 0 || p.calls.Load() != 0 || len(d.slots) != 0 {
		t.Fatal("cancelled probe claimed/generated or retained a slot", err)
	}
}

func TestDispatchDoesNotAdvanceAfterGenerationSentinelOrIncomplete(t *testing.T) {
	for _, mode := range []string{"admission_sentinel", "inflight_sentinel", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			cfg, first := fixtureConfig(t)
			peer := &fixtureProvider{}
			first.generate = func(context.Context, inference.Request) (inference.Response, error) {
				if mode == "incomplete" {
					r := fixtureResponse()
					r.Complete = false
					return r, nil
				}
				if mode == "inflight_sentinel" {
					return inference.Response{}, inference.ErrInFlight
				}
				return inference.Response{}, inference.ErrAdmissionDeferred
			}
			d := newDispatch(t, cfg.Runner.Broker, []Node{{"first", first, readyDispatch}, {"peer", peer, readyDispatch}}, DispatchOptions{MaxWait: time.Second, WaitForPeer: true})
			out, err := d.Run(t.Context(), fixtureSpec())
			if err == nil || inference.PreClaimDeferred(out.Result, err) || first.calls.Load() != 1 || peer.calls.Load() != 0 || out.Nodes[1].Runs != 0 || out.Nodes[0].GenerationCalls != 1 {
				t.Fatalf("execution error advanced to peer: %+v %v", out, err)
			}
		})
	}
}

func TestDispatchRechecksCapacityDeadlineBeforeClaimAndReleasesLease(t *testing.T) {
	cfg, p := fixtureConfig(t)
	audit := &dispatchAudit{Broker: cfg.Runner.Broker}
	var released atomic.Int64
	gate := func(ctx context.Context, _ inference.Spec) (func(), error) {
		<-ctx.Done()
		return func() { released.Add(1) }, nil
	}
	d := newDispatch(t, audit, []Node{{"local", p, gate}}, DispatchOptions{MaxWait: 20 * time.Millisecond})
	out, err := d.Run(t.Context(), fixtureSpec())
	if !errors.Is(err, context.DeadlineExceeded) || !inference.PreClaimDeferred(out.Result, err) || released.Load() != 1 || audit.writes.Load() != 0 || p.calls.Load() != 0 {
		t.Fatalf("late ready admission claimed work: %+v %v", out, err)
	}
}

type dispatchCountErrorProvider struct{ *fixtureProvider }

func (p dispatchCountErrorProvider) CountInputTokens(context.Context, inference.Request) (int64, error) {
	p.counts.Add(1)
	return 0, inference.ErrInFlight
}

func TestDispatchCountErrorCannotBecomePeerWait(t *testing.T) {
	cfg, p := fixtureConfig(t)
	d := newDispatch(t, cfg.Runner.Broker, []Node{{"local", dispatchCountErrorProvider{p}, readyDispatch}, {"peer", &fixtureProvider{}, readyDispatch}}, DispatchOptions{MaxWait: time.Second, WaitForPeer: true})
	out, err := d.Run(t.Context(), fixtureSpec())
	if !errors.Is(err, inference.ErrInFlight) || out.Nodes[0].Runs != 1 || out.Nodes[0].PeerPolls != 0 || out.Nodes[1].Runs != 0 || p.counts.Load() != 1 || p.calls.Load() != 0 {
		t.Fatalf("provider count error became peer wait: %+v %v", out, err)
	}
}

type dispatchRefreshErrorProvider struct {
	*fixtureProvider
	refreshes atomic.Int64
}

func (p *dispatchRefreshErrorProvider) RefreshIdentity(context.Context) error {
	p.refreshes.Add(1)
	return errors.Join(errors.New("synthetic runtime validation failed"), inference.ErrInFlight)
}

func TestDispatchRuntimeValidationErrorCannotBecomePeerWait(t *testing.T) {
	cfg, p := fixtureConfig(t)
	provider := &dispatchRefreshErrorProvider{fixtureProvider: p}
	d := newDispatch(t, cfg.Runner.Broker, []Node{{"local", provider, readyDispatch}, {"peer", &fixtureProvider{}, readyDispatch}}, DispatchOptions{MaxWait: time.Second, WaitForPeer: true})
	out, err := d.Run(t.Context(), fixtureSpec())
	if !errors.Is(err, inference.ErrInFlight) || provider.refreshes.Load() != 1 || out.Nodes[0].Runs != 1 || out.Nodes[0].PeerPolls != 0 || out.Nodes[1].Runs != 0 || p.counts.Load() != 0 || p.calls.Load() != 0 {
		t.Fatalf("runtime validation error became peer wait: %+v %v", out, err)
	}
}

func dispatchFence(t *testing.T, b inference.Broker, p inference.Provider, state string) string {
	t.Helper()
	plan, err := inference.BuildPlan(fixtureSpec())
	if err != nil {
		t.Fatal(err)
	}
	fp := sha256.Sum256([]byte(plan.Fingerprint + "\x00" + p.Identity()))
	fingerprint := hex.EncodeToString(fp[:])
	key := "inference/v1/jobs/" + fingerprint
	data, _ := json.Marshal(map[string]any{"schema": 1, "fingerprint": fingerprint, "request_fingerprint": plan.Fingerprint, "state": state, "created_at": time.Now().UTC()})
	if _, err := b.Publish(t.Context(), protocol.PublishRequest{Key: key, OperationID: "synthetic-dispatch-fence", Content: string(data)}); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestDispatchPendingAndUncertainNeverProbeOrReroute(t *testing.T) {
	for _, state := range []string{"pending", "uncertain"} {
		t.Run(state, func(t *testing.T) {
			cfg, p := fixtureConfig(t)
			dispatchFence(t, cfg.Runner.Broker, p, state)
			audit := &dispatchAudit{Broker: cfg.Runner.Broker}
			gate := func(context.Context, inference.Spec) (func(), error) {
				t.Error("ownership fence reached admission")
				return nil, nil
			}
			d := newDispatch(t, audit, []Node{{"local", p, gate}, {"peer", &fixtureProvider{}, gate}}, DispatchOptions{MaxWait: 25 * time.Millisecond, PollInterval: 10 * time.Millisecond, WaitForPeer: true})
			out, err := d.Run(t.Context(), fixtureSpec())
			if !errors.Is(err, inference.ErrInFlight) || inference.PreClaimDeferred(out.Result, err) || audit.writes.Load() != 0 || p.calls.Load() != 0 || out.Nodes[1].Runs != 0 || out.Nodes[0].PeerPolls == 0 {
				t.Fatalf("ownership fence bypassed: %+v %v", out, err)
			}
		})
	}
}

type dispatchIdentityProvider struct {
	*fixtureProvider
	identity string
}

func (p dispatchIdentityProvider) Identity() string { return p.identity }

func TestDispatchRejectsIdentityHostingLimitsAndOversizedSpec(t *testing.T) {
	cfg, p := fixtureConfig(t)
	for _, nodes := range [][]Node{
		nil,
		{{"local", p, nil}},
		{{"local", p, readyDispatch}, {"local", p, readyDispatch}},
		{{"local", p, readyDispatch}, {"other", dispatchIdentityProvider{&fixtureProvider{}, "different-endpoint-or-weights"}, readyDispatch}},
		{{"empty", dispatchIdentityProvider{&fixtureProvider{}, ""}, readyDispatch}},
		{{"hosted", undeclaredProvider{p}, readyDispatch}},
	} {
		if _, err := NewDispatcher(cfg.Runner.Broker, nodes, DispatchOptions{}); err == nil {
			t.Fatal("invalid nodes accepted")
		}
	}
	nodes := []Node{{"local", p, readyDispatch}}
	for _, opts := range []DispatchOptions{{MaxPending: 65}, {MaxPending: -1}, {MaxWait: -1}, {MaxWait: 25 * time.Hour}, {PollInterval: time.Millisecond}, {PollInterval: time.Minute + 1}} {
		if _, err := NewDispatcher(cfg.Runner.Broker, nodes, opts); err == nil {
			t.Fatal("invalid limits accepted")
		}
	}
	d := newDispatch(t, cfg.Runner.Broker, nodes, DispatchOptions{})
	s := fixtureSpec()
	s.Input = strings.Repeat("x", MaxSpecBytes)
	if _, err := d.Run(t.Context(), s); err == nil || p.calls.Load() != 0 {
		t.Fatal("oversized snapshot reached provider", err)
	}
	s = fixtureSpec()
	s.Provider = "openai"
	if _, err := d.Run(t.Context(), s); !errors.Is(err, ErrHostedDisabled) {
		t.Fatal("hosted spec accepted", err)
	}
}

type dispatchGetBroker struct {
	inference.Broker
	get func(context.Context, string) (protocol.Artifact, error)
}

func (b dispatchGetBroker) Get(ctx context.Context, key string) (protocol.Artifact, error) {
	return b.get(ctx, key)
}

func TestDispatchPeerWaitNeverClaimsAbsentOrExpiredResult(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(strconvBool(expired), func(t *testing.T) {
			cfg, p := fixtureConfig(t)
			key := dispatchFence(t, cfg.Runner.Broker, p, "pending")
			var reads atomic.Int64
			b := dispatchGetBroker{Broker: cfg.Runner.Broker, get: func(ctx context.Context, requested string) (protocol.Artifact, error) {
				if reads.Add(1) == 1 {
					return cfg.Runner.Broker.Get(ctx, requested)
				}
				if !expired {
					return protocol.Artifact{}, &client.Error{Code: "not_found"}
				}
				a, err := cfg.Runner.Broker.Get(ctx, requested)
				if err != nil {
					return a, err
				}
				var rec map[string]any
				_ = json.Unmarshal([]byte(a.Content), &rec)
				rec["state"], rec["response"] = "complete", fixtureResponse()
				rec["created_at"], rec["expires_at"] = time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
				data, _ := json.Marshal(rec)
				a.Content = string(data)
				return a, nil
			}}
			audit := &dispatchAudit{Broker: b}
			d := newDispatch(t, audit, []Node{{"local", p, readyDispatch}, {"peer", &fixtureProvider{}, readyDispatch}}, DispatchOptions{WaitForPeer: true, MaxWait: time.Second, PollInterval: 10 * time.Millisecond})
			out, err := d.Run(t.Context(), fixtureSpec())
			before, readErr := cfg.Runner.Broker.Get(t.Context(), key)
			if !errors.Is(err, inference.ErrInFlight) || !errors.Is(err, inference.ErrAdmissionDeferred) || inference.PreClaimDeferred(out.Result, err) || readErr != nil || before.Version != 1 || audit.writes.Load() != 0 || p.counts.Load() != 0 || p.calls.Load() != 0 || out.Nodes[1].Runs != 0 || out.Nodes[0].PeerPolls != 1 {
				t.Fatalf("absent/expired peer permitted fresh work: %+v %v", out, err)
			}
		})
	}
}

func TestDispatchPeerReadUsesCapacityWaitDeadline(t *testing.T) {
	cfg, p := fixtureConfig(t)
	dispatchFence(t, cfg.Runner.Broker, p, "pending")
	var reads atomic.Int64
	b := dispatchGetBroker{Broker: cfg.Runner.Broker, get: func(ctx context.Context, key string) (protocol.Artifact, error) {
		if reads.Add(1) == 1 {
			return cfg.Runner.Broker.Get(ctx, key)
		}
		<-ctx.Done()
		return protocol.Artifact{}, ctx.Err()
	}}
	d := newDispatch(t, b, []Node{{"local", p, readyDispatch}}, DispatchOptions{MaxWait: 30 * time.Millisecond, PollInterval: 10 * time.Millisecond, WaitForPeer: true})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	started := time.Now()
	out, err := d.Run(ctx, fixtureSpec())
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, inference.ErrInFlight) || inference.PreClaimDeferred(out.Result, err) || time.Since(started) > 300*time.Millisecond || ctx.Err() != nil || p.calls.Load() != 0 || out.Nodes[0].PeerPolls != 1 {
		t.Fatalf("peer read exceeded capacity wait: %+v %v", out, err)
	}
}

func TestConcurrentDispatchersKeepSharedClaimOwnership(t *testing.T) {
	cfg, first := fixtureConfig(t)
	second := &fixtureProvider{}
	entered, release := make(chan struct{}, 2), make(chan struct{})
	gate := func(ctx context.Context, _ inference.Spec) (func(), error) {
		entered <- struct{}{}
		select {
		case <-release:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	a := newDispatch(t, cfg.Runner.Broker, []Node{{"first", first, gate}}, DispatchOptions{})
	b := newDispatch(t, cfg.Runner.Broker, []Node{{"second", second, gate}}, DispatchOptions{})
	done := make(chan error, 2)
	for _, d := range []*Dispatcher{a, b} {
		go func() { _, err := d.Run(t.Context(), fixtureSpec()); done <- err }()
	}
	<-entered
	<-entered
	close(release)
	errA, errB := <-done, <-done
	if !((errA == nil && errors.Is(errB, inference.ErrInFlight)) || (errB == nil && errors.Is(errA, inference.ErrInFlight))) || first.calls.Load()+second.calls.Load() != 1 || first.counts.Load()+second.counts.Load() != 1 {
		t.Fatal("shared claim allowed duplicate model work", errA, errB)
	}
}

type dispatchHostingProvider struct {
	*fixtureProvider
	hosted atomic.Bool
}

func (p *dispatchHostingProvider) SelfHosted() bool { return !p.hosted.Load() }

func TestDispatchRechecksSelfHostedAssertionBeforeAttempt(t *testing.T) {
	cfg, p := fixtureConfig(t)
	provider := &dispatchHostingProvider{fixtureProvider: p}
	d := newDispatch(t, cfg.Runner.Broker, []Node{{"local", provider, readyDispatch}}, DispatchOptions{})
	provider.hosted.Store(true)
	if _, err := d.Run(t.Context(), fixtureSpec()); !errors.Is(err, ErrHostedDisabled) || p.calls.Load() != 0 || p.counts.Load() != 0 {
		t.Fatal("changed provider hosting assertion reached model", err)
	}
}

func TestDispatchBoundsPendingCallsAndReleasesCancelledLease(t *testing.T) {
	cfg, p := fixtureConfig(t)
	entered := make(chan struct{})
	var released atomic.Int64
	gate := func(ctx context.Context, _ inference.Spec) (func(), error) {
		close(entered)
		<-ctx.Done()
		return func() { released.Add(1) }, errors.Join(inference.ErrAdmissionDeferred, ctx.Err())
	}
	d := newDispatch(t, cfg.Runner.Broker, []Node{{"local", p, gate}}, DispatchOptions{MaxPending: 1, MaxWait: time.Second})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := d.Run(ctx, fixtureSpec()); done <- err }()
	<-entered
	if _, err := d.Run(t.Context(), fixtureSpec()); !errors.Is(err, admission.ErrQueueFull) {
		t.Fatal("pending call bound ignored", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) || released.Load() != 1 || len(d.slots) != 0 || p.calls.Load() != 0 {
		t.Fatal("cancelled wait retained lease or dispatched", err)
	}
}
