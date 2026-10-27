package outcomes

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/inference"
)

func verificationAttempt(index int, passed bool, cost float64) Attempt {
	a := completed("verification", cost).Attempts[0]
	a.TierID = fmt.Sprintf("reuse-verifier-%d", index)
	a.Fingerprint = digest(fmt.Sprintf("verifier-fingerprint-%d", index))
	a.OutputSHA256 = digest(fmt.Sprintf("verifier-output-%d", index))
	a.CheckPassed = passed
	return a
}

func unknownVerification(index int) Attempt {
	a := verificationAttempt(index, false, 0)
	a.Complete, a.Durable, a.UsageKnown = false, false, false
	a.OutputSHA256, a.CostBasis = "", "unknown"
	a.Usage, a.EstimatedCostUSD = inference.Usage{}, nil
	return a
}

func TestVerificationWorkRecordsIndependentDecisionsAndUnknownAccounting(t *testing.T) {
	s, _, _ := testStore(t)
	task := task("independent-verifier-accounting")
	run := completed("target", .25)
	run.VerificationWork = []Attempt{
		verificationAttempt(1, true, .125),
		verificationAttempt(2, false, .25),
		unknownVerification(3),
		verificationAttempt(4, false, .375),
	}
	r := record(t, s, task, run)
	got := snapshot(t, s, task)
	if len(got.Runs) != 1 || !reflect.DeepEqual(got.Runs[0].Run, run) || got.State.Status != "unreviewed" {
		t.Fatalf("auxiliary decisions changed target or human state: %+v", got)
	}
	if got.Runs[0].Status != "completed" || !got.Runs[0].Attempts[0].CheckPassed || got.Runs[0].VerificationWork[3].CheckPassed {
		t.Fatal("final rejected verifier changed the passing target decision")
	}
	replay, err := s.Record(ctx, task, run)
	if err != nil || replay != r {
		t.Fatal("exact verifier accounting replay appended or changed the receipt")
	}
	report, err := s.Report(ctx, []Task{task}, number(60))
	if err != nil {
		t.Fatal(err)
	}
	if report.AttemptCount != 5 || report.Tasks[0].AttemptCount != 5 || report.KnownInferenceSubtotalUSD != 1 || report.MissingInferenceCosts != 1 || report.MissingUsage != 1 || report.InferenceCostUSD != nil || report.AcceptedTasks != 0 || report.CostPerAcceptedTaskUSD != nil {
		t.Fatalf("verification work disappeared from cost or coverage: %+v", report)
	}
	feedback(t, s, task, r.Version, accept("human-acceptance", run.ID, number(2)))
	report, err = s.Report(ctx, []Task{task}, number(60))
	if err != nil || report.AcceptedTasks != 1 || report.MissingUsage != 1 || report.MissingInferenceCosts != 1 || report.InferenceCostPerAcceptedTaskUSD != nil || report.TotalCostUSD != nil || report.CostPerAcceptedTaskUSD != nil {
		t.Fatalf("human acceptance erased unknown verifier work: %+v, %v", report, err)
	}
}

func TestTenVerificationRecordsContributeToKnownCostNumerator(t *testing.T) {
	s, _, _ := testStore(t)
	task := task("ten-verifiers")
	run := completed("target", .5)
	for i := range MaxVerificationWork {
		run.VerificationWork = append(run.VerificationWork, verificationAttempt(i+1, i != MaxVerificationWork-1, .125))
	}
	r := record(t, s, task, run)
	got := snapshot(t, s, task)
	if len(got.Runs[0].Attempts) != 1 || len(got.Runs[0].VerificationWork) != 10 || got.State.Status != "unreviewed" {
		t.Fatal("auxiliary work was merged with target attempts or inferred acceptance")
	}
	feedback(t, s, task, r.Version, accept("human-acceptance", run.ID, number(0)))
	report, err := s.Report(ctx, []Task{task}, number(60))
	if err != nil {
		t.Fatal(err)
	}
	if report.RunCount != 1 || report.AttemptCount != 11 || report.AcceptedTasks != 1 || report.KnownInferenceSubtotalUSD != 1.75 || report.MissingInferenceCosts != 0 || report.MissingUsage != 0 || report.TotalCostUSD == nil || *report.TotalCostUSD != 1.75 || report.InferenceCostPerAcceptedTaskUSD == nil || *report.InferenceCostPerAcceptedTaskUSD != 1.75 || report.CostPerAcceptedTaskUSD == nil || *report.CostPerAcceptedTaskUSD != 1.75 {
		t.Fatalf("known verifier work was not charged exactly once: %+v", report)
	}
}

