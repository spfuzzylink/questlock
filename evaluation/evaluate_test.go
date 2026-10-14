package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/protocol"
)

func TestPublicSixCaseFixtureSuite(t *testing.T) {
	suite := DefaultSuite()
	counts := map[string]int{}
	for _, c := range suite.Cases {
		counts[c.Complexity]++
	}
	if counts["simple"] != 3 || counts["complex"] != 3 {
		t.Fatalf("scenario distribution: %v", counts)
	}
	report, err := RunFixtures(context.Background(), suite)
	if err != nil {
		t.Fatal(err)
	}
	if report.Mode != "fixture" || report.Selection == nil || report.Selection.ConfigurationID != "balanced-256" || report.Selection.Scope != "simulated_fixture_suite_only" {
		t.Fatalf("unsafe fixture recommendation: %+v", report)
	}
	if len(report.Results) != 12 || report.Candidates[0].CasesPassed != 4 || report.Candidates[1].CasesPassed != 6 {
		t.Fatalf("wrong gates: %+v", report.Candidates)
	}
	for _, row := range report.Results {
		if row.First.TimingSource != "simulated_fixture" || row.First.Inference != nil || row.First.CostUSD != nil {
			t.Fatalf("fixture measurements misrepresented: %+v", row)
		}
		if row.ConfigurationID == "balanced-256" {
			if !row.Eligible || row.Reuse == nil || row.Reuse.ThisRunUsage != (inference.Usage{}) || row.Reuse.AvoidedUsage != row.First.ThisRunUsage || row.First.OutputSHA256 != row.Reuse.OutputSHA256 {
				t.Fatalf("reuse not isolated: %+v", row)
			}
		}
		if row.ConfigurationID == "compact-64" && row.CaseID == "incident-handoff" {
			if !row.First.FreshGeneration || row.First.ErrorCategory != "incomplete" || row.Reuse != nil || row.Eligible {
				t.Fatalf("truncation silently retried: %+v", row)
			}
		}
		if row.ConfigurationID == "compact-64" && row.CaseID == "release-readiness" {
			if !row.First.Complete || !row.First.Durable || row.First.QualityPassed || row.Reuse == nil || !row.Reuse.Reused || row.Eligible {
				t.Fatalf("protocol completion treated as task quality: %+v", row)
			}
		}
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "must be completed") || strings.Contains(string(encoded), `"response"`) || strings.Contains(string(encoded), `"text"`) {
		t.Fatal("report retained prompt/output bodies")
	}
}

func TestDeterministicChecksRejectWrongButKeywordRichAnswers(t *testing.T) {
	checks := []Check{{ID: "value", Kind: "json_equals", Pointer: "/ship", Expected: json.RawMessage("false")}, {ID: "unsupported", Kind: "not_contains", Text: "infinite", IgnoreCase: true}}
	for _, text := range []string{
		`{"ship":true,"missing":["container"],"claim":"not_released"}`,
		`{"ship":false,"claim":"INFINITE free inference guaranteed"}`,
		`{"ship":false,"ship":true}`,
		`{"ship":false} {"ship":true}`,
		`{"ship":false`,
	} {
		if _, ok := CheckOutput(text, checks); ok {
			t.Fatalf("wrong/contradictory output passed: %s", text)
		}
	}
	if _, ok := CheckOutput(`{"ship":false}`, checks); !ok {
		t.Fatal("correct expected fields failed")
	}
	// A check at /ship intentionally leaves unrelated fields unvalidated.
	if _, ok := CheckOutput(`{"ship":false,"unvalidated":"possibly wrong"}`, checks); !ok {
		t.Fatal("partial-field check changed into an undeclared full-schema check")
	}
	if _, ok := CheckOutput(`{"ship":false}`, nil); ok {
		t.Fatal("no checks asserted quality")
	}
}

func TestJSONPointersExactNumbersAndStructure(t *testing.T) {
	for _, tc := range []struct {
		text, pointer, expected string
		pass                    bool
	}{
		{`{"a/b":{"~key":[100]}}`, "/a~1b/~0key/0", "1e2", true},
		{`{"value":9007199254740993}`, "/value", "9007199254740992", false},
		{`{"value":100.0}`, "/value", "100", true},
		{`["correct"]`, "/00", `"correct"`, false},
		{`{"x":null}`, "/missing", "null", false},
		{`{"x":1,"extra":2}`, "", `{"x":1}`, false},
		{`{"x":1}`, "", `{"x":1.0}`, true},
	} {
		_, pass := CheckOutput(tc.text, []Check{{ID: "exact", Kind: "json_equals", Pointer: tc.pointer, Expected: json.RawMessage(tc.expected)}})
		if pass != tc.pass {
			t.Fatalf("comparison %s at %s to %s = %v", tc.text, tc.pointer, tc.expected, pass)
		}
	}
}

