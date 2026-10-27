package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/internal/validityeval"
	"github.com/spfuzzylink/questlock/semantic"
)

func semanticEvalTestSuite() semanticEvalSuite {
	yes, no := true, false
	return semanticEvalSuite{SchemaVersion: 1, Population: "Synthetic parser and metrics fixture only.", LabelRule: "Serve only a fully valid unchanged answer.", Cases: []semanticEvalCase{{ID: "handbook", Family: "handbook", Instructions: "Use approved evidence.", Evidence: "The handbook is in the portal.", Answer: "Find the handbook in the portal.", OriginalQuestion: "Where is the handbook?", Variants: []semanticEvalVariant{{ID: "positive", Question: "Where can I find the handbook?", ShouldReuse: &yes, Reason: "Same requested location."}, {ID: "negative", Question: "Explain how to edit the handbook.", ShouldReuse: &no, Reason: "Different operation."}}}}}
}

func semanticEvalTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSemanticEvaluationFrozenCorpusIdentityAndLabels(t *testing.T) {
	suite, raw, err := loadSemanticEvalSuite(filepath.Join("..", "..", "examples", "semantic", "controlled-evaluation.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(raw)
	if hex.EncodeToString(h[:]) != semanticEvalFrozenSHA256 {
		t.Fatal("frozen corpus changed; review before changing the recorded identity")
	}
	var positive, negative int
	for _, c := range suite.Cases {
		for _, v := range c.Variants {
			if *v.ShouldReuse {
				positive++
			} else {
				negative++
			}
		}
	}
	if len(suite.Cases) != 6 || positive != 36 || negative != 36 {
		t.Fatalf("unexpected corpus population: %d cases, %d positive, %d negative", len(suite.Cases), positive, negative)
	}
}

func TestSemanticEvaluationParserRejectsAmbiguousOrUnboundedInput(t *testing.T) {
	valid := semanticEvalTestJSON(t, semanticEvalTestSuite())
	if _, err := parseSemanticEvalSuite(valid); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{
		"duplicate":          bytes.Replace(valid, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1),
		"unknown":            bytes.Replace(valid, []byte(`"schema_version":1`), []byte(`"schema_version":1,"unknown":true`), 1),
		"case alias":         bytes.Replace(valid, []byte(`"schema_version":1`), []byte(`"Schema_Version":1`), 1),
		"trailing":           append(append([]byte{}, valid...), []byte(` {}`)...),
		"missing label":      bytes.Replace(valid, []byte(`"should_reuse":true,`), nil, 1),
		"null label":         bytes.Replace(valid, []byte(`"should_reuse":true`), []byte(`"should_reuse":null`), 1),
		"unpaired surrogate": bytes.Replace(valid, []byte(`"handbook"`), []byte(`"\ud800"`), 1),
		"oversize bytes":     bytes.Repeat([]byte(" "), semanticEvalMaxBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseSemanticEvalSuite(raw); err == nil {
				t.Fatal("accepted invalid suite")
			}
		})
	}
	mutations := map[string]func(*semanticEvalSuite){
		"schema":            func(s *semanticEvalSuite) { s.SchemaVersion = 2 },
		"no cases":          func(s *semanticEvalSuite) { s.Cases = nil },
		"duplicate case":    func(s *semanticEvalSuite) { s.Cases = append(s.Cases, s.Cases[0]) },
		"duplicate variant": func(s *semanticEvalSuite) { s.Cases[0].Variants = append(s.Cases[0].Variants, s.Cases[0].Variants[0]) },
		"oversize text":     func(s *semanticEvalSuite) { s.Cases[0].Evidence = strings.Repeat("a", semanticEvalMaxText+1) },
		"empty answer":      func(s *semanticEvalSuite) { s.Cases[0].Answer = " " },
		"control label":     func(s *semanticEvalSuite) { s.Cases[0].ID = "bad\u0001id" },
		"too many cases": func(s *semanticEvalSuite) {
			for i := 1; i < 25; i++ {
				c := s.Cases[0]
				c.ID = fmt.Sprintf("case-%d", i)
				c.Variants = []semanticEvalVariant{{ID: fmt.Sprintf("variant-%d", i), Question: "Where is the handbook?", ShouldReuse: c.Variants[0].ShouldReuse, Reason: "fixture"}}
				s.Cases = append(s.Cases, c)
			}
		},
		"too many variants": func(s *semanticEvalSuite) {
			for i := 2; i < 101; i++ {
				v := s.Cases[0].Variants[0]
				v.ID = fmt.Sprintf("variant-%d", i)
				s.Cases[0].Variants = append(s.Cases[0].Variants, v)
			}
		},
		"too many total pairs": func(s *semanticEvalSuite) {
			base := s.Cases[0]
			s.Cases = nil
			for i := range 11 {
				c := base
				c.ID = fmt.Sprintf("case-%d", i)
				c.Variants = nil
				for j := range 100 {
					v := base.Variants[0]
					v.ID = fmt.Sprintf("variant-%d-%d", i, j)
					c.Variants = append(c.Variants, v)
				}
				s.Cases = append(s.Cases, c)
			}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			suite := semanticEvalTestSuite()
			mutate(&suite)
			if _, err := parseSemanticEvalSuite(semanticEvalTestJSON(t, suite)); err == nil {
				t.Fatal("accepted invalid suite")
			}
		})
	}
}

