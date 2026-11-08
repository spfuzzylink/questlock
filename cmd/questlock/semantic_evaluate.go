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
	StopOnExecutionError         bool                             `json:"stop_on_execution_error,omitempty"`
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
	VerifierPairThink            bool                             `json:"verifier_pair_think,omitempty"`
	VerifierPairTemperature      *float64                         `json:"verifier_pair_temperature,omitempty"`
	VerifierPairSeed             *int64                           `json:"verifier_pair_seed,omitempty"`
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
		report.Totals, report.Usage = semanticEvalSummarize(report.Pairs, report.SourceEmbeddings, report.BatchVerificationWork)
		if runErr == nil {
			report.Status = "completed"
		}
		knownComplete := runErr == nil && report.Totals.Unattempted == 0 && report.Totals.Errors == 0 && report.Totals.ResourceDeferred == 0 && report.Totals.Unscored == 0 && report.Usage.VerifierUnknownUsageCalls == 0 && report.Usage.EmbeddingUnknownUsageCalls == 0
		if state != nil {
			if err := state.close(); err != nil {
				runErr = errors.Join(runErr, errors.New("semantic evaluation state close unconfirmed"))
				knownComplete = false
				report.Status = "partial_failed"
				report.Errors = append(report.Errors, "state_close_failed")
			}
			report.StateRetained = state.explicit || !knownComplete
		}
		if err := writeModelJSON(output, report); err != nil {
			runErr = errors.Join(runErr, err)
			knownComplete = false // Never remove state when report delivery failed.
		}
		if state != nil {
			retained := state.explicit || !knownComplete
			if !retained {
				if err := state.discard(); err != nil {
					retained = true
					runErr = errors.Join(runErr, errors.New("semantic evaluation state removal unconfirmed"))
				}
			}
			if retained {
				_, _ = fmt.Fprintf(diagnostics, "Private evaluation state retained at %q. Inspect its state.db and worker.token; do not rerun or change a revision to escape an uncertain claim.\n", state.dir)
			}
		}
	}()
	fail := func(category string) error {
		report.Errors = append(report.Errors, category)
		return errors.New("semantic evaluation failed: " + category)
	}
	f := flag.NewFlagSet("semantic evaluate", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	live := f.Bool("live", false, "required: contact explicitly installed local embedding and verifier models")
	suitePath := f.String("suite", "", "required: frozen labelled evaluation suite JSON")
	base := f.String("ollama-url", "http://127.0.0.1:11434", "literal-loopback Ollama HTTP origin")
	embeddingModel := f.String("embedding-model", "all-minilm", "installed local embedding model")
	verifierModel := f.String("verifier-model", "qwen3.5:4b", "installed local validity-verifier model")
	verifierSourceInput := f.Int64("verifier-source-max-input-tokens", 0, "optional source input cap, at most half the total verifier input cap; zero keeps the existing half")
	verifierPairModel := f.String("verifier-pair-model", "", "optional installed factorized applicability model; empty uses verifier-model")
	verifierPairInput := f.Int64("verifier-pair-max-input-tokens", 0, "optional applicability input cap; zero uses half total verifier input, positive values cannot exceed that half")
	verifierInput := f.Int64("verifier-max-input-tokens", 0, "total verifier input reservation cap; zero selects the policy default")
	verifierOutput := f.Int64("verifier-max-output-tokens", 0, "total verifier output cap; zero selects the policy default")
	verifierTimeout := f.Int("verifier-timeout-ms", 0, "shared source+pair deadline in ms; zero selects default (thinking: 120000); max 600000 only for factorized thinking, otherwise 120000; earlier caller deadline wins")
	verifierPolicy := f.String("verifier-policy", "", "empty for published standalone; factorized-v1 for source certification plus answer applicability")
	verifierSourceThink := f.Bool("verifier-source-think", false, "explicit bounded reasoning for factorized-v1 source certification only")
	verifierPairThink := f.Bool("verifier-pair-think", false, "explicit bounded reasoning for factorized-v1 applicability only; source reasoning is independent")
	verifierPairTemperature := f.Float64("verifier-pair-temperature", 0, "optional factorized pair temperature in 0..2; absent keeps zero")
	verifierPairSeed := f.Int64("verifier-pair-seed", 0, "optional factorized pair seed in 0..2147483647; absent leaves the runtime default")
	threshold := f.Float64("min-similarity", 0.9, "retrieval cosine cutoff; not validity confidence")
	batchSize := f.Int("batch-size", 1, "1 for standalone validity; 2 for compatible validity pairs")
	timeout := f.Duration("timeout", 20*time.Minute, "overall diagnostic deadline; max 1h, or 2h only with --verifier-pair-think")
	resourcePolicyPath := f.String("resource-policy", "", "optional strict JSON runtime admission policy for factorized-v1; resource deferral prevents full qualification")
	stopOnExecutionError := f.Bool("stop-on-execution-error", false, "stop scheduling after an execution error or unconfirmed evidence; known semantic outcomes continue")
	stateDir := f.String("state-dir", "", "new absolute private state directory; explicit state is retained, failed temporary state is also retained")
	f.Usage = func() {
		_, _ = fmt.Fprintln(diagnostics, "Usage: questlock semantic evaluate --live --suite PATH [--state-dir NEW_PRIVATE_PATH] [--embedding-model all-minilm] [--verifier-model qwen3.5:4b] [--min-similarity 0.9] [--batch-size 1|2] [--verifier-policy factorized-v1] [--verifier-source-think] [--verifier-pair-think] [--verifier-pair-temperature N --verifier-pair-seed N] [--verifier-source-max-input-tokens N] [--verifier-pair-model MODEL --verifier-pair-max-input-tokens N] [--resource-policy PATH] [--stop-on-execution-error] [--timeout 20m]\nThis diagnostic uses fixed labelled source answers, local embeddings and durable local validity inference. It measures no answer-generation saving. Verifier deadlines default to 120000ms for thinking; an explicit factorized-thinking deadline may be at most 600000ms and is shared by both stages.")
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fail("invalid_flags")
	}
	var pairTemperature *float64
	var pairSeed *int64
	f.Visit(func(v *flag.Flag) {
		switch v.Name {
		case "verifier-pair-temperature":
			pairTemperature = verifierPairTemperature
		case "verifier-pair-seed":
			pairSeed = verifierPairSeed
		}
	})
	if pairTemperature != nil && !math.IsNaN(*pairTemperature) && !math.IsInf(*pairTemperature, 0) {
		report.VerifierPairTemperature = pairTemperature
	}
	report.VerifierPairSeed = pairSeed
	report.EmbeddingModel, report.VerifierModel = *embeddingModel, *verifierModel
	report.VerifierSourceMaxInputTokens = *verifierSourceInput
	report.VerifierPairModel, report.VerifierPairMaxInputTokens = *verifierPairModel, *verifierPairInput
	report.BatchSize = *batchSize
	report.VerifierPolicy = *verifierPolicy
	report.VerifierSourceThink = *verifierSourceThink
	report.VerifierPairThink = *verifierPairThink
	report.StopOnExecutionError = *stopOnExecutionError
	if *verifierPolicy == "factorized-v1" {
		report.OutputContractGuardVersion = semantic.OutputContractGuardVersion
		report.LanguageContractGuardVersion = semantic.LanguageContractGuardVersion
	}
	if !math.IsNaN(*threshold) && !math.IsInf(*threshold, 0) {
		report.MinSimilarity = *threshold
	}
	maxDiagnosticTimeout := time.Hour
	if *verifierPairThink {
		maxDiagnosticTimeout = 2 * time.Hour
	}
	if (*resourcePolicyPath != "" && *verifierPolicy != "factorized-v1") || (*verifierPolicy != "" && *verifierPolicy != "factorized-v1") || (*verifierPolicy != "" && *batchSize != 1) || ((*verifierSourceThink || *verifierPairThink) && *verifierPolicy != "factorized-v1") || f.NArg() != 0 || !*live || *suitePath == "" || *batchSize < 1 || *batchSize > 2 || *timeout <= 0 || *timeout > maxDiagnosticTimeout || math.IsNaN(*threshold) || math.IsInf(*threshold, 0) || *threshold < 0 || *threshold > 1 {
		return fail("explicit_live_suite_and_bounded_flags_required")
	}
	suite, raw, err := loadSemanticEvalSuite(*suitePath)
	if raw != nil {
		h := sha256.Sum256(raw)
		report.SourceSHA256 = hex.EncodeToString(h[:])
	}
	report.MatchesFrozenCorpus = report.SourceSHA256 == semanticEvalFrozenSHA256
	if err != nil {
		return fail("invalid_suite")
	}
	report.Population, report.LabelRule = suite.Population, suite.LabelRule
	for _, c := range suite.Cases {
		for _, variant := range c.Variants {
			report.Pairs = append(report.Pairs, semanticEvalPair{CaseID: c.ID, ID: variant.ID, ExpectedReuse: *variant.ShouldReuse, Status: "not_run", Verdict: "not_requested", Abstained: true, GuardCodes: []string{}})
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	config, err := semantic.NormalizeReuseConfig(semantic.ReuseConfig{Config: semantic.Config{Mode: "controlled", Family: "controlled-evaluation", Domain: "synthetic-challenge", EvidenceRevision: report.SourceSHA256, EmbeddingModel: *embeddingModel, MinSimilarity: *threshold, MaxInputBytes: semantic.MaxInputBytes}, ReadOnly: true, Intent: "answer-validity-evaluation", VerifierModel: *verifierModel, VerifierSourceMaxInputTokens: *verifierSourceInput, VerifierPairModel: *verifierPairModel, VerifierPairMaxInputTokens: *verifierPairInput, VerifierPolicy: *verifierPolicy, VerifierSourceThink: *verifierSourceThink, VerifierPairThink: *verifierPairThink, VerifierPairTemperature: pairTemperature, VerifierPairSeed: pairSeed, VerifierMaxInputTokens: *verifierInput, VerifierMaxOutputTokens: *verifierOutput, VerifierTimeoutMS: *verifierTimeout})
	if err != nil {
		return fail("invalid_local_model_policy")
	}
	report.VerifierMaxInputTokens, report.VerifierMaxOutputTokens, report.VerifierTimeoutMS = config.VerifierMaxInputTokens, config.VerifierMaxOutputTokens, config.VerifierTimeoutMS
	var admit inference.AdmissionFunc
	if *resourcePolicyPath != "" {
		file, openErr := os.Open(*resourcePolicyPath)
		if openErr != nil {
			return fail("invalid_resource_policy")
		}
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			_ = file.Close()
			return fail("invalid_resource_policy")
		}
		policy, loadErr := delegation.LoadReuseAdmissionPolicy(file)
		closeErr := file.Close()
		if loadErr != nil || closeErr != nil {
			return fail("invalid_resource_policy")
		}
		admit, err = delegation.NewResourceAdmission(policy, nil)
		if err != nil {
			return fail("invalid_resource_policy")
		}
		report.ResourcePolicy = &policy
	}
	// Validate and create state before any provider metadata or inference. Existing
	// paths are never reused: a retained uncertain job needs operator inspection.
	state, err = newSemanticEvalBroker(ctx, *stateDir)
	if err != nil {
		return fail("evaluation_state_setup_failed")
	}
	broker := state.broker
	// The embedder validates the literal-loopback origin before discovery. It
	// never pulls weights, follows redirects, or uses environment proxies.
	embedder, err := semantic.NewOllamaEmbedderContext(ctx, *base, *embeddingModel, config.MaxInputBytes)
	if err != nil {
		return fail("embedding_setup_failed")
	}
	defer embedder.Close()
	var verifier interface {
		Identity() string
		Close()
	}
	if *batchSize == 2 {
		verifier, err = validityeval.NewPairVerifier(ctx, broker, *base, config)
	} else if *verifierPolicy == "factorized-v1" {
		var factorized *delegation.FactorizedVerifier
		factorized, err = delegation.NewFactorizedVerifier(ctx, broker, *base, config)
		if factorized != nil {
			factorized.Admission = admit
			verifier = factorized
		}
	} else {
		verifier, err = delegation.NewLocalVerifier(ctx, broker, *base, config)
	}
	if err != nil {
		return fail("verifier_setup_failed")
	}
	defer verifier.Close()
	report.VerifierIdentity = verifier.Identity()
	semanticEvalRun(ctx, suite, embedder, verifier, config, *threshold, &report)
	if ctx.Err() != nil {
		return fail(semanticEvalContextCategory(ctx))
	}
	for _, pair := range report.Pairs {
		if semanticEvalResourceDeferred(pair) {
			return fail("resource_admission_deferred")
		}
		if pair.Status == "not_run" || pair.Status == "queued" || pair.Status == "batch_required" {
			return fail("pair_execution_incomplete")
		}
		if pair.ErrorCategory != "" {
			return fail("pair_execution_errors")
		}
	}
	totals, usage := semanticEvalSummarize(report.Pairs, report.SourceEmbeddings, report.BatchVerificationWork)
	if totals.Unscored != 0 {
		return fail("pair_execution_incomplete")
	}
	if usage.VerifierUnknownUsageCalls != 0 || usage.EmbeddingUnknownUsageCalls != 0 {
		return fail("usage_unconfirmed")
	}
	return nil
}

func loadSemanticEvalSuite(path string) (semanticEvalSuite, []byte, error) {
	var suite semanticEvalSuite
	f, err := os.Open(path)
	if err != nil {
		return suite, nil, errors.New("could not open semantic suite")
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > semanticEvalMaxBytes {
		return suite, nil, errors.New("semantic suite must be a regular file at most 1MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(f, semanticEvalMaxBytes+1))
	if err != nil {
		return suite, nil, errors.New("could not read semantic suite")
	}
	suite, err = parseSemanticEvalSuite(raw)
	return suite, raw, err
}

func parseSemanticEvalSuite(raw []byte) (semanticEvalSuite, error) {
	var suite semanticEvalSuite
	invalid := errors.New("invalid bounded semantic evaluation suite")
	if len(raw) > semanticEvalMaxBytes || protocol.DecodeDataJSON(raw, &suite) != nil || suite.SchemaVersion != 1 || !semanticEvalText(suite.Population) || !semanticEvalText(suite.LabelRule) || len(suite.Cases) < 1 || len(suite.Cases) > 24 {
		return semanticEvalSuite{}, invalid
	}
	ids := map[string]bool{}
	variantIDs := map[string]bool{}
	total := 0
	for _, c := range suite.Cases {
		if !semanticEvalLabel(c.ID) || !semanticEvalLabel(c.Family) || ids[c.ID] || !semanticEvalText(c.Instructions) || !semanticEvalText(c.Evidence) || !semanticEvalText(c.Answer) || !semanticEvalText(c.OriginalQuestion) || len(c.Variants) < 1 || len(c.Variants) > 100 || c.Intent != "" && !semanticEvalLabel(c.Intent) || len(c.Bindings) > 32 {
			return semanticEvalSuite{}, invalid
		}
		ids[c.ID] = true
		for key, value := range c.Bindings {
			if !semanticEvalLabel(key) || !semanticEvalText(value) {
				return semanticEvalSuite{}, invalid
			}
		}
		for _, v := range c.Variants {
			if !semanticEvalLabel(v.ID) || variantIDs[v.ID] || !semanticEvalText(v.Question) || !semanticEvalText(v.Reason) || v.ShouldReuse == nil {
				return semanticEvalSuite{}, invalid
			}
			variantIDs[v.ID] = true
			total++
		}
	}
	if total > 1000 {
		return semanticEvalSuite{}, invalid
	}
	return suite, nil
}

func semanticEvalText(value string) bool {
	return len(value) <= semanticEvalMaxText && strings.TrimSpace(value) != ""
}
func semanticEvalLabel(value string) bool {
	if !semanticEvalText(value) || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if unicode.IsSpace(c) || unicode.IsControl(c) {
			return false
		}
	}
	return true
}

// Evaluation state is disposable only after a fully known completed run. Closing
// network/database handles must never implicitly delete pending job ownership.
type semanticEvalState struct {
	broker   inference.Broker
	dir      string
	explicit bool
	info     os.FileInfo
	store    *store.Store
	server   *httptest.Server
	closed   bool
}

func newSemanticEvalBroker(ctx context.Context, requested string) (*semanticEvalState, error) {
	dir, info, err := semanticEvalNewStateDirectory(requested)
	if err != nil {
		return nil, err
	}
	state := &semanticEvalState{dir: dir, info: info, explicit: requested != ""}
	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		return state, err
	}
	state.store = s
	token, err := s.CreateToken(ctx, "semantic-evaluator", "semantic-evaluation")
	if err != nil {
		return state, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "worker.token"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return state, errors.New("cannot persist private evaluation credential")
	}
	_, writeErr := io.WriteString(f, token+"\n")
	syncErr, closeErr := f.Sync(), f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return state, errors.New("private evaluation credential write unconfirmed")
	}
	state.server = httptest.NewServer(httpapi.New(s))
	state.broker, err = client.New(state.server.URL, token)
	return state, err
}

func (s *semanticEvalState) close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	if s.server != nil {
		s.server.Close()
	}
	if s.store != nil {
		return s.store.Close()
	}
	return nil
}