func TestAuxiliaryChecksCannotAuthorizeTargetCompletionFailureOrEscalation(t *testing.T) {
	for _, test := range []struct {
		name  string
		alter func(*Run)
		valid bool
	}{
		{"completed needs target", func(r *Run) { r.Attempts = nil }, false},
		{"completed needs passing target", func(r *Run) { r.Attempts[0].CheckPassed = false }, false},
		{"completed needs durable target", func(r *Run) { r.Attempts[0].Durable = false }, false},
		{"quality failure needs target", func(r *Run) { r.Status, r.Attempts = "quality_failed", nil }, false},
		{"quality failure cannot use rejected verifier", func(r *Run) { r.Status = "quality_failed"; r.VerificationWork[0].CheckPassed = false }, false},
		{"quality failure ignores accepted verifier", func(r *Run) { r.Status = "quality_failed"; r.Attempts[0].CheckPassed = false }, true},
		{"rejected verifier cannot authorize escalation", func(r *Run) {
			r.VerificationWork[0].CheckPassed = false
			r.Attempts = append(r.Attempts, r.Attempts[0])
		}, false},
		{"accepted verifier cannot repair uncertain first target", func(r *Run) {
			r.Attempts[0].CheckPassed = false
			r.Attempts[0].Durable = false
			r.Attempts = append(r.Attempts, completed("next", 0).Attempts[0])
		}, false},
		{"stopped admission can retain verifier work", func(r *Run) { r.Status, r.Attempts = "stopped", nil }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _, _ := testStore(t)
			task := task("target-status")
			run := completed("target", 0)
			run.VerificationWork = []Attempt{verificationAttempt(1, true, 0)}
			test.alter(&run)
			_, err := s.Record(ctx, task, run)
			if test.valid {
				if err != nil || snapshot(t, s, task).State.Status != "unreviewed" {
					t.Fatalf("valid target state or human boundary changed: %v", err)
				}
			} else if !errors.Is(err, ErrInvalid) {
				t.Fatalf("auxiliary checks authorized an invalid target: %v", err)
			}
		})
	}
}

