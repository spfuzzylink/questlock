package inference

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const SGLangRuntimeVersion = "0.5.20"

// SGLangConfig identifies an explicitly operated, immutable serving deployment.
// DeploymentID is the operator's model/configuration revision, not a weight
// digest proved by the HTTP API. Change it whenever that deployment changes.
type SGLangConfig struct {
	DeploymentID   string `json:"deployment_id"`
	CacheNamespace string `json:"cache_namespace"`
	Profile        string `json:"profile"`
	SelfHosted     bool   `json:"self_hosted"`
	MaxInputTokens int64  `json:"max_input_tokens"`
}

func ValidateSGLangConfig(cfg SGLangConfig) error {
	if !cfg.SelfHosted {
		return errors.New("SGLang requires an explicit self_hosted declaration")
	}
	if !validID(cfg.DeploymentID, 128) || !validID(cfg.CacheNamespace, 128) {
		return errors.New("SGLang requires deployment_id and cache_namespace identifiers of at most 128 bytes")
	}
	if cfg.Profile != "dense" && cfg.Profile != "hicache" && cfg.Profile != "hisparse" {
		return errors.New("SGLang profile must be dense, hicache, or hisparse")
	}
	if cfg.MaxInputTokens < 1 || cfg.MaxInputTokens > 1_000_000 {
		return errors.New("SGLang max_input_tokens must be between 1 and 1000000")
	}
	return nil
}

// SGLangCapabilities contains allowlisted readback facts, without endpoints,
// prompts, model paths, launch commands, or credentials. Observed configuration
// is not proof that a particular offload kernel ran or improved performance.
type SGLangCapabilities struct {
	Profile               string `json:"profile"`
	RuntimeVersion        string `json:"runtime_version"`
	GenerationSupported   bool   `json:"generation_supported"`
	RuntimeIdentitySHA256 string `json:"runtime_identity_sha256"`
	Observed              bool   `json:"observed"`
	Reason                string `json:"reason,omitempty"`
}

// SGLangProvider implements the pinned native single-turn, text-only contract.
// SGLang owns its KV tensors and storage. Questlock performs no administration,
// model download, runtime reconfiguration, retry, or PD bootstrap orchestration.
type SGLangProvider struct {
	baseURL, apiKey, model string
	cfg                    SGLangConfig
	client                 *http.Client
	runtimeIdentity        string
	identity               string
	cacheSalt              string
	capabilities           SGLangCapabilities
	contextLength          int64
	weightVersion          string
	mu                     sync.Mutex
	lastRequest            [32]byte
	lastTokens             []int64
}

