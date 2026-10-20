package team

import (
	"encoding/json"
	"errors"
	"math"

	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
)

type Case struct {
	ID         string  `json:"id"`
	Complexity string  `json:"complexity"`
	Request    Request `json:"request"`
}
type Suite struct {
	Schema int    `json:"schema"`
	ID     string `json:"id"`
	Cases  []Case `json:"cases"`
}
type Baseline struct {
	Schema        int             `json:"schema"`
	TaskSHA256    string          `json:"task_sha256"`
	PromptSHA256  string          `json:"prompt_sha256"`
	Model         string          `json:"model"`
	Source        string          `json:"source"`
	Complete      bool            `json:"complete"`
	UsageKnown    bool            `json:"usage_known"`
	Usage         inference.Usage `json:"usage"`
	Text          string          `json:"text,omitempty"`
	ErrorCategory string          `json:"error_category,omitempty"`
}
type Pair struct {
	Case     Case
	Local    Report
	Baseline Baseline
}
type ComparisonRow struct {
	AdditionalLocalOutputTypes map[string]string `json:"additional_local_output_types,omitempty"`
	CaseID                     string            `json:"case_id"`
	Complexity                 string            `json:"complexity"`
	Matched                    bool              `json:"matched"`
	LocalPassed                bool              `json:"local_passed"`
	BaselinePassed             bool              `json:"baseline_passed"`
	UsageKnown                 bool              `json:"usage_known"`
	BaselineHostedTokens       int64             `json:"baseline_hosted_tokens"`
	LocalHostedTokens          int64             `json:"local_hosted_tokens"`
	Eligible                   bool              `json:"eligible"`
}
type Comparison struct {
	WorkflowComparison           string          `json:"workflow_comparison,omitempty"`
	Schema                       int             `json:"schema"`
	Metric                       string          `json:"metric"`
	Scope                        string          `json:"scope"`
	Cases                        []ComparisonRow `json:"cases"`
	Eligible                     bool            `json:"eligible"`
	BaselineHostedTokens         int64           `json:"baseline_hosted_tokens"`
	LocalHostedTokens            int64           `json:"local_hosted_tokens"`
	ReductionPercent             *float64        `json:"hosted_token_reduction_percent"`
	Target99Established          bool            `json:"target_99_percent_established"`
	HostedCreditReductionPercent *float64        `json:"hosted_credit_reduction_percent"`
	InvoiceSavingsUSD            *float64        `json:"invoice_savings_usd"`
	CostPerAcceptedTaskUSD       *float64        `json:"cost_per_accepted_task_usd"`
	HumanRework                  string          `json:"human_rework"`
	BootstrapHostedUsage         string          `json:"bootstrap_hosted_usage"`
}

// TaskDigest excludes provider settings while pinning the same task, context,
// revision and quality contract on both sides of a comparison.
func TaskDigest(r Request) string {
	v := struct {
		Task         string
		Revision     string
		Instructions string
		Input        string
		Context      []inference.ContextItem
		Checks       []evaluation.Check
	}{r.Task, r.Revision, r.Instructions, r.Input, r.Context, r.Checks}
	b, _ := json.Marshal(v)
	return hash(string(b))
}

// MasterPrompt is the exact provider-independent master prompt, suitable for
// an explicitly authorized separate Codex subscription baseline. Producing it
// does not start a hosted model, import authentication or enable fallback.
func MasterPrompt(r Request) (string, error) {
	if err := Validate(r); err != nil {
		return "", err
	}
	w, err := workerRequest(r, r.Master, "")
	if err != nil {
		return "", err
	}
	spec := w.Policy.Local.Spec
	spec.Task = w.Spec.Task
	spec.Revision = w.Spec.Revision
	spec.Instructions = w.Spec.Instructions
	spec.Input = w.Spec.Input
	spec.Context = w.Spec.Context
	p, err := inference.BuildPlan(spec)
	if err != nil {
		return "", err
	}
	return inference.BoundedTextPrompt(p.Request), nil
}

