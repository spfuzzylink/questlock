package delegation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/protocol"
)

type factorizedAdmissionBroker struct {
	inference.Broker
	writes int
}

func (b *factorizedAdmissionBroker) Publish(ctx context.Context, req protocol.PublishRequest) (protocol.PublishResult, error) {
	if strings.HasPrefix(req.Key, "inference/v1/jobs/") {
		b.writes++
	}
	return b.Broker.Publish(ctx, req)
}

func TestFactorizedAdmissionDefersBeforeClaimAndKeepsIdentity(t *testing.T) {
	broker := &factorizedAdmissionBroker{Broker: testBroker(t)}
	v, p := factorizedTestVerifier(t, broker)
	identity := v.Identity()
	var calls, releases int
	v.Admission = func(context.Context, inference.Spec) (func(), error) {
		calls++
		return func() { releases++ }, inference.ErrAdmissionDeferred
	}
	out, err := v.Verify(t.Context(), factorizedTestRequest())
	if !errors.Is(err, inference.ErrAdmissionDeferred) || out.Accepted() || out.GenerationCalled || out.Complete || out.Durable || len(out.Work) != 1 || !out.Work[0].AdmissionDeferred || out.Work[0].ReceiptKey != "" || out.Work[0].OutputSHA256 != "" || out.InputTokens == nil || *out.InputTokens != 0 || out.OutputTokens == nil || *out.OutputTokens != 0 || calls != 1 || releases != 1 || broker.writes != 0 || p.counts != 0 || len(p.requests) != 0 || v.Identity() != identity {
		t.Fatalf("resource wait changed identity or claimed work: %+v %v calls=%d releases=%d writes=%d", out, err, calls, releases, broker.writes)
	}
	// The unchanged task remains executable by a capable consumer, because the
	// deferred invocation created neither a job nor a reservation.
	v.Admission = nil
	known, err := v.Verify(t.Context(), factorizedTestRequest())
	if err != nil || !known.Accepted() || p.sourceCalls != 1 || p.pairCalls != 1 {
		t.Fatalf("deferral blocked later admission: %+v %v", known, err)
	}
}

func TestFactorizedAdmissionCachedStagesSkipGateAndPairDeferralRetainsSource(t *testing.T) {
	for _, cachedSource := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh source", true: "cached source"}[cachedSource], func(t *testing.T) {
			v, p := factorizedTestVerifier(t, nil)
			var calls int
			deny := false
			v.Admission = func(_ context.Context, s inference.Spec) (func(), error) {
				calls++
				if deny && strings.Contains(s.Task, "equivalence") {
					return nil, inference.ErrAdmissionDeferred
				}
				return nil, nil
			}
			request := factorizedTestRequest()
			if cachedSource {
				first, err := v.Verify(t.Context(), request)
				if err != nil || !first.Accepted() || calls != 2 {
					t.Fatal("setup", err)
				}
				deny = true
				repeat, err := v.Verify(t.Context(), request)
				if err != nil || !repeat.Reused || repeat.GenerationCalled || calls != 2 {
					t.Fatalf("exact verification waited: %+v %v", repeat, err)
				}
				request.Question = "Which person is responsible for that remaining preview access review?"
			} else {
				deny = true
			}
			out, err := v.Verify(t.Context(), request)
			if !errors.Is(err, inference.ErrAdmissionDeferred) || out.Accepted() || out.Complete || out.Durable || len(out.Work) != 2 || !out.AnswerSupported || !out.Work[1].AdmissionDeferred || out.Work[1].GenerationCalled || out.Work[1].ReceiptKey != "" || out.ReceiptKey != "" || out.Work[0].Reused != cachedSource || out.Work[0].GenerationCalled == cachedSource || *out.Work[1].InputTokens != 0 || *out.Work[1].OutputTokens != 0 || p.sourceCalls != 1 {
				t.Fatalf("pair deferral hid source work: %+v %v", out, err)
			}
			wantCalls, wantInput, wantOutput, wantPairs := 2, int64(101), int64(20), 0
			if cachedSource {
				wantCalls, wantInput, wantOutput, wantPairs = 3, 0, 0, 1
			}
			if calls != wantCalls || *out.InputTokens != wantInput || *out.OutputTokens != wantOutput || p.pairCalls != wantPairs {
				t.Fatalf("wrong source accounting/callback count: %+v calls=%d", out, calls)
			}
		})
	}
}

func TestFactorizedAdmissionUnknownFencesPrecedeCallbackAndStayImmutable(t *testing.T) {
	for _, stage := range []string{"source", "equivalence"} {
		t.Run(stage, func(t *testing.T) {
			v, p := factorizedTestVerifier(t, nil)
			p.failStage = stage
			calls := 0
			v.Admission = func(context.Context, inference.Spec) (func(), error) { calls++; return nil, nil }
			first, err := v.Verify(t.Context(), factorizedTestRequest())
			last := first.Work[len(first.Work)-1]
			if err == nil || !last.GenerationCalled || last.AdmissionDeferred || last.InputTokens != nil {
				t.Fatalf("unknown execution disappeared: %+v %v", first, err)
			}
			before, err := v.runner.Broker.Get(t.Context(), last.ReceiptKey)
			if err != nil {
				t.Fatal(err)
			}
			priorCalls := calls
			v.Admission = func(context.Context, inference.Spec) (func(), error) {
				calls++
				return nil, inference.ErrAdmissionDeferred
			}
			again, err := v.Verify(t.Context(), factorizedTestRequest())
			after, readErr := v.runner.Broker.Get(t.Context(), last.ReceiptKey)
			if !errors.Is(err, inference.ErrInFlight) || errors.Is(err, inference.ErrAdmissionDeferred) || calls != priorCalls || again.Work[len(again.Work)-1].AdmissionDeferred || readErr != nil || after.Content != before.Content || after.Version != before.Version {
				t.Fatalf("resource gate bypassed/changed unknown fence: %+v %v", again, err)
			}
		})
	}
}

type generationSentinelProvider struct{ *factorizedTestProvider }

func (p generationSentinelProvider) Generate(ctx context.Context, r inference.Request) (inference.Response, error) {
	_, _ = p.factorizedTestProvider.Generate(ctx, r)
	return inference.Response{}, inference.ErrAdmissionDeferred
}

func TestFactorizedAdmissionSentinelFromExecutedProviderIsNotDeferral(t *testing.T) {
	v, p := factorizedTestVerifier(t, nil)
	v.runner.Provider = generationSentinelProvider{p}
	v.Admission = func(context.Context, inference.Spec) (func(), error) { return nil, nil }
	out, err := v.Verify(t.Context(), factorizedTestRequest())
	if !errors.Is(err, inference.ErrAdmissionDeferred) || len(out.Work) != 1 || !out.Work[0].GenerationCalled || out.Work[0].AdmissionDeferred || out.Work[0].InputTokens != nil || p.sourceCalls != 1 {
		t.Fatalf("executed/unknown work became free deferral: %+v %v", out, err)
	}
}
