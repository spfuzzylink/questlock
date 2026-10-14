// Command langfuse-smoke validates the exporter against a disposable Langfuse
// v4 server. It exports deterministic metadata fixtures, never invokes a model,
// and reads the accepted observations and scores back from the real service.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spfuzzylink/questlock/observability"
)

const maxReadBytes = 128 << 10

type reader struct {
	baseURL, publicKey, secretKey string
	client                        *http.Client
}

type observation struct {
	ID                  string             `json:"id"`
	TraceID             string             `json:"traceId"`
	ParentObservationID *string            `json:"parentObservationId"`
	Type                string             `json:"type"`
	Name                string             `json:"name"`
	Model               *string            `json:"model"`
	Input               json.RawMessage    `json:"input"`
	Output              json.RawMessage    `json:"output"`
	UserID              *string            `json:"userId"`
	SessionID           *string            `json:"sessionId"`
	StatusMessage       *string            `json:"statusMessage"`
	PromptName          *string            `json:"promptName"`
	Metadata            map[string]any     `json:"metadata"`
	UsageDetails        map[string]int64   `json:"usageDetails"`
	CostDetails         map[string]float64 `json:"costDetails"`
	TotalCost           *float64           `json:"totalCost"`
	Latency             *float64           `json:"latency"`
	CompletionStartTime *time.Time         `json:"completionStartTime"`
	TimeToFirstToken    *float64           `json:"timeToFirstToken"`
}

type score struct {
	Name     string   `json:"name"`
	DataType string   `json:"dataType"`
	Value    *float64 `json:"value"`
	Subject  struct {
		Kind    string `json:"kind"`
		ID      string `json:"id"`
		TraceID string `json:"traceId"`
	} `json:"subject"`
}

type fixture struct {
	name      string
	event     observability.Execution
	score     float64
	receipt   observability.Receipt
	usage     map[string]int64
	cost      float64
	costKind  string
	completed bool
}

func main() {
	if err := run(); err != nil {
		// Errors never interpolate API responses, private credentials, or URLs.
		fmt.Fprintln(os.Stderr, "Langfuse live contract failed:", err)
		os.Exit(1)
	}
}

