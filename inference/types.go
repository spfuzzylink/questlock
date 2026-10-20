// Package inference runs bounded, reusable text-generation jobs through Questlock.
// The broker stores coordination and results; API credentials stay in the worker.
package inference

import (
	"context"
	"time"

	"github.com/spfuzzylink/questlock/protocol"
)

// Spec is an explicit snapshot of a task. Reuse is opt-in and should only be
// enabled for tasks whose answer remains valid for the supplied revision.
type Spec struct {
	OpenRouter      *OpenRouterConfig `json:"openrouter,omitempty"`
	SGLang          *SGLangConfig     `json:"sglang,omitempty"`
	Ollama          *OllamaOptions    `json:"ollama,omitempty"`
	Stream          bool              `json:"stream,omitempty"` // Optional local Ollama content streaming.
	Task            string            `json:"task"`
	Revision        string            `json:"revision"`
	Provider        string            `json:"provider"`
	Model           string            `json:"model"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
	Instructions    string            `json:"instructions"`
	Input           string            `json:"input"`
	Context         []ContextItem     `json:"context,omitempty"`
	MaxContextBytes int               `json:"max_context_bytes"`
	MaxInputTokens  int64             `json:"max_input_tokens"`
	MaxOutputTokens int64             `json:"max_output_tokens"`
	CacheMode       string            `json:"cache_mode,omitempty"` // default or prefix
	ReuseForSeconds int64             `json:"reuse_for_seconds,omitempty"`
	Prices          *Prices           `json:"prices,omitempty"`
	MaxCostUSD      float64           `json:"max_cost_usd,omitempty"`
	DailyBudgetUSD  float64           `json:"daily_budget_usd,omitempty"`
}

type ContextItem struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Required bool   `json:"required,omitempty"`
	Priority int    `json:"priority,omitempty"`
	Stable   bool   `json:"stable,omitempty"`
}

// Request is provider-independent, single-turn text generation without tools.
// Prefix contains approved instructions and stable reference material; Input
// contains changing reference material and the user's task.
type Request struct {
	OpenRouter      *OpenRouterConfig `json:"openrouter,omitempty"`
	SGLang          *SGLangConfig     `json:"sglang,omitempty"`
	Ollama          *OllamaOptions    `json:"ollama,omitempty"`
	Stream          bool              `json:"stream,omitempty"`
	Provider        string            `json:"provider"`
	Model           string            `json:"model"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
	ContextWindow   int64             `json:"context_window,omitempty"` // local runtime only
	Prefix          string            `json:"prefix"`
	Input           string            `json:"input"`
	MaxOutputTokens int64             `json:"max_output_tokens"`
	CacheMode       string            `json:"cache_mode"`
}

// OllamaOptions opts into local generation controls without changing runtime
// defaults for requests that omit them. Format accepts only "json" or empty;
// a non-nil Temperature must be finite and between 0 and 2, inclusive.
// KeepAliveSeconds accepts 0..300; 0 requests unloading after generation.
// Think defaults to false. Explicit true requires nonstream generation and
// installed model metadata advertising boolean true; reasoning is discarded.
// GenerationTimeoutSeconds accepts 1..600 for generation HTTP only; omission
// preserves the 120-second default. Earlier caller deadlines still apply.
// PromptMode accepts empty for legacy framing or "bounded_text" for the exact
// BoundedTextPrompt user prompt. Its request system is empty unless an explicit
// OutputTypes contract adds the declared type hint and effective model system.
// OutputTypes is an optional manually declared JSON object contract: at most
// 32 lower snake_case fields with primitive integer/string/number/boolean types.
// It requires Format=json and supplies no answer values or property constraints.
// The serialized decoder schema is limited to 4096 bytes; empty maps are invalid.
type OllamaOptions struct {
	Format                   string            `json:"format,omitempty"`
	Temperature              *float64          `json:"temperature,omitempty"`
	KeepAliveSeconds         *int64            `json:"keep_alive_seconds,omitempty"`
	Think                    *bool             `json:"think,omitempty"`
	GenerationTimeoutSeconds *int64            `json:"generation_timeout_seconds,omitempty"`
	PromptMode               string            `json:"prompt_mode,omitempty"`
	OutputTypes              map[string]string `json:"output_types,omitempty"`
}

