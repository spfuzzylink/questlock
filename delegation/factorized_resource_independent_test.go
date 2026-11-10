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
	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/semantic"
)

// Synthetic probes and scripted providers verify admission and claim behavior;
// they neither load the host nor establish model quality or real-load savings.
type independentResourceProvider struct {
	inference.Provider
	counts, generations int
}

func (p *independentResourceProvider) CountInputTokens(ctx context.Context, request inference.Request) (int64, error) {
	p.counts++
	return p.Provider.CountInputTokens(ctx, request)
}

func (p *independentResourceProvider) Generate(ctx context.Context, request inference.Request) (inference.Response, error) {
	p.generations++
	return p.Provider.Generate(ctx, request)
}

type independentResourceVerifier struct {
	*FactorizedVerifier
	verifications int
}

func (v *independentResourceVerifier) Verify(ctx context.Context, request semantic.ValidityRequest) (semantic.ValidityResult, error) {
	v.verifications++
	return v.FactorizedVerifier.Verify(ctx, request)
}

type independentResourceWork struct {
	embedderFactories, verifications, counts, generations int
	embeddings                                            int32
}

func independentResourceSetup(t *testing.T, resources admission.Source) (*independentFactorizedFixture, func() independentResourceWork) {
	t.Helper()
	f := independentFactorizedSetup(t)
	p := &independentResourceProvider{Provider: f.verifier.runner.Provider}
	f.verifier.runner.Provider = p
	v := &independentResourceVerifier{FactorizedVerifier: f.verifier}
	embedder := &shadowFailureEmbedder{}
	factories := 0
	f.router.Reuse.Resources = resources
	f.router.Reuse.EmbedderFactory = func(context.Context, semantic.Config) (semantic.Embedder, error) {
		factories++
		return embedder, nil
	}
	f.router.Reuse.VerifierFactory = func(context.Context, semantic.ReuseConfig) (semantic.Verifier, error) { return v, nil }
	// Verifier construction/identity can inspect runtime metadata before this
	// gate. The counters deliberately measure Verify, counts and generation.
	return f, func() independentResourceWork {
		return independentResourceWork{embedderFactories: factories, embeddings: embedder.calls.Load(), verifications: v.verifications, counts: p.counts, generations: p.generations}
	}
}

func TestIndependentFactorizedDeniedResourcesSkipAllOptionalInference(t *testing.T) {
	for _, test := range []struct {
		name, reason string
		change       func(*admission.Snapshot)
		err          error
	}{
		{"high pressure", "memory_pressure", func(s *admission.Snapshot) { s.MemoryPressure = admission.High }, nil},
		{"critical pressure", "memory_pressure", func(s *admission.Snapshot) { s.MemoryPressure = admission.Critical }, nil},
		{"no headroom", "insufficient_memory", func(s *admission.Snapshot) { s.AvailableMemoryBytes = 0 }, nil},
		{"cpu stall", "cpu_stall", func(s *admission.Snapshot) { s.CPUStallMilliPercent = 20000 }, nil},
		{"load", "load", func(s *admission.Snapshot) { s.LoadMilli = 20001 }, nil},
		{"stale", "sample_expired", func(s *admission.Snapshot) { s.At = s.At.Add(-time.Minute) }, nil},
		{"unknown", "sample_unavailable", func(s *admission.Snapshot) { s.Known = false }, nil},
		{"invalid", "sample_invalid", func(s *admission.Snapshot) { s.At = s.At.Add(time.Hour) }, nil},
		{"probe failure", "sample_unavailable", nil, errors.New("synthetic probe failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			denied, probes := false, 0
			resources := admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
				probes++
				sample := reuseAdmissionSnapshot(time.Now())
				if denied {
					if test.change != nil {
						test.change(&sample)
					}
					return sample, test.err
				}
				return sample, nil
			})
			f, work := independentResourceSetup(t, resources)
			f.seed(t)
			before := work()
			denied = true
			f.request.Spec.Input = "Before the preview can proceed, what is still required?"
			out, err := f.router.Run(t.Context(), f.request)
			obs := out.SemanticReuse
			if err != nil || out.Status != "completed" || obs == nil || obs.ResourceAdmission == nil || obs.ResourceAdmission.Reason != test.reason || obs.Status != "resource_pressure" || probes != 2 {
				t.Fatalf("resource denial changed normal target completion or lost its reason: observation=%+v error=%v", obs, err)
			}
			if work() != before || obs.Searches != 0 || len(obs.Verifications) != 0 || obs.GenerationAvoided || obs.Indexed || obs.Source != nil || f.target.generations != 2 || f.target.countCalls != 2 || len(out.Attempts) != 1 || out.Attempts[0].Result.Resolution != nil || out.Attempts[0].Result.ReuseKind == "semantic" {
				t.Fatalf("denied optional work ran or created a serving alias: before=%+v after=%+v observation=%+v", before, work(), obs)
			}
		})
	}
}

