package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/outcomes"
	"github.com/spfuzzylink/questlock/protocol"
)

func TestOutcomeQuestSixRealBrokerScenarios(t *testing.T) {
	var output bytes.Buffer
	if err := outcomeQuestIO(&output); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "PASS  ") != 6 || !strings.Contains(output.String(), "QUEST CLEARED") {
		t.Fatal(output.String())
	}
}

func TestOutcomeCLIRecordsExplicitFeedbackAndReportsOnlyKnownCosts(t *testing.T) {
	fixture, err := newModelQuestFixture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.close()
	t.Setenv("QUESTLOCK_TOKEN", fixture.tokens[0])
	cost, minutes := 0.25, 2.0
	write := func(name string, value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	invoke := func(args ...string) []byte {
		var output, diagnostics bytes.Buffer
		args = append(args, "--task", "cli-task", "--revision", "r1", "--broker", fixture.server.URL)
		if err := outcomeCommandIO(args, &output, &diagnostics); err != nil {
			t.Fatal(err)
		}
		if diagnostics.Len() != 0 {
			t.Fatal("unexpected diagnostics")
		}
		return output.Bytes()
	}
	invoke("record", "--run", write("run.json", fixtureOutcomeRun("run-1", &cost, false)))
	invoke("feedback", "--expected-version", "1", "--event", write("accept.json", fixtureAcceptance("accept-1", "run-1", &minutes)))
	for _, rate := range []bool{false, true} {
		args := []string{"report"}
		if rate {
			args = append(args, "--hourly-rate-usd", "60")
		}
		var report outcomes.Report
		if err := json.Unmarshal(invoke(args...), &report); err != nil {
			t.Fatal(err)
		}
		if report.AcceptedTasks != 1 || (report.CostPerAcceptedTaskUSD != nil) != rate {
			t.Fatalf("wrong nullable metric: %+v", report)
		}
		if rate && *report.CostPerAcceptedTaskUSD != 2.25 {
			t.Fatal("explicit human effort was omitted")
		}
	}
}

func TestOutcomeCLIRejectsAmbiguousMetadataWithoutLeakingIt(t *testing.T) {
	for _, data := range []string{
		`{"id":"private-value","id":"run-2"}`,
		`{"id":"run-1","attempts":[],"private-field":"private-value"}`,
		`{} {"private":"private-value"}`,
		strings.Repeat("[", 34) + `"private-value"` + strings.Repeat("]", 34),
	} {
		path := filepath.Join(t.TempDir(), "input.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		var output, diagnostics bytes.Buffer
		err := outcomeCommandIO([]string{"record", "--task", "task", "--revision", "r1", "--run", path}, &output, &diagnostics)
		if err == nil || strings.Contains(err.Error(), "private-value") || output.Len() != 0 {
			t.Fatalf("ambiguous metadata accepted or exposed: %v", err)
		}
	}
}

func TestWorkerOutcomeTrackingReusesIdentityAndRecordsStoppedPassingChecks(t *testing.T) {
	for _, exceedCap := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh-and-reuse", true: "known-cap-error"}[exceedCap], func(t *testing.T) {
			fixture, err := newModelQuestFixture(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer fixture.close()
			t.Setenv("QUESTLOCK_TOKEN", fixture.tokens[0])
			var calls atomic.Int64
			provider := outcomeTestProvider(t, &calls, exceedCap)
			defer provider.Close()
			request := workerCLIRequest()
			// Generate untracked first. Enabling accounting must reuse the exact
			// answer, not turn an accounting setting into a fresh inference.
			for _, tracked := range []bool{false, true} {
				if exceedCap && !tracked {
					continue
				}
				request.TrackOutcome = tracked
				var output, diagnostics bytes.Buffer
				err := workerCommandIO([]string{"run", "--request", saveWorkerCLIRequest(t, request), "--broker", fixture.server.URL, "--ollama-url", provider.URL}, io.NopCloser(strings.NewReader("")), &output, &diagnostics)
				if (err != nil) != exceedCap {
					t.Fatalf("unexpected worker error: %v", err)
				}
				var result delegation.Result
				if err := json.Unmarshal(output.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if !tracked {
					if result.OutcomeTracking != nil {
						t.Fatal("unrequested accounting was enabled")
					}
					continue
				}
				if result.OutcomeTracking == nil || result.OutcomeTracking.Status != "recorded" || result.OutcomeTracking.Receipt == nil {
					t.Fatalf("tracking not recorded: %+v", result.OutcomeTracking)
				}
				snapshot, err := (outcomes.Store{Broker: fixture.clients[0]}).Get(context.Background(), outcomes.Task{ID: request.Spec.Task, Revision: request.Spec.Revision})
				if err != nil {
					t.Fatal(err)
				}
				attempt := snapshot.Runs[0].Attempts[0]
				if snapshot.State.Status != "unreviewed" || !attempt.CheckPassed || attempt.EstimatedCostUSD == nil || *attempt.EstimatedCostUSD != 0 {
					t.Fatal("checks were conflated with acceptance, or local cost was wrong")
				}
				if exceedCap {
					if result.Status != "stopped" || result.Response != nil || attempt.Complete || !attempt.GenerationCalled || !attempt.UsageKnown || attempt.Usage.InputTokens != 8193 {
						t.Fatalf("cap failure lost observed checks/current usage or exposed answer: %+v", attempt)
					}
				} else if !attempt.Reused || attempt.GenerationCalled || attempt.Usage != (inference.Usage{}) || attempt.CostBasis != "reuse_zero" {
					t.Fatalf("accounting opt-in generated again or charged original usage: %+v", attempt)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("generated %d times", calls.Load())
			}
		})
	}
}

func outcomeTestProvider(t *testing.T, calls *atomic.Int64, exceedCap bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": "qwen3.5:4b", "model": "qwen3.5:4b", "digest": strings.Repeat("a", 64)}}})
		case "/api/show":
			json.NewEncoder(w).Encode(map[string]any{"template": "{{ .System }} {{ .Prompt }}", "capabilities": []string{"completion"}, "model_info": map[string]any{"general.architecture": "qwen3_5", "qwen3_5.context_length": 262144}})
		case "/api/generate":
			calls.Add(1)
			count := 100
			if exceedCap {
				count = 8193
			}
			json.NewEncoder(w).Encode(map[string]any{"model": "qwen3.5:4b", "response": `{"ship":false}`, "done": true, "done_reason": "stop", "prompt_eval_count": count, "eval_count": 25})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestWorkerOutcomeRegistrationFailureReturnsRecoveryRecordWithoutAnotherGeneration(t *testing.T) {
	fixture, err := newModelQuestFixture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.close()
	t.Setenv("QUESTLOCK_TOKEN", fixture.tokens[0])
	var calls atomic.Int64
	provider := outcomeTestProvider(t, &calls, false)
	defer provider.Close()
	handler := httpapi.New(fixture.store)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/artifact" {
			data, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(data))
			var publish protocol.PublishRequest
			json.Unmarshal(data, &publish)
			if strings.HasPrefix(publish.Key, "outcomes/") {
				http.Error(w, "private registration error", http.StatusServiceUnavailable)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer broken.Close()
	request := workerCLIRequest()
	request.TrackOutcome = true
	var output, diagnostics bytes.Buffer
	err = workerCommandIO([]string{"run", "--request", saveWorkerCLIRequest(t, request), "--broker", broken.URL, "--ollama-url", provider.URL}, io.NopCloser(strings.NewReader("")), &output, &diagnostics)
	if err == nil || !strings.Contains(err.Error(), "registration is unconfirmed") || strings.Contains(output.String(), "private registration error") {
		t.Fatalf("registration failed unsafely: %v", err)
	}
	var result delegation.Result
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	tracking := result.OutcomeTracking
	if tracking == nil || tracking.Status != "failed" || tracking.Record == nil || tracking.Record.ID != tracking.RunID || result.Status != "completed" || result.Response == nil {
		t.Fatal("metadata failure lost checked answer or recovery identity")
	}
	store := outcomes.Store{Broker: fixture.clients[0]}
	first, err := store.Record(context.Background(), tracking.Task, *tracking.Record)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Record(context.Background(), tracking.Task, *tracking.Record)
	if err != nil || first != second || calls.Load() != 1 {
		t.Fatal("exact metadata recovery changed receipt or regenerated")
	}
}

func TestWorkerOutcomeConversionUnknownHostedCostStaysUnknown(t *testing.T) {
	request := workerCLIRequest()
	request.Policy.Escalation = &delegation.Tier{ID: "paid", Spec: inference.Spec{Provider: "openai", Prices: &inference.Prices{Input: 1, CacheRead: 0.5, CacheWrite: 2, Output: 3}}}
	result := delegation.Result{Status: "stopped", RequestSHA256: strings.Repeat("a", 64), Attempts: []delegation.Attempt{{TierID: "paid", Result: inference.Result{Measurement: &inference.Measurement{GenerationCalled: true}}}}}
	record := workerOutcomeRecord(request, result, "run-1")
	if record.Attempts[0].EstimatedCostUSD != nil || record.Attempts[0].UsageKnown || record.Attempts[0].CostBasis != "unknown" {
		t.Fatal("ambiguous hosted generation was called free")
	}
	usage := inference.Usage{InputTokens: 10, UncachedInputTokens: 4, CacheReadTokens: 3, CacheWriteTokens: 3, OutputTokens: 5, ReasoningTokens: 2}
	result.Attempts[0].Result.Measurement.KnownGenerationUsage = true
	result.Attempts[0].Result.Measurement.GenerationUsage = &usage
	record = workerOutcomeRecord(request, result, "run-1")
	if record.Attempts[0].EstimatedCostUSD == nil || *record.Attempts[0].EstimatedCostUSD != 0.000027 {
		t.Fatal("exclusive cache categories, rounding, or reasoning accounting is wrong")
	}
}