func TestSemanticEvaluationPartialReportsBeforeAnyModelCall(t *testing.T) {
	for _, args := range [][]string{
		{"--suite", "missing.json"},
		{"--live"},
		{"--live", "--suite", "missing.json", "--timeout", "61m"},
		{"--live", "--suite", "missing.json", "--timeout", "0s"},
		{"--live", "--suite", "missing.json", "--min-similarity", "NaN"},
		{"--live", "--suite", "missing.json", "--min-similarity", "Inf"},
		{"--live", "--suite", "missing.json", "--unknown"},
	} {
		var output, diagnostics bytes.Buffer
		if err := semanticEvaluateCommand(args, &output, &diagnostics); err == nil {
			t.Fatal("invalid invocation accepted", args)
		}
		var report semanticEvalReport
		if err := json.Unmarshal(output.Bytes(), &report); err != nil {
			t.Fatalf("failed invocation lost JSON report: %s %v", output.String(), err)
		}
		if report.Status != "partial_failed" || report.ProtectedTermsVersion != semantic.ProtectedTermsVersion || len(report.Errors) == 0 || report.Usage.LogicalModelCalls != 0 || report.ProviderAPIChargeUSD != 0 || report.PaidFallbackEnabled {
			t.Fatalf("failed invocation lost safe accounting: %+v", report)
		}
	}
	path := filepath.Join(t.TempDir(), "invalid.json")
	raw := []byte(`{"schema_version":2}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := semanticEvaluateCommand([]string{"--live", "--suite", path}, &output, &bytes.Buffer{}); err == nil {
		t.Fatal("invalid suite accepted")
	}
	var report semanticEvalReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(raw)
	if report.SourceSHA256 != hex.EncodeToString(h[:]) || report.Usage.LogicalModelCalls != 0 {
		t.Fatal("partial report lost exact source identity")
	}
}

func TestSemanticEvaluationProtectedTermsVersionJSONField(t *testing.T) {
	var output bytes.Buffer
	if err := semanticEvaluateCommand(nil, &output, &bytes.Buffer{}); err == nil {
		t.Fatal("evaluation without an explicit live invocation succeeded")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["protected_terms_version"]) != fmt.Sprint(semantic.ProtectedTermsVersion) {
		t.Fatalf("report lost lexical guard version: %s", output.String())
	}
}

func TestSemanticEvaluationMetricsKeepMissesErrorsAndCachedUsage(t *testing.T) {
	one, two, zero := int64(1), int64(2), int64(0)
	pairs := []semanticEvalPair{
		{ExpectedReuse: true, Served: true, Status: "served", EmbeddingAttempted: true, EmbeddingInputTokens: &one, VerificationAttempted: true, Verification: &semantic.ValidityResult{GenerationCalled: true, InputTokens: &two, OutputTokens: &one}},
		{ExpectedReuse: false, Served: true, Status: "served", EmbeddingAttempted: true, VerificationAttempted: true, Verification: &semantic.ValidityResult{Reused: true, InputTokens: &zero, OutputTokens: &zero}},
		{ExpectedReuse: false, Status: "guard_rejected"},
		{ExpectedReuse: true, Status: "retrieval_miss"},
		{ExpectedReuse: true, Status: "error", ErrorCategory: "verification_failed", Abstained: true, VerificationAttempted: true, Verification: &semantic.ValidityResult{GenerationCalled: true, Verdict: "unknown"}},
		{ExpectedReuse: false, Status: "not_run", ErrorCategory: "canceled", Abstained: true},
	}
	sources := []semanticEvalSourceEmbedding{{Attempted: true, InputTokens: &two}}
	totals, usage := semanticEvalSummarize(pairs, sources)
	if totals.Total != 6 || totals.TP != 1 || totals.FP != 1 || totals.TN != 2 || totals.FN != 2 || totals.Errors != 2 || totals.Abstentions != 2 || totals.Unattempted != 1 || totals.RetrievalMisses != 1 || totals.GuardRejections != 1 || totals.UnknownVerdicts != 1 {
		t.Fatalf("lost outcome categories: %+v", totals)
	}
	if totals.Precision == nil || *totals.Precision != 0.5 || math.Abs(*totals.Recall-1.0/3) > 1e-12 || *totals.Accuracy != 0.5 || math.Abs(*totals.Coverage-1.0/3) > 1e-12 {
		t.Fatalf("wrong population metrics: %+v", totals)
	}
	if usage.EmbeddingCalls != 3 || usage.EmbeddingKnownUsageCalls != 2 || usage.EmbeddingUnknownUsageCalls != 1 || usage.EmbeddingInputTokens != 3 || usage.VerificationAttempts != 3 || usage.VerifierGenerationCalls != 2 || usage.CachedVerifications != 1 || usage.VerifierInputTokens != 2 || usage.VerifierOutputTokens != 1 || usage.VerifierUnknownUsageCalls != 1 || usage.LogicalModelCalls != 5 {
		t.Fatalf("usage double-counted cached or unknown work: %+v", usage)
	}
	empty, _ := semanticEvalSummarize([]semanticEvalPair{{ExpectedReuse: true, Status: "retrieval_miss"}}, nil)
	if empty.Precision != nil || empty.PrecisionWilson95Lower != nil || empty.PrecisionWilson95Upper != nil || empty.Recall == nil || *empty.Recall != 0 {
		t.Fatalf("no accepted sample invented precision: %+v", empty)
	}
}

func TestSemanticEvaluationWilsonBoundsDescribeSampleUncertainty(t *testing.T) {
	lower, upper := semanticEvalWilson95(36, 36)
	if lower == nil || upper == nil || math.Abs(*lower-0.9035813714055363) > 1e-12 || math.Abs(*upper-1) > 1e-12 {
		t.Fatalf("unexpected Wilson interval: %v %v", lower, upper)
	}
	// Even a perfect sample of six cannot establish a 90% lower bound.
	lower, upper = semanticEvalWilson95(6, 6)
	if lower == nil || *lower >= 0.9 || *upper != 1 {
		t.Fatal("small perfect sample became a universal quality claim")
	}
	if lower, upper = semanticEvalWilson95(0, 0); lower != nil || upper != nil {
		t.Fatal("empty sample acquired confidence bounds")
	}
}

type semanticEvalFakeEmbedder struct{ calls int }

func (e *semanticEvalFakeEmbedder) Embed(_ context.Context, _ string) (semantic.Embedding, error) {
	e.calls++
	tokens := int64(3)
	return semantic.Embedding{ModelID: "offline-fixture", Vector: []float32{1, 0}, InputTokens: &tokens}, nil
}

type semanticEvalFakeVerifier struct {
	calls    int
	requests []semantic.ValidityRequest
}

func (v *semanticEvalFakeVerifier) Identity() string { return "offline-fixture" }
func (v *semanticEvalFakeVerifier) Verify(_ context.Context, request semantic.ValidityRequest) (semantic.ValidityResult, error) {
	v.calls++
	v.requests = append(v.requests, request)
	in, out := int64(5), int64(2)
	return semantic.ValidityResult{GenerationCalled: true, Complete: true, Durable: true, Verdict: "accept", SameIntent: true, SameFacts: true, SameConstraints: true, AnswerSupported: true, InputTokens: &in, OutputTokens: &out, ReceiptKey: "fixture-receipt"}, nil
}

func TestSemanticEvaluationOfflinePipelineEmbedsEachInputOnceAndKeepsAbstentions(t *testing.T) {
	suite := semanticEvalTestSuite()
	// Use an unchanged second question solely to exercise fake-adapter
	// accounting; these synthetic fixtures make no model-quality claim.
	suite.Cases[0].Variants[1].Question = suite.Cases[0].OriginalQuestion
	report := semanticEvalReport{}
	for _, variant := range suite.Cases[0].Variants {
		report.Pairs = append(report.Pairs, semanticEvalPair{ID: variant.ID, ExpectedReuse: *variant.ShouldReuse, Status: "not_run", Abstained: true})
	}
	embedder, verifier := &semanticEvalFakeEmbedder{}, &semanticEvalFakeVerifier{}
	config := semantic.ReuseConfig{VerifierMaxInputTokens: 100, VerifierMaxOutputTokens: 100}
	semanticEvalRun(t.Context(), suite, embedder, verifier, config, 0.9, &report)
	if embedder.calls != 3 || len(report.SourceEmbeddings) != 1 || verifier.calls != 2 || !report.Pairs[0].Served || !report.Pairs[1].Served {
		t.Fatalf("pipeline repeated or lost work: %+v", report)
	}
	for _, request := range verifier.requests {
		if request.OriginalQuestion != suite.Cases[0].OriginalQuestion || request.Instructions != suite.Cases[0].Instructions || request.Context != suite.Cases[0].Evidence || request.CachedAnswer != suite.Cases[0].Answer || request.Intent != suite.Cases[0].Family || request.Bindings == nil {
			t.Fatalf("verifier did not receive the approved data snapshot: %+v", request)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	canceledReport := semanticEvalReport{Pairs: []semanticEvalPair{{Status: "not_run", Abstained: true}, {Status: "not_run", Abstained: true}}}
	semanticEvalRun(ctx, suite, embedder, verifier, config, 0.9, &canceledReport)
	if embedder.calls != 3 || verifier.calls != 2 {
		t.Fatal("canceled evaluation still called adapters")
	}
	for _, row := range canceledReport.Pairs {
		if row.Served || row.Status != "not_run" || row.ErrorCategory != "canceled" {
			t.Fatalf("cancellation lost an unattempted pair: %+v", row)
		}
	}
}

func TestSemanticEvaluationRequiresCompleteDurableBoundedVerifierEvidence(t *testing.T) {
	in, out := int64(5), int64(2)
	config := semantic.ReuseConfig{VerifierMaxInputTokens: 10, VerifierMaxOutputTokens: 10}
	valid := semantic.ValidityResult{GenerationCalled: true, Complete: true, Durable: true, InputTokens: &in, OutputTokens: &out, ReceiptKey: "receipt"}
	if !semanticEvalVerified(valid, config) {
		t.Fatal("valid evidence refused")
	}
	for name, mutate := range map[string]func(*semantic.ValidityResult){"incomplete": func(r *semantic.ValidityResult) { r.Complete = false }, "not durable": func(r *semantic.ValidityResult) { r.Durable = false }, "no receipt": func(r *semantic.ValidityResult) { r.ReceiptKey = "" }, "unknown usage": func(r *semantic.ValidityResult) { r.InputTokens = nil }, "not generated": func(r *semantic.ValidityResult) { r.GenerationCalled = false }, "false cache": func(r *semantic.ValidityResult) { r.Reused = true }} {
		t.Run(name, func(t *testing.T) {
			r := valid
			mutate(&r)
			if semanticEvalVerified(r, config) {
				t.Fatal("unconfirmed verifier accepted")
			}
		})
	}
}

type semanticEvalFakeBatchVerifier struct {
	batches [][]semantic.ValidityRequest
	unknown bool
}

func (v *semanticEvalFakeBatchVerifier) VerifyBatch(_ context.Context, requests []semantic.ValidityRequest) (validityeval.BatchVerificationResult, error) {
	v.batches = append(v.batches, append([]semantic.ValidityRequest(nil), requests...))
	in, out := int64(11), int64(9)
	result := validityeval.BatchVerificationResult{Work: semantic.ValidityResult{GenerationCalled: true, Complete: true, Durable: true, InputTokens: &in, OutputTokens: &out, ReceiptKey: "batch-receipt"}}
	if v.unknown {
		result.Work.InputTokens = nil
		result.Work.OutputTokens = nil
		result.Work.Complete = false
		return result, fmt.Errorf("lost stream")
	}
	for range requests {
		zero := int64(0)
		result.Decisions = append(result.Decisions, semantic.ValidityResult{Verdict: "accept", SameIntent: true, SameFacts: true, SameConstraints: true, AnswerSupported: true, Complete: true, Durable: true, SharedWork: true, Reused: true, InputTokens: &zero, OutputTokens: &zero, ReceiptKey: result.Work.ReceiptKey})
	}
	return result, nil
}

func TestSemanticEvaluationBatchAccountsParentOnceAndRetainsSingletonAndUnknown(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		suite := semanticEvalTestSuite()
		suite.Cases[0].Variants[1].Question = "Where would I find the handbook?"
		third := suite.Cases[0].Variants[0]
		third.ID = "third"
		third.Question = "Where should I look for the handbook?"
		suite.Cases[0].Variants = append(suite.Cases[0].Variants, third)
		report := semanticEvalReport{BatchSize: 2}
		for _, variant := range suite.Cases[0].Variants {
			report.Pairs = append(report.Pairs, semanticEvalPair{ID: variant.ID, ExpectedReuse: *variant.ShouldReuse, Status: "not_run", Abstained: true})
		}
		v := &semanticEvalFakeBatchVerifier{unknown: unknown}
		semanticEvalRun(t.Context(), suite, &semanticEvalFakeEmbedder{}, v, semantic.ReuseConfig{VerifierMaxInputTokens: 100, VerifierMaxOutputTokens: 100}, 0.65, &report)
		_, usage := semanticEvalSummarize(report.Pairs, report.SourceEmbeddings, report.BatchVerificationWork)
		if _, serving := any(v).(semantic.Verifier); serving {
			t.Fatal("pair diagnostic fixture unexpectedly implements serving verification")
		}
		if len(v.batches) != 1 || len(v.batches[0]) != 2 || report.Pairs[2].Status != "batch_required" || report.Pairs[2].VerificationAttempted || usage.VerifierGenerationCalls != 1 || usage.CachedVerifications != 0 {
			t.Fatalf("batch lost population or double counted: %+v", report)
		}
		if unknown {
			if usage.VerifierUnknownUsageCalls != 1 || report.Pairs[0].Served || report.Pairs[1].Served {
				t.Fatalf("unknown shared work became free acceptance: %+v", report)
			}
		} else if usage.VerifierInputTokens != 11 || usage.VerifierOutputTokens != 9 || usage.VerifierKnownUsageCalls != 1 || !report.Pairs[0].Served || !report.Pairs[1].Served {
			t.Fatalf("shared work not accounted once: %+v", usage)
		}
	}
}
