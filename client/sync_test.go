package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/protocol"
)

func TestSyncClientSnapshotChangesAndDifferentAuthority(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	token, err := s.CreateToken(ctx, "reader", "scope")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(s))
	defer server.Close()
	c, err := client.New(server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a", "b"} {
		if _, err := c.Publish(ctx, protocol.PublishRequest{Key: key, OperationID: key, Content: key}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := c.Sync(ctx, protocol.SyncRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != "snapshot" || !first.HasMore || len(first.Entries) != 1 {
		t.Fatal(first)
	}
	second, err := c.Sync(ctx, protocol.SyncRequest{Cursor: first.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	if second.HasMore || len(second.Entries) != 1 || second.Entries[0].Artifact.Key != "b" {
		t.Fatal(second)
	}
	_, err = c.Publish(ctx, protocol.PublishRequest{Key: "a", OperationID: "a2", ExpectedVersion: 1, Content: "new"})
	if err != nil {
		t.Fatal(err)
	}
	changes, err := c.Sync(ctx, protocol.SyncRequest{Cursor: second.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	if changes.Kind != "changes" || len(changes.Entries) != 1 || changes.Entries[0].Artifact.Content != "new" {
		t.Fatal(changes)
	}
	// Independent loopback fixtures model separate authorities, not proof of
	// actual remote-host/TLS deployment behavior.
	other, err := store.Open(filepath.Join(t.TempDir(), "other.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	otherToken, err := other.CreateToken(ctx, "reader", "scope")
	if err != nil {
		t.Fatal(err)
	}
	otherServer := httptest.NewServer(httpapi.New(other))
	defer otherServer.Close()
	otherClient, err := client.New(otherServer.URL, otherToken)
	if err != nil {
		t.Fatal(err)
	}
	_, err = otherClient.Sync(ctx, protocol.SyncRequest{Cursor: changes.Cursor})
	var apiError *client.Error
	if !errors.As(err, &apiError) || apiError.Status != 400 {
		t.Fatalf("different authority cursor: %v", err)
	}
}

func TestSyncClientRejectsMalformedOrOversizedResponsesWithoutRetry(t *testing.T) {
	valid := protocol.SyncPage{Authority: strings.Repeat("a", 64), Scope: "scope", Kind: "changes", Entries: []protocol.SyncEntry{}, Cursor: "opaque"}
	base, _ := json.Marshal(valid)
	duplicate := strings.Replace(string(base), `"scope":"scope"`, `"scope":"scope","scope":"different"`, 1)
	for name, body := range map[string]string{"duplicate": duplicate, "unknown": strings.TrimSuffix(string(base), "}") + `,"unknown":true}`, "trailing": string(base) + " {}", "null entries": strings.Replace(string(base), `"entries":[]`, `"entries":null`, 1), "oversize": strings.TrimSuffix(string(base), "}") + strings.Repeat(" ", 5000) + "}", "outer whitespace": strings.Repeat(" ", 5000) + string(base)} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			c := newClient(t, server.URL)
			if _, err := c.Sync(context.Background(), protocol.SyncRequest{MaxBytes: 4096}); err == nil {
				t.Fatal("invalid sync response accepted")
			}
			if calls.Load() != 1 {
				t.Fatal("sync automatically retried")
			}
		})
	}
}

func TestSyncDoesNotForwardCredentialsAcrossRedirects(t *testing.T) {
	var forwarded atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	c := newClient(t, source.URL)
	if _, err := c.Sync(context.Background(), protocol.SyncRequest{}); err == nil {
		t.Fatal("redirect accepted")
	}
	if forwarded.Load() != 0 {
		t.Fatal("credential-bearing sync followed redirect")
	}
}

func TestSyncClientValidatesArtifactScopeAndSequenceOrder(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*protocol.SyncPage)
	}{
		{name: "valid ordered page"},
		{name: "artifact outside page scope", mutate: func(p *protocol.SyncPage) { p.Entries[1].Artifact.Scope = "another-scope" }},
		{name: "decreasing sequence", mutate: func(p *protocol.SyncPage) { p.Entries[0].Sequence, p.Entries[1].Sequence = 2, 1 }},
		{name: "duplicate sequence", mutate: func(p *protocol.SyncPage) { p.Entries[1].Sequence = p.Entries[0].Sequence }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := protocol.SyncPage{
				Authority: strings.Repeat("a", 64), Scope: "scope", Kind: "changes", Through: 2, Cursor: "opaque",
				Entries: []protocol.SyncEntry{
					{Sequence: 1, Artifact: protocol.Artifact{Scope: "scope", Key: "a", Version: 1, Content: "first", UpdatedAt: "2026-10-16T00:00:00Z"}},
					{Sequence: 2, Artifact: protocol.Artifact{Scope: "scope", Key: "b", Version: 1, Content: "second", UpdatedAt: "2026-10-16T00:00:00Z"}},
				},
			}
			if tc.mutate != nil {
				tc.mutate(&page)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(page) }))
			defer server.Close()
			got, err := newClient(t, server.URL).Sync(context.Background(), protocol.SyncRequest{})
			if tc.mutate == nil {
				if err != nil || len(got.Entries) != 2 {
					t.Fatalf("valid ordered page rejected: %+v %v", got, err)
				}
			} else if err == nil || len(got.Entries) != 0 || got.Cursor != "" {
				t.Fatalf("malformed semantic page was exposed: %+v %v", got, err)
			}
		})
	}
}

func TestSyncClientRejectsMissingAndNullRequiredScalars(t *testing.T) {
	page := protocol.SyncPage{Authority: strings.Repeat("a", 64), Scope: "scope", Kind: "snapshot", Through: 1, Cursor: "opaque", HasMore: true, Entries: []protocol.SyncEntry{{Sequence: 1, Artifact: protocol.Artifact{Scope: "scope", Key: "a", Version: 1, Content: "", UpdatedAt: "2026-10-16T00:00:00Z"}}}}
	raw, _ := json.Marshal(page)
	for _, field := range []string{"content", "has_more", "through", "sequence"} {
		for _, mode := range []string{"null", "missing"} {
			t.Run(field+"/"+mode, func(t *testing.T) {
				var object map[string]any
				_ = json.Unmarshal(raw, &object)
				target := object
				if field == "sequence" || field == "content" {
					target = object["entries"].([]any)[0].(map[string]any)
				}
				if field == "content" {
					target = target["artifact"].(map[string]any)
				}
				if mode == "null" {
					target[field] = nil
				} else {
					delete(target, field)
				}
				var calls atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); json.NewEncoder(w).Encode(object) }))
				defer server.Close()
				got, err := newClient(t, server.URL).Sync(context.Background(), protocol.SyncRequest{})
				if err == nil || len(got.Entries) != 0 || got.Cursor != "" || calls.Load() != 1 {
					t.Fatalf("malformed page exposed or retried: %+v %v calls=%d", got, err, calls.Load())
				}
			})
		}
	}
	// Explicitly empty content remains a valid complete scalar.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { json.NewEncoder(w).Encode(page) }))
	defer server.Close()
	got, err := newClient(t, server.URL).Sync(context.Background(), protocol.SyncRequest{})
	if err != nil || len(got.Entries) != 1 || got.Entries[0].Artifact.Content != "" || !got.HasMore {
		t.Fatal("valid empty content was rejected", err)
	}
}
