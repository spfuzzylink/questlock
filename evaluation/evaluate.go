package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"time"

	"github.com/spfuzzylink/questlock/inference"
)

// Evaluate runs each explicit configuration once per case, then measures one
// reuse of that exact successful snapshot. It never retries, changes models,
// or invokes a judge. Callers choose runners and isolated broker scopes; an
// already-reused first result cannot qualify as a fresh-generation baseline.
func Evaluate(ctx context.Context, suite Suite, factory RunnerFactory) (Report, error) {
	return evaluate(ctx, suite, factory, "live", nil)
}

type fixtureTiming func(Case, Configuration, bool) float64

func evaluate(ctx context.Context, suite Suite, factory RunnerFactory, mode string, timing fixtureTiming) (Report, error) {
	if err := Validate(suite); err != nil {
		return Report{}, err
	}
	if factory == nil {
		return Report{}, errors.New("evaluation runner factory is required")
	}
	objective := suite.Objective
	if objective == "" {
		objective = "latency"
	}
	report := Report{Schema: Schema, SuiteID: suite.ID, Revision: suite.Revision, SuiteSHA256: suiteDigest(suite), Mode: mode, Objective: objective}
	report.Limitations = []string{
		"Checks validate only the declared JSON values, literal text and text counts; they do not establish factual support, tone, suitability, general semantic accuracy, code correctness, or a universal model ranking.",
		"One generation and one snapshot reuse per case/configuration are observations, not statistically reliable latency estimates or percentile measurements.",
		"Elapsed time covers the whole Runner call, including planning, coordination and provider work; it is not time to first token or pure model inference time.",
		"Reported USD uses supplied token prices and provider-reported usage, not invoices. Missing USD is unknown; local hardware and electricity costs are not measured.",
		"Generated-token totals depend on each model's tokenizer and are not a dollar-cost comparison across providers.",
		"Selection applies only to this suite, these settings and this environment; it does not change any model or route subsequent work.",
	}
	if mode == "fixture" {
		report.Limitations = append(report.Limitations, "Fixture answers, usage and durations are supplied simulations. In-memory broker coordination simulates durable records; this mode does not benchmark models or prove restart persistence.")
	}
	for _, config := range suite.Configurations {
		candidate := Candidate{ConfigurationID: config.ID, CasesTotal: len(suite.Cases), Eligible: true}
		if config.Spec.Prices != nil {
			zero := float64(0)
			candidate.EstimatedUSD = &zero
		}
		for _, c := range suite.Cases {
			if err := ctx.Err(); err != nil {
				return report, err
			}
			row := CaseResult{CaseID: c.ID, Complexity: c.Complexity, ConfigurationID: config.ID}
			runner, err := factory(c, config)
			if err != nil || runner == nil {
				row.First.ErrorCategory = "runner_initialization_failed"
				row.First.TimingSource = timingSource(mode)
				row.First.Checks = NotEvaluated(c.Checks)
				candidate.Eligible = false
				report.Results = append(report.Results, row)
				continue
			}
			spec := caseSpec(suite, c, config, report.SuiteSHA256)
			started := time.Now()
			first, firstErr := runner.Run(ctx, spec)
			elapsed := float64(time.Since(started)) / float64(time.Millisecond)
			if timing != nil {
				elapsed = timing(c, config, false)
			}
			row.Fingerprint = first.Fingerprint
			row.First = measurement(first, firstErr, elapsed, mode, c.Checks)
			candidate.GeneratedTokens += first.ThisRunUsage.InputTokens + first.ThisRunUsage.OutputTokens
			if candidate.EstimatedUSD != nil {
				if first.CostUSD == nil || !finiteNonnegative(*first.CostUSD) {
					candidate.EstimatedUSD = nil
				} else {
					*candidate.EstimatedUSD += *first.CostUSD
				}
			}
			candidate.MeanFirstRunMS += elapsed
			candidate.FirstRunSamples++
			// This second call is allowed only after an unambiguous durable,
			// complete response. A failed quality check does not secretly change
			// Runner's protocol reuse policy; that candidate stays ineligible.
			if firstErr == nil && first.Response.Complete && first.Durable {
				started = time.Now()
				second, secondErr := runner.Run(ctx, spec)
				elapsed = float64(time.Since(started)) / float64(time.Millisecond)
				if timing != nil {
					elapsed = timing(c, config, true)
				}
				reused := measurement(second, secondErr, elapsed, mode, c.Checks)
				row.Reuse = &reused
				candidate.MeanReuseMS += elapsed
				candidate.ReuseSamples++
				row.Eligible = row.First.FreshGeneration && validFreshMetadata(first) && validGenerationUsage(first.GenerationUsage) && first.ThisRunUsage == first.GenerationUsage && first.Response.Usage == first.GenerationUsage &&
					row.First.QualityPassed && reused.QualityPassed && secondErr == nil && reused.Reused && reused.Complete && reused.Durable &&
					first.Fingerprint != "" && first.Fingerprint == second.Fingerprint && row.First.OutputSHA256 == reused.OutputSHA256 &&
					second.ThisRunUsage == (inference.Usage{}) && second.Response.Usage == (inference.Usage{}) && second.AvoidedUsage == first.GenerationUsage &&
					validReuseMetadata(second) &&
					validReuseCost(first, second, config.Spec.Prices != nil) &&
					(config.MaxFirstRunMS == 0 || row.First.ElapsedMS <= float64(config.MaxFirstRunMS)) &&
					(config.MaxReuseMS == 0 || reused.ElapsedMS <= float64(config.MaxReuseMS))
			}
			if row.Eligible {
				candidate.CasesPassed++
			} else {
				candidate.Eligible = false
			}
			report.Results = append(report.Results, row)
		}
		if candidate.FirstRunSamples > 0 {
			candidate.MeanFirstRunMS /= float64(candidate.FirstRunSamples)
		}
		if candidate.ReuseSamples > 0 {
			candidate.MeanReuseMS /= float64(candidate.ReuseSamples)
		}
		report.Candidates = append(report.Candidates, candidate)
	}
	selectCandidate(&report)
	return report, nil
}

