package client_test

import (
	"context"
	"fmt"
	"github.com/spfuzzylink/questlock/protocol"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPinnedClientStrictResponsesDoNotResolveAmbiguousJSON(t *testing.T) {
	for name, body := range map[string]string{
		"duplicate receipt":  `{"artifact":{"scope":"scope","key":"a","version":1,"content":"original","content":"changed","updated_at":"2026-10-16T00:00:00Z"},"replayed":false}`,
		"null receipt":       `{"artifact":null,"replayed":false}`,
		"unknown receipt":    `{"artifact":{},"replayed":false,"ignored":true}`,
		"duplicate conflict": `{"code":"internal","code":"version_conflict","message":"ambiguous"}`,
		"unknown conflict":   `{"code":"version_conflict","message":"ambiguous","ignored":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get(protocol.ExpectedAuthorityHeader) != strings.Repeat("a", 64) || r.Header.Get(protocol.ExpectedScopeHeader) != "scope" || r.Header.Get(protocol.ExpectedAgentHeader) != "agent" {
					t.Error("identity pin missing")
				}
				if strings.Contains(name, "conflict") {
					w.WriteHeader(409)
				}
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			c := newClient(t, server.URL)
			_, err := c.PublishPinned(context.Background(), protocol.Identity{Authority: strings.Repeat("a", 64), Scope: "scope", AgentID: "agent"}, protocol.PublishRequest{Key: "a", OperationID: "one", Content: "original"})
			if err == nil {
				t.Fatal("ambiguous pinned response accepted")
			}
			if strings.Contains(name, "conflict") && strings.Contains(err.Error(), "version_conflict") {
				t.Fatal("malformed negative envelope classified as a known conflict")
			}
			if calls.Load() != 1 {
				t.Fatal("client retried automatically")
			}
		})
	}
}
