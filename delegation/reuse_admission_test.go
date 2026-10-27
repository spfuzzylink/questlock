package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/semantic"
)

func reuseAdmissionSnapshot(now time.Time) admission.Snapshot {
	return admission.Snapshot{At: now, Known: true, Source: "PRIVATE_SOURCE_SECRET", AvailableMemoryBytes: 512 * admission.MiB, MemoryPressure: admission.Normal, LoadCapacityMilli: 10000, LoadMilli: 1000}
}

func TestReuseResourceAdmissionExplainsReadingsAndPreservesThresholds(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name, reason string
		change       func(*admission.Snapshot)
		err          error
		unknown      bool
	}{
		{name: "probe error", reason: "sample_unavailable", err: errors.New("PRIVATE_PROBE_ERROR_SECRET"), unknown: true},
		{name: "unknown", reason: "sample_unavailable", change: func(s *admission.Snapshot) { s.Known = false }, unknown: true},
		{name: "zero timestamp", reason: "sample_invalid", change: func(s *admission.Snapshot) { s.At = time.Time{} }, unknown: true},
		{name: "future timestamp", reason: "sample_invalid", change: func(s *admission.Snapshot) { s.At = now.Add(time.Nanosecond) }, unknown: true},
		{name: "expired", reason: "sample_expired", change: func(s *admission.Snapshot) { s.At = now.Add(-5*time.Second - time.Nanosecond) }},
		{name: "age boundary", reason: "ready", change: func(s *admission.Snapshot) { s.At = now.Add(-5 * time.Second) }},
		{name: "invalid pressure", reason: "sample_invalid", change: func(s *admission.Snapshot) { s.MemoryPressure = "PRIVATE_PRESSURE_SECRET" }, unknown: true},
		{name: "negative memory", reason: "sample_invalid", change: func(s *admission.Snapshot) { s.AvailableMemoryBytes = -1 }, unknown: true},
		{name: "negative stall", reason: "sample_invalid", change: func(s *admission.Snapshot) { s.CPUStallMilliPercent = -1 }, unknown: true},
		{name: "impossible stall", reason: "sample_invalid", change: func(s *admission.Snapshot) { s.CPUStallMilliPercent = 100001 }, unknown: true},
		{name: "negative load", reason: "sample_invalid", change: func(s *admission.Snapshot) { s.LoadMilli = -1 }, unknown: true},
		{name: "negative load capacity", reason: "sample_invalid", change: func(s *admission.Snapshot) { s.LoadCapacityMilli = -1 }, unknown: true},
		{name: "warning with ample cached capacity", reason: "memory_pressure", change: func(s *admission.Snapshot) { s.MemoryPressure = admission.High; s.AvailableMemoryBytes = 8 << 30 }},
		{name: "critical with ample cached capacity", reason: "memory_pressure", change: func(s *admission.Snapshot) { s.MemoryPressure = admission.Critical; s.AvailableMemoryBytes = 8 << 30 }},
		{name: "warning before headroom", reason: "memory_pressure", change: func(s *admission.Snapshot) { s.MemoryPressure = admission.High; s.AvailableMemoryBytes = 0 }},
		{name: "zero available remains known", reason: "insufficient_memory", change: func(s *admission.Snapshot) { s.AvailableMemoryBytes = 0 }},
		{name: "below headroom", reason: "insufficient_memory", change: func(s *admission.Snapshot) { s.AvailableMemoryBytes = 256*admission.MiB - 1 }},
		{name: "headroom boundary", reason: "ready", change: func(s *admission.Snapshot) { s.AvailableMemoryBytes = 256 * admission.MiB }},
		{name: "stall boundary", reason: "cpu_stall", change: func(s *admission.Snapshot) { s.CPUStallMilliPercent = 20000 }},
		{name: "below stall boundary", reason: "ready", change: func(s *admission.Snapshot) { s.CPUStallMilliPercent = 19999 }},
		{name: "above load boundary", reason: "load", change: func(s *admission.Snapshot) { s.LoadMilli = 20001 }},
		{name: "load boundary", reason: "ready", change: func(s *admission.Snapshot) { s.LoadMilli = 20000 }},
		{name: "normal", reason: "ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sample := reuseAdmissionSnapshot(now)
			if tc.change != nil {
				tc.change(&sample)
			}
			got := reuseResourceAdmission(sample, tc.err, now)
			if got.Reason != tc.reason {
				t.Fatalf("wrong admission reason: %+v", got)
			}
			if tc.unknown {
				if got.AvailableMemoryBytes != nil || got.MemoryPressure != "" {
					t.Fatalf("unavailable/invalid reading invented resource measurements: %+v", got)
				}
			} else if got.AvailableMemoryBytes == nil || *got.AvailableMemoryBytes != sample.AvailableMemoryBytes || got.MemoryPressure != sample.MemoryPressure {
				t.Fatalf("valid measurement lost, including known zero: %+v", got)
			}
			data, err := json.Marshal(got)
			if err != nil || strings.Contains(string(data), "SECRET") || tc.unknown && !strings.Contains(string(data), `"available_memory_bytes":null`) {
				t.Fatalf("resource diagnostics exposed raw input or lost unknown availability: %s %v", data, err)
			}
		})
	}
}

