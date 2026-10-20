package inference

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// OllamaProvider runs installed model weights through a literal loopback HTTP
// endpoint. It does not sign in, pull models, send keys, or use cloud models.
// A provider captures one installed model snapshot; recreate it to refresh a
// mutable model alias after its weights or template change.
type OllamaProvider struct {
	baseURL string
	model   string
	digest  string
	client  *http.Client
}

type ollamaMetadata struct {
	model         string
	digest        string
	template      string
	system        string
	contextLength int64
}

const (
	maxOllamaStreamFrameBytes = 1 << 20
	maxOllamaStreamFrames     = 131072
)

type ollamaGenerationFrame struct {
	Model                 string          `json:"model"`
	CreatedAt             string          `json:"created_at"`
	Response              string          `json:"response"`
	Thinking              string          `json:"thinking"`
	Done                  *bool           `json:"done"`
	DoneReason            string          `json:"done_reason"`
	PromptEvalCount       *int64          `json:"prompt_eval_count"`
	PromptEvalCachedCount *int64          `json:"prompt_eval_cached_count"`
	EvalCount             *int64          `json:"eval_count"`
	ToolCalls             json.RawMessage `json:"tool_calls"`
	Error                 json.RawMessage `json:"error"`
}

// NewOllamaProvider checks that model is already installed and advertises
// local text completion. baseURL defaults to http://127.0.0.1:11434; literal
// loopback HTTP is required so a local-mode configuration cannot use a hosted
// endpoint or depend on DNS resolution.
func NewOllamaProvider(baseURL, model string) (*OllamaProvider, error) {
	return NewOllamaProviderContext(context.Background(), baseURL, model)
}

