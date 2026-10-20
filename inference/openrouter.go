package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// OpenRouterConfig describes one explicitly approved route. The input bound is
// supplied by the operator: OpenRouter has no documented preflight counting API
// for arbitrary models. It is a reservation bound, not a measured token count.
// CacheWriteUsage must be "required" for a route with cache-write pricing, or
// "not_applicable" for a route whose cache writes have no separate token charge.
type OpenRouterConfig struct {
	UpstreamProvider     string `json:"upstream_provider"`
	InputTokenUpperBound int64  `json:"input_token_upper_bound"`
	CacheWriteUsage      string `json:"cache_write_usage"`
}

func validateOpenRouterConfig(c OpenRouterConfig) error {
	if c.UpstreamProvider == "" || len(c.UpstreamProvider) > 100 {
		return errors.New("OpenRouter requires one explicit upstream provider slug")
	}
	for _, r := range c.UpstreamProvider {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return errors.New("OpenRouter upstream provider must be a lowercase provider slug without endpoint or routing variants")
		}
	}
	if c.InputTokenUpperBound < 1 || c.InputTokenUpperBound > maxProviderTokens {
		return errors.New("OpenRouter requires an explicit input token upper bound between 1 and 1000000000")
	}
	if c.CacheWriteUsage != "required" && c.CacheWriteUsage != "not_applicable" {
		return errors.New("OpenRouter cache_write_usage must be required or not_applicable")
	}
	return nil
}

// OpenRouterProvider implements a deliberately narrow Chat Completions route:
// one model, one upstream, no fallbacks, retries, tools, or streaming. It does
// not translate OpenAI Responses cache breakpoints into gateway parameters.
type OpenRouterProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
	config  OpenRouterConfig
}

func NewOpenRouterProvider(apiKey, baseURL string, config OpenRouterConfig) (*OpenRouterProvider, error) {
	if err := validateOpenRouterConfig(config); err != nil {
		return nil, err
	}
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	// Reuse only the URL/credential validation and hardened transport. The wire
	// request and usage normalization below are OpenRouter-specific.
	h, err := NewHTTPProvider("openai", apiKey, baseURL)
	if err != nil {
		return nil, err
	}
	return &OpenRouterProvider{apiKey: apiKey, baseURL: h.baseURL, client: h.client, config: config}, nil
}

func (p *OpenRouterProvider) Identity() string {
	return "openrouter:" + p.baseURL + ":" + p.config.UpstreamProvider + ":" + strconv.FormatInt(p.config.InputTokenUpperBound, 10) + ":" + p.config.CacheWriteUsage
}

func (p *OpenRouterProvider) validate(req Request) error {
	if req.Ollama != nil {
		return errors.New("ollama settings require the ollama provider")
	}
	if req.Provider != "openrouter" || req.OpenRouter == nil || *req.OpenRouter != p.config {
		return errors.New("OpenRouter request must match the explicitly configured route and counting policy")
	}
	if req.Stream || req.ReasoningEffort != "" || req.ContextWindow != 0 {
		return errors.New("OpenRouter adapter does not support streaming, reasoning effort overrides, or local context windows")
	}
	if req.CacheMode != "" && req.CacheMode != "default" {
		return errors.New("OpenRouter adapter supports only default provider-managed caching")
	}
	parts := strings.Split(req.Model, "/")
	if len(parts) != 2 || len(req.Model) > 200 || parts[0] == "" || parts[1] == "" || parts[0] == "openrouter" {
		return errors.New("OpenRouter requires an explicit author/model slug without auto routing, presets, or variants")
	}
	for _, part := range parts {
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				return errors.New("OpenRouter model contains an unsupported routing or model character")
			}
		}
	}
	if req.Input == "" || !utf8.ValidString(req.Input) || !utf8.ValidString(req.Prefix) {
		return errors.New("OpenRouter requires nonempty valid UTF-8 input")
	}
	if len(req.Prefix) > maxProviderRequestBytes || len(req.Input) > maxProviderRequestBytes || len(req.Prefix)+len(req.Input) > maxProviderRequestBytes {
		return errors.New("OpenRouter request exceeds the byte limit")
	}
	if req.MaxOutputTokens < 1 || req.MaxOutputTokens > 1_000_000 {
		return errors.New("OpenRouter max output tokens must be between 1 and 1000000")
	}
	return nil
}

