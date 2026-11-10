package delegation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/semantic"
)

func TestRelationV20PreservesSourceFrameAndEveryApprovedPairValue(t *testing.T) {
	v, p := factorizedTestVerifier(t, nil)
	request := factorizedTestRequest()
	request.Context += "\nOpaque approved detail: café Ω, decimal 3.14, and literal punctuation {[]} remain verbatim."
	request.Bindings["source_origin"] = "literal value: 001; unchanged"
	out, err := v.Verify(t.Context(), request)
	if err != nil || !out.Accepted() || len(p.requests) != 2 {
		t.Fatal("native fixture failed", err)
	}
	expectedSource, _ := json.Marshal(struct {
		OriginalQuestion string            `json:"original_question"`
		Instructions     string            `json:"instructions"`
		Context          string            `json:"context"`
		CachedAnswer     string            `json:"cached_answer"`
		Bindings         map[string]string `json:"bindings"`
	}{request.OriginalQuestion, request.Instructions, request.Context, request.CachedAnswer, request.Bindings})
	if p.requests[0].Input != "\n\nTask:\n"+string(expectedSource) {
		t.Fatal("source payload bytes changed")
	}
	for i, stage := range p.requests {
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(strings.TrimPrefix(stage.Input, "\n\nTask:\n")), &fields) != nil {
			t.Fatal("invalid data frame")
		}
		bindingKey, otherKey := "bindings", "source_bindings"
		if i == 1 {
			bindingKey, otherKey = otherKey, bindingKey
		}
		if fields[otherKey] != nil {
			t.Fatal("unexpected binding alias", i)
		}
		var actual map[string]string
		if json.Unmarshal(fields[bindingKey], &actual) != nil || !reflect.DeepEqual(actual, request.Bindings) {
			t.Fatal("approved bindings lost", i)
		}
		want := map[string]string{"context": request.Context, "instructions": request.Instructions, "cached_answer": request.CachedAnswer, "original_question": request.OriginalQuestion}
		if i == 1 {
			want["question"] = request.Question
		}
		if len(fields) != len(want)+1 {
			t.Fatal("data field set changed", i)
		}
		for key, value := range want {
			var got string
			if json.Unmarshal(fields[key], &got) != nil || got != value {
				t.Fatal("approved string changed", i, key)
			}
		}
	}
	hash := sha256.Sum256([]byte(factorizedSupportInstructions))
	if hex.EncodeToString(hash[:]) != "6b49234e307e9072c827c3a05e530ada6219b94bc930745bf468314016dd7141" || !strings.HasPrefix(p.requests[0].Prefix, factorizedSupportInstructions) || factorizedVerifierVersion != "questlock-factorized-validity-v20" || semantic.TargetRiskGuardVersion != 7 {
		t.Fatal("source or version contract changed")
	}
}

func TestRelationGuardWithholdsBeforePairWithoutAcceptShortcut(t *testing.T) {
	v, p := factorizedTestVerifier(t, nil)
	request := factorizedTestRequest()
	request.OriginalQuestion = "Calculate the amount for returning only the silver case."
	request.Question = "Calculate the amount for returning only the black stand while keeping the silver case."
	out, err := v.Verify(t.Context(), request)
	if err != nil || out.Accepted() || out.Verdict != "reject" || len(out.Work) != 1 || p.sourceCalls != 1 || p.pairCalls != 0 || out.InputTokens == nil || *out.InputTokens != 101 || out.OutputTokens == nil || *out.OutputTokens != 20 {
		t.Fatalf("guard omitted source cost or allowed pair: %+v %v", out, err)
	}
}
