package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSyncStrictJSONBoundsAndUnicode(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `{} {}`, `{"cursor":"one","cursor":"two"}`, `{"Cursor":"one","cursor":"two"}`, `{"limit":null}`, `{"max_bytes":null}`, `{"limit":0}`, `{"limit":1e0}`, `{"scope":"other"}`, `{"cursor":"\ud800"}`, `{"cursor":"\udc00"}`} {
		if _, err := DecodeSyncRequest([]byte(body)); err == nil {
			t.Fatalf("accepted invalid sync request: %s", body)
		}
	}
	for _, body := range []string{`{}`, `{"cursor":""}`, `{"cursor":"\ud83d\ude00"}`, `{"cursor":"\\ud800"}`, `{"limit":128,"max_bytes":8388608}`} {
		if _, err := DecodeSyncRequest([]byte(body)); err != nil {
			t.Fatalf("valid request rejected: %s: %v", body, err)
		}
	}
	for _, body := range []string{`{"entries":[` + strings.Repeat(`{},`, 128) + `{}]}`, `{"x":` + strings.Repeat(`[`, 18) + `0` + strings.Repeat(`]`, 18) + `}`} {
		var value any
		if err := DecodeSyncJSON([]byte(body), &value); err == nil {
			t.Fatal("unbounded JSON container accepted")
		}
	}
}

func TestSyncPageRequiresEveryNonNullFieldAndPreservesEmptyContent(t *testing.T) {
	page := SyncPage{Authority: strings.Repeat("a", 64), Scope: "scope", Kind: "snapshot", Through: 1, Cursor: "opaque", Entries: []SyncEntry{{Sequence: 1, Artifact: Artifact{Scope: "scope", Key: "a", Version: 1, Content: "", UpdatedAt: "2026-10-16T00:00:00Z"}}}}
	data, _ := json.Marshal(page)
	got, err := DecodeSyncPage(data)
	if err != nil || got.Entries[0].Artifact.Content != "" {
		t.Fatal("intentionally empty content was rejected", err)
	}
	for _, level := range []struct {
		name   string
		fields []string
	}{
		{"envelope", []string{"authority", "scope", "kind", "through", "entries", "cursor", "has_more"}},
		{"entry", []string{"sequence", "artifact"}},
		{"artifact", []string{"scope", "key", "version", "content", "updated_at"}},
	} {
		for _, field := range level.fields {
			for _, mode := range []string{"missing", "null"} {
				t.Run(level.name+"/"+field+"/"+mode, func(t *testing.T) {
					var object map[string]any
					if err := json.Unmarshal(data, &object); err != nil {
						t.Fatal(err)
					}
					target := object
					if level.name != "envelope" {
						target = object["entries"].([]any)[0].(map[string]any)
					}
					if level.name == "artifact" {
						target = target["artifact"].(map[string]any)
					}
					if mode == "missing" {
						delete(target, field)
					} else {
						target[field] = nil
					}
					raw, _ := json.Marshal(object)
					if decoded, err := DecodeSyncPage(raw); err == nil || len(decoded.Entries) != 0 || decoded.Cursor != "" {
						t.Fatalf("ambiguous page exposed: %+v %v", decoded, err)
					}
				})
			}
		}
	}
}