// NewOllamaProviderContext includes installed-model discovery in the caller's
// cancellation and deadline. Discovery also has a 15-second maximum of its own.
func NewOllamaProviderContext(ctx context.Context, baseURL, model string) (*OllamaProvider, error) {
	if baseURL == "" {
		baseURL = "http://127.0.0.1:11434"
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("Ollama URL must be a literal loopback HTTP origin without credentials, path, query, or fragment")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("Ollama URL requires a literal loopback IP address")
	}
	if !validOllamaModel(model) {
		return nil, errors.New("Ollama requires a valid installed model name without cloud markers")
	}
	u.Path = ""
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		ResponseHeaderTimeout:  providerTimeout,
		MaxResponseHeaderBytes: 64 << 10,
		MaxIdleConns:           4,
		IdleConnTimeout:        90 * time.Second,
	}
	p := &OllamaProvider{baseURL: u.String(), model: canonicalOllamaModel(model), client: &http.Client{
		Transport: transport, Timeout: providerTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	metadata, err := p.inspect(ctx)
	if err != nil {
		p.client.CloseIdleConnections()
		return nil, err
	}
	p.model, p.digest = metadata.model, metadata.digest
	return p, nil
}

func validOllamaModel(model string) bool {
	if len(model) == 0 || len(model) > 128 || strings.Contains(strings.ToLower(model), "cloud") {
		return false
	}
	for _, c := range model {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_-.:/", c)) {
			return false
		}
	}
	for _, segment := range strings.Split(model, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func canonicalOllamaModel(model string) string {
	if !strings.Contains(model[strings.LastIndex(model, "/")+1:], ":") {
		return model + ":latest"
	}
	return model
}

func (p *OllamaProvider) Identity() string {
	return "ollama:" + p.baseURL + ":" + p.model + "@" + p.digest
}

// ModelDigest exposes the installed weights identity without endpoint, prompt,
// template or system content. It performs no inference or metadata request.
func (p *OllamaProvider) ModelDigest() string { return p.digest }

func (p *OllamaProvider) inspect(ctx context.Context) (ollamaMetadata, error) {
	var tags map[string]any
	if err := p.request(ctx, http.MethodGet, "/api/tags", nil, &tags); err != nil {
		return ollamaMetadata{}, err
	}
	models, ok := tags["models"].([]any)
	if !ok {
		return ollamaMetadata{}, errors.New("Ollama did not return an installed model list")
	}
	var metadata ollamaMetadata
	for _, entry := range models {
		item, ok := entry.(map[string]any)
		if !ok {
			return metadata, errors.New("Ollama returned invalid installed model metadata")
		}
		name, _ := item["name"].(string)
		if name == "" {
			name, _ = item["model"].(string)
		}
		if canonicalOllamaModel(name) != p.model {
			continue
		}
		if metadata.model != "" {
			return metadata, errors.New("Ollama returned ambiguous installed model metadata")
		}
		if !validOllamaModel(name) || hasRemoteOllamaMetadata(item) {
			return metadata, errors.New("Ollama local mode refuses cloud or remote model metadata")
		}
		digest, _ := item["digest"].(string)
		digest = strings.TrimPrefix(digest, "sha256:")
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size {
			return metadata, errors.New("Ollama installed model is missing a valid digest")
		}
		metadata.model, metadata.digest = canonicalOllamaModel(name), strings.ToLower(digest)
	}
	if metadata.model == "" {
		return metadata, errors.New("Ollama model is not installed; install local weights before running Questlock")
	}
	if p.digest != "" && p.digest != metadata.digest {
		return metadata, errors.New("Ollama model digest changed; recreate the provider to use the new model snapshot")
	}
	var show map[string]any
	if err := p.request(ctx, http.MethodPost, "/api/show", struct {
		Model string `json:"model"`
	}{metadata.model}, &show); err != nil {
		return metadata, err
	}
	if hasRemoteOllamaMetadata(show) {
		return metadata, errors.New("Ollama local mode refuses cloud or remote model metadata")
	}
	capabilities, ok := show["capabilities"].([]any)
	completion := false
	if ok {
		for _, capability := range capabilities {
			if capability == "completion" {
				completion = true
			}
		}
	}
	if !completion {
		return metadata, errors.New("Ollama model does not advertise text completion")
	}
	if thinking, ok := show["thinking"].(map[string]any); ok {
		values, ok := thinking["values"].([]any)
		canDisable := false
		if ok {
			for _, value := range values {
				if disabled, ok := value.(bool); ok && !disabled {
					canDisable = true
				}
			}
		}
		if !canDisable {
			return metadata, errors.New("Ollama model does not advertise support for disabling thinking")
		}
	}
	metadata.template, _ = show["template"].(string)
	metadata.system, _ = show["system"].(string)
	if !utf8.ValidString(metadata.template+metadata.system) || len(metadata.template)+len(metadata.system) > maxProviderRequestBytes {
		return metadata, errors.New("Ollama model prompt metadata exceeds supported limits")
	}
	info, ok := show["model_info"].(map[string]any)
	if !ok {
		return metadata, errors.New("Ollama model does not report context capacity")
	}
	architecture, _ := info["general.architecture"].(string)
	if architecture != "" {
		capacity, ok := info[architecture+".context_length"].(float64)
		if ok && capacity >= 1 && capacity <= float64(maxProviderTokens) && capacity == float64(int64(capacity)) {
			metadata.contextLength = int64(capacity)
		}
	} else {
		for key, value := range info {
			if !strings.HasSuffix(key, ".context_length") {
				continue
			}
			capacity, ok := value.(float64)
			if !ok || capacity < 1 || capacity > float64(maxProviderTokens) || capacity != float64(int64(capacity)) {
				return metadata, errors.New("Ollama model reports invalid context capacity")
			}
			if metadata.contextLength == 0 || int64(capacity) < metadata.contextLength {
				metadata.contextLength = int64(capacity)
			}
		}
	}
	if metadata.contextLength < 1 {
		return metadata, errors.New("Ollama model does not report valid context capacity")
	}
	return metadata, nil
}

func hasRemoteOllamaMetadata(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if key == "remote_host" || key == "remote_model" {
				if item != nil && item != "" {
					return true
				}
			}
			if hasRemoteOllamaMetadata(item) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if hasRemoteOllamaMetadata(item) {
				return true
			}
		}
	}
	return false
}

