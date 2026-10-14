package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/outcomes"
)

func outcomeQuestIO(output io.Writer) error {
	if _, err := fmt.Fprintln(output, "OUTCOME QUEST — six offline acceptance and rework scenarios\nReal scoped HTTP broker + temporary SQLite; synthetic costs and effort; no model calls."); err != nil {
		return err
	}
	scenarios := []struct {
		name string
		run  func(context.Context, *modelQuestFixture) (string, error)
	}{
		{"simple 1: checks are separate from acceptance", outcomeQuestAcceptance},
		{"simple 2: rewrite and explicit reacceptance", outcomeQuestRewrite},
		{"simple 3: exact metadata replay", outcomeQuestReplay},
		{"complex 1: failed work remains in cohort cost", outcomeQuestCohort},
		{"complex 2: unknowns and empty denominator", outcomeQuestUnknown},
		{"complex 3: stale feedback and durable restart", outcomeQuestRestart},
	}
	for _, scenario := range scenarios {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		fixture, err := newModelQuestFixture(ctx)
		var detail string
		if err == nil {
			detail, err = scenario.run(ctx, fixture)
			fixture.close()
		}
		cancel()
		if err != nil {
			return fmt.Errorf("%s: %w", scenario.name, err)
		}
		if _, err := fmt.Fprintf(output, "PASS  %s — %s\n", scenario.name, detail); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(output, "QUEST CLEARED — six scenarios passed. Synthetic accounting is not a live-provider saving or a verified human review.")
	return err
}

func fixtureOutcomeRun(id string, cost *float64, reused bool) outcomes.Run {
	basis := "caller_estimate"
	usage := inference.Usage{InputTokens: 12, UncachedInputTokens: 12, OutputTokens: 5}
	if reused {
		basis, usage = "reuse_zero", inference.Usage{}
	}
	return outcomes.Run{ID: id, RequestDigest: strings.Repeat("a", 64), Status: "completed", Attempts: []outcomes.Attempt{{
		TierID: "fixture", Fingerprint: strings.Repeat("b", 64), OutputSHA256: strings.Repeat("c", 64),
		CheckPassed: true, Complete: true, Durable: true, Reused: reused, GenerationCalled: !reused,
		UsageKnown: true, Usage: usage, EstimatedCostUSD: cost, CostBasis: basis,
	}}}
}

func fixtureAcceptance(id, runID string, minutes *float64) outcomes.Feedback {
	return outcomes.Feedback{ID: id, RunID: runID, Kind: "accepted", FinalArtifactSHA256: strings.Repeat("d", 64), HumanMinutes: minutes}
}

func outcomeQuestAcceptance(ctx context.Context, fixture *modelQuestFixture) (string, error) {
	store := outcomes.Store{Broker: fixture.clients[0]}
	task := outcomes.Task{ID: "checked-only", Revision: "fixture-1"}
	cost, zero, rate := 0.25, 0.0, 60.0
	run := fixtureOutcomeRun("run-1", &cost, false)
	receipt, err := store.Record(ctx, task, run)
	if err != nil {
		return "", err
	}
	report, err := store.Report(ctx, []outcomes.Task{task}, &rate)
	if err != nil || report.AcceptedTasks != 0 || report.CostPerAcceptedTaskUSD != nil || report.HumanCostUSD != nil {
		return "", errors.New("check success was confused with human acceptance or unknown effort")
	}
	if _, err := store.Feedback(ctx, task, receipt.Version, fixtureAcceptance("accept-1", run.ID, &zero)); err != nil {
		return "", err
	}
	report, err = store.Report(ctx, []outcomes.Task{task}, &rate)
	if err != nil || report.AcceptedTasks != 1 || report.CostPerAcceptedTaskUSD == nil || *report.CostPerAcceptedTaskUSD != cost {
		return "", errors.New("explicit acceptance did not qualify the recorded cost")
	}
	return "check-passed remains unreviewed; explicit acceptance qualifies one task", nil
}

func outcomeQuestRewrite(ctx context.Context, fixture *modelQuestFixture) (string, error) {
	store := outcomes.Store{Broker: fixture.clients[0]}
	cost, minutes, rate := 0.25, 1.0, 60.0
	for _, reason := range []string{"style", "correctness"} {
		task := outcomes.Task{ID: "rewrite-" + reason, Revision: "fixture-1"}
		run := fixtureOutcomeRun("run-1", &cost, false)
		receipt, err := store.Record(ctx, task, run)
		if err != nil {
			return "", err
		}
		receipt, err = store.Feedback(ctx, task, receipt.Version, fixtureAcceptance("accept-1", run.ID, &minutes))
		if err != nil {
			return "", err
		}
		receipt, err = store.Feedback(ctx, task, receipt.Version, outcomes.Feedback{ID: "rewrite-1", RunID: run.ID, Kind: "rewritten", Reason: reason, HumanMinutes: &minutes})
		if err != nil {
			return "", err
		}
		report, err := store.Report(ctx, []outcomes.Task{task}, &rate)
		if err != nil || report.AcceptedTasks != 0 || report.CostPerAcceptedTaskUSD != nil {
			return "", errors.New("rewritten task remained accepted")
		}
		if _, err := store.Feedback(ctx, task, receipt.Version, fixtureAcceptance("accept-2", run.ID, &minutes)); err != nil {
			return "", err
		}
		snapshot, err := store.Get(ctx, task)
		if err != nil || snapshot.State.Status != "accepted" || !snapshot.Runs[0].Attempts[0].CheckPassed || len(snapshot.Events) != 3 {
			return "", errors.New("reacceptance changed the original check result or lost feedback")
		}
		report, err = store.Report(ctx, []outcomes.Task{task}, &rate)
		if err != nil || report.CostPerAcceptedTaskUSD == nil || *report.CostPerAcceptedTaskUSD != 3.25 {
			return "", errors.New("review and correction effort was lost from cost")
		}
	}
	return "style and correctness rewrites reopen acceptance; reacceptance retains original checks and effort", nil
}

func outcomeQuestReplay(ctx context.Context, fixture *modelQuestFixture) (string, error) {
	store := outcomes.Store{Broker: fixture.clients[0]}
	task := outcomes.Task{ID: "replay", Revision: "fixture-1"}
	cost, zero, rate := 0.25, 0.0, 60.0
	run := fixtureOutcomeRun("run-1", &cost, false)
	original, err := store.Record(ctx, task, run)
	if err != nil {
		return "", err
	}
	replayed, err := (outcomes.Store{Broker: fixture.clients[1]}).Record(ctx, task, run)
	if err != nil || replayed != original {
		return "", errors.New("run replay changed its receipt")
	}
	feedback := fixtureAcceptance("accept-1", run.ID, &zero)
	accepted, err := store.Feedback(ctx, task, original.Version, feedback)
	if err != nil {
		return "", err
	}
	replayed, err = store.Feedback(ctx, task, original.Version, feedback)
	if err != nil || replayed != accepted {
		return "", errors.New("event replay changed its receipt")
	}
	if _, err := store.Record(ctx, task, fixtureOutcomeRun("reuse-1", &zero, true)); err != nil {
		return "", err
	}
	report, err := store.Report(ctx, []outcomes.Task{task}, &rate)
	if err != nil || report.RunCount != 2 || report.AcceptedTasks != 1 || report.KnownInferenceSubtotalUSD != cost {
		return "", errors.New("metadata replay or result reuse was billed twice")
	}
	return "cross-token run replay and event replay retain receipts; reuse adds zero new inference charge", nil
}

func outcomeQuestCohort(ctx context.Context, fixture *modelQuestFixture) (string, error) {
	store := outcomes.Store{Broker: fixture.clients[0]}
	accepted := outcomes.Task{ID: "accepted", Revision: "fixture-1"}
	rejected := outcomes.Task{ID: "failed", Revision: "fixture-1"}
	costA, costB, one, two, rate := 0.25, 0.75, 1.0, 2.0, 60.0
	runA := fixtureOutcomeRun("run-a", &costA, false)
	runB := fixtureOutcomeRun("run-b", &costB, false)
	runB.Status, runB.Attempts[0].CheckPassed = "quality_failed", false
	for _, item := range []struct {
		task  outcomes.Task
		run   outcomes.Run
		event outcomes.Feedback
	}{{accepted, runA, fixtureAcceptance("accept-1", runA.ID, &one)},
		{rejected, runB, outcomes.Feedback{ID: "reject-1", RunID: runB.ID, Kind: "rejected", HumanMinutes: &two}}} {
		receipt, err := store.Record(ctx, item.task, item.run)
		if err != nil {
			return "", err
		}
		if _, err := store.Feedback(ctx, item.task, receipt.Version, item.event); err != nil {
			return "", err
		}
	}
	report, err := store.Report(ctx, []outcomes.Task{accepted, rejected}, &rate)
	if err != nil || report.AcceptedTasks != 1 || report.KnownInferenceSubtotalUSD != 1 || report.CostPerAcceptedTaskUSD == nil || *report.CostPerAcceptedTaskUSD != 4 {
		return "", errors.New("failed task cost or human rework was omitted from cohort numerator")
	}
	return "synthetic $1 inference + 3 minutes at $60/hour across two tasks / one acceptance = $4", nil
}

func outcomeQuestUnknown(ctx context.Context, fixture *modelQuestFixture) (string, error) {
	store := outcomes.Store{Broker: fixture.clients[0]}
	cost, zero, rate := 0.25, 0.0, 60.0
	for _, mode := range []string{"cost", "effort", "rate", "unaccepted"} {
		task := outcomes.Task{ID: "unknown-" + mode, Revision: "fixture-1"}
		run := fixtureOutcomeRun("run-1", &cost, false)
		if mode == "cost" {
			run.Status = "stopped"
			run.Attempts[0] = outcomes.Attempt{Fingerprint: strings.Repeat("b", 64), GenerationCalled: true, CostBasis: "unknown"}
		}
		receipt, err := store.Record(ctx, task, run)
		if err != nil {
			return "", err
		}
		if mode != "unaccepted" {
			effort := &zero
			if mode == "effort" {
				effort = nil
			}
			if _, err := store.Feedback(ctx, task, receipt.Version, fixtureAcceptance("accept-1", run.ID, effort)); err != nil {
				return "", err
			}
		}
		valuation := &rate
		if mode == "rate" {
			valuation = nil
		}
		report, err := store.Report(ctx, []outcomes.Task{task}, valuation)
		if err != nil || report.CostPerAcceptedTaskUSD != nil {
			return "", errors.New("unknown cost, effort, rate, or no accepted tasks yielded a dollar-per-task number")
		}
	}
	return "missing cost, effort, rate, and zero accepted tasks produce null metrics", nil
}

func outcomeQuestRestart(ctx context.Context, fixture *modelQuestFixture) (string, error) {
	store := outcomes.Store{Broker: fixture.clients[0]}
	task := outcomes.Task{ID: "restart", Revision: "fixture-1"}
	cost, zero := 0.25, 0.0
	run := fixtureOutcomeRun("run-1", &cost, false)
	recorded, err := store.Record(ctx, task, run)
	if err != nil {
		return "", err
	}
	acceptedEvent := fixtureAcceptance("accept-1", run.ID, &zero)
	accepted, err := store.Feedback(ctx, task, recorded.Version, acceptedEvent)
	if err != nil {
		return "", err
	}
	rewrite := outcomes.Feedback{ID: "rewrite-1", RunID: run.ID, Kind: "rewritten", Reason: "correctness", HumanMinutes: &zero}
	if _, err := store.Feedback(ctx, task, recorded.Version, rewrite); !errors.Is(err, outcomes.ErrConflict) {
		return "", errors.New("stale feedback silently rebased")
	}
	if err := fixture.restart(); err != nil {
		return "", err
	}
	store = outcomes.Store{Broker: fixture.clients[0]}
	snapshot, err := store.Get(ctx, task)
	if err != nil || snapshot.Version != accepted.Version || snapshot.State.Status != "accepted" {
		return "", errors.New("accepted state did not survive broker restart")
	}
	if _, err := store.Feedback(ctx, task, accepted.Version, rewrite); err != nil {
		return "", err
	}
	replayed, err := store.Feedback(ctx, task, recorded.Version, acceptedEvent)
	if err != nil || replayed != accepted {
		return "", errors.New("old event retry did not retain its original receipt")
	}
	snapshot, err = store.Get(ctx, task)
	if err != nil || snapshot.State.Status != "rewritten" || snapshot.State.AcceptedRunID != "" {
		return "", errors.New("old acceptance retry overwrote a later rewrite")
	}
	return "stale feedback fails; restart retains state; an old acceptance retry cannot overwrite a later rewrite", nil
}
