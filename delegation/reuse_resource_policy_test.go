package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/outcomes"
	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/semantic"
)

func policyResolver(owner *ControlledReuser, p ReuseAdmissionPolicy) *controlledResolver {
	return &controlledResolver{owner: owner, admissionPolicy: &p, observation: &ReuseObservation{}}
}

func TestResourcePolicyDefersWholeFreshTaskWithoutClaimOrQualityVerdict(t *testing.T) {
	var probes atomic.Int64
	router, target, verifier, embedder := reuseAdmissionRouter(t, admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		probes.Add(1)
		s := reuseAdmissionSnapshot(time.Now())
		s.MemoryPressure = admission.High
		return s, nil
	}))
	router.Reuse.AdmissionPolicy = &ReuseAdmissionPolicy{}
	req := controlledWaitRequest("How do I reset an account password?", 0)
	out, err := router.Run(t.Context(), req)
	if !errors.Is(err, inference.ErrAdmissionDeferred) || out.Status != "stopped" || out.Response != nil || len(out.Attempts) != 1 || out.Attempts[0].ErrorCategory != "resource_deferred" || out.Attempts[0].QualityPassed || out.Attempts[0].Result.Measurement.Outcome != "deferred" || target.generations != 0 || target.countCalls != 0 || verifier.generations != 0 || embedder.calls.Load() != 0 || probes.Load() != 1 {
		t.Fatalf("capacity became quality/generation: %+v %v", out, err)
	}
	d := out.SemanticReuse.ResourceAdmission
	if d == nil || d.Outcome != "deferred" || d.Reason != "memory_pressure" || d.Samples != 1 || d.DeferredReason != "resource_not_ready" {
		t.Fatalf("deferral evidence lost: %+v", d)
	}
	key := "inference/v1/jobs/" + out.Attempts[0].Result.Measurement.Fingerprint
	if _, err = router.Reuse.Broker.Get(t.Context(), key); err == nil {
		t.Fatal("resource deferral published a claim")
	}
	// A second capable consumer may execute the unchanged task immediately.
	peer := router
	peer.Reuse = &ControlledReuser{Broker: router.Reuse.Broker, Index: router.Reuse.Index, EmbedderFactory: router.Reuse.EmbedderFactory, VerifierFactory: router.Reuse.VerifierFactory}
	result, err := peer.Run(t.Context(), req)
	if err != nil || result.Status != "completed" || target.generations != 1 {
		t.Fatal("deferred worker blocked capable peer", err)
	}
}

func TestResourcePolicyPendingUnknownAndExactResultsPrecedeResources(t *testing.T) {
	for _, state := range []string{"pending", "uncertain"} {
		t.Run(state, func(t *testing.T) {
			router, target, _, _ := reuseAdmissionRouter(t, admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
				t.Fatal("ownership fence probed resources")
				return admission.Snapshot{}, nil
			}))
			router.Reuse.AdmissionPolicy = &ReuseAdmissionPolicy{MaxWaitMS: 100}
			req := controlledWaitRequest("How do I reset an account password?", 0)
			s := tierSpec(req, req.Policy.Local, requestDigest(req))
			plan, _ := inference.BuildPlan(s)
			fp := reuseHash(plan.Fingerprint + "\x00" + target.Identity())
			key := "inference/v1/jobs/" + fp
			b, _ := json.Marshal(map[string]any{"schema": 1, "fingerprint": fp, "request_fingerprint": plan.Fingerprint, "state": state, "created_at": time.Now().UTC()})
			before, err := router.Reuse.Broker.Publish(t.Context(), protocol.PublishRequest{Key: key, OperationID: "policy-fence", Content: string(b)})
			if err != nil {
				t.Fatal(err)
			}
			out, err := router.Run(t.Context(), req)
			after, readErr := router.Reuse.Broker.Get(t.Context(), key)
			if !errors.Is(err, inference.ErrInFlight) || readErr != nil || after.Content != before.Artifact.Content || after.Version != before.Artifact.Version || out.SemanticReuse.ResourceAdmission != nil || target.generations != 0 {
				t.Fatal("resource policy changed an ownership fence", err, readErr)
			}
		})
	}
	var deny atomic.Bool
	var probes atomic.Int64
	router, target, _, _ := reuseAdmissionRouter(t, admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		if deny.Load() {
			t.Error("exact replay probed resources")
		}
		probes.Add(1)
		return reuseAdmissionSnapshot(time.Now()), nil
	}))
	router.Reuse.AdmissionPolicy = &ReuseAdmissionPolicy{MaxWaitMS: 100}
	req := controlledWaitRequest("How do I reset an account password?", 0)
	first, err := router.Run(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	deny.Store(true)
	exact, err := router.Run(t.Context(), req)
	if err != nil || exact.Status != "completed" || exact.Attempts[0].Result.Fingerprint != first.Attempts[0].Result.Fingerprint || !exact.Attempts[0].Result.Reused || probes.Load() != 1 || target.generations != 1 || exact.SemanticReuse.ResourceAdmission != nil {
		t.Fatal("exact identity/replay changed under resource policy", err)
	}
}

