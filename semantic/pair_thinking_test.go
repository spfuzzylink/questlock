package semantic

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/protocol"
)

func TestPairThinkingIsIndependentOptionalAndBounded(t *testing.T) {
	for _, tc := range []struct {
		name         string
		source, pair bool
		output       int64
		deadline     int
	}{
		{"unchanged", false, false, 128, 30000},
		{"source only", true, false, 4096, 120000},
		{"pair only", false, true, 4096, 120000},
		{"both", true, true, 4096, 120000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validReuseConfig()
			c.VerifierPolicy = "factorized-v1"
			c.VerifierSourceThink, c.VerifierPairThink = tc.source, tc.pair
			got, err := NormalizeReuseConfig(c)
			if err != nil || got.VerifierSourceThink != tc.source || got.VerifierPairThink != tc.pair || got.VerifierMaxOutputTokens != tc.output || got.VerifierTimeoutMS != tc.deadline {
				t.Fatalf("stage thinking defaults changed: %+v %v", got, err)
			}
			whole := 60000
			if tc.source || tc.pair {
				whole = 120000
			}
			if got.TimeoutMS != whole {
				t.Fatal("whole-path deadline did not follow explicit reasoning opt-in")
			}
			encoded, _ := json.Marshal(got)
			if strings.Contains(string(encoded), "verifier_pair_think") != tc.pair {
				t.Fatal("false pair setting changed legacy serialization")
			}
			var roundTrip ReuseConfig
			if json.Unmarshal(encoded, &roundTrip) != nil || !reflect.DeepEqual(roundTrip, got) {
				t.Fatal("optional reasoning did not round-trip")
			}
		})
	}
	for _, policy := range []string{"", "unknown"} {
		c := validReuseConfig()
		c.VerifierPolicy = policy
		c.VerifierPairThink = true
		if _, err := NormalizeReuseConfig(c); err == nil {
			t.Fatal("pair reasoning enabled a non-factorized policy")
		}
	}
	for _, limits := range []struct {
		out             int64
		whole, verifier int
		valid           bool
	}{{32, 1, 1, true}, {128, 750, 900, true}, {4096, 120000, 120000, true}, {31, 120000, 120000, false}, {4097, 120000, 120000, false}, {4096, 120001, 120000, false}, {4096, 120000, 120001, false}} {
		c := validReuseConfig()
		c.VerifierPolicy = "factorized-v1"
		c.VerifierPairThink = true
		c.VerifierMaxOutputTokens, c.TimeoutMS, c.VerifierTimeoutMS = limits.out, limits.whole, limits.verifier
		got, err := NormalizeReuseConfig(c)
		if (err == nil) != limits.valid {
			t.Fatalf("thinking limit validation mismatch: %+v %v", limits, err)
		}
		if limits.valid && (got.VerifierMaxOutputTokens != limits.out || got.TimeoutMS != limits.whole || got.VerifierTimeoutMS != limits.verifier) {
			t.Fatal("explicit shorter limits were replaced")
		}
	}
}

func TestPairThinkingFalsePreservesOldJSONAndRejectsOlderDecoder(t *testing.T) {
	c := validReuseConfig()
	c.VerifierPolicy = "factorized-v1"
	old, err := NormalizeReuseConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(old)
	var explicit ReuseConfig
	if json.Unmarshal([]byte(strings.TrimSuffix(string(before), "}")+`,"verifier_pair_think":false}`), &explicit) != nil {
		t.Fatal("explicit false was rejected")
	}
	after, _ := json.Marshal(explicit)
	if string(after) != string(before) {
		t.Fatal("false changed existing normalized policy bytes")
	}
	for _, raw := range []string{`{"verifier_pair_think":true}`, `{"verifier_pair_think":false}`} {
		var legacy struct {
			VerifierSourceThink bool `json:"verifier_source_think,omitempty"`
		}
		if protocol.DecodeDataJSON([]byte(raw), &legacy) == nil {
			t.Fatal("olderstrictdecoder silently ignored a new field")
		}
	}
}