func measurement(result inference.Result, err error, elapsed float64, mode string, checks []Check) Measurement {
	checked, passed := NotEvaluated(checks), false
	if err == nil && result.Response.Complete {
		checked, passed = CheckOutput(result.Response.Text, checks)
	}
	fresh := !result.Reused && validGenerationUsage(result.GenerationUsage) && result.ThisRunUsage == result.GenerationUsage && result.Response.Usage == result.GenerationUsage
	if result.Measurement != nil {
		fresh = !result.Reused && result.Measurement.GenerationCalled
	}
	m := Measurement{ElapsedMS: elapsed, TimingSource: timingSource(mode), FreshGeneration: fresh,
		Reused: result.Reused, Complete: result.Response.Complete, Durable: result.Durable, QualityPassed: passed, Checks: checked,
		ThisRunUsage: result.ThisRunUsage, AvoidedUsage: result.AvoidedUsage, CostUSD: result.CostUSD, AvoidedCostUSD: result.AvoidedCostUSD}
	if mode == "live" {
		m.Inference = result.Measurement
	}
	if result.Response.Text != "" {
		digest := sha256.Sum256([]byte(result.Response.Text))
		m.OutputSHA256 = hex.EncodeToString(digest[:])
	}
	if err != nil {
		m.ErrorCategory = errorCategory(err)
	}
	return m
}

func timingSource(mode string) string {
	if mode == "fixture" {
		return "simulated_fixture"
	}
	return "observed_runner_wall_clock"
}

func errorCategory(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, inference.ErrInFlight):
		return "pending_or_uncertain"
	case errors.Is(err, inference.ErrIncomplete):
		return "incomplete"
	case errors.Is(err, inference.ErrBudgetExceeded):
		return "budget_exceeded"
	default:
		return "run_failed"
	}
}

func finiteNonnegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func validGenerationUsage(usage inference.Usage) bool {
	for _, n := range []int64{usage.InputTokens, usage.UncachedInputTokens, usage.CacheReadTokens, usage.CacheWriteTokens, usage.OutputTokens, usage.ReasoningTokens} {
		if n < 0 || n > 1000000000 {
			return false
		}
	}
	return usage.InputTokens > 0 && usage.InputTokens == usage.UncachedInputTokens+usage.CacheReadTokens+usage.CacheWriteTokens && usage.ReasoningTokens <= usage.OutputTokens
}

func validReuseCost(first, second inference.Result, priced bool) bool {
	if !priced {
		return true
	}
	return first.CostUSD != nil && finiteNonnegative(*first.CostUSD) && second.CostUSD != nil && *second.CostUSD == 0 && second.AvoidedCostUSD != nil && *second.AvoidedCostUSD == *first.CostUSD
}

func validFreshMetadata(result inference.Result) bool {
	if result.Measurement == nil {
		return true
	}
	m := result.Measurement
	return m.GenerationCalled && m.KnownGenerationUsage && m.GenerationUsage != nil && *m.GenerationUsage == result.GenerationUsage
}

func validReuseMetadata(result inference.Result) bool {
	return result.Measurement == nil || (!result.Measurement.GenerationCalled && !result.Measurement.KnownGenerationUsage && result.Measurement.GenerationUsage == nil)
}

func selectCandidate(report *Report) {
	best := -1
	for i, candidate := range report.Candidates {
		if !candidate.Eligible || (report.Objective == "priced_cost" && candidate.EstimatedUSD == nil) {
			continue
		}
		if best == -1 || lessCandidate(candidate, report.Candidates[best], report.Objective) {
			best = i
		}
	}
	if best == -1 {
		report.SelectionNote = "No configuration satisfied every declared quality, durability, fresh-generation, exact-reuse, and latency gate with the required measurements."
		return
	}
	metric := "mean_first_run_ms"
	if report.Objective == "generated_tokens" {
		metric = "generated_tokens"
	} else if report.Objective == "priced_cost" {
		metric = "estimated_usd_at_supplied_prices"
	}
	scope := "observed_suite_only"
	if report.Mode == "fixture" {
		scope = "simulated_fixture_suite_only"
	}
	report.Selection = &Selection{ConfigurationID: report.Candidates[best].ConfigurationID, Metric: metric, Scope: scope}
	report.SelectionNote = "Selected the lowest objective value among configurations passing every gate; ties use mean reuse time then configuration ID. No model settings were changed."
}

func lessCandidate(a, b Candidate, objective string) bool {
	av, bv := a.MeanFirstRunMS, b.MeanFirstRunMS
	if objective == "generated_tokens" {
		av, bv = float64(a.GeneratedTokens), float64(b.GeneratedTokens)
	} else if objective == "priced_cost" {
		av, bv = *a.EstimatedUSD, *b.EstimatedUSD
	}
	if av != bv {
		return av < bv
	}
	if a.MeanReuseMS != b.MeanReuseMS {
		return a.MeanReuseMS < b.MeanReuseMS
	}
	return a.ConfigurationID < b.ConfigurationID
}