type scriptedRunner struct {
	results []inference.Result
	errors  []error
	calls   int
	specs   []inference.Spec
}

func (r *scriptedRunner) Run(_ context.Context, spec inference.Spec) (inference.Result, error) {
	i := r.calls
	r.calls++
	r.specs = append(r.specs, spec)
	if i >= len(r.results) {
		return inference.Result{}, errors.New("unexpected extra generation")
	}
	var err error
	if i < len(r.errors) {
		err = r.errors[i]
	}
	return r.results[i], err
}

func oneCaseSuite() Suite {
	s := DefaultSuite()
	s.Cases = s.Cases[:1]
	s.Configurations = s.Configurations[:1]
	delete(s.Cases[0].Fixtures, "balanced-256")
	return s
}

func successfulPair() []inference.Result {
	u := inference.Usage{InputTokens: 81, UncachedInputTokens: 81, OutputTokens: 26}
	response := inference.Response{Complete: true, Text: `{"issue":"OPS-17","deadline":"2030-04-03"}`, Usage: u}
	first := inference.Result{Fingerprint: "snapshot", Response: response, Durable: true, GenerationUsage: u, ThisRunUsage: u}
	response.Usage = inference.Usage{}
	second := inference.Result{Fingerprint: "snapshot", Response: response, Durable: true, Reused: true, GenerationUsage: u, AvoidedUsage: u}
	return []inference.Result{first, second}
}

func TestEvaluationSkipsReuseAfterUnsafeFirstRun(t *testing.T) {
	for _, kind := range []string{"incomplete", "not_durable", "uncertain", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			pair := successfulPair()
			var runErr error
			switch kind {
			case "incomplete":
				pair[0].Response.Complete = false
				runErr = inference.ErrIncomplete
			case "not_durable":
				pair[0].Durable = false
			case "uncertain":
				runErr = inference.ErrInFlight
			case "canceled":
				runErr = context.Canceled
			}
			runner := &scriptedRunner{results: pair, errors: []error{runErr}}
			report, err := Evaluate(context.Background(), oneCaseSuite(), func(Case, Configuration) (Runner, error) { return runner, nil })
			if err != nil || runner.calls != 1 || report.Results[0].Reuse != nil || report.Selection != nil {
				t.Fatalf("unsafe retry/recommendation: %v, %+v, calls %d", err, report, runner.calls)
			}
		})
	}
}

func TestExactReuseAndFreshBaselineAreMandatory(t *testing.T) {
	for _, kind := range []string{"changed_output", "charged_reuse", "wrong_avoided_usage", "changed_fingerprint", "already_reused", "invalid_first_usage", "generation_not_called", "generation_usage_unknown", "reused_but_generated"} {
		t.Run(kind, func(t *testing.T) {
			pair := successfulPair()
			switch kind {
			case "changed_output":
				pair[1].Response.Text = `{"deadline":"2030-04-03", "issue":"OPS-17"}` // Same check values; different exact stored text.
			case "charged_reuse":
				pair[1].ThisRunUsage.InputTokens = 1
			case "wrong_avoided_usage":
				pair[1].AvoidedUsage.OutputTokens++
			case "changed_fingerprint":
				pair[1].Fingerprint = "other"
			case "already_reused":
				pair[0].Reused = true
				pair[0].ThisRunUsage = inference.Usage{}
			case "invalid_first_usage":
				pair[0].GenerationUsage.InputTokens = -1
			case "generation_not_called":
				pair[0].Measurement = &inference.Measurement{GenerationCalled: false}
			case "generation_usage_unknown":
				pair[0].Measurement = &inference.Measurement{GenerationCalled: true, KnownGenerationUsage: false}
			case "reused_but_generated":
				pair[1].Measurement = &inference.Measurement{GenerationCalled: true}
			}
			runner := &scriptedRunner{results: pair}
			report, err := Evaluate(context.Background(), oneCaseSuite(), func(Case, Configuration) (Runner, error) { return runner, nil })
			if err != nil || report.Selection != nil || report.Results[0].Eligible {
				t.Fatalf("invalid cached pair recommended: %v %+v", err, report)
			}
		})
	}
}

func TestEvaluationErrorsAreSanitized(t *testing.T) {
	secret := "sensitive-example-upstream-body"
	report, err := Evaluate(context.Background(), oneCaseSuite(), func(Case, Configuration) (Runner, error) { return nil, errors.New(secret) })
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), secret) || report.Selection != nil || report.Results[0].First.ErrorCategory != "runner_initialization_failed" {
		t.Fatalf("factory error leaked or recommended: %s", encoded)
	}
	runner := &scriptedRunner{results: successfulPair(), errors: []error{errors.New(secret)}}
	report, err = Evaluate(context.Background(), oneCaseSuite(), func(Case, Configuration) (Runner, error) { return runner, nil })
	encoded, _ = json.Marshal(report)
	if err != nil || strings.Contains(string(encoded), secret) || runner.calls != 1 || report.Results[0].First.ErrorCategory != "run_failed" {
		t.Fatalf("run error leaked/retried: %v %s", err, encoded)
	}
}

