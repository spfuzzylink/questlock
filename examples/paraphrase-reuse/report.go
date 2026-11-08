package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"

	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/semantic"
)

type measurementHeader struct {
	Schema                       string                           `json:"schema"`
	CasesSHA256                  string                           `json:"cases_sha256"`
	Model                        string                           `json:"model"`
	VerifierModel                string                           `json:"verifier_model"`
	VerifierPairModel            string                           `json:"verifier_pair_model,omitempty"`
	VerifierMaxInputTokens       int64                            `json:"verifier_max_input_tokens,omitempty"`
	VerifierSourceMaxInputTokens int64                            `json:"verifier_source_max_input_tokens,omitempty"`
	VerifierPairMaxInputTokens   int64                            `json:"verifier_pair_max_input_tokens,omitempty"`
	VerifierPolicy               string                           `json:"verifier_policy"`
	VerifierSourceThink          bool                             `json:"verifier_source_think,omitempty"`
	VerifierPairThink            bool                             `json:"verifier_pair_think,omitempty"`
	VerifierPairTemperature      *float64                         `json:"verifier_pair_temperature,omitempty"`
	VerifierPairSeed             *int64                           `json:"verifier_pair_seed,omitempty"`
	VerifierMaxOutputTokens      int64                            `json:"verifier_max_output_tokens,omitempty"`
	VerifierTimeoutMS            int                              `json:"verifier_timeout_ms,omitempty"`
	SemanticTimeoutMS            int                              `json:"semantic_timeout_ms,omitempty"`
	MinSimilarity                float64                          `json:"min_similarity"`
	DailyBudget                  inference.TokenBudget            `json:"daily_budget"`
	FreshBaseline                bool                             `json:"fresh_baseline"`
	FreshRepeatBaseline          bool                             `json:"fresh_repeat_baseline,omitempty"`
	PaidFallback                 bool                             `json:"paid_fallback_enabled"`
	ProviderCharge               float64                          `json:"provider_api_charge_usd"`
	ResourcePolicy               *delegation.ReuseAdmissionPolicy `json:"resource_policy,omitempty"`
}
type usageSummary struct {
	Calls       int   `json:"generation_calls"`
	KnownInput  int64 `json:"known_input_tokens"`
	KnownOutput int64 `json:"known_output_tokens"`
	Unknown     int   `json:"unknown_usage_records"`
	ZeroCurrent int   `json:"zero_current_usage_records"`
}
type phaseSummary struct {
	Expected             int                      `json:"expected_rows"`
	Rows                 int                      `json:"rows"`
	Missing              int                      `json:"missing_rows"`
	Passed               int                      `json:"quality_and_phase_passed"`
	QualityPassed        int                      `json:"output_checks_passed"`
	Failed               int                      `json:"failed"`
	ResourceDeferred     int                      `json:"resource_deferred"`
	Withheld             int                      `json:"withheld"`
	Recorded             int                      `json:"outcome_recorded"`
	Unconfirmed          int                      `json:"outcome_unconfirmed"`
	ExactWork            int                      `json:"exact_work_failures"`
	Generation           usageSummary             `json:"answer_generation"`
	Verification         usageSummary             `json:"verification"`
	VerificationByModel  map[string]*usageSummary `json:"verification_by_model,omitempty"`
	VerificationRuntimes map[string]string        `json:"verification_runtime_identities,omitempty"`
	MixedModelUsage      bool                     `json:"mixed_model_usage,omitempty"`
	EmbeddingWorkRecords int                      `json:"embedding_work_records"`
	EmbeddingTokens      int64                    `json:"known_embedding_input_tokens"`
	EmbeddingUnknown     int                      `json:"unknown_embedding_usage"`
	AttemptMS            float64                  `json:"known_attempt_elapsed_ms"`
	UnknownAttemptMS     int                      `json:"unknown_attempt_elapsed_records"`
	VerificationMS       float64                  `json:"reported_verification_ms"`
	EmbeddingMS          float64                  `json:"reported_embedding_ms"`
	EndToEndMS           float64                  `json:"known_end_to_end_ms"`
	UnknownEndToEndMS    int                      `json:"unknown_end_to_end_records"`
}
type attemptReport struct {
	Schema                string                   `json:"schema"`
	Phases                map[string]*phaseSummary `json:"phases"`
	FinalMarker           bool                     `json:"final_marker_present"`
	DeclaredFailures      bool                     `json:"declared_failures"`
	Eligibility           map[string]bool          `json:"comparison_eligibility"`
	Reasons               []string                 `json:"ineligible_reasons"`
	Comparison            string                   `json:"comparison_basis"`
	ComparedRequests      int                      `json:"compared_requests_per_arm"`
	WholeEligibility      map[string]bool          `json:"whole_cohort_comparison_eligibility"`
	WholeReasons          []string                 `json:"whole_cohort_ineligible_reasons"`
	WholeComparedRequests int                      `json:"whole_cohort_compared_requests_per_arm"`
	LatencyBasis          string                   `json:"latency_basis"`
	LatencyEligibility    map[string]bool          `json:"end_to_end_latency_comparison_eligibility"`
}