func (s *semanticEvalState) discard() error {
	if s.explicit || !s.closed {
		return errors.New("evaluation state is not disposable")
	}
	current, err := os.Lstat(s.dir)
	if err != nil || !current.IsDir() || !os.SameFile(current, s.info) {
		return errors.New("evaluation state directory changed")
	}
	return os.RemoveAll(s.dir)
}

func semanticEvalNewStateDirectory(requested string) (string, os.FileInfo, error) {
	invalid := errors.New("evaluation state requires a new absolute directory below an owned non-writable-by-others parent, without symlinks")
	var dir string
	if requested == "" {
		var err error
		dir, err = os.MkdirTemp("", "questlock-semantic-evaluation-")
		if err != nil {
			return "", nil, invalid
		}
	} else {
		if !filepath.IsAbs(requested) || filepath.Clean(requested) != requested || requested == string(filepath.Separator) {
			return "", nil, invalid
		}
		// Walk directory descriptors rather than resolving symlinks. Creation is
		// exclusive under the inspected parent; no existing database is opened.
		fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return "", nil, invalid
		}
		defer func() { unix.Close(fd) }()
		parts := strings.Split(strings.TrimPrefix(requested, "/"), "/")
		for _, part := range parts[:len(parts)-1] {
			next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return "", nil, invalid
			}
			var ancestor unix.Stat_t
			if unix.Fstat(next, &ancestor) != nil || ancestor.Uid != 0 && ancestor.Uid != uint32(os.Getuid()) || ancestor.Mode&0022 != 0 && ancestor.Mode&unix.S_ISVTX == 0 {
				unix.Close(next)
				return "", nil, invalid
			}
			unix.Close(fd)
			fd = next
		}
		var parent unix.Stat_t
		if unix.Fstat(fd, &parent) != nil || parent.Uid != uint32(os.Getuid()) || parent.Mode&0022 != 0 {
			return "", nil, invalid
		}
		if err := unix.Mkdirat(fd, parts[len(parts)-1], 0700); err != nil {
			return "", nil, invalid
		}
		dir = requested
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", nil, invalid
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return "", nil, invalid
	}
	return dir, info, nil
}

