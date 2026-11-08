package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSemanticEvaluationPairThinkingFlagsAndScopedWallDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suite.json")
	if err := os.WriteFile(path, semanticEvalTestJSON(t, semanticEvalTestSuite()), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		extra    []string
		pair     bool
		output   int64
		deadline int
		category string
	}{
		{"old defaults", nil, false, 128, 30000, "embedding_setup_failed"},
		{"pair defaults", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-think"}, true, 4096, 120000, "embedding_setup_failed"},
		{"explicit shorter", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-think", "--verifier-max-output-tokens", "128", "--verifier-timeout-ms", "750"}, true, 128, 750, "embedding_setup_failed"},
		{"pair two hours", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-think", "--timeout", "2h"}, true, 4096, 120000, "embedding_setup_failed"},
		{"pair too long", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-think", "--timeout", "2h1ns"}, true, 0, 0, "explicit_live_suite_and_bounded_flags_required"},
		{"pair zero", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-think", "--timeout", "0"}, true, 0, 0, "explicit_live_suite_and_bounded_flags_required"},
		{"ordinary still one hour", []string{"--timeout", "1h1ns"}, false, 0, 0, "explicit_live_suite_and_bounded_flags_required"},
		{"explicit false still one hour", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-think=false", "--timeout", "2h"}, false, 0, 0, "explicit_live_suite_and_bounded_flags_required"},
		{"source still one hour", []string{"--verifier-policy", "factorized-v1", "--verifier-source-think", "--timeout", "1h1ns"}, false, 0, 0, "explicit_live_suite_and_bounded_flags_required"},
		{"pair legacy rejected", []string{"--verifier-pair-think"}, true, 0, 0, "explicit_live_suite_and_bounded_flags_required"},
		{"pair output bounded", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-think", "--verifier-max-output-tokens", "4097"}, true, 0, 0, "invalid_local_model_policy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--live", "--suite", path, "--ollama-url", "not-an-origin"}, tc.extra...)
			var out bytes.Buffer
			if semanticEvaluateCommand(args, &out, &bytes.Buffer{}) == nil {
				t.Fatal("invalidorigin reachedexecution")
			}
			var got semanticEvalReport
			if json.Unmarshal(out.Bytes(), &got) != nil || got.VerifierPairThink != tc.pair || got.VerifierMaxOutputTokens != tc.output || got.VerifierTimeoutMS != tc.deadline || len(got.Errors) != 1 || got.Errors[0] != tc.category || got.Usage.LogicalModelCalls != 0 || got.PaidFallbackEnabled || got.ProviderAPIChargeUSD != 0 {
				t.Fatalf("pair thinking report/config lost: %+v", got)
			}
			var fields map[string]json.RawMessage
			json.Unmarshal(out.Bytes(), &fields)
			if _, present := fields["verifier_pair_think"]; present != tc.pair {
				t.Fatal("falseoptionalreportfield changedoldwire")
			}
		})
	}
}