func reportFile(path string, out io.Writer) error {
	file, err := privateFile(path, false)
	if err != nil {
		return errors.New("report requires an existing absolute private regular file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > 8<<20 {
		return errors.New("report exceeds the 8 MiB file bound")
	}
	report, err := summarizeMeasurement(file)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(report)
}

func summarizeMeasurement(input io.Reader) (attemptReport, error) {
	bad := errors.New("invalid measurement schema or work attribution") // Never echo private input or decoder errors.
	report := attemptReport{Schema: "questlock.paraphrase-attempt-report.v1", Phases: map[string]*phaseSummary{}, Eligibility: map[string]bool{}, Reasons: []string{}, Comparison: "observed usage subtotals only; primary comparison is 12 paraphrases versus 12 fresh baselines; opt-in whole-cohort comparison is 12 paraphrases plus 12 exact repeats versus 24 measured fresh baselines; verifier and embedding tokenizers remain separate; source-answer generation is common cold work, while seed indexing/registration and embedding are reuse overhead; the full seed phase is reported separately and is not all common work; no dollar or savings estimate", LatencyBasis: "elapsed_ms measures immediately before Router.Run through RecordOutcome, including finish/indexing and outcome registration; excludes forensic output fetch and JSONL writes. Runner Measurement.TotalMS is a partial clock; separate verifier/embedding clocks may overlap it or extend after runner finish and are never added."}
	scanner := bufio.NewScanner(io.LimitReader(input, (8<<20)+1))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	if !scanner.Scan() {
		return report, bad
	}
	var header measurementHeader
	sum := sha256.Sum256(casesJSON)
	if strictRecord(scanner.Bytes(), &header) != nil || header.Schema != "questlock.paraphrase-measurement.v1" || header.CasesSHA256 != hex.EncodeToString(sum[:]) || header.Model == "" || header.VerifierModel == "" || header.VerifierPolicy != "factorized-v1" || header.PaidFallback || header.ProviderCharge != 0 || header.FreshRepeatBaseline && !header.FreshBaseline {
		return report, bad
	}
	if !validMeasurementVerifierHeader(scanner.Bytes(), header) {
		return report, bad
	}
	if header.ResourcePolicy != nil {
		policy, err := delegation.NormalizeReuseAdmissionPolicy(*header.ResourcePolicy)
		if err != nil || policy != *header.ResourcePolicy {
			return report, bad
		}
	}
	if header.VerifierPairModel != "" && header.VerifierPairModel != header.VerifierModel {
		report.Comparison += " Source and applicability tokenizers differ: verifier aggregate counts are reservation-accounting sums only; use the per-model buckets, calls and elapsed time, not a normalized token-saving percentage."
	}
	var set caseSet
	if json.Unmarshal(casesJSON, &set) != nil {
		return report, bad
	}
	expected, checks := map[string]bool{}, map[string]businessCase{}
	for _, phase := range []string{"seed", "paraphrase", "exact", "baseline", "baseline_repeat"} {
		count := 12
		if phase == "seed" {
			count = 6
		}
		if phase == "baseline" && !header.FreshBaseline || phase == "baseline_repeat" && !header.FreshRepeatBaseline {
			count = 0
		}
		report.Phases[phase] = &phaseSummary{Expected: count}
		for _, c := range set.Cases {
			checks[c.ID] = c
			inputs := c.Paraphrases
			if phase == "seed" {
				inputs = []string{c.OriginalQuestion}
			}
			if count == 0 {
				continue
			}
			for _, question := range inputs {
				expected[c.ID+"\x00"+phase+"\x00"+question] = true
			}
		}
	}
	seen, runIDs := map[string]bool{}, map[string]bool{}
	receipts := map[string]receiptAttribution{}
	modelReceipts := map[string]receiptAttribution{}
	modelRuntimes := map[string]string{}
	rows := map[string]row{}
	for line := 2; scanner.Scan(); line++ {
		if line > 56 || report.FinalMarker {
			return report, bad
		}
		data := scanner.Bytes()
		var kind struct {
			Phase string `json:"phase"`
		}
		if json.Unmarshal(data, &kind) != nil {
			return report, bad
		}
		if kind.Phase == "complete" {
			var end struct {
				Phase  string `json:"phase"`
				Failed bool   `json:"known_quality_or_reuse_failures"`
			}
			if strictRecord(data, &end) != nil {
				return report, bad
			}
			report.FinalMarker, report.DeclaredFailures = true, end.Failed
			continue
		}
		var r row
		if strictRecord(data, &r) != nil {
			return report, bad
		}
		key := r.CaseID + "\x00" + r.Phase + "\x00" + r.Input
		if !expected[key] || seen[key] {
			return report, bad
		}
		seen[key], rows[key] = true, r
		p := report.Phases[r.Phase]
		p.Rows++
		if r.Withheld != "" {
			if r.Withheld != "source_failed_checks" || r.Phase != "paraphrase" && r.Phase != "exact" || r.Result != nil || r.Error != "" || r.ModelOutput != "" || r.ElapsedMS != nil {
				return report, bad
			}
			p.Withheld++
			continue
		}
		if r.Result == nil || len(r.Result.Attempts) > 2 {
			return report, bad
		}
		if r.ElapsedMS == nil {
			p.UnknownEndToEndMS++
		} else if *r.ElapsedMS < 0 {
			return report, bad
		} else {
			p.EndToEndMS += *r.ElapsedMS
		}
		result := r.Result
		c := checks[r.CaseID]
		if result.Status != "completed" && result.Status != "quality_failed" && result.Status != "stopped" {
			return report, bad
		}
		deferred := len(result.Attempts) == 1 && result.Attempts[0].ErrorCategory == "resource_deferred"
		if deferred {
			if header.ResourcePolicy == nil || result.Status != "stopped" || r.Error == "" || result.Response != nil || r.ModelOutput != "" {
				return report, bad
			}
			a := result.Attempts[0]
			m := a.Result.Measurement
			obs := result.SemanticReuse
			if !inference.PreClaimDeferred(a.Result, inference.ErrAdmissionDeferred) || a.ErrorCategory != "resource_deferred" || a.QualityPassed || a.Result.Durable || a.Result.Reused || a.Result.Response.Complete || a.Result.Response.Text != "" || a.Result.Fingerprint != "" || m == nil || m.Outcome != "deferred" || m.GenerationCalled || m.KnownGenerationUsage || m.GenerationUsage != nil {
				return report, bad
			}
			if obs == nil {
				if r.Phase != "baseline" && r.Phase != "baseline_repeat" {
					return report, bad
				}
			} else if obs.Status != "resource_deferred" || obs.ResourceAdmission == nil || obs.ResourceAdmission.Outcome != "deferred" || len(obs.Verifications) != 0 || obs.EmbeddingInputTokens != nil || obs.EmbeddingMS != 0 || obs.Searches != 0 || obs.Indexed || obs.GenerationAvoided || obs.Source != nil {
				return report, bad
			}
			for _, check := range a.Checks {
				if check.Passed || check.Status != "not_evaluated" {
					return report, bad
				}
			}
		}
		passed := r.Error == "" && result.Status == "completed" && len(result.Attempts) == 1
		qualityPassed := len(result.Attempts) == 1
		if t := result.OutcomeTracking; t != nil && t.Status == "recorded" {
			taskID := "semantic-source-" + c.ID
			if r.Phase == "baseline" || r.Phase == "baseline_repeat" {
				taskID = "semantic-baseline-" + c.ID + "-0"
				if r.Input == c.Paraphrases[1] {
					taskID = "semantic-baseline-" + c.ID + "-1"
				}
				if r.Phase == "baseline_repeat" {
					taskID = "semantic-baseline-" + c.ID + "-2"
					if r.Input == c.Paraphrases[1] {
						taskID = "semantic-baseline-" + c.ID + "-3"
					}
				}
			}
			if t.RunID == "" || runIDs[t.RunID] || t.Receipt == nil || t.Receipt.ID != t.RunID || t.Receipt.Task != t.Task || t.Task.ID != taskID || t.Task.Revision != set.Revision+"-"+c.ID || t.Receipt.Version < 1 || t.Record != nil {
				return report, bad
			}
			runIDs[t.RunID] = true
			p.Recorded++
		} else {
			p.Unconfirmed++
		}
		work := false
		for _, a := range result.Attempts {
			answer, m := a.Result, a.Result.Measurement
			quality := len(a.Checks) == len(c.Checks)
			for i, check := range a.Checks {
				if i >= len(c.Checks) || check.ID != c.Checks[i].ID || check.Kind != c.Checks[i].Kind {
					return report, bad
				}
				quality = quality && check.Passed && check.Status != "not_evaluated"
			}
			if a.QualityPassed && !quality {
				return report, bad
			}
			qualityPassed = qualityPassed && a.QualityPassed && quality
			passed = passed && a.QualityPassed && answer.Durable && answer.Response.Complete && a.ErrorCategory == ""
			if m == nil {
				p.Generation.Unknown++
				p.UnknownAttemptMS++
				passed = false
			} else {
				if m.Fingerprint != "" && answer.Fingerprint != "" && m.Fingerprint != answer.Fingerprint || m.KnownGenerationUsage != (m.GenerationUsage != nil) || m.KnownGenerationUsage && !m.GenerationCalled || m.TotalMS < 0 {
					return report, bad
				}
				p.AttemptMS += m.TotalMS
				receipt := answer.Fingerprint
				if receipt == "" {
					receipt = m.Fingerprint
				}
				if addUsage(&p.Generation, receipts, receipt, "answer:"+r.CaseID+":"+r.Input, m.GenerationCalled, answer.Reused, m.GenerationUsage) != nil {
					return report, bad
				}
				work = work || m.GenerationCalled || m.GenerationUsage != nil
				if m.GenerationUsage != nil && (*m.GenerationUsage != answer.ThisRunUsage || *m.GenerationUsage != answer.GenerationUsage) || !m.GenerationCalled && answer.ThisRunUsage != (inference.Usage{}) {
					return report, bad
				}
				fresh := !answer.Reused && m.GenerationCalled && m.KnownGenerationUsage
				if r.Phase == "seed" || r.Phase == "baseline" || r.Phase == "baseline_repeat" {
					passed = passed && fresh
				}
				if r.Phase == "paraphrase" {
					passed = passed && answer.Reused && answer.ReuseKind == "semantic"
				}
				if r.Phase == "exact" {
					passed = passed && answer.Reused
				}
			}
			if answer.Response.Model != "" && strings.TrimSuffix(answer.Response.Model, ":latest") != strings.TrimSuffix(header.Model, ":latest") {
				return report, bad
			}
			work = work || answer.ThisRunUsage != (inference.Usage{})
		}
		if result.SemanticShadow != nil {
			return report, bad
		}
		verified := false
		if obs := result.SemanticReuse; obs != nil {
			if obs.Status == "resource_deferred" && !deferred {
				return report, bad
			}
			if r.Phase == "paraphrase" && obs.Status != "reused" {
				passed = false
			}
			if obs.EmbeddingMS < 0 {
				return report, bad
			}
			p.EmbeddingMS += obs.EmbeddingMS
			embedding := obs.EmbeddingInputTokens != nil || obs.EmbeddingMS != 0 || obs.Searches > 0
			switch obs.Status {
			case "not_called", "skipped_exact", "busy", "resource_pressure", "resource_deferred", "verifier_unavailable", "embedding_unavailable":
			case "reused":
				// An exact semantic alias retains its "reused" status without
				// running retrieval. Novel semantic hits still require embedding
				// accounting even when their usage fields are missing.
				embedding = embedding || r.Phase != "exact"
			default:
				embedding = true
			}
			if embedding {
				p.EmbeddingWorkRecords++
				work = true
				if obs.EmbeddingInputTokens == nil {
					p.EmbeddingUnknown++
				} else if *obs.EmbeddingInputTokens < 0 || *obs.EmbeddingInputTokens > 1e9 {
					return report, bad
				} else {
					p.EmbeddingTokens += *obs.EmbeddingInputTokens
				}
			}
			if len(obs.Verifications) > 5 {
				return report, bad
			}
			for _, v := range obs.Verifications {
				work = true
				if v.DurationMS < 0 {
					return report, bad
				}
				p.VerificationMS += v.DurationMS
				if len(v.Work) == 0 {
					p.Verification.Unknown++
					if v.SharedWork || v.Reused {
						p.Verification.ZeroCurrent++
					}
					passed = false
					continue
				}
				if len(v.Work) > 2 || v.SharedWork {
					return report, bad
				}
				pairModel := header.VerifierPairModel
				if pairModel == "" {
					pairModel = header.VerifierModel
				}
				if v.MixedModelUsage != (len(v.Work) == 2 && pairModel != header.VerifierModel) {
					return report, bad
				}
				p.MixedModelUsage = p.MixedModelUsage || v.MixedModelUsage
				for i, w := range v.Work {
					stage := "source"
					if i == 1 {
						stage = "equivalence"
					}
					if w.Stage != stage || (w.InputTokens == nil) != (w.OutputTokens == nil) || w.ReceiptKey != "" && !strings.HasPrefix(w.ReceiptKey, "inference/v1/jobs/") || w.Complete && w.Durable && !w.GenerationCalled && !w.Reused {
						return report, bad
					}
					attributed := header.VerifierPairModel != "" || header.VerifierPairMaxInputTokens != 0 || header.VerifierSourceMaxInputTokens != 0
					totalCap := header.VerifierMaxInputTokens
					if totalCap == 0 {
						totalCap = 16384
					}
					model, inputCap := header.VerifierModel, totalCap/2
					if header.VerifierSourceMaxInputTokens != 0 {
						inputCap = header.VerifierSourceMaxInputTokens
					}
					if i == 1 {
						model, inputCap = pairModel, totalCap/2
						if header.VerifierPairMaxInputTokens != 0 {
							inputCap = header.VerifierPairMaxInputTokens
						}
					}
					if attributed {
						runtime, err := hex.DecodeString(w.RuntimeIdentity)
						if w.Model != model || err != nil || len(runtime) != 32 || strings.ToLower(w.RuntimeIdentity) != w.RuntimeIdentity {
							return report, bad
						}
						if prior := modelRuntimes[model]; prior != "" && prior != w.RuntimeIdentity {
							return report, bad
						}
						for other, identity := range modelRuntimes {
							if other != model && identity == w.RuntimeIdentity {
								return report, bad
							}
						}
						modelRuntimes[model] = w.RuntimeIdentity
					} else if w.Model != "" || w.RuntimeIdentity != "" {
						return report, bad
					}
					if w.InputTokens != nil && (*w.InputTokens < 0 || *w.InputTokens > inputCap) {
						return report, bad
					}
					var usage *inference.Usage
					if w.InputTokens != nil {
						usage = &inference.Usage{InputTokens: *w.InputTokens, UncachedInputTokens: *w.InputTokens, OutputTokens: *w.OutputTokens}
					}
					owner := "verifier:" + r.CaseID + ":" + stage
					if stage == "equivalence" {
						owner += ":" + r.Input
					}
					if addUsage(&p.Verification, receipts, strings.TrimPrefix(w.ReceiptKey, "inference/v1/jobs/"), owner, w.GenerationCalled, w.Reused, usage) != nil {
						return report, bad
					}
					if attributed {
						if p.VerificationByModel == nil {
							p.VerificationByModel, p.VerificationRuntimes = map[string]*usageSummary{}, map[string]string{}
						}
						if p.VerificationByModel[model] == nil {
							p.VerificationByModel[model] = &usageSummary{}
						}
						p.VerificationRuntimes[model] = w.RuntimeIdentity
						if addUsage(p.VerificationByModel[model], modelReceipts, strings.TrimPrefix(w.ReceiptKey, "inference/v1/jobs/"), owner, w.GenerationCalled, w.Reused, usage) != nil {
							return report, bad
						}
					}
					passed = passed && w.Complete && w.Durable
				}
				passed = passed && v.Complete && v.Durable
				verified = verified || v.Accepted() && len(v.Work) == 2
			}
		}
		if r.Phase == "paraphrase" && !verified {
			passed = false
		}
		if (r.Phase == "baseline" || r.Phase == "baseline_repeat") && result.SemanticReuse != nil {
			return report, bad
		}
		if r.Phase == "exact" && work {
			p.ExactWork++
			passed = false
		}
		if qualityPassed {
			p.QualityPassed++
		}
		if deferred {
			p.ResourceDeferred++
		} else if passed {
			p.Passed++
		} else {
			p.Failed++
		}
	}
	if scanner.Err() != nil {
		return report, bad
	}
	for key, r := range rows {
		c := checks[r.CaseID]
		source, ok := rows[r.CaseID+"\x00seed\x00"+c.OriginalQuestion]
		if r.Withheld != "" && ok && source.Result != nil && source.Result.Status == "completed" && source.Error == "" {
			return report, bad
		}
		if r.Phase == "exact" && r.Result != nil && len(r.Result.Attempts) == 1 {
			peer := rows[strings.Replace(key, "\x00exact\x00", "\x00paraphrase\x00", 1)]
			if peer.Result != nil && len(peer.Result.Attempts) == 1 && r.Result.Attempts[0].Result.Fingerprint != peer.Result.Attempts[0].Result.Fingerprint {
				return report, bad
			}
		}
	}
	complete, known, coverage, timed := report.FinalMarker, true, true, true
	for _, p := range report.Phases {
		p.Missing = p.Expected - p.Rows
		complete = complete && p.Missing == 0
		known = known && p.Generation.Unknown+p.Verification.Unknown+p.EmbeddingUnknown == 0
		coverage = coverage && p.Recorded == p.Rows-p.Withheld && p.Missing == 0
		timed = timed && p.UnknownEndToEndMS == 0 && p.Missing == 0
	}
	report.Eligibility = map[string]bool{"complete_run": complete, "matched_12_questions": report.Phases["paraphrase"].Rows == 12 && report.Phases["baseline"].Rows == 12, "source_quality": report.Phases["seed"].Passed == 6, "reuse_quality": report.Phases["paraphrase"].Passed == 12, "baseline_quality": report.Phases["baseline"].Passed == 12, "exact_zero_work_and_quality": report.Phases["exact"].Passed == 12, "all_usage_known": known && complete, "outcome_coverage_complete": coverage, "no_declared_failures": !report.DeclaredFailures}
	eligible := true
	for _, name := range []string{"complete_run", "matched_12_questions", "source_quality", "reuse_quality", "baseline_quality", "exact_zero_work_and_quality", "all_usage_known", "outcome_coverage_complete", "no_declared_failures"} {
		if !report.Eligibility[name] {
			report.Reasons = append(report.Reasons, name)
			eligible = false
		}
	}
	report.Eligibility["eligible"] = eligible
	if eligible {
		report.ComparedRequests = 12
	}
	report.WholeEligibility = map[string]bool{"matched_24_requests": report.Phases["paraphrase"].Rows+report.Phases["exact"].Rows == 24 && report.Phases["baseline"].Rows+report.Phases["baseline_repeat"].Rows == 24, "repeat_baseline_quality": report.Phases["baseline_repeat"].Passed == 12, "repeat_baseline_measured": header.FreshRepeatBaseline, "primary_conditions": eligible}
	wholeEligible := true
	report.WholeReasons = []string{}
	for _, key := range []string{"matched_24_requests", "repeat_baseline_quality", "repeat_baseline_measured", "primary_conditions"} {
		if !report.WholeEligibility[key] {
			report.WholeReasons = append(report.WholeReasons, key)
			wholeEligible = false
		}
	}
	report.WholeEligibility["eligible"] = wholeEligible
	if wholeEligible {
		report.WholeComparedRequests = 24
	}
	report.LatencyEligibility = map[string]bool{"all_executed_rows_timed": timed && complete, "primary_eligible": eligible && timed && complete, "whole_cohort_eligible": wholeEligible && timed && complete}
	return report, nil
}

// Optional metadata is absent in older logs. An explicitly present zero/null
// cap is not a normalized declaration and cannot silently inherit a default.
func validMeasurementVerifierHeader(raw []byte, h measurementHeader) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for key := range fields {
		for _, canonical := range []string{"verifier_source_think", "verifier_pair_think", "verifier_pair_temperature", "verifier_pair_seed", "verifier_max_output_tokens", "verifier_timeout_ms", "semantic_timeout_ms", "resource_policy", "verifier_pair_model", "verifier_pair_max_input_tokens", "verifier_max_input_tokens", "verifier_source_max_input_tokens"} {
			if strings.EqualFold(key, canonical) && key != canonical {
				return false
			}
		}
	}
	if _, present := fields["resource_policy"]; present && h.ResourcePolicy == nil {
		return false
	}
	if _, present := fields["verifier_pair_model"]; present && h.VerifierPairModel == "" {
		return false
	}
	if _, present := fields["verifier_pair_max_input_tokens"]; present && h.VerifierPairMaxInputTokens < 1 {
		return false
	}
	for _, item := range []struct {
		key   string
		value int64
	}{{"verifier_max_input_tokens", h.VerifierMaxInputTokens}, {"verifier_source_max_input_tokens", h.VerifierSourceMaxInputTokens}} {
		if _, present := fields[item.key]; present && item.value < 1 {
			return false
		}
	}
	output := int64(128)
	if h.VerifierSourceThink || h.VerifierPairThink {
		output = 0
	}
	if _, present := fields["verifier_max_output_tokens"]; present {
		if h.VerifierMaxOutputTokens < 32 {
			return false
		}
		output = h.VerifierMaxOutputTokens
	}
	timeout := 60000
	if h.VerifierSourceThink || h.VerifierPairThink {
		timeout = 0
	}
	if _, present := fields["verifier_timeout_ms"]; present {
		if h.VerifierTimeoutMS < 1 {
			return false
		}
		timeout = h.VerifierTimeoutMS
	}
	config, err := measurementVerifierConfig(h.VerifierModel, h.VerifierSourceThink, h.VerifierPairThink, output, timeout, h.MinSimilarity)
	if err != nil {
		return false
	}
	for key, value := range map[string]bool{"verifier_pair_temperature": h.VerifierPairTemperature != nil, "verifier_pair_seed": h.VerifierPairSeed != nil} {
		if _, present := fields[key]; present && !value {
			return false
		}
	}
	config.VerifierPairTemperature, config.VerifierPairSeed = h.VerifierPairTemperature, h.VerifierPairSeed
	config.VerifierPairModel, config.VerifierPairMaxInputTokens = h.VerifierPairModel, h.VerifierPairMaxInputTokens
	if h.VerifierMaxInputTokens != 0 {
		config.VerifierMaxInputTokens = h.VerifierMaxInputTokens
	}
	config.VerifierSourceMaxInputTokens = h.VerifierSourceMaxInputTokens
	config, err = semantic.NormalizeReuseConfig(config)
	if err != nil {
		return false
	}
	if _, present := fields["semantic_timeout_ms"]; present {
		if h.SemanticTimeoutMS < 1 {
			return false
		}
		config.TimeoutMS = h.SemanticTimeoutMS
		_, err = semantic.NormalizeReuseConfig(config)
	}
	return err == nil
}

