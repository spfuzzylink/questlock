package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/semantic"
)

func TestSemanticResourcePolicyLoadsStrictlyBeforeProviderAccess(t *testing.T) {
	dir := t.TempDir()
	suitePath := filepath.Join(dir, "suite.json")
	if err := os.WriteFile(suitePath, semanticEvalTestJSON(t, semanticEvalTestSuite()), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, raw, policy, category string
		declared                    bool
	}{
		{"valid", "{\"max_wait_ms\":25,\"max_concurrent\":2}", "factorized-v1", "embedding_setup_failed", true},
		{"unknown field", "{\"magic\":1}", "factorized-v1", "invalid_resource_policy", false},
		{"duplicate", "{\"max_concurrent\":1,\"max_concurrent\":2}", "factorized-v1", "invalid_resource_policy", false},
		{"null", "{\"max_wait_ms\":null}", "factorized-v1", "invalid_resource_policy", false},
		{"out of bounds", "{\"max_wait_ms\":-1}", "factorized-v1", "invalid_resource_policy", false},
		{"trailing", "{}{}", "factorized-v1", "invalid_resource_policy", false},
		{"legacy", "{}", "", "explicit_live_suite_and_bounded_flags_required", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policyPath := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".json")
			if err := os.WriteFile(policyPath, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			statePath := filepath.Join(semanticPrivateTestParent(t), "private-state")
			args := []string{"--live", "--suite", suitePath, "--resource-policy", policyPath, "--ollama-url", "invalid-origin", "--state-dir", statePath}
			if tc.policy != "" {
				args = append(args, "--verifier-policy", tc.policy)
			}
			var out, diagnostics bytes.Buffer
			if err := semanticEvaluateCommand(args, &out, &diagnostics); err == nil {
				t.Fatal("invalid origin unexpectedly completed")
			}
			var report semanticEvalReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if len(report.Errors) != 1 || report.Errors[0] != tc.category || (report.ResourcePolicy != nil) != tc.declared || report.Usage.LogicalModelCalls != 0 {
				t.Fatalf("wrong strict policy behavior: %+v", report)
			}
			if tc.declared && (report.ResourcePolicy.MaxWaitMS != 25 || report.ResourcePolicy.MaxConcurrent != 2 || report.ResourcePolicy.MinAvailableMemoryBytes != 256*1024*1024) {
				t.Fatal("normalized policy missing", report.ResourcePolicy)
			}
			if bytes.Contains(out.Bytes(), []byte(policyPath)) || bytes.Contains(out.Bytes(), []byte(statePath)) {
				t.Fatal("private policy/state path leaked to JSON")
			}
			if !tc.declared {
				if _, err := os.Lstat(statePath); !os.IsNotExist(err) {
					t.Fatal("invalid resource policy reached broker creation", err)
				}
			}
		})
	}
}

func TestSemanticCommandResourceDeferralFailsFullQualificationWithoutGeneration(t *testing.T) {
	// The model endpoint is an in-process fixture. An intentionally impossible
	// operator headroom requirement defers on every native probe outcome; no
	// installed model, provider API or machine-capacity override is involved.
	parent := semanticPrivateTestParent(t)
	endpoint, _, generations := semanticStateModelFixture(t, false)
	args := semanticStateCommandArgs(t, parent, endpoint)
	policyPath := filepath.Join(parent, "admission.json")
	policy, _ := json.Marshal(map[string]int64{"min_available_memory_bytes": math.MaxInt64})
	if err := os.WriteFile(policyPath, policy, 0600); err != nil {
		t.Fatal(err)
	}
	args = append(args, "--resource-policy", policyPath, "--state-dir", filepath.Join(parent, "retained-state"))
	var out, diagnostics bytes.Buffer
	if err := semanticEvaluateCommand(args, &out, &diagnostics); err == nil {
		t.Fatal("deferral qualified an incomplete population")
	}
	var report semanticEvalReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "partial_failed" || !report.StateRetained || report.ResourcePolicy == nil || len(report.Errors) != 1 || report.Errors[0] != "resource_admission_deferred" || generations.Load() != 0 || report.Usage.VerifierGenerationCalls != 0 || report.Usage.VerifierUnknownUsageCalls != 0 || report.Usage.DeferredVerifierStages != 1 || report.Totals.ResourceDeferred != 1 || report.Totals.Total != 1 || report.Totals.PositiveLabels != 1 || report.Totals.ConfusionPairs == nil || *report.Totals.ConfusionPairs != 0 || report.Totals.TP+report.Totals.FP+report.Totals.TN+report.Totals.FN != 0 || len(report.Pairs) != 1 || !semanticEvalResourceDeferred(report.Pairs[0]) {
		t.Fatalf("pre-claim deferral lost population, state, or zero-call proof: %+v", report)
	}
}

func semanticDeferredFixture(withSource bool) semantic.ValidityResult {
	ptr := func(n int64) *int64 { return &n }
	w := semantic.VerificationWork{Stage: "source", AdmissionDeferred: true, InputTokens: ptr(0), OutputTokens: ptr(0)}
	v := semantic.ValidityResult{Verdict: "unknown", InputTokens: ptr(0), OutputTokens: ptr(0), Work: []semantic.VerificationWork{w}}
	if withSource {
		w.Stage = "equivalence"
		v.Work = []semantic.VerificationWork{{Stage: "source", GenerationCalled: true, Complete: true, Durable: true, InputTokens: ptr(90), OutputTokens: ptr(20), ReceiptKey: "inference/v1/jobs/" + strings.Repeat("a", 64), OutputSHA256: strings.Repeat("b", 64)}, w}
		v.GenerationCalled, v.AnswerSupported, v.InputTokens, v.OutputTokens = true, true, ptr(90), ptr(20)
	}
	return v
}