func run() error {
	base := os.Getenv("LANGFUSE_BASE_URL")
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("disposable smoke requires an explicit literal loopback HTTP base URL")
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !ip.IsLoopback() || ip.Zone() != "" {
		return errors.New("disposable smoke requires an explicit literal loopback HTTP base URL")
	}
	publicKey, secretKey := os.Getenv("LANGFUSE_PUBLIC_KEY"), os.Getenv("LANGFUSE_SECRET_KEY")
	exporter, err := observability.NewLangfuse(observability.Config{
		Endpoint: base + "/api/public/otel/v1/traces", PublicKey: publicKey,
		SecretKey: secretKey, Release: "0.3.0", Timeout: 5 * time.Second,
	})
	if err != nil {
		return errors.New("invalid disposable exporter configuration")
	}
	read := reader{baseURL: base, publicKey: publicKey, secretKey: secretKey, client: &http.Client{
		Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 16 << 10},
		Timeout:   5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	fixtures := fixtures(time.Now().UTC().Add(-time.Second))
	for i := range fixtures {
		f := &fixtures[i]
		// Exactly one attempt per fixture. A failed export or score is terminal;
		// polling below only performs reads and cannot manufacture acceptance.
		f.receipt, err = exporter.Export(ctx, f.event)
		if err != nil {
			return fmt.Errorf("%s export was not acknowledged", f.name)
		}
		if err := exporter.Score(ctx, f.receipt, f.score); err != nil {
			return fmt.Errorf("%s quality score was not acknowledged", f.name)
		}
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		complete := true
		for i := range fixtures {
			f := &fixtures[i]
			if f.completed {
				continue
			}
			observations, ready, err := read.observations(ctx, f.receipt)
			if err != nil {
				return fmt.Errorf("%s observation read failed: %w", f.name, err)
			}
			if !ready {
				complete = false
				continue
			}
			if err := validateObservations(*f, observations); err != nil {
				return fmt.Errorf("%s: %w", f.name, err)
			}
			scores, ready, err := read.scores(ctx, f.receipt)
			if err != nil {
				return fmt.Errorf("%s score read failed: %w", f.name, err)
			}
			if !ready {
				complete = false
				continue
			}
			if err := validateScore(*f, scores); err != nil {
				return fmt.Errorf("%s: %w", f.name, err)
			}
			f.completed = true
		}
		if complete {
			fmt.Println("PASS: self-hosted Langfuse accepted and returned 3 roots, 2 generation children, exclusive token buckets, metadata-only privacy, explicit estimate/local-zero costs, zero-new-usage reuse, streamed first-content/output timing, and root quality scores 1/1/0.")
			fmt.Println("Fixtures are deterministic API-contract checks; no model inference or paid API call was made.")
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("bounded ingestion read timeout; no export, score, or generation was retried")
		case <-ticker.C:
		}
	}
}

func fixtures(start time.Time) []fixture {
	usage := observability.Usage{InputTokens: 100, UncachedInputTokens: 60, CacheReadTokens: 30, CacheWriteTokens: 10, OutputTokens: 40, ReasoningTokens: 8}
	estimate, zero := 0.0042, 0.0
	first, output := 10.0, 20.0
	e := observability.Execution{
		StartedAt: start, EndedAt: start.Add(100 * time.Millisecond),
		Fingerprint: strings.Repeat("a", 64), Provider: "anthropic", Model: "questlock-smoke-model",
		Outcome: observability.Complete, Durable: true,
		ContextBytes: 500, TotalContextBytes: 1000, SelectedContext: 2, OmittedContext: 1,
		ThisRunUsage: usage, CostUSD: &estimate,
		Generation: &observability.Generation{StartedAt: start.Add(25 * time.Millisecond), EndedAt: start.Add(75 * time.Millisecond), Model: "questlock-smoke-model", Outcome: observability.Complete, KnownUsage: true, Usage: usage, CostUSD: &estimate},
		PhaseMS:    map[string]float64{"generation": 50, "plan": 5},
	}
	r := observability.Execution{
		StartedAt: start.Add(200 * time.Millisecond), EndedAt: start.Add(210 * time.Millisecond),
		Fingerprint: e.Fingerprint, Provider: e.Provider, Model: e.Model,
		Outcome: observability.Reused, Reused: true, Durable: true,
		ContextBytes: 500, TotalContextBytes: 1000, SelectedContext: 2, OmittedContext: 1,
		OriginalUsage: usage, AvoidedUsage: usage, CostUSD: &zero, AvoidedCostUSD: &estimate,
		PhaseMS: map[string]float64{"lookup": 10},
	}
	l := observability.Execution{
		StartedAt: start.Add(300 * time.Millisecond), EndedAt: start.Add(400 * time.Millisecond),
		Fingerprint: strings.Repeat("b", 64), Provider: "ollama", Model: "questlock-smoke-local-model",
		Outcome: observability.Complete, Durable: true,
		ThisRunUsage: observability.Usage{InputTokens: 7, UncachedInputTokens: 7, OutputTokens: 3}, CostUSD: &zero,
		Generation: &observability.Generation{StartedAt: start.Add(325 * time.Millisecond), EndedAt: start.Add(375 * time.Millisecond), Model: "questlock-smoke-local-model", Outcome: observability.Complete, KnownUsage: true, Usage: observability.Usage{InputTokens: 7, UncachedInputTokens: 7, OutputTokens: 3}, CostUSD: &zero, FirstTokenMS: &first, OutputMS: &output, TimingBasis: "client_observed_content_chunks"},
	}
	return []fixture{
		{name: "estimated-generation", event: e, score: 1, cost: estimate, costKind: "caller_estimate", usage: map[string]int64{"input": 60, "input_cached_tokens": 30, "input_cache_creation": 10, "output": 32, "output_reasoning_tokens": 8, "total": 140}},
		{name: "snapshot-reuse", event: r, score: 1, cost: 0, costKind: "caller_estimate"},
		{name: "local-generation-quality-fail", event: l, score: 0, cost: 0, costKind: "local_zero", usage: map[string]int64{"input": 7, "input_cached_tokens": 0, "input_cache_creation": 0, "output": 3, "output_reasoning_tokens": 0, "total": 10}},
	}
}

func (r reader) get(ctx context.Context, path string, query url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+path+"?"+query.Encode(), nil)
	if err != nil {
		return errors.New("invalid read request")
	}
	req.SetBasicAuth(r.publicKey, r.secretKey)
	req.Header.Set("Accept", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return errors.New("read transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("read HTTP status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxReadBytes+1))
	if err != nil || len(data) > maxReadBytes || json.Unmarshal(data, out) != nil {
		return errors.New("invalid or oversized read schema")
	}
	return nil
}

