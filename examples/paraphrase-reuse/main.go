// A bounded local measurement. The example inputs are not a production allowlist.
package main

import (
	"bytes"
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
	brokerURL := f.String("broker", "", "explicit literal-loopback broker origin")
	ollamaURL := f.String("ollama", "", "explicit literal-loopback installed Ollama runtime")
	model := f.String("model", "qwen3.5:9b", "explicit installed local answer model; no weight downloads")
	verifierModel := f.String("verifier-model", "qwen3.5:9b", "explicit installed local verifier model")
	tokenPath := f.String("token-file", "", "absolute private scoped token file")
	statePath := f.String("state", "", "new absolute private progress file")
	outputPath := f.String("output", "", "new absolute private JSONL report")
	if e := f.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if !*live {
		fmt.Fprintln(out, "No model or file access. Review source, facts and checks, then supply --live and explicit local endpoints.")
		f.PrintDefaults()
		return nil
	}
	if f.NArg() != 0 || !loopbackOrigin(*brokerURL) || !loopbackOrigin(*ollamaURL) {
		return errors.New("explicit literal-loopback endpoints required")
	}
	var set caseSet
	d := json.NewDecoder(bytes.NewReader(casesJSON))
	d.DisallowUnknownFields()
	if d.Decode(&set) != nil || set.Schema != 1 || len(set.Cases) != 6 {
		return errors.New("invalid frozen six-case snapshot")
	}
	for _, c := range set.Cases {
		if c.ID == "" || len(c.Paraphrases) != 2 || len(c.Checks) != 2 || c.Checks[1].Kind != "json_equals" {
			return errors.New("invalid frozen business checks")
		}
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
	budget := &inference.TokenBudget{InputTokens: 524288, OutputTokens: 16384, TotalTokens: 540672}
	h := sha256.Sum256(casesJSON)
	if e = appendJSON(report, map[string]any{"schema": "questlock.paraphrase-measurement.v1", "cases_sha256": hex.EncodeToString(h[:]), "model": *model, "verifier_model": *verifierModel, "verifier_policy": "factorized-v1", "min_similarity": 0.25, "daily_budget": budget, "fresh_baseline": *baseline, "paid_fallback_enabled": false, "provider_api_charge_usd": 0}); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
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
	router := delegation.Router{Factory: func(delegation.Tier) (delegation.Runner, error) { return runner, nil }, Reuse: reuse, AllowPaid: false}
	failed := false
	for _, c := range set.Cases {
		sourceOK := false
		fingerprints := [2]string{}
		for step := 0; step < 7; step++ {
			phase, input := "seed", c.OriginalQuestion
			if step > 0 && step < 3 {
				phase, input = "paraphrase", c.Paraphrases[step-1]
			}
			if step >= 3 && step < 5 {
				phase, input = "exact", c.Paraphrases[step-3]
			}
			if step >= 5 {
				if !*baseline {
					break
				}
				phase, input = "baseline", c.Paraphrases[step-5]
			}
			request := requestFor(set.Revision, c, input, budget)
			request.Policy.Local.Spec.Model = *model
			request.SemanticReuse.VerifierModel = *verifierModel
			if phase == "baseline" {
				request.SemanticReuse = nil
				request.Spec.Task = fmt.Sprintf("semantic-baseline-%s-%d", c.ID, step-5)
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
			result, runErr := router.Run(ctx, request)
			r.Result = &result
			delegation.RecordOutcome(ctx, broker, request, &result, id)
			if runErr != nil {
				r.Error = runErr.Error()
				failed = true
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
				if phase == "paraphrase" {
					fingerprints[step-1] = a.Fingerprint
					if !a.Reused || a.ReuseKind != "semantic" || a.ThisRunUsage != (inference.Usage{}) {
						r.Error = "semantic_reuse_miss_or_fresh_generation"
						failed = true
					}
				}
				if phase == "exact" {
					if !a.Reused || a.Fingerprint != fingerprints[step-3] || a.ThisRunUsage != (inference.Usage{}) || a.Measurement == nil || a.Measurement.GenerationCalled || result.SemanticReuse != nil && (len(result.SemanticReuse.Verifications) != 0 || result.SemanticReuse.EmbeddingInputTokens != nil) {
						r.Error = "exact_replay_performed_new_work"
						failed = true
					}
				}
				if (phase == "seed" || phase == "baseline") && runErr == nil && !fresh {
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
func requestFor(revision string, c businessCase, input string, budget *inference.TokenBudget) delegation.Request {
	zero, timeout, think, temp := int64(0), int64(90), false, float64(0)
	reuse := &semantic.ReuseConfig{Config: semantic.Config{Mode: "controlled", Family: c.ID, Domain: "synthetic-business-measurement", EvidenceRevision: revision, EmbeddingModel: "all-minilm", MinSimilarity: 0.25, Bindings: c.Bindings}, ReadOnly: true, Intent: c.Intent, VerifierModel: "qwen3.5:9b", VerifierPolicy: "factorized-v1", VerifierMaxInputTokens: 16384, VerifierMaxOutputTokens: 128, VerifierTimeoutMS: 60000}
	return delegation.Request{TrackOutcome: true, Spec: inference.Spec{Task: "semantic-source-" + c.ID, Revision: revision + "-" + c.ID, Instructions: c.Instructions, Input: input, Context: c.Context}, Policy: delegation.Policy{Local: delegation.Tier{ID: "local", Spec: inference.Spec{Provider: "ollama", Model: "qwen3.5:9b", MaxContextBytes: 16 << 10, MaxInputTokens: 8192, MaxOutputTokens: 256, ReuseForSeconds: 3600, DailyTokenBudget: budget, Ollama: &inference.OllamaOptions{Format: "json", Think: &think, Temperature: &temp, KeepAliveSeconds: &zero, GenerationTimeoutSeconds: &timeout, OutputTypes: c.OutputTypes}}}}, Checks: c.Checks, SemanticReuse: reuse}
}
