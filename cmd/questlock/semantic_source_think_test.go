package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSemanticEvaluationSourceThinkingFlagsAndDeclaredBoundsBeforeNetwork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suite.json")
	if err := os.WriteFile(path, semanticEvalTestJSON(t, semanticEvalTestSuite()), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name          string
		extra         []string
		think         bool
		input, output int64
		timeout       int
		category      string
	}{
		{"legacy unchanged", nil, false, 8192, 128, 30000, "embedding_setup_failed"},
		{"factorized unchanged", []string{"--verifier-policy", "factorized-v1"}, false, 16384, 128, 30000, "embedding_setup_failed"},
		{"explicit source reasoning defaults", []string{"--verifier-policy", "factorized-v1", "--verifier-source-think"}, true, 16384, 4096, 120000, "embedding_setup_failed"},
		{"explicit source reasoning limits", []string{"--verifier-policy", "factorized-v1", "--verifier-source-think", "--verifier-max-output-tokens", "2048", "--verifier-timeout-ms", "120000"}, true, 16384, 2048, 120000, "embedding_setup_failed"},
		{"legacy source reasoning rejected", []string{"--verifier-source-think"}, true, 0, 0, 0, "explicit_live_suite_and_bounded_flags_required"},
		{"no-think output ceiling retained", []string{"--verifier-policy", "factorized-v1", "--verifier-max-output-tokens", "4096"}, false, 0, 0, 0, "invalid_local_model_policy"},
		{"reasoning output bounded", []string{"--verifier-policy", "factorized-v1", "--verifier-source-think", "--verifier-max-output-tokens", "4097"}, true, 0, 0, 0, "invalid_local_model_policy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A syntactically invalid origin is rejected before discovery, DNS,
			// broker creation or inference. This tests configuration/reporting,
			// not model quality or live provider compatibility.
			args := []string{"--live", "--suite", path, "--ollama-url", "not-an-origin"}
			args = append(args, tc.extra...)
			var out bytes.Buffer
			if err := semanticEvaluateCommand(args, &out, &bytes.Buffer{}); err == nil {
				t.Fatal("invalid origin unexpectedly reached a completed evaluation")
			}
			var report semanticEvalReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if report.VerifierSourceThink != tc.think || report.VerifierMaxInputTokens != tc.input || report.VerifierMaxOutputTokens != tc.output || report.VerifierTimeoutMS != tc.timeout || len(report.Errors) != 1 || report.Errors[0] != tc.category || report.Usage.LogicalModelCalls != 0 || report.PaidFallbackEnabled || report.ProviderAPIChargeUSD != 0 {
				t.Fatalf("lost safe config/reporting contract: %+v", report)
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(out.Bytes(), &fields) != nil || fields["verifier_source_think"] == nil {
				t.Fatal("report must declare whether source thinking was requested")
			}
		})
	}
}
