package inference

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestMeasurementIsCurrentInvocationAndDoesNotChangeStoredJobs(t *testing.T) {
	r, provider := testRunner(t)
	spec := testSpec()
	before := time.Now().UTC()
	first, err := r.Run(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	m := first.Measurement
	if m == nil || !m.GenerationCalled || !m.KnownGenerationUsage || m.Outcome != "complete" || m.TotalMS < 0 || m.StartedAt.Before(before) || m.StartedAt.Equal(r.now()) || m.EndedAt.Before(m.StartedAt) {
		t.Fatalf("incorrect fresh measurement: %+v", m)
	}
	if m.GenerationUsage == nil || *m.GenerationUsage != first.ThisRunUsage || m.GenerationEndedAt.Before(m.GenerationStartedAt) {
		t.Fatal("known generation usage/timing lost")
	}
	stored, err := r.Broker.Get(context.Background(), "inference/v1/jobs/"+first.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stored.Content), &record); err != nil {
		t.Fatal(err)
	}
	if _, ok := record["measurement"]; ok {
		t.Fatal("invocation telemetry entered durable job format")
	}
	second, err := r.Run(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Reused || second.Fingerprint != first.Fingerprint || second.Measurement == m || second.Measurement.GenerationCalled || second.Measurement.KnownGenerationUsage || second.Measurement.Outcome != "reused" || !second.Measurement.GenerationStartedAt.IsZero() || second.ThisRunUsage != (Usage{}) || provider.calls != 1 || provider.counts != 1 {
		t.Fatalf("reuse invented fresh inference: %+v", second)
	}
	if _, ok := second.Measurement.PhaseMS["generation"]; ok {
		t.Fatal("reuse has generation latency")
	}
	storedAfter, err := r.Broker.Get(context.Background(), "inference/v1/jobs/"+first.Fingerprint)
	if err != nil || storedAfter.Version != stored.Version || storedAfter.Content != stored.Content {
		t.Fatal("measuring reuse mutated the old-format durable result")
	}
}

func TestMeasurementRetainsUnknownGenerationAndPendingBoundary(t *testing.T) {
	r, provider := testRunner(t)
	provider.err = errors.New("ambiguous generation")
	first, err := r.Run(context.Background(), testSpec())
	if err == nil || first.Measurement == nil || first.Measurement.Outcome != "uncertain" || !first.Measurement.GenerationCalled || first.Measurement.KnownGenerationUsage || first.Measurement.GenerationUsage != nil {
		t.Fatal("unknown generation was hidden or reported free")
	}
	second, err := r.Run(context.Background(), testSpec())
	if !errors.Is(err, ErrInFlight) || second.Measurement.Outcome != "pending" || second.Measurement.GenerationCalled || provider.calls != 1 || provider.counts != 1 {
		t.Fatal("measurement changed uncertain retry semantics")
	}
}

func TestMeasurementPreflightCapsDoNotBecomeGenerationFailures(t *testing.T) {
	r, provider := testRunner(t)
	provider.input = 1001
	result, err := r.Run(context.Background(), testSpec())
	if !errors.Is(err, ErrBudgetExceeded) || result.Measurement == nil || result.Measurement.Outcome != "budget_blocked" || result.Measurement.GenerationCalled || provider.calls != 0 {
		t.Fatal("preflight rejection became billed generation")
	}
}