type receiptAttribution struct {
	owner     string
	generated bool
}

func addUsage(total *usageSummary, receipts map[string]receiptAttribution, receipt, owner string, called, reused bool, u *inference.Usage) error {
	bad := errors.New("invalid work attribution")
	if called && reused {
		return bad
	}
	if receipt != "" || called || reused {
		digest, err := hex.DecodeString(receipt)
		previous, seen := receipts[receipt]
		if err != nil || len(digest) != 32 || strings.ToLower(receipt) != receipt || seen && (previous.owner != owner || called && previous.generated) {
			return bad
		}
		receipts[receipt] = receiptAttribution{owner: owner, generated: called || previous.generated}
	}
	if u != nil {
		if u.InputTokens < 0 || u.InputTokens > 1e9 || u.OutputTokens < 0 || u.OutputTokens > 1e9 || u.UncachedInputTokens < 0 || u.UncachedInputTokens > 1e9 || u.CacheReadTokens < 0 || u.CacheReadTokens > 1e9 || u.CacheWriteTokens < 0 || u.CacheWriteTokens > 1e9 || u.InputTokens != u.UncachedInputTokens+u.CacheReadTokens+u.CacheWriteTokens || u.ReasoningTokens < 0 || u.ReasoningTokens > u.OutputTokens {
			return bad
		}
	}
	if !called {
		if u != nil && *u != (inference.Usage{}) {
			return bad
		}
		total.ZeroCurrent++
		return nil
	}
	total.Calls++
	if u == nil {
		total.Unknown++
	} else {
		total.KnownInput += u.InputTokens
		total.KnownOutput += u.OutputTokens
	}
	return nil
}

