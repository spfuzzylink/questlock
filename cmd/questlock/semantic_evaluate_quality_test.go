package main

import (
	"testing"

	"github.com/spfuzzylink/questlock/semantic"
)

// Identical retrieval scores can accompany both valid and invalid answers.
// Quality must come from the declared labels and actual serving decisions.
func TestSemanticQualityUsesLabelsInsteadOfRetrievalSimilarity(t *testing.T) {
	similarity := 1.0
	pairs := []semanticEvalPair{
		{ExpectedReuse: true, Served: true, Status: "served", Similarity: &similarity},
		{ExpectedReuse: false, Served: true, Status: "served", Similarity: &similarity},
		{ExpectedReuse: true, Status: "verifier_rejected", Similarity: &similarity},
		{ExpectedReuse: false, Status: "verifier_rejected", Similarity: &similarity},
	}
	totals, _ := semanticEvalSummarize(pairs, nil)
	if totals.TP != 1 || totals.FP != 1 || totals.FN != 1 || totals.TN != 1 || totals.Unscored != 0 {
		t.Fatalf("retrieval scores obscured labelled outcomes: %+v", totals)
	}
	if totals.Precision == nil || *totals.Precision != .5 || totals.Recall == nil || *totals.Recall != .5 || totals.Accuracy == nil || *totals.Accuracy != .5 {
		t.Fatalf("perfect retrieval similarity became answer quality: %+v", totals)
	}
}

func TestSemanticQualityOutputContractMismatchStopsVerification(t *testing.T) {
	for _, tc := range []struct {
		name, question, answer, guard string
	}{
		{"json", "Return only JSON.", "The status is pending.", "output_json_contract_mismatch"},
		{"numeric", "Return only an integer.", "seven", "output_numeric_contract_mismatch"},
		{"prose", "Respond in prose.", `{"status":"pending"}`, "output_prose_contract_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			suite := semanticEvalTestSuite()
			c := &suite.Cases[0]
			c.OriginalQuestion, c.Answer = tc.question, tc.answer
			c.Variants = c.Variants[:1]
			c.Variants[0].Question = tc.question
			report := semanticEvalReport{Pairs: []semanticEvalPair{{Status: "not_run", Abstained: true}}}
			embedder, verifier := &semanticEvalFakeEmbedder{}, &semanticEvalFakeVerifier{}
			config := semantic.ReuseConfig{VerifierPolicy: "factorized-v1", VerifierMaxInputTokens: 100, VerifierMaxOutputTokens: 100}
			semanticEvalRun(t.Context(), suite, embedder, verifier, config, .9, &report)
			row := report.Pairs[0]
			if embedder.calls != 2 || row.Similarity == nil || *row.Similarity != 1 || !row.Retrieved || row.Status != "guard_rejected" || row.Served || row.VerificationAttempted || verifier.calls != 0 {
				t.Fatalf("high similarity bypassed output contract: %+v", row)
			}
			if len(row.GuardCodes) != 1 || row.GuardCodes[0] != tc.guard {
				t.Fatalf("wrong output-contract evidence: %v", row.GuardCodes)
			}
		})
	}
}

func TestSemanticQualityUnknownVerdictNeverServesHighSimilarity(t *testing.T) {
	suite := semanticEvalTestSuite()
	suite.Cases[0].Variants = suite.Cases[0].Variants[:1]
	in, out := int64(5), int64(2)
	verifier := semanticAdmissionVerifier{result: semantic.ValidityResult{
		GenerationCalled: true, Complete: true, Durable: true, Verdict: "unknown",
		InputTokens: &in, OutputTokens: &out, ReceiptKey: "fixture-receipt",
	}}
	report := semanticEvalReport{Pairs: []semanticEvalPair{{ExpectedReuse: true, Status: "not_run", Abstained: true}}}
	semanticEvalRun(t.Context(), suite, &semanticEvalFakeEmbedder{}, verifier, semantic.ReuseConfig{VerifierMaxInputTokens: 100, VerifierMaxOutputTokens: 100}, .9, &report)
	row := report.Pairs[0]
	if row.Similarity == nil || *row.Similarity != 1 || row.Served || !row.Abstained || row.Status != "abstained" || row.Verdict != "unknown" {
		t.Fatalf("unknown verifier acquired invented confidence: %+v", row)
	}
	totals, usage := semanticEvalSummarize(report.Pairs, report.SourceEmbeddings)
	if totals.TP != 0 || totals.FN != 1 || totals.UnknownVerdicts != 1 || totals.Abstentions != 1 || totals.Precision != nil || totals.Recall == nil || *totals.Recall != 0 || usage.LogicalModelCalls != 3 || usage.VerifierInputTokens != in || usage.VerifierOutputTokens != out {
		t.Fatalf("unknown verdict hid withholding or real work: %+v %+v", totals, usage)
	}
}

func TestSemanticQualityDeferralLabelCannotHideMissingProofOrServedError(t *testing.T) {
	proof := semanticDeferredFixture(false)
	for _, tc := range []struct {
		name         string
		verification *semantic.ValidityResult
		served       bool
	}{
		{"missing proof", nil, false},
		{"served despite deferral", &proof, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := semanticEvalPair{Status: "resource_deferred", ErrorCategory: "resource_deferred", Verification: tc.verification, Served: tc.served}
			if semanticEvalResourceDeferred(row) {
				t.Fatal("unsupported resource label qualified as pre-claim deferral")
			}
			totals, _ := semanticEvalSummarize([]semanticEvalPair{row}, nil)
			if totals.Total != 1 || totals.ResourceDeferred != 0 || totals.Errors != 1 || totals.TN != 0 {
				t.Fatalf("deferral label hid an operational error: %+v", totals)
			}
			if tc.served {
				if totals.FP != 1 || totals.Unscored != 0 || totals.Precision == nil || *totals.Precision != 0 {
					t.Fatalf("served false acceptance disappeared: %+v", totals)
				}
			} else if totals.FP != 0 || totals.Unscored != 1 || totals.UnscoredNegativeLabels != 1 {
				t.Fatalf("missing proof became scored answer quality: %+v", totals)
			}
		})
	}
}
