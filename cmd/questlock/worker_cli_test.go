package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/observability"
)

func workerCLIRequest() delegation.Request {
	return delegation.Request{
		Spec:   inference.Spec{Task: "release-review", Revision: "approved-snapshot", Instructions: "Return only JSON; ship false when a required gate is missing.", Input: "private-approved-input: unit checks passed; container check missing."},
		Policy: delegation.Policy{Local: delegation.Tier{ID: "compact", Spec: inference.Spec{Provider: "ollama", Model: "qwen3.5:4b", MaxContextBytes: 8192, MaxInputTokens: 8192, MaxOutputTokens: 128, ReuseForSeconds: 3600}}},
		Checks: []evaluation.Check{{ID: "ship", Kind: "json_equals", Pointer: "/ship", Expected: json.RawMessage("false")}},
	}
}

func saveWorkerCLIRequest(t *testing.T, request delegation.Request) string {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "approved-request.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWorkerCLIQualityFailureStripsAnswerAndReusesDurableRecord(t *testing.T) {
	broker, err := newModelQuestFixture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer broker.close()
	t.Setenv("QUESTLOCK_TOKEN", broker.tokens[0])
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	const badAnswer = `{"ship":true,"private":"private-unapproved-worker-answer"}`
	var generations atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": "qwen3.5:4b", "model": "qwen3.5:4b", "digest": strings.Repeat("a", 64)}}})
		case "/api/show":
			json.NewEncoder(w).Encode(map[string]any{"template": "{{ .System }} {{ .Prompt }}", "capabilities": []string{"completion", "thinking"}, "thinking": map[string]any{"values": []bool{true, false}}, "model_info": map[string]any{"general.architecture": "qwen3_5", "qwen3_5.context_length": 262144}})
		case "/api/generate":
			generations.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"model": "qwen3.5:4b", "response": badAnswer, "done": true, "done_reason": "stop", "prompt_eval_count": 100, "eval_count": 25})
		default:
			t.Errorf("unexpected local fixture route: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	request := workerCLIRequest()
	path := saveWorkerCLIRequest(t, request)
	digest := sha256.Sum256([]byte(badAnswer))
	for invocation := 0; invocation < 2; invocation++ {
		var output, diagnostics bytes.Buffer
		err := workerCommandIO([]string{"run", "--request", path, "--broker", broker.server.URL, "--ollama-url", provider.URL, "--timeout", "5s"}, io.NopCloser(strings.NewReader("")), &output, &diagnostics)
		if err == nil || !strings.Contains(err.Error(), "did not produce an approved answer") {
			t.Fatalf("wrong complete answer received a successful worker exit: %v", err)
		}
		var result delegation.Result
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Status != "quality_failed" || result.Response != nil || result.SelectedTier != "" || len(result.Attempts) != 1 {
			t.Fatalf("failed answer became selected or was retried: %+v", result)
		}
		attempt := result.Attempts[0]
		if attempt.QualityPassed || attempt.Result.Response.Text != "" || !attempt.Result.Response.Complete || !attempt.Result.Durable || attempt.OutputSHA256 != hex.EncodeToString(digest[:]) || attempt.Result.Reused != (invocation == 1) || attempt.Result.GenerationUsage.OutputTokens != 25 {
			t.Fatalf("quality failure lost inspectable metadata or exposed text: %+v", attempt)
		}
		if invocation == 1 && (attempt.Result.ThisRunUsage != (inference.Usage{}) || attempt.Result.AvoidedUsage != attempt.Result.GenerationUsage) {
			t.Fatalf("repeated policy charged historical usage: %+v", attempt.Result)
		}
		if strings.Contains(output.String(), "private-unapproved-worker-answer") || strings.Contains(output.String(), request.Spec.Input) || diagnostics.Len() != 0 {
			t.Fatal("failed worker output exposed approved input or unapproved response")
		}
	}
	if generations.Load() != 1 {
		t.Fatalf("repeated known quality failure generated %d times", generations.Load())
	}
	artifacts, err := broker.clients[0].List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	retained := false
	for _, artifact := range artifacts {
		artifact, err = broker.clients[0].Get(context.Background(), artifact.Key)
		if err != nil {
			t.Fatal(err)
		}
		var record struct {
			Response *inference.Response `json:"response"`
		}
		if json.Unmarshal([]byte(artifact.Content), &record) == nil && record.Response != nil && record.Response.Text == badAnswer {
			retained = true
		}
	}
	if !retained {
		t.Fatal("CLI stripping removed the saved answer needed for inspection/reuse")
	}
}

