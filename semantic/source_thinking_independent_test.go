package semantic

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestIndependentSourceThinkingRequiresExplicitFactorizedPolicyAndBoundedOutput(t *testing.T) {
	for _, policy := range []string{"", "factorized-v1"} {
		c := validReuseConfig()
		c.VerifierPolicy = policy
		got, err := NormalizeReuseConfig(c)
		if err != nil || got.VerifierSourceThink || got.VerifierMaxOutputTokens != 128 || got.VerifierTimeoutMS != DefaultVerifierTimeoutMS {
			t.Fatalf("existing policy defaults changed: %+v %v", got, err)
		}
		encoded, _ := json.Marshal(got)
		if strings.Contains(string(encoded), "verifier_source_think") {
			t.Fatal("disabled source thinking changed existing serialized policy")
		}
		c.VerifierMaxOutputTokens = 513
		if _, err := NormalizeReuseConfig(c); err == nil {
			t.Fatal("existing no-thinking output ceiling was raised")
		}
	}
	c := validReuseConfig()
	c.VerifierSourceThink = true
	if got, err := NormalizeReuseConfig(c); err == nil || !reflect.DeepEqual(got, ReuseConfig{}) {
		t.Fatal("source thinking silently enabled the standalone verifier")
	}
	c.VerifierPolicy = "factorized-v1"
	got, err := NormalizeReuseConfig(c)
	if err != nil || !got.VerifierSourceThink || got.VerifierMaxOutputTokens != 4096 || got.VerifierMaxInputTokens != 16384 || got.VerifierTimeoutMS != 120000 {
		t.Fatalf("explicit thinking lacks bounded defaults: %+v %v", got, err)
	}
	encoded, _ := json.Marshal(got)
	var roundTrip ReuseConfig
	if !strings.Contains(string(encoded), `"verifier_source_think":true`) || json.Unmarshal(encoded, &roundTrip) != nil || !reflect.DeepEqual(got, roundTrip) {
		t.Fatal("explicit source thinking was lost in the public JSON contract")
	}
	for _, cap := range []int64{-1, 1, 16, 31, 4097} {
		c.VerifierMaxOutputTokens = cap
		if got, err := NormalizeReuseConfig(c); err == nil || !reflect.DeepEqual(got, ReuseConfig{}) {
			t.Fatalf("invalid thinking output allowance accepted: %d", cap)
		}
	}
	for _, cap := range []int64{32, 128, 512, 513, 4096} {
		c.VerifierMaxOutputTokens = cap
		got, err := NormalizeReuseConfig(c)
		if err != nil || got.VerifierMaxOutputTokens != cap || !got.VerifierSourceThink {
			t.Fatalf("valid explicit thinking allowance changed: %d %+v %v", cap, got, err)
		}
	}
	c.VerifierTimeoutMS = MaxVerifierTimeoutMS + 1
	if _, err := NormalizeReuseConfig(c); err == nil {
		t.Fatal("source thinking bypassed the existing maximum timeout")
	}
}
