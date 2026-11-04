package semantic

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPairVerifierOptionsAreExplicitBoundedAndDoNotFillDefaults(t *testing.T) {
	for _, policy := range []string{"", "factorized-v1"} {
		c := validReuseConfig()
		c.VerifierPolicy = policy
		got, err := NormalizeReuseConfig(c)
		if err != nil || got.VerifierPairModel != "" || got.VerifierPairMaxInputTokens != 0 {
			t.Fatalf("default pair route changed: %+v %v", got, err)
		}
		b, _ := json.Marshal(got)
		if strings.Contains(string(b), "verifier_pair_") {
			t.Fatal("new pair options changed default serialization", string(b))
		}
	}
	for name, mutate := range map[string]func(*ReuseConfig){
		"legacy model":     func(c *ReuseConfig) { c.VerifierPolicy = ""; c.VerifierPairModel = "other:local" },
		"legacy cap":       func(c *ReuseConfig) { c.VerifierPolicy = ""; c.VerifierPairMaxInputTokens = 100 },
		"negative cap":     func(c *ReuseConfig) { c.VerifierPairMaxInputTokens = -1 },
		"larger than half": func(c *ReuseConfig) { c.VerifierMaxInputTokens = 1000; c.VerifierPairMaxInputTokens = 501 },
		"cloud":            func(c *ReuseConfig) { c.VerifierPairModel = "other:cloud" },
		"endpoint":         func(c *ReuseConfig) { c.VerifierPairModel = "https://example.invalid/model" },
		"padded":           func(c *ReuseConfig) { c.VerifierPairModel = " other:local" },
	} {
		t.Run(name, func(t *testing.T) {
			c := validReuseConfig()
			c.VerifierPolicy = "factorized-v1"
			mutate(&c)
			if _, err := NormalizeReuseConfig(c); err == nil {
				t.Fatal("invalid pair route accepted")
			}
		})
	}
	c := validReuseConfig()
	c.VerifierPolicy, c.VerifierPairModel = "factorized-v1", "other:local"
	c.VerifierMaxInputTokens, c.VerifierPairMaxInputTokens = 1000, 500
	got, err := NormalizeReuseConfig(c)
	if err != nil || got.VerifierPairModel != c.VerifierPairModel || got.VerifierPairMaxInputTokens != 500 || got.VerifierMaxInputTokens != 1000 {
		t.Fatalf("explicit route changed: %+v %v", got, err)
	}
}

func TestMixedVerifierWorkRequiresPerStageAttributionAndCaps(t *testing.T) {
	fixture := func() (ValidityResult, ReuseConfig) {
		v, c := factoredWorkFixture()
		c.VerifierPairModel, c.VerifierPairMaxInputTokens = "other:local", 80
		v.MixedModelUsage = true
		v.Work[0].Model, v.Work[0].RuntimeIdentity = c.VerifierModel, strings.Repeat("1", 64)
		v.Work[1].Model, v.Work[1].RuntimeIdentity = c.VerifierPairModel, strings.Repeat("2", 64)
		return v, c
	}
	v, c := fixture()
	if err := ValidateVerifierWork(v, c); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ValidityResult){
		"deferred cannot authorize": func(v *ValidityResult) { v.Work[1].AdmissionDeferred = true },
		"missing model":             func(v *ValidityResult) { v.Work[1].Model = "" },
		"wrong source model":        func(v *ValidityResult) { v.Work[0].Model = "other:local" },
		"wrong pair model":          func(v *ValidityResult) { v.Work[1].Model = c.VerifierModel },
		"missing runtime":           func(v *ValidityResult) { v.Work[0].RuntimeIdentity = "" },
		"invalid runtime":           func(v *ValidityResult) { v.Work[1].RuntimeIdentity = "private endpoint" },
		"missing mixed marker":      func(v *ValidityResult) { v.MixedModelUsage = false },
		"pair cap exceeded": func(v *ValidityResult) {
			n := int64(81)
			v.Work[1].InputTokens = &n
			n2 := int64(181)
			v.InputTokens = &n2
		},
	} {
		t.Run(name, func(t *testing.T) {
			v, c := fixture()
			mutate(&v)
			if ValidateVerifierWork(v, c) == nil {
				t.Fatal("misattributed or over-cap work accepted")
			}
		})
	}
	// A source-only rejection has not used the second model. Its known cost is
	// still retained; zero pair work must not be invented.
	v, c = fixture()
	v.Verdict, v.MixedModelUsage = "reject", false
	v.Work = v.Work[:1]
	v.InputTokens, v.OutputTokens = v.Work[0].InputTokens, v.Work[0].OutputTokens
	if err := ValidateVerifierWork(v, c); err != nil {
		t.Fatal(err)
	}
}