func (p *OllamaProvider) validate(req Request) error {
	if req.Provider != "" && req.Provider != "ollama" {
		return errors.New("request provider does not match Ollama")
	}
	if err := validateOllamaOptions(req.Ollama); err != nil {
		return err
	}
	if !validOllamaModel(req.Model) || canonicalOllamaModel(req.Model) != p.model {
		return errors.New("request model does not match the installed Ollama snapshot")
	}
	if req.ReasoningEffort != "" {
		return errors.New("Ollama local mode uses think:false and does not accept reasoning_effort")
	}
	if req.CacheMode != "" && req.CacheMode != "default" {
		return errors.New("Ollama uses automatic local caching; cache mode must be default")
	}
	if req.Input == "" || !utf8.ValidString(req.Prefix+req.Input) {
		return errors.New("Ollama input must be nonempty UTF-8 text")
	}
	if len(req.Prefix) > maxProviderRequestBytes || len(req.Input) > maxProviderRequestBytes || len(req.Prefix)+len(req.Input) > maxProviderRequestBytes {
		return errors.New("Ollama request exceeds the byte limit")
	}
	if req.MaxOutputTokens < 1 || req.MaxOutputTokens > 1_000_000 {
		return errors.New("Ollama max output tokens must be between 1 and 1000000")
	}
	if req.ContextWindow < 1 || req.ContextWindow > 65_536 {
		return errors.New("Ollama requires a context window between 1 and 65536 to bound local memory use")
	}
	return nil
}

// CountInputTokens is a conservative local policy ESTIMATE, not an Ollama
// tokenizer API or a tokenizer-specific proof. It allocates one token per byte
// of requested text and model template/system, plus 512 tokens of overhead.
// Metadata reads do not run inference; reported usage replaces the estimate
// after generation. Context and generation caps still govern local resources.
func (p *OllamaProvider) CountInputTokens(ctx context.Context, req Request) (int64, error) {
	if err := p.validate(req); err != nil {
		return 0, err
	}
	metadata, err := p.inspect(ctx)
	if err != nil {
		return 0, err
	}
	return ollamaInputEstimate(req, metadata)
}

func ollamaInputEstimate(req Request, metadata ollamaMetadata) (int64, error) {
	estimate := int64(len(req.Prefix)+len(req.Input)+len(metadata.template)+len(metadata.system)) + 512
	if req.ContextWindow > metadata.contextLength {
		return 0, errors.New("requested Ollama context window exceeds the model's reported capacity")
	}
	if req.MaxOutputTokens > req.ContextWindow || estimate > req.ContextWindow-req.MaxOutputTokens {
		return 0, errors.New("estimated Ollama prompt and output allowance exceed the context window")
	}
	return estimate, nil
}

