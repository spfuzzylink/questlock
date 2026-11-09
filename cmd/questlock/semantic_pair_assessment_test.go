package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSemanticEvaluationAssessmentFlagsAreBoundedBeforeModelAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suite.json")
	if err := os.WriteFile(path, semanticEvalTestJSON(t, semanticEvalTestSuite()), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		extra    []string
		want     int64
		category string
		enabled  bool
	}{
		{"default", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-assessment"}, 512, "embedding_setup_failed", true},
		{"explicit cap", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-assessment", "--verifier-max-output-tokens", "64"}, 64, "embedding_setup_failed", true},
		{"false", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-assessment=false"}, 128, "embedding_setup_failed", false},
		{"legacy rejected", []string{"--verifier-pair-assessment"}, 0, "explicit_live_suite_and_bounded_flags_required", true},
		{"duplicate reasoning", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-assessment", "--verifier-pair-think"}, 0, "invalid_local_model_policy", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			args := append([]string{"--live", "--suite", path, "--ollama-url", "not-an-origin"}, tc.extra...)
			if semanticEvaluateCommand(args, &out, &bytes.Buffer{}) == nil {
				t.Fatal("invalid origin executed")
			}
			var got semanticEvalReport
			if json.Unmarshal(out.Bytes(), &got) != nil || got.VerifierPairAssessment != tc.enabled || got.VerifierMaxOutputTokens != tc.want || len(got.Errors) != 1 || got.Errors[0] != tc.category || got.Usage.LogicalModelCalls != 0 || got.PaidFallbackEnabled || got.ProviderAPIChargeUSD != 0 {
				t.Fatalf("assessment CLI metadata lost: %+v", got)
			}
			var fields map[string]json.RawMessage
			json.Unmarshal(out.Bytes(), &fields)
			if _, present := fields["verifier_pair_assessment"]; present != tc.enabled {
				t.Fatal("false changed legacy report wire")
			}
		})
	}
}
