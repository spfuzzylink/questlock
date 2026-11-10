package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spfuzzylink/questlock/semantic"
)

type semanticEvalStopVerifier struct {
	calls int
	fault string
}

func (v *semanticEvalStopVerifier) Identity() string { return "offline-stop-fixture" }
func (v *semanticEvalStopVerifier) Verify(context.Context, semantic.ValidityRequest) (semantic.ValidityResult, error) {
	v.calls++
	in, out := int64(5), int64(2)
	r := semantic.ValidityResult{GenerationCalled: true, Complete: true, Durable: true, Verdict: "accept", SameIntent: true, SameFacts: true, SameConstraints: true, AnswerSupported: true, InputTokens: &in, OutputTokens: &out, ReceiptKey: "retained-current-receipt"}
	if v.calls != 1 {
		return r, nil
	}
	switch v.fault {
	case "error":
		return r, errors.New("native execution failed after retaining known usage")
	case "incomplete":
		r.Complete = false
	case "unknown usage":
		r.OutputTokens = nil
	case "unconfirmed":
		r.Durable = false
	case "reject", "unknown":
		r.Verdict = v.fault
		r.SameIntent = false
		r.SameFacts = false
		r.SameConstraints = false
	}
	return r, nil
}

func semanticEvalStopFixture() (semanticEvalSuite, semanticEvalReport) {
	suite := semanticEvalTestSuite()
	suite.Cases = append(suite.Cases, suite.Cases[0])
	suite.Cases[1].ID = "second-source"
	report := semanticEvalReport{}
	for i := range suite.Cases {
		// Clone variants: the second source must not alias the first source's slice.
		suite.Cases[i].Variants = append([]semanticEvalVariant(nil), suite.Cases[i].Variants...)
		for j := range suite.Cases[i].Variants {
			v := &suite.Cases[i].Variants[j]
			v.Question = suite.Cases[i].OriginalQuestion
			report.Pairs = append(report.Pairs, semanticEvalPair{CaseID: suite.Cases[i].ID, ID: v.ID, ExpectedReuse: *v.ShouldReuse, Status: "not_run", Verdict: "not_requested", Abstained: true})
		}
	}
	return suite, report
}

func TestSemanticEvaluationStopOnExecutionErrorBoundsSchedulingAndKeepsCosts(t *testing.T) {
	for _, fault := range []string{"error", "incomplete", "unknown usage", "unconfirmed"} {
		for _, enabled := range []bool{false, true} {
			t.Run(fault+map[bool]string{false: " default continues", true: " explicit stop"}[enabled], func(t *testing.T) {
				suite, report := semanticEvalStopFixture()
				report.StopOnExecutionError = enabled
				embedder, verifier := &semanticEvalFakeEmbedder{}, &semanticEvalStopVerifier{fault: fault}
				semanticEvalRun(t.Context(), suite, embedder, verifier, semantic.ReuseConfig{VerifierMaxInputTokens: 100, VerifierMaxOutputTokens: 100}, .9, &report)
				if report.Pairs[0].ErrorCategory == "" || report.Pairs[0].Verification == nil || report.Pairs[0].Verification.ReceiptKey != "retained-current-receipt" {
					t.Fatal("lost current failed execution evidence", report.Pairs[0])
				}
				if enabled {
					if verifier.calls != 1 || embedder.calls != 2 || len(report.SourceEmbeddings) != 1 {
						t.Fatalf("scheduled after first fault: verifier=%d embeds=%d sources=%d", verifier.calls, embedder.calls, len(report.SourceEmbeddings))
					}
					for _, row := range report.Pairs[1:] {
						if row.Status != "not_run" || row.VerificationAttempted || row.EmbeddingAttempted || row.Served || row.Verification != nil {
							t.Fatal("remaining row was attempted/scored", row)
						}
					}
					totals, usage := semanticEvalSummarize(report.Pairs, report.SourceEmbeddings)
					if totals.Unscored != 4 || totals.Unattempted != 3 || totals.TP != 0 || totals.TN != 0 || totals.FP != 0 || totals.FN != 0 || usage.VerifierGenerationCalls != 1 {
						t.Fatalf("fault denominator/cost lost: %+v %+v", totals, usage)
					}
					if fault == "unknown usage" {
						if usage.VerifierUnknownUsageCalls != 1 || report.Pairs[0].Verification.InputTokens == nil || *report.Pairs[0].Verification.InputTokens != 5 {
							t.Fatal("missing usage became known")
						}
					} else if usage.VerifierInputTokens != 5 || usage.VerifierOutputTokens != 2 {
						t.Fatal("recoverable known output lost")
					}
				} else if verifier.calls != 4 || embedder.calls != 6 || len(report.SourceEmbeddings) != 2 {
					t.Fatal("default scheduling changed")
				}
			})
		}
	}
}

