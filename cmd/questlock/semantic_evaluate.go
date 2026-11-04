package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/internal/validityeval"
	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/semantic"
	"golang.org/x/sys/unix"
)

const (
	semanticEvalMaxBytes     = 1 << 20
	semanticEvalMaxText      = 64 << 10
	semanticEvalFrozenSHA256 = "74a6d1dceca122d969438d1f98c3461178bf39554539cbb320651cf546e51779"
)

type semanticEvalSuite struct {
	SchemaVersion int                `json:"schema_version"`
	Population    string             `json:"population"`
	LabelRule     string             `json:"label_rule"`
	Cases         []semanticEvalCase `json:"cases"`
}

type semanticEvalCase struct {
	ID               string                `json:"id"`
	Family           string                `json:"family"`
	Instructions     string                `json:"instructions"`
	Evidence         string                `json:"evidence"`
	Answer           string                `json:"answer"`
	OriginalQuestion string                `json:"original_question"`
	Bindings         map[string]string     `json:"bindings,omitempty"`
	Intent           string                `json:"intent,omitempty"`
	Variants         []semanticEvalVariant `json:"variants"`
}

type semanticEvalVariant struct {
	ID          string `json:"id"`
	Question    string `json:"question"`
	ShouldReuse *bool  `json:"should_reuse"`
	Reason      string `json:"reason"`
}

type semanticEvalPair struct {
	CaseID                string                   `json:"case_id"`
	ID                    string                   `json:"id"`
	ExpectedReuse         bool                     `json:"expected_reuse"`
	Served                bool                     `json:"served"`
	Status                string                   `json:"status"`
	Similarity            *float64                 `json:"similarity"`
	Retrieved             bool                     `json:"retrieved"`
	GuardCodes            []string                 `json:"guard_codes"`
	Verdict               string                   `json:"verdict"`
	ErrorCategory         string                   `json:"error_category,omitempty"`
	Abstained             bool                     `json:"abstained"`
	EmbeddingAttempted    bool                     `json:"embedding_attempted"`
	EmbeddingInputTokens  *int64                   `json:"embedding_input_tokens"`
	EmbeddingModelID      string                   `json:"embedding_model_id,omitempty"`
	EmbeddingMS           float64                  `json:"embedding_ms"`
	VerificationAttempted bool                     `json:"verification_attempted"`
	Verification          *semantic.ValidityResult `json:"verification,omitempty"`
	TotalMS               float64                  `json:"total_ms"`
}

type semanticEvalSourceEmbedding struct {
	CaseID        string  `json:"case_id"`
	Attempted     bool    `json:"attempted"`
	ModelID       string  `json:"model_id,omitempty"`
	InputTokens   *int64  `json:"input_tokens"`
	DurationMS    float64 `json:"duration_ms"`
	ErrorCategory string  `json:"error_category,omitempty"`
}

type semanticEvalTotals struct {
	Total                  int      `json:"total"`
	PositiveLabels         int      `json:"positive_labels"`
	NegativeLabels         int      `json:"negative_labels"`
	Completed              int      `json:"completed"`
	Unattempted            int      `json:"unattempted"`
	Errors                 int      `json:"errors"`
	Abstentions            int      `json:"abstentions"`
	RetrievalMisses        int      `json:"retrieval_misses"`
	GuardRejections        int      `json:"guard_rejections"`
	UnknownVerdicts        int      `json:"unknown_verdicts"`
	ResourceDeferred       int      `json:"resource_deferred,omitempty"`
	Unscored               int      `json:"unscored,omitempty"`
	UnscoredPositiveLabels int      `json:"unscored_positive_labels,omitempty"`
	UnscoredNegativeLabels int      `json:"unscored_negative_labels,omitempty"`
	DeferredPositiveLabels int      `json:"deferred_positive_labels,omitempty"`
	DeferredNegativeLabels int      `json:"deferred_negative_labels,omitempty"`
	ConfusionPairs         *int     `json:"confusion_pairs,omitempty"`
	TP                     int      `json:"true_positives"`
	FP                     int      `json:"false_positives"`
	TN                     int      `json:"true_negatives"`
	FN                     int      `json:"false_negatives"`
	Precision              *float64 `json:"precision"`
	Recall                 *float64 `json:"recall"`
	Accuracy               *float64 `json:"accuracy"`
	Coverage               *float64 `json:"coverage"`
	PrecisionWilson95Lower *float64 `json:"precision_wilson_95_lower"`
	PrecisionWilson95Upper *float64 `json:"precision_wilson_95_upper"`
}