// NewSGLangProviderContext observes an already-running SGLang v0.5.20 server.
// Literal loopback HTTP permits no key; remote HTTPS requires an explicit key.
// Constructor readbacks run no generation and include no admin writes.
func NewSGLangProviderContext(ctx context.Context, apiKey, baseURL, model string, cfg SGLangConfig) (*SGLangProvider, error) {
	if err := ValidateSGLangConfig(cfg); err != nil {
		return nil, err
	}
	if !validID(model, 128) {
		return nil, errors.New("SGLang requires a served model identifier of at most 128 bytes")
	}
	if baseURL == "" {
		baseURL = "http://127.0.0.1:30000"
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, errors.New("invalid SGLang endpoint")
	}
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return nil, errors.New("SGLang plain HTTP requires a literal loopback IP address")
		}
	} else if u.Scheme == "https" && apiKey == "" {
		return nil, errors.New("remote SGLang HTTPS requires an explicit API key")
	}
	// Share the hardened, no-proxy/no-redirect transport. The placeholder only
	// satisfies its constructor; SGLang adds Authorization only for a real key.
	transportKey := apiKey
	if transportKey == "" {
		transportKey = "questlock-local-sglang"
	}
	hp, err := NewHTTPProvider("openai", transportKey, baseURL)
	if err != nil {
		return nil, err
	}
	p := &SGLangProvider{baseURL: hp.baseURL, apiKey: apiKey, model: model, cfg: cfg, client: hp.client}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	observation, err := p.inspect(ctx)
	if err != nil {
		p.client.CloseIdleConnections()
		return nil, err
	}
	p.runtimeIdentity, p.contextLength, p.weightVersion = observation.digest, observation.contextLength, observation.weightVersion
	p.capabilities = SGLangCapabilities{Profile: cfg.Profile, RuntimeVersion: SGLangRuntimeVersion, RuntimeIdentitySHA256: observation.digest, Observed: true, GenerationSupported: observation.mode == "null" && cfg.Profile != "hisparse"}
	if cfg.Profile == "hisparse" {
		p.capabilities.Reason = "HiSparse requires a validated PD ingress; direct decode generation is unsupported"
	} else if !p.capabilities.GenerationSupported {
		p.capabilities.Reason = "SGLang PD instances require a validated ingress; direct PD generation is unsupported"
	}
	encoded, _ := json.Marshal(struct {
		Endpoint, Model, Runtime string
		Config                   SGLangConfig
	}{p.baseURL, p.model, p.runtimeIdentity, cfg})
	digest := sha256.Sum256(encoded)
	p.identity = "sglang:" + hex.EncodeToString(digest[:])
	encoded, _ = json.Marshal(struct{ Namespace, Deployment string }{cfg.CacheNamespace, cfg.DeploymentID})
	digest = sha256.Sum256(encoded)
	p.cacheSalt = hex.EncodeToString(digest[:])
	return p, nil
}

func (p *SGLangProvider) Identity() string { return p.identity }

func (p *SGLangProvider) InputTokenCountBasis() string { return "provider_token_count" }

func (p *SGLangProvider) Capabilities() SGLangCapabilities { return p.capabilities }

// RefreshIdentity is used before a Runner result lookup as well as around
// tokenization/generation. An existing provider never silently adopts a new
// model or restart; construct a fresh provider for that new deployment.
func (p *SGLangProvider) RefreshIdentity(ctx context.Context) error {
	_, err := p.inspect(ctx)
	return err
}

// These fields affect identity, but only their digest leaves the provider.
// Do not hash whole server_info: it may contain API keys and launch commands.
var sglangIdentityFields = []string{
	"version", "startup_time", "model_path", "tokenizer_path", "served_model_name", "revision", "tokenizer_revision",
	"dtype", "quantization", "kv_cache_dtype", "context_length", "load_format", "json_model_override_args",
	"chat_template", "hf_chat_template_name", "completion_template", "preferred_sampling_params", "reasoning_parser", "tool_call_parser",
	"attention_backend", "dsa_prefill_backend", "dsa_decode_backend", "nsa_prefill_backend", "nsa_decode_backend",
	"enable_hierarchical_cache", "enable_hisparse", "disable_radix_cache", "disaggregation_mode", "hisparse_config",
	"hicache_host_memory_mode", "hicache_ratio", "hicache_size", "hicache_write_policy", "hicache_io_backend", "hicache_mem_layout",
	"hicache_storage_backend", "hicache_storage_prefetch_policy", "enable_unified_cache_external_linker", "page_size",
	"tp_size", "dp_size", "pp_size", "dcp_size", "enable_dp_attention", "speculative_algorithm",
}

type sglangObservation struct {
	digest, weightVersion, mode string
	contextLength               int64
}