// CountInputTokens returns the explicit operator-approved reservation bound.
// No tokenizer equivalence, exact preflight count, or upstream call is implied.
func (p *OpenRouterProvider) CountInputTokens(ctx context.Context, req Request) (int64, error) {
	if err := p.validate(req); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return p.config.InputTokenUpperBound, nil
}

type openRouterMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openRouterRequest struct {
	Model               string              `json:"model"`
	Messages            []openRouterMessage `json:"messages"`
	MaxCompletionTokens int64               `json:"max_completion_tokens"`
	Stream              bool                `json:"stream"`
	Provider            struct {
		Order             []string       `json:"order"`
		Only              []string       `json:"only"`
		AllowFallbacks    bool           `json:"allow_fallbacks"`
		RequireParameters bool           `json:"require_parameters"`
		MaxPrice          map[string]int `json:"max_price"`
	} `json:"provider"`
}

func (p *OpenRouterProvider) Generate(ctx context.Context, req Request) (Response, error) {
	if err := p.validate(req); err != nil {
		return Response{}, err
	}
	body := openRouterRequest{Model: req.Model, MaxCompletionTokens: req.MaxOutputTokens}
	if req.Prefix != "" {
		body.Messages = append(body.Messages, openRouterMessage{Role: "system", Content: req.Prefix})
	}
	body.Messages = append(body.Messages, openRouterMessage{Role: "user", Content: req.Input})
	body.Provider.Order = []string{p.config.UpstreamProvider}
	body.Provider.Only = []string{p.config.UpstreamProvider}
	body.Provider.RequireParameters = true
	// This adapter accounts only token prices, so per-request fees are forbidden.
	body.Provider.MaxPrice = map[string]int{"request": 0}
	var raw openRouterResponse
	if err := p.post(ctx, body, &raw); err != nil {
		return Response{}, err
	}
	return raw.normalize(req, p.config)
}

func (p *OpenRouterProvider) post(ctx context.Context, payload, result any) error {
	data, err := json.Marshal(payload)
	if err != nil || len(data) > maxProviderRequestBytes {
		return errors.New("cannot encode bounded OpenRouter request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return errors.New("cannot create OpenRouter request")
	}
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("OpenRouter request interrupted: %w", ctx.Err())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("OpenRouter request timed out: %w", context.DeadlineExceeded)
		}
		return errors.New("OpenRouter request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("OpenRouter provider returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxProviderResponseBytes {
		return errors.New("OpenRouter response exceeds the byte limit")
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, maxProviderResponseBytes+1))
	if err != nil || len(data) > maxProviderResponseBytes {
		return errors.New("cannot read bounded OpenRouter response")
	}
	if err := json.Unmarshal(data, result); err != nil {
		return errors.New("OpenRouter returned invalid JSON")
	}
	return nil
}

// The fixture contract follows OpenRouter's native-token usage accounting:
// https://openrouter.ai/docs/cookbook/administration/usage-accounting
// Cache-write omission is allowed only under an explicit not_applicable policy.
type openRouterResponse struct {
	ID       string          `json:"id"`
	Object   string          `json:"object"`
	Model    string          `json:"model"`
	Provider string          `json:"provider"`
	Error    json.RawMessage `json:"error"`
	Choices  []struct {
		Index        *int64 `json:"index"`
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Role         string          `json:"role"`
			Content      *string         `json:"content"`
			Refusal      json.RawMessage `json:"refusal"`
			ToolCalls    json.RawMessage `json:"tool_calls"`
			FunctionCall json.RawMessage `json:"function_call"`
			Images       json.RawMessage `json:"images"`
			Audio        json.RawMessage `json:"audio"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     *int64   `json:"prompt_tokens"`
		CompletionTokens *int64   `json:"completion_tokens"`
		TotalTokens      *int64   `json:"total_tokens"`
		Cost             *float64 `json:"cost"`
		PromptDetails    *struct {
			CachedTokens     *int64 `json:"cached_tokens"`
			CacheWriteTokens *int64 `json:"cache_write_tokens"`
			AudioTokens      *int64 `json:"audio_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionDetails *struct {
			ReasoningTokens *int64 `json:"reasoning_tokens"`
			AudioTokens     *int64 `json:"audio_tokens"`
		} `json:"completion_tokens_details"`
		ServerToolUse json.RawMessage `json:"server_tool_use_details"`
	} `json:"usage"`
}

