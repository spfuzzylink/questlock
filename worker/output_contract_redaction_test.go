package worker

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestOutputContractDiagnosticsAreFixedCodesOnly(t *testing.T) {
	codes := []string{"invalid_output_contract_text", "output_contract_conflict", "output_numeric_contract_mismatch", "output_json_contract_mismatch", "output_csv_contract_mismatch", "output_tsv_contract_mismatch", "output_table_contract_mismatch", "output_prose_contract_mismatch", "output_no_json_contract_mismatch", "invalid_language_contract_text", "output_language_changed", "output_language_unknown", "output_language_source_unknown", "output_language_ambiguous"}
	original := reuseResultWithUnselectedResponse()
	original.SemanticReuse.Rejections = append(append([]string{}, codes...), "output_csv_contract_mismatch:PRIVATE_QUESTION", "output_language_changed:PRIVATE_LANGUAGE", "PRIVATE_ANSWER")
	output := safeOutput(original, nil)
	data, err := json.Marshal(output)
	if err != nil || !reflect.DeepEqual(output.SemanticReuse.Rejections, codes) || strings.Contains(string(data), "PRIVATE") {
		t.Fatalf("format diagnostic crossed its fixed-code boundary: %s %v", data, err)
	}
	output.SemanticReuse.Rejections[0] = "changed"
	if original.SemanticReuse.Rejections[0] != codes[0] {
		t.Fatal("diagnostic redaction mutated the original observation")
	}
}
