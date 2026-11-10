package inference

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/protocol"
)

type admissionAuditBroker struct {
	Broker
	writes atomic.Int64
}

func (b *admissionAuditBroker) Publish(ctx context.Context, p protocol.PublishRequest) (protocol.PublishResult, error) {
	b.writes.Add(1)
	return b.Broker.Publish(ctx, p)
}

func TestDeferredAdmissionCreatesNoClaimQuotaOrModelWork(t *testing.T) {
	r, p := testRunner(t)
	audit := &admissionAuditBroker{Broker: r.Broker}
	r.Broker = audit
	s := testSpec()
	s.DailyTokenBudget = &TokenBudget{InputTokens: 1000, OutputTokens: 1000, TotalTokens: 2000}
	var released atomic.Int64
	got, err := r.RunWithAdmission(t.Context(), s, func(context.Context, Spec) (func(), error) { return func() { released.Add(1) }, ErrAdmissionDeferred })
	if !errors.Is(err, ErrAdmissionDeferred) || got.Measurement == nil || got.Measurement.Outcome != "deferred" || got.Measurement.GenerationCalled || audit.writes.Load() != 0 || p.counts != 0 || p.calls != 0 || released.Load() != 1 {
		t.Fatalf("deferral did work or lost evidence: %+v %v", got, err)
	}
	for _, key := range []string{"inference/v1/jobs/" + got.Measurement.Fingerprint, "inference/v1/token-budget/2030-01-02"} {
		_, readErr := r.Broker.Get(t.Context(), key)
		var ce *client.Error
		if !errors.As(readErr, &ce) || ce.Code != "not_found" {
			t.Fatalf("deferral left state %s: %v", key, readErr)
		}
	}
	first, err := r.Run(t.Context(), s)
	if err != nil || !first.Durable || p.calls != 1 {
		t.Fatal("another capable worker could not execute after deferral", err)
	}
	exact, err := r.RunWithAdmission(t.Context(), s, func(context.Context, Spec) (func(), error) {
		t.Fatal("exact hit waited for capacity")
		return nil, ErrAdmissionDeferred
	})
	if err != nil || !exact.Reused || exact.Fingerprint != first.Fingerprint || p.calls != 1 {
		t.Fatalf("runtime policy changed exact identity: %+v %v", exact, err)
	}
}