func semanticEvalRun(ctx context.Context, suite semanticEvalSuite, embedder semantic.Embedder, verifier any, config semantic.ReuseConfig, threshold float64, report *semanticEvalReport) {
	index := 0
	for _, c := range suite.Cases {
		var batchRequests []semantic.ValidityRequest
		var batchRows []int
		var original semantic.Embedding
		var originalErr error
		source := semanticEvalSourceEmbedding{CaseID: c.ID}
		if ctx.Err() == nil {
			source.Attempted = true
			started := time.Now()
			original, originalErr = embedder.Embed(ctx, c.OriginalQuestion)
			source.DurationMS = float64(time.Since(started)) / float64(time.Millisecond)
			source.ModelID, source.InputTokens = original.ModelID, original.InputTokens
			if originalErr != nil {
				source.ErrorCategory = "source_embedding_failed"
			}
		} else {
			source.ErrorCategory = semanticEvalContextCategory(ctx)
			originalErr = ctx.Err()
		}
		report.SourceEmbeddings = append(report.SourceEmbeddings, source)
		if semanticEvalStopRequested(report) {
			return
		}
		for _, variant := range c.Variants {
			row := &report.Pairs[index]
			index++
			if ctx.Err() != nil {
				row.ErrorCategory = semanticEvalContextCategory(ctx)
				if report.StopOnExecutionError {
					return
				}
				continue
			}
			started := time.Now()
			row.EmbeddingAttempted = true
			embedding, err := embedder.Embed(ctx, variant.Question)
			row.EmbeddingMS = float64(time.Since(started)) / float64(time.Millisecond)
			row.EmbeddingInputTokens, row.EmbeddingModelID = embedding.InputTokens, embedding.ModelID
			row.GuardCodes = semantic.CheckReuseProtectedTerms(c.OriginalQuestion, variant.Question, config.VerifierPolicy)
			if config.VerifierPolicy == "factorized-v1" {
				row.GuardCodes = append(row.GuardCodes, semantic.CheckReuseAnswerContract(variant.Question, c.Answer)...)
				row.GuardCodes = append(row.GuardCodes, semantic.CheckReuseLanguageContract(c.OriginalQuestion, c.Instructions, variant.Question)...)
			}
			if row.GuardCodes == nil {
				row.GuardCodes = []string{}
			}
			if err != nil {
				row.Status, row.ErrorCategory = "error", "variant_embedding_failed"
			} else if originalErr != nil {
				row.Status, row.ErrorCategory = "error", "source_embedding_failed"
			} else if original.ModelID == "" || embedding.ModelID != original.ModelID {
				row.Status, row.ErrorCategory = "error", "embedding_identity_changed"
			} else {
				score, scoreErr := semantic.Cosine(original.Vector, embedding.Vector)
				if scoreErr != nil {
					row.Status, row.ErrorCategory = "error", "invalid_embedding_vector"
				} else {
					row.Similarity = &score
					row.Retrieved = score >= threshold
					row.Abstained = false
					if !row.Retrieved {
						row.Status = "retrieval_miss"
					} else if len(row.GuardCodes) > 0 {
						row.Status = "guard_rejected"
					} else {
						row.VerificationAttempted = true
						intent := c.Intent
						if intent == "" {
							intent = c.Family
						}
						bindings := c.Bindings
						if bindings == nil {
							bindings = map[string]string{}
						}
						request := semantic.ValidityRequest{OriginalQuestion: c.OriginalQuestion, Question: variant.Question, Instructions: c.Instructions, Context: c.Evidence, CachedAnswer: c.Answer, Bindings: bindings, Intent: intent}
						if config.VerifierPolicy == "factorized-v1" {
							request.Source = semanticEvalValiditySource(request)
						}
						if report.BatchSize > 1 {
							batchRequests = append(batchRequests, request)
							batchRows = append(batchRows, index-1)
							row.VerificationAttempted = false
							row.Status, row.Abstained = "queued", true
							row.TotalMS = float64(time.Since(started)) / float64(time.Millisecond)
							continue
						}
						var result semantic.ValidityResult
						var verifyErr error
						if single, ok := verifier.(semantic.Verifier); ok {
							result, verifyErr = single.Verify(ctx, request)
						} else {
							verifyErr = errors.New("standalone diagnostic verifier unavailable")
						}
						row.Verification = &result
						row.Verdict = result.Verdict
						if errors.Is(verifyErr, inference.ErrAdmissionDeferred) && semanticEvalDeferredVerification(result) {
							row.Status, row.ErrorCategory, row.Abstained = "resource_deferred", "resource_deferred", true
						} else if verifyErr != nil {
							row.Status, row.ErrorCategory, row.Abstained = "error", "verification_failed", true
						} else if !semanticEvalVerified(result, config) {
							row.Status, row.ErrorCategory, row.Abstained = "error", "verification_evidence_unconfirmed", true
						} else if result.Verdict == "unknown" || result.Verdict == "" {
							row.Status, row.Abstained = "abstained", true
						} else if result.Accepted() {
							row.Status, row.Served = "served", true
						} else {
							row.Status = "verifier_rejected"
						}
					}
				}
			}
			row.TotalMS = float64(time.Since(started)) / float64(time.Millisecond)
			if semanticEvalStopRequested(report) {
				return
			}
		}
		if report.BatchSize > 1 {
			semanticEvalRunBatches(ctx, verifier, config, report, batchRequests, batchRows)
			if semanticEvalStopRequested(report) {
				return
			}
		}
	}
}

