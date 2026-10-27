package worker

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/semantic"
)

func reuseResultWithUnselectedResponse() delegation.Result {
	result := completedResult()
	result.Attempts[0].Result.Resolution = &inference.ReuseResolution{
		PolicyID: strings.Repeat("b", 64), VerificationKey: "inference/v1/jobs/" + strings.Repeat("c", 64),
		Source:   inference.ReuseSource{Key: "inference/v1/jobs/" + strings.Repeat("d", 64), Version: 2, SHA256: strings.Repeat("e", 64), OutputSHA256: strings.Repeat("f", 64), RequestFingerprint: strings.Repeat("a", 64)},
		Response: inference.Response{Text: "NESTED_FAILED_CONTENT_SECRET", ID: "NESTED_PROVIDER_ID_SECRET", Model: "NESTED_MODEL_SECRET", StopReason: "NESTED_STOP_SECRET", Complete: true, Usage: inference.Usage{InputTokens: 3, UncachedInputTokens: 3, OutputTokens: 2}},
	}
	input, output, embedded := int64(11), int64(7), int64(5)
	result.SemanticReuse = &delegation.ReuseObservation{
		Mode: "controlled", Status: "reused", Partition: strings.Repeat("a", 64),
		Source:               &semantic.Source{Key: "inference/v1/jobs/" + strings.Repeat("d", 64), Version: 2, SHA256: strings.Repeat("e", 64), OutputSHA256: strings.Repeat("f", 64)},
		EmbeddingInputTokens: &embedded, GenerationAvoided: true, HistoricalUsageIsProxy: true,
		Verifications: []semantic.ValidityResult{{Verdict: "accept", SameIntent: true, SameFacts: true, SameConstraints: true, AnswerSupported: true, InputTokens: &input, OutputTokens: &output, ReceiptKey: "inference/v1/jobs/" + strings.Repeat("c", 64), GenerationCalled: true, Complete: true, Durable: true}},
		Rejections:    []string{"protected_numbers_changed"},
	}
	return result
}

func TestFaultyExecutorCannotExposeNestedReuseResponse(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "success"
		if failed {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			requestFile(t, root, "approved.json", approvedRequest())
			original := reuseResultWithUnselectedResponse()
			if failed {
				original.Status = "quality_failed"
				original.Response.Text = "TOP_LEVEL_FAILED_CONTENT_SECRET"
			}
			before, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			session, _, _, _ := connectWorker(t, Options{Workspace: root, Execute: func(context.Context, delegation.Request) (delegation.Result, error) {
				if failed {
					return original, errors.Join(delegation.ErrQualityFailed, errors.New("RAW_ERROR_SECRET"))
				}
				return original, nil
			}}, "2026-07-28")
			wire := call(t, session, map[string]any{"request_path": "approved.json"})
			output := decodeOutput(t, wire)
			if wire.IsError != failed || len(output.Attempts) != 1 || output.Attempts[0].Result.Resolution == nil || !reflect.DeepEqual(output.Attempts[0].Result.Resolution.Response, inference.Response{}) {
				t.Fatalf("nested response was not stripped: %+v", output)
			}
			if failed && (output.Response != nil || output.SelectedTier != "") {
				t.Fatal("failed call exposed the selected response")
			}
			if !failed && (output.Response == nil || output.Response.Text != "approved answer") {
				t.Fatal("successful call lost the selected passing answer")
			}
			encoded, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "SECRET") || strings.Contains(string(encoded), "FAILED_OUTPUT") {
				t.Fatalf("MCP returned unselected response content: %s", encoded)
			}
			after, err := json.Marshal(original)
			if err != nil || string(before) != string(after) {
				t.Fatal("safe output mutated the executor's durable result")
			}
		})
	}
}

func TestSafeOutputCopiesReuseMetadataAndPreservesSafeReferences(t *testing.T) {
	original := reuseResultWithUnselectedResponse()
	output := safeOutput(original, nil)
	resolution := output.Attempts[0].Result.Resolution
	observation := output.SemanticReuse
	if resolution == original.Attempts[0].Result.Resolution || observation == original.SemanticReuse || observation.Source == original.SemanticReuse.Source || observation.EmbeddingInputTokens == original.SemanticReuse.EmbeddingInputTokens || observation.Verifications[0].InputTokens == original.SemanticReuse.Verifications[0].InputTokens || observation.Verifications[0].OutputTokens == original.SemanticReuse.Verifications[0].OutputTokens {
		t.Fatal("safe output shares mutable caller-owned reuse metadata")
	}
	if resolution.Source != original.Attempts[0].Result.Resolution.Source || resolution.PolicyID != original.Attempts[0].Result.Resolution.PolicyID || resolution.VerificationKey != original.Attempts[0].Result.Resolution.VerificationKey || !reflect.DeepEqual(observation, original.SemanticReuse) {
		t.Fatal("safe reuse references or controlled metadata were lost")
	}
	resolution.Source.Version = 99
	observation.Source.Version = 99
	*observation.EmbeddingInputTokens = 99
	*observation.Verifications[0].InputTokens = 99
	*observation.Verifications[0].OutputTokens = 99
	observation.Rejections[0] = "changed"
	if original.Attempts[0].Result.Resolution.Source.Version != 2 || original.SemanticReuse.Source.Version != 2 || *original.SemanticReuse.EmbeddingInputTokens != 5 || *original.SemanticReuse.Verifications[0].InputTokens != 11 || *original.SemanticReuse.Verifications[0].OutputTokens != 7 || original.SemanticReuse.Rejections[0] != "protected_numbers_changed" || original.Attempts[0].Result.Resolution.Response.Text != "NESTED_FAILED_CONTENT_SECRET" {
		t.Fatal("editing safe output changed original durable evidence")
	}
}

func TestSafeOutputRejectsRawTextInReuseLabelsAndReferences(t *testing.T) {
	original := reuseResultWithUnselectedResponse()
	original.Attempts[0].Result.ReuseKind = "REUSE_KIND_SECRET"
	resolution := original.Attempts[0].Result.Resolution
	resolution.PolicyID, resolution.VerificationKey = "POLICY_SECRET", "RECEIPT_SECRET"
	resolution.Source = inference.ReuseSource{Key: "SOURCE_SECRET", SHA256: "HASH_SECRET", OutputSHA256: "OUTPUT_HASH_SECRET", RequestFingerprint: "FINGERPRINT_SECRET"}
	observation := original.SemanticReuse
	observation.Mode, observation.Status, observation.Partition = "MODE_SECRET", "STATUS_SECRET", "PARTITION_SECRET"
	observation.Source.Key = "SOURCE_SECRET"
	observation.Rejections = append(observation.Rejections, "REJECTION_SECRET")
	observation.Verifications[0].Verdict = "VERDICT_SECRET"
	observation.Verifications[0].ReceiptKey = "VERIFIER_RECEIPT_SECRET"
	before, _ := json.Marshal(original)
	output := safeOutput(original, nil)
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "SECRET") || output.SemanticReuse.Status != "unavailable" || output.SemanticReuse.Source != nil || output.SemanticReuse.Verifications[0].Verdict != "unknown" || !reflect.DeepEqual(output.SemanticReuse.Rejections, []string{"protected_numbers_changed"}) {
		t.Fatalf("raw reuse text crossed the output boundary: %s", encoded)
	}
	after, _ := json.Marshal(original)
	if string(before) != string(after) {
		t.Fatal("label redaction mutated original result")
	}
}
