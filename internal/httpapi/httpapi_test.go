package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/protocol"
)

func setup(t *testing.T) (*store.Store, http.Handler, string) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	token, err := s.CreateToken(context.Background(), "worker-a", "workspace-a")
	if err != nil {
		t.Fatal(err)
	}
	return s, httpapi.New(s), token
}

func request(t *testing.T, handler http.Handler, method, path, token string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing security response headers")
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unexpected permissive CORS header")
	}
	return response
}

func publish(t *testing.T, handler http.Handler, token string, body protocol.PublishRequest) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return request(t, handler, http.MethodPost, "/v1/artifact", token, bytes.NewReader(encoded))
}

func TestAuthenticationAndMethods(t *testing.T) {
	s, handler, token := setup(t)
	for _, test := range []struct {
		method, path, token string
		status              int
	}{
		{http.MethodGet, "/healthz", "", 200},
		{http.MethodPost, "/healthz", "", 405},
		{http.MethodGet, "/missing", "", 404},
		{http.MethodGet, "/v1/artifacts", "", 401},
		{http.MethodGet, "/v1/artifacts", "wrong-token", 401},
		{http.MethodDelete, "/v1/artifact", token, 405},
		{http.MethodPost, "/v1/audit", token, 405},
		{http.MethodGet, "/v1/artifacts", token, 200},
	} {
		t.Run(test.method+test.path+test.token[:min(3, len(test.token))], func(t *testing.T) {
			response := request(t, handler, test.method, test.path, test.token, nil)
			if response.Code != test.status {
				t.Fatalf("got %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
			if response.Code == 401 && response.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatal("missing authentication challenge")
			}
			if response.Code == 405 && response.Header().Get("Allow") == "" {
				t.Fatal("missing allowed methods")
			}
		})
	}
	if err := s.RevokeAgent(context.Background(), "worker-a"); err != nil {
		t.Fatal(err)
	}
	if got := request(t, handler, http.MethodGet, "/v1/artifacts", token, nil); got.Code != 401 {
		t.Fatalf("revoked token returned %d", got.Code)
	}
}

func TestStrictJSONAndBodyLimit(t *testing.T) {
	_, handler, token := setup(t)
	for _, body := range []string{
		``, `null`, `[]`, `{`,
		`{"key":"x","operation_id":"op","content":"x"}`,
		`{"key":"x","operation_id":"op","expected_version":null,"content":"x"}`,
		`{"key":"x","operation_id":"op","expected_version":"0","content":"x"}`,
		`{"key":"x","operation_id":"op","expected_version":0}`,
		`{"key":"x","operation_id":"op","expected_version":0,"content":null}`,
		"{\"key\":\"x\",\"operation_id\":\"op\",\"expected_version\":0,\"content\":\"\xff\"}",
		`{"key":"x","operation_id":"op","expected_version":0,"content":"x","unknown":true}`,
		`{"key":"x","operation_id":"op","expected_version":0,"content":"x"} {}`,
		`{"key":"x","operation_id":"op","expected_version":0,"content":"x"} garbage`,
	} {
		response := request(t, handler, http.MethodPost, "/v1/artifact", token, strings.NewReader(body))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %q returned %d: %s", body, response.Code, response.Body.String())
		}
	}
	oversized := `{"key":"x","operation_id":"op","expected_version":0,"content":"` + strings.Repeat("x", int(httpapi.MaxRequestBytes)) + `"}`
	if got := request(t, handler, http.MethodPost, "/v1/artifact", token, strings.NewReader(oversized)); got.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large request returned %d: %s", got.Code, got.Body.String())
	}
	// A valid 1MiB value can require six bytes per character in its JSON form.
	got := publish(t, handler, token, protocol.PublishRequest{Key: "escaped", OperationID: "escaped-op", Content: strings.Repeat("\x00", protocol.MaxContentBytes)})
	if got.Code != http.StatusOK {
		t.Fatalf("valid escaped content returned %d: %s", got.Code, got.Body.String())
	}
}

