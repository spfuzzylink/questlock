package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSemanticEvaluationPairSamplingOptionalFlagsWithoutModelAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suite.json")
	if err := os.WriteFile(path, semanticEvalTestJSON(t, semanticEvalTestSuite()), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		flags    []string
		category string
		present  bool
	}{
		{"legacy absent", nil, "embedding_setup_failed", false},
		{"explicit zero", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-temperature", "0", "--verifier-pair-seed", "0"}, "embedding_setup_failed", true},
		{"sampling profile", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-think", "--verifier-pair-temperature", "1", "--verifier-pair-seed", "42"}, "embedding_setup_failed", true},
		{"standalone forbidden", []string{"--verifier-pair-seed", "0"}, "invalid_local_model_policy", false},
		{"temperature nan", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-temperature", "NaN"}, "invalid_local_model_policy", false},
		{"temperature infinity", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-temperature", "+Inf"}, "invalid_local_model_policy", false},
		{"temperature high", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-temperature", "2.1"}, "invalid_local_model_policy", false},
		{"seed high", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-seed", "2147483648"}, "invalid_local_model_policy", false},
		{"seed negative", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-seed", "-1"}, "invalid_local_model_policy", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--live", "--suite", path, "--ollama-url", "not-an-origin"}, tc.flags...)
			var out bytes.Buffer
			if semanticEvaluateCommand(args, &out, &bytes.Buffer{}) == nil {
				t.Fatal("invalid origin reached execution")
			}
			var report semanticEvalReport
			if json.Unmarshal(out.Bytes(), &report) != nil || len(report.Errors) != 1 || report.Errors[0] != tc.category || report.Usage.LogicalModelCalls != 0 {
				t.Fatalf("invalid offline flag result: %s", out.String())
			}
			if tc.present && (report.VerifierPairTemperature == nil || report.VerifierPairSeed == nil) {
				t.Fatal("explicit zero/value options lost")
			}
			if tc.name == "legacy absent" {
				var fields map[string]json.RawMessage
				json.Unmarshal(out.Bytes(), &fields)
				if fields["verifier_pair_temperature"] != nil || fields["verifier_pair_seed"] != nil {
					t.Fatal("nil report changed legacy wire")
				}
			}
		})
	}
}
