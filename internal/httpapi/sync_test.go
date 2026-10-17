package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/protocol"
)

func TestSyncAuthenticationStrictInputAndNoIngestion(t *testing.T) {
	s, handler, token := setup(t)
	initial := protocol.PublishRequest{Key: "state", OperationID: "create", Content: "authoritative"}
	if got := publish(t, handler, token, initial); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	for _, tc := range []struct {
		method, token, body string
		status              int
	}{
		{http.MethodGet, token, "{}", 405}, {http.MethodPost, "", "{}", 401}, {http.MethodPost, "wrong", "{}", 401},
		{http.MethodPost, token, "{}", 200}, {http.MethodPost, token, "null", 400}, {http.MethodPost, token, "[]", 400},
		{http.MethodPost, token, `{} {}`, 400}, {http.MethodPost, token, `{"limit":1,"limit":2}`, 400},
		{http.MethodPost, token, `{"cursor":null}`, 400}, {http.MethodPost, token, `{"limit":0}`, 400},
		{http.MethodPost, token, `{"limit":129}`, 400}, {http.MethodPost, token, `{"max_bytes":4095}`, 400},
		{http.MethodPost, token, `{"limit":1.5}`, 400}, {http.MethodPost, token, `{"Limit":1}`, 400},
		{http.MethodPost, token, `{"scope":"other"}`, 400}, {http.MethodPost, token, `{"agent":"trusted-admin"}`, 400},
		{http.MethodPost, token, `{"current_version":999}`, 400}, {http.MethodPost, token, `{"journal_entries":[{"content":"offline overwrite"}]}`, 400},
		{http.MethodPost, token, "{\"cursor\":\"\xff\"}", 400},
		{http.MethodPost, token, strings.Repeat(" ", protocol.MaxSyncRequestBytes) + "{}", 413},
	} {
		got := request(t, handler, tc.method, "/v1/sync", tc.token, strings.NewReader(tc.body))
		if got.Code != tc.status {
			t.Fatalf("%s %q: status %d want %d: %s", tc.method, tc.body[:min(80, len(tc.body))], got.Code, tc.status, got.Body.String())
		}
	}
	if got := request(t, handler, http.MethodPost, "/v1/sync?scope=other", token, strings.NewReader("{}")); got.Code != 400 {
		t.Fatal("sync query authority accepted")
	}
	got := request(t, handler, http.MethodGet, "/v1/artifact?key=state", token, nil)
	var artifact protocol.Artifact
	if err := json.Unmarshal(got.Body.Bytes(), &artifact); err != nil || artifact.Content != "authoritative" || artifact.Version != 1 {
		t.Fatal("read-only sync changed authoritative artifact")
	}
	if err := s.RevokeAgent(context.Background(), "worker-a"); err != nil {
		t.Fatal(err)
	}
	if got := request(t, handler, http.MethodPost, "/v1/sync", token, strings.NewReader("{}")); got.Code != 401 {
		t.Fatal("revoked sync token accepted")
	}
}

func TestSyncScopeCursorAndWireByteBounds(t *testing.T) {
	s, handler, token := setup(t)
	other, err := s.CreateToken(context.Background(), "other", "other-scope")
	if err != nil {
		t.Fatal(err)
	}
	for i, key := range []string{"one", "two", "three"} {
		body := protocol.PublishRequest{Key: key, OperationID: key, Content: strings.Repeat("payload", 100)}
		if got := publish(t, handler, token, body); got.Code != 200 {
			t.Fatal(i, got.Body.String())
		}
	}
	first := request(t, handler, http.MethodPost, "/v1/sync", token, strings.NewReader(`{"limit":1,"max_bytes":4096}`))
	if first.Code != 200 || first.Body.Len() > 4096 {
		t.Fatalf("response bound: %d bytes status %d", first.Body.Len(), first.Code)
	}
	var page protocol.SyncPage
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil || !page.HasMore || len(page.Entries) != 1 {
		t.Fatal(err, page)
	}
	body, _ := json.Marshal(protocol.SyncRequest{Cursor: page.Cursor})
	if got := request(t, handler, http.MethodPost, "/v1/sync", other, bytes.NewReader(body)); got.Code != 400 {
		t.Fatal("cross-scope cursor accepted")
	}
	// A byte limit cannot cause the next entry to be skipped or partially sent.
	if got := publish(t, handler, token, protocol.PublishRequest{Key: "0-big", OperationID: "big", Content: strings.Repeat("x", 3000)}); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	if got := request(t, handler, http.MethodPost, "/v1/sync", token, strings.NewReader(`{"max_bytes":4096}`)); got.Code != 413 || !strings.Contains(got.Body.String(), "sync_item_too_large") {
		t.Fatalf("oversized item: %d %s", got.Code, got.Body.String())
	}
}
