package semantic

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSourceThinkingIsExplicitAndFactorizedOnly(t *testing.T) {
	if MaxVerifierOutputTokens != 512 || MaxFactorizedThinkingOutputTokens != 4096 {
		t.Fatal("ordinary and opt-in reasoning ceilings must remain separate")
	}
	for _, tc := range []struct {
		name, policy  string
		think         bool
		input, output int64
		timeout       int
	}{
		{"published defaults", "", false, 8192, 128, 30000},
		{"factorized defaults", "factorized-v1", false, 16384, 128, 30000},
		{"explicit source reasoning", "factorized-v1", true, 16384, 4096, 120000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validReuseConfig()
			c.VerifierPolicy, c.VerifierSourceThink = tc.policy, tc.think
			got, err := NormalizeReuseConfig(c)
			if err != nil || got.VerifierSourceThink != tc.think || got.VerifierMaxInputTokens != tc.input || got.VerifierMaxOutputTokens != tc.output || got.VerifierTimeoutMS != tc.timeout {
				t.Fatalf("unexpected normalized policy: %+v %v", got, err)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(encoded, &fields) != nil {
				t.Fatal("invalid config encoding")
			}
			if tc.think && string(fields["verifier_source_think"]) != "true" || !tc.think && fields["verifier_source_think"] != nil {
				t.Fatalf("explicit opt-in was lost or changed default JSON shape: %s", encoded)
			}
			var restored ReuseConfig
			if json.Unmarshal(encoded, &restored) != nil || !reflect.DeepEqual(got, restored) {
				t.Fatal("source-thinking configuration did not round-trip")
			}
		})
	}
}

func TestSourceThinkingDoesNotBroadenOtherPoliciesOrExplicitLimits(t *testing.T) {
	for _, tc := range []struct {
		name, policy string
		think        bool
		output       int64
		timeout      int
		valid        bool
	}{
		{"legacy cannot think", "", true, 128, 30000, false},
		{"unknown cannot think", "unknown", true, 128, 30000, false},
		{"legacy ceiling", "", false, 512, 30000, true},
		{"legacy ceiling not raised", "", false, 513, 30000, false},
		{"factorized no-think ceiling", "factorized-v1", false, 512, 30000, true},
		{"factorized no-think ceiling not raised", "factorized-v1", false, 513, 30000, false},
		{"explicit small reasoning cap retained", "factorized-v1", true, 128, 30000, true},
		{"both stage minimum", "factorized-v1", true, 32, 30000, true},
		{"below both stage minimum", "factorized-v1", true, 31, 30000, false},
		{"negative reasoning cap", "factorized-v1", true, -1, 30000, false},
		{"inclusive reasoning maximum", "factorized-v1", true, 4096, 120000, true},
		{"above reasoning maximum", "factorized-v1", true, 4097, 120000, false},
		{"reasoning timeout not raised", "factorized-v1", true, 4096, 120001, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validReuseConfig()
			c.VerifierPolicy, c.VerifierSourceThink = tc.policy, tc.think
			c.VerifierMaxOutputTokens, c.VerifierTimeoutMS = tc.output, tc.timeout
			got, err := NormalizeReuseConfig(c)
			if tc.valid {
				if err != nil || got.VerifierMaxOutputTokens != tc.output || got.VerifierTimeoutMS != tc.timeout {
					t.Fatalf("explicit reasoning limits changed: %+v %v", got, err)
				}
			} else if err == nil || !reflect.DeepEqual(got, ReuseConfig{}) {
				t.Fatalf("invalid policy returned usable configuration: %+v %v", got, err)
			}
		})
	}
}
