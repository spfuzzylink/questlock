package semantic

import (
	"strings"
	"testing"
)

func factoredWorkFixture() (ValidityResult, ReuseConfig) {
	c := validReuseConfig()
	c.VerifierPolicy = "factorized-v1"
	c, _ = NormalizeReuseConfig(c)
	in1, in2, out1, out2, totalIn, totalOut := int64(100), int64(80), int64(20), int64(18), int64(180), int64(38)
	v := ValidityResult{Verdict: "accept", SameIntent: true, SameFacts: true, SameConstraints: true, AnswerSupported: true, Complete: true, Durable: true, GenerationCalled: true, InputTokens: &totalIn, OutputTokens: &totalOut, ReceiptKey: "semantic/v1/decisions/" + strings.Repeat("a", 64), OutputSHA256: strings.Repeat("a", 64), Work: []VerificationWork{
		{Stage: "source", Complete: true, Durable: true, GenerationCalled: true, InputTokens: &in1, OutputTokens: &out1, ReceiptKey: "inference/v1/jobs/" + strings.Repeat("b", 64), OutputSHA256: strings.Repeat("c", 64)},
		{Stage: "equivalence", Complete: true, Durable: true, GenerationCalled: true, InputTokens: &in2, OutputTokens: &out2, ReceiptKey: "inference/v1/jobs/" + strings.Repeat("d", 64), OutputSHA256: strings.Repeat("e", 64)},
	}}
	return v, c
}
func TestFactorizedWorkRejectsUnconfirmedAndMisattributedProofs(t *testing.T) {
	v, c := factoredWorkFixture()
	if err := ValidateVerifierWork(v, c); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*ValidityResult){
		"no stages":              func(v *ValidityResult) { v.Work = nil },
		"missing pair":           func(v *ValidityResult) { v.Work = v.Work[:1] },
		"wrong order":            func(v *ValidityResult) { v.Work[0], v.Work[1] = v.Work[1], v.Work[0] },
		"missing receipt":        func(v *ValidityResult) { v.Work[0].ReceiptKey = "" },
		"missing usage":          func(v *ValidityResult) { v.Work[0].InputTokens = nil },
		"unknown work":           func(v *ValidityResult) { v.Work[0].Complete = false },
		"not durable":            func(v *ValidityResult) { v.Work[1].Durable = false },
		"duplicate job":          func(v *ValidityResult) { v.Work[1].ReceiptKey = v.Work[0].ReceiptKey },
		"usage mismatch":         func(v *ValidityResult) { n := int64(0); v.InputTokens = &n },
		"hidden generation":      func(v *ValidityResult) { v.GenerationCalled = false },
		"false free work":        func(v *ValidityResult) { v.Work[0].Reused = true },
		"unsupported stage":      func(v *ValidityResult) { v.Work[0].Stage = "external" },
		"bad composite":          func(v *ValidityResult) { v.ReceiptKey = "inference/v1/jobs/" + strings.Repeat("a", 64) },
		"changed composite hash": func(v *ValidityResult) { v.OutputSHA256 = strings.Repeat("f", 64) },
		"negative usage":         func(v *ValidityResult) { n := int64(-1); v.Work[0].InputTokens = &n },
		"stage exceeds cap":      func(v *ValidityResult) { n := c.VerifierMaxInputTokens; v.Work[0].InputTokens = &n },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			v, c := factoredWorkFixture()
			mutate(&v)
			if ValidateVerifierWork(v, c) == nil {
				t.Fatal("unconfirmed attribution accepted")
			}
		})
	}
	v, c = factoredWorkFixture()
	zero := int64(0)
	for i := range v.Work {
		v.Work[i].Reused = true
		v.Work[i].GenerationCalled = false
		v.Work[i].InputTokens = &zero
		v.Work[i].OutputTokens = &zero
	}
	v.Reused = true
	v.GenerationCalled = false
	v.InputTokens = &zero
	v.OutputTokens = &zero
	if err := ValidateVerifierWork(v, c); err != nil {
		t.Fatal(err)
	}
}
func TestFactorizedPolicyBoundsAndLegacyGuardStaySeparate(t *testing.T) {
	c := validReuseConfig()
	c.VerifierPolicy = "factorized-v1"
	c, e := NormalizeReuseConfig(c)
	if e != nil || c.VerifierMaxInputTokens != 2*DefaultVerifierMaxInputTokens {
		t.Fatalf("two-stage bound: %+v %v", c, e)
	}
	c.VerifierMaxOutputTokens = 16
	if _, e = NormalizeReuseConfig(c); e == nil {
		t.Fatal("one-stage output allowance accepted")
	}
	old, current := "Explain ticket VR-731.", "Explain the ticket."
	if len(CheckReuseProtectedTerms(old, current, "")) == 0 {
		t.Fatal("legacy omission guard changed")
	}
	if len(CheckReuseProtectedTerms(old, current, "factorized-v1")) != 0 {
		t.Fatal("omitted identifier should reach mandatory context verifier")
	}
	if len(CheckReuseProtectedTerms(old, "Explain ticket VR-732.", "factorized-v1")) != 0 {
		t.Fatal("the contextual verifier must decide changed identifiers")
	}
	if len(CheckReuseProtectedTerms("Show A1 with 3 and B2 with 7.", "Show B2 with 7 and A1 with 3.", "factorized-v1")) != 0 {
		t.Fatal("literal order should reach role-aware verifier")
	}
	for _, bad := range []string{"", "bad\x00text", "Show \"unclosed", strings.Repeat("x", MaxInputBytes+1)} {
		if len(CheckReuseProtectedTerms(old, bad, "factorized-v1")) == 0 {
			t.Fatal("invalid text passed")
		}
	}
}
