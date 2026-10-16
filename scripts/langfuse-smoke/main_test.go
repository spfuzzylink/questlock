package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/observability"
)

// These are read-API-shaped fixtures, not mocked exporter responses. Regressions
// must reject false billing, missing zero values, wrong subjects, and leaked I/O.
func TestReadContractRejectsDoubleBillingAndContent(t *testing.T) {
	f := fixtures(time.Now())[0]
	f.receipt = observability.Receipt{TraceID: "trace-fixture", RootSpanID: "root", GenerationSpanID: "child"}
	const reply = `[
	{"id":"root","traceId":"trace-fixture","type":"SPAN","name":"questlock.run","input":null,"output":null,"metadata":{"questlock_outcome":"complete","questlock_provider":"anthropic","questlock_reused":false,"questlock_cost_source":"caller_estimate","questlock_this_run_input_tokens":100,"questlock_this_run_output_tokens":40,"questlock_this_run_cost_usd":0.0042}},
	{"id":"child","traceId":"trace-fixture","parentObservationId":"root","type":"GENERATION","name":"questlock.generate","model":"questlock-smoke-model","input":null,"output":null,"metadata":{"questlock_cost_source":"caller_estimate"},"usageDetails":{"input":60,"input_cached_tokens":30,"input_cache_creation":10,"output":32,"output_reasoning_tokens":8,"total":140},"costDetails":{"total":0.0042},"totalCost":0.0042,"latency":0.05}
	]`
	for _, tc := range []struct {
		name   string
		mutate func([]observation)
	}{
		{"valid", func([]observation) {}},
		{"input-content", func(o []observation) { o[1].Input = json.RawMessage(`"private prompt fixture"`) }},
		{"root-double-bills", func(o []observation) { o[0].UsageDetails = map[string]int64{"input": 100} }},
		{"cache-double-counted", func(o []observation) { o[1].UsageDetails["input"] = 100 }},
		{"reasoning-double-counted", func(o []observation) { o[1].UsageDetails["output"] = 40 }},
		{"missing-cost", func(o []observation) { o[1].TotalCost = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var replyData []observation
			if err := json.Unmarshal([]byte(reply), &replyData); err != nil {
				t.Fatal(err)
			}
			tc.mutate(replyData)
			err := validateObservations(f, replyData)
			if (tc.name == "valid") != (err == nil) {
				t.Fatalf("unexpected read validation: %v", err)
			}
		})
	}
}

func TestReuseRequiresExplicitZeroAndNoGeneration(t *testing.T) {
	f := fixtures(time.Now())[1]
	f.receipt = observability.Receipt{TraceID: "trace-fixture", RootSpanID: "root"}
	const reply = `[{"id":"root","traceId":"trace-fixture","type":"SPAN","name":"questlock.run","metadata":{"questlock_outcome":"reused","questlock_provider":"anthropic","questlock_reused":true,"questlock_generation_attempted":false,"questlock_cost_source":"caller_estimate","questlock_this_run_input_tokens":0,"questlock_this_run_output_tokens":0,"questlock_this_run_cost_usd":0,"questlock_original_input_tokens":100,"questlock_original_output_tokens":40,"questlock_avoided_input_tokens":100,"questlock_avoided_output_tokens":40,"questlock_avoided_cost_usd":0.0042}}]`
	var observations []observation
	if err := json.Unmarshal([]byte(reply), &observations); err != nil {
		t.Fatal(err)
	}
	if err := validateObservations(f, observations); err != nil {
		t.Fatal(err)
	}
	delete(observations[0].Metadata, "questlock_this_run_input_tokens")
	if err := validateObservations(f, observations); err == nil {
		t.Fatal("missing zero-new-input metadata was accepted")
	}
}

func TestQualityMustAttachToCorrectRoot(t *testing.T) {
	f := fixture{score: 0, receipt: observability.Receipt{TraceID: "trace-fixture", RootSpanID: "root"}}
	const reply = `[{"name":"questlock.quality","dataType":"NUMERIC","value":0,"subject":{"kind":"observation","id":"root","traceId":"trace-fixture"}}]`
	var scores []score
	if err := json.Unmarshal([]byte(reply), &scores); err != nil {
		t.Fatal(err)
	}
	if err := validateScore(f, scores); err != nil {
		t.Fatal(err)
	}
	scores[0].Subject.ID = "generation-child"
	if err := validateScore(f, scores); err == nil {
		t.Fatal("score on wrong observation was accepted")
	}
}