func (raw openRouterResponse) normalize(req Request, config OpenRouterConfig) (Response, error) {
	if raw.Usage == nil || raw.Usage.PromptTokens == nil || raw.Usage.CompletionTokens == nil || raw.Usage.TotalTokens == nil || raw.Usage.PromptDetails == nil || raw.Usage.PromptDetails.CachedTokens == nil {
		return Response{}, errors.New("OpenRouter response is missing required token usage")
	}
	u := Usage{InputTokens: *raw.Usage.PromptTokens, OutputTokens: *raw.Usage.CompletionTokens, CacheReadTokens: *raw.Usage.PromptDetails.CachedTokens}
	if raw.Usage.PromptDetails.CacheWriteTokens != nil {
		u.CacheWriteTokens = *raw.Usage.PromptDetails.CacheWriteTokens
	} else if config.CacheWriteUsage == "required" {
		return Response{}, errors.New("OpenRouter response is missing required cache-write usage")
	}
	if config.CacheWriteUsage == "not_applicable" && u.CacheWriteTokens != 0 {
		return Response{}, errors.New("OpenRouter response contradicts the declared cache-write policy")
	}
	if raw.Usage.CompletionDetails != nil && raw.Usage.CompletionDetails.ReasoningTokens != nil {
		u.ReasoningTokens = *raw.Usage.CompletionDetails.ReasoningTokens
	}
	if !validTokenCount(u.InputTokens) || !validTokenCount(u.CacheReadTokens) || !validTokenCount(u.CacheWriteTokens) || u.CacheReadTokens+u.CacheWriteTokens > u.InputTokens || !validTokenCount(*raw.Usage.TotalTokens) || *raw.Usage.TotalTokens != u.InputTokens+u.OutputTokens {
		return Response{}, errors.New("OpenRouter response contains unsupported or invalid token partitions")
	}
	u.UncachedInputTokens = u.InputTokens - u.CacheReadTokens - u.CacheWriteTokens
	if err := validateOutputUsage(u, req); err != nil {
		return Response{}, err
	}
	if raw.Usage.Cost != nil && (math.IsNaN(*raw.Usage.Cost) || math.IsInf(*raw.Usage.Cost, 0) || *raw.Usage.Cost < 0) {
		return Response{}, errors.New("OpenRouter response contains invalid reported cost")
	}
	if raw.Usage.PromptDetails.AudioTokens != nil && *raw.Usage.PromptDetails.AudioTokens != 0 || raw.Usage.CompletionDetails != nil && raw.Usage.CompletionDetails.AudioTokens != nil && *raw.Usage.CompletionDetails.AudioTokens != 0 || nonNullJSON(raw.Usage.ServerToolUse) {
		return Response{}, errors.New("OpenRouter response contains unsupported non-text or server-tool usage")
	}
	result := Response{ID: raw.ID, Model: raw.Model, Usage: u, StopReason: "unsupported_output"}
	if len(raw.Choices) != 1 {
		return result, nil
	}
	choice := raw.Choices[0]
	if choice.Message.Content != nil {
		result.Text = *choice.Message.Content
	}
	result.Complete = raw.Object == "chat.completion" && raw.ID != "" && raw.Model == req.Model && strings.ReplaceAll(strings.ToLower(raw.Provider), " ", "-") == config.UpstreamProvider && choice.Index != nil && *choice.Index == 0 && choice.FinishReason == "stop" && choice.Message.Role == "assistant" && result.Text != "" && !nonNullJSON(raw.Error) && !nonNullJSON(choice.Message.Refusal) && !nonNullJSON(choice.Message.ToolCalls) && !nonNullJSON(choice.Message.FunctionCall) && !nonNullJSON(choice.Message.Images) && !nonNullJSON(choice.Message.Audio)
	if result.Complete {
		result.StopReason = "stop"
	} else if raw.Model != req.Model || strings.ReplaceAll(strings.ToLower(raw.Provider), " ", "-") != config.UpstreamProvider {
		result.StopReason = "route_mismatch"
	} else if choice.FinishReason == "length" || choice.FinishReason == "content_filter" || choice.FinishReason == "error" {
		result.StopReason = choice.FinishReason
	}
	if u.OutputTokens > req.MaxOutputTokens || u.InputTokens > config.InputTokenUpperBound {
		result.Complete = false
		result.StopReason = "usage_limit_exceeded"
	}
	return result, nil
}
