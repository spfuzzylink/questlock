package inference

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestNewJobPersistsPlanProvenanceAndLegacyReuseRemainsReadOnly(t *testing.T) {
	r, provider := testRunner(t)
	spec := testSpec()
	plan, err := BuildPlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.Run(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	key := "inference/v1/jobs/" + first.Fingerprint
	artifact, err := r.Broker.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	var record jobRecord
	if err := json.Unmarshal([]byte(artifact.Content), &record); err != nil || record.RequestFingerprint != plan.Fingerprint || record.Fingerprint != first.Fingerprint {
		t.Fatalf("job lost approved-plan provenance: %v %+v", err, record)
	}
	if strings.Contains(artifact.Content, provider.Identity()) || record.RequestFingerprint == record.Fingerprint {
		t.Fatal("runtime identity exposed or confused with request provenance")
	}
	// Simulate a published legacy job: omission must not change how inference
	// reads or reuses the old record, nor quietly backfill unproved provenance.
	record.RequestFingerprint = ""
	legacy, err := r.publish(context.Background(), key, artifact.Version, "synthetic-legacy-job", record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(legacy.Content, "request_fingerprint") {
		t.Fatal("nil provenance changed legacy JSON")
	}
	again, err := r.Run(context.Background(), spec)
	if err != nil || !again.Reused || again.Fingerprint != first.Fingerprint || provider.calls != 1 || provider.counts != 1 {
		t.Fatalf("legacy job stopped reusing: %v %+v", err, again)
	}
	after, err := r.Broker.Get(context.Background(), key)
	if err != nil || after.Version != legacy.Version || after.Content != legacy.Content {
		t.Fatalf("legacy reuse rewrote its original provenance: %v", err)
	}
}