// Per-model counters keep tokenizer units separate. Aggregate verifier counters
// remain the existing reservation-accounting sums, not equivalent token work.
type semanticEvalModelUsage struct {
	Model               string `json:"model"`
	RuntimeIdentity     string `json:"runtime_identity"`
	GenerationCalls     int    `json:"generation_calls"`
	CachedVerifications int    `json:"cached_verifications"`
	KnownUsageCalls     int    `json:"known_usage_calls"`
	UnknownUsageCalls   int    `json:"unknown_usage_calls"`
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
}

type semanticEvalUsage struct {
	VerifierModels             []semanticEvalModelUsage `json:"verifier_models,omitempty"`
	MixedVerifierModelUsage    bool                     `json:"mixed_verifier_model_usage,omitempty"`
	VerifierTokenAccounting    string                   `json:"verifier_token_accounting,omitempty"`
	EmbeddingCalls             int                      `json:"embedding_calls"`
	EmbeddingKnownUsageCalls   int                      `json:"embedding_known_usage_calls"`
	EmbeddingUnknownUsageCalls int                      `json:"embedding_unknown_usage_calls"`
	EmbeddingInputTokens       int64                    `json:"embedding_input_tokens"`
	VerificationAttempts       int                      `json:"verification_attempts"`
	VerifierGenerationCalls    int                      `json:"verifier_generation_calls"`
	DeferredVerifierStages     int                      `json:"deferred_verifier_stages,omitempty"`
	CachedVerifications        int                      `json:"cached_verifications"`
	VerifierKnownUsageCalls    int                      `json:"verifier_known_usage_calls"`
	VerifierUnknownUsageCalls  int                      `json:"verifier_unknown_usage_calls"`
	VerifierInputTokens        int64                    `json:"verifier_input_tokens"`
	VerifierOutputTokens       int64                    `json:"verifier_output_tokens"`
	LogicalModelCalls          int                      `json:"logical_model_calls"`
}