func TestSemanticEvaluationStopKeepsKnownRejectAndUnknownScheduling(t *testing.T) {
	for _, verdict := range []string{"reject", "unknown"} {
		suite, report := semanticEvalStopFixture()
		report.StopOnExecutionError = true
		embedder, verifier := &semanticEvalFakeEmbedder{}, &semanticEvalStopVerifier{fault: verdict}
		semanticEvalRun(t.Context(), suite, embedder, verifier, semantic.ReuseConfig{VerifierMaxInputTokens: 100, VerifierMaxOutputTokens: 100}, .9, &report)
		if verifier.calls != 4 || embedder.calls != 6 || report.Pairs[0].ErrorCategory != "" || report.Pairs[0].Verdict != verdict {
			t.Fatal("known semantic outcome stopped scheduling", verdict, report)
		}
	}
}

type semanticEvalStopEmbedder struct{ calls int }

func (e *semanticEvalStopEmbedder) Embed(context.Context, string) (semantic.Embedding, error) {
	e.calls++
	tokens := int64(3)
	return semantic.Embedding{ModelID: "offline-fixture", Vector: []float32{1, 0}, InputTokens: &tokens}, errors.New("retained source embedding fault")
}
func TestSemanticEvaluationStopSourceEmbeddingErrorBeforeVariantWork(t *testing.T) {
	suite, report := semanticEvalStopFixture()
	report.StopOnExecutionError = true
	embedder, verifier := &semanticEvalStopEmbedder{}, &semanticEvalStopVerifier{}
	semanticEvalRun(t.Context(), suite, embedder, verifier, semantic.ReuseConfig{VerifierMaxInputTokens: 100, VerifierMaxOutputTokens: 100}, .9, &report)
	if embedder.calls != 1 || verifier.calls != 0 || len(report.SourceEmbeddings) != 1 || report.SourceEmbeddings[0].ErrorCategory != "source_embedding_failed" {
		t.Fatal("source fault allowed more work", report)
	}
	for _, row := range report.Pairs {
		if row.Status != "not_run" || row.EmbeddingAttempted || row.VerificationAttempted {
			t.Fatal("source fault attempted variant", row)
		}
	}
	_, usage := semanticEvalSummarize(report.Pairs, report.SourceEmbeddings)
	if usage.EmbeddingInputTokens != 3 || usage.EmbeddingCalls != 1 {
		t.Fatal("source embedding cost lost", usage)
	}
}

func TestSemanticEvaluationStopBatchRetainsStartedRowsAndSkipsNextBatch(t *testing.T) {
	suite, report := semanticEvalStopFixture()
	report.StopOnExecutionError = true
	report.BatchSize = 2
	// Four compatible variants in one source allow a second native batch.
	suite.Cases[0].Variants = append(suite.Cases[0].Variants, suite.Cases[1].Variants...)
	suite.Cases = suite.Cases[:1]
	verifier := &semanticEvalFakeBatchVerifier{unknown: true}
	semanticEvalRun(t.Context(), suite, &semanticEvalFakeEmbedder{}, verifier, semantic.ReuseConfig{VerifierMaxInputTokens: 100, VerifierMaxOutputTokens: 100}, .9, &report)
	if len(verifier.batches) != 1 || len(report.BatchVerificationWork) != 1 {
		t.Fatal("extra batch scheduled or current parent receipt lost")
	}
	for i, row := range report.Pairs {
		if row.VerificationAttempted != (i < 2) {
			t.Fatal("batch attempt attribution lost", i, row)
		}
	}
}

func TestSemanticEvaluationStopFlagIsExplicitDiagnosticOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suite.json")
	if err := os.WriteFile(path, semanticEvalTestJSON(t, semanticEvalTestSuite()), 0600); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		args := []string{"--live", "--suite", path, "--ollama-url", "not-an-origin"}
		if enabled {
			args = append(args, "--stop-on-execution-error")
		}
		var out bytes.Buffer
		if semanticEvaluateCommand(args, &out, &bytes.Buffer{}) == nil {
			t.Fatal("invalid origin reached execution")
		}
		var report semanticEvalReport
		var fields map[string]json.RawMessage
		if json.Unmarshal(out.Bytes(), &report) != nil || json.Unmarshal(out.Bytes(), &fields) != nil || report.StopOnExecutionError != enabled || report.Usage.LogicalModelCalls != 0 {
			t.Fatal("diagnostic flag/report lost", out.String())
		}
		if _, present := fields["stop_on_execution_error"]; present != enabled {
			t.Fatal("false optional field changed old report wire")
		}
	}
}
