package outcomes

import (
	"context"
	"time"
)

func number(v float64) *float64 { return &v }

func addFinite(total *float64, value float64) error {
	next := *total + value
	if !finiteNonnegative(next) {
		return invalid("reported accounting sum overflows")
	}
	*total = next
	return nil
}

// Report never scans a possibly truncated artifact listing. The caller chooses
// a bounded explicit cohort, whose entries must all have registered ledgers.
// Every recorded attempt contributes to the numerator, including failed,
// rejected, reopened, and still-unreviewed tasks. Only current explicit human
// acceptance contributes to the denominator.
func (s Store) Report(ctx context.Context, tasks []Task, hourlyRateUSD *float64) (Report, error) {
	if len(tasks) < 1 || len(tasks) > MaxCohortTasks {
		return Report{}, invalid("report requires 1 to 256 explicit cohort tasks")
	}
	if hourlyRateUSD != nil && !finiteNonnegative(*hourlyRateUSD) {
		return Report{}, invalid("hourly USD rate must be finite and nonnegative")
	}
	seen := make(map[string]bool)
	for _, task := range tasks {
		key, err := Key(task)
		if err != nil {
			return Report{}, err
		}
		if seen[key] {
			return Report{}, invalid("cohort contains a duplicate task revision")
		}
		seen[key] = true
	}
	report := Report{Tasks: make([]TaskSummary, 0, len(tasks)), TaskCount: len(tasks), GeneratedAt: time.Now().UTC(), Basis: "caller_estimates_and_reported_local_token_charges", Coverage: "explicit_cohort_recorded_attempts_and_feedback_only"}
	if hourlyRateUSD != nil {
		report.HourlyRateUSD = number(*hourlyRateUSD)
	}
	for _, task := range tasks {
		snapshot, err := s.Get(ctx, task)
		if err != nil {
			return Report{}, err
		}
		t := TaskSummary{Task: task, Version: snapshot.Version, State: snapshot.State, RunCount: len(snapshot.Runs)}
		for _, run := range snapshot.Runs {
			t.AttemptCount += len(run.Attempts)
			for _, a := range run.Attempts {
				if !a.UsageKnown {
					t.MissingUsage++
				}
				if a.EstimatedCostUSD == nil {
					t.MissingInferenceCosts++
				} else if err := addFinite(&t.KnownInferenceSubtotalUSD, *a.EstimatedCostUSD); err != nil {
					return Report{}, err
				}
			}
		}
		for _, event := range snapshot.Events {
			if event.HumanMinutes == nil {
				t.MissingHumanEffort++
			} else if err := addFinite(&t.RecordedHumanMinutes, *event.HumanMinutes); err != nil {
				return Report{}, err
			}
		}
		if len(snapshot.Events) == 0 {
			t.MissingHumanEffort++
		}
		if t.State.Status == "accepted" {
			report.AcceptedTasks++
		}
		report.RunCount += t.RunCount
		report.AttemptCount += t.AttemptCount
		report.MissingInferenceCosts += t.MissingInferenceCosts
		report.MissingUsage += t.MissingUsage
		report.MissingHumanEffort += t.MissingHumanEffort
		if err := addFinite(&report.KnownInferenceSubtotalUSD, t.KnownInferenceSubtotalUSD); err != nil {
			return Report{}, err
		}
		if err := addFinite(&report.RecordedHumanMinutes, t.RecordedHumanMinutes); err != nil {
			return Report{}, err
		}
		report.Tasks = append(report.Tasks, t)
	}
	if report.MissingInferenceCosts == 0 {
		report.InferenceCostUSD = number(report.KnownInferenceSubtotalUSD)
		if report.AcceptedTasks > 0 {
			report.InferenceCostPerAcceptedTaskUSD = number(*report.InferenceCostUSD / float64(report.AcceptedTasks))
		}
	}
	if report.MissingHumanEffort == 0 && report.HourlyRateUSD != nil {
		cost := report.RecordedHumanMinutes / 60 * *report.HourlyRateUSD
		if !finiteNonnegative(cost) {
			return Report{}, invalid("human cost overflows")
		}
		report.HumanCostUSD = number(cost)
	}
	if report.InferenceCostUSD != nil && report.HumanCostUSD != nil {
		cost := *report.InferenceCostUSD + *report.HumanCostUSD
		if !finiteNonnegative(cost) {
			return Report{}, invalid("total cost overflows")
		}
		report.TotalCostUSD = number(cost)
		if report.AcceptedTasks > 0 {
			report.CostPerAcceptedTaskUSD = number(cost / float64(report.AcceptedTasks))
		}
	}
	return report, nil
}