func TestIndependentFactorizedResourceSkipPreservesTargetQualityBoundsAndUncertainty(t *testing.T) {
	for _, kind := range []string{"quality failure", "input cap", "unknown generation"} {
		t.Run(kind, func(t *testing.T) {
			denied, probes := true, 0
			resources := admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
				probes++
				sample := reuseAdmissionSnapshot(time.Now())
				if denied {
					sample.MemoryPressure = admission.High
				}
				return sample, nil
			})
			f, work := independentResourceSetup(t, resources)
			wantError, wantGenerations := ErrStopped, 1
			switch kind {
			case "quality failure":
				f.target.response = knownResult(`{"ship":true}`).Response
				wantError = ErrQualityFailed
			case "input cap":
				f.request.Policy.Local.Spec.MaxInputTokens = 99
				wantGenerations = 0
			case "unknown generation":
				f.target.err = errors.New("synthetic unknown target outcome")
			}
			out, err := f.router.Run(t.Context(), f.request)
			if !errors.Is(err, wantError) || out.Response != nil || out.SemanticReuse == nil || out.SemanticReuse.Status != "resource_pressure" || work() != (independentResourceWork{}) || probes != 1 || f.target.countCalls != 1 || f.target.generations != wantGenerations {
				t.Fatalf("resource skip bypassed target quality/bounds/outcome rules: observation=%+v work=%+v error=%v", out.SemanticReuse, work(), err)
			}
			if kind != "unknown generation" {
				return
			}
			key, _ := independentResourceTargetIdentity(t, f)
			before, err := f.broker.Get(t.Context(), key)
			var claim struct{ State string }
			if err != nil || json.Unmarshal([]byte(before.Content), &claim) != nil || claim.State != "uncertain" {
				t.Fatal("unknown target did not retain an uncertain claim")
			}
			denied, f.target.err = false, nil
			again, err := f.router.Run(t.Context(), f.request)
			after, readErr := f.broker.Get(t.Context(), key)
			if !errors.Is(err, inference.ErrInFlight) || again.Response != nil || readErr != nil || after.Version != before.Version || after.Content != before.Content || probes != 1 || f.target.generations != 1 || f.target.countCalls != 1 || work() != (independentResourceWork{}) {
				t.Fatalf("resource recovery bypassed or rewrote uncertain target work: observation=%+v error=%v", again.SemanticReuse, err)
			}
		})
	}
}

func independentResourceTargetIdentity(t *testing.T, f *independentFactorizedFixture) (key, requestFingerprint string) {
	t.Helper()
	spec := tierSpec(f.request, f.request.Policy.Local, requestDigest(f.request))
	plan, err := inference.BuildPlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	return "inference/v1/jobs/" + reuseHash(plan.Fingerprint+"\x00"+f.target.Identity()), plan.Fingerprint
}

func TestIndependentFactorizedPendingTargetPrecedesResourceAdmission(t *testing.T) {
	probes := 0
	f, work := independentResourceSetup(t, admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		probes++
		return admission.Snapshot{}, errors.New("synthetic unavailable resources")
	}))
	key, requestFingerprint := independentResourceTargetIdentity(t, f)
	data, _ := json.Marshal(map[string]any{"schema": 1, "fingerprint": strings.TrimPrefix(key, "inference/v1/jobs/"), "request_fingerprint": requestFingerprint, "state": "pending", "created_at": f.now})
	published, err := f.broker.Publish(t.Context(), protocol.PublishRequest{Key: key, OperationID: "independent-pending-target", ExpectedVersion: 0, Content: string(data)})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		out, err := f.router.Run(t.Context(), f.request)
		after, readErr := f.broker.Get(t.Context(), key)
		if !errors.Is(err, inference.ErrInFlight) || out.Response != nil || readErr != nil || after.Version != published.Artifact.Version || after.Content != published.Artifact.Content || probes != 0 || f.target.generations != 0 || f.target.countCalls != 0 || work() != (independentResourceWork{}) {
			t.Fatalf("pending target was bypassed by optional resource handling: observation=%+v error=%v", out.SemanticReuse, err)
		}
	}
}

