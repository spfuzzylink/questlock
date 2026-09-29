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

	"github.com/spfuzzylink/agent-fence/client"
	"github.com/spfuzzylink/agent-fence/internal/httpapi"
	"github.com/spfuzzylink/agent-fence/internal/store"
	"github.com/spfuzzylink/agent-fence/protocol"
)

func newClient(t *testing.T, baseURL string) *client.Client {
	t.Helper()
	c, err := client.New(baseURL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestURLValidation(t *testing.T) {
	for _, baseURL := range []string{"", "/relative", "ftp://localhost", "http://", "http://user:pass@localhost", "http://localhost?token=x", "http://localhost?", "http://localhost#anchor", "http://localhost#", "http://[::1"} {
		if _, err := client.New(baseURL, "token"); err == nil {
			t.Errorf("accepted invalid base URL %q", baseURL)
		}
	}
	for _, token := range []string{"", "bearer token", "secret\n"} {
		if _, err := client.New("http://localhost:7777", token); err == nil {
			t.Errorf("accepted invalid bearer token")
		}
	}
}

func TestClientIntegration(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	token, err := s.CreateToken(ctx, "client-worker", "client-space")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(s))
	defer server.Close()
	c, err := client.New(server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	input := protocol.PublishRequest{Key: "output.txt", OperationID: "op-first", Content: "hello"}
	result, err := c.Publish(ctx, input)
	if err != nil || result.Artifact.Version != 1 {
		t.Fatalf("publish: %+v %v", result, err)
	}
	if artifact, err := c.Get(ctx, input.Key); err != nil || artifact.Content != input.Content {
		t.Fatalf("get: %+v %v", artifact, err)
	}
	if artifacts, err := c.List(ctx); err != nil || len(artifacts) != 1 {
		t.Fatalf("list: %+v %v", artifacts, err)
	}
	if events, err := c.Audit(ctx, 10); err != nil || len(events) != 1 {
		t.Fatalf("audit: %+v %v", events, err)
	}
	input.OperationID = "op-stale"
	input.Content = "stale"
	_, err = c.Publish(ctx, input)
	var apiError *client.Error
	if !errors.As(err, &apiError) || apiError.Code != "version_conflict" || apiError.Status != 409 || apiError.CurrentVersion != 1 {
		t.Fatalf("error lost conflict details: %v", err)
	}
}

func TestLostResponseReplayPreservesNewerArtifact(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	token, err := s.CreateToken(ctx, "retry-worker", "retry-space")
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(s)
	var dropResponse atomic.Bool
	dropResponse.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && dropResponse.CompareAndSwap(true, false) {
			// Commit through the real API, then close the transport before the
			// successful response can reach the client. The caller is uncertain.
			recorded := httptest.NewRecorder()
			api.ServeHTTP(recorded, r)
			if recorded.Code != http.StatusOK {
				t.Errorf("publish before disconnect failed: %d", recorded.Code)
			}
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("cannot simulate lost response: %v", err)
				return
			}
			_ = connection.Close()
			return
		}
		api.ServeHTTP(w, r)
	}))
	defer server.Close()
	c, err := client.New(server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	original := protocol.PublishRequest{Key: "answer", OperationID: "uncertain", Content: "first"}
	if _, err := c.Publish(ctx, original); err == nil {
		t.Fatal("caller unexpectedly received the dropped response")
	}
	if _, err := c.Publish(ctx, protocol.PublishRequest{Key: original.Key, OperationID: "newer", ExpectedVersion: 1, Content: "second"}); err != nil {
		t.Fatalf("newer publish failed: %v", err)
	}
	replayed, err := c.Publish(ctx, original)
	if err != nil || !replayed.Replayed || replayed.Artifact.Version != 1 || replayed.Artifact.Content != "first" {
		t.Fatalf("lost response did not replay original receipt: %+v %v", replayed, err)
	}
	current, err := c.Get(ctx, original.Key)
	if err != nil || current.Version != 2 || current.Content != "second" {
		t.Fatalf("retry overwrote newer artifact: %+v %v", current, err)
	}
}

func TestRedirectDoesNotForwardToken(t *testing.T) {
	var redirectedRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedRequests.Add(1)
		w.WriteHeader(200)
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	_, err := newClient(t, server.URL).Publish(context.Background(), protocol.PublishRequest{})
	var apiError *client.Error
	if !errors.As(err, &apiError) || apiError.Status != 307 || redirectedRequests.Load() != 0 {
		t.Fatalf("redirect followed or unexpected error: calls %d, err %v", redirectedRequests.Load(), err)
	}
}

func TestRequestEncodingAndNoRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("authorization header missing")
		}
		if r.URL.Path != "/prefix/v1/artifact" || r.URL.Query().Get("key") != "a&b?c=d +e" {
			t.Errorf("bad request URL: %s", r.URL)
		}
		w.WriteHeader(503)
		_ = json.NewEncoder(w).Encode(protocol.ErrorBody{Code: "unavailable", Message: "try later"})
	}))
	defer server.Close()
	c := newClient(t, server.URL+"/prefix/")
	_, err := c.Get(context.Background(), "a&b?c=d +e")
	var apiError *client.Error
	if !errors.As(err, &apiError) || apiError.Code != "unavailable" || calls.Load() != 1 {
		t.Fatalf("unexpected retry or error: calls %d, err %v", calls.Load(), err)
	}
	var mutationCalls atomic.Int32
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutationCalls.Add(1)
		w.WriteHeader(503)
	}))
	defer failing.Close()
	_, _ = newClient(t, failing.URL).Publish(context.Background(), protocol.PublishRequest{Key: "x", OperationID: "op"})
	if mutationCalls.Load() != 1 {
		t.Fatalf("mutation was retried %d times", mutationCalls.Load())
	}
}

func TestResponseLimitMalformedResponseAndCancellation(t *testing.T) {
	for _, body := range []string{`{`, `{} {}`, strings.Repeat("x", client.MaxResponseBytes+1)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		_, err := newClient(t, server.URL).Get(context.Background(), "x")
		server.Close()
		if err == nil {
			t.Fatalf("accepted malformed or oversized response (%d bytes)", len(body))
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newClient(t, "http://127.0.0.1:1").List(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestPublishRejectsMalformedUTF8WithoutSending(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
	}))
	defer server.Close()
	c := newClient(t, server.URL)
	for _, input := range []protocol.PublishRequest{
		{Key: "\xff", OperationID: "op", Content: "valid"},
		{Key: "x", OperationID: "\xff", Content: "valid"},
		{Key: "x", OperationID: "op", Content: "\xff"},
	} {
		if _, err := c.Publish(context.Background(), input); err == nil {
			t.Fatal("malformed UTF-8 accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("sent %d malformed requests", calls.Load())
	}
}
