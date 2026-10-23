package observability

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxPayloadBytes  = 64 << 10
	maxResponseBytes = 64 << 10
	defaultTimeout   = 2 * time.Second
	maxTimeout       = 5 * time.Second
)

var (
	ErrInvalidExecution = errors.New("invalid observability metadata")
	ErrUnavailable      = errors.New("Langfuse export was not acknowledged")
	ErrRejected         = errors.New("Langfuse rejected one or more spans")
	ErrInvalidResponse  = errors.New("Langfuse returned an invalid or oversized OTLP response")
)

// Config explicitly opts in to metadata export. Endpoint is the full traces URL
// ending in /api/public/otel/v1/traces, including any self-hosted path prefix.
// HTTPS is required except for a literal loopback HTTP address. Keys are sent
// only as HTTP Basic authentication; neither keys nor endpoint enter spans.
// Timeout defaults to two seconds and may be shortened, but not exceed five.
type Config struct {
	Endpoint  string
	PublicKey string
	SecretKey string
	Timeout   time.Duration
	Release   string
}

// Langfuse is a small OTLP/HTTP JSON exporter with no global telemetry state,
// environment discovery, background queue, proxy, redirects, or export retries.
// Its deliberately narrow schema uses the official OTLP JSON mapping, including
// hexadecimal IDs, numeric enums, and string-encoded 64-bit integers.
type Langfuse struct {
	endpoint, publicKey, secretKey, release string
	client                                  *http.Client
}

// NewLangfuse validates configuration before any network access. Constructing an
// exporter does not create a project, probe the endpoint, or modify a client.
func NewLangfuse(c Config) (*Langfuse, error) {
	if len(c.Endpoint) == 0 || len(c.Endpoint) > 2048 {
		return nil, errors.New("Langfuse requires an explicit traces endpoint")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" {
		return nil, errors.New("Langfuse endpoint must be an absolute URL without credentials, query, fragment, or encoded path")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, errors.New("Langfuse endpoint requires HTTPS or literal loopback HTTP")
	}
	loopbackHTTP := u.Scheme == "http"
	if loopbackHTTP {
		ip, err := netip.ParseAddr(u.Hostname())
		if err != nil || !ip.IsLoopback() || ip.Zone() != "" {
			return nil, errors.New("plain HTTP Langfuse endpoint requires a literal loopback IP")
		}
	}
	if !strings.HasSuffix(u.Path, "/api/public/otel/v1/traces") || strings.Contains(u.Path, "\\") {
		return nil, errors.New("Langfuse endpoint must end in /api/public/otel/v1/traces")
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return nil, errors.New("Langfuse endpoint has an unsupported path")
		}
	}
	if !validKey(c.PublicKey) || !validKey(c.SecretKey) || strings.Contains(c.PublicKey, ":") {
		return nil, errors.New("Langfuse requires valid project public and secret keys")
	}
	if c.Timeout == 0 {
		c.Timeout = defaultTimeout
	}
	if c.Timeout < time.Millisecond || c.Timeout > maxTimeout {
		return nil, errors.New("Langfuse timeout must be between 1 millisecond and 5 seconds")
	}
	if c.Release != "" && !validLabel(c.Release) {
		return nil, errors.New("Langfuse release must be a bounded version identifier")
	}
	dialer := &net.Dialer{Timeout: c.Timeout}
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
					return nil, ErrUnavailable
				}
			}
			return conn, nil
		},
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    c.Timeout,
		ResponseHeaderTimeout:  c.Timeout,
		MaxResponseHeaderBytes: 16 << 10,
		DisableKeepAlives:      true,
	}
	return &Langfuse{endpoint: u.String(), publicKey: c.PublicKey, secretKey: c.SecretKey, release: c.Release, client: &http.Client{
		Transport: transport,
		Timeout:   c.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}, nil
}

// Export sends at most one request after validating the allowlisted metadata.
// No transport or server response text is included in its errors. Acknowledged
// export confirms endpoint acceptance, not visibility in a Langfuse dashboard.
func (l *Langfuse) Export(ctx context.Context, e Execution) (Receipt, error) {
	if l == nil || l.client == nil || ctx == nil {
		return Receipt{}, ErrUnavailable
	}
	if err := validateExecution(e); err != nil {
		return Receipt{}, err
	}
	receipt, err := newReceipt(e.Generation != nil)
	if err != nil {
		return Receipt{}, ErrUnavailable
	}
	body, err := json.Marshal(l.envelope(e, receipt))
	if err != nil || len(body) > maxPayloadBytes {
		return receipt, ErrInvalidExecution
	}
	return receipt, l.sendTraces(ctx, body)
}