// Enforce required non-optional fields recursively and reject duplicate JSON keys.
// Decoder details are deliberately kept out of errors because they can contain task data.
func strictRecord(data []byte, dst any) error {
	bad := errors.New("invalid record")
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil || d.Decode(new(any)) != io.EOF {
		return bad
	}
	d = json.NewDecoder(bytes.NewReader(data))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 40 {
			return bad
		}
		token, err := d.Token()
		if err != nil {
			return bad
		}
		delim, compound := token.(json.Delim)
		if !compound {
			return nil
		}
		keys := map[string]bool{}
		for d.More() {
			if delim == '{' {
				k, err := d.Token()
				if err != nil {
					return bad
				}
				name := k.(string)
				if keys[name] {
					return bad
				}
				keys[name] = true
			}
			if walk(depth+1) != nil {
				return bad
			}
		}
		_, err = d.Token()
		return err
	}
	if walk(0) != nil {
		return bad
	}
	var required func(json.RawMessage, reflect.Type) error
	required = func(raw json.RawMessage, typ reflect.Type) error {
		if typ.Kind() == reflect.Pointer {
			if bytes.Equal(raw, []byte("null")) {
				return nil
			}
			typ = typ.Elem()
		}
		if typ.Kind() == reflect.Struct && typ.PkgPath() != "time" {
			var fields map[string]json.RawMessage
			if json.Unmarshal(raw, &fields) != nil || fields == nil {
				return bad
			}
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				tag := strings.Split(f.Tag.Get("json"), ",")
				value, exists := fields[tag[0]]
				if !exists {
					if len(tag) == 1 {
						return bad
					}
					continue
				}
				if required(value, f.Type) != nil {
					return bad
				}
			}
		} else if typ.Kind() == reflect.Slice {
			var items []json.RawMessage
			if json.Unmarshal(raw, &items) != nil {
				return bad
			}
			for _, item := range items {
				if required(item, typ.Elem()) != nil {
					return bad
				}
			}
		} else if bytes.Equal(raw, []byte("null")) {
			return bad
		}
		return nil
	}
	return required(data, reflect.TypeOf(dst))
}
