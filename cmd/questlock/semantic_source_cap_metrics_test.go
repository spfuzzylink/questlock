package main

import (
	"bytes"
	"encoding/json"
	"github.com/spfuzzylink/questlock/semantic"
	"os"
	"path/filepath"
	"testing"
)

func TestSourceInputFlagReportsAndRejectsBeforeProviderAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suite.json")
	if e := os.WriteFile(path, semanticEvalTestJSON(t, semanticEvalTestSuite()), 0600); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		extra    []string
		cap      int64
		category string
	}{{nil, 0, "embedding_setup_failed"}, {[]string{"--verifier-source-max-input-tokens", "5376", "--verifier-policy", "factorized-v1", "--verifier-max-input-tokens", "13312"}, 5376, "embedding_setup_failed"}, {[]string{"--verifier-source-max-input-tokens", "1"}, 1, "invalid_local_model_policy"}, {[]string{"--verifier-source-max-input-tokens", "8193", "--verifier-policy", "factorized-v1"}, 8193, "invalid_local_model_policy"}} {
		args := append([]string{"--live", "--suite", path, "--ollama-url", "invalid-origin"}, tc.extra...)
		var out bytes.Buffer
		if e := semanticEvaluateCommand(args, &out, &bytes.Buffer{}); e == nil {
			t.Fatal("invalid origin completed")
		}
		var r semanticEvalReport
		if e := json.Unmarshal(out.Bytes(), &r); e != nil {
			t.Fatal(e)
		}
		if r.VerifierSourceMaxInputTokens != tc.cap || len(r.Errors) != 1 || r.Errors[0] != tc.category || r.Usage.LogicalModelCalls != 0 {
			t.Fatalf("wrong source option report: %+v", r)
		}
		if tc.cap == 0 && bytes.Contains(out.Bytes(), []byte("verifier_source_max_input_tokens")) {
			t.Fatal("default header gained field")
		}
	}
}
func TestUnservedExecutionErrorsAndMissingRowsEarnNoSemanticCredit(t *testing.T) {
	rows := []semanticEvalPair{{ExpectedReuse: false, Status: "error", ErrorCategory: "verification_failed", Verification: &semantic.ValidityResult{Verdict: "unknown", GenerationCalled: true}}}
	for i := 0; i < 8; i++ {
		rows = append(rows, semanticEvalPair{ExpectedReuse: i < 6, Status: "not_run", Abstained: true})
	}
	totals, _ := semanticEvalSummarize(rows, nil)
	if totals.Total != 9 || totals.PositiveLabels != 6 || totals.NegativeLabels != 3 || totals.Unscored != 9 || totals.UnscoredPositiveLabels != 6 || totals.UnscoredNegativeLabels != 3 || totals.Completed != 0 || totals.Unattempted != 8 || totals.Errors != 1 || totals.UnknownVerdicts != 1 || totals.TP+totals.FP+totals.TN+totals.FN != 0 || totals.ConfusionPairs == nil || *totals.ConfusionPairs != 0 || totals.Accuracy != nil || totals.Recall != nil || totals.Precision != nil {
		t.Fatalf("non-decisions became semantic failures/success: %+v", totals)
	}
}
func TestAnyServedResultRemainsScoredEvenWhenExecutionFailed(t *testing.T) {
	rows := []semanticEvalPair{{ExpectedReuse: false, Served: true, Status: "error", ErrorCategory: "verification_failed"}, {ExpectedReuse: true, Served: true, Status: "not_run", ErrorCategory: "unknown_execution"}, {ExpectedReuse: true, Status: "verifier_rejected"}, {ExpectedReuse: false, Status: "guard_rejected"}, {ExpectedReuse: true, Status: "retrieval_miss"}}
	totals, _ := semanticEvalSummarize(rows, nil)
	if totals.Total != 5 || totals.Unscored != 0 || totals.FP != 1 || totals.TP != 1 || totals.FN != 2 || totals.TN != 1 || totals.Errors != 2 || totals.Accuracy == nil || *totals.Accuracy != .4 {
		t.Fatalf("unsafe served answer was hidden or known decisions lost: %+v", totals)
	}
}
