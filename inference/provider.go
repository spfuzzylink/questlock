package inference

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxProviderRequestBytes        = 2 << 20
	maxProviderResponseBytes       = 8 << 20
	maxProviderTokens        int64 = 1_000_000_000
	providerTimeout                = 2 * time.Minute
)

// HTTPProvider sends one text-only request to OpenAI Responses or Claude
// Messages. It does not retry, follow redirects, use environment proxies, or
// fall back to another model. Credentials are held only by this worker.
type HTTPProvider struct {
	provider string
	apiKey   string
	baseURL  string
	client   *http.Client
}

// NewHTTPProvider accepts openai, anthropic, or claude. An empty baseURL uses
// the provider's public /v1 endpoint. Custom endpoints require HTTPS, except
// loopback HTTP endpoints for local gateways and tests.
func NewHTTPProvider(provider, apiKey, baseURL string) (*HTTPProvider, error) {
	provider, err := canonicalProvider(provider)
	if err != nil {
		return nil, err
	}
	if apiKey == "" || len(apiKey) > 4096 {
		return nil, errors.New("provider API key is required and must be at most 4096 bytes")
	}
	for _, c := range apiKey {
		if c < 33 || c > 126 {
			return nil, errors.New("provider API key contains invalid characters")
		}
	}
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
		if provider == "anthropic" {
			baseURL = "https://api.anthropic.com/v1"
		}
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("provider base URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, errors.New("provider base URL requires HTTPS")
	}
	loopbackHTTP := u.Scheme == "http"
	if loopbackHTTP && !isLoopbackHost(u.Hostname()) {
		return nil, errors.New("plain HTTP provider URLs are allowed only on loopback")
	}
	if u.RawPath != "" || strings.Contains(u.Path, "\\") {
		return nil, errors.New("provider base URL contains an unsupported path")
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return nil, errors.New("provider base URL contains an unsupported path")
		}
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			if loopbackHTTP {
				remote, ok := conn.RemoteAddr().(*net.TCPAddr)
				if !ok || !remote.IP.IsLoopback() {
					conn.Close()
					return nil, errors.New("plain HTTP provider connection is not loopback")
				}
			}
			return conn, nil
		},
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    15 * time.Second,
		ResponseHeaderTimeout:  providerTimeout,
		MaxResponseHeaderBytes: 64 << 10,
		MaxIdleConns:           8,
		IdleConnTimeout:        90 * time.Second,
		ForceAttemptHTTP2:      true,
	}
	return &HTTPProvider{provider: provider, apiKey: apiKey, baseURL: u.String(), client: &http.Client{
		Transport:     transport,
		Timeout:       providerTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func canonicalProvider(provider string) (string, error) {
	switch provider {
	case "openai":
		return "openai", nil
	case "anthropic", "claude":
		return "anthropic", nil
	default:
		return "", errors.New("provider must be openai, anthropic, or claude")
	}
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Identity excludes credentials while keeping custom gateways isolated from
// each other and from the provider's public endpoint in result fingerprints.
func (p *HTTPProvider) Identity() string { return p.provider + ":" + p.baseURL }

func (p *HTTPProvider) InputTokenCountBasis() string { return "provider_token_count" }

func (p *HTTPProvider) validate(req Request, generating bool) error {
	if req.Ollama != nil {
		return errors.New("ollama settings require the ollama provider")
	}
	if req.Stream {
		return errors.New("stream measurements are supported only by the local Ollama adapter")
	}
	if req.Provider != "" {
		provider, err := canonicalProvider(req.Provider)
		if err != nil || provider != p.provider {
			return errors.New("request provider does not match the configured provider")
		}
	}
	if req.Model == "" || len(req.Model) > 200 {
		return errors.New("model is required and must be at most 200 bytes")
	}
	for _, c := range req.Model {
		if c < 33 || c > 126 {
			return errors.New("model contains invalid characters")
		}
	}
	if !utf8.ValidString(req.Prefix) || !utf8.ValidString(req.Input) {
		return errors.New("provider input must be valid UTF-8")
	}
	if req.Input == "" {
		return errors.New("provider input is required")
	}
	if len(req.Prefix) > maxProviderRequestBytes || len(req.Input) > maxProviderRequestBytes || len(req.Prefix)+len(req.Input) > maxProviderRequestBytes {
		return errors.New("provider request exceeds the byte limit")
	}
	if generating && (req.MaxOutputTokens < 1 || req.MaxOutputTokens > 1_000_000) {
		return errors.New("max output tokens must be between 1 and 1000000")
	}
	if req.ReasoningEffort != "" {
		if p.provider != "openai" {
			return errors.New("reasoning effort is supported only for OpenAI requests")
		}
		switch req.ReasoningEffort {
		case "none", "low", "medium", "high", "xhigh", "max":
		default:
			return errors.New("unsupported reasoning effort")
		}
	}
	switch req.CacheMode {
	case "", "default":
	case "prefix":
		if req.Prefix == "" {
			return errors.New("prefix caching requires a nonempty stable prefix")
		}
		if p.provider == "openai" && !supportsOpenAIExplicitCache(req.Model) {
			return errors.New("prefix caching requires a documented OpenAI explicit-cache model; use cache mode default for other models")
		}
	default:
		return errors.New("cache mode must be default or prefix")
	}
	return nil
}

// Deliberately conservative: unknown/new aliases must use the API's default
// cache behavior until their explicit-breakpoint support is established.
func supportsOpenAIExplicitCache(model string) bool {
	for _, base := range []string{"gpt-5.6", "gpt-5.6-pro", "gpt-6-sol", "gpt-6-astra", "gpt-6-luna", "gpt-6.1-sol"} {
		if model == base {
			return true
		}
		if strings.HasPrefix(model, base+"-") {
			suffix := strings.TrimPrefix(model, base+"-")
			if _, err := time.Parse("2006-01-02", suffix); err == nil {
				return true
			}
		}
	}
	return false
}

type openAITextBlock struct {
	Type       string                 `json:"type"`
	Text       string                 `json:"text"`
	Breakpoint *openAICacheBreakpoint `json:"prompt_cache_breakpoint,omitempty"`
}
type openAICacheBreakpoint struct {
	Mode string `json:"mode"`
}
type openAIMessage struct {
	Role    string            `json:"role"`
	Content []openAITextBlock `json:"content"`
}
type openAICacheOptions struct {
	Mode string `json:"mode"`
	TTL  string `json:"ttl"`
}
type openAIReasoning struct {
	Effort string `json:"effort"`
}
type openAIRequest struct {
	Model           string              `json:"model"`
	Input           []openAIMessage     `json:"input"`
	MaxOutputTokens int64               `json:"max_output_tokens,omitempty"`
	Store           *bool               `json:"store,omitempty"`
	CacheOptions    *openAICacheOptions `json:"prompt_cache_options,omitempty"`
	Reasoning       *openAIReasoning    `json:"reasoning,omitempty"`
}
type claudeCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl"`
}
type claudeTextBlock struct {
	Type         string              `json:"type"`
	Text         string              `json:"text"`
	CacheControl *claudeCacheControl `json:"cache_control,omitempty"`
}
type claudeMessage struct {
	Role    string            `json:"role"`
	Content []claudeTextBlock `json:"content"`
}
type claudeRequest struct {
	Model     string            `json:"model"`
	System    []claudeTextBlock `json:"system,omitempty"`
	Messages  []claudeMessage   `json:"messages"`
	MaxTokens int64             `json:"max_tokens,omitempty"`
}

func (p *HTTPProvider) payload(req Request, generating bool) any {
	if p.provider == "openai" {
		body := openAIRequest{Model: req.Model}
		if req.ReasoningEffort != "" {
			body.Reasoning = &openAIReasoning{Effort: req.ReasoningEffort}
		}
		if req.Prefix != "" {
			prefix := openAITextBlock{Type: "input_text", Text: req.Prefix}
			if req.CacheMode == "prefix" {
				prefix.Breakpoint = &openAICacheBreakpoint{Mode: "explicit"}
			}
			body.Input = append(body.Input, openAIMessage{Role: "developer", Content: []openAITextBlock{prefix}})
		}
		body.Input = append(body.Input, openAIMessage{Role: "user", Content: []openAITextBlock{{Type: "input_text", Text: req.Input}}})
		if generating {
			body.MaxOutputTokens = req.MaxOutputTokens
			store := false
			body.Store = &store
			if req.CacheMode == "prefix" {
				body.CacheOptions = &openAICacheOptions{Mode: "explicit", TTL: "30m"}
			}
		}
		return body
	}
	body := claudeRequest{Model: req.Model, Messages: []claudeMessage{{Role: "user", Content: []claudeTextBlock{{Type: "text", Text: req.Input}}}}}
	if req.Prefix != "" {
		prefix := claudeTextBlock{Type: "text", Text: req.Prefix}
		if req.CacheMode == "prefix" {
			prefix.CacheControl = &claudeCacheControl{Type: "ephemeral", TTL: "5m"}
		}
		body.System = []claudeTextBlock{prefix}
	}
	if generating {
		body.MaxTokens = req.MaxOutputTokens
	}
	return body
}

// CountInputTokens uses the upstream counting endpoint and sends the same
// text and roles as generation, without unsupported generation-only fields.
func (p *HTTPProvider) CountInputTokens(ctx context.Context, req Request) (int64, error) {
	if err := p.validate(req, false); err != nil {
		return 0, err
	}
	path := "/responses/input_tokens"
	if p.provider == "anthropic" {
		path = "/messages/count_tokens"
	}
	var count struct {
		InputTokens *int64 `json:"input_tokens"`
	}
	if err := p.post(ctx, path, p.payload(req, false), &count); err != nil {
		return 0, err
	}
	if count.InputTokens == nil || *count.InputTokens < 1 || *count.InputTokens > maxProviderTokens {
		return 0, errors.New("provider returned an invalid input token count")
	}
	return *count.InputTokens, nil
}

func (p *HTTPProvider) Generate(ctx context.Context, req Request) (Response, error) {
	if err := p.validate(req, true); err != nil {
		return Response{}, err
	}
	if p.provider == "openai" {
		var raw openAIResponse
		if err := p.post(ctx, "/responses", p.payload(req, true), &raw); err != nil {
			return Response{}, err
		}
		return raw.normalize(req)
	}
	var raw claudeResponse
	if err := p.post(ctx, "/messages", p.payload(req, true), &raw); err != nil {
		return Response{}, err
	}
	return raw.normalize(req)
}

func (p *HTTPProvider) post(ctx context.Context, path string, payload, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.New("cannot encode provider request")
	}
	if len(body) > maxProviderRequestBytes {
		return errors.New("provider request exceeds the byte limit")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return errors.New("cannot create provider request")
	}
	// Do not provide a body replay function to the HTTP transport.
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if p.provider == "openai" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	} else {
		req.Header.Set("x-api-key", p.apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("provider request interrupted: %w", ctx.Err())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("provider request timed out: %w", context.DeadlineExceeded)
		}
		return errors.New("provider request failed")
	}
	defer resp.Body.Close()
	// Never expose upstream bodies, status text, URLs, or request headers in
	// errors: gateways can echo sensitive inputs or credentials there.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s provider returned HTTP %d", p.provider, resp.StatusCode)
	}
	if resp.ContentLength > maxProviderResponseBytes {
		return errors.New("provider response exceeds the byte limit")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderResponseBytes+1))
	if err != nil {
		return errors.New("cannot read provider response")
	}
	if len(data) > maxProviderResponseBytes {
		return errors.New("provider response exceeds the byte limit")
	}
	if err := json.Unmarshal(data, result); err != nil {
		return errors.New("provider returned invalid JSON")
	}
	return nil
}

