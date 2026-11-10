package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/outcomes"
	"github.com/spfuzzylink/questlock/semantic"
)

func fixtureDigest(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func fixtureDailyBudget() inference.TokenBudget {
	return inference.TokenBudget{InputTokens: 524288, OutputTokens: 16384, TotalTokens: 540672}
}

func fixtureMeasurement(t *testing.T, rows []row, complete bool) string {
	t.Helper()
	h := measurementHeader{Schema: "questlock.paraphrase-measurement.v1", CasesSHA256: fixtureDigest(string(casesJSON)), Model: "fixture", VerifierModel: "other-tokenizer", VerifierPolicy: "factorized-v1", MinSimilarity: .25, DailyBudget: inference.TokenBudget{InputTokens: 524288, OutputTokens: 16384, TotalTokens: 540672}}
	h.FreshBaseline = true
	for _, r := range rows {
		h.FreshRepeatBaseline = h.FreshRepeatBaseline || r.Phase == "baseline_repeat"
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	if err := encoder.Encode(h); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if err := encoder.Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	if complete {
		_ = encoder.Encode(map[string]any{"phase": "complete", "known_quality_or_reuse_failures": false})
	}
	return out.String()
}
func fixtureRows(t *testing.T, repeat ...bool) []row {
	t.Helper()
	var set caseSet
	if err := json.Unmarshal(casesJSON, &set); err != nil {
		t.Fatal(err)
	}
	rows := []row{}
	zero, ten, one, two := int64(0), int64(10), int64(1), int64(2)
	phases := []string{"seed", "paraphrase", "exact", "baseline"}
	if len(repeat) > 0 && repeat[0] {
		phases = append(phases, "baseline_repeat")
	}
	for _, c := range set.Cases {
		for _, phase := range phases {
			inputs := c.Paraphrases
			if phase == "seed" {
				inputs = []string{c.OriginalQuestion}
			}
			for i, input := range inputs {
				id := fmt.Sprintf("%s-%s-%d", c.ID, phase, i)
				fingerprint := fixtureDigest(id)
				reused := phase == "paraphrase" || phase == "exact"
				if phase == "exact" {
					fingerprint = fixtureDigest(fmt.Sprintf("%s-paraphrase-%d", c.ID, i))
				}
				usage := inference.Usage{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 10}
				a := delegation.Attempt{TierID: "local", QualityPassed: true, Result: inference.Result{Fingerprint: fingerprint, Durable: true, Reused: reused, GenerationUsage: usage, Response: inference.Response{Model: "fixture", Complete: true}, Measurement: &inference.Measurement{TotalMS: 50, PhaseMS: map[string]float64{}, Fingerprint: fingerprint, GenerationCalled: !reused, KnownGenerationUsage: !reused}}}
				for _, check := range c.Checks {
					a.Checks = append(a.Checks, evaluation.CheckResult{ID: check.ID, Kind: check.Kind, Passed: true})
				}
				if reused {
					a.Result.Measurement.Outcome = "reused"
				} else {
					a.Result.ThisRunUsage = usage
					a.Result.Measurement.GenerationUsage = &usage
				}
				if phase == "paraphrase" {
					a.Result.ReuseKind = "semantic"
				}
				task := outcomes.Task{ID: "semantic-source-" + c.ID, Revision: set.Revision + "-" + c.ID}
				if phase == "baseline" {
					task.ID = fmt.Sprintf("semantic-baseline-%s-%d", c.ID, i)
				}
				if phase == "baseline_repeat" {
					task.ID = fmt.Sprintf("semantic-baseline-%s-%d", c.ID, i+2)
				}
				result := &delegation.Result{Status: "completed", Attempts: []delegation.Attempt{a}, OutcomeTracking: &delegation.OutcomeTracking{Status: "recorded", RunID: id, Task: task, Receipt: &outcomes.Receipt{ID: id, Task: task, Version: 1}}}
				if phase == "seed" {
					result.SemanticReuse = &delegation.ReuseObservation{Status: "miss_or_rejected", EmbeddingInputTokens: &two}
				}
				if phase == "exact" {
					result.SemanticReuse = &delegation.ReuseObservation{Status: "skipped_exact"}
				}
				if phase == "paraphrase" {
					support := semantic.VerificationWork{Stage: "source", GenerationCalled: i == 0, Reused: i == 1, Complete: true, Durable: true, ReceiptKey: "inference/v1/jobs/" + fixtureDigest(c.ID+"support"), InputTokens: &ten, OutputTokens: &one}
					if i == 1 {
						support.InputTokens = &zero
						support.OutputTokens = &zero
					}
					pair := semantic.VerificationWork{Stage: "equivalence", GenerationCalled: true, Complete: true, Durable: true, ReceiptKey: "inference/v1/jobs/" + fixtureDigest(id+"pair"), InputTokens: &ten, OutputTokens: &one}
					// Deliberately large aggregate: only Work records contribute usage.
					aggregate := int64(999999)
					result.SemanticReuse = &delegation.ReuseObservation{Status: "reused", EmbeddingMS: 3, EmbeddingInputTokens: &two, Verifications: []semantic.ValidityResult{{DurationMS: 10, Work: []semantic.VerificationWork{support, pair}, Verdict: "accept", SameIntent: true, SameFacts: true, SameConstraints: true, AnswerSupported: true, Complete: true, Durable: true, InputTokens: &aggregate, OutputTokens: &aggregate}}}
				}
				elapsed := float64(70)
				rows = append(rows, row{CaseID: c.ID, Phase: phase, Input: input, Result: result, ElapsedMS: &elapsed})
			}
		}
	}
	return rows
}
func TestReportCountsWholeAttemptAndCachedCertificates(t *testing.T) {
	report, err := summarizeMeasurement(strings.NewReader(fixtureMeasurement(t, fixtureRows(t), true)))
	if err != nil {
		t.Fatal(err)
	}
	p := report.Phases["paraphrase"]
	if !report.Eligibility["eligible"] || p.Verification.Calls != 18 || p.Verification.KnownInput != 180 || p.Verification.ZeroCurrent != 6 || p.EmbeddingTokens != 24 || report.Phases["baseline"].Generation.Calls != 12 || report.Phases["exact"].Generation.Calls != 0 {
		t.Fatalf("wrong accounting: %+v, %+v", report, p)
	}
	if report.WholeEligibility["eligible"] || report.WholeComparedRequests != 0 || p.AttemptMS != 600 || p.VerificationMS != 120 || p.EmbeddingMS != 36 {
		t.Fatal("wrong latency or unmeasured whole comparison", report)
	}
	if !report.LatencyEligibility["primary_eligible"] || p.EndToEndMS != 840 {
		t.Fatal("missing measured end-to-end time", report)
	}
}

func TestReportExactSemanticAliasDoesNotInventEmbeddingWork(t *testing.T) {
	rows := fixtureRows(t, true)
	for i := range rows {
		if rows[i].Phase == "exact" {
			rows[i].Result.Attempts[0].Result.ReuseKind = "semantic"
			rows[i].Result.SemanticReuse = &delegation.ReuseObservation{Status: "reused", GenerationAvoided: true, HistoricalUsageIsProxy: true}
		}
	}
	report, err := summarizeMeasurement(strings.NewReader(fixtureMeasurement(t, rows, true)))
	if err != nil || !report.WholeEligibility["eligible"] || report.Phases["exact"].EmbeddingWorkRecords != 0 || report.Phases["exact"].EmbeddingUnknown != 0 || report.Phases["exact"].ExactWork != 0 || report.Phases["paraphrase"].EmbeddingWorkRecords != 12 {
		t.Fatalf("exact alias status invented current retrieval work: %+v %v", report, err)
	}
	// A novel semantic hit with missing embedding evidence remains unknown;
	// the alias exception must not erase actual retrieval accounting.
	rows[1].Result.SemanticReuse.EmbeddingInputTokens = nil
	rows[1].Result.SemanticReuse.EmbeddingMS = 0
	report, err = summarizeMeasurement(strings.NewReader(fixtureMeasurement(t, rows, true)))
	if err != nil || report.Eligibility["eligible"] || report.Phases["paraphrase"].EmbeddingUnknown != 1 || report.Phases["paraphrase"].EmbeddingWorkRecords != 12 {
		t.Fatalf("novel semantic hit lost unknown embedding work: %+v %v", report, err)
	}
}

func TestReportEndToEndClockDoesNotInventOrAddComponents(t *testing.T) {
	rows := fixtureRows(t, true)
	for i := range rows {
		if rows[i].Phase == "seed" {
			elapsed := float64(150)
			rows[i].ElapsedMS = &elapsed
			rows[i].Result.SemanticReuse.EmbeddingMS = 80
		}
	}
	report, err := summarizeMeasurement(strings.NewReader(fixtureMeasurement(t, rows, true)))
	if err != nil {
		t.Fatal(err)
	}
	source := report.Phases["seed"]
	if source.EndToEndMS != 900 || source.AttemptMS != 300 || source.EmbeddingMS != 480 || !report.LatencyEligibility["whole_cohort_eligible"] {
		t.Fatal("post-finish embedding clock was combined with partial runner time", report)
	}
	for i := range rows {
		rows[i].ElapsedMS = nil
	}
	report, err = summarizeMeasurement(strings.NewReader(fixtureMeasurement(t, rows, true)))
	if err != nil || !report.WholeEligibility["eligible"] || report.LatencyEligibility["primary_eligible"] || report.LatencyEligibility["whole_cohort_eligible"] || report.Phases["seed"].EndToEndMS != 0 || report.Phases["seed"].UnknownEndToEndMS != 6 {
		t.Fatal("invented old end-to-end timing or lost token comparison", report, err)
	}
}
func TestFreshRepeatBaselinePlanAndWholeCohort(t *testing.T) {
	if run([]string{"--fresh-repeat-baseline"}, &bytes.Buffer{}) == nil {
		t.Fatal("missing baseline dependency accepted")
	}
	var set caseSet
	if err := json.Unmarshal(casesJSON, &set); err != nil {
		t.Fatal(err)
	}
	for _, c := range set.Cases {
		tasks := map[string]bool{}
		for step := 5; step < 9; step++ {
			phase, r := measurementStep(set.Revision, c, step, nil)
			want := "baseline"
			if step >= 7 {
				want = "baseline_repeat"
			}
			if phase != want || r.SemanticReuse != nil || tasks[r.Spec.Task] || r.Spec.Input != c.Paraphrases[(step-5)%2] || r.Spec.Revision != set.Revision+"-"+c.ID {
				t.Fatal("repeat baseline must be a distinct fresh task", step)
			}
			tasks[r.Spec.Task] = true
		}
	}
	rows := fixtureRows(t, true)
	report, err := summarizeMeasurement(strings.NewReader(fixtureMeasurement(t, rows, true)))
	if err != nil || len(rows) != 54 || !report.WholeEligibility["eligible"] || report.WholeComparedRequests != 24 || report.Phases["baseline_repeat"].Generation.Calls != 12 {
		t.Fatalf("whole cohort: %+v %v", report, err)
	}
	rows[7].Result.Attempts[0].Result.Reused = true
	if _, err := summarizeMeasurement(strings.NewReader(fixtureMeasurement(t, rows, true))); err == nil {
		t.Fatal("baseline repeat attributed as generated and reused")
	}
}
func TestReportRetainsUnknownFailedWithheldAndExactWork(t *testing.T) {
	rows := fixtureRows(t)
	rows[0].Error = "PRIVATE ERROR"
	rows[0].ModelOutput = "PRIVATE ANSWER"
	rows[0].Result.Status = "stopped"
	m := rows[0].Result.Attempts[0].Result.Measurement
	m.KnownGenerationUsage = false
	m.GenerationUsage = nil
	a := &rows[0].Result.Attempts[0]
	a.QualityPassed, a.Result.Durable, a.Result.Response.Complete = false, false, false
	a.Checks = evaluation.NotEvaluated([]evaluation.Check{{ID: a.Checks[0].ID, Kind: a.Checks[0].Kind}, {ID: a.Checks[1].ID, Kind: a.Checks[1].Kind}})
	a.Result.ThisRunUsage, a.Result.GenerationUsage = inference.Usage{}, inference.Usage{}
	for _, i := range []int{1, 2, 3, 4} {
		rows[i].Result = nil
		rows[i].Withheld = "source_failed_checks"
		rows[i].ElapsedMS = nil
	}
	report, err := summarizeMeasurement(strings.NewReader(fixtureMeasurement(t, rows[:5], false)))
	if err != nil || report.Phases["seed"].Generation.Unknown != 1 || report.Phases["paraphrase"].Withheld != 2 || report.Eligibility["eligible"] || report.FinalMarker {
		t.Fatalf("lost failed/unknown run: %+v %v", report, err)
	}
	out, _ := json.Marshal(report)
	if strings.Contains(string(out), "PRIVATE") || strings.Contains(string(out), rows[0].Input) {
		t.Fatal("private content leaked")
	}
	rows = fixtureRows(t)
	rows[0].Result.Status = "quality_failed"
	rows[0].Result.Attempts[0].QualityPassed = false
	rows[0].Result.Attempts[0].Checks[0].Passed = false
	for _, i := range []int{1, 2, 3, 4} {
		rows[i].Result = nil
		rows[i].Withheld = "source_failed_checks"
		rows[i].ElapsedMS = nil
	}
	report, err = summarizeMeasurement(strings.NewReader(fixtureMeasurement(t, rows, true)))
	if err != nil || report.Phases["seed"].Generation.KnownInput != 600 || report.Phases["seed"].QualityPassed != 5 || report.Phases["baseline"].QualityPassed != 12 || report.Eligibility["eligible"] {
		t.Fatalf("lost failed source or fresh baseline: %+v %v", report, err)
	}
	rows = fixtureRows(t)
	rows[3].Result.SemanticReuse.EmbeddingInputTokens = new(int64)
	report, err = summarizeMeasurement(strings.NewReader(fixtureMeasurement(t, rows, true)))
	if err != nil || report.Phases["exact"].ExactWork != 1 || report.Eligibility["eligible"] {
		t.Fatalf("exact work accepted: %+v %v", report, err)
	}
}
func TestReportRejectsSchemaAndAttribution(t *testing.T) {
	for _, mutate := range []func([]row) string{
		func(r []row) string {
			elapsed := float64(-1)
			r[0].ElapsedMS = &elapsed
			return fixtureMeasurement(t, r, false)
		},
		func(r []row) string { return fixtureMeasurement(t, append(r, r[0]), false) },
		func(r []row) string {
			r[2].Result.SemanticReuse.Verifications[0].Work[1].ReceiptKey = r[1].Result.SemanticReuse.Verifications[0].Work[1].ReceiptKey
			return fixtureMeasurement(t, r, false)
		},
		func(r []row) string {
			r[1].Result.SemanticReuse.Verifications[0].Work[0].Reused = true
			return fixtureMeasurement(t, r, false)
		},
		func(r []row) string {
			return strings.Replace(fixtureMeasurement(t, r, false), `"generation_called":true,`, "", 1)
		},
		func(r []row) string {
			return strings.Replace(fixtureMeasurement(t, r, false), `"fresh_baseline":true`, `"fresh_baseline":true,"fresh_baseline":true`, 1)
		},
		func(r []row) string {
			return strings.Replace(fixtureMeasurement(t, r, false), `"input_tokens":100`, `"input_tokens":"PRIVATE MALFORMED"`, 1)
		},
	} {
		report, err := summarizeMeasurement(strings.NewReader(mutate(fixtureRows(t))))
		if err == nil {
			t.Fatalf("accepted invalid report: %+v", report)
		}
		if strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal("decoder error leaked input")
		}
	}
}
func TestReportExplicitPrivateFileAndNoLiveOptions(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "results.jsonl")
	if err := os.WriteFile(path, []byte(fixtureMeasurement(t, fixtureRows(t), true)), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"--report", path}, &out); err != nil || !json.Valid(out.Bytes()) {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--report", path, "--live"}, {"--report", "relative.jsonl"}} {
		if run(args, &bytes.Buffer{}) == nil {
			t.Fatal("accepted invalid report dispatch")
		}
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if run([]string{"--report", path}, &bytes.Buffer{}) == nil {
		t.Fatal("accepted public input")
	}
}
