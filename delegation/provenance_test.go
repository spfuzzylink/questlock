package delegation

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/inference"
)

func TestSpecForTierMatchesRouterAndPersistedPlanProvenance(t *testing.T) {
	request := testRequest()
	for _, tier := range tiers(request.Policy) {
		spec, err := SpecForTier(request, tier.ID, false)
		if err != nil || !reflect.DeepEqual(spec, tierSpec(request, tier, requestDigest(request))) {
			t.Fatalf("exported tier spec changed canonical identity: %v %+v", err, spec)
		}
	}
	broker := testBroker(t)
	provider := &fixtureProvider{response: knownResult(`{"ship":false}`).Response, identity: "synthetic-private-runtime-identity"}
	result, err := (Router{Factory: func(Tier) (Runner, error) {
		return inference.Runner{Broker: broker, Provider: provider}, nil
	}}).Run(context.Background(), request)
	if err != nil || result.SelectedTier != request.Policy.Local.ID || len(result.Attempts) != 1 {
		t.Fatalf("delegated fixture failed: %v %+v", err, result)
	}
	spec, err := SpecForTier(request, result.SelectedTier, false)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := inference.BuildPlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := broker.Get(context.Background(), "inference/v1/jobs/"+result.Attempts[0].Result.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Fingerprint        string `json:"fingerprint"`
		RequestFingerprint string `json:"request_fingerprint"`
	}
	if err := json.Unmarshal([]byte(artifact.Content), &saved); err != nil || saved.RequestFingerprint != plan.Fingerprint || saved.Fingerprint != result.Attempts[0].Result.Fingerprint || strings.Contains(artifact.Content, provider.identity) {
		t.Fatalf("persisted delegated provenance does not bind approved plan: %v %+v", err, saved)
	}
	if _, err := SpecForTier(request, "private-unknown-tier", false); err == nil || strings.Contains(err.Error(), "private-unknown-tier") {
		t.Fatal("unknown tier accepted or echoed")
	}
	if _, err := SpecForTier(paidRequest(), "compact", false); err == nil {
		t.Fatal("tier helper bypassed complete policy authorization")
	}
	if _, err := SpecForTier(paidRequest(), "balanced", true); err != nil {
		t.Fatalf("explicit hosted policy rejected: %v", err)
	}
}