// sendTraces preserves the same bounded, single-attempt transport for each
// allowlisted observation type. Callers validate their schema before encoding.
func (l *Langfuse) sendTraces(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.endpoint, bytes.NewReader(body))
	if err != nil {
		return ErrUnavailable
	}
	req.SetBasicAuth(l.publicKey, l.secretKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-langfuse-ingestion-version", "4")
	req.Header.Set("User-Agent", "questlock-otlp")
	resp, err := l.client.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ErrUnavailable
	}
	contentType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" || resp.ContentLength > maxResponseBytes {
		return ErrInvalidResponse
	}
	response, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(response) > maxResponseBytes {
		return ErrInvalidResponse
	}
	return validateAcknowledgement(response)
}

func validKey(s string) bool {
	if len(s) < 1 || len(s) > 4096 {
		return false
	}
	for _, c := range s {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func validLabel(s string) bool {
	if len(s) == 0 || len(s) > 200 {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("._-:/", c)) {
			return false
		}
	}
	return !strings.Contains(s, "://") && !strings.HasPrefix(s, "/")
}

func validOutcome(o Outcome) bool {
	switch o {
	case Complete, Reused, Pending, BudgetBlocked, Incomplete, LimitExceeded, Uncertain, NotDurable, Failed:
		return true
	}
	return false
}

func validInterval(start, end time.Time) bool {
	// UnixNano is undefined outside its signed int64 range. Reject rather than
	// wrapping a timestamp into an unrelated observation or emitting zero time.
	return !start.IsZero() && !end.IsZero() && start.Year() >= 1970 && end.Year() < 2262 && !end.Before(start)
}

func validUsage(u Usage) bool {
	for _, n := range []int64{u.InputTokens, u.UncachedInputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.OutputTokens, u.ReasoningTokens} {
		if n < 0 || n > 1_000_000_000 {
			return false
		}
	}
	return u.InputTokens == u.UncachedInputTokens+u.CacheReadTokens+u.CacheWriteTokens && u.ReasoningTokens <= u.OutputTokens
}

func validCost(cost *float64) bool {
	return cost == nil || (!math.IsNaN(*cost) && !math.IsInf(*cost, 0) && *cost >= 0 && *cost <= 1_000_000_000)
}

func validateExecution(e Execution) error {
	if !validInterval(e.StartedAt, e.EndedAt) || !validOutcome(e.Outcome) {
		return ErrInvalidExecution
	}
	if e.Fingerprint != "" {
		digest, err := hex.DecodeString(e.Fingerprint)
		if err != nil || len(digest) != 32 {
			return ErrInvalidExecution
		}
	}
	switch e.Provider {
	case "", "openai", "anthropic", "claude", "ollama", "openrouter", "sglang":
	default:
		return ErrInvalidExecution
	}
	if (e.Model != "" && !validLabel(e.Model)) || !validUsage(e.ThisRunUsage) || !validUsage(e.OriginalUsage) || !validUsage(e.AvoidedUsage) || !validCost(e.CostUSD) || !validCost(e.AvoidedCostUSD) {
		return ErrInvalidExecution
	}
	if e.ContextBytes < 0 || e.TotalContextBytes < e.ContextBytes || e.TotalContextBytes > 8<<20 || e.SelectedContext < 0 || e.OmittedContext < 0 || e.SelectedContext > 10000 || e.OmittedContext > 10000 {
		return ErrInvalidExecution
	}
	for name, ms := range e.PhaseMS {
		switch name {
		case "provider_setup", "runtime_identity", "plan", "lookup", "admission", "preflight", "reservation", "generation", "publication", "settlement", "recovery":
		default:
			return ErrInvalidExecution
		}
		if math.IsNaN(ms) || math.IsInf(ms, 0) || ms < 0 {
			return ErrInvalidExecution
		}
	}
	if e.Reused && (e.Generation != nil || e.ThisRunUsage != (Usage{}) || (e.CostUSD != nil && *e.CostUSD != 0) || e.Outcome != Reused) {
		return ErrInvalidExecution
	}
	if !e.Reused && (e.AvoidedUsage != (Usage{}) || e.AvoidedCostUSD != nil || e.Outcome == Reused) {
		return ErrInvalidExecution
	}
	if g := e.Generation; g != nil {
		if cache := g.RuntimeCache; cache != nil {
			if e.Provider != "sglang" || !g.KnownUsage || (cache.Profile != "dense" && cache.Profile != "hicache" && cache.Profile != "hisparse") {
				return ErrInvalidExecution
			}
			var sum int64
			for _, count := range []*int64{cache.CachedTokens, cache.DeviceTokens, cache.HostTokens, cache.StorageTokens} {
				if count != nil && (*count < 0 || *count > g.Usage.InputTokens) {
					return ErrInvalidExecution
				}
			}
			for _, count := range []*int64{cache.DeviceTokens, cache.HostTokens, cache.StorageTokens} {
				if count != nil {
					sum += *count
				}
			}
			if sum > g.Usage.InputTokens || (cache.CachedTokens != nil && (sum > *cache.CachedTokens || (cache.DeviceTokens != nil && cache.HostTokens != nil && cache.StorageTokens != nil && sum != *cache.CachedTokens))) {
				return ErrInvalidExecution
			}
		}
		if !validInterval(g.StartedAt, g.EndedAt) || g.StartedAt.Before(e.StartedAt) || g.EndedAt.After(e.EndedAt) || !validOutcome(g.Outcome) || g.Outcome == Reused || g.Outcome == Pending || g.Outcome == BudgetBlocked || (g.Model != "" && !validLabel(g.Model)) || !validUsage(g.Usage) || !validCost(g.CostUSD) {
			return ErrInvalidExecution
		}
		if !g.KnownUsage && (g.Usage != (Usage{}) || g.CostUSD != nil || e.ThisRunUsage != (Usage{}) || e.CostUSD != nil) {
			return ErrInvalidExecution
		}
		if g.KnownUsage && g.Usage != e.ThisRunUsage {
			return ErrInvalidExecution
		}
		if g.TimingBasis != "" && g.TimingBasis != "client_observed_content_chunks" {
			return ErrInvalidExecution
		}
		if g.FirstTokenMS != nil {
			if g.TimingBasis != "client_observed_content_chunks" || !validMillis(*g.FirstTokenMS) || *g.FirstTokenMS > milliseconds(g.StartedAt, g.EndedAt) {
				return ErrInvalidExecution
			}
		}
		if g.OutputMS != nil {
			if g.FirstTokenMS == nil || !validMillis(*g.OutputMS) || *g.FirstTokenMS+*g.OutputMS > milliseconds(g.StartedAt, g.EndedAt) {
				return ErrInvalidExecution
			}
		}
	} else if !e.Reused && (e.ThisRunUsage != (Usage{}) || e.CostUSD != nil) {
		return ErrInvalidExecution
	}
	return nil
}

func validMillis(ms float64) bool { return !math.IsNaN(ms) && !math.IsInf(ms, 0) && ms >= 0 }

func newReceipt(generation bool) (Receipt, error) {
	var ids [32]byte
	if _, err := rand.Read(ids[:]); err != nil {
		return Receipt{}, err
	}
	r := Receipt{TraceID: hex.EncodeToString(ids[:16]), RootSpanID: hex.EncodeToString(ids[16:24])}
	if generation {
		r.GenerationSpanID = hex.EncodeToString(ids[24:])
	}
	return r, nil
}

func validateAcknowledgement(body []byte) error {
	var response map[string]json.RawMessage
	if json.Unmarshal(body, &response) != nil || response == nil {
		return ErrInvalidResponse
	}
	partial, ok := response["partialSuccess"]
	if !ok || bytes.Equal(bytes.TrimSpace(partial), []byte("null")) {
		return nil
	}
	var p struct {
		RejectedSpans json.RawMessage `json:"rejectedSpans"`
		ErrorMessage  string          `json:"errorMessage"`
	}
	if json.Unmarshal(partial, &p) != nil {
		return ErrInvalidResponse
	}
	if len(p.RejectedSpans) == 0 {
		return nil
	}
	text := string(p.RejectedSpans)
	if strings.HasPrefix(text, "\"") {
		if json.Unmarshal(p.RejectedSpans, &text) != nil {
			return ErrInvalidResponse
		}
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || n < 0 {
		return ErrInvalidResponse
	}
	if n > 0 {
		return ErrRejected
	}
	// The OTLP contract permits a warning with zero rejected spans. A warning
	// does not invalidate acknowledgement, and server text is never forwarded.
	return nil
}
