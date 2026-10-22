package observability

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/inference"
)

func inferenceFixture() (inference.Spec, inference.Result) {
	start := time.Date(2030, 1, 2, 12, 0, 0, 0, time.UTC)
	usage := inference.Usage{InputTokens: 100, UncachedInputTokens: 70, CacheReadTokens: 20, CacheWriteTokens: 10, OutputTokens: 25, ReasoningTokens: 5}
	cost, first, last := 0.0123, float64(10), float64(20)
	return inference.Spec{Provider: "anthropic", Model: "requested-model", Input: "private prompt", Task: "private task", Revision: "private revision"},
		inference.Result{Durable: true, Response: inference.Response{Text: "private answer", Model: "actual-model", Complete: true},
			ThisRunUsage: usage, GenerationUsage: usage, CostUSD: &cost,
			Measurement: &inference.Measurement{StartedAt: start, EndedAt: start.Add(time.Second), Outcome: "complete",
				Fingerprint: strings.Repeat("a", 64), PhaseMS: map[string]float64{"generation": 100},
				GenerationCalled: true, KnownGenerationUsage: true, GenerationUsage: &usage,
				GenerationStartedAt: start.Add(100 * time.Millisecond), GenerationEndedAt: start.Add(200 * time.Millisecond),
				GenerationTiming: &inference.GenerationTiming{FirstTokenMS: &first, OutputMS: &last, Basis: "client_observed_content_chunks"},
			}}
}

func TestFromInferenceKeepsContentOutAndCopiesMutableMetadata(t *testing.T) {
	spec, result := inferenceFixture()
	e, err := FromInference(spec, result)
	if err != nil {
		t.Fatal(err)
	}
	if e.Provider != "claude" || e.Generation == nil || e.Generation.Model != "actual-model" || e.Generation.Usage.OutputTokens != 25 || e.Generation.Usage.ReasoningTokens != 5 {
		t.Fatalf("provider/usage mapping changed: %+v", e)
	}
	data, _ := json.Marshal(e)
	if strings.Contains(string(data), "private") {
		t.Fatal("private content entered telemetry")
	}
	e.PhaseMS["generation"] = 999
	*e.CostUSD = 999
	*e.Generation.CostUSD = 888
	*e.Generation.FirstTokenMS = 777
	if result.Measurement.PhaseMS["generation"] != 100 || *result.CostUSD != 0.0123 || *result.Measurement.GenerationTiming.FirstTokenMS != 10 {
		t.Fatal("caller mutation changed original inference metadata")
	}
}

func TestFromInferenceDistinguishesReuseUnknownAndLocalCost(t *testing.T) {
	spec, result := inferenceFixture()
	spec.Provider = "ollama"
	e, err := FromInference(spec, result)
	if err != nil || e.CostUSD == nil || *e.CostUSD != 0 || e.Generation.CostUSD == nil || *e.Generation.CostUSD != 0 {
		t.Fatalf("known local cost not explicit: %+v, %v", e, err)
	}
	result.Reused = true
	result.Measurement.GenerationCalled = false
	result.Measurement.Outcome = "reused"
	result.ThisRunUsage = inference.Usage{}
	result.AvoidedUsage = result.GenerationUsage
	result.GenerationTiming = result.Measurement.GenerationTiming
	e, err = FromInference(spec, result)
	if err != nil || e.Generation != nil || e.ThisRunUsage != (Usage{}) || e.CostUSD == nil || *e.CostUSD != 0 || e.AvoidedUsage.InputTokens != 100 {
		t.Fatalf("historical generation became a new charge/span: %+v, %v", e, err)
	}
	_, unknown := inferenceFixture()
	unknown.Measurement.Outcome = "uncertain"
	unknown.Measurement.KnownGenerationUsage = false
	unknown.Measurement.GenerationUsage = nil
	unknown.ThisRunUsage, unknown.GenerationUsage = inference.Usage{}, inference.Usage{}
	unknown.CostUSD = nil
	e, err = FromInference(spec, unknown)
	if err != nil || e.Generation == nil || e.Generation.KnownUsage || e.CostUSD != nil || e.Generation.CostUSD != nil {
		t.Fatalf("unknown cost became local zero: %+v, %v", e, err)
	}
}

func TestFromInferenceCopiesFreshKVAndOmitsHistoricalKVOnReuse(t *testing.T) {
	spec, result := inferenceFixture()
	spec.Provider = "sglang"
	spec.SGLang = &inference.SGLangConfig{SelfHosted: true}
	tokens := int64(5)
	result.Response.RuntimeCache = &inference.RuntimeCacheObservation{Profile: "hicache", CachedTokens: &tokens}
	e, err := FromInference(spec, result)
	if err != nil || e.Generation.RuntimeCache == nil {
		t.Fatalf("fresh KV missing: %v", err)
	}
	*e.Generation.RuntimeCache.CachedTokens = 4
	if tokens != 5 {
		t.Fatal("KV observation aliased original")
	}
	result.GenerationRuntimeCache = result.Response.RuntimeCache
	result.Reused = true
	result.Measurement.Outcome, result.Measurement.GenerationCalled = "reused", false
	result.ThisRunUsage, result.AvoidedUsage = inference.Usage{}, result.GenerationUsage
	e, err = FromInference(spec, result)
	if err != nil || e.Generation != nil {
		t.Fatalf("reuse emitted historical KV: %+v, %v", e, err)
	}
}

func TestFromInferenceRejectsMissingOrUnsafeMetadata(t *testing.T) {
	if _, err := FromInference(inference.Spec{}, inference.Result{}); !errors.Is(err, ErrInvalidExecution) {
		t.Fatal("missing measurement accepted")
	}
	spec, result := inferenceFixture()
	result.Measurement.PhaseMS["private arbitrary phase"] = 1
	if _, err := FromInference(spec, result); !errors.Is(err, ErrInvalidExecution) {
		t.Fatal("unallowlisted metadata accepted")
	}
}