func (p *OllamaProvider) Generate(ctx context.Context, req Request) (Response, error) {
	started := time.Now()
	if err := p.validate(req); err != nil {
		return Response{}, err
	}
	metadata, err := p.inspect(ctx)
	if err != nil {
		return Response{}, err
	}
	if _, err = ollamaInputEstimate(req, metadata); err != nil {
		return Response{}, err
	}
	body := struct {
		Model     string `json:"model"`
		System    string `json:"system"`
		Prompt    string `json:"prompt"`
		Stream    bool   `json:"stream"`
		Think     bool   `json:"think"`
		Format    string `json:"format,omitempty"`
		KeepAlive *int64 `json:"keep_alive,omitempty"`
		Options   struct {
			NumPredict  int64    `json:"num_predict"`
			NumCtx      int64    `json:"num_ctx"`
			Temperature *float64 `json:"temperature,omitempty"`
		} `json:"options"`
	}{Model: p.model, System: req.Prefix, Prompt: req.Input, Stream: req.Stream}
	body.Options.NumPredict, body.Options.NumCtx = req.MaxOutputTokens, req.ContextWindow
	if req.Ollama != nil {
		body.Format, body.Options.Temperature = req.Ollama.Format, req.Ollama.Temperature
		body.KeepAlive = req.Ollama.KeepAliveSeconds
	}
	var raw ollamaGenerationFrame
	var timing *GenerationTiming
	if req.Stream {
		raw, timing, err = p.requestStream(ctx, body, started)
	} else {
		err = p.request(ctx, http.MethodPost, "/api/generate", body, &raw)
	}
	// Timing can survive an uncertain stream. Unvalidated content and usage
	// cannot: the Runner retains the claim rather than treating a partial answer
	// as a complete response or retrying generation.
	failure := func(err error) (Response, error) {
		if timing != nil {
			timing.OutputMS = nil
		}
		return Response{Timing: timing}, err
	}
	if err != nil {
		return failure(err)
	}
	if raw.PromptEvalCount == nil || raw.EvalCount == nil {
		return failure(errors.New("Ollama response is missing token usage"))
	}
	u := Usage{InputTokens: *raw.PromptEvalCount, OutputTokens: *raw.EvalCount}
	if raw.PromptEvalCachedCount != nil {
		u.CacheReadTokens = *raw.PromptEvalCachedCount
	}
	if u.InputTokens < 1 || !validTokenCount(u.InputTokens) || !validTokenCount(u.CacheReadTokens) || u.CacheReadTokens > u.InputTokens || !validTokenCount(u.OutputTokens) {
		return failure(errors.New("Ollama response contains invalid token usage"))
	}
	u.UncachedInputTokens = u.InputTokens - u.CacheReadTokens
	result := Response{Model: raw.Model, Text: raw.Response, Usage: u, Timing: timing, Complete: raw.Done != nil && *raw.Done && raw.DoneReason == "stop" && raw.Response != "" && canonicalOllamaModel(raw.Model) == p.model && !nonNullJSON(raw.Error) && !nonNullJSON(raw.ToolCalls) && raw.Thinking == ""}
	switch raw.DoneReason {
	case "stop", "length", "load":
		result.StopReason = raw.DoneReason
	default:
		result.StopReason = "unknown_status"
	}
	if raw.Done == nil || !*raw.Done {
		result.Complete = false
		result.StopReason = "incomplete"
	}
	if raw.Thinking != "" {
		result.StopReason = "unexpected_thinking"
	}
	if nonNullJSON(raw.ToolCalls) {
		result.StopReason = "unsupported_output"
	}
	id := sha256.Sum256([]byte(raw.CreatedAt + "\x00" + raw.Model + "\x00" + raw.Response))
	result.ID = "ollama-" + hex.EncodeToString(id[:12])
	if u.OutputTokens > req.MaxOutputTokens || u.InputTokens+u.OutputTokens > req.ContextWindow {
		result.Complete = false
		result.StopReason = "usage_limit_exceeded"
	}
	// Catch an alias changed during inference before storing its output under
	// the constructor's model digest. Rechecking metadata performs no generation.
	if _, err = p.inspect(ctx); err != nil {
		return failure(err)
	}
	return result, nil
}

