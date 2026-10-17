package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/protocol"
)

func TestIdentityAndStrictPublicationPins(t *testing.T) {
	_, handler, token := setup(t)
	if got := request(t, handler, http.MethodGet, "/v1/identity", "", nil); got.Code != 401 {
		t.Fatal("identity was unauthenticated")
	}
	got := request(t, handler, http.MethodGet, "/v1/identity", token, nil)
	var pin protocol.Identity
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &pin) != nil || pin.Validate() != nil || pin.AgentID != "worker-a" || pin.Scope != "workspace-a" {
		t.Fatal("identity did not derive from the bearer token")
	}
	if got := request(t, handler, http.MethodGet, "/v1/identity?agent=other", token, nil); got.Code != 400 {
		t.Fatal("identity accepted a claimed agent")
	}
	body, _ := json.Marshal(protocol.PublishRequest{Key: "artifact", OperationID: "one", Content: "one"})
	cases := []struct {
		name    string
		headers map[string][]string
		status  int
	}{
		{"partial", map[string][]string{protocol.ExpectedAuthorityHeader: {pin.Authority}}, 400},
		{"duplicate", map[string][]string{protocol.ExpectedAuthorityHeader: {pin.Authority, pin.Authority}, protocol.ExpectedScopeHeader: {pin.Scope}, protocol.ExpectedAgentHeader: {pin.AgentID}}, 400},
		{"malformed", map[string][]string{protocol.ExpectedAuthorityHeader: {"invalid"}, protocol.ExpectedScopeHeader: {pin.Scope}, protocol.ExpectedAgentHeader: {pin.AgentID}}, 400},
		{"wrong scope", map[string][]string{protocol.ExpectedAuthorityHeader: {pin.Authority}, protocol.ExpectedScopeHeader: {"another"}, protocol.ExpectedAgentHeader: {pin.AgentID}}, 409},
		{"wrong agent", map[string][]string{protocol.ExpectedAuthorityHeader: {pin.Authority}, protocol.ExpectedScopeHeader: {pin.Scope}, protocol.ExpectedAgentHeader: {"another"}}, 409},
		{"wrong source", map[string][]string{protocol.ExpectedAuthorityHeader: {strings.Repeat("0", 64)}, protocol.ExpectedScopeHeader: {pin.Scope}, protocol.ExpectedAgentHeader: {pin.AgentID}}, 409},
	}
	cases = append(cases, struct {
		name    string
		headers map[string][]string
		status  int
	}{"all absent", nil, 400})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/artifact/pinned", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			for name, values := range tc.headers {
				for _, v := range values {
					req.Header.Add(name, v)
				}
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != tc.status {
				t.Fatalf("pin status %d want %d", rr.Code, tc.status)
			}
		})
	}
	valid := httptest.NewRequest(http.MethodPost, "/v1/artifact/pinned", bytes.NewReader(body))
	valid.Header.Set("Authorization", "Bearer "+token)
	valid.Header.Set(protocol.ExpectedAuthorityHeader, pin.Authority)
	valid.Header.Set(protocol.ExpectedScopeHeader, pin.Scope)
	valid.Header.Set(protocol.ExpectedAgentHeader, pin.AgentID)
	// A read or a claimed query identity cannot reach publication.
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if got := request(t, handler, method, "/v1/artifact/pinned", token, nil); got.Code != 405 {
			t.Fatal("pinned route accepted unsupported method")
		}
	}
	queried := valid.Clone(valid.Context())
	queried.URL.RawQuery = "scope=other"
	queried.Body = io.NopCloser(bytes.NewReader(body))
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, queried)
	if denied.Code != 400 {
		t.Fatal("pinned route accepted query fields")
	}
	if got := request(t, handler, http.MethodGet, "/v1/artifact?key=artifact", token, nil); got.Code != 404 {
		t.Fatal("invalid pin created an artifact")
	}
	accepted := httptest.NewRecorder()
	handler.ServeHTTP(accepted, valid)
	if accepted.Code != 200 {
		t.Fatal("dedicated pinned route did not accept complete matching pins")
	}
	if got := publish(t, handler, token, protocol.PublishRequest{Key: "artifact", OperationID: "one", Content: "one"}); got.Code != 200 {
		t.Fatal("old Publish without pins changed")
	}
}