type openAIResponse struct {
	ID                string          `json:"id"`
	Model             string          `json:"model"`
	Status            string          `json:"status"`
	Error             json.RawMessage `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Output []struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Status  string `json:"status"`
		Content []struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage *struct {
		InputTokens  *int64 `json:"input_tokens"`
		OutputTokens *int64 `json:"output_tokens"`
		InputDetails *struct {
			CachedTokens     *int64 `json:"cached_tokens"`
			CacheWriteTokens *int64 `json:"cache_write_tokens"`
		} `json:"input_tokens_details"`
		OutputDetails *struct {
			ReasoningTokens *int64 `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

func (raw openAIResponse) normalize(req Request) (Response, error) {
	if raw.Usage == nil || raw.Usage.InputTokens == nil || raw.Usage.OutputTokens == nil || raw.Usage.InputDetails == nil || raw.Usage.InputDetails.CachedTokens == nil {
		return Response{}, errors.New("OpenAI response is missing token usage")
	}
	u := Usage{InputTokens: *raw.Usage.InputTokens, CacheReadTokens: *raw.Usage.InputDetails.CachedTokens, OutputTokens: *raw.Usage.OutputTokens}
	if raw.Usage.InputDetails.CacheWriteTokens != nil {
		u.CacheWriteTokens = *raw.Usage.InputDetails.CacheWriteTokens
	} else if supportsOpenAIExplicitCache(req.Model) {
		return Response{}, errors.New("OpenAI response is missing cache-write token usage")
	}
	if raw.Usage.OutputDetails != nil && raw.Usage.OutputDetails.ReasoningTokens != nil {
		u.ReasoningTokens = *raw.Usage.OutputDetails.ReasoningTokens
	}
	if !validTokenCount(u.InputTokens) || !validTokenCount(u.CacheReadTokens) || !validTokenCount(u.CacheWriteTokens) || u.CacheReadTokens+u.CacheWriteTokens > u.InputTokens {
		return Response{}, errors.New("OpenAI response contains invalid input token usage")
	}
	u.UncachedInputTokens = u.InputTokens - u.CacheReadTokens - u.CacheWriteTokens
	if err := validateOutputUsage(u, req); err != nil {
		return Response{}, err
	}
	result := Response{ID: raw.ID, Model: raw.Model, Usage: u, Complete: raw.Status == "completed" && !nonNullJSON(raw.Error) && raw.IncompleteDetails == nil}
	switch raw.Status {
	case "completed", "incomplete", "failed", "cancelled", "queued", "in_progress":
		result.StopReason = raw.Status
	default:
		result.StopReason = "unknown_status"
	}
	if raw.IncompleteDetails != nil {
		switch raw.IncompleteDetails.Reason {
		case "max_output_tokens", "content_filter":
			result.StopReason = raw.IncompleteDetails.Reason
		default:
			result.StopReason = "incomplete"
		}
	}
	var text strings.Builder
	for _, item := range raw.Output {
		switch item.Type {
		case "reasoning": // Reasoning output is already included in output_tokens.
			if item.Status != "" && item.Status != "completed" {
				result.Complete = false
			}
		case "message":
			if item.Role != "assistant" || item.Status != "completed" {
				result.Complete = false
				continue
			}
			for _, block := range item.Content {
				if block.Type == "output_text" && block.Text != nil {
					text.WriteString(*block.Text)
				} else {
					result.Complete = false
					if block.Type == "refusal" {
						result.StopReason = "refusal"
					} else {
						result.StopReason = "unsupported_output"
					}
				}
			}
		default:
			result.Complete = false
			result.StopReason = "unsupported_output"
		}
	}
	result.Text = text.String()
	if result.ID == "" || result.Model == "" || result.Text == "" {
		result.Complete = false
	}
	if u.OutputTokens > req.MaxOutputTokens {
		result.Complete = false
		result.StopReason = "usage_limit_exceeded"
	}
	return result, nil
}

type claudeResponse struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Role        string          `json:"role"`
	Model       string          `json:"model"`
	StopReason  string          `json:"stop_reason"`
	StopDetails json.RawMessage `json:"stop_details"`
	Content     []struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	} `json:"content"`
	Usage *struct {
		InputTokens              *int64 `json:"input_tokens"`
		CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
		OutputTokens             *int64 `json:"output_tokens"`
		OutputDetails            *struct {
			ThinkingTokens *int64 `json:"thinking_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

func (raw claudeResponse) normalize(req Request) (Response, error) {
	if raw.Usage == nil || raw.Usage.InputTokens == nil || raw.Usage.OutputTokens == nil {
		return Response{}, errors.New("Claude response is missing token usage")
	}
	u := Usage{UncachedInputTokens: *raw.Usage.InputTokens, OutputTokens: *raw.Usage.OutputTokens}
	if raw.Usage.CacheCreationInputTokens != nil {
		u.CacheWriteTokens = *raw.Usage.CacheCreationInputTokens
	}
	if raw.Usage.CacheReadInputTokens != nil {
		u.CacheReadTokens = *raw.Usage.CacheReadInputTokens
	}
	if raw.Usage.OutputDetails != nil && raw.Usage.OutputDetails.ThinkingTokens != nil {
		u.ReasoningTokens = *raw.Usage.OutputDetails.ThinkingTokens
	}
	if req.CacheMode == "prefix" && (raw.Usage.CacheCreationInputTokens == nil || raw.Usage.CacheReadInputTokens == nil) {
		return Response{}, errors.New("Claude response is missing cache token usage")
	}
	if !validTokenCount(u.UncachedInputTokens) || !validTokenCount(u.CacheReadTokens) || !validTokenCount(u.CacheWriteTokens) {
		return Response{}, errors.New("Claude response contains invalid input token usage")
	}
	u.InputTokens = u.UncachedInputTokens + u.CacheReadTokens + u.CacheWriteTokens
	if !validTokenCount(u.InputTokens) {
		return Response{}, errors.New("Claude response contains invalid total input token usage")
	}
	if err := validateOutputUsage(u, req); err != nil {
		return Response{}, err
	}
	result := Response{ID: raw.ID, Model: raw.Model, Usage: u, Complete: raw.Type == "message" && raw.Role == "assistant" && raw.StopReason == "end_turn" && !nonNullJSON(raw.StopDetails)}
	switch raw.StopReason {
	case "end_turn", "max_tokens", "stop_sequence", "tool_use", "pause_turn", "refusal", "model_context_window_exceeded":
		result.StopReason = raw.StopReason
	default:
		result.StopReason = "unknown_status"
	}
	if nonNullJSON(raw.StopDetails) {
		result.StopReason = "stop_details"
		var details struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw.StopDetails, &details) == nil && details.Type == "refusal" {
			result.StopReason = "refusal"
		}
	}
	var text strings.Builder
	for _, block := range raw.Content {
		switch block.Type {
		case "text":
			if block.Text == nil {
				result.Complete = false
			} else {
				text.WriteString(*block.Text)
			}
		case "thinking", "redacted_thinking": // Do not expose or separately charge reasoning.
		default:
			result.Complete = false
			result.StopReason = "unsupported_output"
		}
	}
	result.Text = text.String()
	if result.ID == "" || result.Model == "" || result.Text == "" {
		result.Complete = false
	}
	if u.OutputTokens > req.MaxOutputTokens {
		result.Complete = false
		result.StopReason = "usage_limit_exceeded"
	}
	return result, nil
}

func nonNullJSON(raw json.RawMessage) bool {
	return len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
func validTokenCount(n int64) bool { return n >= 0 && n <= maxProviderTokens }
func validateOutputUsage(u Usage, req Request) error {
	// Numeric validity differs from a policy violation. Keep a known overrun
	// available so the Runner can persist its usage and reconcile the ledger.
	if u.InputTokens < 1 || !validTokenCount(u.OutputTokens) || !validTokenCount(u.ReasoningTokens) || u.ReasoningTokens > u.OutputTokens {
		return errors.New("provider response contains invalid output token usage")
	}
	return nil
}
