// Explicit local-model measurement of caller-approved question forms. See README.md.
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
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
)

//go:embed cases.json
var caseJSON []byte

type businessCase struct {
	ID           string                         `json:"id"`
	Approved     delegation.ApprovedQuestionSet `json:"approved_questions"`
	Instructions string                         `json:"instructions"`
	Context      []inference.ContextItem        `json:"context"`
	OutputTypes  map[string]string              `json:"output_types"`
	Checks       []evaluation.Check             `json:"checks"`
}
type caseSet struct {
	Schema   int            `json:"schema"`
	Revision string         `json:"revision"`
	Cases    []businessCase `json:"cases"`
}
type phaseTotals struct {
	Calls        int   `json:"calls"`
	Generated    int   `json:"generation_calls"`
	Passed       int   `json:"quality_passed"`
	Withheld     int   `json:"withheld"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	UsageUnknown bool  `json:"usage_unknown"`
}
type reportRow struct {
	CaseID      string                              `json:"case_id"`
	Phase       string                              `json:"phase"`
	Input       string                              `json:"input"`
	Error       string                              `json:"error,omitempty"`
	Withheld    string                              `json:"withheld,omitempty"`
	Decision    delegation.ApprovedQuestionDecision `json:"decision"`
	Result      *delegation.Result                  `json:"result,omitempty"`
	ModelOutput string                              `json:"model_output,omitempty"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	f := flag.NewFlagSet("approved-questions", flag.ContinueOnError)
	f.SetOutput(out)
	live := f.Bool("live", false, "explicitly run the six local source cases and approved repeats")
	baseline := f.Bool("fresh-baseline", false, "also run twelve separate fresh requests with the same facts and checks")
	brokerURL := f.String("broker", "", "explicit literal-loopback broker origin")
	ollamaURL := f.String("ollama", "", "explicit literal-loopback Ollama origin; installed qwen3.5:4b only")
	tokenPath := f.String("token-file", "", "absolute private regular scoped-token file")
	statePath := f.String("state", "", "new absolute private progress JSONL file; exclusive creation")
	outputPath := f.String("output", "", "new absolute private report JSONL file; exclusive creation")
	maxTotal := f.Int64("max-total-tokens", 0, "lower the fixed daily total cap; zero uses 50688, or 152064 with baseline")
	if err := f.Parse(args); err != nil {
		return err
	}
	if !*live {
		fmt.Fprintln(out, "No model or file access. Review README.md, then explicitly supply --live and local endpoints.")
		f.PrintDefaults()
		return nil
	}
	if f.NArg() != 0 || !loopbackOrigin(*brokerURL) || !loopbackOrigin(*ollamaURL) {
		return errors.New("live mode requires explicit literal-loopback origins and no positional arguments")
	}
	budget := inference.TokenBudget{InputTokens: 49152, OutputTokens: 1536, TotalTokens: 50688}
	if *baseline {
		budget = inference.TokenBudget{InputTokens: 147456, OutputTokens: 4608, TotalTokens: 152064}
	}
	if *maxTotal < 0 || *maxTotal > budget.TotalTokens {
		return errors.New("total budget may only lower the selected fixed cap")
	}
	if *maxTotal > 0 {
		budget.TotalTokens = *maxTotal
	}
	cases, err := loadCases()
	if err != nil {
		return err
	}
	tokenFile, err := privateFile(*tokenPath, false)
	if err != nil {
		return err
	}
	defer tokenFile.Close()
	token, err := io.ReadAll(io.LimitReader(tokenFile, 4097))
	if err != nil || len(token) == 0 || len(token) > 4096 {
		return errors.New("invalid bounded scoped-token file")
	}
	broker, err := client.New(*brokerURL, strings.TrimSpace(string(token)))
	if err != nil {
		return err
	}
	state, err := privateFile(*statePath, true)
	if err != nil {
		return err
	}
	defer state.Close()
	report, err := privateFile(*outputPath, true)
	if err != nil {
		return err
	}
	defer report.Close()
	hash := sha256.Sum256(caseJSON)
	if err := appendJSON(report, map[string]any{"schema": "questlock.approved-questions-measurement.v1", "cases_sha256": hex.EncodeToString(hash[:]), "model": "qwen3.5:4b", "sources": 6, "planned_replays": 12, "fresh_baseline": *baseline, "daily_budget": budget}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	if err := appendJSON(state, map[string]any{"phase": "initializing", "cases_sha256": hex.EncodeToString(hash[:])}); err != nil {
		return err
	}
	provider, err := inference.NewOllamaProviderContext(ctx, *ollamaURL, "qwen3.5:4b")
	if err != nil {
		return errors.New("installed local runtime unavailable; inspect retained state")
	}
	defer provider.Close()
	runner := &inference.Runner{Broker: broker, Provider: provider}
	router := delegation.Router{Factory: func(delegation.Tier) (delegation.Runner, error) { return runner, nil }, AllowPaid: false}
	if err := appendJSON(report, map[string]any{"phase": "runtime", "runtime_identity": runner.RuntimeIdentity()}); err != nil {
		return err
	}
	return measure(ctx, router, broker, cases, &budget, *baseline, state, report)
}

func measure(ctx context.Context, router delegation.Router, broker inference.Broker, cases caseSet, budget *inference.TokenBudget, baseline bool, state, report *os.File) (runErr error) {
	totals := map[string]*phaseTotals{"seed": {}, "replay": {}, "baseline": {}}
	defer func() {
		if err := appendJSON(report, map[string]any{"phase": "summary", "completed_without_errors": runErr == nil, "totals": totals}); err != nil {
			runErr = err
		}
	}()
	failed := false
	for _, c := range cases.Cases {
		base := requestFor(cases.Revision, c, budget)
		prepared, decision, err := delegation.ApplyApprovedQuestions(base, c.Approved)
		if err != nil || !decision.Matched {
			return errors.New("frozen canonical policy did not match")
		}
		var sourceFingerprint string
		sourceOK := false
		for step := 0; step < 5; step++ {
			phase, input, request := "seed", c.Approved.CanonicalQuestion, prepared
			if step > 0 && step < 3 {
				phase, input = "replay", c.Approved.Forms[step]
				raw := base
				raw.Spec.Input = input
				request, decision, err = delegation.ApplyApprovedQuestions(raw, c.Approved)
				if err != nil || !decision.Matched || request.Spec.Revision != prepared.Spec.Revision {
					return errors.New("frozen approved form did not retain canonical identity")
				}
			}
			if step >= 3 {
				if !baseline {
					break
				}
				phase, input = "baseline", c.Approved.Forms[step-2]
				request = base
				request.Spec.Input = input
				request.Spec.Task = fmt.Sprintf("approved-baseline-%s-%d", c.ID, step-2)
				decision = delegation.ApprovedQuestionDecision{}
			}
			row := reportRow{CaseID: c.ID, Phase: phase, Input: input, Decision: decision}
			if phase == "replay" && !sourceOK {
				row.Withheld = "source_not_fresh_and_quality_passing"
				totals[phase].Withheld++
				if err := appendJSON(report, row); err != nil {
					return err
				}
				continue
			}
			if err := appendJSON(state, map[string]any{"case_id": c.ID, "phase": phase, "status": "started", "task": request.Spec.Task, "revision": request.Spec.Revision}); err != nil {
				return err
			}
			result, callErr := router.Run(ctx, request)
			row.Result = &result
			t := totals[phase]
			t.Calls++
			if callErr != nil {
				row.Error = callErr.Error()
				failed = true
			}
			if result.Status == "completed" {
				t.Passed++
			}
			fresh := false
			if len(result.Attempts) == 1 {
				a := result.Attempts[0].Result
				if a.Measurement != nil && a.Measurement.GenerationCalled {
					t.Generated++
					if a.Measurement.KnownGenerationUsage && a.Measurement.GenerationUsage != nil {
						t.InputTokens += a.Measurement.GenerationUsage.InputTokens
						t.OutputTokens += a.Measurement.GenerationUsage.OutputTokens
					} else {
						t.UsageUnknown = true
					}
				}
				fresh = !a.Reused && a.Durable && a.Response.Complete && a.Measurement != nil && a.Measurement.GenerationCalled && a.Measurement.KnownGenerationUsage
				if phase == "seed" {
					sourceFingerprint = a.Fingerprint
					sourceOK = callErr == nil && result.Status == "completed" && fresh
				}
				if phase != "replay" && a.Durable {
					artifact, readErr := broker.Get(ctx, "inference/v1/jobs/"+a.Fingerprint)
					var saved struct {
						Response *inference.Response `json:"response"`
					}
					if readErr == nil && json.Unmarshal([]byte(artifact.Content), &saved) == nil && saved.Response != nil {
						row.ModelOutput = saved.Response.Text
					} else {
						row.Error = "model_output_receipt_unavailable"
						failed = true
						sourceOK = false
					}
				}
				if phase == "replay" && (callErr != nil || !a.Reused || a.Fingerprint != sourceFingerprint || a.ThisRunUsage != (inference.Usage{}) || a.Measurement == nil || a.Measurement.GenerationCalled) {
					row.Error = "replay_did_not_preserve_zero_generation_exact_identity"
					failed = true
				}
			}
			if (phase == "seed" || phase == "baseline") && callErr == nil && !fresh {
				row.Error = "fresh_scope_required"
				failed = true
				sourceOK = false
			}
			if err := appendJSON(report, row); err != nil {
				return err
			}
			if err := appendJSON(state, map[string]any{"case_id": c.ID, "phase": phase, "status": result.Status, "error": row.Error}); err != nil {
				return err
			}
			if callErr != nil && !errors.Is(callErr, delegation.ErrQualityFailed) || phase == "replay" && row.Error != "" || row.Error == "model_output_receipt_unavailable" || row.Error == "fresh_scope_required" {
				return errors.New("incomplete, uncertain or invalid replay stopped this run; preserve its receipts and do not retry automatically")
			}
		}
	}
	if failed {
		return errors.New("known source or baseline quality failures retained; inspect report")
	}
	return nil
}

func requestFor(revision string, c businessCase, budget *inference.TokenBudget) delegation.Request {
	zero, timeout, think, temperature := int64(0), int64(60), false, float64(0)
	return delegation.Request{Spec: inference.Spec{Task: "approved-source-" + c.ID, Revision: revision + "-" + c.ID, Instructions: c.Instructions, Input: c.Approved.CanonicalQuestion, Context: c.Context}, Policy: delegation.Policy{Local: delegation.Tier{ID: "local", Spec: inference.Spec{Provider: "ollama", Model: "qwen3.5:4b", MaxContextBytes: 16 << 10, MaxInputTokens: 8192, MaxOutputTokens: 256, ReuseForSeconds: 3600, DailyTokenBudget: budget, Ollama: &inference.OllamaOptions{Format: "json", Think: &think, Temperature: &temperature, KeepAliveSeconds: &zero, GenerationTimeoutSeconds: &timeout, OutputTypes: c.OutputTypes}}}}, Checks: c.Checks}
}

func loadCases() (caseSet, error) {
	var set caseSet
	d := json.NewDecoder(bytes.NewReader(caseJSON))
	d.DisallowUnknownFields()
	if d.Decode(&set) != nil || set.Schema != 1 || set.Revision == "" || len(set.Cases) != 6 {
		return set, errors.New("invalid frozen six-case data")
	}
	seen := map[string]bool{}
	for _, c := range set.Cases {
		if seen[c.ID] || c.ID == "" || len(c.Approved.Forms) != 3 || c.Approved.Forms[0] != c.Approved.CanonicalQuestion || len(c.OutputTypes) < 3 || len(c.OutputTypes) > 5 || len(c.Checks) != 2 || c.Checks[0].Kind != "json_valid" || c.Checks[1].Kind != "json_equals" || c.Checks[1].Pointer != "" {
			return set, errors.New("case needs unique ID, canonical plus two forms, 3..5 primitive fields and whole-object JSON checks")
		}
		seen[c.ID] = true
		r := requestFor(set.Revision, c, nil)
		prepared, decision, err := delegation.ApplyApprovedQuestions(r, c.Approved)
		if err != nil || !decision.Matched || delegation.Validate(prepared, false) != nil {
			return set, errors.New("invalid frozen approved request")
		}
	}
	return set, nil
}

func loopbackOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return (u.Scheme == "http" || u.Scheme == "https") && ip != nil && ip.IsLoopback() && u.Port() != "" && u.User == nil && (u.Path == "" || u.Path == "/") && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.Opaque == ""
}

func privateFile(path string, create bool) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("token, state and output paths must be absolute and private")
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return nil, errors.New("file parent must be an existing private directory")
	}
	if create {
		return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("token must be a private regular file")
	}
	return os.Open(path)
}

func appendJSON(file *os.File, value any) error {
	if err := json.NewEncoder(file).Encode(value); err != nil {
		return err
	}
	return file.Sync()
}