// requestStream consumes one bounded NDJSON response without exposing chunks
// as approved output. FirstTokenMS is time from Generate entry to the first
// nonempty content chunk observed by this client, including metadata/network
// overhead. OutputMS is the first-to-last content-chunk interval. Neither is
// server-only compute time, token throughput, or time to the first reasoning
// token. Empty metadata/terminal chunks do not start or extend that interval.
func (p *OllamaProvider) requestStream(ctx context.Context, payload any, started time.Time) (ollamaGenerationFrame, *GenerationTiming, error) {
	timing := &GenerationTiming{Streaming: true, Basis: "client_observed_content_chunks"}
	failure := func(err error) (ollamaGenerationFrame, *GenerationTiming, error) {
		return ollamaGenerationFrame{}, timing, err
	}
	body, err := json.Marshal(payload)
	if err != nil || len(body) > maxProviderRequestBytes {
		return failure(errors.New("cannot encode bounded Ollama stream request"))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return failure(errors.New("cannot create Ollama stream request"))
	}
	req.GetBody = nil
	req.Header.Set("Accept", "application/x-ndjson")
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return failure(ollamaStreamError(ctx, err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return failure(fmt.Errorf("Ollama returned HTTP %d", resp.StatusCode))
	}
	contentType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || contentType != "application/x-ndjson" {
		return failure(errors.New("Ollama stream requires application/x-ndjson"))
	}
	if resp.ContentLength > maxProviderResponseBytes {
		return failure(errors.New("Ollama stream exceeds the byte limit"))
	}
	limited := &io.LimitedReader{R: resp.Body, N: maxProviderResponseBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), maxOllamaStreamFrameBytes)
	var output strings.Builder
	var final ollamaGenerationFrame
	var first, last time.Time
	frames := 0
	for scanner.Scan() {
		frames++
		if limited.N < 1 || frames > maxOllamaStreamFrames {
			return failure(errors.New("Ollama stream exceeds the byte or frame limit"))
		}
		data := scanner.Bytes()
		if len(bytes.TrimSpace(data)) == 0 || !utf8.Valid(data) {
			return failure(errors.New("Ollama stream contains an invalid frame"))
		}
		if final.Done != nil {
			return failure(errors.New("Ollama stream contains data after its final frame"))
		}
		var frame ollamaGenerationFrame
		if json.Unmarshal(data, &frame) != nil || frame.Done == nil {
			return failure(errors.New("Ollama stream contains an invalid frame"))
		}
		if nonNullJSON(frame.Error) {
			return failure(errors.New("Ollama stream reported a generation error"))
		}
		if !validOllamaModel(frame.Model) || canonicalOllamaModel(frame.Model) != p.model {
			return failure(errors.New("Ollama stream model does not match the installed snapshot"))
		}
		if frame.Thinking != "" || nonNullJSON(frame.ToolCalls) {
			return failure(errors.New("Ollama stream returned unsupported output"))
		}
		if frame.Response != "" {
			observed := time.Now()
			if first.IsZero() {
				first = observed
				ms := float64(observed.Sub(started)) / float64(time.Millisecond)
				timing.FirstTokenMS = &ms
			}
			last = observed
			if output.Len()+len(frame.Response) > maxProviderResponseBytes {
				return failure(errors.New("Ollama stream output exceeds the byte limit"))
			}
			output.WriteString(frame.Response)
		}
		if *frame.Done {
			final = frame
		}
	}
	if err := scanner.Err(); err != nil {
		return failure(ollamaStreamError(ctx, err))
	}
	if limited.N < 1 {
		return failure(errors.New("Ollama stream exceeds the byte limit"))
	}
	if final.Done == nil {
		return failure(errors.New("Ollama stream ended without a final frame"))
	}
	final.Response = output.String()
	if !first.IsZero() {
		ms := float64(last.Sub(first)) / float64(time.Millisecond)
		timing.OutputMS = &ms
	}
	return final, timing, nil
}

func ollamaStreamError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("Ollama stream interrupted: %w", ctx.Err())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("Ollama stream timed out: %w", context.DeadlineExceeded)
	}
	return errors.New("Ollama stream could not be read or exceeded its frame limit")
}

func (p *OllamaProvider) request(ctx context.Context, method, path string, payload, result any) error {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return errors.New("cannot encode Ollama request")
		}
	}
	if len(body) > maxProviderRequestBytes {
		return errors.New("Ollama request exceeds the byte limit")
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return errors.New("cannot create Ollama request")
	}
	req.GetBody = nil
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("Ollama request interrupted: %w", ctx.Err())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("Ollama request timed out: %w", context.DeadlineExceeded)
		}
		return errors.New("Ollama request failed; verify the local runtime is running")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Ollama returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxProviderResponseBytes {
		return errors.New("Ollama response exceeds the byte limit")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderResponseBytes+1))
	if err != nil {
		return errors.New("cannot read Ollama response")
	}
	if len(data) > maxProviderResponseBytes {
		return errors.New("Ollama response exceeds the byte limit")
	}
	if json.Unmarshal(data, result) != nil {
		return errors.New("Ollama returned invalid JSON")
	}
	return nil
}