func TestResourcePolicyWaitsForRecoveryAndRespectsEarlierCancellation(t *testing.T) {
	var healthy atomic.Bool
	sampled := make(chan struct{}, 1)
	o := &ControlledReuser{Resources: admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		s := reuseAdmissionSnapshot(time.Now())
		if !healthy.Load() {
			s.AvailableMemoryBytes = 0
			select {
			case sampled <- struct{}{}:
			default:
			}
		}
		return s, nil
	})}
	p := ReuseAdmissionPolicy{MaxWaitMS: 500, PollIntervalMS: 10}
	r := policyResolver(o, p)
	done := make(chan error, 1)
	go func() {
		release, err := r.admit(t.Context(), inference.Spec{})
		if release != nil {
			release()
		}
		done <- err
	}()
	<-sampled
	healthy.Store(true)
	if err := <-done; err != nil || r.observation.ResourceAdmission.Outcome != "admitted" || r.observation.ResourceAdmission.Samples < 2 || len(o.admissionSlots) != 0 {
		t.Fatal("recovery wait failed or leaked capacity", err)
	}
	healthy.Store(false)
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	r = policyResolver(o, p)
	start := time.Now()
	release, err := r.admit(ctx, inference.Spec{})
	if release != nil {
		release()
	}
	if !errors.Is(err, inference.ErrAdmissionDeferred) || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second || len(o.admissionSlots) != 0 || r.observation.Status != "resource_deferred" {
		t.Fatal("caller deadline did not bound resource wait", err)
	}
}

func TestResourcePolicyBoundsConcurrencyQueueAndReleasesCancelledWaiter(t *testing.T) {
	o := &ControlledReuser{Resources: admission.SourceFunc(func(context.Context) (admission.Snapshot, error) { return reuseAdmissionSnapshot(time.Now()), nil })}
	p := ReuseAdmissionPolicy{MaxConcurrent: 2, MaxQueued: 1, MaxWaitMS: 500, PollIntervalMS: 10}
	a, err := policyResolver(o, p).admit(t.Context(), inference.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	defer a()
	b, err := policyResolver(o, p).admit(t.Context(), inference.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	defer b()
	ctx, cancel := context.WithCancel(t.Context())
	queued := policyResolver(o, p)
	done := make(chan error, 1)
	go func() {
		release, err := queued.admit(ctx, inference.Spec{})
		if release != nil {
			release()
		}
		done <- err
	}()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for len(o.admissionQueue) != 1 {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("waiter never joined bounded queue")
		}
	}
	full := policyResolver(o, p)
	release, err := full.admit(t.Context(), inference.Spec{})
	if release != nil {
		release()
	}
	if !errors.Is(err, inference.ErrAdmissionDeferred) || full.observation.ResourceAdmission.DeferredReason != "queue_full" || len(o.admissionSlots) != 2 {
		t.Fatal("queue/concurrency limit ignored", err)
	}
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) || !errors.Is(err, inference.ErrAdmissionDeferred) || len(o.admissionQueue) != 0 || len(o.admissionSlots) != 2 {
		t.Fatal("cancelled queued waiter leaked capacity", err)
	}
}