func (p *SGLangProvider) inspect(ctx context.Context) (sglangObservation, error) {
	var model, server map[string]json.RawMessage
	if err := p.request(ctx, http.MethodGet, "/model_info", nil, &model); err != nil {
		return sglangObservation{}, err
	}
	if err := p.request(ctx, http.MethodGet, "/server_info", nil, &server); err != nil {
		return sglangObservation{}, err
	}
	readString := func(m map[string]json.RawMessage, key string) (string, bool) {
		var v string
		if json.Unmarshal(m[key], &v) != nil || v == "" || len(v) > 4096 || !utf8.ValidString(v) {
			return "", false
		}
		return v, true
	}
	version, ok := readString(server, "version")
	if !ok || version != SGLangRuntimeVersion {
		return sglangObservation{}, errors.New("SGLang adapter requires runtime version 0.5.20")
	}
	served, ok := readString(model, "served_model_name")
	if !ok || served != p.model {
		return sglangObservation{}, errors.New("SGLang served model does not match the requested model")
	}
	if _, ok := readString(model, "model_path"); !ok {
		return sglangObservation{}, errors.New("SGLang model readback is missing model identity")
	}
	weightVersion, ok := readString(model, "weight_version")
	if !ok {
		return sglangObservation{}, errors.New("SGLang model readback is missing weight version")
	}
	var generation *bool
	if json.Unmarshal(model["is_generation"], &generation) != nil || generation == nil || !*generation {
		return sglangObservation{}, errors.New("SGLang endpoint does not advertise text generation")
	}
	var startup string
	if json.Unmarshal(server["startup_time"], &startup) != nil || strings.TrimSpace(startup) == "" {
		var n json.Number
		if json.Unmarshal(server["startup_time"], &n) != nil {
			return sglangObservation{}, errors.New("SGLang server readback is missing startup identity")
		}
		value, err := strconv.ParseFloat(n.String(), 64)
		if err != nil || value <= 0 {
			return sglangObservation{}, errors.New("SGLang server readback has invalid startup identity")
		}
	} else if len(startup) > 256 {
		return sglangObservation{}, errors.New("SGLang server readback has invalid startup identity")
	}
	readBool := func(key string) (bool, error) {
		var value *bool
		if json.Unmarshal(server[key], &value) != nil || value == nil {
			return false, errors.New("SGLang server readback is missing cache configuration")
		}
		return *value, nil
	}
	hierarchical, err := readBool("enable_hierarchical_cache")
	if err != nil {
		return sglangObservation{}, err
	}
	sparse, err := readBool("enable_hisparse")
	if err != nil {
		return sglangObservation{}, err
	}
	disabled, err := readBool("disable_radix_cache")
	if err != nil {
		return sglangObservation{}, err
	}
	mode, ok := readString(server, "disaggregation_mode")
	if !ok || (mode != "null" && mode != "prefill" && mode != "decode") {
		return sglangObservation{}, errors.New("SGLang server readback has invalid disaggregation mode")
	}
	if hierarchical && (disabled || sparse) {
		return sglangObservation{}, errors.New("SGLang cache configuration contains incompatible runtime profiles")
	}
	switch p.cfg.Profile {
	case "dense":
		if hierarchical || sparse {
			return sglangObservation{}, errors.New("SGLang runtime does not match the declared dense profile")
		}
	case "hicache":
		if !hierarchical || sparse || disabled {
			return sglangObservation{}, errors.New("SGLang runtime does not match the declared HiCache profile")
		}
	case "hisparse":
		if !sparse || hierarchical || !disabled || mode != "decode" {
			return sglangObservation{}, errors.New("SGLang runtime does not match the declared HiSparse decode profile")
		}
	}
	var contextLength int64
	if raw := server["context_length"]; nonNullJSON(raw) {
		if json.Unmarshal(raw, &contextLength) != nil || contextLength < 1 || !validTokenCount(contextLength) {
			return sglangObservation{}, errors.New("SGLang server readback has invalid context length")
		}
	}
	// Model metadata intentionally excludes unrecognized fields as well. The
	// operator deployment revision must cover weight/template content not proved
	// by these readbacks. Mutable runtime administration is unsupported.
	snapshot := struct {
		Model  map[string]json.RawMessage
		Server map[string]json.RawMessage
	}{map[string]json.RawMessage{}, map[string]json.RawMessage{}}
	for _, field := range append(sglangIdentityFields[:len(sglangIdentityFields):len(sglangIdentityFields)], "is_generation", "weight_version", "model_type", "architectures") {
		if value, exists := model[field]; exists {
			snapshot.Model[field] = value
		}
	}
	for _, field := range sglangIdentityFields {
		if value, exists := server[field]; exists {
			snapshot.Server[field] = value
		}
	}
	encoded, _ := json.Marshal(snapshot)
	digest := sha256.Sum256(encoded)
	identity := hex.EncodeToString(digest[:])
	if p.runtimeIdentity != "" && identity != p.runtimeIdentity {
		return sglangObservation{}, errors.New("SGLang runtime identity changed; recreate the provider for the new deployment")
	}
	return sglangObservation{digest: identity, weightVersion: weightVersion, mode: mode, contextLength: contextLength}, nil
}