func TestInvalidVerificationWorkCannotMutateLedger(t *testing.T) {
	s, _, _ := testStore(t)
	task := task("invalid-verifier-work")
	original := record(t, s, task, completed("original", 0))
	tests := map[string]func(*Run){
		"too many auxiliary records": func(r *Run) {
			for i := range MaxVerificationWork {
				r.VerificationWork = append(r.VerificationWork, verificationAttempt(i+2, false, 0))
			}
		},
		"ordinary attempts still capped at two": func(r *Run) { r.Attempts = append(r.Attempts, r.Attempts[0], r.Attempts[0]) },
		"wrong verifier prefix":                 func(r *Run) { r.VerificationWork[0].TierID = "local" },
		"empty verifier suffix":                 func(r *Run) { r.VerificationWork[0].TierID = "reuse-verifier-" },
		"missing fingerprint":                   func(r *Run) { r.VerificationWork[0].Fingerprint = "" },
		"invalid fingerprint":                   func(r *Run) { r.VerificationWork[0].Fingerprint = "not-a-hash" },
		"uppercase fingerprint":                 func(r *Run) { r.VerificationWork[0].Fingerprint = strings.ToUpper(digest("verifier")) },
		"missing complete output hash":          func(r *Run) { r.VerificationWork[0].OutputSHA256 = "" },
		"invalid output hash":                   func(r *Run) { r.VerificationWork[0].OutputSHA256 = "not-a-hash" },
		"unknown usage with counters":           func(r *Run) { r.VerificationWork[0].UsageKnown = false },
		"unknown cost with guessed zero":        func(r *Run) { r.VerificationWork[0].CostBasis = "unknown" },
		"negative cost":                         func(r *Run) { r.VerificationWork[0].EstimatedCostUSD = number(-1) },
		"nonfinite cost":                        func(r *Run) { r.VerificationWork[0].EstimatedCostUSD = number(math.Inf(1)) },
		"invalid normalized usage":              func(r *Run) { r.VerificationWork[0].Usage.CacheReadTokens++ },
		"token bucket overflow": func(r *Run) {
			r.VerificationWork[0].Usage = inference.Usage{InputTokens: math.MaxInt64, UncachedInputTokens: math.MaxInt64, CacheReadTokens: 1}
		},
		"deterministic edit forbidden": func(r *Run) { r.VerificationWork[0].CostBasis = "deterministic_edit" },
	}
	for name, alter := range tests {
		t.Run(name, func(t *testing.T) {
			run := completed("invalid", 0)
			run.VerificationWork = []Attempt{verificationAttempt(1, false, 0)}
			alter(&run)
			if _, err := s.Record(ctx, task, run); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid auxiliary work accepted: %v", err)
			}
			if snapshot(t, s, task).Version != original.Version {
				t.Fatal("invalid verification work changed the ledger")
			}
		})
	}
}

func TestVerificationCostsParticipateInLedgerAndCohortOverflowChecks(t *testing.T) {
	s, _, _ := testStore(t)
	for _, bothAuxiliary := range []bool{false, true} {
		run := completed("overflow", math.MaxFloat64)
		run.VerificationWork = []Attempt{verificationAttempt(1, false, math.MaxFloat64)}
		if bothAuxiliary {
			run.Attempts[0].EstimatedCostUSD = number(0)
			run.VerificationWork = append(run.VerificationWork, verificationAttempt(2, true, math.MaxFloat64))
		}
		if _, err := s.Record(ctx, task("per-ledger-overflow"), run); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unreportable target or auxiliary subtotal published: %v", err)
		}
	}
	tasks := []Task{task("auxiliary-overflow-a"), task("auxiliary-overflow-b")}
	for i, task := range tasks {
		run := completed(fmt.Sprintf("target-%d", i), 0)
		run.VerificationWork = []Attempt{verificationAttempt(1, false, math.MaxFloat64)}
		record(t, s, task, run)
	}
	if _, err := s.Report(ctx, tasks, number(0)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cohort auxiliary cost overflow became unknown or free: %v", err)
	}
}

func TestLegacyRunsOmitAuxiliaryFieldAndKeepExactReplayIdentity(t *testing.T) {
	s, _, _ := testStore(t)
	task := task("legacy-without-verifiers")
	run := completed("legacy", 0)
	legacy := struct {
		ID            string    `json:"id"`
		RequestDigest string    `json:"request_digest"`
		Status        string    `json:"status"`
		Attempts      []Attempt `json:"attempts"`
	}{run.ID, run.RequestDigest, run.Status, run.Attempts}
	encoded, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	oldEncoded, _ := json.Marshal(legacy)
	if string(encoded) != string(oldEncoded) || strings.Contains(string(encoded), "verification_work") {
		t.Fatal("legacy run serialization changed")
	}
	r := record(t, s, task, run)
	run.VerificationWork = []Attempt{}
	replay, err := s.Record(ctx, task, run)
	if err != nil || replay != r || snapshot(t, s, task).Runs[0].VerificationWork != nil {
		t.Fatalf("nil or empty auxiliary work changed an old record or replay: %v", err)
	}
}
