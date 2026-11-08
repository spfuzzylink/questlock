package worker

import (
	"reflect"
	"testing"

	"github.com/spfuzzylink/questlock/delegation"
)

func TestTargetRiskReasonsRemainClosedAndCopied(t *testing.T) {
	codes := []string{"target_risk_invalid_text", "target_risk_binary_orientation", "target_risk_scope_expansion", "target_risk_authority_change"}
	original := &delegation.ReuseObservation{Status: "miss_or_rejected", Rejections: append(append([]string{}, codes...), "target_risk_binary_orientation:PRIVATE_QUESTION", "PRIVATE_ANSWER")}
	out := safeSemanticReuse(original)
	if out == nil || !reflect.DeepEqual(out.Rejections, codes) {
		t.Fatalf("risk reasons lost or arbitrary text escaped: %+v", out)
	}
	out.Rejections[0] = "changed"
	if original.Rejections[0] != codes[0] {
		t.Fatal("safe risk reasons alias the original")
	}
}
