package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

type semanticEvalUsage struct {
	EmbeddingCalls             int   `json:"embedding_calls"`
	EmbeddingKnownUsageCalls   int   `json:"embedding_known_usage_calls"`
	EmbeddingUnknownUsageCalls int   `json:"embedding_unknown_usage_calls"`
	EmbeddingInputTokens       int64 `json:"embedding_input_tokens"`
	VerificationAttempts       int   `json:"verification_attempts"`
	VerifierGenerationCalls    int   `json:"verifier_generation_calls"`
	CachedVerifications        int   `json:"cached_verifications"`
	VerifierKnownUsageCalls    int   `json:"verifier_known_usage_calls"`
	VerifierUnknownUsageCalls  int   `json:"verifier_unknown_usage_calls"`
	VerifierInputTokens        int64 `json:"verifier_input_tokens"`
	VerifierOutputTokens       int64 `json:"verifier_output_tokens"`
	LogicalModelCalls          int   `json:"logical_model_calls"`
}

type semanticEvalReport struct {
	BatchSize                 int                           `json:"batch_size,omitempty"`
	BatchVerificationWork     []semantic.ValidityResult     `json:"batch_verification_work,omitempty"`
	Schema                    int                           `json:"schema"`
	ProtectedTermsVersion     int                           `json:"protected_terms_version"`
	Mode                      string                        `json:"mode"`
	Status                    string                        `json:"status"`
	SourceSHA256              string                        `json:"source_sha256"`
	FrozenCorpusSHA256        string                        `json:"frozen_corpus_sha256"`
	MatchesFrozenCorpus       bool                          `json:"matches_frozen_corpus"`
	Population                string                        `json:"population"`
	LabelRule                 string                        `json:"label_rule"`
	EmbeddingModel            string                        `json:"embedding_model"`
	VerifierModel             string                        `json:"verifier_model"`
	VerifierIdentity          string                        `json:"verifier_identity,omitempty"`
	MinSimilarity             float64                       `json:"min_similarity"`
	RetrievalThresholdMeaning string                        `json:"retrieval_threshold_meaning"`
	QualityTargetMeaning      string                        `json:"quality_target_meaning"`
	MetricMeaning             string                        `json:"metric_meaning"`
	ModelCallBasis            string                        `json:"model_call_basis"`
	SourceAnswers             string                        `json:"source_answers"`
	AnswerGenerationCalls     int                           `json:"answer_generation_calls"`
	SavingsMeasured           bool                          `json:"savings_measured"`
	PhysicalHosts             int                           `json:"physical_hosts"`
	ProviderAPIChargeUSD      int                           `json:"provider_api_charge_usd"`
	PaidFallbackEnabled       bool                          `json:"paid_fallback_enabled"`
	Errors                    []string                      `json:"errors"`
	SourceEmbeddings          []semanticEvalSourceEmbedding `json:"source_embeddings"`
	Pairs                     []semanticEvalPair            `json:"pairs"`
	Totals                    semanticEvalTotals            `json:"totals"`
	Usage                     semanticEvalUsage             `json:"usage"`
	TotalMS                   float64                       `json:"total_ms"`
}

