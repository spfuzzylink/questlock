package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/inference"
)

func deferredFixture(t *testing.T, phase ...string) (measurementHeader, row) {
	t.Helper()
	r := fixtureRows(t)[0]
	if len(phase) > 0 {
		for _, candidate := range fixtureRows(t) {
			if candidate.Phase == phase[0] {
				r = candidate
				break
			}
		}
	}
	r.Error = "resource_deferred"
	a := &r.Result.Attempts[0]
	a.QualityPassed, a.ErrorCategory = false, "resource_deferred"
	for i := range a.Checks {
		a.Checks[i].Passed, a.Checks[i].Status = false, "not_evaluated"
	}
	a.Result = inference.Result{Measurement: &inference.Measurement{Fingerprint: fixtureDigest("deferred"), Outcome: "deferred", TotalMS: 25, PhaseMS: map[string]float64{"resource_admission": 25}}}
	r.Result.Status = "stopped"
	r.Result.SemanticReuse = &delegation.ReuseObservation{Status: "resource_deferred", ResourceAdmission: &delegation.ReuseResourceAdmission{Reason: "insufficient_memory", Outcome: "deferred", Samples: 1, WaitMS: 25, DeferredReason: "resource_not_ready"}}
	p, err := delegation.NormalizeReuseAdmissionPolicy(delegation.ReuseAdmissionPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	h := measurementHeader{Schema: "questlock.paraphrase-measurement.v1", CasesSHA256: fixtureDigest(string(casesJSON)), Model: "fixture", VerifierModel: "other-tokenizer", VerifierPolicy: "factorized-v1", MinSimilarity: .25, ResourcePolicy: &p}
	return h, r
}

func encodeDeferred(t *testing.T, h measurementHeader, r row) string {
	t.Helper()
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	if err := e.Encode(h); err != nil {
		t.Fatal(err)
	}
	if err := e.Encode(r); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestMeasurementRetainsResourceDeferralOutsideQualityFailures(t *testing.T) {
	for _, phase := range []string{"seed", "exact"} {
		t.Run(phase, func(t *testing.T) {
			h, r := deferredFixture(t, phase)
			report, err := summarizeMeasurement(strings.NewReader(encodeDeferred(t, h, r)))
			if err != nil {
				t.Fatal(err)
			}
			p := report.Phases[phase]
			if p.Rows != 1 || p.ResourceDeferred != 1 || p.Failed != 0 || p.QualityPassed != 0 || p.Passed != 0 || p.Missing != p.Expected-1 || p.Generation.Calls != 0 || p.Generation.Unknown != 0 || p.EmbeddingWorkRecords != 0 || p.EmbeddingTokens != 0 || p.EmbeddingUnknown != 0 || p.Verification.Calls != 0 || p.Verification.Unknown != 0 || p.ExactWork != 0 || report.FinalMarker {
				t.Fatalf("deferral became a quality failure, invented work or excluded row: %+v", report)
			}
			if report.Eligibility["eligible"] || report.WholeEligibility["eligible"] || report.LatencyEligibility["primary_eligible"] {
				t.Fatal("partial resource-deferred cohort became savings-eligible")
			}
			if report.DeclaredFailures {
				t.Fatal("resource deferral became a declared quality failure")
			}
		})
	}
}

func TestMeasurementRejectsFalseResourceDeferralEvidence(t *testing.T) {
	for _, name := range []string{"completed", "generation", "quality", "missing_policy", "claim_phase", "preflight_phase", "history", "avoided", "embedding"} {
		t.Run(name, func(t *testing.T) {
			h, r := deferredFixture(t)
			switch name {
			case "completed":
				r.Result.Status = "completed"
			case "generation":
				r.Result.Attempts[0].Result.Measurement.GenerationCalled = true
			case "quality":
				r.Result.Attempts[0].Checks[0].Passed = true
			case "missing_policy":
				h.ResourcePolicy = nil
			}
			switch name {
			case "claim_phase":
				r.Result.Attempts[0].Result.Measurement.PhaseMS["admission"] = 1
			case "preflight_phase":
				r.Result.Attempts[0].Result.Measurement.PhaseMS["preflight"] = 1
			case "history":
				r.Result.Attempts[0].Result.GenerationUsage.InputTokens = 10
			case "avoided":
				r.Result.Attempts[0].Result.AvoidedUsage.OutputTokens = 10
			case "embedding":
				r.Result.SemanticReuse.EmbeddingMS = 1
			}
			if _, err := summarizeMeasurement(strings.NewReader(encodeDeferred(t, h, r))); err == nil {
				t.Fatal("false resource deferral accepted")
			}
		})
	}
}
