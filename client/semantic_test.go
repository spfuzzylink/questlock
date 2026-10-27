package client_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/semantic"
)

func semanticDigest(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}
func semanticClientQuery() semantic.Query {
	return semantic.Query{Partition: semanticDigest("partition"), ModelID: semanticDigest("model"), Vector: []float32{1, 0}, MinSimilarity: 0.8, TopK: 1, MaxScan: 10}
}

func TestSemanticClientIntegrationAndExactSourceRevalidation(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	token, err := s.CreateToken(t.Context(), "semantic-client", "scope")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(s))
	defer server.Close()
	c, err := client.New(server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC().Add(-time.Minute)
	fingerprint := semanticDigest("job")
	content, _ := json.Marshal(map[string]any{"schema": 1, "fingerprint": fingerprint, "state": "complete", "created_at": created, "expires_at": created.Add(time.Hour), "response": map[string]any{"complete": true, "text": "synthetic answer"}})
	request := protocol.PublishRequest{Key: "inference/v1/jobs/" + fingerprint, OperationID: "semantic-source", Content: string(content)}
	a, err := c.Publish(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	q := semanticClientQuery()
	e := semantic.Entry{Partition: q.Partition, ModelID: q.ModelID, Vector: q.Vector, Source: semantic.Source{Key: a.Artifact.Key, Version: 1, SHA256: semanticDigest(a.Artifact.Content), OutputSHA256: semanticDigest("synthetic answer")}, CreatedAt: created, ExpiresAt: created.Add(30 * time.Minute)}
	e.ID = semantic.EntryID(e)
	if err = c.PutSemantic(t.Context(), semantic.PutRequest{Entry: e, MaxEntries: 10}); err != nil {
		t.Fatal(err)
	}
	result, err := c.QuerySemantic(t.Context(), q)
	if err != nil || result.Scanned != 1 || len(result.Candidates) != 1 || result.Candidates[0].Source != e.Source || result.Candidates[0].Similarity != 1 {
		t.Fatal(result, err)
	}
	request.ExpectedVersion = 1
	request.OperationID = "source-changed"
	request.Content = strings.Replace(string(content), `"state":"complete"`, `"state":"uncertain"`, 1)
	if _, err = c.Publish(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	result, err = c.QuerySemantic(t.Context(), q)
	if err != nil || result.Scanned != 1 || len(result.Candidates) != 0 {
		t.Fatal("stale source accepted", result, err)
	}
}

func TestSemanticClientValidatesBeforeRequestAndNeverRetries(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/v1/semantic/query" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("wrong method, route or auth")
		}
		w.WriteHeader(503)
		w.Write([]byte(`{"code":"archive_unavailable","message":"archive unavailable"}`))
	}))
	defer server.Close()
	c := newClient(t, server.URL)
	q := semanticClientQuery()
	q.Vector = []float32{0, 0}
	if _, err := c.QuerySemantic(t.Context(), q); err == nil {
		t.Fatal("invalid query accepted")
	}
	if err := c.PutSemantic(t.Context(), semantic.PutRequest{}); err == nil {
		t.Fatal("invalid put accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid inputs sent")
	}
	q = semanticClientQuery()
	_, err := c.QuerySemantic(t.Context(), q)
	var apiError *client.Error
	if !errors.As(err, &apiError) || apiError.Code != "archive_unavailable" || calls.Load() != 1 {
		t.Fatal("archive error lost or retried", err, calls.Load())
	}
}

func TestSemanticClientRejectsMalformedAndUnboundedResponses(t *testing.T) {
	for _, body := range []string{
		`{}`, `null`, `{"candidates":[],"scanned":0}`, `{"candidates":null,"scanned":0,"truncated":false}`,
		`{"candidates":[],"scanned":0,"scanned":0,"truncated":false}`,
		`{"candidates":[],"scanned":0,"truncated":null}`,
		`{"candidates":[],"scanned":11,"truncated":false}`,
		`{"candidates":[],"scanned":1,"truncated":true}`,
		`{"candidates":[],"scanned":-1,"truncated":false}`,
		`{"candidates":[],"scanned":0,"truncated":false,"scope":"other"}`,
		`{"candidates":[{}],"scanned":1,"truncated":false}`,
		strings.Repeat(" ", 128<<10) + `{}`,
	} {
		t.Run(body[:min(30, len(body))], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer server.Close()
			if _, err := newClient(t, server.URL).QuerySemantic(t.Context(), semanticClientQuery()); err == nil {
				t.Fatal("accepted invalid semantic response")
			}
		})
	}
}