func (p *SGLangProvider) validate(req Request) error {
	if req.Ollama != nil {
		return errors.New("ollama settings require the ollama provider")
	}
	if req.Provider != "sglang" || req.Model != p.model || req.SGLang == nil || *req.SGLang != p.cfg {
		return errors.New("SGLang request does not match the configured deployment")
	}
	if req.Stream || req.ReasoningEffort != "" || (req.CacheMode != "" && req.CacheMode != "default") {
		return errors.New("SGLang adapter supports nonstream text with default cache mode and no reasoning-effort override")
	}
	if !utf8.ValidString(req.Prefix) || !utf8.ValidString(req.Input) || strings.TrimSpace(req.Input) == "" || len(req.Prefix)+len(req.Input) > maxProviderRequestBytes {
		return errors.New("SGLang request requires bounded UTF-8 text")
	}
	if req.MaxOutputTokens < 1 || req.MaxOutputTokens > 1_000_000 {
		return errors.New("SGLang output token allowance must be between 1 and 1000000")
	}
	if !p.capabilities.GenerationSupported {
		return errors.New(p.capabilities.Reason)
	}
	return nil
}

type sglangTokenization struct {
	Tokens      []int64 `json:"tokens"`
	Count       *int64  `json:"count"`
	MaxModelLen *int64  `json:"max_model_len"`
}

func (p *SGLangProvider) tokenize(ctx context.Context, req Request) (sglangTokenization, error) {
	var raw sglangTokenization
	payload := struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}{Model: p.model}
	if req.Prefix != "" {
		payload.Messages = append(payload.Messages, struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{"system", req.Prefix})
	}
	payload.Messages = append(payload.Messages, struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{"user", req.Input})
	if err := p.request(ctx, http.MethodPost, "/tokenize", payload, &raw); err != nil {
		return raw, err
	}
	if raw.Count == nil || *raw.Count < 1 || *raw.Count > p.cfg.MaxInputTokens || int64(len(raw.Tokens)) != *raw.Count || raw.MaxModelLen == nil || *raw.MaxModelLen < 1 || !validTokenCount(*raw.MaxModelLen) {
		return raw, errors.New("SGLang returned invalid or excessive tokenization")
	}
	for _, token := range raw.Tokens {
		if token < 0 || token > 1<<31-1 {
			return raw, errors.New("SGLang returned an invalid input token ID")
		}
	}
	limit := *raw.MaxModelLen
	if p.contextLength > 0 && p.contextLength < limit {
		limit = p.contextLength
	}
	if req.MaxOutputTokens > limit || *raw.Count > limit-req.MaxOutputTokens {
		diagnostic := inputDiagnostic("model_context_tokens", limit, *raw.Count, "provider_token_count", req.MaxOutputTokens)
		diagnostic.ContextWindow, diagnostic.Required = limit, *raw.Count+req.MaxOutputTokens
		return raw, &BudgetError{Diagnostic: diagnostic}
	}
	return raw, nil
}

