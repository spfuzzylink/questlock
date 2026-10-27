package semantic

import (
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
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxEmbeddingReplyBytes = 1 << 20

// OllamaEmbedder captures already-installed local weights and the Ollama runtime
// version. It never pulls models, follows redirects, or uses environment proxies.
// Its methods are safe for concurrent use; its pinned identity is immutable.
type OllamaEmbedder struct {
	baseURL       string
	model         string
	digest        string
	runtime       string
	modelID       string
	maxInputBytes int
	client        *http.Client
}

type embeddingMetadata struct {
	digest  string
	runtime string
}

// NewOllamaEmbedder validates the installed model without generating a vector.
// The empty origin selects http://127.0.0.1:11434. Discovery is capped at 10s.
func NewOllamaEmbedder(baseURL, model string, maxInputBytes int) (*OllamaEmbedder, error) {
	return NewOllamaEmbedderContext(context.Background(), baseURL, model, maxInputBytes)
}

// NewOllamaEmbedderContext also includes metadata discovery in caller's deadline.
func NewOllamaEmbedderContext(ctx context.Context, baseURL, model string, maxInputBytes int) (*OllamaEmbedder, error) {
	if ctx == nil {
		return nil, errors.New("semantic embedding requires a context")
	}
	if baseURL == "" {
		baseURL = "http://127.0.0.1:11434"
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("semantic Ollama URL requires a literal loopback HTTP origin")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("semantic Ollama URL requires a literal loopback IP address")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, errors.New("semantic Ollama URL has an invalid port")
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("semantic Ollama URL has an invalid port")
	}
	if !validOllamaModel(model) {
		return nil, errors.New("semantic Ollama requires an installed local model name without cloud markers")
	}
	if maxInputBytes == 0 {
		maxInputBytes = DefaultMaxInputBytes
	}
	if maxInputBytes < 1 || maxInputBytes > MaxInputBytes {
		return nil, errors.New("semantic embedding input limit exceeds supported bounds")
	}
	u.Path = ""
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ResponseHeaderTimeout:  time.Duration(MaxTimeoutMS) * time.Millisecond,
		MaxResponseHeaderBytes: 32 << 10,
		MaxIdleConns:           4,
		MaxIdleConnsPerHost:    4,
		IdleConnTimeout:        30 * time.Second,
		DisableCompression:     true,
	}
	p := &OllamaEmbedder{baseURL: u.String(), model: canonicalOllamaModel(model), maxInputBytes: maxInputBytes, client: &http.Client{
		Transport: transport,
		Timeout:   time.Duration(MaxTimeoutMS) * time.Millisecond,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(DefaultTimeoutMS)*time.Millisecond)
	defer cancel()
	metadata, err := p.inspect(ctx)
	if err != nil {
		p.Close()
		return nil, err
	}
	p.digest, p.runtime = metadata.digest, metadata.runtime
	identity, _ := json.Marshal(struct {
		Adapter string `json:"adapter"`
		Digest  string `json:"digest"`
		Runtime string `json:"runtime"`
	}{"questlock/semantic/ollama-embed/v1:single-text:truncate-false:default-dimensions", p.digest, p.runtime})
	sum := sha256.Sum256(identity)
	p.modelID = hex.EncodeToString(sum[:])
	return p, nil
}

// Close releases idle local connections. It does not unload model weights.
func (p *OllamaEmbedder) Close() {
	if p != nil && p.client != nil {
		p.client.CloseIdleConnections()
	}
}

// Embed sends one bounded text input with truncation disabled. Both sides of
// inference recheck the model digest, embedding capability and runtime version.
// A failed or changed snapshot never returns a usable vector or token count.
func (p *OllamaEmbedder) Embed(ctx context.Context, text string) (Embedding, error) {
	if p == nil || p.client == nil || !validHash(p.modelID) {
		return Embedding{}, errors.New("semantic Ollama embedder is not initialized")
	}
	if ctx == nil {
		return Embedding{}, errors.New("semantic embedding requires a context")
	}
	if strings.TrimSpace(text) == "" || !utf8.ValidString(text) || len(text) > p.maxInputBytes {
		return Embedding{}, errors.New("semantic embedding text is empty, invalid UTF-8 or exceeds the input limit")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(MaxTimeoutMS)*time.Millisecond)
	defer cancel()
	if _, err := p.inspect(ctx); err != nil {
		return Embedding{}, err
	}
	start := time.Now()
	data, err := p.request(ctx, http.MethodPost, "/api/embed", struct {
		Model    string `json:"model"`
		Input    string `json:"input"`
		Truncate bool   `json:"truncate"`
	}{p.model, text, false})
	duration := float64(time.Since(start)) / float64(time.Millisecond)
	if err != nil {
		return Embedding{}, err
	}
	var reply struct {
		Model           string      `json:"model"`
		Embeddings      [][]float32 `json:"embeddings"`
		PromptEvalCount *int64      `json:"prompt_eval_count"`
	}
	if json.Unmarshal(data, &reply) != nil || !validOllamaModel(reply.Model) || canonicalOllamaModel(reply.Model) != p.model || len(reply.Embeddings) != 1 {
		return Embedding{}, errors.New("semantic Ollama returned an invalid embedding reply")
	}
	if err := validateVector(reply.Embeddings[0]); err != nil {
		return Embedding{}, err
	}
	if reply.PromptEvalCount != nil && (*reply.PromptEvalCount < 1 || *reply.PromptEvalCount > 1<<20) {
		return Embedding{}, errors.New("semantic Ollama reported an invalid embedding token count")
	}
	if _, err := p.inspect(ctx); err != nil {
		return Embedding{}, err
	}
	return Embedding{ModelID: p.modelID, Vector: reply.Embeddings[0], InputTokens: reply.PromptEvalCount, DurationMS: duration}, nil
}

func (p *OllamaEmbedder) inspect(ctx context.Context) (embeddingMetadata, error) {
	var metadata embeddingMetadata
	data, err := p.request(ctx, http.MethodGet, "/api/version", nil)
	if err != nil {
		return metadata, err
	}
	var version struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &version) != nil || !validRuntimeVersion(version.Version) {
		return metadata, errors.New("semantic Ollama did not report a valid runtime version")
	}
	metadata.runtime = version.Version
	if p.runtime != "" && metadata.runtime != p.runtime {
		return metadata, errors.New("semantic Ollama runtime changed; recreate the embedder")
	}
	data, err = p.request(ctx, http.MethodGet, "/api/tags", nil)
	if err != nil {
		return metadata, err
	}
	var tags struct {
		Models []json.RawMessage `json:"models"`
	}
	if json.Unmarshal(data, &tags) != nil || tags.Models == nil {
		return metadata, errors.New("semantic Ollama did not return installed model metadata")
	}
	for _, raw := range tags.Models {
		var item struct {
			Name   string `json:"name"`
			Model  string `json:"model"`
			Digest string `json:"digest"`
		}
		if json.Unmarshal(raw, &item) != nil || item.Name == "" && item.Model == "" {
			return metadata, errors.New("semantic Ollama returned invalid installed model metadata")
		}
		if item.Name == "" {
			item.Name = item.Model
		}
		if !validOllamaModel(item.Name) || canonicalOllamaModel(item.Name) != p.model {
			continue
		}
		if item.Model != "" && (!validOllamaModel(item.Model) || canonicalOllamaModel(item.Model) != p.model) || metadata.digest != "" {
			return metadata, errors.New("semantic Ollama returned ambiguous installed model metadata")
		}
		var fields map[string]any
		if json.Unmarshal(raw, &fields) != nil || hasRemoteMetadata(fields) {
			return metadata, errors.New("semantic Ollama local mode refuses remote installed model metadata")
		}
		digest := strings.TrimPrefix(item.Digest, "sha256:")
		if !validHash(digest) {
			return metadata, errors.New("semantic Ollama installed model has no valid digest")
		}
		metadata.digest = digest
	}
	if metadata.digest == "" {
		return metadata, errors.New("semantic Ollama model is not installed; install local weights first")
	}
	if p.digest != "" && p.digest != metadata.digest {
		return metadata, errors.New("semantic Ollama model digest changed; recreate the embedder")
	}
	data, err = p.request(ctx, http.MethodPost, "/api/show", struct {
		Model string `json:"model"`
	}{p.model})
	if err != nil {
		return metadata, err
	}
	var show struct {
		Capabilities []string `json:"capabilities"`
	}
	if json.Unmarshal(data, &show) == nil {
		for _, capability := range show.Capabilities {
			if capability == "embedding" {
				return metadata, nil
			}
		}
	}
	return metadata, errors.New("semantic Ollama model does not advertise embedding capability")
}

