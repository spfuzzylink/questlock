package main

import (
	"testing"

	"github.com/spfuzzylink/questlock/semantic"
)

func TestSemanticEvaluationLanguageGuardPreventsFactorizedVerificationOnly(t *testing.T) {
	for _, policy := range []string{"", "factorized-v1"} {
		suite := semanticEvalTestSuite()
		suite.Cases[0].OriginalQuestion = "Give a summary in english."
		suite.Cases[0].Variants = suite.Cases[0].Variants[:1]
		suite.Cases[0].Variants[0].Question = "Give a summary in spanish."
		report := semanticEvalReport{Pairs: []semanticEvalPair{{Status: "not_run", Abstained: true}}}
		embedder, verifier := &semanticEvalFakeEmbedder{}, &semanticEvalFakeVerifier{}
		config := semantic.ReuseConfig{VerifierPolicy: policy, VerifierMaxInputTokens: 100, VerifierMaxOutputTokens: 100}
		semanticEvalRun(t.Context(), suite, embedder, verifier, config, .9, &report)
		if policy == "factorized-v1" {
			if verifier.calls != 0 || report.Pairs[0].Status != "guard_rejected" || report.Pairs[0].VerificationAttempted || report.Pairs[0].Served || len(report.Pairs[0].GuardCodes) != 1 || report.Pairs[0].GuardCodes[0] != "output_language_changed" {
				t.Fatalf("explicit changed language reached a permissive verifier: %+v", report.Pairs[0])
			}
		} else if verifier.calls != 1 || !report.Pairs[0].Served {
			t.Fatalf("factorized language guard changed the published diagnostic path: %+v", report.Pairs[0])
		}
	}
}