type Plan struct {
	Fingerprint       string   `json:"fingerprint"`
	Request           Request  `json:"request"`
	Selected          []string `json:"selected_context"`
	Omitted           []string `json:"omitted_context"`
	ContextBytes      int      `json:"context_bytes"`
	TotalContextBytes int      `json:"total_context_bytes"`
}

// Usage partitions input tokens into uncached, cache reads, and cache writes.
// InputTokens is their sum on all providers. Reasoning is already included in
// OutputTokens, and must not be charged a second time.
type Usage struct {
	InputTokens         int64 `json:"input_tokens"`
	UncachedInputTokens int64 `json:"uncached_input_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheWriteTokens    int64 `json:"cache_write_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens,omitempty"`
}

type Response struct {
	RuntimeCache *RuntimeCacheObservation `json:"runtime_cache,omitempty"`
	Timing       *GenerationTiming        `json:"timing,omitempty"`
	ID           string                   `json:"id"`
	Model        string                   `json:"model"`
	Text         string                   `json:"text"`
	Complete     bool                     `json:"complete"`
	StopReason   string                   `json:"stop_reason"`
	Usage        Usage                    `json:"usage"`
}

// RuntimeCacheObservation describes compute reuse reported by a serving runtime.
// These counters are not billing cache buckets or proof of an invoice discount.
// A nil counter means unreported, rather than zero. Historical observations on
// a reused result describe its original generation, not a new runtime request.
type RuntimeCacheObservation struct {
	CachedTokens   *int64 `json:"cached_tokens,omitempty"`
	DeviceTokens   *int64 `json:"device_tokens,omitempty"`
	HostTokens     *int64 `json:"host_tokens,omitempty"`
	StorageTokens  *int64 `json:"storage_tokens,omitempty"`
	StorageBackend string `json:"storage_backend,omitempty"`
	Profile        string `json:"profile"`
	RuntimeVersion string `json:"runtime_version"`
	Evidence       string `json:"evidence"`
}

// SelfHosted reports the explicit no-provider-token-metering contract. It does
// not assert that hardware, electricity, infrastructure or operator time is free.
func SelfHosted(s Spec) bool {
	return s.Provider == "ollama" || (s.Provider == "sglang" && s.SGLang != nil && s.SGLang.SelfHosted)
}

// Prices are explicit USD per million tokens, not a built-in or live price list.
// CacheWrite must cover the cache policy in use, including any write premium.
type Prices struct {
	Input      float64 `json:"input_per_million"`
	CacheRead  float64 `json:"cache_read_per_million"`
	CacheWrite float64 `json:"cache_write_per_million"`
	Output     float64 `json:"output_per_million"`
}

type Result struct {
	GenerationRuntimeCache *RuntimeCacheObservation `json:"generation_runtime_cache,omitempty"`
	GenerationTiming       *GenerationTiming        `json:"generation_timing,omitempty"`
	Measurement            *Measurement             `json:"measurement,omitempty"`
	Fingerprint            string                   `json:"fingerprint"`
	Response               Response                 `json:"response"`
	Reused                 bool                     `json:"reused"`
	Durable                bool                     `json:"durable"`
	GenerationUsage        Usage                    `json:"generation_usage"`
	ThisRunUsage           Usage                    `json:"this_run_usage"`
	AvoidedUsage           Usage                    `json:"avoided_usage"`
	CostUSD                *float64                 `json:"cost_usd,omitempty"`
	AvoidedCostUSD         *float64                 `json:"avoided_cost_usd,omitempty"`
	CreatedAt              time.Time                `json:"created_at"`
	ExpiresAt              time.Time                `json:"expires_at"`
}

type Broker interface {
	Get(context.Context, string) (protocol.Artifact, error)
	Publish(context.Context, protocol.PublishRequest) (protocol.PublishResult, error)
}

type Provider interface {
	// Identity distinguishes upstream endpoints in durable request fingerprints.
	Identity() string
	CountInputTokens(context.Context, Request) (int64, error)
	Generate(context.Context, Request) (Response, error)
}