func (r reader) observations(ctx context.Context, receipt observability.Receipt) ([]observation, bool, error) {
	var response struct {
		Data []observation `json:"data"`
		Meta struct {
			Cursor *string `json:"cursor"`
		} `json:"meta"`
	}
	err := r.get(ctx, "/api/public/v2/observations", url.Values{
		"traceId": {receipt.TraceID}, "limit": {"10"},
		"fields": {"basic,time,io,metadata,model,usage,prompt,metrics"},
	}, &response)
	if err != nil {
		return nil, false, err
	}
	if response.Meta.Cursor != nil && *response.Meta.Cursor != "" {
		return nil, false, errors.New("unexpected pagination for a bounded single trace")
	}
	want := 1
	if receipt.GenerationSpanID != "" {
		want++
	}
	return response.Data, len(response.Data) >= want, nil
}

func (r reader) scores(ctx context.Context, receipt observability.Receipt) ([]score, bool, error) {
	var response struct {
		Data []score `json:"data"`
		Meta struct {
			Cursor *string `json:"cursor"`
		} `json:"meta"`
	}
	err := r.get(ctx, "/api/public/v3/scores", url.Values{
		"traceId": {receipt.TraceID}, "observationId": {receipt.RootSpanID},
		"name": {"questlock.quality"}, "limit": {"10"}, "fields": {"subject"},
	}, &response)
	if err != nil {
		return nil, false, err
	}
	if response.Meta.Cursor != nil && *response.Meta.Cursor != "" {
		return nil, false, errors.New("unexpected pagination for one root score")
	}
	return response.Data, len(response.Data) > 0, nil
}

