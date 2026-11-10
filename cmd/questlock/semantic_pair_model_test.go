package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/semantic"
)

func TestSemanticEvaluationPairOptionsRejectInvalidPoliciesBeforeDiscovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suite.json")
	if err := os.WriteFile(path, semanticEvalTestJSON(t, semanticEvalTestSuite()), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		extra    []string
		model    string
		cap      int64
		category string
	}{
		{"default", nil, "", 0, "embedding_setup_failed"},
		{"mixed", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-model", "other:local", "--verifier-pair-max-input-tokens", "6400"}, "other:local", 6400, "embedding_setup_failed"},
		{"cap only", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-max-input-tokens", "100"}, "", 100, "embedding_setup_failed"},
		{"legacy model", []string{"--verifier-pair-model", "other:local"}, "other:local", 0, "invalid_local_model_policy"},
		{"legacy cap", []string{"--verifier-pair-max-input-tokens", "100"}, "", 100, "invalid_local_model_policy"},
		{"too large", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-max-input-tokens", "8193"}, "", 8193, "invalid_local_model_policy"},
		{"negative", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-max-input-tokens", "-1"}, "", -1, "invalid_local_model_policy"},
		{"cloud", []string{"--verifier-policy", "factorized-v1", "--verifier-pair-model", "other:cloud"}, "other:cloud", 0, "invalid_local_model_policy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"--live", "--suite", path, "--ollama-url", "not-an-origin"}
			args = append(args, tc.extra...)
			var out bytes.Buffer
			if err := semanticEvaluateCommand(args, &out, &bytes.Buffer{}); err == nil {
				t.Fatal("invalid origin unexpectedly completed")
			}
			var report semanticEvalReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if report.VerifierPairModel != tc.model || report.VerifierPairMaxInputTokens != tc.cap || len(report.Errors) != 1 || report.Errors[0] != tc.category || report.Usage.LogicalModelCalls != 0 || report.PaidFallbackEnabled || report.ProviderAPIChargeUSD != 0 {
				t.Fatalf("unsafe option declaration: %+v", report)
			}
			if tc.name == "default" && bytes.Contains(out.Bytes(), []byte("verifier_pair_")) {
				t.Fatal("default serialization gained pair fields")
			}
		})
	}
}

func TestSemanticEvaluationMixedUsageKeepsModelBucketsAndUnknowns(t *testing.T) {
	ptr := func(n int64) *int64 { return &n }
	sourceRuntime, pairRuntime := strings.Repeat("a", 64), strings.Repeat("b", 64)
	rows := []semanticEvalPair{
		{VerificationAttempted: true, Verification: &semantic.ValidityResult{MixedModelUsage: true, Work: []semantic.VerificationWork{
			{Model: "source:local", RuntimeIdentity: sourceRuntime, GenerationCalled: true, InputTokens: ptr(110), OutputTokens: ptr(20)},
			{Model: "pair:local", RuntimeIdentity: pairRuntime, GenerationCalled: true, InputTokens: ptr(70), OutputTokens: ptr(15)},
		}}},
		{VerificationAttempted: true, Verification: &semantic.ValidityResult{MixedModelUsage: true, Work: []semantic.VerificationWork{
			{Model: "source:local", RuntimeIdentity: sourceRuntime, Reused: true, InputTokens: ptr(0), OutputTokens: ptr(0)},
			{Model: "pair:local", RuntimeIdentity: pairRuntime, GenerationCalled: true},
		}}},
	}
	_, usage := semanticEvalSummarize(rows, nil)
	if len(usage.VerifierModels) != 2 || !usage.MixedVerifierModelUsage || usage.VerifierTokenAccounting != "reservation_accounting_sum_of_separately_metered_model_units" || usage.VerifierGenerationCalls != 3 || usage.VerifierUnknownUsageCalls != 1 || usage.VerifierInputTokens != 180 || usage.VerifierOutputTokens != 35 {
		t.Fatalf("mixed accounting lost units or unknown work: %+v", usage)
	}
	source, pair := usage.VerifierModels[0], usage.VerifierModels[1]
	if source.Model != "source:local" || source.RuntimeIdentity != sourceRuntime || source.GenerationCalls != 1 || source.CachedVerifications != 1 || source.KnownUsageCalls != 2 || source.InputTokens != 110 || source.OutputTokens != 20 || pair.Model != "pair:local" || pair.RuntimeIdentity != pairRuntime || pair.GenerationCalls != 2 || pair.UnknownUsageCalls != 1 || pair.InputTokens != 70 || pair.OutputTokens != 15 {
		t.Fatalf("per model accounting mismatch: %+v", usage.VerifierModels)
	}
	_, ordinary := semanticEvalSummarize([]semanticEvalPair{{Verification: &semantic.ValidityResult{GenerationCalled: true, InputTokens: ptr(1), OutputTokens: ptr(2)}}}, nil)
	b, _ := json.Marshal(ordinary)
	if bytes.Contains(b, []byte("verifier_models")) || bytes.Contains(b, []byte("mixed_verifier")) || bytes.Contains(b, []byte("verifier_token_accounting")) {
		t.Fatal("ordinary report serialization changed", string(b))
	}
}
