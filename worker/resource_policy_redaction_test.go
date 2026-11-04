package worker

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/semantic"
)

func TestResourceDeferralKeepsClosedClassificationAndCopiesMetadata(t *testing.T) {
	original := delegation.Result{Status: "stopped", SemanticReuse: &delegation.ReuseObservation{Mode: "controlled", Status: "resource_deferred", ResourceAdmission: &delegation.ReuseResourceAdmission{Reason: "memory_stall", Outcome: "deferred", DeferredReason: "admission_wait_expired", Samples: 3, WaitMS: 25, QueueWaitMS: 5}}, Attempts: []delegation.Attempt{{ErrorCategory: "resource_deferred", Result: inference.Result{Measurement: &inference.Measurement{Outcome: "deferred", PhaseMS: map[string]float64{"resource_admission": 25}}}}}}
	output := safeOutput(original, errors.Join(inference.ErrAdmissionDeferred, context.DeadlineExceeded))
	d := output.SemanticReuse.ResourceAdmission
	if output.Status != "stopped" || output.ErrorCategory != "resource_deferred" || output.Attempts[0].ErrorCategory != "resource_deferred" || output.SemanticReuse.Status != "resource_deferred" || d.Outcome != "deferred" || d.DeferredReason != "admission_wait_expired" || d.Reason != "memory_stall" || d.WaitMS != 25 || d == original.SemanticReuse.ResourceAdmission {
		t.Fatalf("resource deferral lost classification or shares input: %+v", output)
	}
	d.Outcome = ""
	if original.SemanticReuse.ResourceAdmission.Outcome != "deferred" {
		t.Fatal("safe output mutated input")
	}
}

func TestWorkerDoesNotRelabelExecutedOrOutputBearingWorkAsDeferred(t *testing.T) {
	for _, executed := range []bool{false, true} {
		m := &inference.Measurement{Outcome: "deferred", PhaseMS: map[string]float64{"resource_admission": 1}, GenerationCalled: executed}
		original := delegation.Result{Status: "stopped", Attempts: []delegation.Attempt{{Result: inference.Result{Response: inference.Response{Text: "unconfirmed output"}, Measurement: m}}}}
		if executed {
			original.Attempts[0].Result.Response.Text = ""
			m.Outcome = "uncertain"
		}
		out := safeOutput(original, inference.ErrAdmissionDeferred)
		if out.ErrorCategory == "resource_deferred" {
			t.Fatal("unsafe execution/output evidence was erased before classification")
		}
	}
}

func TestResourceMetadataRejectsArbitraryTextAndInvalidNumbers(t *testing.T) {
	original := delegation.Result{Status: "stopped", SemanticReuse: &delegation.ReuseObservation{Status: "PRIVATE_STATUS_SECRET", ResourceAdmission: &delegation.ReuseResourceAdmission{Reason: "PRIVATE_REASON_SECRET", Outcome: "PRIVATE_OUTCOME_SECRET", DeferredReason: "PRIVATE_DEFERRED_SECRET", WaitMS: math.NaN(), QueueWaitMS: math.Inf(1), Samples: -1}}, Attempts: []delegation.Attempt{{ErrorCategory: "PRIVATE_ERROR_SECRET"}}}
	output := safeOutput(original, errors.New("PRIVATE_PROVIDER_SECRET"))
	data, err := json.Marshal(output)
	if err != nil || strings.Contains(string(data), "SECRET") {
		t.Fatalf("resource metadata leaked text or invalid numeric output: %s %v", data, err)
	}
	d := output.SemanticReuse.ResourceAdmission
	if d.Outcome != "" || d.DeferredReason != "" || d.WaitMS != 0 || d.QueueWaitMS != 0 || d.Samples != 0 || !math.IsNaN(original.SemanticReuse.ResourceAdmission.WaitMS) {
		t.Fatal("invalid resource metadata was retained or input mutated")
	}
}

func TestMixedStageMetadataKeepsOnlyOpaqueRuntimeAttribution(t *testing.T) {
	zero := int64(0)
	identity := strings.Repeat("a", 64)
	original := delegation.Result{Status: "stopped", SemanticReuse: &delegation.ReuseObservation{Verifications: []semantic.ValidityResult{{MixedModelUsage: true, Work: []semantic.VerificationWork{{Stage: "source", Model: "PRIVATE_MODEL_SECRET", RuntimeIdentity: identity, AdmissionDeferred: true, InputTokens: &zero, OutputTokens: &zero}}}}}}
	output := safeOutput(original, nil)
	w := output.SemanticReuse.Verifications[0].Work[0]
	data, err := json.Marshal(output)
	if err != nil || strings.Contains(string(data), "SECRET") || w.Model != "" || w.RuntimeIdentity != identity || !w.AdmissionDeferred || !output.SemanticReuse.Verifications[0].MixedModelUsage || original.SemanticReuse.Verifications[0].Work[0].Model != "PRIVATE_MODEL_SECRET" {
		t.Fatal("mixed model metadata leaked text, lost hash, or mutated input", err)
	}
	original.SemanticReuse.Verifications[0].Work[0].GenerationCalled = true
	output = safeOutput(original, nil)
	if output.SemanticReuse.Verifications[0].Work[0].AdmissionDeferred {
		t.Fatal("executed stage retained preclaim deferral flag")
	}
}
