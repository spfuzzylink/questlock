package protocol

import (
	"encoding/json"
	"testing"
)

func TestDataJSONPreservesExpectedCaseWithoutAliases(t *testing.T) {
	var v struct {
		Expected json.RawMessage `json:"expected"`
	}
	if err := DecodeDataJSON([]byte(`{"expected":{"IssueID":"OPS-17","optional":null}}`), &v); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"Expected":{}}`, `{"expected":{"a":1,"a":2}}`, `{"expected":{"A":1,"a":2}}`, `{"expected":"\ud800"}`, `{"expected":{}} {}`} {
		if DecodeDataJSON([]byte(bad), &v) == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
