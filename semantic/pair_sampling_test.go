package semantic

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestPairSamplingBoundsIsolationAndOwnedNormalization(t *testing.T) {
	for _, temp := range []float64{0, 1, 2, -.01, 2.01, math.NaN(), math.Inf(1), math.Inf(-1)} {
		for _, seed := range []int64{0, 42, 2147483647, -1, 2147483648} {
			c := validReuseConfig()
			c.VerifierPolicy = "factorized-v1"
			temperature, randomSeed := temp, seed
			c.VerifierPairTemperature, c.VerifierPairSeed = &temperature, &randomSeed
			got, err := NormalizeReuseConfig(c)
			valid := !math.IsNaN(temp) && !math.IsInf(temp, 0) && temp >= 0 && temp <= 2 && seed >= 0 && seed <= 2147483647
			if (err == nil) != valid {
				t.Fatalf("bounds temp=%v seed=%d: %v", temp, seed, err)
			}
			if !valid {
				continue
			}
			if got.VerifierPairTemperature == c.VerifierPairTemperature || got.VerifierPairSeed == c.VerifierPairSeed || *got.VerifierPairTemperature != temp || *got.VerifierPairSeed != seed || got.VerifierSourceThink || got.VerifierPairThink || got.VerifierMaxOutputTokens != 128 || got.VerifierTimeoutMS != 30000 || got.TimeoutMS != 60000 {
				t.Fatal("sampling changed limits, flags or aliased caller")
			}
			temperature, randomSeed = 99, 99
			if *got.VerifierPairTemperature != temp || *got.VerifierPairSeed != seed {
				t.Fatal("caller changed normalized config")
			}
			raw, _ := json.Marshal(got)
			var decoded ReuseConfig
			if json.Unmarshal(raw, &decoded) != nil || !reflect.DeepEqual(decoded, got) {
				t.Fatal("pointer values did not round trip")
			}
		}
	}
	for _, temperatureOnly := range []bool{false, true} {
		c := validReuseConfig()
		temperature, seed := float64(0), int64(0)
		if temperatureOnly {
			c.VerifierPairTemperature = &temperature
		} else {
			c.VerifierPairSeed = &seed
		}
		if _, err := NormalizeReuseConfig(c); err == nil {
			t.Fatal("sampling enabled standalone")
		}
	}
	c := validReuseConfig()
	c.VerifierPolicy = "factorized-v1"
	got, err := NormalizeReuseConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "verifier_pair_temperature") || strings.Contains(string(raw), "verifier_pair_seed") {
		t.Fatal("nil changed legacy JSON")
	}
}