type semanticEvalReport struct {
	BatchSize                    int                              `json:"batch_size,omitempty"`
	BatchVerificationWork        []semantic.ValidityResult        `json:"batch_verification_work,omitempty"`
	Schema                       int                              `json:"schema"`
	ProtectedTermsVersion        int                              `json:"protected_terms_version"`
	OutputContractGuardVersion   int                              `json:"output_contract_guard_version,omitempty"`
	LanguageContractGuardVersion int                              `json:"language_contract_guard_version,omitempty"`
	Mode                         string                           `json:"mode"`
	Status                       string                           `json:"status"`
	SourceSHA256                 string                           `json:"source_sha256"`
	FrozenCorpusSHA256           string                           `json:"frozen_corpus_sha256"`
	MatchesFrozenCorpus          bool                             `json:"matches_frozen_corpus"`
	Population                   string                           `json:"population"`
	LabelRule                    string                           `json:"label_rule"`
	EmbeddingModel               string                           `json:"embedding_model"`
	VerifierModel                string                           `json:"verifier_model"`
	VerifierSourceMaxInputTokens int64                            `json:"verifier_source_max_input_tokens,omitempty"`
	VerifierPairModel            string                           `json:"verifier_pair_model,omitempty"`
	VerifierPairMaxInputTokens   int64                            `json:"verifier_pair_max_input_tokens,omitempty"`
	VerifierPolicy               string                           `json:"verifier_policy,omitempty"`
	VerifierSourceThink          bool                             `json:"verifier_source_think"`
	VerifierMaxInputTokens       int64                            `json:"verifier_max_input_tokens,omitempty"`
	VerifierMaxOutputTokens      int64                            `json:"verifier_max_output_tokens,omitempty"`
	VerifierTimeoutMS            int                              `json:"verifier_timeout_ms,omitempty"`
	VerifierIdentity             string                           `json:"verifier_identity,omitempty"`
	ResourcePolicy               *delegation.ReuseAdmissionPolicy `json:"resource_policy,omitempty"`
	MinSimilarity                float64                          `json:"min_similarity"`
	RetrievalThresholdMeaning    string                           `json:"retrieval_threshold_meaning"`
	QualityTargetMeaning         string                           `json:"quality_target_meaning"`
	MetricMeaning                string                           `json:"metric_meaning"`
	ModelCallBasis               string                           `json:"model_call_basis"`
	SourceAnswers                string                           `json:"source_answers"`
	AnswerGenerationCalls        int                              `json:"answer_generation_calls"`
	SavingsMeasured              bool                             `json:"savings_measured"`
	PhysicalHosts                int                              `json:"physical_hosts"`
	ProviderAPIChargeUSD         int                              `json:"provider_api_charge_usd"`
	PaidFallbackEnabled          bool                             `json:"paid_fallback_enabled"`
	StateRetained                bool                             `json:"state_retained"`
	Errors                       []string                         `json:"errors"`
	SourceEmbeddings             []semanticEvalSourceEmbedding    `json:"source_embeddings"`
	Pairs                        []semanticEvalPair               `json:"pairs"`
	Totals                       semanticEvalTotals               `json:"totals"`
	Usage                        semanticEvalUsage                `json:"usage"`
	TotalMS                      float64                          `json:"total_ms"`
}

func semanticEvaluateCommand(args []string, output, diagnostics io.Writer) (runErr error) {
	started := time.Now()
	var state *semanticEvalState
	report := semanticEvalReport{Schema: 1, ProtectedTermsVersion: semantic.ProtectedTermsVersion, Mode: "local_semantic_validity_diagnostic", Status: "partial_failed", FrozenCorpusSHA256: semanticEvalFrozenSHA256, PhysicalHosts: 1, SourceAnswers: "fixed_labelled_fixtures", RetrievalThresholdMeaning: "Cosine retrieval cutoff only; it is not a validity confidence or quality estimate.", QualityTargetMeaning: "The requested 90–98% target is not a universal guarantee. Metrics and Wilson intervals describe this declared sample only; corpus selection and dependence limit generalization.", MetricMeaning: "TP/FP/TN/FN classify served results and known withholding decisions. Any served result is counted even when execution also failed, so unsafe false acceptances cannot disappear. Unserved execution errors, unattempted work and proven pre-claim resource deferrals remain in Total and label counts but are unscored. Accuracy and recall use the scored population; coverage is served/Total. Operational errors, unknowns and missing work remain visible and prevent full qualification. Precision is null when nothing is served. Proven pre-claim resource deferrals remain in the declared population and label counts but are excluded from TP/FP/TN/FN, recall and accuracy; confusion_pairs declares that denominator. Any deferred, missing or failed work prevents complete qualification.", ModelCallBasis: "Logical embedding Embed calls plus verifier Generate calls reported by the inference runner; metadata reads and token-count preflight requests are excluded. Verification attempts, cached verifications and unknown usage are separate.", Errors: []string{}, SourceEmbeddings: []semanticEvalSourceEmbedding{}, Pairs: []semanticEvalPair{}}
	defer func() {
		report.TotalMS = float64(time.Since(started)) / float64(time.Millisecond)
	QualityScores               []paraphraseConfidenceScore       `json:"quality_scores,omitempty`
}