func TestIndependentFactorizedUnknownVerificationCannotBecomeResourceSkip(t *testing.T) {
	denied, probes := false, 0
	f, work := independentResourceSetup(t, admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		probes++
		sample := reuseAdmissionSnapshot(time.Now())
		if denied {
			sample.MemoryPressure = admission.High
		}
		return sample, nil
	}))
	f.seed(t)
	f.stages.failSource = true
	f.request.Spec.Input = "Before the preview can proceed, what is still required?"
	first, err := f.router.Run(t.Context(), f.request)
	if !errors.Is(err, ErrStopped) || first.SemanticReuse == nil || len(first.SemanticReuse.Verifications) != 1 || len(first.SemanticReuse.Verifications[0].Work) != 1 || probes != 2 || f.target.generations != 1 || work().verifications != 1 || work().counts != 1 || work().generations != 1 {
		t.Fatalf("unknown factorized source setup failed: observation=%+v error=%v", first.SemanticReuse, err)
	}
	targetKey, _ := independentResourceTargetIdentity(t, f)
	keys := []string{targetKey, first.SemanticReuse.Verifications[0].Work[0].ReceiptKey}
	before := make([]protocol.Artifact, len(keys))
	for i, key := range keys {
		before[i], err = f.broker.Get(t.Context(), key)
		var claim struct{ State string }
		if err != nil || json.Unmarshal([]byte(before[i].Content), &claim) != nil || claim.State != "uncertain" {
			t.Fatal("unknown verification failed to fence both source stage and target")
		}
	}
	beforeWork := work()
	denied, f.stages.failSource = true, false
	again, err := f.router.Run(t.Context(), f.request)
	if !errors.Is(err, inference.ErrInFlight) || again.Response != nil || probes != 2 || work() != beforeWork || f.target.generations != 1 || f.target.countCalls != 1 {
		t.Fatalf("denied optional capacity bypassed unknown verification: observation=%+v error=%v", again.SemanticReuse, err)
	}
	for i, key := range keys {
		after, err := f.broker.Get(t.Context(), key)
		if err != nil || after.Version != before[i].Version || after.Content != before[i].Content {
			t.Fatal("resource change rewrote an uncertain verifier or target claim")
		}
	}
}

func TestIndependentFactorizedExactOriginalAndAliasNeedNoOptionalCapacity(t *testing.T) {
	denied, probes := false, 0
	f, work := independentResourceSetup(t, admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		probes++
		sample := reuseAdmissionSnapshot(time.Now())
		if denied {
			sample.AvailableMemoryBytes, sample.MemoryPressure = 0, admission.Critical
		}
		return sample, nil
	}))
	originalQuestion := f.request.Spec.Input
	f.seed(t)
	f.request.Spec.Input = "Before the preview can proceed, what is still required?"
	alias, err := f.router.Run(t.Context(), f.request)
	if err != nil || alias.SemanticReuse == nil || !alias.SemanticReuse.GenerationAvoided || probes != 2 || work().verifications != 1 || work().counts != 2 || work().generations != 2 || f.target.generations != 1 {
		t.Fatalf("factorized alias setup failed: observation=%+v error=%v", alias.SemanticReuse, err)
	}
	aliasQuestion := f.request.Spec.Input
	before := work()
	denied = true
	for _, question := range []string{originalQuestion, aliasQuestion} {
		f.request.Spec.Input = question
		out, err := f.router.Run(t.Context(), f.request)
		if err != nil || out.Response == nil || out.SemanticReuse == nil || out.SemanticReuse.ResourceAdmission != nil || probes != 2 || work() != before || f.target.generations != 1 || f.target.countCalls != 1 {
			t.Fatalf("durable exact replay required optional capacity: observation=%+v error=%v", out.SemanticReuse, err)
		}
		if question == originalQuestion && out.SemanticReuse.Status != "skipped_exact" || question == aliasQuestion && (out.SemanticReuse.Status != "reused" || !out.SemanticReuse.GenerationAvoided) {
			t.Fatalf("resource-independent replay lost its original/alias semantics: %+v", out.SemanticReuse)
		}
	}
}