func validateObservations(f fixture, observations []observation) error {
	want := 1
	if f.event.Generation != nil {
		want++
	}
	if len(observations) != want {
		return errors.New("unexpected observation count; generation was duplicated or missing")
	}
	var root *observation
	var child *observation
	for i := range observations {
		o := &observations[i]
		if o.TraceID != f.receipt.TraceID {
			return errors.New("trace filter returned an unrelated observation")
		}
		if !emptyIO(o.Input) || !emptyIO(o.Output) || nonempty(o.UserID) || nonempty(o.SessionID) || nonempty(o.StatusMessage) || nonempty(o.PromptName) {
			return errors.New("metadata-only privacy contract violated")
		}
		if o.ID == f.receipt.RootSpanID && root == nil {
			root = o
		} else if o.ID == f.receipt.GenerationSpanID && child == nil {
			child = o
		} else {
			return errors.New("unexpected or duplicate observation ID")
		}
	}
	if root == nil || root.Type != "SPAN" || root.Name != "questlock.run" || nonempty(root.ParentObservationID) {
		return errors.New("root span schema or relationship mismatch")
	}
	for _, n := range root.UsageDetails {
		if n != 0 {
			return errors.New("root span duplicates generation usage")
		}
	}
	for _, n := range root.CostDetails {
		if n != 0 {
			return errors.New("root span duplicates generation cost")
		}
	}
	if root.TotalCost != nil && *root.TotalCost != 0 {
		return errors.New("root span duplicates generation cost")
	}
	if root.CompletionStartTime != nil || root.TimeToFirstToken != nil {
		return errors.New("root span must not invent a generation first-token time")
	}
	if root.Metadata["questlock_outcome"] != string(f.event.Outcome) || root.Metadata["questlock_provider"] != f.event.Provider || root.Metadata["questlock_cost_source"] != f.costKind || root.Metadata["questlock_reused"] != f.event.Reused {
		return errors.New("root outcome, provider, reuse, or explicit cost classification mismatch")
	}
	for key, expected := range map[string]float64{
		"questlock_this_run_input_tokens":  float64(f.event.ThisRunUsage.InputTokens),
		"questlock_this_run_output_tokens": float64(f.event.ThisRunUsage.OutputTokens),
		"questlock_this_run_cost_usd":      f.cost,
	} {
		if !equalNumber(root.Metadata[key], expected) {
			return errors.New("root this-run metadata is missing or inaccurate")
		}
	}
	if f.event.Reused {
		if child != nil || root.Metadata["questlock_generation_attempted"] != false || !equalNumber(root.Metadata["questlock_original_input_tokens"], 100) || !equalNumber(root.Metadata["questlock_original_output_tokens"], 40) || !equalNumber(root.Metadata["questlock_avoided_input_tokens"], 100) || !equalNumber(root.Metadata["questlock_avoided_output_tokens"], 40) || !equalNumber(root.Metadata["questlock_avoided_cost_usd"], 0.0042) {
			return errors.New("reuse must preserve original/avoided usage separately from zero new usage")
		}
		return nil
	}
	if child == nil || child.Type != "GENERATION" || child.Name != "questlock.generate" || child.ParentObservationID == nil || *child.ParentObservationID != root.ID || child.Model == nil || *child.Model != f.event.Model {
		return errors.New("one generation child with the correct root and model was not returned")
	}
	for key, expected := range f.usage {
		if child.UsageDetails[key] != expected {
			return errors.New("generation exclusive cache/reasoning token bucket mismatch")
		}
	}
	if len(child.UsageDetails) != len(f.usage) {
		return errors.New("unexpected generation usage bucket")
	}
	actualCost, hasCost := child.CostDetails["total"]
	if !hasCost || !equalNumber(actualCost, f.cost) || child.TotalCost == nil || !equalNumber(*child.TotalCost, f.cost) || child.Metadata["questlock_cost_source"] != f.costKind {
		return errors.New("generation explicit cost was altered or classified incorrectly")
	}
	if child.Latency == nil || !equalNumber(*child.Latency, 0.05) {
		return errors.New("non-streaming generation duration is missing or inaccurate")
	}
	if f.event.Generation.FirstTokenMS != nil {
		wantFirst := f.event.Generation.StartedAt.Add(10 * time.Millisecond)
		if child.CompletionStartTime == nil || math.Abs(float64(child.CompletionStartTime.Sub(wantFirst))) > float64(time.Millisecond) || child.TimeToFirstToken == nil || !equalNumber(*child.TimeToFirstToken, 0.01) || child.Metadata["questlock_timing_basis"] != "client_observed_content_chunks" || !equalNumber(child.Metadata["questlock_first_content_ms"], 10) || !equalNumber(child.Metadata["questlock_output_content_ms"], 20) {
			return errors.New("streamed client first-content/output timing mapping mismatch")
		}
	} else if child.CompletionStartTime != nil || child.TimeToFirstToken != nil {
		return errors.New("non-streamed generation must not invent first-token timing")
	}
	return nil
}

func validateScore(f fixture, scores []score) error {
	if len(scores) != 1 || scores[0].Name != "questlock.quality" || scores[0].DataType != "NUMERIC" || scores[0].Value == nil || *scores[0].Value != f.score || scores[0].Subject.Kind != "observation" || scores[0].Subject.ID != f.receipt.RootSpanID || scores[0].Subject.TraceID != f.receipt.TraceID {
		return errors.New("quality score value or root observation relationship mismatch")
	}
	return nil
}

func equalNumber(actual any, expected float64) bool {
	value, ok := actual.(float64)
	return ok && !math.IsNaN(value) && math.Abs(value-expected) < 1e-9
}

func emptyIO(value json.RawMessage) bool {
	return len(value) == 0 || string(value) == "null" || string(value) == `""`
}

func nonempty(value *string) bool {
	return value != nil && *value != ""
}