func (p *OllamaEmbedder) request(ctx context.Context, method, path string, body any) ([]byte, error) {
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil || len(encoded) > 256<<10 {
			return nil, errors.New("semantic Ollama request exceeds supported bounds")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, errors.New("semantic Ollama request could not be constructed")
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("semantic Ollama local request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("semantic Ollama request failed with HTTP status %d", response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || response.Header.Get("Content-Encoding") != "" {
		return nil, errors.New("semantic Ollama response must be uncompressed JSON")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxEmbeddingReplyBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("semantic Ollama response could not be read")
	}
	if len(data) > maxEmbeddingReplyBytes || !utf8.Valid(data) {
		return nil, errors.New("semantic Ollama response exceeds supported bounds or is invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeOllamaJSON(decoder, 0)
	if err != nil {
		return nil, errors.New("semantic Ollama returned malformed or ambiguous JSON")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("semantic Ollama returned trailing JSON")
	}
	object, ok := value.(map[string]any)
	if !ok || path != "/api/tags" && hasRemoteMetadata(object) {
		return nil, errors.New("semantic Ollama local mode refuses invalid or remote model metadata")
	}
	if value, exists := object["error"]; exists && value != nil && value != "" {
		return nil, errors.New("semantic Ollama returned a local runtime error")
	}
	if value, exists := object["truncated"]; exists && value != false {
		return nil, errors.New("semantic Ollama refused a truncated embedding response")
	}
	return data, nil
}

func validOllamaModel(model string) bool {
	if len(model) == 0 || len(model) > 128 || strings.Contains(strings.ToLower(model), "cloud") || strings.Contains(model, "://") {
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

func validRuntimeVersion(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._+-", c)) {
			return false
		}
	}
	return true
}

func hasRemoteMetadata(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if (key == "remote_host" || key == "remote_model") && item != nil && item != "" {
				return true
			}
			if hasRemoteMetadata(item) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if hasRemoteMetadata(item) {
				return true
			}
		}
	}
	return false
}

// Refuse duplicate object keys and excessive nesting rather than interpreting
// an ambiguous remote/local declaration differently from the runtime.
func decodeOllamaJSON(decoder *json.Decoder, depth int) (any, error) {
	if depth > 32 {
		return nil, errors.New("JSON nesting limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		object := make(map[string]any)
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok || len(object) >= 16384 {
				return nil, errors.New("invalid JSON object")
			}
			if _, exists := object[name]; exists {
				return nil, errors.New("duplicate JSON key")
			}
			value, err := decodeOllamaJSON(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			object[name] = value
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
			return nil, errors.New("invalid JSON object end")
		}
		return object, nil
	case json.Delim('['):
		array := make([]any, 0)
		for decoder.More() {
			if len(array) >= 16384 {
				return nil, errors.New("JSON array limit")
			}
			value, err := decodeOllamaJSON(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
			return nil, errors.New("invalid JSON array end")
		}
		return array, nil
	case json.Delim('}'), json.Delim(']'):
		return nil, errors.New("unexpected JSON delimiter")
	default:
		return token, nil
	}
}
