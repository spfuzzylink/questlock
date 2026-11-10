package semantic

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/protocol"
)

func TestPairAssessmentIsOptionalBoundedAndIndependentOfSourceThinking(t *testing.T) {
	for _, tc := range []struct {
		name               string
		assessment, source bool
		output             int64
		want               int64
	}{
		{"legacy", false, false, 0, 128},
		{"assessment", true, false, 0, 512},
		{"explicit small cap", true, false, 64, 64},
		{"source thinking", true, true, 0, 4096},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validReuseConfig()
			c.VerifierPolicy = "factorized-v1"
			c.VerifierPairAssessment, c.VerifierSourceThink = tc.assessment, tc.source
			c.VerifierMaxOutputTokens = tc.output
			got, err := NormalizeReuseConfig(c)
			if err != nil || got.VerifierPairAssessment != tc.assessment || got.VerifierPairThink || got.VerifierMaxOutputTokens != tc.want {
				t.Fatalf("assessment normalization: %+v %v", got, err)
			}
			wantDeadline := 30000
			if tc.source {
				wantDeadline = 120000
			}
			if got.VerifierTimeoutMS != wantDeadline {
				t.Fatal("assessment changed thinking deadline")
			}
			raw, _ := json.Marshal(got)
			if strings.Contains(string(raw), "verifier_pair_assessment") != tc.assessment {
				t.Fatal("false changed legacy JSON")
			}
			var round ReuseConfig
			if json.Unmarshal(raw, &round) != nil || !reflect.DeepEqual(got, round) {
				t.Fatal("assessment did not round trip")
			}
		})
	}
	for _, change := range []func(*ReuseConfig){
		func(c *ReuseConfig) { c.VerifierPolicy = "" },
		func(c *ReuseConfig) { c.VerifierPairThink = true },
		func(c *ReuseConfig) { c.VerifierMaxOutputTokens = 513 },
	} {
		c := validReuseConfig()
		c.VerifierPolicy = "factorized-v1"
		c.VerifierPairAssessment = true
		change(&c)
		if _, err := NormalizeReuseConfig(c); err == nil {
			t.Fatal("unsupported assessment policy accepted")
		}
	}
}

func TestPairAssessmentFalsePreservesLegacyIdentityAndOlderDecoderRejects(t *testing.T) {
	c := validReuseConfig()
	c.VerifierPolicy = "factorized-v1"
	old, err := NormalizeReuseConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(old)
	var explicit ReuseConfig
	if json.Unmarshal([]byte(strings.TrimSuffix(string(before), "}")+`,"verifier_pair_assessment":false}`), &explicit) != nil {
		t.Fatal("false rejected")
	}
	after, _ := json.Marshal(explicit)
	if string(before) != string(after) {
		t.Fatal("false altered normalized identity bytes")
	}
	for _, raw := range []string{`{"verifier_pair_assessment":true}`, `{"verifier_pair_assessment":false}`} {
		var old struct {
			VerifierPairThink bool `json:"verifier_pair_think,omitempty"`
		}
		if protocol.DecodeDataJSON([]byte(raw), &old) == nil {
			t.Fatal("legacy strict decoder silently ignored new field")
		}
	}
}