func semanticEvaluateCommand(args []string, output, diagnostics io.Writer) (runErr error) {
	started := time.Now()
	report := semanticEvalReport{Schema: 1, ProtectedTermsVersion: semantic.ProtectedTermsVersion, Mode: "local_semantic_validity_diagnostic", Status: "partial_failed", FrozenCorpusSHA256: semanticEvalFrozenSHA256, PhysicalHosts: 1, SourceAnswers: "fixed_labelled_fixtures", RetrievalThresholdMeaning: "Cosine retrieval cutoff only; it is not a validity confidence or quality estimate.", QualityTargetMeaning: "The requested 90–98% target is not a universal guarantee. Metrics and Wilson intervals describe this declared sample only; corpus selection and dependence limit generalization.", MetricMeaning: "TP/FP/TN/FN classify served versus withheld for every labelled pair, including abstentions. Accuracy counts correct serve/withhold decisions; coverage is served/total. Errors, unknowns and unattempted pairs remain visible. Precision is null when nothing is served.", ModelCallBasis: "Logical embedding Embed calls plus verifier Generate calls reported by the inference runner; metadata reads and token-count preflight requests are excluded. Verification attempts, cached verifications and unknown usage are separate.", Errors: []string{}, SourceEmbeddings: []semanticEvalSourceEmbedding{}, Pairs: []semanticEvalPair{}}
	defer func() {
		report.TotalMS = float64(time.Since(started)) / float64(time.Millisecond)
		report.Totals, report.Usage = semanticEvalSummarize(report.Pairs, report.SourceEmbeddings, report.BatchVerificationWork)
		if runErr == nil {
			report.Status = "completed"
		}
		if err := writeModelJSON(output, report); err != nil {
			runErr = errors.Join(runErr, err)
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
	threshold := f.Float64("min-similarity", 0.9, "retrieval cosine cutoff; not validity confidence")
	batchSize := f.Int("batch-size", 1, "1 for standalone validity; 2 for compatible validity pairs")
	timeout := f.Duration("timeout", 20*time.Minute, "positive total evaluation bound, at most 60m")
	f.Usage = func() {
		_, _ = fmt.Fprintln(diagnostics, "Usage: questlock semantic evaluate --live --suite PATH [--embedding-model all-minilm] [--verifier-model qwen3.5:4b] [--min-similarity 0.9] [--batch-size 1|2] [--timeout 20m]\nThis diagnostic uses fixed labelled source answers, local embeddings and durable local validity inference. It measures no answer-generation saving.")
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fail("invalid_flags")
	}
	report.EmbeddingModel, report.VerifierModel = *embeddingModel, *verifierModel
	report.BatchSize = *batchSize
	if !math.IsNaN(*threshold) && !math.IsInf(*threshold, 0) {
		report.MinSimilarity = *threshold
	}
	if f.NArg() != 0 || !*live || *suitePath == "" || *batchSize < 1 || *batchSize > 2 || *timeout <= 0 || *timeout > 60*time.Minute || math.IsNaN(*threshold) || math.IsInf(*threshold, 0) || *threshold < 0 || *threshold > 1 {
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
	config, err := semantic.NormalizeReuseConfig(semantic.ReuseConfig{Config: semantic.Config{Mode: "controlled", Family: "controlled-evaluation", Domain: "synthetic-challenge", EvidenceRevision: report.SourceSHA256, EmbeddingModel: *embeddingModel, MinSimilarity: *threshold, MaxInputBytes: semantic.MaxInputBytes}, ReadOnly: true, Intent: "answer-validity-evaluation", VerifierModel: *verifierModel})
	if err != nil {
		return fail("invalid_local_model_policy")
	}
	// The embedder validates the literal-loopback origin before discovery. It
	// never pulls weights, follows redirects, or uses environment proxies.
	embedder, err := semantic.NewOllamaEmbedderContext(ctx, *base, *embeddingModel, config.MaxInputBytes)
	if err != nil {
		return fail("embedding_setup_failed")
	}
	defer embedder.Close()
	broker, closeBroker, err := newSemanticEvalBroker(ctx)
	if err != nil {
		return fail("temporary_broker_failed")
	}
	defer closeBroker()
	var verifier interface {
		Identity() string
		Close()
	}
	if *batchSize == 2 {
		verifier, err = validityeval.NewPairVerifier(ctx, broker, *base, config)
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
		if pair.ErrorCategory != "" {
			return fail("pair_execution_errors")
		}
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

func newSemanticEvalBroker(ctx context.Context) (inference.Broker, func(), error) {
	dir, err := os.MkdirTemp("", "questlock-semantic-evaluation-")
	if err != nil {
		return nil, nil, err
	}
	remove := func() { _ = os.RemoveAll(dir) }
	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		remove()
		return nil, nil, err
	}
	closeStore := func() { _ = s.Close(); remove() }
	token, err := s.CreateToken(ctx, "semantic-evaluator", "semantic-evaluation")
	if err != nil {
		closeStore()
		return nil, nil, err
	}
	server := httptest.NewServer(httpapi.New(s))
	closeAll := func() { server.Close(); closeStore() }
	broker, err := client.New(server.URL, token)
	if err != nil {
		closeAll()
		return nil, nil, err
	}
	return broker, closeAll, nil
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
		for _, variant := range c.Variants {
			row := &report.Pairs[index]
			index++
			if ctx.Err() != nil {
				row.ErrorCategory = semanticEvalContextCategory(ctx)
				continue
			}
			started := time.Now()
			row.EmbeddingAttempted = true
			embedding, err := embedder.Embed(ctx, variant.Question)
			row.EmbeddingMS = float64(time.Since(started)) / float64(time.Millisecond)
			row.EmbeddingInputTokens, row.EmbeddingModelID = embedding.InputTokens, embedding.ModelID
			row.GuardCodes = semantic.CheckProtectedTerms(c.OriginalQuestion, variant.Question)
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
						if verifyErr != nil {
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
		}
		if report.BatchSize > 1 {
			semanticEvalRunBatches(ctx, verifier, config, report, batchRequests, batchRows)
		}
	}
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
	}
}

func semanticEvalVerified(result semantic.ValidityResult, config semantic.ReuseConfig) bool {
	if !result.Complete || !result.Durable || result.ReceiptKey == "" || result.InputTokens == nil || result.OutputTokens == nil || *result.InputTokens < 0 || *result.OutputTokens < 0 || *result.InputTokens > config.VerifierMaxInputTokens || *result.OutputTokens > config.VerifierMaxOutputTokens {
		return false
	}
	if result.Reused {
		return !result.GenerationCalled && *result.InputTokens == 0 && *result.OutputTokens == 0
	}
	return result.GenerationCalled && *result.InputTokens > 0
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
	countVerification := func(result semantic.ValidityResult) {
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
		if row.ExpectedReuse {
			totals.PositiveLabels++
			if row.Served {
				totals.TP++
			} else {
				totals.FN++
			}
		} else {
			totals.NegativeLabels++
			if row.Served {
				totals.FP++
			} else {
				totals.TN++
			}
		}
		if row.Status == "not_run" {
			totals.Unattempted++
		} else if row.ErrorCategory == "" {
			totals.Completed++
		}
		if row.ErrorCategory != "" {
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
		if row.Verification != nil && row.Verification.Verdict == "unknown" {
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
	totals.Recall = ratio(totals.TP, totals.PositiveLabels)
	totals.Accuracy = ratio(totals.TP+totals.TN, totals.Total)
	totals.Coverage = ratio(totals.TP+totals.FP, totals.Total)
	totals.PrecisionWilson95Lower, totals.PrecisionWilson95Upper = semanticEvalWilson95(totals.TP, totals.TP+totals.FP)
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