// This scheduling control uses existing execution evidence only. A protocol-invalid
// native response normalized by an adapter to a known semantic UNKNOWN cannot be
// distinguished here; qualification must still inspect its retained raw receipt.
func semanticEvalStopRequested(report *semanticEvalReport) bool {
	if !report.StopOnExecutionError {
		return false
	}
	for _, source := range report.SourceEmbeddings {
		if source.ErrorCategory != "" {
			return true
		}
	}
	for _, row := range report.Pairs {
		if row.ErrorCategory != "" {
			return true
		}
	}
	return false
}

type internalPairVerifier interface {
	VerifyBatch(context.Context, []semantic.ValidityRequest) (validityeval.BatchVerificationResult, error)
}

func semanticEvalRunBatches(ctx context.Context, verifier any, config semantic.ReuseConfig, report *semanticEvalReport, requests []semantic.ValidityRequest, rows []int) {
	for offset := 0; offset < len(requests); {
		n := min(report.BatchSize, len(requests)-offset)
		// Avoid an avoidable singleton tail without choosing by expected labels.
		if len(requests)-offset-n == 1 && n > 2 {
			n--
		}
		selected, indices := requests[offset:offset+n], rows[offset:offset+n]
		offset += n
		if n < 2 {
			report.Pairs[indices[0]].Status = "batch_required"
			continue
		}
		v, ok := verifier.(internalPairVerifier)
		if !ok {
			for _, i := range indices {
				report.Pairs[i].Status, report.Pairs[i].ErrorCategory = "error", "batch_verifier_unavailable"
			}
			if semanticEvalStopRequested(report) {
				return
			}
			continue
		}
		started := time.Now()
		batch, err := v.VerifyBatch(ctx, selected)
		if batch.Work.ReceiptKey != "" {
			report.BatchVerificationWork = append(report.BatchVerificationWork, batch.Work)
		}
		for position, i := range indices {
			row := &report.Pairs[i]
			row.TotalMS += float64(time.Since(started)) / float64(time.Millisecond)
			row.VerificationAttempted = true
			if err != nil || len(batch.Decisions) != n || !semanticEvalVerified(batch.Work, config) {
				row.Status, row.ErrorCategory, row.Abstained = "error", "batch_verification_unconfirmed", true
				continue
			}
			result := batch.Decisions[position]
			row.Verification, row.Verdict = &result, result.Verdict
			if !result.SharedWork || !result.Reused || result.GenerationCalled || result.InputTokens == nil || result.OutputTokens == nil || *result.InputTokens != 0 || *result.OutputTokens != 0 || result.ReceiptKey != batch.Work.ReceiptKey || !semanticEvalVerified(result, config) {
				row.Status, row.ErrorCategory, row.Abstained = "error", "verification_evidence_unconfirmed", true
			} else if result.Verdict == "unknown" || result.Verdict == "" {
				row.Status, row.Abstained = "abstained", true
			} else if result.Accepted() {
				row.Status, row.Served, row.Abstained = "served", true, false
			} else {
				row.Status, row.Abstained = "verifier_rejected", false
			}
		}
		if semanticEvalStopRequested(report) {
			return
		}
	}
}

