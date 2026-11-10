package semantic

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSourceInputCapIsExplicitFactorizedOnlyAndBounded(t *testing.T) {
	for _, policy := range []string{"", "factorized-v1"} {
		c := validReuseConfig()
		c.VerifierPolicy = policy
		n, e := NormalizeReuseConfig(c)
		if e != nil || n.VerifierSourceMaxInputTokens != 0 {
			t.Fatal("default changed", e)
		}
		b, _ := json.Marshal(n)
		if strings.Contains(string(b), "verifier_source_max_input_tokens") {
			t.Fatal("default JSON changed")
		}
	}
	for _, tc := range []struct {
		policy string
		cap    int64
		valid  bool
	}{{"", 1, false}, {"factorized-v1", -1, false}, {"factorized-v1", 6657, false}, {"factorized-v1", 5376, true}, {"factorized-v1", 6656, true}} {
		c := validReuseConfig()
		c.VerifierPolicy, c.VerifierMaxInputTokens, c.VerifierSourceMaxInputTokens = tc.policy, 13312, tc.cap
		n, e := NormalizeReuseConfig(c)
		if (e == nil) != tc.valid {
			t.Fatalf("wrong cap validation: %+v %v", tc, e)
		}
		if tc.valid && n.VerifierSourceMaxInputTokens != tc.cap {
			t.Fatal("explicit cap changed")
		}
	}
}
func TestSourceCapAloneAttributesWorkAndDoesNotShrinkPair(t *testing.T) {
	v, c := factoredWorkFixture()
	c.VerifierSourceMaxInputTokens = 100
	for i := range v.Work {
		v.Work[i].Model = c.VerifierModel
		v.Work[i].RuntimeIdentity = strings.Repeat("a", 64)
	}
	if e := ValidateVerifierWork(v, c); e != nil {
		t.Fatal(e)
	}
	v.Work[0].Model = ""
	if ValidateVerifierWork(v, c) == nil {
		t.Fatal("source-only option allowed unattributed work")
	}
	v.Work[0].Model = c.VerifierModel
	n := int64(101)
	v.Work[0].InputTokens = &n
	n2 := int64(181)
	v.InputTokens = &n2
	if ValidateVerifierWork(v, c) == nil {
		t.Fatal("source over cap accepted")
	}
	// The pair retains its declared half even when the source cap is smaller.
	v, c = factoredWorkFixture()
	c.VerifierSourceMaxInputTokens = 100
	for i := range v.Work {
		v.Work[i].Model = c.VerifierModel
		v.Work[i].RuntimeIdentity = strings.Repeat("a", 64)
	}
	pair := int64(150)
	sum := int64(250)
	v.Work[1].InputTokens = &pair
	v.InputTokens = &sum
	if e := ValidateVerifierWork(v, c); e != nil {
		t.Fatal("source cap incorrectly applied to pair", e)
	}
}