func reuseAdmissionRouter(t *testing.T, resources admission.Source) (Router, *fixtureProvider, *fixtureProvider, *shadowFailureEmbedder) {
	t.Helper()
	broker := testBroker(t)
	target := &fixtureProvider{identity: "reuse-admission-target-fixture", response: knownResult(`{"ship":false}`).Response}
	verifier := &fixtureProvider{identity: "reuse-admission-verifier-fixture", response: knownResult(`{"verdict":"accept","same_intent":true,"same_facts":true,"same_constraints":true,"answer_supported":true}`).Response}
	embedder := &shadowFailureEmbedder{}
	reuser := &ControlledReuser{
		Broker: broker, Index: controlledStoredIndex{broker}, Resources: resources,
		EmbedderFactory: func(context.Context, semantic.Config) (semantic.Embedder, error) { return embedder, nil },
		VerifierFactory: func(_ context.Context, config semantic.ReuseConfig) (semantic.Verifier, error) {
			return &LocalVerifier{runner: &inference.Runner{Broker: broker, Provider: verifier}, config: config, identity: reuseHash("reuse-admission-verifier-fixture")}, nil
		},
	}
	return Router{Reuse: reuser, Factory: func(Tier) (Runner, error) { return &inference.Runner{Broker: broker, Provider: target}, nil }}, target, verifier, embedder
}

func TestControlledReusePressureDiagnosticsDoNotStartOptionalInference(t *testing.T) {
	for _, reason := range []string{"sample_unavailable", "sample_invalid", "sample_expired", "memory_pressure", "insufficient_memory", "cpu_stall", "load", "ready"} {
		t.Run(reason, func(t *testing.T) {
			probes := 0
			resources := admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
				probes++
				sample := reuseAdmissionSnapshot(time.Now())
				switch reason {
				case "sample_unavailable":
					return sample, errors.New("PRIVATE_PROBE_ERROR_SECRET")
				case "sample_invalid":
					sample.At = sample.At.Add(time.Hour)
				case "sample_expired":
					sample.At = sample.At.Add(-time.Minute)
				case "memory_pressure":
					sample.MemoryPressure = admission.High
				case "insufficient_memory":
					sample.AvailableMemoryBytes = 256*admission.MiB - 1
				case "cpu_stall":
					sample.CPUStallMilliPercent = 20000
				case "load":
					sample.LoadMilli = 20001
				}
				return sample, nil
			})
			router, target, verifier, embedder := reuseAdmissionRouter(t, resources)
			result, err := router.Run(t.Context(), controlledWaitRequest("How do I reset an account password?", 0))
			observation := result.SemanticReuse
			if err != nil || result.Status != "completed" || observation == nil || observation.ResourceAdmission == nil || observation.ResourceAdmission.Reason != reason || probes != 1 {
				t.Fatalf("optional admission diagnostic lost: %+v %v probes=%d", result, err, probes)
			}
			// This optional gate does not control the caller's fresh-generation path.
			if target.generations != 1 || verifier.generations != 0 {
				t.Fatalf("optional skip changed target generation or called verifier: target=%d verifier=%d", target.generations, verifier.generations)
			}
			if reason == "ready" {
				if observation.Status != "miss_or_rejected" || !observation.Indexed || embedder.calls.Load() != 1 {
					t.Fatalf("ready resource probe did not preserve normal retrieval: %+v", observation)
				}
			} else if observation.Status != "resource_pressure" || observation.Indexed || embedder.calls.Load() != 0 || observation.Searches != 0 || len(observation.Verifications) != 0 {
				t.Fatalf("resource rejection started optional inference or changed status: %+v embeddings=%d", observation, embedder.calls.Load())
			}
		})
	}
}

func TestControlledReuseExactAndAliasReplayDoNotProbeResources(t *testing.T) {
	probes, blockProbe := 0, false
	resources := admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		probes++
		if blockProbe {
			t.Fatal("durable replay probed resources")
		}
		return reuseAdmissionSnapshot(time.Now()), nil
	})
	router, target, verifier, embedder := reuseAdmissionRouter(t, resources)
	sourceRequest := controlledWaitRequest("How do I reset an account password?", 0)
	source, err := router.Run(t.Context(), sourceRequest)
	if err != nil || source.SemanticReuse == nil || !source.SemanticReuse.Indexed || source.SemanticReuse.ResourceAdmission == nil || source.SemanticReuse.ResourceAdmission.Reason != "ready" || probes != 1 {
		t.Fatalf("source setup failed: %+v %v probes=%d", source, err, probes)
	}
	blockProbe = true
	exact, err := router.Run(t.Context(), sourceRequest)
	if err != nil || exact.SemanticReuse == nil || exact.SemanticReuse.Status != "skipped_exact" || exact.SemanticReuse.ResourceAdmission != nil || probes != 1 || target.generations != 1 || embedder.calls.Load() != 1 {
		t.Fatalf("exact replay did resource or inference work: %+v %v probes=%d", exact, err, probes)
	}
	blockProbe = false
	paraphraseRequest := controlledWaitRequest("How can I reset an account password?", 0)
	paraphrase, err := router.Run(t.Context(), paraphraseRequest)
	if err != nil || paraphrase.SemanticReuse == nil || !paraphrase.SemanticReuse.GenerationAvoided || paraphrase.SemanticReuse.ResourceAdmission == nil || paraphrase.SemanticReuse.ResourceAdmission.Reason != "ready" || probes != 2 || target.generations != 1 || verifier.generations != 1 || embedder.calls.Load() != 2 {
		t.Fatalf("semantic alias setup failed: %+v %v probes=%d", paraphrase, err, probes)
	}
	blockProbe = true
	alias, err := router.Run(t.Context(), paraphraseRequest)
	if err != nil || alias.SemanticReuse == nil || alias.SemanticReuse.Status != "reused" || !alias.SemanticReuse.GenerationAvoided || alias.SemanticReuse.ResourceAdmission != nil || probes != 2 || target.generations != 1 || verifier.generations != 1 || embedder.calls.Load() != 2 {
		t.Fatalf("alias replay did resource or inference work: %+v %v probes=%d", alias, err, probes)
	}
}
