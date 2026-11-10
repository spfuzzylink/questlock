package worker

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/semantic"
)

func TestIndependentFactorizedOutputKeepsCompositeAndCopiesStageEvidence(t *testing.T) {
	original := reuseResultWithUnselectedResponse()
	decision := "semantic/v1/decisions/" + strings.Repeat("a", 64)
	original.Attempts[0].Result.Resolution.VerificationKey = decision
	v := &original.SemanticReuse.Verifications[0]
	v.ReceiptKey, v.OutputSHA256 = decision, strings.Repeat("a", 64)
	input, output := int64(11), int64(7)
	v.Work = []semantic.VerificationWork{{Stage: "source", GenerationCalled: true, Complete: true, Durable: true, InputTokens: &input, OutputTokens: &output, ReceiptKey: "inference/v1/jobs/" + strings.Repeat("b", 64), OutputSHA256: strings.Repeat("c", 64)}, {Stage: "equivalence", Reused: true, Complete: true, Durable: true, ReceiptKey: "inference/v1/jobs/" + strings.Repeat("d", 64), OutputSHA256: strings.Repeat("e", 64)}}
	before, _ := json.Marshal(original)
	safe := safeOutput(original, nil)
	got := safe.SemanticReuse.Verifications[0]
	if safe.Attempts[0].Result.Resolution.VerificationKey != decision || got.ReceiptKey != decision || !reflect.DeepEqual(got.Work, v.Work) || len(got.Work) != 2 || got.Work[1].InputTokens != nil || got.Work[1].OutputTokens != nil {
		t.Fatalf("safe stage references or unknown usage lost: %+v", safe)
	}
	if &got.Work[0] == &v.Work[0] || got.Work[0].InputTokens == v.Work[0].InputTokens || got.Work[0].OutputTokens == v.Work[0].OutputTokens {
		t.Fatal("safe output aliases executor-owned stage evidence")
	}
	got.Work[0].ReceiptKey = "changed"
	*got.Work[0].InputTokens = 999
	*got.Work[0].OutputTokens = 999
	after, _ := json.Marshal(original)
	if string(after) != string(before) {
		t.Fatal("editing safe output changed original evidence")
	}
}

func TestIndependentFactorizedOutputRedactsArbitraryStageMetadata(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		original := reuseResultWithUnselectedResponse()
		original.Attempts[0].Result.Resolution.VerificationKey = "semantic/v1/decisions/PRIVATE_RECEIPT_SECRET"
		v := &original.SemanticReuse.Verifications[0]
		v.ReceiptKey = "semantic/v1/decisions/PRIVATE_RECEIPT_SECRET"
		v.Work = []semantic.VerificationWork{{Stage: "source", ReceiptKey: "PRIVATE_SOURCE_SECRET", OutputSHA256: "PRIVATE_HASH_SECRET"}, {Stage: "PRIVATE_STAGE_SECRET", ReceiptKey: "PRIVATE_JOB_SECRET"}}
		if oversized {
			v.Work = append(v.Work, semantic.VerificationWork{Stage: "source", ReceiptKey: "PRIVATE_EXTRA_SECRET"})
		}
		before, _ := json.Marshal(original)
		safe := safeOutput(original, nil)
		encoded, err := json.Marshal(safe)
		if err != nil || strings.Contains(string(encoded), "SECRET") {
			t.Fatalf("model-authored metadata crossed MCP boundary: %s %v", encoded, err)
		}
		after, _ := json.Marshal(original)
		if string(after) != string(before) {
			t.Fatal("redaction mutated durable metadata")
		}
	}
}

func TestIndependentApplicabilityMCPOutputHidesUnselectedAnswerAndTaskData(t *testing.T) {
	// Applicability now reads the exact saved answer. Its unselected content
	// remains private even when an executor returns raw verifier/task text.
	const hidden = `{"cached_answer":"UNSELECTED_ANSWER_SECRET","question":"CURRENT_TASK_SECRET","context":"APPROVED_CONTEXT_SECRET","bindings":{"owner":"BOUND_OWNER_SECRET"}}`
	for _, failed := range []bool{false, true} {
		name := "passing selected answer"
		if failed {
			name = "failed selected answer"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			requestFile(t, root, "approved.json", approvedRequest())
			original := reuseResultWithUnselectedResponse()
			decision := "semantic/v1/decisions/" + strings.Repeat("a", 64)
			original.Attempts[0].Result.Response.Text = hidden
			original.Attempts[0].Result.Resolution.Response.Text = hidden
			original.Attempts[0].Result.Resolution.VerificationKey = decision
			v := &original.SemanticReuse.Verifications[0]
			v.ReceiptKey, v.OutputSHA256 = decision, strings.Repeat("a", 64)
			input, output := int64(89), int64(19)
			v.Work = []semantic.VerificationWork{
				{Stage: "source", GenerationCalled: true, Complete: true, Durable: true, InputTokens: &input, OutputTokens: &output, ReceiptKey: "inference/v1/jobs/" + strings.Repeat("b", 64), OutputSHA256: strings.Repeat("c", 64)},
				{Stage: "equivalence", GenerationCalled: true, Complete: true, Durable: true, InputTokens: &input, OutputTokens: &output, ReceiptKey: "inference/v1/jobs/" + strings.Repeat("d", 64), OutputSHA256: strings.Repeat("e", 64)},
			}
			if failed {
				original.Status = "quality_failed"
				original.Response.Text = hidden
			}
			before, _ := json.Marshal(original)
			session, _, _, _ := connectWorker(t, Options{Workspace: root, Execute: func(context.Context, delegation.Request) (delegation.Result, error) {
				if failed {
					return original, errors.Join(delegation.ErrQualityFailed, errors.New(hidden))
				}
				return original, nil
			}}, "2026-07-28")
			wire := call(t, session, map[string]any{"request_path": "approved.json"})
			out := decodeOutput(t, wire)
			encoded, err := json.Marshal(wire)
			if err != nil || strings.Contains(string(encoded), "SECRET") || wire.IsError != failed {
				t.Fatalf("unselected applicability content crossed MCP: %s %v", encoded, err)
			}
			if failed && out.Response != nil || !failed && (out.Response == nil || out.Response.Text != "approved answer") {
				t.Fatal("MCP did not restrict output to the passing selected answer")
			}
			if out.SemanticReuse == nil || len(out.SemanticReuse.Verifications) != 1 || out.SemanticReuse.Verifications[0].ReceiptKey != decision || len(out.SemanticReuse.Verifications[0].Work) != 2 || out.SemanticReuse.Verifications[0].Work[1].Stage != "equivalence" {
				t.Fatal("redaction lost safe composite or stage evidence")
			}
			after, _ := json.Marshal(original)
			if string(after) != string(before) {
				t.Fatal("MCP redaction mutated the executor's source or verifier evidence")
			}
		})
	}
}
