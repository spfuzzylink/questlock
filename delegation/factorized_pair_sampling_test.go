package delegation

import (
	"encoding/json"
	"github.com/spfuzzylink/questlock/inference"
	"strings"
	"testing"
)

func TestFactorizedPairSamplingIdentityAndStageSnapshots(t *testing.T) {
	v, _ := factorizedTestVerifier(t, nil)
	identity := factorizedPolicyIdentity(v.config, strings.Repeat("a", 64), strings.Repeat("b", 64))
	source := v.stageSpec("source", "same", factorizedSupportInstructions, `{"data":"whole"}`, "supported", "complete")
	pair := v.stageSpec("equivalence", "same", factorizedPairInstructions, `{"data":"whole"}`, "answer_covers_current", "unchanged_facts")
	legacy, _ := inference.BuildPlan(pair)
	for _, seed := range []int64{0, 42} {
		temp := float64(1)
		v.config.VerifierPairTemperature, v.config.VerifierPairSeed = &temp, &seed
		if factorizedPolicyIdentity(v.config, strings.Repeat("a", 64), strings.Repeat("b", 64)) == identity {
			t.Fatal("explicit sampling did not bind policy")
		}
		s := v.stageSpec("source", "same", factorizedSupportInstructions, `{"data":"whole"}`, "supported", "complete")
		old, _ := json.Marshal(source)
		now, _ := json.Marshal(s)
		if string(old) != string(now) {
			t.Fatal("pair sampling altered source")
		}
		p := v.stageSpec("equivalence", "same", factorizedPairInstructions, `{"data":"whole"}`, "answer_covers_current", "unchanged_facts")
		plan, err := inference.BuildPlan(p)
		if err != nil || plan.Fingerprint == legacy.Fingerprint || p.Ollama.Seed == v.config.VerifierPairSeed || p.Ollama.Temperature == v.config.VerifierPairTemperature || *p.Ollama.Seed != seed || *p.Ollama.Temperature != 1 {
			t.Fatal("pair sampling missing, shared or unbound", err)
		}
		temp = 2
		seed = 7
		if *p.Ollama.Temperature != 1 || *plan.Request.Ollama.Seed == 7 {
			t.Fatal("stage options drifted")
		}
	}
	v.config.VerifierPairTemperature, v.config.VerifierPairSeed = nil, nil
	if factorizedPolicyIdentity(v.config, strings.Repeat("a", 64), strings.Repeat("b", 64)) != identity {
		t.Fatal("nil changed v17 identity")
	}
}
