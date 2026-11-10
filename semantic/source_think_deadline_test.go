package semantic

import (
	"reflect"
	"testing"
)

func TestSourceThinkingDeadlineDoesNotBroadenShadowOrOtherControlledPolicies(t *testing.T) {
	if MaxTimeoutMS != 60000 || DefaultTimeoutMS != 10000 || DefaultVerifierTimeoutMS != 30000 {
		t.Fatal("published shadow or verifier defaults changed")
	}
	for _, timeout := range []int{60000, 60001, 120000} {
		c := validConfig()
		c.TimeoutMS = timeout
		got, err := NormalizeConfig(c)
		if timeout <= 60000 {
			if err != nil || got.TimeoutMS != timeout {
				t.Fatalf("valid shadow deadline changed: %+v %v", got, err)
			}
		} else if err == nil || !reflect.DeepEqual(got, Config{}) {
			t.Fatal("ordinary shadow deadline was raised")
		}
	}
	for _, policy := range []string{"", "factorized-v1"} {
		c := validReuseConfig()
		c.VerifierPolicy = policy
		got, err := NormalizeReuseConfig(c)
		if err != nil || got.TimeoutMS != 60000 || got.VerifierTimeoutMS != 30000 {
			t.Fatalf("no-thinking controlled defaults changed: %+v %v", got, err)
		}
		c.TimeoutMS = 60001
		if got, err = NormalizeReuseConfig(c); err == nil || !reflect.DeepEqual(got, ReuseConfig{}) {
			t.Fatal("no-thinking controlled deadline was raised")
		}
	}
}

func TestSourceThinkingHasBoundedWholePathAndVerifierDeadlines(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		whole, verifier         int
		wantWhole, wantVerifier int
		valid                   bool
	}{
		{"explicit thinking defaults", 0, 0, 120000, 120000, true},
		{"inclusive maximum", 120000, 120000, 120000, 120000, true},
		{"smaller whole path retained", 750, 0, 750, 120000, true},
		{"smaller verifier retained", 0, 900, 120000, 900, true},
		{"both smaller retained", 32000, 1000, 32000, 1000, true},
		{"whole path too long", 600001, 120000, 0, 0, false},
		{"verifier too long", 120000, 600001, 0, 0, false},
		{"negative whole path", -1, 120000, 0, 0, false},
		{"negative verifier", 120000, -1, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validReuseConfig()
			c.VerifierPolicy = "factorized-v1"
			c.VerifierSourceThink = true
			c.TimeoutMS = tc.whole
			c.VerifierTimeoutMS = tc.verifier
			got, err := NormalizeReuseConfig(c)
			if tc.valid {
				if err != nil || got.TimeoutMS != tc.wantWhole || got.VerifierTimeoutMS != tc.wantVerifier {
					t.Fatalf("thinking deadline drift: %+v %v", got, err)
				}
			} else if err == nil || !reflect.DeepEqual(got, ReuseConfig{}) {
				t.Fatalf("unbounded thinking config accepted: %+v %v", got, err)
			}
		})
	}
	for _, policy := range []string{"", "other-policy"} {
		c := validReuseConfig()
		c.VerifierPolicy = policy
		c.VerifierSourceThink = true
		c.TimeoutMS = 120000
		if _, err := NormalizeReuseConfig(c); err == nil {
			t.Fatal("thinking bypassed explicit factorized-policy requirement")
		}
	}
}