type countedProvider struct {
	fixtureProvider
	counts, generations int
}

func (p *countedProvider) CountInputTokens(ctx context.Context, request inference.Request) (int64, error) {
	p.counts++
	return p.fixtureProvider.CountInputTokens(ctx, request)
}

func (p *countedProvider) Generate(ctx context.Context, request inference.Request) (inference.Response, error) {
	p.generations++
	return p.fixtureProvider.Generate(ctx, request)
}

func TestInjectedRealRunnerGeneratesExactlyOnceAndExposesOnlyMetadata(t *testing.T) {
	suite := oneCaseSuite()
	c, config := suite.Cases[0], suite.Configurations[0]
	provider := &countedProvider{fixtureProvider: fixtureProvider{fixture: c.Fixtures[config.ID], identity: "counted-fixture"}}
	broker := &fixtureBroker{artifacts: make(map[string]protocol.Artifact), receipts: make(map[string]fixtureReceipt)}
	runner := inference.Runner{Broker: broker, Provider: provider}
	report, err := Evaluate(context.Background(), suite, func(Case, Configuration) (Runner, error) { return runner, nil })
	if err != nil || report.Selection == nil || provider.counts != 1 || provider.generations != 1 {
		t.Fatalf("actual provider calls: count=%d generation=%d report=%+v err=%v", provider.counts, provider.generations, report, err)
	}
	row := report.Results[0]
	if row.First.Inference == nil || !row.First.Inference.GenerationCalled || !row.First.Inference.KnownGenerationUsage || row.Reuse.Inference.GenerationCalled {
		t.Fatalf("invocation metadata not separated: %+v", row)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), c.Input) || strings.Contains(string(encoded), provider.fixture.Response.Text) {
		t.Fatal("real-mode report contains raw prompt or output")
	}
}

func TestIdentityChangesWhenSuiteRevisionOrConfigurationChanges(t *testing.T) {
	s := oneCaseSuite()
	baseline := caseSpec(s, s.Cases[0], s.Configurations[0], suiteDigest(s))
	for _, field := range []string{"id", "revision", "case", "configuration", "expected"} {
		other := oneCaseSuite()
		switch field {
		case "id":
			other.ID += "-other"
		case "revision":
			other.Revision += "-other"
		case "case":
			other.Cases[0].ID += "-other"
		case "configuration":
			other.Configurations[0].ID += "-other"
		case "expected":
			other.Cases[0].Checks[1].Expected = json.RawMessage(`{"issue":"other"}`)
		}
		candidate := caseSpec(other, other.Cases[0], other.Configurations[0], suiteDigest(other))
		if baseline.Task == candidate.Task || baseline.Revision == candidate.Revision {
			t.Fatalf("evaluation identity collision after %s", field)
		}
		if strings.Contains(candidate.Input, "expected-task-values") {
			t.Fatal("checks leaked into generated prompt")
		}
	}
}

func TestLatencyLimitsAndObjectiveSelection(t *testing.T) {
	s := DefaultSuite()
	s.Configurations[1].MaxFirstRunMS = 1
	report, err := RunFixtures(context.Background(), s)
	if err != nil || report.Selection != nil {
		t.Fatalf("latency gate ignored: %v %+v", err, report.Selection)
	}
	s = DefaultSuite()
	s.Cases = s.Cases[:1]
	f := s.Cases[0].Fixtures["compact-64"]
	f.FirstRunMS = 900
	f.Response.Usage.OutputTokens = 20
	s.Cases[0].Fixtures["compact-64"] = f
	for _, objective := range []string{"latency", "generated_tokens", "priced_cost"} {
		s.Objective = objective
		if objective == "priced_cost" {
			for i := range s.Configurations {
				s.Configurations[i].Spec.Provider = "openai"
				s.Configurations[i].Spec.Stream = false
				s.Configurations[i].Spec.Prices = &inference.Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
			}
		}
		report, err = RunFixtures(context.Background(), s)
		if err != nil || report.Selection == nil {
			t.Fatalf("objective %s failed: %v %+v", objective, err, report)
		}
		expected := "compact-64"
		if objective == "latency" {
			expected = "balanced-256"
		}
		if report.Selection.ConfigurationID != expected {
			t.Fatalf("objective %s chose %s", objective, report.Selection.ConfigurationID)
		}
	}
}
