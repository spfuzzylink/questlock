package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/observability"
	"github.com/spfuzzylink/questlock/protocol"
)

func TestCollectedRunnerRecordsSetupOnceAndScoresOnlyKnownAnswers(t *testing.T) {
	broker, err := newModelQuestFixture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer broker.close()
	provider := &modelQuestProvider{}
	setupEnd := time.Now().UTC()
	var records []telemetryRecord
	runner := collectedRunner{
		inner:  &preparedRunner{runner: broker.runner(0, provider), setupStart: setupEnd.Add(-2 * time.Millisecond), setupEnd: setupEnd},
		checks: []evaluation.Check{{ID: "fixture-quality", Kind: "contains", Text: "complete offline fixture"}}, records: &records,
	}
	spec := modelQuestSpec("collected-records")
	first, err := runner.Run(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.Run(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if first.Measurement.ProviderSetupStartedAt.IsZero() || first.Measurement.ProviderSetupMS != 2 || !second.Measurement.ProviderSetupStartedAt.IsZero() || second.Measurement.ProviderSetupMS != 0 || len(records) != 2 || records[0].quality == nil || !*records[0].quality || records[1].quality == nil || !*records[1].quality || records[0].execution.Generation == nil || records[1].execution.Generation != nil || !records[1].execution.Reused {
		t.Fatalf("prepared runner repeated setup or failed to score the known cached answer: first=%+v second=%+v records=%+v", first.Measurement, second.Measurement, records)
	}
	provider.failure = errors.New("private-uncertain-provider-error")
	spec.Revision = "uncertain-revision"
	if _, err := runner.Run(context.Background(), spec); err == nil {
		t.Fatal("uncertain fixture unexpectedly succeeded")
	}
	if _, err := runner.Run(context.Background(), spec); !errors.Is(err, inference.ErrInFlight) {
		t.Fatalf("uncertain result was regenerated: %v", err)
	}
	if len(records) != 4 || records[2].quality != nil || records[3].quality != nil || records[2].execution.Generation == nil || records[2].execution.Generation.KnownUsage || records[3].execution.Generation != nil || records[3].execution.Outcome != observability.Pending || provider.calls.Load() != 2 {
		t.Fatalf("unknown/pending outcomes were quality-scored or retried: %+v", records)
	}
}

func TestObservationSeparatesFreshReuseAndUnknownStreaming(t *testing.T) {
	started := time.Date(2030, 1, 2, 12, 0, 0, 0, time.UTC)
	usage := inference.Usage{InputTokens: 100, UncachedInputTokens: 70, CacheReadTokens: 20, CacheWriteTokens: 10, OutputTokens: 25, ReasoningTokens: 5}
	firstContent, outputContent, historicalContent := float64(10), float64(20), float64(999)
	cost := 0.000125
	measurement := &inference.Measurement{
		StartedAt: started, EndedAt: started.Add(time.Second), Outcome: "not_durable", Fingerprint: strings.Repeat("a", 64),
		PhaseMS: map[string]float64{"generation": 100}, ContextBytes: 10, TotalContextBytes: 20, SelectedContext: 1, OmittedContext: 1,
		GenerationCalled: true, KnownGenerationUsage: true, GenerationUsage: &usage,
		GenerationStartedAt: started.Add(100 * time.Millisecond), GenerationEndedAt: started.Add(200 * time.Millisecond),
		ProviderSetupStartedAt: started.Add(-2 * time.Millisecond), ProviderSetupEndedAt: started, ProviderSetupMS: 2,
		GenerationTiming: &inference.GenerationTiming{Streaming: true, FirstTokenMS: &firstContent, OutputMS: &outputContent, Basis: "client_observed_content_chunks"},
	}
	result := inference.Result{Measurement: measurement, Response: inference.Response{Model: "actual-model", Complete: true}, GenerationUsage: usage, ThisRunUsage: usage, CostUSD: &cost,
		GenerationTiming: &inference.GenerationTiming{Streaming: true, FirstTokenMS: &historicalContent, Basis: "client_observed_content_chunks"}}
	execution := observation(inference.Spec{Provider: "anthropic", Model: "requested-model"}, result)
	if execution.Provider != "claude" || execution.StartedAt != measurement.ProviderSetupStartedAt || execution.EndedAt != measurement.EndedAt || execution.Outcome != observability.NotDurable || execution.Durable || execution.PhaseMS["provider_setup"] != 2 {
		t.Fatalf("execution/setup metadata was changed or omitted: %+v", execution)
	}
	generation := execution.Generation
	if generation == nil || generation.Model != "actual-model" || generation.Outcome != observability.Complete || !generation.KnownUsage || generation.Usage != observedUsage(usage) || generation.CostUSD == nil || *generation.CostUSD != cost || generation.FirstTokenMS == nil || *generation.FirstTokenMS != firstContent || generation.OutputMS == nil || *generation.OutputMS != outputContent || generation.TimingBasis != "client_observed_content_chunks" || execution.ThisRunUsage != generation.Usage {
		t.Fatalf("fresh generation lost billing or used historical timing: %+v", generation)
	}
	execution.PhaseMS["generation"] = 999
	if measurement.PhaseMS["generation"] != 100 {
		t.Fatal("observation mutated invocation measurement phases")
	}

	result.Reused, result.Durable = true, true
	result.ThisRunUsage, result.AvoidedUsage = inference.Usage{}, usage
	result.Measurement = &inference.Measurement{StartedAt: started, EndedAt: started.Add(time.Millisecond), Outcome: "reused", PhaseMS: map[string]float64{"lookup": 1}}
	result.Response.Timing = result.GenerationTiming // Historical timing must never create a new generation.
	result.AvoidedCostUSD = &cost
	execution = observation(inference.Spec{Provider: "ollama", Model: "requested-model"}, result)
	if !execution.Reused || execution.Generation != nil || execution.ThisRunUsage != (observability.Usage{}) || execution.OriginalUsage != observedUsage(usage) || execution.AvoidedUsage != observedUsage(usage) || execution.CostUSD == nil || *execution.CostUSD != 0 || execution.AvoidedCostUSD == nil || *execution.AvoidedCostUSD != cost {
		t.Fatalf("reuse was billed or historical streaming became current: %+v", execution)
	}
	if *result.CostUSD != cost {
		t.Fatal("zero-cost reuse observation mutated original result cost")
	}

	unknown := inference.Result{Measurement: &inference.Measurement{
		StartedAt: started, EndedAt: started.Add(time.Second), Outcome: "uncertain", GenerationCalled: true,
		GenerationStartedAt: started, GenerationEndedAt: started.Add(time.Second),
		GenerationTiming: &inference.GenerationTiming{Streaming: true, FirstTokenMS: &firstContent, Basis: "client_observed_content_chunks"},
	}, GenerationTiming: result.GenerationTiming}
	execution = observation(inference.Spec{Provider: "ollama", Model: "requested-model"}, unknown)
	if execution.Outcome != observability.Uncertain || execution.Generation == nil || execution.Generation.KnownUsage || execution.Generation.CostUSD != nil || execution.CostUSD != nil || execution.Generation.Usage != (observability.Usage{}) || execution.ThisRunUsage != (observability.Usage{}) || execution.Generation.FirstTokenMS == nil || *execution.Generation.FirstTokenMS != firstContent || execution.Generation.OutputMS != nil {
		t.Fatalf("unknown stream fabricated zero billing or completion timing: %+v", execution)
	}
	measurement.Outcome = "complete"
	result = inference.Result{Measurement: measurement, Response: inference.Response{Complete: true}, GenerationUsage: usage, ThisRunUsage: usage}
	execution = observation(inference.Spec{Provider: "ollama", Model: "requested-model"}, result)
	if execution.CostUSD == nil || *execution.CostUSD != 0 || execution.Generation.CostUSD == nil || *execution.Generation.CostUSD != 0 {
		t.Fatal("known local generation did not export its explicit zero provider token cost")
	}
	if !reflect.DeepEqual(observation(inference.Spec{}, inference.Result{}), observability.Execution{}) {
		t.Fatal("missing measurement invented an observation")
	}
}

func TestModelRunExporterFailurePreservesGenerationAndDurableAccounting(t *testing.T) {
	broker, err := newModelQuestFixture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer broker.close()
	const answer = "private-approved-generation-answer"
	const key = "fixture-provider-key"
	const rejection = "private-langfuse-server-response"
	t.Setenv("QUESTLOCK_TOKEN", broker.tokens[0])
	t.Setenv("OPENAI_API_KEY", key)
	var counts, generations, exports atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/responses/input_tokens":
			counts.Add(1)
			fmt.Fprintln(w, `{"input_tokens":100}`)
		case "/responses":
			generations.Add(1)
			json.NewEncoder(w).Encode(map[string]any{
				"id": "fixture-result", "model": "gpt-4.1-mini", "status": "completed",
				"output": []any{map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]string{"type": "output_text", "text": answer}}}},
				"usage":  map[string]any{"input_tokens": 100, "output_tokens": 25, "input_tokens_details": map[string]int{"cached_tokens": 0}, "output_tokens_details": map[string]int{"reasoning_tokens": 0}},
			})
		default:
			t.Errorf("unexpected fixture route: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	langfuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exports.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		for _, private := range []string{answer, key, broker.tokens[0], validModelSpec().Input} {
			if bytes.Contains(body, []byte(private)) {
				t.Error("telemetry payload exposed approved content or credentials")
			}
		}
		http.Error(w, rejection, http.StatusServiceUnavailable)
	}))
	defer langfuse.Close()
	t.Setenv("QUESTLOCK_LANGFUSE_ENDPOINT", langfuse.URL+"/api/public/otel/v1/traces")
	t.Setenv("LANGFUSE_PUBLIC_KEY", "fixture-public")
	t.Setenv("LANGFUSE_SECRET_KEY", "fixture-secret")
	spec := validModelSpec()
	spec.MaxInputTokens = 100
	spec.ReuseForSeconds = 3600
	spec.Prices = &inference.Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
	spec.MaxCostUSD, spec.DailyBudgetUSD = 0.001, 0.0005
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	path := saveModelSpec(t, data)
	invoke := func() inference.Result {
		t.Helper()
		var output, diagnostics bytes.Buffer
		if err := modelCommandIO([]string{"run", "--spec", path, "--broker", broker.server.URL, "--api-base-url", provider.URL, "--langfuse", "--langfuse-timeout", "100ms", "--timeout", "5s"}, &output, &diagnostics); err != nil {
			t.Fatalf("optional exporter failure changed command success: %v", err)
		}
		var result inference.Result
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		var receipt struct {
			Langfuse struct {
				Status      string                `json:"status"`
				ScoreStatus string                `json:"score_status"`
				Receipt     observability.Receipt `json:"receipt"`
			} `json:"langfuse"`
		}
		if err := json.Unmarshal(diagnostics.Bytes(), &receipt); err != nil || receipt.Langfuse.Status != "unconfirmed" || receipt.Langfuse.ScoreStatus != "not_requested" || receipt.Langfuse.Receipt.TraceID == "" || receipt.Langfuse.Receipt.RootSpanID == "" {
			t.Fatalf("export failure lacks a controlled receipt: %v, %s", err, diagnostics.String())
		}
		if (receipt.Langfuse.Receipt.GenerationSpanID == "") != result.Reused || strings.Contains(diagnostics.String(), rejection) || strings.Contains(diagnostics.String(), "fixture-secret") || strings.Contains(diagnostics.String(), langfuse.URL) {
			t.Fatal("export failure diagnostics leaked content or misrepresented generation")
		}
		return result
	}
	first := invoke()
	if first.Reused || !first.Durable || !first.Response.Complete || first.Response.Text != answer || first.ThisRunUsage.InputTokens != 100 || first.ThisRunUsage.OutputTokens != 25 || first.CostUSD == nil || *first.CostUSD != 0.000125 {
		t.Fatalf("failed export changed the known generation: %+v", first)
	}
	before, err := broker.clients[0].List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ledgers := 0
	for i, artifact := range before {
		artifact, err = broker.clients[0].Get(context.Background(), artifact.Key)
		if err != nil {
			t.Fatal(err)
		}
		before[i] = artifact
		if strings.HasPrefix(artifact.Key, "inference/v1/budget/") {
			ledgers++
			var ledger struct {
				Entries map[string]int64 `json:"entries"`
			}
			if err := json.Unmarshal([]byte(artifact.Content), &ledger); err != nil || len(ledger.Entries) != 1 {
				t.Fatalf("accounting did not retain one settled generation: %v, %+v", err, ledger)
			}
			for _, charge := range ledger.Entries {
				if charge != 125 {
					t.Fatalf("export failure changed settled microdollars: %d", charge)
				}
			}
		}
	}
	if ledgers != 1 {
		t.Fatal("generation did not create its expected daily accounting ledger")
	}
	second := invoke()
	after, err := broker.clients[0].List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, artifact := range after {
		after[i], err = broker.clients[0].Get(context.Background(), artifact.Key)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !second.Reused || !second.Durable || second.Response.Text != answer || second.ThisRunUsage != (inference.Usage{}) || second.AvoidedUsage != first.GenerationUsage || second.CostUSD == nil || *second.CostUSD != 0 || second.AvoidedCostUSD == nil || *second.AvoidedCostUSD != *first.CostUSD || generations.Load() != 1 || counts.Load() != 1 || exports.Load() != 2 || !reflect.DeepEqual(telemetryArtifactSnapshot(before), telemetryArtifactSnapshot(after)) {
		t.Fatalf("export failure caused retry or changed durable accounting: second=%+v counts=%d generations=%d exports=%d", second, counts.Load(), generations.Load(), exports.Load())
	}
}

func telemetryArtifactSnapshot(artifacts []protocol.Artifact) map[string]protocol.Artifact {
	result := make(map[string]protocol.Artifact, len(artifacts))
	for _, artifact := range artifacts {
		result[artifact.Key] = artifact
	}
	return result
}