func TestAdmissionWaitOwnsNoClaimWhilePeerCompletes(t *testing.T) {
	r, p := testRunner(t)
	s := testSpec()
	entered := make(chan struct{})
	proceed := make(chan struct{})
	done := make(chan error, 1)
	var releases atomic.Int64
	go func() {
		_, err := r.RunWithAdmission(t.Context(), s, func(ctx context.Context, _ Spec) (func(), error) {
			close(entered)
			select {
			case <-proceed:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return func() { releases.Add(1) }, nil
		})
		done <- err
	}()
	<-entered
	peer, err := r.Run(t.Context(), s)
	if err != nil || !peer.Durable || p.calls != 1 {
		t.Fatal("waiting worker owned a claim", err)
	}
	close(proceed)
	if err = <-done; !errors.Is(err, ErrInFlight) || releases.Load() != 1 || p.calls != 1 {
		t.Fatal("waiting worker overwrote peer or leaked capacity", err)
	}
	again, err := r.Run(t.Context(), s)
	if err != nil || !again.Reused || again.Fingerprint != peer.Fingerprint || p.calls != 1 {
		t.Fatal("peer completion was not preserved", err)
	}
}

func TestPendingAndUnknownFencesPrecedeAdmission(t *testing.T) {
	for _, state := range []string{"pending", "uncertain"} {
		t.Run(state, func(t *testing.T) {
			r, p := testRunner(t)
			s := testSpec()
			plan, _ := BuildPlan(s)
			fp := reuseDigest(plan.Fingerprint + "\x00" + p.Identity())
			key := "inference/v1/jobs/" + fp
			content, _ := json.Marshal(jobRecord{Schema: 1, Fingerprint: fp, RequestFingerprint: plan.Fingerprint, State: state, CreatedAt: r.now()})
			before, err := r.Broker.Publish(t.Context(), protocol.PublishRequest{Key: key, OperationID: "fixture-fence", ExpectedVersion: 0, Content: string(content)})
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.RunWithAdmission(t.Context(), s, func(context.Context, Spec) (func(), error) {
				t.Fatal("ownership fence reached admission")
				return nil, nil
			})
			after, readErr := r.Broker.Get(t.Context(), key)
			if !errors.Is(err, ErrInFlight) || readErr != nil || after.Version != before.Artifact.Version || after.Content != before.Artifact.Content || p.counts != 0 || p.calls != 0 {
				t.Fatal("fence changed under admission", err, readErr)
			}
		})
	}
}

func TestAdmissionCancellationReturnsWithoutClaim(t *testing.T) {
	r, p := testRunner(t)
	audit := &admissionAuditBroker{Broker: r.Broker}
	r.Broker = audit
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	got, err := r.RunWithAdmission(ctx, testSpec(), func(c context.Context, _ Spec) (func(), error) {
		<-c.Done()
		return nil, errors.Join(ErrAdmissionDeferred, c.Err())
	})
	if !errors.Is(err, ErrAdmissionDeferred) || !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second || audit.writes.Load() != 0 || p.calls != 0 || got.Measurement == nil || got.Measurement.Outcome != "deferred" {
		t.Fatalf("cancelled wait did work: %+v %v", got, err)
	}
}

func TestAdmissionLeaseCoversResolverAndAliasReplaySkipsIt(t *testing.T) {
	r, p, s, resolution := reuseFixture(t)
	resolver := fixtureResolver(resolution)
	active := false
	acquired, released := 0, 0
	resolver.resolve = func(context.Context, Spec) (*ReuseResolution, error) {
		if !active {
			t.Fatal("resolver ran without capacity lease")
		}
		return resolution, nil
	}
	admit := func(context.Context, Spec) (func(), error) {
		active = true
		acquired++
		return func() { active = false; released++ }, nil
	}
	first, err := r.RunWithResolverAdmission(t.Context(), s, resolver, admit)
	if err != nil {
		t.Fatal(err)
	}
	requireSemanticReuse(t, first, resolution)
	second, err := r.RunWithResolverAdmission(t.Context(), s, resolver, func(context.Context, Spec) (func(), error) {
		t.Fatal("alias replay waited for capacity")
		return nil, ErrAdmissionDeferred
	})
	if err != nil || active || acquired != 1 || released != 1 || p.calls != 1 {
		t.Fatal("alias replay/lease regression", err)
	}
	requireSemanticReuse(t, second, resolution)
}

func TestProviderDeferralSentinelAfterGenerateRemainsUnknownAndFenced(t *testing.T) {
	r, p := testRunner(t)
	p.err = ErrAdmissionDeferred
	p.response = Response{}
	result, err := r.RunWithAdmission(t.Context(), testSpec(), func(context.Context, Spec) (func(), error) { return nil, nil })
	if !errors.Is(err, ErrAdmissionDeferred) || PreClaimDeferred(result, err) || result.Measurement == nil || !result.Measurement.GenerationCalled || result.Measurement.KnownGenerationUsage || result.Measurement.Outcome != "uncertain" || p.calls != 1 {
		t.Fatalf("executed work masqueraded as resource deferral: %+v %v", result, err)
	}
	_, err = r.RunWithAdmission(t.Context(), testSpec(), func(context.Context, Spec) (func(), error) {
		t.Fatal("unknown job reached resource admission")
		return nil, nil
	})
	if !errors.Is(err, ErrInFlight) || p.calls != 1 {
		t.Fatal("unknown provider work lost its fence", err)
	}
}
