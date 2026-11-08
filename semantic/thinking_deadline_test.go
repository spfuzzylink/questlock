package semantic

import (
	"reflect"
	"testing"
)

func TestThinkingDeadlineExplicitCeilingDefaultsAndIdempotence(t *testing.T) {
	if MaxVerifierTimeoutMS != 120000 || MaxFactorizedThinkingTimeoutMS != 600000 || DefaultVerifierTimeoutMS != 30000 || MaxTimeoutMS != 60000 {
		t.Fatal("legacy limits/defaults changed")
	}
	for _, flags := range []struct{ source, pair bool }{{true, false}, {false, true}, {true, true}} {
		for _, tc := range []struct {
			whole, verifier, wantWhole, wantVerifier int
			valid                                    bool
		}{
			{0, 0, 120000, 120000, true}, {0, 240000, 240000, 240000, true}, {0, 600000, 600000, 600000, true}, {0, 120001, 120001, 120001, true},
			{45000, 240000, 45000, 240000, true}, {600000, 120000, 600000, 120000, true}, {0, 900, 120000, 900, true},
			{0, 600001, 0, 0, false}, {600001, 120000, 0, 0, false}, {-1, 120000, 0, 0, false}, {120000, -1, 0, 0, false},
		} {
			c := validReuseConfig()
			c.VerifierPolicy = "factorized-v1"
			c.VerifierSourceThink, c.VerifierPairThink = flags.source, flags.pair
			c.TimeoutMS, c.VerifierTimeoutMS = tc.whole, tc.verifier
			got, err := NormalizeReuseConfig(c)
			if (err == nil) != tc.valid {
				t.Fatalf("flags=%+v limits=%+v: %v", flags, tc, err)
			}
			if !tc.valid {
				continue
			}
			if got.TimeoutMS != tc.wantWhole || got.VerifierTimeoutMS != tc.wantVerifier || got.VerifierMaxOutputTokens != 4096 {
				t.Fatal("explicit/default timeout or output changed", got)
			}
			again, err := NormalizeReuseConfig(got)
			if err != nil || !reflect.DeepEqual(again, got) {
				t.Fatal("normalization reinterpreted a nonzero deadline")
			}
		}
	}
	for _, policy := range []string{"", "factorized-v1"} {
		c := validReuseConfig()
		c.VerifierPolicy = policy
		c.VerifierTimeoutMS = 120001
		if _, err := NormalizeReuseConfig(c); err == nil {
			t.Fatal("nonthinking verifier limit broadened")
		}
		c.VerifierTimeoutMS = 120000
		c.TimeoutMS = 60001
		if _, err := NormalizeReuseConfig(c); err == nil {
			t.Fatal("nonthinking resolver limit broadened")
		}
	}
	// A previously normalized nonzero deadline is caller-selected thereafter.
	c, _ := NormalizeReuseConfig(validReuseConfig())
	c.VerifierPolicy = "factorized-v1"
	c.VerifierPairThink = true
	c.VerifierTimeoutMS = 240000
	got, err := NormalizeReuseConfig(c)
	if err != nil || got.TimeoutMS != 60000 {
		t.Fatal("previous nonzero deadline was silently expanded", err)
	}
}