func sglangRequestDigest(req Request) [32]byte {
	encoded, _ := json.Marshal(req)
	return sha256.Sum256(encoded)
}

func (p *SGLangProvider) CountInputTokens(ctx context.Context, req Request) (int64, error) {
	if err := p.validate(req); err != nil {
		return 0, err
	}
	if _, err := p.inspect(ctx); err != nil {
		return 0, err
	}
	raw, err := p.tokenize(ctx, req)
	if err != nil {
		return 0, err
	}
	if _, err := p.inspect(ctx); err != nil {
		return 0, err
	}
	p.mu.Lock()
	p.lastRequest, p.lastTokens = sglangRequestDigest(req), append([]int64(nil), raw.Tokens...)
	p.mu.Unlock()
	return *raw.Count, nil
}

func (p *SGLangProvider) Generate(ctx context.Context, req Request) (Response, error) {
	if err := p.validate(req); err != nil {
		return Response{}, err
	}
	if _, err := p.inspect(ctx); err != nil {
		return Response{}, err
	}
	digest := sglangRequestDigest(req)
	p.mu.Lock()
	var tokens []int64
	if p.lastRequest == digest {
		tokens = append([]int64(nil), p.lastTokens...)
	}
	p.mu.Unlock()
	if len(tokens) == 0 {
		raw, err := p.tokenize(ctx, req)
		if err != nil {
			return Response{}, err
		}
		tokens = raw.Tokens
		if _, err := p.inspect(ctx); err != nil {
			return Response{}, err
		}
	}
	payload := struct {
		InputIDs       []int64 `json:"input_ids"`
		CacheSalt      string  `json:"cache_salt"`
		Stream         bool    `json:"stream"`
		SamplingParams struct {
			Temperature  float64 `json:"temperature"`
			MaxNewTokens int64   `json:"max_new_tokens"`
		} `json:"sampling_params"`
	}{InputIDs: tokens, CacheSalt: p.cacheSalt}
	payload.SamplingParams.MaxNewTokens = req.MaxOutputTokens
	var raw sglangGeneration
	if err := p.request(ctx, http.MethodPost, "/generate", payload, &raw); err != nil {
		return Response{}, err
	}
	if _, err := p.inspect(ctx); err != nil {
		return Response{}, err
	}
	return p.normalize(raw, req, int64(len(tokens)))
}

type sglangGeneration struct {
	Text     string          `json:"text"`
	Error    json.RawMessage `json:"error"`
	MetaInfo *struct {
		ID               string `json:"id"`
		PromptTokens     *int64 `json:"prompt_tokens"`
		CompletionTokens *int64 `json:"completion_tokens"`
		ReasoningTokens  *int64 `json:"reasoning_tokens"`
		CachedTokens     *int64 `json:"cached_tokens"`
		WeightVersion    string `json:"weight_version"`
		WeightVersions   []struct {
			Version string `json:"version"`
			Start   int64  `json:"start"`
			End     int64  `json:"end"`
		} `json:"weight_versions"`
		FinishReason *struct {
			Type string `json:"type"`
		} `json:"finish_reason"`
		CachedTokensDetails *struct {
			Device         *int64 `json:"device"`
			Host           *int64 `json:"host"`
			Storage        *int64 `json:"storage"`
			StorageBackend string `json:"storage_backend"`
		} `json:"cached_tokens_details"`
	} `json:"meta_info"`
}