func TestStreamedTimingReadMapping(t *testing.T) {
	start := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	f := fixtures(start)[2]
	f.receipt = observability.Receipt{TraceID: "trace-fixture", RootSpanID: "root", GenerationSpanID: "child"}
	const reply = `[
	{"id":"root","traceId":"trace-fixture","type":"SPAN","name":"questlock.run","metadata":{"questlock_outcome":"complete","questlock_provider":"ollama","questlock_reused":false,"questlock_cost_source":"local_zero","questlock_this_run_input_tokens":7,"questlock_this_run_output_tokens":3,"questlock_this_run_cost_usd":0}},
	{"id":"child","traceId":"trace-fixture","parentObservationId":"root","type":"GENERATION","name":"questlock.generate","model":"questlock-smoke-local-model","metadata":{"questlock_cost_source":"local_zero","questlock_timing_basis":"client_observed_content_chunks","questlock_first_content_ms":10,"questlock_output_content_ms":20},"usageDetails":{"input":7,"input_cached_tokens":0,"input_cache_creation":0,"output":3,"output_reasoning_tokens":0,"total":10},"costDetails":{"total":0},"totalCost":0,"latency":0.05,"timeToFirstToken":0.01,"completionStartTime":"2026-10-14T00:00:00.335Z"}
	]`
	var observations []observation
	if err := json.Unmarshal([]byte(reply), &observations); err != nil {
		t.Fatal(err)
	}
	if err := validateObservations(f, observations); err != nil {
		t.Fatal(err)
	}
	observations[1].TimeToFirstToken = nil
	if err := validateObservations(f, observations); err == nil {
		t.Fatal("streamed generation without first-token mapping was accepted")
	}
}

func TestRuntimeCacheReadbackDoesNotBecomeBilling(t *testing.T) {
	f := fixtures(time.Now())[3]
	f.receipt = observability.Receipt{TraceID: "trace-fixture", RootSpanID: "root", GenerationSpanID: "child"}
	const reply = `[
	{"id":"root","traceId":"trace-fixture","type":"SPAN","name":"questlock.run","metadata":{"questlock_outcome":"complete","questlock_provider":"sglang","questlock_reused":false,"questlock_cost_source":"local_zero","questlock_this_run_input_tokens":100,"questlock_this_run_output_tokens":40,"questlock_this_run_cost_usd":0}},
	{"id":"child","traceId":"trace-fixture","parentObservationId":"root","type":"GENERATION","name":"questlock.generate","model":"questlock-smoke-sglang-model","metadata":{"questlock_cost_source":"local_zero","questlock_runtime_cache_profile":"hicache","questlock_runtime_cache_basis":"runtime_reported_compute_reuse","questlock_runtime_cache_cached_known":true,"questlock_runtime_cache_cached_tokens":80,"questlock_runtime_cache_device_known":true,"questlock_runtime_cache_device_tokens":50,"questlock_runtime_cache_host_known":true,"questlock_runtime_cache_host_tokens":30,"questlock_runtime_cache_storage_known":false},"usageDetails":{"input":100,"input_cached_tokens":0,"input_cache_creation":0,"output":40,"output_reasoning_tokens":0,"total":140},"costDetails":{"total":0},"totalCost":0,"latency":0.05}
	]`
	for _, kind := range []string{"valid", "billing", "missing-host", "invented-storage"} {
		t.Run(kind, func(t *testing.T) {
			var observations []observation
			if err := json.Unmarshal([]byte(reply), &observations); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "billing":
				observations[1].UsageDetails["input_cached_tokens"] = 80
			case "missing-host":
				delete(observations[1].Metadata, "questlock_runtime_cache_host_tokens")
			case "invented-storage":
				observations[1].Metadata["questlock_runtime_cache_storage_tokens"] = float64(0)
			}
			if err := validateObservations(f, observations); (err == nil) != (kind == "valid") {
				t.Fatal("runtime evidence contract mismatch", err)
			}
		})
	}
}
