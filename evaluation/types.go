// Package evaluation compares explicit inference configurations against bounded,
// deterministic task checks. Checks establish only the supplied expectations;
// they do not establish general model quality or authorize automatic routing.
package evaluation

import (
	"context"
	"encoding/json"

	"github.com/spfuzzylink/questlock/inference"
)

const Schema = 1

type Suite struct {
	Schema         int             `json:"schema"`
	ID             string          `json:"id"`
	Revision       string          `json:"revision"`
	Objective      string          `json:"objective,omitempty"` // latency, generated_tokens, or priced_cost
	Cases          []Case          `json:"cases"`
	Configurations []Configuration `json:"configurations"`
}

type Case struct {
	ID           string                  `json:"id"`
	Complexity   string                  `json:"complexity"`
	Instructions string                  `json:"instructions"`
	Input        string                  `json:"input"`
	Context      []inference.ContextItem `json:"context,omitempty"`
	Checks       []Check                 `json:"checks"`
	Fixtures     map[string]Fixture      `json:"fixture_by_configuration,omitempty"`
}

// Configuration.Spec supplies model, provider and explicit resource limits.
// Task identity and prompt material come exclusively from the suite and case.
type Configuration struct {
	ID            string         `json:"id"`
	Spec          inference.Spec `json:"spec"`
	MaxFirstRunMS int64          `json:"max_first_run_ms,omitempty"`
	MaxReuseMS    int64          `json:"max_reuse_ms,omitempty"`
}

// Check supports JSON predicates, literal substrings, and bounded text counts.
// Substring and count checks do not establish factual support, tone, suitability,
// or the correctness of arbitrary generated code. Count bounds are inclusive;
// pointers distinguish an explicit zero from an omitted bound.
type Check struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Pointer    string          `json:"pointer,omitempty"`
	Expected   json.RawMessage `json:"expected,omitempty"`
	Text       string          `json:"text,omitempty"`
	IgnoreCase bool            `json:"ignore_case,omitempty"`
	Min        *int            `json:"min,omitempty"`
	Max        *int            `json:"max,omitempty"`
}

// Fixture is supplied public test data. Its usage and durations are simulated,
// never a benchmark of the model named by a configuration.
type Fixture struct {
	Response   inference.Response `json:"response"`
	FirstRunMS int64              `json:"first_run_ms"`
	ReuseMS    int64              `json:"reuse_ms"`
}

type Runner interface {
	Run(context.Context, inference.Spec) (inference.Result, error)
}

type RunnerFactory func(Case, Configuration) (Runner, error)

type CheckResult struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Passed   bool   `json:"passed"`
	Observed *int   `json:"observed,omitempty"`
	Status   string `json:"status,omitempty"`
}

type Measurement struct {
	ElapsedMS       float64                `json:"elapsed_ms"`
	TimingSource    string                 `json:"timing_source"`
	FreshGeneration bool                   `json:"fresh_generation"`
	Reused          bool                   `json:"reused"`
	Complete        bool                   `json:"complete"`
	Durable         bool                   `json:"durable"`
	QualityPassed   bool                   `json:"quality_passed"`
	Checks          []CheckResult          `json:"checks"`
	ThisRunUsage    inference.Usage        `json:"this_run_usage"`
	AvoidedUsage    inference.Usage        `json:"avoided_usage"`
	CostUSD         *float64               `json:"cost_usd,omitempty"`
	AvoidedCostUSD  *float64               `json:"avoided_cost_usd,omitempty"`
	OutputSHA256    string                 `json:"output_sha256,omitempty"`
	ErrorCategory   string                 `json:"error_category,omitempty"`
	Inference       *inference.Measurement `json:"inference,omitempty"`
}

type CaseResult struct {
	CaseID          string       `json:"case_id"`
	Complexity      string       `json:"complexity"`
	ConfigurationID string       `json:"configuration_id"`
	Fingerprint     string       `json:"fingerprint,omitempty"`
	First           Measurement  `json:"first"`
	Reuse           *Measurement `json:"reuse,omitempty"`
	Eligible        bool         `json:"eligible"`
}

type Candidate struct {
	ConfigurationID string   `json:"configuration_id"`
	CasesPassed     int      `json:"cases_passed"`
	CasesTotal      int      `json:"cases_total"`
	Eligible        bool     `json:"eligible"`
	GeneratedTokens int64    `json:"generated_tokens"`
	EstimatedUSD    *float64 `json:"estimated_usd,omitempty"`
	FirstRunSamples int      `json:"first_run_samples"`
	ReuseSamples    int      `json:"reuse_samples"`
	MeanFirstRunMS  float64  `json:"mean_first_run_ms"`
	MeanReuseMS     float64  `json:"mean_reuse_ms"`
}

type Selection struct {
	ConfigurationID string `json:"configuration_id"`
	Metric          string `json:"metric"`
	Scope           string `json:"scope"`
}

type Report struct {
	Schema        int          `json:"schema"`
	SuiteID       string       `json:"suite_id"`
	Revision      string       `json:"revision"`
	SuiteSHA256   string       `json:"suite_sha256"`
	Mode          string       `json:"mode"`
	Objective     string       `json:"objective"`
	Results       []CaseResult `json:"results"`
	Candidates    []Candidate  `json:"candidates"`
	Selection     *Selection   `json:"selection,omitempty"`
	SelectionNote string       `json:"selection_note"`
	Limitations   []string     `json:"limitations"`
}
