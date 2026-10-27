package worker

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/delegation"
)

func TestNewOutcomeErrorCategoryIsCopiedAndAllowlisted(t *testing.T) {
	for _, category := range []string{"", "shared_work_requires_batch_accounting", "PRIVATE_ERROR_SECRET"} {
		original := completedResult()
		original.OutcomeTracking = &delegation.OutcomeTracking{Status: "failed", ErrorCategory: category}
		result := safeOutput(original, nil)
		if result.OutcomeTracking == original.OutcomeTracking || original.OutcomeTracking.ErrorCategory != category {
			t.Fatal("boundary mutated executor metadata")
		}
		encoded, err := json.Marshal(result)
		if err != nil || strings.Contains(string(encoded), "PRIVATE_ERROR_SECRET") {
			t.Fatalf("untrusted error escaped: %s %v", encoded, err)
		}
		if category != "PRIVATE_ERROR_SECRET" && result.OutcomeTracking.ErrorCategory != category {
			t.Fatal("safe refusal lost")
		}
	}
}