func TestWorkerCLIPaidAuthorityAndBudgetsValidateBeforeAnyProvider(t *testing.T) {
	var calls atomic.Int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "must not be reached", http.StatusInternalServerError)
	}))
	defer endpoint.Close()
	t.Setenv("QUESTLOCK_TOKEN", "fixture-token")
	t.Setenv("OPENAI_API_KEY", "fixture-provider-key")
	for _, tc := range []struct {
		name      string
		allowPaid bool
		budgets   bool
	}{
		{name: "missing explicit opt in", budgets: true},
		{name: "missing explicit budgets", allowPaid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := workerCLIRequest()
			hosted := request.Policy.Local
			hosted.ID, hosted.Spec.Provider, hosted.Spec.Model = "hosted", "openai", "gpt-4.1-mini"
			if tc.budgets {
				hosted.Spec.Prices = &inference.Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
				hosted.Spec.MaxCostUSD, hosted.Spec.DailyBudgetUSD = 0.001, 0.01
			}
			request.Policy.Escalation = &hosted
			args := []string{"run", "--request", saveWorkerCLIRequest(t, request), "--broker", endpoint.URL, "--ollama-url", endpoint.URL}
			if tc.allowPaid {
				args = append(args, "--allow-paid")
			}
			var output, diagnostics bytes.Buffer
			err := workerCommandIO(args, io.NopCloser(strings.NewReader("")), &output, &diagnostics)
			if err == nil || !strings.Contains(err.Error(), "did not produce an approved answer") || calls.Load() != 0 || output.Len() != 0 || diagnostics.Len() != 0 {
				t.Fatalf("invalid hosted policy reached provider/broker or emitted a result: %v, calls=%d", err, calls.Load())
			}
		})
	}
}

type blockingWorkerDiagnostics struct {
	calls   atomic.Int64
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingWorkerDiagnostics) Write(p []byte) (int, error) {
	w.calls.Add(1)
	select {
	case w.entered <- struct{}{}:
	default:
	}
	<-w.release
	return len(p), nil
}

func (w *blockingWorkerDiagnostics) unblock() { w.once.Do(func() { close(w.release) }) }

func workerQueueExecution() observability.Execution {
	now := time.Now().UTC()
	return observability.Execution{StartedAt: now, EndedAt: now.Add(time.Millisecond), Provider: "ollama", Model: "qwen3.5:4b", Outcome: observability.Complete, Durable: true}
}

func TestWorkerTelemetryQueueNeverWritesBlockedDiagnostics(t *testing.T) {
	var exports atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exports.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "{}")
	}))
	defer server.Close()
	exporter, err := observability.NewLangfuse(observability.Config{Endpoint: server.URL + "/api/public/otel/v1/traces", PublicKey: "fixture-public", SecretKey: "fixture-secret", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := &blockingWorkerDiagnostics{entered: make(chan struct{}, 1), release: make(chan struct{})}
	defer diagnostics.unblock()
	queue := newWorkerTelemetryQueue(exporter, diagnostics)
	queue.submit([]telemetryRecord{{execution: workerQueueExecution()}})
	closed := make(chan struct{})
	go func() { queue.close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		diagnostics.unblock()
		queue.cancel()
		<-closed
		t.Fatal("background telemetry shutdown waited on an unread diagnostic sink")
	}
	if diagnostics.calls.Load() != 0 || exports.Load() != 1 {
		t.Fatalf("background queue wrote diagnostics or failed to drain export: writes=%d exports=%d", diagnostics.calls.Load(), exports.Load())
	}
}

func TestWorkerTelemetryQueueCloseCancelsBoundedOutstandingExports(t *testing.T) {
	entered, canceled := make(chan struct{}, 32), make(chan struct{}, 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		entered <- struct{}{}
		<-r.Context().Done()
		canceled <- struct{}{}
	}))
	defer server.Close()
	exporter, err := observability.NewLangfuse(observability.Config{Endpoint: server.URL + "/api/public/otel/v1/traces", PublicKey: "fixture-public", SecretKey: "fixture-secret", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := &blockingWorkerDiagnostics{entered: make(chan struct{}, 1), release: make(chan struct{})}
	defer diagnostics.unblock()
	queue := newWorkerTelemetryQueue(exporter, diagnostics)
	queue.submit([]telemetryRecord{{execution: workerQueueExecution()}})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		queue.cancel()
		queue.close()
		t.Fatal("fixture exporter never started")
	}
	// Each outstanding record would take five seconds if shutdown drained every
	// timeout. Close must cancel the queue after one bounded shutdown interval.
	for range 16 {
		queue.submit([]telemetryRecord{{execution: workerQueueExecution()}})
	}
	closed := make(chan struct{})
	started := time.Now()
	go func() { queue.close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(7 * time.Second):
		queue.cancel()
		diagnostics.unblock()
		<-closed
		t.Fatal("shutdown drained outstanding export timeouts instead of canceling them")
	}
	if time.Since(started) > 6500*time.Millisecond || diagnostics.calls.Load() != 0 {
		t.Fatalf("shutdown exceeded its bound or wrote diagnostics: duration=%s writes=%d", time.Since(started), diagnostics.calls.Load())
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("outstanding export did not honor cancellation")
	}
}
