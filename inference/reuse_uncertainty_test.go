package inference

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestUnknownResolverWorkFencesTargetAcrossChangedResolver(t *testing.T) {
	r, provider := testRunner(t)
	spec := testSpec()
	first := &fixtureReuseResolver{id: "first-source-policy", resolve: func(context.Context, Spec) (*ReuseResolution, error) {
		return nil, errors.Join(ErrReuseUncertain, errors.New("lost verifier stream"))
	}}
	result, err := r.RunWithResolver(t.Context(), spec, first)
	if !errors.Is(err, ErrReuseUncertain) || provider.calls != 0 || provider.counts != 0 {
		t.Fatalf("unknown resolver generated: %+v %v", result, err)
	}
	a, err := r.Broker.Get(t.Context(), "inference/v1/jobs/"+result.Measurement.Fingerprint)
	var saved jobRecord
	if err != nil || json.Unmarshal([]byte(a.Content), &saved) != nil || saved.State != "uncertain" {
		t.Fatalf("unknown work lost target fence: %+v %v", saved, err)
	}
	changed := &fixtureReuseResolver{id: "different-source-policy", resolve: func(context.Context, Spec) (*ReuseResolution, error) {
		t.Fatal("changed source re-entered resolver after unknown work")
		return nil, nil
	}}
	if _, err := r.RunWithResolver(t.Context(), spec, changed); !errors.Is(err, ErrInFlight) {
		t.Fatal("changed resolver bypassed uncertainty", err)
	}
	if _, err := r.Run(t.Context(), spec); !errors.Is(err, ErrInFlight) || provider.calls != 0 || provider.counts != 0 {
		t.Fatal("ordinary generation bypassed uncertainty", err)
	}
}
