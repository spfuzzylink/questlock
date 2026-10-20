package team

import (
	"context"
	"github.com/spfuzzylink/questlock/inference"
	"math"
	"sync/atomic"
	"testing"
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