func semanticEvalVerified(result semantic.ValidityResult, config semantic.ReuseConfig) bool {
	if semantic.ValidateVerifierWork(result, config) != nil {
		return false
	}
	if !result.Complete || !result.Durable || result.ReceiptKey == "" || result.InputTokens == nil || result.OutputTokens == nil || *result.InputTokens < 0 || *result.OutputTokens < 0 || *result.InputTokens > config.VerifierMaxInputTokens || *result.OutputTokens > config.VerifierMaxOutputTokens {
		return false
	}
	if result.Reused {
		return !result.GenerationCalled && *result.InputTokens == 0 && *result.OutputTokens == 0
	}
	return result.GenerationCalled && *result.InputTokens > 0
}

func semanticEvalDeferredStage(w semantic.VerificationWork) bool {
	return w.AdmissionDeferred && !w.GenerationCalled && !w.Complete && !w.Durable && !w.Reused && w.ReceiptKey == "" && w.OutputSHA256 == "" && w.InputTokens != nil && w.OutputTokens != nil && *w.InputTokens == 0 && *w.OutputTokens == 0
}

// This is adapter evidence of a missing applicability decision, not a negative
// model verdict. A completed source certificate may precede the deferred stage;
// its real work remains counted. Unknown or executed final work cannot qualify.
func semanticEvalDeferredVerification(v semantic.ValidityResult) bool {
	if len(v.Work) < 1 || len(v.Work) > 2 || v.Complete || v.Durable || v.Reused || v.SharedWork || v.Verdict != "unknown" || v.ReceiptKey != "" || v.OutputSHA256 != "" || v.InputTokens == nil || v.OutputTokens == nil {
		return false
	}
	var input, output int64
	generated := false
	for i, w := range v.Work {
		stage := "source"
		if i == 1 {
			stage = "equivalence"
		}
		if w.Stage != stage {
			return false
		}
		if i == len(v.Work)-1 {
			if !semanticEvalDeferredStage(w) {
				return false
			}
		} else {
			const prefix = "inference/v1/jobs/"
			job, err := hex.DecodeString(strings.TrimPrefix(w.ReceiptKey, prefix))
			hash, hashErr := hex.DecodeString(w.OutputSHA256)
			if w.AdmissionDeferred || !w.Complete || !w.Durable || !strings.HasPrefix(w.ReceiptKey, prefix) || err != nil || len(job) != 32 || hashErr != nil || len(hash) != 32 || w.InputTokens == nil || w.OutputTokens == nil || *w.InputTokens < 0 || *w.OutputTokens < 0 {
				return false
			}
			if w.Reused {
				if w.GenerationCalled || *w.InputTokens != 0 || *w.OutputTokens != 0 {
					return false
				}
			} else if !w.GenerationCalled || *w.InputTokens < 1 || *w.OutputTokens < 1 {
				return false
			}
			input += *w.InputTokens
			output += *w.OutputTokens
			generated = generated || w.GenerationCalled
		}
	}
	return *v.InputTokens == input && *v.OutputTokens == output && v.GenerationCalled == generated
}