type policyActiveProvider struct {
	inference.Provider
	started       chan struct{}
	finish        chan struct{}
	activeContext chan context.Context
}

func (p *policyActiveProvider) Generate(ctx context.Context, r inference.Request) (inference.Response, error) {
	close(p.started)
	p.activeContext <- ctx
	select {
	case <-p.finish:
		return p.Provider.Generate(ctx, r)
	case <-ctx.Done():
		return inference.Response{}, ctx.Err()
	}
}

func TestResourcePolicyDoesNotCancelAlreadyAdmittedWork(t *testing.T) {
	var critical atomic.Bool
	var probes atomic.Int64
	router, target, _, _ := reuseAdmissionRouter(t, admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		probes.Add(1)
		s := reuseAdmissionSnapshot(time.Now())
		if critical.Load() {
			s.MemoryPressure = admission.Critical
		}
		return s, nil
	}))
	router.Reuse.AdmissionPolicy = &ReuseAdmissionPolicy{MaxWaitMS: 100}
	p := &policyActiveProvider{Provider: target, started: make(chan struct{}), finish: make(chan struct{}), activeContext: make(chan context.Context, 1)}
	router.Factory = func(Tier) (Runner, error) { return &inference.Runner{Broker: router.Reuse.Broker, Provider: p}, nil }
	done := make(chan error, 1)
	go func() {
		out, err := router.Run(t.Context(), controlledWaitRequest("How do I reset an account password?", 0))
		if err == nil && out.Status != "completed" {
			err = errors.New("admitted task did not complete")
		}
		done <- err
	}()
	<-p.started
	active := <-p.activeContext
	critical.Store(true)
	if active.Err() != nil || probes.Load() != 1 {
		t.Fatal("later pressure cancelled/reprobed active work")
	}
	close(p.finish)
	if err := <-done; err != nil || target.generations != 1 || len(router.Reuse.admissionSlots) != 0 {
		t.Fatal("known active result was not retained", err)
	}
}

func TestResourcePolicyAllowsExplicitHostTradeoffsButNotCriticalAdmission(t *testing.T) {
	p, err := NormalizeReuseAdmissionPolicy(ReuseAdmissionPolicy{MinAvailableMemoryBytes: 64 * admission.MiB, MaxConcurrent: 4, MaxQueued: 16, MaxWaitMS: 60000, AllowHighMemoryPressure: true})
	if err != nil {
		t.Fatal(err)
	}
	s := reuseAdmissionSnapshot(time.Now())
	s.AvailableMemoryBytes = 128 * admission.MiB
	s.MemoryPressure = admission.High
	if got := resourcePolicyAdmission(s, nil, time.Now(), p); got.Reason != "ready" {
		t.Fatal("explicit host tradeoff ignored", got)
	}
	s.MemoryPressure = admission.Critical
	if got := resourcePolicyAdmission(s, nil, time.Now(), p); got.Reason != "memory_pressure" {
		t.Fatal("critical pressure admitted", got)
	}
	if _, err = NormalizeReuseAdmissionPolicy(ReuseAdmissionPolicy{MaxQueued: -1}); !errors.Is(err, ErrResourceAdmissionConfig) {
		t.Fatal("negative queue accepted")
	}
}

