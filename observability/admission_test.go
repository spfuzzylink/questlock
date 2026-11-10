package observability

import (
	"strings"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/inference"
)

func TestDeferredInferenceExportsNoGenerationOrErrorStatus(t *testing.T) {
	now := time.Now().UTC()
	r := inference.Result{Measurement: &inference.Measurement{StartedAt: now.Add(-20 * time.Millisecond), EndedAt: now, Fingerprint: strings.Repeat("a", 64), Outcome: "deferred", PhaseMS: map[string]float64{"plan": 1, "lookup": 1, "resource_admission": 18}}}
	e, err := FromInference(inference.Spec{Provider: "ollama", Model: "fixture"}, r)
	if err != nil || e.Outcome != Deferred || e.Generation != nil || e.Durable || e.Reused || e.ThisRunUsage != (Usage{}) || e.OriginalUsage != (Usage{}) {
		t.Fatalf("capacity became model failure/generation: %+v %v", e, err)
	}
	s := span(strings.Repeat("a", 32), strings.Repeat("b", 16), "", "deferred", 1, e.StartedAt, e.EndedAt, e.Outcome, nil)
	if s.Status.Code != 0 {
		t.Fatal("resource deferral exported as an execution/quality error")
	}
	e.Durable = true
	if validateExecution(e) == nil {
		t.Fatal("deferred execution claimed durable model output")
	}
	e.Durable = false
	e.Generation = &Generation{StartedAt: e.StartedAt, EndedAt: e.EndedAt, Outcome: Deferred}
	if validateExecution(e) == nil {
		t.Fatal("deferred execution accepted a generation child")
	}
}