func (p *SGLangProvider) normalize(raw sglangGeneration, req Request, count int64) (Response, error) {
	meta := raw.MetaInfo
	if nonNullJSON(raw.Error) || meta == nil || meta.PromptTokens == nil || meta.CompletionTokens == nil || meta.FinishReason == nil {
		return Response{}, errors.New("SGLang response is missing valid completion metadata")
	}
	if *meta.PromptTokens != count || *meta.PromptTokens > p.cfg.MaxInputTokens || meta.WeightVersion != p.weightVersion {
		return Response{}, errors.New("SGLang response does not match the counted deployment request")
	}
	u := Usage{InputTokens: *meta.PromptTokens, UncachedInputTokens: *meta.PromptTokens, OutputTokens: *meta.CompletionTokens}
	if meta.ReasoningTokens != nil {
		u.ReasoningTokens = *meta.ReasoningTokens
	}
	if !validTokenCount(u.InputTokens) || validateOutputUsage(u, req) != nil {
		return Response{}, errors.New("SGLang response contains invalid token usage")
	}
	for _, version := range meta.WeightVersions {
		if version.Version != p.weightVersion || version.Start < 0 || version.End < version.Start || version.End > u.OutputTokens {
			return Response{}, errors.New("SGLang response contains mixed or invalid weight versions")
		}
	}
	cache := &RuntimeCacheObservation{CachedTokens: meta.CachedTokens, Profile: p.cfg.Profile, RuntimeVersion: SGLangRuntimeVersion, Evidence: "runtime_reported"}
	if meta.CachedTokens != nil && (!validTokenCount(*meta.CachedTokens) || *meta.CachedTokens > u.InputTokens) {
		return Response{}, errors.New("SGLang response contains invalid runtime cache usage")
	}
	if details := meta.CachedTokensDetails; details != nil {
		cache.DeviceTokens, cache.HostTokens, cache.StorageTokens = details.Device, details.Host, details.Storage
		var sum int64
		for _, value := range []*int64{details.Device, details.Host, details.Storage} {
			if value != nil {
				if !validTokenCount(*value) || *value > u.InputTokens {
					return Response{}, errors.New("SGLang response contains invalid cache tier usage")
				}
				sum += *value
			}
		}
		if sum > u.InputTokens || (meta.CachedTokens != nil && (sum > *meta.CachedTokens || (details.Device != nil && details.Host != nil && details.Storage != nil && sum != *meta.CachedTokens))) {
			return Response{}, errors.New("SGLang response contains inconsistent cache tier usage")
		}
		if details.StorageBackend != "" {
			if !sglangBackendIdentifier(details.StorageBackend) {
				return Response{}, errors.New("SGLang response contains an invalid cache backend identifier")
			}
			cache.StorageBackend = details.StorageBackend
		}
	}
	result := Response{ID: meta.ID, Model: p.model, Text: raw.Text, Usage: u, RuntimeCache: cache, Complete: meta.FinishReason.Type == "stop" && raw.Text != "" && meta.ID != ""}
	switch meta.FinishReason.Type {
	case "stop", "length", "abort":
		result.StopReason = meta.FinishReason.Type
	default:
		result.StopReason = "unknown_status"
	}
	if (result.ID != "" && !validID(result.ID, 256)) || !utf8.ValidString(result.Text) {
		return Response{}, errors.New("SGLang response contains invalid text or request identity")
	}
	if u.OutputTokens > req.MaxOutputTokens {
		result.Complete, result.StopReason = false, "usage_limit_exceeded"
	}
	return result, nil
}

func sglangBackendIdentifier(v string) bool {
	if len(v) == 0 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func (p *SGLangProvider) request(ctx context.Context, method, path string, payload, result any) error {
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil || len(body) > maxProviderRequestBytes {
			return errors.New("SGLang request exceeds the bounded payload contract")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return errors.New("cannot create SGLang request")
	}
	req.GetBody = nil
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("SGLang request interrupted: %w", ctx.Err())
		}
		return errors.New("SGLang request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("SGLang returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxProviderResponseBytes {
		return errors.New("SGLang response exceeds the byte limit")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderResponseBytes+1))
	if err != nil || len(data) > maxProviderResponseBytes {
		return errors.New("cannot read bounded SGLang response")
	}
	if !utf8.Valid(data) || json.Unmarshal(data, result) != nil {
		return errors.New("SGLang returned invalid JSON")
	}
	return nil
}