func TestContentMustBeExplicitButMayBeEmpty(t *testing.T) {
	_, handler, token := setup(t)
	if got := publish(t, handler, token, protocol.PublishRequest{Key: "answer.txt", OperationID: "create", Content: "keep me"}); got.Code != 200 {
		t.Fatalf("initial write failed: %d %s", got.Code, got.Body.String())
	}
	for _, body := range []string{
		`{"key":"answer.txt","operation_id":"replacement","expected_version":1}`,
		`{"key":"answer.txt","operation_id":"replacement","expected_version":1,"content":null}`,
	} {
		if got := request(t, handler, http.MethodPost, "/v1/artifact", token, strings.NewReader(body)); got.Code != 400 {
			t.Fatalf("missing/null content accepted: %d %s", got.Code, got.Body.String())
		}
	}
	got := request(t, handler, http.MethodGet, "/v1/artifact?key=answer.txt", token, nil)
	var existing protocol.Artifact
	if err := json.Unmarshal(got.Body.Bytes(), &existing); err != nil || existing.Version != 1 || existing.Content != "keep me" {
		t.Fatalf("invalid request modified artifact: %+v %v", existing, err)
	}
	got = publish(t, handler, token, protocol.PublishRequest{Key: "answer.txt", OperationID: "empty", ExpectedVersion: 1, Content: ""})
	var result protocol.PublishResult
	if err := json.Unmarshal(got.Body.Bytes(), &result); err != nil || got.Code != 200 || result.Artifact.Version != 2 || result.Artifact.Content != "" {
		t.Fatalf("explicit empty content failed: %d %+v %v", got.Code, result, err)
	}
}

func TestCASReplayScopeAndAuditRoutes(t *testing.T) {
	s, handler, token := setup(t)
	first := protocol.PublishRequest{Key: "answer.txt", OperationID: "first", Content: "first result"}
	response := publish(t, handler, token, first)
	if response.Code != 200 {
		t.Fatalf("first publish returned %d: %s", response.Code, response.Body.String())
	}
	var result protocol.PublishResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Artifact.Version != 1 || result.Replayed {
		t.Fatalf("bad initial result: %+v, %v", result, err)
	}
	response = publish(t, handler, token, first)
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != 200 || !result.Replayed {
		t.Fatalf("bad replay: status %d, result %+v, %v", response.Code, result, err)
	}
	response = publish(t, handler, token, protocol.PublishRequest{Key: "answer.txt", OperationID: "stale", Content: "stale result"})
	var failure protocol.ErrorBody
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil || response.Code != 409 || failure.Code != "version_conflict" || failure.CurrentVersion != 1 {
		t.Fatalf("bad conflict: status %d, result %+v, %v", response.Code, failure, err)
	}
	response = publish(t, handler, token, protocol.PublishRequest{Key: "answer.txt", OperationID: "first", ExpectedVersion: 1, Content: "changed request"})
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil || response.Code != 409 || failure.Code != "operation_conflict" {
		t.Fatalf("bad operation conflict: status %d, result %+v, %v", response.Code, failure, err)
	}
	response = request(t, handler, http.MethodGet, "/v1/artifact?key=answer.txt", token, nil)
	var artifact protocol.Artifact
	if err := json.Unmarshal(response.Body.Bytes(), &artifact); err != nil || response.Code != 200 || artifact.Content != first.Content {
		t.Fatalf("artifact overwritten or unavailable: %d %+v %v", response.Code, artifact, err)
	}
	response = request(t, handler, http.MethodGet, "/v1/artifacts", token, nil)
	var artifacts []protocol.Artifact
	if err := json.Unmarshal(response.Body.Bytes(), &artifacts); err != nil || response.Code != 200 || len(artifacts) != 1 {
		t.Fatalf("bad artifact listing: %d %+v %v", response.Code, artifacts, err)
	}
	response = request(t, handler, http.MethodGet, "/v1/audit?limit=100", token, nil)
	var events []protocol.AuditEvent
	if err := json.Unmarshal(response.Body.Bytes(), &events); err != nil || response.Code != 200 || len(events) < 3 {
		t.Fatalf("missing audit events: %d %+v %v", response.Code, events, err)
	}
	otherToken, err := s.CreateToken(context.Background(), "worker-other", "workspace-b")
	if err != nil {
		t.Fatal(err)
	}
	response = request(t, handler, http.MethodGet, "/v1/artifact?key=answer.txt", otherToken, nil)
	if response.Code != 404 {
		t.Fatalf("cross-scope read returned %d", response.Code)
	}
	for _, limit := range []string{"0", "-1", "1001", "not-a-number", "", "1&limit=2"} {
		if got := request(t, handler, http.MethodGet, "/v1/audit?limit="+limit, token, nil); got.Code != 400 {
			t.Fatalf("limit %q returned %d", limit, got.Code)
		}
	}
}

func TestInternalErrorsAreRedacted(t *testing.T) {
	s, handler, token := setup(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	response := request(t, handler, http.MethodGet, "/v1/artifacts", token, nil)
	if response.Code != 500 || strings.Contains(response.Body.String(), "database") || strings.Contains(response.Body.String(), "state.db") {
		t.Fatalf("unexpected internal error response: %d %s", response.Code, response.Body.String())
	}
}
