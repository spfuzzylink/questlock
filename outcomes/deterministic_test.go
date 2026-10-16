package outcomes

import (
	"errors"
	"testing"

	"github.com/spfuzzylink/questlock/inference"
)

func deterministicRun(status string) Run {
	run := completed("revision-1", 0)
	run.Status = status
	attempt := &run.Attempts[0]
	attempt.TierID = "checked_revision"
	attempt.GenerationCalled, attempt.UsageKnown = false, true
	attempt.Usage, attempt.CostBasis = inference.Usage{}, "deterministic_edit"
	if status == "quality_failed" {
		attempt.CheckPassed, attempt.Durable = false, false
	}
	return run
}

func TestDeterministicEditBasisIsAdditiveAndRecordsFailedChecks(t *testing.T) {
	s, _, _ := testStore(t)
	for _, status := range []string{"completed", "quality_failed", "stopped"} {
		run := deterministicRun(status)
		if status == "stopped" {
			run.Attempts[0].Durable = false
		}
		task := task("deterministic-" + status)
		if _, err := s.Record(ctx, task, run); err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		state := snapshot(t, s, task)
		if state.State.Status != "unreviewed" || state.Runs[0].Attempts[0].GenerationCalled {
			t.Fatal("edit claimed inference or human acceptance")
		}
	}
	for name, change := range map[string]func(*Run){
		"generation":            func(r *Run) { r.Attempts[0].GenerationCalled = true },
		"tokens":                func(r *Run) { r.Attempts[0].Usage = inference.Usage{InputTokens: 1, UncachedInputTokens: 1} },
		"cost":                  func(r *Run) { r.Attempts[0].EstimatedCostUSD = number(.01) },
		"unknown usage":         func(r *Run) { r.Attempts[0].UsageKnown = false },
		"incomplete":            func(r *Run) { r.Attempts[0].Complete = false },
		"wrong tier":            func(r *Run) { r.Attempts[0].TierID = "local" },
		"reuse":                 func(r *Run) { r.Attempts[0].Reused = true },
		"failed durable":        func(r *Run) { r.Status = "quality_failed"; r.Attempts[0].CheckPassed = false },
		"completed unpublished": func(r *Run) { r.Attempts[0].Durable = false },
	} {
		t.Run(name, func(t *testing.T) {
			run := deterministicRun("completed")
			change(&run)
			if err := validateRun(run); !errors.Is(err, ErrInvalid) {
				t.Fatal("incompatible deterministic record admitted")
			}
		})
	}
}

func TestLegacyNoGenerationAndInferenceFailureRequirementsAreUnchanged(t *testing.T) {
	legacy := deterministicRun("completed")
	legacy.Attempts[0].CostBasis = "no_generation"
	if err := validateRun(legacy); !errors.Is(err, ErrInvalid) {
		t.Fatal("legacy no_generation now permits completed output")
	}
	legacy = completed("run-1", .01)
	legacy.Status = "quality_failed"
	legacy.Attempts[0].CheckPassed, legacy.Attempts[0].Durable = false, false
	if err := validateRun(legacy); !errors.Is(err, ErrInvalid) {
		t.Fatal("non-durable inference failure now admitted")
	}
	legacy.Attempts[0].Durable = true
	if err := validateRun(legacy); err != nil {
		t.Fatal("legacy durable failed inference rejected")
	}
}
