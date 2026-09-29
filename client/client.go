// Package client provides a bounded, authenticated HTTP client for Agent Fence.
// Publish never automatically retries. To retry after an uncertain response,
// reuse the exact same request and operation ID so the server can replay it.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spfuzzylink/agent-fence/protocol"
)

const (
	DefaultTimeout   = 15 * time.Second
	MaxResponseBytes = 16 << 20
)

// Error is an error returned by the service, with its HTTP status preserved.
type Error struct {
	Code           string
	Message        string
	CurrentVersion int64
	Status         int
}

func (e *Error) Error() string {
	return fmt.Sprintf("agent-fence: %s (HTTP %d): %s", e.Code, e.Status, e.Message)
}

// Client may be shared by concurrent goroutines. Its credentials are sent only
// to the configured server; redirects are rejected.
type Client struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
}

// New creates a client with a 15-second request timeout and 16MiB response limit.
// Plain HTTP should be used only with a trusted local transport.
func New(baseURL, token string) (*Client, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, errors.New("agent-fence: invalid base URL")
	}
	if (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" || base.Opaque != "" || base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" || strings.Contains(baseURL, "#") {
		return nil, errors.New("agent-fence: base URL must use http or https and have no credentials, query, or fragment")
	}
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("agent-fence: a nonempty bearer token without whitespace is required")
	}
	return &Client{
		baseURL: base,
		token:   token,
		httpClient: &http.Client{
			Timeout: DefaultTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c *Client) Publish(ctx context.Context, request protocol.PublishRequest) (protocol.PublishResult, error) {
	var result protocol.PublishResult
	// json.Marshal would silently replace malformed UTF-8 in Go strings.
	if !utf8.ValidString(request.Key) || !utf8.ValidString(request.OperationID) || !utf8.ValidString(request.Content) {
		return result, errors.New("agent-fence: publish strings must contain valid UTF-8")
	}
	err := c.do(ctx, http.MethodPost, "/v1/artifact", nil, request, &result)
	return result, err
}

func (c *Client) Get(ctx context.Context, key string) (protocol.Artifact, error) {
	var artifact protocol.Artifact
	err := c.do(ctx, http.MethodGet, "/v1/artifact", url.Values{"key": {key}}, nil, &artifact)
	return artifact, err
}

func (c *Client) List(ctx context.Context) ([]protocol.Artifact, error) {
	var artifacts []protocol.Artifact
	err := c.do(ctx, http.MethodGet, "/v1/artifacts", nil, nil, &artifacts)
	return artifacts, err
}

func (c *Client) Audit(ctx context.Context, limit int) ([]protocol.AuditEvent, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("agent-fence: audit limit must be between 1 and 1000")
	}
	var events []protocol.AuditEvent
	err := c.do(ctx, http.MethodGet, "/v1/audit", url.Values{"limit": {strconv.Itoa(limit)}}, nil, &events)
	return events, err
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, input, output any) error {
	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
	endpoint.RawPath = ""
	endpoint.RawQuery = query.Encode()
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("agent-fence: encode request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return fmt.Errorf("agent-fence: create request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("agent-fence: request failed: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("agent-fence: read response: %w", err)
	}
	if len(data) > MaxResponseBytes {
		return errors.New("agent-fence: response exceeds 16MiB limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var body protocol.ErrorBody
		if json.Unmarshal(data, &body) != nil || body.Code == "" {
			body = protocol.ErrorBody{Code: "http_error", Message: http.StatusText(response.StatusCode)}
		}
		return &Error{Code: body.Code, Message: body.Message, CurrentVersion: body.CurrentVersion, Status: response.StatusCode}
	}
	if err := json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("agent-fence: decode response: %w", err)
	}
	return nil
}
