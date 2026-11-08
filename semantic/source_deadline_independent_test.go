package semantic

import "testing"

func TestIndependentSourceDeadlineOptInDoesNotBroadenShadowOrPublishedReuse(t *testing.T) {
	shadow, err := NormalizeConfig(validConfig())
	if err != nil || shadow.TimeoutMS != 10000 {
		t.Fatalf("published shadow default changed: %+v %v", shadow, err)
	}
	for _, deadline := range []int{60000, 60001, 120000} {
		c := validConfig()
		c.TimeoutMS = deadline
		_, err := NormalizeConfig(c)
		if (err == nil) != (deadline == 60000) {
			t.Fatalf("shadow bound changed for %d: %v", deadline, err)
		}
	}
	for _, policy := range []string{"", "factorized-v1"} {
		c := validReuseConfig()
		c.VerifierPolicy = policy
		got, err := NormalizeReuseConfig(c)
		if err != nil || got.TimeoutMS != 60000 || got.VerifierTimeoutMS != 30000 {
			t.Fatalf("published no-thinking deadlines changed: %+v %v", got, err)
		}
		c.TimeoutMS = 60001
		if _, err := NormalizeReuseConfig(c); err == nil {
			t.Fatal("unrequested reasoning deadline enabled on existing policy")
		}
	}
	c := validReuseConfig()
	c.VerifierPolicy, c.VerifierSourceThink = "factorized-v1", true
	got, err := NormalizeReuseConfig(c)
	if err != nil || got.TimeoutMS != 120000 || got.VerifierTimeoutMS != 120000 {
		t.Fatalf("explicit reasoning did not get a usable bounded resolver deadline: %+v %v", got, err)
	}
	for _, explicit := range []int{1, 15000, 60000, 120000} {
		c.TimeoutMS, c.VerifierTimeoutMS = explicit, explicit
		got, err := NormalizeReuseConfig(c)
		if err != nil || got.TimeoutMS != explicit || got.VerifierTimeoutMS != explicit {
			t.Fatalf("explicit smaller deadline was replaced: %+v %v", got, err)
		}
	}
	for _, bad := range []int{-1, 600001} {
		c.TimeoutMS, c.VerifierTimeoutMS = bad, 120000
		if _, err := NormalizeReuseConfig(c); err == nil {
			t.Fatal("source reasoning bypassed the resolver deadline ceiling")
		}
		c.TimeoutMS, c.VerifierTimeoutMS = 120000, bad
		if _, err := NormalizeReuseConfig(c); err == nil {
			t.Fatal("source reasoning bypassed the verifier deadline ceiling")
		}
	}
}
