package worker

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/delegation"
)

func TestTargetRiskReasonsRemainClosedAndCopied(t *testing.T) {
	codes := []string{"target_risk_invalid_text", "target_risk_binary_orientation", "target_risk_scope_expansion", "target_risk_authority_change", "target_risk_operation_object_change"}
	original := &delegation.ReuseObservation{Status: "miss_or_rejected", Rejections: append(append([]string{}, codes...), "target_risk_binary_orientation:PRIVATE_QUESTION", "target_risk_operation_object_change:PRIVATE_QUESTION", "target_risk_operation_object_change_PRIVATE_ANSWER", "TARGET_RISK_OPERATION_OBJECT_CHANGE", " target_risk_operation_object_change", "target_risk_operation_object_change\nPRIVATE_ANSWER", "PRIVATE_ANSWER")}
	out := safeSemanticReuse(original)
	if out == nil || !reflect.DeepEqual(out.Rejections, codes) {
		t.Fatalf("risk reasons lost or arbitrary text escaped: %+v", out)
	}
	encoded, err := json.Marshal(out)
	if err != nil || strings.Contains(string(encoded), "PRIVATE_") {
		t.Fatal("lookalike rejection leaked private text")
	}
	out.Rejections[len(codes)-1] = "changed new reason"
	if original.Rejections[len(codes)-1] != codes[len(codes)-1] {
		t.Fatal("new safe reason aliases the original")
	}
	out.Rejections[0] = "changed"
	if original.Rejections[0] != codes[0] {
		t.Fatal("safe risk reasons alias the original")
	}
}

func TestOperationObjectReasonPropagatesThroughMCPWithoutLookalikeText(t *testing.T) {
	root := t.TempDir()
	requestFile(t, root, "approved.json", approvedRequest())
	original := completedResult()
	original.SemanticReuse = &delegation.ReuseObservation{Status: "miss_or_rejected", Rejections: []string{"target_risk_operation_object_change", "target_risk_operation_object_change:PRIVATE_QUESTION"}}
	before, _ := json.Marshal(original)
	session, _, _, _ := connectWorker(t, Options{Workspace: root, Execute: func(context.Context, delegation.Request) (delegation.Result, error) { return original, nil }}, "")
	wire := call(t, session, map[string]any{"request_path": "approved.json"})
	out := decodeOutput(t, wire)
	encoded, err := json.Marshal(wire)
	if err != nil || wire.IsError || out.SemanticReuse == nil || !reflect.DeepEqual(out.SemanticReuse.Rejections, []string{"target_risk_operation_object_change"}) || strings.Contains(string(encoded), "PRIVATE_") {
		t.Fatal("MCP lost exact reason or leaked lookalike text")
	}
	after, _ := json.Marshal(original)
	if string(before) != string(after) {
		t.Fatal("MCP redaction mutated original evidence")
	}
}
