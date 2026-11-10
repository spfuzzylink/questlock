// A bounded local measurement. The example inputs are not a production allowlist.
package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/semantic"
)

//go:embed cases.json
var casesJSON []byte

type businessCase struct {
	ID               string                  `json:"id"`
	Instructions     string                  `json:"instructions"`
	Context          []inference.ContextItem `json:"context"`
	OutputTypes      map[string]string       `json:"output_types"`
	Checks           []evaluation.Check      `json:"checks"`
	OriginalQuestion string                  `json:"original_question"`
	Paraphrases      []string                `json:"paraphrases"`
	Intent           string                  `json:"intent"`
	Bindings         map[string]string       `json:"bindings"`
}
type caseSet struct {
	Schema   int            `json:"schema"`
	Revision string         `json:"revision"`
	Cases    []businessCase `json:"cases"`
}
type row struct {
	CaseID      string             `json:"case_id"`
	Phase       string             `json:"phase"`
	Input       string             `json:"input"`
	Withheld    string             `json:"withheld,omitempty"`
	Error       string             `json:"error,omitempty"`
	Result      *delegation.Result `json:"result,omitempty"`
	ModelOutput string             `json:"model_output,omitempty"`
	ElapsedMS   *float64           `json:"elapsed_ms,omitempty"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, out io.Writer) error {
	f := flag.NewFlagSet("paraphrase-reuse", flag.ContinueOnError)
	f.SetOutput(out)
	live := f.Bool("live", false, "explicit local generation, semantic reuse and exact replay")
	baseline := f.Bool("fresh-baseline", false, "also run twelve fresh quality-matched requests")
	repeatBaseline := f.Bool("fresh-repeat-baseline", false, "also measure twelve fresh repeat baselines; requires --fresh-baseline")
	brokerURL := f.String("broker", "", "explicit literal-loopback broker origin")
	ollamaURL := f.String("ollama", "", "explicit literal-loopback installed Ollama runtime")
	model := f.String("model", "qwen3.5:9b", "explicit installed local answer model; no weight downloads")
	verifierModel := f.String("verifier-model", "qwen3.5:9b", "explicit installed local verifier model")
	verifierPairModel := f.String("verifier-pair-model", "", "optional installed applicability model; empty uses the source verifier model")
	verifierInput := f.Int64("verifier-max-input-tokens", 16384, "total verifier input reservation cap")
	verifierSourceInput := f.Int64("verifier-source-max-input-tokens", 0, "optional source input cap, at most half the total verifier input cap")
	verifierPairInput := f.Int64("verifier-pair-max-input-tokens", 0, "optional applicability input cap, at most half the total verifier input cap")
	verifierSourceThink := f.Bool("verifier-source-think", false, "explicit bounded source reasoning, independent of applicability reasoning")
	verifierPairThink := f.Bool("verifier-pair-think", false, "explicit bounded applicability reasoning; source reasoning is independent")
	verifierPairAssessment := f.Bool("verifier-pair-assessment", false, "bounded pair assessment before decisions; cannot combine with pair thinking")
	verifierPairTemperature := f.Float64("verifier-pair-temperature", 0, "optional applicability temperature in 0..2; absent keeps zero")
	verifierPairSeed := f.Int64("verifier-pair-seed", 0, "optional applicability seed in 0..2147483647; absent leaves runtime default")
	verifierOutput := f.Int64("verifier-max-output-tokens", 128, "total verifier output cap; either reasoning flag without this option selects its policy default")
	verifierTimeout := f.Int("verifier-timeout-ms", 60000, "shared source+pair deadline in ms; thinking defaults to 120000 and permits an explicit value up to 600000, otherwise max 120000; earlier caller/resolver deadline wins")
	minSimilarity := f.Float64("min-similarity", 0.25, "retrieval cutoff in 0..1; zero selects the semantic policy default")
	casesPath := f.String("cases-file", "", "optional absolute private six-case JSON snapshot; also selects cases for --report")
	maxDuration := f.Duration("max-duration", 30*time.Minute, "explicit whole-run deadline, positive and at most 2h")
	dailyInput := f.Int64("daily-input-tokens", 524288, "scope/day input token cap, positive and at most 1e12")
	dailyOutput := f.Int64("daily-output-tokens", 16384, "scope/day output token cap, positive and at most 1e12")
	dailyTotal := f.Int64("daily-total-tokens", 540672, "scope/day joint token cap, positive and at most 1e12")
	answerOutput := f.Int64("answer-max-output-tokens", 256, "same answer output ceiling for source and fresh controls, 1..4096")
	resourcePolicyPath := f.String("resource-policy", "", "optional runtime-only JSON admission policy; bounded wait before any fresh claim")
	tokenPath := f.String("token-file", "", "absolute private scoped token file")
	statePath := f.String("state", "", "new absolute private progress file")
	outputPath := f.String("output", "", "new absolute private JSONL report")
	reportPath := f.String("report", "", "offline summary of an absolute private measurement JSONL file")
	if e := f.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	outputExplicit, timeoutExplicit, verifierOptionsExplicit := false, false, false
	f.Visit(func(v *flag.Flag) {
		switch v.Name {
		case "max-duration", "daily-input-tokens", "daily-output-tokens", "daily-total-tokens", "answer-max-output-tokens":
			verifierOptionsExplicit = true
		case "verifier-max-output-tokens":
			outputExplicit, verifierOptionsExplicit = true, true
		case "verifier-timeout-ms":
			timeoutExplicit, verifierOptionsExplicit = true, true
		case "verifier-pair-temperature", "verifier-pair-seed", "verifier-source-think", "verifier-pair-think", "verifier-pair-assessment", "min-similarity", "verifier-pair-model", "verifier-pair-max-input-tokens", "verifier-max-input-tokens", "verifier-source-max-input-tokens":
			verifierOptionsExplicit = true
		}
	})
	if *reportPath != "" {
		if f.NArg() != 0 || *live || *baseline || *repeatBaseline || *brokerURL != "" || *ollamaURL != "" || *tokenPath != "" || *statePath != "" || *outputPath != "" || *resourcePolicyPath != "" || verifierOptionsExplicit {
			return errors.New("--report must be used without live-run options")
		}
		data, _, err := loadMeasurementCases(*casesPath)
		if err != nil {
			return err
		}
		return reportFileWithCases(*reportPath, data, out)
	}
	if *repeatBaseline && !*baseline {
		return errors.New("--fresh-repeat-baseline requires --fresh-baseline")
	}
	if !*live {
		fmt.Fprintln(out, "No model or file access. Review source, facts and checks, then supply --live and explicit local endpoints.")
		f.PrintDefaults()
		return nil
	}
	if f.NArg() != 0 || !loopbackOrigin(*brokerURL) || !loopbackOrigin(*ollamaURL) {
		return errors.New("explicit literal-loopback endpoints required")
	}
	budget := &inference.TokenBudget{InputTokens: *dailyInput, OutputTokens: *dailyOutput, TotalTokens: *dailyTotal}
	if !validMeasurementBudget(*budget) || *maxDuration < time.Millisecond || *maxDuration > 2*time.Hour || *maxDuration%time.Millisecond != 0 || *answerOutput < 1 || *answerOutput > 4096 {
		return errors.New("invalid measurement budget, duration or answer output ceiling")
	}
	outputLimit := *verifierOutput
	if (*verifierSourceThink || *verifierPairThink || *verifierPairAssessment) && !outputExplicit {
		outputLimit = 0 // Explicit reasoning selects its separately bounded policy default.
	}
	timeoutLimit := *verifierTimeout
	if (*verifierSourceThink || *verifierPairThink) && !timeoutExplicit {
		timeoutLimit = 0
	}
	verifierConfig, e := measurementVerifierConfigWithAssessment(*verifierModel, *verifierSourceThink, *verifierPairThink, *verifierPairAssessment, outputLimit, timeoutLimit, *minSimilarity)
	if e != nil {
		return e
	}
	if *verifierInput < 1 || *verifierSourceInput < 0 {
		return errors.New("invalid bounded semantic verifier options")
	}
	verifierConfig.VerifierMaxInputTokens = *verifierInput
	verifierConfig.VerifierSourceMaxInputTokens = *verifierSourceInput
	verifierConfig.VerifierPairModel = *verifierPairModel
	verifierConfig.VerifierPairMaxInputTokens = *verifierPairInput
	f.Visit(func(v *flag.Flag) {
		switch v.Name {
		case "verifier-pair-temperature":
			verifierConfig.VerifierPairTemperature = verifierPairTemperature
		case "verifier-pair-seed":
			verifierConfig.VerifierPairSeed = verifierPairSeed
		}
	})
	verifierConfig, e = semantic.NormalizeReuseConfig(verifierConfig)
	if e != nil {
		return e
	}
	var resourcePolicy *delegation.ReuseAdmissionPolicy
	if *resourcePolicyPath != "" {
		file, err := os.Open(*resourcePolicyPath)
		if err != nil {
			return delegation.ErrResourceAdmissionConfig
		}
		policy, err := delegation.LoadReuseAdmissionPolicy(file)
		file.Close()
		if err != nil {
			return err
		}
		resourcePolicy = &policy
	}
	caseData, set, e := loadMeasurementCases(*casesPath)
	if e != nil {
		return e
	}
	tokenFile, e := privateFile(*tokenPath, false)
	if e != nil {
		return e
	}
	defer tokenFile.Close()
	token, e := io.ReadAll(io.LimitReader(tokenFile, 4097))
	if e != nil || len(token) == 0 || len(token) > 4096 {
		return errors.New("invalid scoped token")
	}
	broker, e := client.New(*brokerURL, strings.TrimSpace(string(token)))
	if e != nil {
		return e
	}
	state, e := privateFile(*statePath, true)
	if e != nil {
		return e
	}
	defer state.Close()
	report, e := privateFile(*outputPath, true)
	if e != nil {
		return e
	}
	defer report.Close()
	h := sha256.Sum256(caseData)
	header := map[string]any{"schema": "questlock.paraphrase-measurement.v1", "cases_sha256": hex.EncodeToString(h[:]), "model": *model, "verifier_model": *verifierModel, "verifier_policy": "factorized-v1", "min_similarity": verifierConfig.MinSimilarity, "daily_budget": budget, "fresh_baseline": *baseline, "paid_fallback_enabled": false, "provider_api_charge_usd": 0}
	if *maxDuration != 30*time.Minute {
		header["max_duration_ms"] = maxDuration.Milliseconds()
	}
	if *answerOutput != 256 {
		header["answer_max_output_tokens"] = *answerOutput
	}
	if verifierConfig.VerifierMaxInputTokens != 16384 {
		header["verifier_max_input_tokens"] = verifierConfig.VerifierMaxInputTokens
	}
	if verifierConfig.VerifierSourceMaxInputTokens != 0 {
		header["verifier_source_max_input_tokens"] = verifierConfig.VerifierSourceMaxInputTokens
	}
	if verifierConfig.VerifierPairModel != "" {
		header["verifier_pair_model"] = verifierConfig.VerifierPairModel
	}
	if verifierConfig.VerifierPairMaxInputTokens != 0 {
		header["verifier_pair_max_input_tokens"] = verifierConfig.VerifierPairMaxInputTokens
	}
	if resourcePolicy != nil {
		header["resource_policy"] = resourcePolicy
	}
	if verifierConfig.VerifierPairTemperature != nil {
		header["verifier_pair_temperature"] = *verifierConfig.VerifierPairTemperature
	}
	if verifierConfig.VerifierPairSeed != nil {
		header["verifier_pair_seed"] = *verifierConfig.VerifierPairSeed
	}
	if verifierConfig.VerifierSourceThink {
		header["verifier_source_think"] = true
	}
	if verifierConfig.VerifierPairThink {
		header["verifier_pair_think"] = true
	}
	if verifierConfig.VerifierPairAssessment {
		header["verifier_pair_assessment"] = true
	}
	if verifierConfig.VerifierSourceThink || verifierConfig.VerifierPairThink {
		header["semantic_timeout_ms"] = verifierConfig.TimeoutMS
	}
	if verifierConfig.VerifierSourceThink || verifierConfig.VerifierPairThink || verifierConfig.VerifierMaxOutputTokens != 128 {
		header["verifier_max_output_tokens"] = verifierConfig.VerifierMaxOutputTokens
	}
	if verifierConfig.VerifierTimeoutMS != 60000 {
		header["verifier_timeout_ms"] = verifierConfig.VerifierTimeoutMS
	}
	if *repeatBaseline {
		header["fresh_repeat_baseline"] = true
	}
	if e = appendJSON(report, header); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), *maxDuration)
	defer cancel()
	p, e := inference.NewOllamaProviderContext(ctx, *ollamaURL, *model)
	if e != nil {
		return e
	}
	defer p.Close()
	runner := &inference.Runner{Broker: broker, Provider: p}
	reuse := &delegation.ControlledReuser{Broker: broker, Index: broker, Resources: admission.SystemSource(), EmbedderFactory: func(ctx context.Context, c semantic.Config) (semantic.Embedder, error) {
		return semantic.NewOllamaEmbedderContext(ctx, *ollamaURL, c.EmbeddingModel, c.MaxInputBytes)
	}, VerifierFactory: func(ctx context.Context, c semantic.ReuseConfig) (semantic.Verifier, error) {
		v, e := delegation.NewFactorizedVerifier(ctx, broker, *ollamaURL, c)
		if e == nil {
			v.DailyTokenBudget = budget
		}
		return v, e
	}}
	reuse.AdmissionPolicy = resourcePolicy
	// Plain baseline calls use the same runtime policy. Controlled calls use the
	// promoted resolver method and its outer gate, so admission is never nested.
	var measuredRunner delegation.Runner = runner
	if resourcePolicy != nil {
		admit, err := delegation.NewResourceAdmission(*resourcePolicy, reuse.Resources)
		if err != nil {
			return err
		}
		measuredRunner = &admittedMeasurementRunner{Runner: runner, admit: admit}
	}
	router := delegation.Router{Factory: func(delegation.Tier) (delegation.Runner, error) { return measuredRunner, nil }, Reuse: reuse, AllowPaid: false}
	failed := false
	for _, c := range set.Cases {
		sourceOK := false
		fingerprints := [2]string{}
		steps := 5
		if *baseline {
			steps = 7
		}
		if *repeatBaseline {
			steps = 9
		}
		for step := 0; step < steps; step++ {
			phase, request := measurementStepWithAnswerLimit(set.Revision, c, step, budget, *answerOutput)
			input := request.Spec.Input
			request.Policy.Local.Spec.Model = *model
			if request.SemanticReuse != nil {
				request.SemanticReuse.VerifierModel = *verifierModel
				request.SemanticReuse.VerifierMaxInputTokens = verifierConfig.VerifierMaxInputTokens
				request.SemanticReuse.VerifierSourceMaxInputTokens = verifierConfig.VerifierSourceMaxInputTokens
				request.SemanticReuse.VerifierPairModel = verifierConfig.VerifierPairModel
				request.SemanticReuse.VerifierPairMaxInputTokens = verifierConfig.VerifierPairMaxInputTokens
				request.SemanticReuse.VerifierSourceThink = verifierConfig.VerifierSourceThink
				request.SemanticReuse.VerifierPairThink = verifierConfig.VerifierPairThink
				request.SemanticReuse.VerifierPairAssessment = verifierConfig.VerifierPairAssessment
				request.SemanticReuse.VerifierPairTemperature = verifierConfig.VerifierPairTemperature
				request.SemanticReuse.VerifierPairSeed = verifierConfig.VerifierPairSeed
				request.SemanticReuse.VerifierMaxOutputTokens = verifierConfig.VerifierMaxOutputTokens
				request.SemanticReuse.VerifierTimeoutMS = verifierConfig.VerifierTimeoutMS
				request.SemanticReuse.MinSimilarity = verifierConfig.MinSimilarity
			}
			r := row{CaseID: c.ID, Phase: phase, Input: input}
			if (phase == "paraphrase" || phase == "exact") && !sourceOK {
				r.Withheld = "source_failed_checks"
				if e = appendJSON(report, r); e != nil {
					return e
				}
				continue
			}
			if e = appendJSON(state, map[string]any{"case_id": c.ID, "phase": phase, "status": "started", "task": request.Spec.Task, "revision": request.Spec.Revision}); e != nil {
				return e
			}
			id, idErr := delegation.NewOutcomeRunID()
			if idErr != nil {
				return errors.New("outcome identity unavailable; generation was not started")
			}
			started := time.Now()
			result, runErr := router.Run(ctx, request)
			deferred := len(result.Attempts) == 1 && inference.PreClaimDeferred(result.Attempts[0].Result, runErr)
			r.Result = &result
			delegation.RecordOutcome(ctx, broker, request, &result, id)
			elapsed := float64(time.Since(started)) / float64(time.Millisecond)
			r.ElapsedMS = &elapsed
			if runErr != nil {
				if deferred {
					r.Error = "resource_deferred"
				} else {
					r.Error = runErr.Error()
					failed = true
				}
			}
			if result.OutcomeTracking == nil || result.OutcomeTracking.Status != "recorded" {
				r.Error = "outcome_accounting_unconfirmed"
				failed = true
			}
			if len(result.Attempts) == 1 {
				a := result.Attempts[0].Result
				fresh := !a.Reused && a.Durable && a.Response.Complete && a.Measurement != nil && a.Measurement.GenerationCalled && a.Measurement.KnownGenerationUsage
				if phase == "seed" {
					sourceOK = runErr == nil && result.Status == "completed" && fresh
				}
				if a.Durable && !a.Reused {
					art, err := broker.Get(ctx, "inference/v1/jobs/"+a.Fingerprint)
					var saved struct {
						Response *inference.Response `json:"response"`
					}
					if err == nil && json.Unmarshal([]byte(art.Content), &saved) == nil && saved.Response != nil {
						r.ModelOutput = saved.Response.Text
					} else {
						r.Error = "source_output_unavailable"
						failed = true
					}
				}
				if phase == "paraphrase" && !deferred {
					fingerprints[step-1] = a.Fingerprint
					if !a.Reused || a.ReuseKind != "semantic" || a.ThisRunUsage != (inference.Usage{}) {
						r.Error = "semantic_reuse_miss_or_fresh_generation"
						failed = true
					}
				}
				if phase == "exact" && !deferred {
					if !a.Reused || a.Fingerprint != fingerprints[step-3] || a.ThisRunUsage != (inference.Usage{}) || a.Measurement == nil || a.Measurement.GenerationCalled || result.SemanticReuse != nil && (len(result.SemanticReuse.Verifications) != 0 || result.SemanticReuse.EmbeddingInputTokens != nil) {
						r.Error = "exact_replay_performed_new_work"
						failed = true
					}
				}
				if (phase == "seed" || phase == "baseline" || phase == "baseline_repeat") && runErr == nil && !fresh {
					r.Error = "fresh_scope_required"
					failed = true
				}
			} else {
				r.Error = "unexpected_attempt_count"
				failed = true
			}
			if e = appendJSON(report, r); e != nil {
				return e
			}
			if e = appendJSON(state, map[string]any{"case_id": c.ID, "phase": phase, "status": result.Status, "error": r.Error}); e != nil {
				return e
			}
			if deferred {
				return errors.New("resource capacity deferred before a fresh claim; partial measurement retained without a quality verdict or generation retry")
			}
			if runErr != nil && !errors.Is(runErr, delegation.ErrQualityFailed) || r.Error == "outcome_accounting_unconfirmed" {
				return errors.New("unconfirmed work stopped the run; inspect receipts without retrying generation")
			}
		}
	}
	if e = appendJSON(report, map[string]any{"phase": "complete", "known_quality_or_reuse_failures": failed}); e != nil {
		return e
	}
	if failed {
		return errors.New("known failures retained; do not select only successful cases")
	}
	return nil
}

// Validate measurement options before opening credentials or constructing a
// provider. The real requests retain their case-specific evidence and checks.
func measurementVerifierConfig(model string, think, pairThink bool, output int64, timeout int, similarity float64) (semantic.ReuseConfig, error) {
	return measurementVerifierConfigWithAssessment(model, think, pairThink, false, output, timeout, similarity)
}

func measurementVerifierConfigWithAssessment(model string, think, pairThink, assessment bool, output int64, timeout int, similarity float64) (semantic.ReuseConfig, error) {
	c, err := semantic.NormalizeReuseConfig(semantic.ReuseConfig{Config: semantic.Config{Mode: "controlled", Family: "business-measurement-options", Domain: "synthetic-business-measurement", EvidenceRevision: "options-1", EmbeddingModel: "all-minilm", MinSimilarity: similarity}, ReadOnly: true, Intent: "validate-measurement-options", VerifierModel: model, VerifierPolicy: "factorized-v1", VerifierSourceThink: think, VerifierPairThink: pairThink, VerifierPairAssessment: assessment, VerifierMaxInputTokens: 16384, VerifierMaxOutputTokens: output, VerifierTimeoutMS: timeout})
	if err != nil {
		return semantic.ReuseConfig{}, errors.New("invalid bounded semantic verifier options")
	}
	return c, nil
}
func measurementStepWithAnswerLimit(revision string, c businessCase, step int, budget *inference.TokenBudget, output int64) (string, delegation.Request) {
	phase, request := measurementStep(revision, c, step, budget)
	request.Policy.Local.Spec.MaxOutputTokens = output
	return phase, request
}

func measurementStep(revision string, c businessCase, step int, budget *inference.TokenBudget) (string, delegation.Request) {
	phase, input := "seed", c.OriginalQuestion
	if step > 0 && step < 3 {
		phase, input = "paraphrase", c.Paraphrases[step-1]
	}
	if step >= 3 && step < 5 {
		phase, input = "exact", c.Paraphrases[step-3]
	}
	if step >= 5 {
		phase, input = "baseline", c.Paraphrases[(step-5)%2]
	}
	if step >= 7 {
		phase = "baseline_repeat"
	}
	r := requestFor(revision, c, input, budget)
	if step >= 5 {
		r.SemanticReuse = nil
		r.Spec.Task = fmt.Sprintf("semantic-baseline-%s-%d", c.ID, step-5)
	}
	return phase, r
}
func requestFor(revision string, c businessCase, input string, budget *inference.TokenBudget) delegation.Request {
	zero, timeout, think, temp := int64(0), int64(90), false, float64(0)
	reuse := &semantic.ReuseConfig{Config: semantic.Config{Mode: "controlled", Family: c.ID, Domain: "synthetic-business-measurement", EvidenceRevision: revision, EmbeddingModel: "all-minilm", MinSimilarity: 0.25, Bindings: c.Bindings}, ReadOnly: true, Intent: c.Intent, VerifierModel: "qwen3.5:9b", VerifierPolicy: "factorized-v1", VerifierMaxInputTokens: 16384, VerifierMaxOutputTokens: 128, VerifierTimeoutMS: 60000}
	return delegation.Request{TrackOutcome: true, Spec: inference.Spec{Task: "semantic-source-" + c.ID, Revision: revision + "-" + c.ID, Instructions: c.Instructions, Input: input, Context: c.Context}, Policy: delegation.Policy{Local: delegation.Tier{ID: "local", Spec: inference.Spec{Provider: "ollama", Model: "qwen3.5:9b", MaxContextBytes: 16 << 10, MaxInputTokens: 8192, MaxOutputTokens: 256, ReuseForSeconds: 3600, DailyTokenBudget: budget, Ollama: &inference.OllamaOptions{Format: "json", Think: &think, Temperature: &temp, KeepAliveSeconds: &zero, GenerationTimeoutSeconds: &timeout, OutputTypes: c.OutputTypes}}}}, Checks: c.Checks, SemanticReuse: reuse}
}

// Run overrides only the plain answer path; resolver methods remain promoted.
type admittedMeasurementRunner struct {
	*inference.Runner
	admit inference.AdmissionFunc
}

func (r *admittedMeasurementRunner) Run(ctx context.Context, spec inference.Spec) (inference.Result, error) {
	return r.Runner.RunWithAdmission(ctx, spec, r.admit)
}
