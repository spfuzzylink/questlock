package observability

import (
	"encoding/json"
	"strconv"
	"time"
)

// These structs implement the trace-only subset of the standard OTLP JSON
// schema. They are not a generic attribute bag: every exported field below is
// chosen explicitly, rather than serialized from an inference request/result.
type otlpEnvelope struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

type otlpResourceSpans struct {
	Resource   otlpResource     `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}

type otlpResource struct {
	Attributes []otlpAttribute `json:"attributes"`
}

type otlpScopeSpans struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

type otlpScope struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type otlpSpan struct {
	TraceID           string          `json:"traceId"`
	SpanID            string          `json:"spanId"`
	ParentSpanID      string          `json:"parentSpanId,omitempty"`
	Name              string          `json:"name"`
	Kind              int             `json:"kind"`
	Flags             uint32          `json:"flags"`
	StartTimeUnixNano string          `json:"startTimeUnixNano"`
	EndTimeUnixNano   string          `json:"endTimeUnixNano"`
	Attributes        []otlpAttribute `json:"attributes"`
	Status            otlpStatus      `json:"status"`
}

type otlpStatus struct {
	Code int `json:"code"`
}

type otlpAttribute struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

type otlpValue struct {
	StringValue *string  `json:"stringValue,omitempty"`
	IntValue    *string  `json:"intValue,omitempty"`
	BoolValue   *bool    `json:"boolValue,omitempty"`
	DoubleValue *float64 `json:"doubleValue,omitempty"`
}

const metadataPrefix = "langfuse.observation.metadata.questlock_"

func (l *Langfuse) envelope(e Execution, r Receipt) otlpEnvelope {
	attrs := []otlpAttribute{
		stringAttribute("langfuse.trace.name", "questlock.run"),
		stringAttribute("langfuse.observation.type", "span"),
		boolAttribute("langfuse.trace.public", false),
		stringAttribute(metadataPrefix+"outcome", string(e.Outcome)),
		boolAttribute(metadataPrefix+"reused", e.Reused),
		boolAttribute(metadataPrefix+"durable", e.Durable),
		boolAttribute(metadataPrefix+"generation_attempted", e.Generation != nil),
		intAttribute(metadataPrefix+"context_bytes", int64(e.ContextBytes)),
		intAttribute(metadataPrefix+"total_context_bytes", int64(e.TotalContextBytes)),
		intAttribute(metadataPrefix+"omitted_context_bytes", int64(e.TotalContextBytes-e.ContextBytes)),
		intAttribute(metadataPrefix+"selected_context_count", int64(e.SelectedContext)),
		intAttribute(metadataPrefix+"omitted_context_count", int64(e.OmittedContext)),
		floatAttribute(metadataPrefix+"execution_ms", milliseconds(e.StartedAt, e.EndedAt)),
	}
	if e.Fingerprint != "" {
		attrs = append(attrs, stringAttribute(metadataPrefix+"fingerprint", e.Fingerprint))
	}
	if e.Provider != "" {
		provider := e.Provider
		if provider == "claude" {
			provider = "anthropic"
		}
		attrs = append(attrs, stringAttribute(metadataPrefix+"provider", provider))
	}
	if e.Model != "" {
		attrs = append(attrs, stringAttribute(metadataPrefix+"model", e.Model))
	}
	known := e.Generation == nil || e.Generation.KnownUsage
	attrs = append(attrs, boolAttribute(metadataPrefix+"usage_known", known))
	if known {
		attrs = append(attrs, usageMetadata("this_run", e.ThisRunUsage)...)
	}
	if e.Reused {
		attrs = append(attrs, usageMetadata("original", e.OriginalUsage)...)
		attrs = append(attrs, usageMetadata("avoided", e.AvoidedUsage)...)
	}
	if e.CostUSD != nil {
		attrs = append(attrs, floatAttribute(metadataPrefix+"this_run_cost_usd", *e.CostUSD))
		attrs = append(attrs, stringAttribute(metadataPrefix+"cost_source", costSource(e.Provider, *e.CostUSD)))
	}
	if e.AvoidedCostUSD != nil {
		attrs = append(attrs, floatAttribute(metadataPrefix+"avoided_cost_usd", *e.AvoidedCostUSD))
		attrs = append(attrs, stringAttribute(metadataPrefix+"avoided_cost_source", costSource(e.Provider, *e.AvoidedCostUSD)))
	}
	for _, name := range []string{"provider_setup", "runtime_identity", "plan", "lookup", "admission", "preflight", "reservation", "generation", "publication", "settlement", "recovery"} {
		if ms, ok := e.PhaseMS[name]; ok {
			attrs = append(attrs, floatAttribute(metadataPrefix+"phase_"+name+"_ms", ms))
		}
	}
	resourceAttrs := []otlpAttribute{stringAttribute("service.name", "questlock")}
	if l.release != "" {
		attrs = append(attrs, stringAttribute("langfuse.release", l.release))
		resourceAttrs = append(resourceAttrs, stringAttribute("service.version", l.release))
	}
	spans := []otlpSpan{span(r.TraceID, r.RootSpanID, "", "questlock.run", 1, e.StartedAt, e.EndedAt, e.Outcome, attrs)}
	if g := e.Generation; g != nil {
		generationAttrs := []otlpAttribute{
			stringAttribute("langfuse.observation.type", "generation"),
			stringAttribute(metadataPrefix+"outcome", string(g.Outcome)),
			boolAttribute(metadataPrefix+"usage_known", g.KnownUsage),
			floatAttribute(metadataPrefix+"generation_ms", milliseconds(g.StartedAt, g.EndedAt)),
		}
		if g.Model != "" {
			generationAttrs = append(generationAttrs, stringAttribute("langfuse.observation.model.name", g.Model))
		}
		if cache := g.RuntimeCache; cache != nil {
			generationAttrs = append(generationAttrs, stringAttribute(metadataPrefix+"runtime_cache_profile", cache.Profile), stringAttribute(metadataPrefix+"runtime_cache_basis", "runtime_reported_compute_reuse"))
			for _, item := range []struct {
				name  string
				value *int64
			}{{"cached", cache.CachedTokens}, {"device", cache.DeviceTokens}, {"host", cache.HostTokens}, {"storage", cache.StorageTokens}} {
				generationAttrs = append(generationAttrs, boolAttribute(metadataPrefix+"runtime_cache_"+item.name+"_known", item.value != nil))
				if item.value != nil {
					generationAttrs = append(generationAttrs, intAttribute(metadataPrefix+"runtime_cache_"+item.name+"_tokens", *item.value))
				}
			}
		}
		if g.TimingBasis != "" {
			generationAttrs = append(generationAttrs, stringAttribute(metadataPrefix+"timing_basis", g.TimingBasis))
		}
		if g.FirstTokenMS != nil {
			generationAttrs = append(generationAttrs, floatAttribute(metadataPrefix+"first_content_ms", *g.FirstTokenMS))
			// Langfuse maps this documented ISO 8601 observation attribute to
			// completionStartTime/TTFT. The accompanying basis makes clear that
			// the timestamp is client-observed content arrival, not server compute.
			first := g.StartedAt.Add(time.Duration(*g.FirstTokenMS * float64(time.Millisecond)))
			generationAttrs = append(generationAttrs, stringAttribute("langfuse.observation.completion_start_time", first.UTC().Format(time.RFC3339Nano)))
		}
		if g.OutputMS != nil {
			generationAttrs = append(generationAttrs, floatAttribute(metadataPrefix+"output_content_ms", *g.OutputMS))
		}
		if g.KnownUsage {
			// Explicit Langfuse usage is stored unchanged. Partition inclusive
			// Questlock totals exactly once so cache and reasoning are not added
			// to their parent totals and falsely counted twice.
			details := map[string]int64{
				"input":                   g.Usage.UncachedInputTokens,
				"input_cached_tokens":     g.Usage.CacheReadTokens,
				"input_cache_creation":    g.Usage.CacheWriteTokens,
				"output":                  g.Usage.OutputTokens - g.Usage.ReasoningTokens,
				"output_reasoning_tokens": g.Usage.ReasoningTokens,
				"total":                   g.Usage.InputTokens + g.Usage.OutputTokens,
			}
			encoded, _ := json.Marshal(details)
			generationAttrs = append(generationAttrs, stringAttribute("langfuse.observation.usage_details", string(encoded)))
		}
		if g.CostUSD != nil {
			encoded, _ := json.Marshal(map[string]float64{"total": *g.CostUSD})
			generationAttrs = append(generationAttrs, stringAttribute("langfuse.observation.cost_details", string(encoded)))
			generationAttrs = append(generationAttrs, stringAttribute(metadataPrefix+"cost_source", costSource(e.Provider, *g.CostUSD)))
		}
		spans = append(spans, span(r.TraceID, r.GenerationSpanID, r.RootSpanID, "questlock.generate", 3, g.StartedAt, g.EndedAt, g.Outcome, generationAttrs))
	}
	return otlpEnvelope{ResourceSpans: []otlpResourceSpans{{
		Resource:   otlpResource{Attributes: resourceAttrs},
		ScopeSpans: []otlpScopeSpans{{Scope: otlpScope{Name: "github.com/spfuzzylink/questlock/observability", Version: l.release}, Spans: spans}},
	}}}
}

func costSource(provider string, value float64) string {
	if (provider == "ollama" || provider == "sglang") && value == 0 {
		return "local_zero"
	}
	return "caller_estimate"
}

func span(traceID, spanID, parentID, name string, kind int, start, end time.Time, outcome Outcome, attrs []otlpAttribute) otlpSpan {
	status := 2 // OTLP STATUS_CODE_ERROR, with no content-bearing status message.
	level := "ERROR"
	if outcome == Complete || outcome == Reused {
		status, level = 1, "DEFAULT"
	} else if outcome == Pending || outcome == BudgetBlocked || outcome == Incomplete {
		level = "WARNING"
	}
	attrs = append(attrs, stringAttribute("langfuse.observation.level", level))
	return otlpSpan{TraceID: traceID, SpanID: spanID, ParentSpanID: parentID, Name: name, Kind: kind, Flags: 1,
		StartTimeUnixNano: strconv.FormatInt(start.UnixNano(), 10), EndTimeUnixNano: strconv.FormatInt(end.UnixNano(), 10),
		Attributes: attrs, Status: otlpStatus{Code: status}}
}

func usageMetadata(prefix string, u Usage) []otlpAttribute {
	prefix = metadataPrefix + prefix + "_"
	return []otlpAttribute{
		intAttribute(prefix+"input_tokens", u.InputTokens),
		intAttribute(prefix+"uncached_input_tokens", u.UncachedInputTokens),
		intAttribute(prefix+"cache_read_tokens", u.CacheReadTokens),
		intAttribute(prefix+"cache_write_tokens", u.CacheWriteTokens),
		intAttribute(prefix+"output_tokens", u.OutputTokens),
		intAttribute(prefix+"reasoning_tokens", u.ReasoningTokens),
	}
}

func milliseconds(start, end time.Time) float64 {
	return float64(end.Sub(start)) / float64(time.Millisecond)
}

func stringAttribute(key, value string) otlpAttribute {
	return otlpAttribute{Key: key, Value: otlpValue{StringValue: &value}}
}

func intAttribute(key string, value int64) otlpAttribute {
	s := strconv.FormatInt(value, 10)
	return otlpAttribute{Key: key, Value: otlpValue{IntValue: &s}}
}

func boolAttribute(key string, value bool) otlpAttribute {
	return otlpAttribute{Key: key, Value: otlpValue{BoolValue: &value}}
}

func floatAttribute(key string, value float64) otlpAttribute {
	return otlpAttribute{Key: key, Value: otlpValue{DoubleValue: &value}}
}
