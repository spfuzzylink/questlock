package httpapi_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/semantic"
)

func semanticHash(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}

func TestSemanticRoutesStrictJSONAuthenticationAndLimits(t *testing.T) {
	_, handler, token := setup(t)
	q := semantic.Query{Partition: semanticHash("partition"), ModelID: semanticHash("model"), Vector: []float32{1, 0}, MinSimilarity: 0.9, TopK: 1, MaxScan: 10}
	encoded, _ := json.Marshal(q)
	valid := string(encoded)
	for _, path := range []string{"/v1/semantic/query", "/v1/semantic/entry"} {
		if got := request(t, handler, http.MethodPost, path, "", strings.NewReader(valid)); got.Code != 401 {
			t.Fatal(path, got.Code)
		}
		if got := request(t, handler, http.MethodGet, path, token, nil); got.Code != 405 || got.Header().Get("Allow") != "POST" {
			t.Fatal(path, got.Code)
		}
		if got := request(t, handler, http.MethodPost, path+"?scope=other", token, strings.NewReader(valid)); got.Code != 400 {
			t.Fatal(path, got.Code)
		}
		for _, body := range []string{"", "null", "[]", "{}", "{", valid + " {}", valid + "garbage", `{"scope":"other"}`, `{"entry":null,"max_entries":1}`, `{"entry":{"source":{"key":"first","key":"last"}},"max_entries":1}`} {
			if got := request(t, handler, http.MethodPost, path, token, strings.NewReader(body)); got.Code != 400 {
				t.Fatalf("%s accepted %q: %d %s", path, body, got.Code, got.Body.String())
			}
		}
		large := strings.Repeat(" ", int(httpapi.MaxSemanticRequestBytes)) + valid
		if got := request(t, handler, http.MethodPost, path, token, strings.NewReader(large)); got.Code != 413 {
			t.Fatal(path, "size limit", got.Code)
		}
	}
	for _, body := range []string{
		strings.TrimSuffix(valid, "}") + `,"model_id":"` + q.ModelID + `"}`,
		strings.Replace(valid, `"vector":[1,0]`, `"vector":[1,null]`, 1),
		strings.Replace(valid, `"top_k":1`, `"top_k":null`, 1),
		strings.Replace(valid, `"model_id"`, `"MODEL_ID"`, 1),
		strings.TrimSuffix(valid, "}") + `,"scope":"other"}`,
		strings.Replace(valid, q.ModelID, `\ud800`, 1),
		strings.Replace(valid, q.ModelID, string([]byte{0xff}), 1),
		strings.Replace(valid, `"top_k":1`, `"top_k":1000`, 1),
		strings.Replace(valid, `"max_scan":10`, `"max_scan":10001`, 1),
		strings.Replace(valid, `"vector":[1,0]`, `"vector":[0,0]`, 1),
	} {
		if got := request(t, handler, http.MethodPost, "/v1/semantic/query", token, strings.NewReader(body)); got.Code != 400 {
			t.Fatalf("accepted %q: %d %s", body, got.Code, got.Body.String())
		}
	}
	if got := request(t, handler, http.MethodPost, "/v1/semantic/query", token, bytes.NewReader(encoded)); got.Code != 200 {
		t.Fatalf("valid query: %d %s", got.Code, got.Body.String())
	}
	q.Vector = make([]float32, semantic.MaxDimensions)
	q.Vector[0] = 1
	encoded, _ = json.Marshal(q)
	if got := request(t, handler, http.MethodPost, "/v1/semantic/query", token, bytes.NewReader(encoded)); got.Code != 200 {
		t.Fatalf("4096-vector: %d %s", got.Code, got.Body.String())
	}
	q.Vector = append(q.Vector, 1)
	encoded, _ = json.Marshal(q)
	if got := request(t, handler, http.MethodPost, "/v1/semantic/query", token, bytes.NewReader(encoded)); got.Code != 400 {
		t.Fatal("oversized vector accepted", got.Code)
	}
}

func TestSemanticEndpointsScopeIsOnlyAuthenticatedPrincipal(t *testing.T) {
	s, handler, token := setup(t)
	p, err := s.Authenticate(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	otherToken, err := s.CreateToken(t.Context(), "semantic-other", "other-scope")
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC().Add(-time.Minute)
	fingerprint := semanticHash("job")
	content, _ := json.Marshal(map[string]any{"schema": 1, "fingerprint": fingerprint, "state": "complete", "created_at": created, "expires_at": created.Add(time.Hour), "response": map[string]any{"complete": true, "text": "synthetic answer"}})
	a, err := s.Publish(t.Context(), p, protocol.PublishRequest{Key: "inference/v1/jobs/" + fingerprint, OperationID: "semantic-source", Content: string(content)})
	if err != nil {
		t.Fatal(err)
	}
	e := semantic.Entry{Partition: semanticHash("partition"), ModelID: semanticHash("model"), Vector: []float32{1, 0}, Source: semantic.Source{Key: a.Artifact.Key, Version: a.Artifact.Version, SHA256: semanticHash(a.Artifact.Content), OutputSHA256: semanticHash("synthetic answer")}, CreatedAt: created, ExpiresAt: created.Add(30 * time.Minute)}
	e.ID = semantic.EntryID(e)
	body, _ := json.Marshal(semantic.PutRequest{Entry: e, MaxEntries: 10})
	if got := request(t, handler, http.MethodPost, "/v1/semantic/entry", token, bytes.NewReader(body)); got.Code != 200 || strings.TrimSpace(got.Body.String()) != "{}" {
		t.Fatal(got.Code, got.Body.String())
	}
	if got := request(t, handler, http.MethodPost, "/v1/semantic/entry", otherToken, bytes.NewReader(body)); got.Code != 404 {
		t.Fatalf("other scope source indexed: %d %s", got.Code, got.Body.String())
	}
	q := semantic.Query{Partition: e.Partition, ModelID: e.ModelID, Vector: e.Vector, MinSimilarity: 0.9, TopK: 1, MaxScan: 1}
	query, _ := json.Marshal(q)
	for _, tc := range []struct {
		token string
		count int
	}{{token, 1}, {otherToken, 0}} {
		got := request(t, handler, http.MethodPost, "/v1/semantic/query", tc.token, bytes.NewReader(query))
		var result semantic.SearchResult
		if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &result) != nil || len(result.Candidates) != tc.count || result.Scanned != tc.count {
			t.Fatal(got.Code, got.Body.String())
		}
		if strings.Contains(got.Body.String(), "synthetic answer") || strings.Contains(got.Body.String(), "vector") {
			t.Fatal("query leaked source text or embeddings")
		}
	}
	// The envelope and nested source both reject caller-provided scope fields.
	for _, injected := range []string{strings.TrimSuffix(string(body), "}") + `,"scope":"workspace-a"}`, strings.Replace(string(body), `"source":{`, `"source":{"scope":"workspace-a",`, 1), strings.Replace(string(body), `"source":{`, `"source":{"key":"duplicate",`, 1)} {
		if got := request(t, handler, http.MethodPost, "/v1/semantic/entry", otherToken, strings.NewReader(injected)); got.Code != 400 {
			t.Fatal(got.Code, got.Body.String())
		}
	}
}