func TestSemanticProvenDeferralsKeepPopulationButExcludeOnlyUnexecutedDecision(t *testing.T) {
	for _, withSource := range []bool{false, true} {
		v := semanticDeferredFixture(withSource)
		if !semanticEvalDeferredVerification(v) {
			t.Fatalf("valid stage evidence rejected: %+v", v)
		}
		rows := []semanticEvalPair{
			{ExpectedReuse: true, Status: "resource_deferred", ErrorCategory: "resource_deferred", Abstained: true, VerificationAttempted: true, Verification: &v},
			{ExpectedReuse: false, Status: "resource_deferred", ErrorCategory: "resource_deferred", Abstained: true, VerificationAttempted: true, Verification: &v},
			{ExpectedReuse: true, Status: "served", Served: true},
			{ExpectedReuse: false, Status: "verifier_rejected"},
		}
		totals, usage := semanticEvalSummarize(rows, nil)
		if totals.Total != 4 || totals.PositiveLabels != 2 || totals.NegativeLabels != 2 || totals.ResourceDeferred != 2 || totals.DeferredPositiveLabels != 1 || totals.DeferredNegativeLabels != 1 || totals.ConfusionPairs == nil || *totals.ConfusionPairs != 2 || totals.TP != 1 || totals.TN != 1 || totals.FP != 0 || totals.FN != 0 || totals.Errors != 0 || totals.Completed != 2 || totals.Recall == nil || *totals.Recall != 1 || totals.Accuracy == nil || *totals.Accuracy != 1 || usage.DeferredVerifierStages != 2 || usage.VerifierUnknownUsageCalls != 0 {
			t.Fatalf("capacity polluted quality or population: %+v %+v", totals, usage)
		}
		wantCalls, wantInput, wantOutput := 0, int64(0), int64(0)
		if withSource {
			wantCalls, wantInput, wantOutput = 2, 180, 40
		}
		if usage.VerifierGenerationCalls != wantCalls || usage.VerifierInputTokens != wantInput || usage.VerifierOutputTokens != wantOutput {
			t.Fatalf("prior source work hidden: %+v", usage)
		}
	}
	for name, mutate := range map[string]func(*semantic.ValidityResult){
		"executed": func(v *semantic.ValidityResult) {
			v.Work[0].GenerationCalled = true
			v.Work[0].InputTokens = nil
			v.GenerationCalled = true
			v.InputTokens = nil
		},
		"claimed": func(v *semantic.ValidityResult) {
			v.Work[0].ReceiptKey = "inference/v1/jobs/" + strings.Repeat("a", 64)
		},
		"unknown usage": func(v *semantic.ValidityResult) { v.Work[0].OutputTokens = nil },
		"completed":     func(v *semantic.ValidityResult) { v.Work[0].Complete = true },
		"durable":       func(v *semantic.ValidityResult) { v.Work[0].Durable = true },
		"wrong stage":   func(v *semantic.ValidityResult) { v.Work[0].Stage = "equivalence" },
	} {
		t.Run(name, func(t *testing.T) {
			v := semanticDeferredFixture(false)
			mutate(&v)
			if semanticEvalDeferredVerification(v) {
				t.Fatal("unproven deferral accepted")
			}
			row := semanticEvalPair{ExpectedReuse: true, Status: "resource_deferred", ErrorCategory: "resource_deferred", Verification: &v}
			totals, _ := semanticEvalSummarize([]semanticEvalPair{row}, nil)
			if totals.ResourceDeferred != 0 || totals.FN != 0 || totals.Unscored != 1 || totals.UnscoredPositiveLabels != 1 || totals.Errors != 1 {
				t.Fatal("invalid deferral disappeared", totals)
			}
		})
	}
}

type semanticAdmissionVerifier struct {
	result semantic.ValidityResult
	err    error
}

func (v semanticAdmissionVerifier) Identity() string { return "offline-admission-fixture" }
func (v semanticAdmissionVerifier) Verify(context.Context, semantic.ValidityRequest) (semantic.ValidityResult, error) {
	return v.result, v.err
}

func TestSemanticPipelineRequiresDeferralSentinelAndProof(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result semantic.ValidityResult
		err    error
		want   string
	}{
		{"source before claim", semanticDeferredFixture(false), inference.ErrAdmissionDeferred, "resource_deferred"},
		{"pair before claim", semanticDeferredFixture(true), inference.ErrAdmissionDeferred, "resource_deferred"},
		{"no sentinel", semanticDeferredFixture(false), errors.New("other failure"), "error"},
		{"sentinel alone", semantic.ValidityResult{GenerationCalled: true, Verdict: "unknown"}, inference.ErrAdmissionDeferred, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			suite := semanticEvalTestSuite()
			report := semanticEvalReport{}
			for _, variant := range suite.Cases[0].Variants {
				report.Pairs = append(report.Pairs, semanticEvalPair{ExpectedReuse: *variant.ShouldReuse, Status: "not_run", Abstained: true})
			}
			semanticEvalRun(t.Context(), suite, &semanticEvalFakeEmbedder{}, semanticAdmissionVerifier{tc.result, tc.err}, semantic.ReuseConfig{VerifierMaxInputTokens: 8192, VerifierMaxOutputTokens: 512}, .9, &report)
			for _, row := range report.Pairs {
				if row.Status != tc.want || row.Served {
					t.Fatalf("wrong resource/quality split: %+v", row)
				}
			}
		})
	}
}