func Compare(pairs []Pair) (Comparison, error) {
	r := Comparison{Schema: 1, Metric: "observed_hosted_input_plus_output_tokens", Scope: "explicit_bounded_suite_only; not account credits or whole development", Eligible: true, HumanRework: "not measured; checks/model reviews are not human acceptance", BootstrapHostedUsage: "excluded and unmeasured; hosted build coordinator/reviewers consume normal allowance"}
	if len(pairs) < 1 || len(pairs) > 32 {
		return r, errors.New("comparison requires 1..32 explicit cases")
	}
	seen := map[string]bool{}
	for _, p := range pairs {
		if !identifier.MatchString(p.Case.ID) || seen[p.Case.ID] || (p.Case.Complexity != "simple" && p.Case.Complexity != "complex") {
			return r, errors.New("invalid or duplicate comparison case")
		}
		seen[p.Case.ID] = true
		fp, err := Fingerprint(p.Case.Request)
		if err != nil {
			return r, err
		}
		prompt, err := MasterPrompt(p.Case.Request)
		if err != nil {
			return r, err
		}
		_, localPass := evaluation.CheckOutput(p.Local.Text, p.Case.Request.Checks)
		_, baselinePass := evaluation.CheckOutput(p.Baseline.Text, p.Case.Request.Checks)
		row := ComparisonRow{CaseID: p.Case.ID, Complexity: p.Case.Complexity, Matched: p.Local.Schema == 1 && p.Local.RequestSHA256 == fp && p.Baseline.Schema == 1 && p.Baseline.TaskSHA256 == TaskDigest(p.Case.Request) && p.Baseline.PromptSHA256 == hash(prompt) && p.Baseline.Source == "operator_recorded_codex_subscription_jsonl" && text(p.Baseline.Model, 128)}
		row.AdditionalLocalOutputTypes = runtimeOutputTypes(p.Case.Request)
		if row.AdditionalLocalOutputTypes != nil {
			r.WorkflowComparison = "decoder-assisted local workflow with additional declared output types; not identical-information model parity"
		}
		row.LocalPassed = p.Local.Status == "completed" && localPass && p.Local.OutputSHA256 == hash(p.Local.Text) && !p.Local.PaidFallbackEnabled && len(p.Local.Steps) == len(p.Case.Request.Reviewers)+1
		roles := append([]Role{p.Case.Request.Master}, p.Case.Request.Reviewers...)
		for i, s := range p.Local.Steps {
			if i >= len(roles) || s.Role != roles[i].ID || s.State != "completed" {
				row.LocalPassed = false
			}
		}
		row.BaselinePassed = p.Baseline.Complete && p.Baseline.ErrorCategory == "" && baselinePass
		u := p.Baseline.Usage
		// This schema has only local roles. A nonzero hosted counter is a
		// malformed receipt, not a number to sum (or risk overflowing).
		row.UsageKnown = p.Local.HostedUsageKnown && p.Baseline.UsageKnown && u.InputTokens > 0 && u.InputTokens <= 1000000000 && u.OutputTokens >= 0 && u.OutputTokens <= 1000000000 && u.InputTokens == u.UncachedInputTokens+u.CacheReadTokens+u.CacheWriteTokens && u.UncachedInputTokens >= 0 && u.UncachedInputTokens <= 1000000000 && u.CacheReadTokens >= 0 && u.CacheReadTokens <= 1000000000 && u.CacheWriteTokens >= 0 && u.CacheWriteTokens <= 1000000000 && u.ReasoningTokens >= 0 && u.ReasoningTokens <= u.OutputTokens && p.Local.HostedTokens == 0
		if row.UsageKnown {
			row.BaselineHostedTokens = u.InputTokens + u.OutputTokens
			row.LocalHostedTokens = p.Local.HostedTokens
			r.BaselineHostedTokens += row.BaselineHostedTokens
			r.LocalHostedTokens += row.LocalHostedTokens
		}
		row.Eligible = row.Matched && row.LocalPassed && row.BaselinePassed && row.UsageKnown
		r.Eligible = r.Eligible && row.Eligible
		r.Cases = append(r.Cases, row)
	}
	if r.Eligible && r.BaselineHostedTokens > 0 {
		v := 100 * (1 - float64(r.LocalHostedTokens)/float64(r.BaselineHostedTokens))
		if !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 100 {
			r.ReductionPercent = &v
			r.Target99Established = v >= 99
		}
	}
	return r, nil
}

// DefaultSuite is deliberately bounded policy/extraction reasoning, not a claim
// that six fixtures represent real repository-sized software engineering.
func DefaultSuite(template Request) Suite {
	rows := []struct{ id, complexity, instructions, input, expected string }{
		{"ticket", "simple", "Return only JSON with issue and deadline copied from input.", "Ticket OPS-17 is due 2030-04-03.", `{"issue":"OPS-17","deadline":"2030-04-03"}`},
		{"route", "simple", "Return JSON with provider. Choose the only permitted provider.", "Policy permits local Ollama. Paid providers and fallback are disabled.", `{"provider":"ollama"}`},
		{"queue", "simple", "Return JSON with waiting_count. Subtract running from total.", "There are 9 jobs total and 3 running.", `{"waiting_count":6}`},
		{"uncertain", "complex", "Return JSON with action and operation_id. An uncertain publish must retain exact identity for inspection; it must never generate again or change operation ID.", "Publish operation op-17 may have committed. Its acknowledgement was lost. The master has no confirmed receipt.", `{"action":"inspect","operation_id":"op-17"}`},
		{"resources", "complex", "Return JSON with max_workers and excess_work. Preserve all queued work. Available worker CPU is 3 cores. Available memory is 1024 MB; keep 256 MB headroom plus 128 MB broker reserve. Each worker needs 1 core and 300 MB. Use the smaller capacity, rounded down. Excess work must wait.", "Compute safe capacity without discarding queued jobs.", `{"max_workers":2,"excess_work":"wait"}`},
		{"rework", "complex", "Return JSON with accepted_tasks and cost_per_accepted_task_usd. Rewritten tasks lose acceptance until reaccepted. Unknown human effort leaves accepted-task cost unknown (null), even with zero provider charges.", "10 tasks passed machine checks and were accepted. A person later rewrote 2; neither was reaccepted. Human effort is unrecorded. Provider charges are 0 USD.", `{"accepted_tasks":8,"cost_per_accepted_task_usd":null}`},
	}
	s := Suite{Schema: 1, ID: "bounded-local-team-v1"}
	for _, v := range rows {
		r := template
		r.Task = "team-benchmark/" + v.id
		r.Revision = "snapshot-1"
		r.Instructions = v.instructions
		r.Input = v.input
		r.Context = nil
		r.Checks = []evaluation.Check{{ID: "expected", Kind: "json_equals", Expected: json.RawMessage(v.expected)}}
		s.Cases = append(s.Cases, Case{ID: v.id, Complexity: v.complexity, Request: r})
	}
	return s
}
