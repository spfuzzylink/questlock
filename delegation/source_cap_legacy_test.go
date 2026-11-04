package delegation

import (
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/semantic"
	"strings"
	"testing"
)

func TestSourceCapZeroPreservesV15IdentityBytes(t *testing.T) {
	c := controlledTestConfig()
	c.VerifierPolicy = "factorized-v1"
	c, e := semantic.NormalizeReuseConfig(c)
	if e != nil {
		t.Fatal(e)
	}
	if got := factorizedPolicyIdentity(c, strings.Repeat("a", 64), strings.Repeat("b", 64)); got != "f650c114870cd94b15bd5d0d154d85ace295ff6a448fc253e03bdbad607a8eb3" {
		t.Fatal("no-option v15 identity changed", got)
	}
}
func TestSourceCapBindsOnlyExplicitSourceContextAndKeepsFullInput(t *testing.T) {
	v, _, _ := mixedFactorizedFixture(t)
	v.config.VerifierMaxInputTokens = 13312
	v.config.VerifierPairMaxInputTokens = 6656
	old := factorizedPolicyIdentity(v.config, v.sourceRuntime, v.pairRuntime)
	v.config.VerifierSourceMaxInputTokens = 5376
	if factorizedPolicyIdentity(v.config, v.sourceRuntime, v.pairRuntime) == old {
		t.Fatal("new cap did not bind identity")
	}
	source := v.stageSpec("source", "same-revision", factorizedSupportInstructions, `{"context":"Full approved evidence","cached_answer":"Unchanged full answer"}`, "supported", "complete")
	pair := v.stageSpec("equivalence", "same-revision", factorizedPairInstructions, source.Input, "answer_covers_current", "unchanged_facts")
	if source.MaxInputTokens != 5376 || pair.MaxInputTokens != 6656 || source.MaxOutputTokens != 64 || pair.MaxOutputTokens != 64 || source.Input != pair.Input || source.Instructions != factorizedSupportInstructions || pair.Instructions != factorizedPairInstructions {
		t.Fatal("cap option changed content or other stage")
	}
	for _, tc := range []struct {
		s       inference.Spec
		context int64
	}{{source, 5952}, {pair, 7232}} {
		p, e := inference.BuildPlan(tc.s)
		if e != nil || p.Request.ContextWindow != tc.context || !strings.Contains(p.Request.Input, tc.s.Input) {
			t.Fatal("native context/full input changed", p, e)
		}
	}
}