func TestResourcePolicyStrictConfigurationAndCompatibleOutcomeAccounting(t *testing.T) {
	for _, raw := range []string{`{"max_concurrent":1,"max_concurrent":2}`, `{"unknown":1}`, `{} {}`, `{"max_concurrent":null}`, `{"max_concurrent":-1}`} {
		if _, err := LoadReuseAdmissionPolicy(strings.NewReader(raw)); err == nil {
			t.Fatalf("invalid runtime policy accepted: %s", raw)
		}
	}
	if p, err := LoadReuseAdmissionPolicy(strings.NewReader(`{"max_concurrent":3,"max_wait_ms":500}`)); err != nil || p.MaxConcurrent != 3 || p.MaxWaitMS != 500 || p.MinAvailableMemoryBytes != 256*admission.MiB {
		t.Fatal("operator policy/defaults not loaded", err)
	}
	router, _, _, _ := reuseAdmissionRouter(t, admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		s := reuseAdmissionSnapshot(time.Now())
		s.AvailableMemoryBytes = 0
		return s, nil
	}))
	router.Reuse.AdmissionPolicy = &ReuseAdmissionPolicy{}
	req := controlledWaitRequest("How do I reset an account password?", 0)
	result, err := router.Run(t.Context(), req)
	if !errors.Is(err, inference.ErrAdmissionDeferred) {
		t.Fatal(err)
	}
	RecordOutcome(t.Context(), router.Reuse.Broker, req, &result, "resource-deferred-run")
	if result.OutcomeTracking == nil || result.OutcomeTracking.Status != "recorded" {
		t.Fatalf("deferred metadata could not be retained: %+v", result.OutcomeTracking)
	}
	snapshot, err := (outcomes.Store{Broker: router.Reuse.Broker}).Get(t.Context(), outcomes.Task{ID: req.Spec.Task, Revision: req.Spec.Revision})
	if err != nil || len(snapshot.Runs) != 1 || snapshot.Runs[0].Status != "stopped" || len(snapshot.Runs[0].Attempts) != 1 {
		t.Fatal("legacy outcome status changed", err)
	}
	a := snapshot.Runs[0].Attempts[0]
	if a.GenerationCalled || !a.UsageKnown || a.Usage != (inference.Usage{}) || a.CheckPassed || a.Complete || a.Durable || a.CostBasis != "no_generation" {
		t.Fatal("resource deferral acquired execution/quality evidence", a)
	}
}

func TestDeferredPairStageRetainsKnownSourceCosts(t *testing.T) {
	broker := testBroker(t)
	req := controlledWaitRequest("How do I reset an account password?", 0)
	zero, input, output := int64(0), int64(100), int64(14)
	verification := semantic.ValidityResult{Verdict: "unknown", GenerationCalled: true, InputTokens: &input, OutputTokens: &output, Work: []semantic.VerificationWork{
		{Stage: "source", GenerationCalled: true, Complete: true, Durable: true, InputTokens: &input, OutputTokens: &output, ReceiptKey: "inference/v1/jobs/" + reuseHash("known-source-job"), OutputSHA256: reuseHash("source-verdict")},
		{Stage: "equivalence", AdmissionDeferred: true, InputTokens: &zero, OutputTokens: &zero},
	}}
	result := Result{Status: "stopped", RequestSHA256: requestDigest(req), Attempts: []Attempt{{TierID: req.Policy.Local.ID, Result: inference.Result{Measurement: &inference.Measurement{Fingerprint: reuseHash("target"), Outcome: "error"}}}}, SemanticReuse: &ReuseObservation{Verifications: []semantic.ValidityResult{verification}}}
	RecordOutcome(t.Context(), broker, req, &result, "known-source-deferred-pair")
	if result.OutcomeTracking == nil || result.OutcomeTracking.Status != "recorded" {
		t.Fatalf("known source cost was lost on pair deferral: %+v", result.OutcomeTracking)
	}
	snapshot, err := (outcomes.Store{Broker: broker}).Get(t.Context(), outcomes.Task{ID: req.Spec.Task, Revision: req.Spec.Revision})
	if err != nil || len(snapshot.Runs) != 1 || len(snapshot.Runs[0].VerificationWork) != 1 {
		t.Fatal("source work not recorded exactly once", err)
	}
	a := snapshot.Runs[0].VerificationWork[0]
	if !a.GenerationCalled || !a.UsageKnown || a.Usage.InputTokens != 100 || a.Usage.OutputTokens != 14 {
		t.Fatal("known source work became zero or unknown", a)
	}
	result.SemanticReuse.Verifications[0].Work[1].GenerationCalled = true
	RecordOutcome(t.Context(), broker, req, &result, "forged-deferred-pair")
	if result.OutcomeTracking.Status != "failed" || result.OutcomeTracking.ErrorCategory != "verification_work_unconfirmed" {
		t.Fatal("executed stage accepted as deferred")
	}
}
