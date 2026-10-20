package team

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spfuzzylink/questlock/inference"
)

func matchedPair(t *testing.T) Pair {
	t.Helper()
	r := fixture()
	c, _ := setup(t)
	e := engine(c, new(atomic.Int64), false, false)
	local, err := e.Run(context.Background(), r, "all")
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := MasterPrompt(r)
	if err != nil {
		t.Fatal(err)
	}
	return Pair{Case: Case{ID: "one", Complexity: "simple", Request: r}, Local: local, Baseline: Baseline{Schema: 1, TaskSHA256: TaskDigest(r), PromptSHA256: hash(prompt), Model: "hosted-fixture", Source: "operator_recorded_codex_subscription_jsonl", Complete: true, UsageKnown: true, Text: `{"answer":17}`, Usage: inference.Usage{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 20}}}
}
func TestComparisonNeverTurnsTokenReductionIntoInvoiceOrCreditClaim(t *testing.T) {
	p := matchedPair(t)
	r, err := Compare([]Pair{p})
	if err != nil || !r.Eligible || !r.Target99Established || r.ReductionPercent == nil || *r.ReductionPercent != 100 {
		t.Fatalf("fixture %+v %v", r, err)
	}
	if r.InvoiceSavingsUSD != nil || r.HostedCreditReductionPercent != nil || r.CostPerAcceptedTaskUSD != nil {
		t.Fatal("invented credit/invoice/human savings")
	}
}
func TestMissingFailedUnknownOrUnmatchedCasePreventsTargetClaim(t *testing.T) {
	for _, change := range []func(*Pair){func(p *Pair) { p.Local.Status = "quality_failed"; p.Local.Text = "" }, func(p *Pair) { p.Local.HostedUsageKnown = false }, func(p *Pair) { p.Baseline.UsageKnown = false }, func(p *Pair) { p.Baseline.PromptSHA256 = hash("wrong prompt") }, func(p *Pair) { p.Baseline.TaskSHA256 = hash("other task") }, func(p *Pair) { p.Baseline.Text = `{"answer":18}` }, func(p *Pair) { p.Local.Steps[1].State = "quality_failed" }, func(p *Pair) { p.Local.Steps = p.Local.Steps[:1] }, func(p *Pair) { p.Baseline = Baseline{} }, func(p *Pair) { p.Baseline.Usage.CacheReadTokens = -1 }} {
		p := matchedPair(t)
		change(&p)
		r, err := Compare([]Pair{p})
		if err != nil {
			t.Fatal(err)
		}
		if r.Eligible || r.Target99Established || r.ReductionPercent != nil {
			t.Fatalf("ineligible established %+v", r)
		}
	}
	good := matchedPair(t)
	bad := good
	bad.Case.ID = "two"
	bad.Baseline = Baseline{}
	r, err := Compare([]Pair{good, bad})
	if err != nil || len(r.Cases) != 2 || r.Eligible || r.Target99Established {
		t.Fatal("missing case disappeared", r, err)
	}
}
func TestChangedTaskQualityContractInvalidatesPair(t *testing.T) {
	p := matchedPair(t)
	p.Case.Request.Checks[0].Expected = []byte(`{"answer":18}`)
	r, err := Compare([]Pair{p})
	if err != nil {
		t.Fatal(err)
	}
	if r.Cases[0].Matched {
		t.Fatal("quality contract change matched old baseline")
	}
}

func TestMalformedLocalHostedCountsCannotOverflowToSavings(t *testing.T) {
	a, b := matchedPair(t), matchedPair(t)
	b.Case.ID = "two"
	for _, count := range []int64{1, math.MaxInt64, -1} {
		a.Local.HostedTokens = count
		b.Local.HostedTokens = count
		r, err := Compare([]Pair{a, b})
		if err != nil || r.Eligible || r.Target99Established || r.ReductionPercent != nil || r.LocalHostedTokens < 0 {
			t.Fatalf("malformed counter created savings: %+v %v", r, err)
		}
	}
}
func TestDefaultSuiteHasThreeSimpleAndThreeBoundedComplexCases(t *testing.T) {
	s := DefaultSuite(fixture())
	counts := map[string]int{}
	for _, c := range s.Cases {
		if err := Validate(c.Request); err != nil {
			t.Fatal(c.ID, err)
		}
		counts[c.Complexity]++
	}
	if counts["simple"] != 3 || counts["complex"] != 3 {
		t.Fatal(counts)
	}
}

func TestComparisonDisclosesAdditionalLocalTypesWithoutClaimingModelParity(t *testing.T) {
	p := matchedPair(t)
	p.Case.Request.Master.Ollama = &inference.OllamaOptions{Format: "json", OutputTypes: map[string]string{"answer": "integer"}}
	fp, err := Fingerprint(p.Case.Request)
	if err != nil {
		t.Fatal(err)
	}
	p.Local.RequestSHA256 = fp
	r, err := Compare([]Pair{p})
	if err != nil || !r.Eligible || !r.Cases[0].Matched || !reflect.DeepEqual(r.Cases[0].AdditionalLocalOutputTypes, map[string]string{"answer": "integer"}) || !strings.Contains(r.WorkflowComparison, "decoder-assisted") || !strings.Contains(r.WorkflowComparison, "source-grounding system instructions") || !strings.Contains(r.WorkflowComparison, "not identical-information model parity") {
		t.Fatal("additional local decoding information was hidden or broke the same-task comparison", r, err)
	}
	encoded, err := json.Marshal(r)
	if err != nil || !strings.Contains(string(encoded), `"additional_local_output_types":{"answer":"integer"}`) {
		t.Fatal("runtime contract omitted from comparison JSON", string(encoded), err)
	}
	p.Local.Text = `{"answer":18}`
	p.Local.OutputSHA256 = hash(p.Local.Text)
	r, err = Compare([]Pair{p})
	if err != nil || r.Eligible || r.Cases[0].LocalPassed || r.Target99Established || r.ReductionPercent != nil {
		t.Fatal("extra type contract weakened original numerical checks", r, err)
	}
}

func TestComparisonOmitsAdditionalTypeMetadataForLegacyPairs(t *testing.T) {
	r, err := Compare([]Pair{matchedPair(t)})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(r)
	if err != nil || r.WorkflowComparison != "" || strings.Contains(string(encoded), "additional_local_output_types") || strings.Contains(string(encoded), "workflow_comparison") {
		t.Fatal("legacy comparison acquired an undeclared runtime contract", string(encoded), err)
	}
}