func semanticEvalResourceDeferred(row semanticEvalPair) bool {
	return row.Status == "resource_deferred" && row.ErrorCategory == "resource_deferred" && !row.Served && row.Verification != nil && semanticEvalDeferredVerification(*row.Verification)
}

func semanticEvalContextCategory(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	return "canceled"
}

func semanticEvalSummarize(pairs []semanticEvalPair, sources []semanticEvalSourceEmbedding, shared ...[]semantic.ValidityResult) (semanticEvalTotals, semanticEvalUsage) {
	totals := semanticEvalTotals{Total: len(pairs)}
	usage := semanticEvalUsage{}
	var countVerification func(semantic.ValidityResult)
	countVerification = func(result semantic.ValidityResult) {
		if len(result.Work) > 0 {
			for _, w := range result.Work {
				if semanticEvalDeferredStage(w) {
					usage.DeferredVerifierStages++
					continue
				}
				if w.Model != "" && w.RuntimeIdentity != "" {
					i := 0
					for i < len(usage.VerifierModels) && (usage.VerifierModels[i].Model != w.Model || usage.VerifierModels[i].RuntimeIdentity != w.RuntimeIdentity) {
						i++
					}
					if i == len(usage.VerifierModels) {
						usage.VerifierModels = append(usage.VerifierModels, semanticEvalModelUsage{Model: w.Model, RuntimeIdentity: w.RuntimeIdentity})
					}
					bucket := &usage.VerifierModels[i]
					if w.GenerationCalled {
						bucket.GenerationCalls++
					}
					if w.Reused {
						bucket.CachedVerifications++
					}
					if w.InputTokens == nil || w.OutputTokens == nil {
						bucket.UnknownUsageCalls++
					} else {
						bucket.KnownUsageCalls++
						bucket.InputTokens += *w.InputTokens
						bucket.OutputTokens += *w.OutputTokens
					}
				}
				countVerification(semantic.ValidityResult{GenerationCalled: w.GenerationCalled, Reused: w.Reused, InputTokens: w.InputTokens, OutputTokens: w.OutputTokens})
			}
			return
		}
		if result.SharedWork {
			return
		}
		if result.GenerationCalled {
			usage.VerifierGenerationCalls++
		}
		if result.Reused {
			usage.CachedVerifications++
		}
		if result.InputTokens == nil || result.OutputTokens == nil {
			usage.VerifierUnknownUsageCalls++
		} else {
			usage.VerifierKnownUsageCalls++
			usage.VerifierInputTokens += *result.InputTokens
			usage.VerifierOutputTokens += *result.OutputTokens
		}
	}
	for _, work := range shared {
		for _, result := range work {
			countVerification(result)
		}
	}
	countEmbedding := func(attempted bool, tokens *int64) {
		if !attempted {
			return
		}
		usage.EmbeddingCalls++
		if tokens == nil {
			usage.EmbeddingUnknownUsageCalls++
		} else {
			usage.EmbeddingKnownUsageCalls++
			usage.EmbeddingInputTokens += *tokens
		}
	}
	for _, source := range sources {
		countEmbedding(source.Attempted, source.InputTokens)
	}
	for _, row := range pairs {
		deferred := semanticEvalResourceDeferred(row)
		unscored := !row.Served && (deferred || row.ErrorCategory != "" || row.Status == "error" || row.Status == "not_run" || row.Status == "queued" || row.Status == "batch_required" || row.Status == "resource_deferred")
		if row.ExpectedReuse {
			totals.PositiveLabels++
		} else {
			totals.NegativeLabels++
		}
		if deferred {
			totals.ResourceDeferred++
			if row.ExpectedReuse {
				totals.DeferredPositiveLabels++
			} else {
				totals.DeferredNegativeLabels++
			}
		}
		if unscored {
			totals.Unscored++
			if row.ExpectedReuse {
				totals.UnscoredPositiveLabels++
			} else {
				totals.UnscoredNegativeLabels++
			}
		} else if row.ExpectedReuse {
			if row.Served {
				totals.TP++
			} else {
				totals.FN++
			}
		} else {
			if row.Served {
				totals.FP++
			} else {
				totals.TN++
			}
		}
		if row.Status == "not_run" {
			totals.Unattempted++
		} else if row.ErrorCategory == "" && !unscored {
			totals.Completed++
		}
		if row.ErrorCategory != "" && !deferred {
			totals.Errors++
		}
		if row.Abstained {
			totals.Abstentions++
		}
		if row.Status == "retrieval_miss" {
			totals.RetrievalMisses++
		}
		if row.Status == "guard_rejected" {
			totals.GuardRejections++
		}
		if row.Verification != nil && row.Verification.Verdict == "unknown" && !deferred {
			totals.UnknownVerdicts++
		}
		countEmbedding(row.EmbeddingAttempted, row.EmbeddingInputTokens)
		if row.VerificationAttempted {
			usage.VerificationAttempts++
		}
		if result := row.Verification; result != nil {
			countVerification(*result)
		}
	}
	ratio := func(n, d int) *float64 {
		if d == 0 {
			return nil
		}
		value := float64(n) / float64(d)
		return &value
	}
	totals.Precision = ratio(totals.TP, totals.TP+totals.FP)
	totals.Recall = ratio(totals.TP, totals.PositiveLabels-totals.UnscoredPositiveLabels)
	totals.Accuracy = ratio(totals.TP+totals.TN, totals.Total-totals.Unscored)
	if totals.Unscored > 0 {
		n := totals.Total - totals.Unscored
		totals.ConfusionPairs = &n
	}
	totals.Coverage = ratio(totals.TP+totals.FP, totals.Total)
	totals.PrecisionWilson95Lower, totals.PrecisionWilson95Upper = semanticEvalWilson95(totals.TP, totals.TP+totals.FP)
	if len(usage.VerifierModels) > 1 {
		usage.MixedVerifierModelUsage = true
		usage.VerifierTokenAccounting = "reservation_accounting_sum_of_separately_metered_model_units"
	}
	usage.LogicalModelCalls = usage.EmbeddingCalls + usage.VerifierGenerationCalls
	return totals, usage
}

func semanticEvalWilson95(successes, total int) (*float64, *float64) {
	if total <= 0 || successes < 0 || successes > total {
		return nil, nil
	}
	const z = 1.959963984540054
	n := float64(total)
	p := float64(successes) / n
	denominator := 1 + z*z/n
	center := (p + z*z/(2*n)) / denominator
	half := z * math.Sqrt(p*(1-p)/n+z*z/(4*n*n)) / denominator
	lower, upper := math.Max(0, center-half), math.Min(1, center+half)
	return &lower, &upper
}

// A diagnostic binds synthetic fixed source data without pretending it was generated.
func semanticEvalValiditySource(r semantic.ValidityRequest) *semantic.ValiditySource {
	r.Question = ""
	raw, _ := json.Marshal(r)
	h := sha256.Sum256(raw)
	output := sha256.Sum256([]byte(r.CachedAnswer))
	digest := hex.EncodeToString(h[:])
	return &semantic.ValiditySource{Key: "inference/v1/jobs/" + digest, Version: 1, SHA256: digest, OutputSHA256: hex.EncodeToString(output[:]), RequestFingerprint: digest}
}
