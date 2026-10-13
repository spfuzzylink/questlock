package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const openAIResponseFixture = `{"id":"resp_1","model":"gpt-6.1-sol","status":"completed","error":null,"incomplete_details":null,"output":[{"type":"reasoning","summary":[]},{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Useful answer."}]}],"usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":600,"cache_write_tokens":300},"output_tokens":20,"output_tokens_details":{"reasoning_tokens":5}}}`
const claudeResponseFixture = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5","stop_reason":"end_turn","stop_details":null,"content":[{"type":"thinking","thinking":"internal"},{"type":"text","text":"Useful answer."}],"usage":{"input_tokens":100,"cache_creation_input_tokens":300,"cache_read_input_tokens":600,"output_tokens":20,"output_tokens_details":{"thinking_tokens":5}}}`

func providerTestRequest(provider string) Request {
	model := "gpt-6.1-sol"
	if provider == "anthropic" || provider == "claude" {
		model = "claude-sonnet-5-5"
	}
	return Request{Provider: provider, Model: model, Prefix: "Stable instructions.", Input: "Summarize this change.", MaxOutputTokens: 64, CacheMode: "prefix"}
}

func testHTTPProvider(t *testing.T, provider string, handler http.HandlerFunc) *HTTPProvider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p, err := NewHTTPProvider(provider, "unit-test-key", srv.URL+"/v1/")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.client.CloseIdleConnections)
	return p
}

func TestHTTPProviderRequestsAndUsage(t *testing.T) {
	for _, provider := range []string{"openai", "claude"} {
		t.Run(provider, func(t *testing.T) {
			recorded := make(chan struct {
				path string
				body map[string]any
			}, 2)
			p := testHTTPProvider(t, provider, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
					t.Error("request must be JSON POST")
				}
				if provider == "openai" {
					if r.Header.Get("Authorization") != "Bearer unit-test-key" || r.Header.Get("x-api-key") != "" {
						t.Error("wrong OpenAI credentials")
					}
				} else if r.Header.Get("x-api-key") != "unit-test-key" || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Authorization") != "" {
					t.Error("wrong Claude credentials")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				recorded <- struct {
					path string
					body map[string]any
				}{r.URL.Path, body}
				if strings.HasSuffix(r.URL.Path, "count_tokens") || strings.HasSuffix(r.URL.Path, "input_tokens") {
					io.WriteString(w, `{"input_tokens":1000}`)
					return
				}
				if provider == "openai" {
					io.WriteString(w, openAIResponseFixture)
				} else {
					io.WriteString(w, claudeResponseFixture)
				}
			})
			req := providerTestRequest(provider)
			count, err := p.CountInputTokens(context.Background(), req)
			if err != nil || count != 1000 {
				t.Fatalf("count = %d, error = %v", count, err)
			}
			response, err := p.Generate(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			want := Usage{InputTokens: 1000, UncachedInputTokens: 100, CacheReadTokens: 600, CacheWriteTokens: 300, OutputTokens: 20, ReasoningTokens: 5}
			if response.Usage != want || !response.Complete || response.Text != "Useful answer." {
				t.Fatalf("response = %+v", response)
			}
			countRequest, generateRequest := <-recorded, <-recorded
			for _, body := range []map[string]any{countRequest.body, generateRequest.body} {
				if _, ok := body["tools"]; ok {
					t.Fatal("unexpected tools")
				}
				if _, ok := body["previous_response_id"]; ok {
					t.Fatal("unexpected response history")
				}
				if body["model"] != req.Model {
					t.Fatal("wrong model")
				}
			}
			if provider == "openai" {
				if countRequest.path != "/v1/responses/input_tokens" || generateRequest.path != "/v1/responses" {
					t.Fatal("wrong OpenAI endpoints")
				}
				if _, ok := countRequest.body["max_output_tokens"]; ok {
					t.Fatal("count request has generation limit")
				}
				if _, ok := countRequest.body["store"]; ok {
					t.Fatal("count request has store field")
				}
				if _, ok := countRequest.body["prompt_cache_options"]; ok {
					t.Fatal("count request has generation cache options")
				}
				if generateRequest.body["max_output_tokens"] != float64(64) || generateRequest.body["store"] != false {
					t.Fatal("generation bounds/storage missing")
				}
				if !reflect.DeepEqual(countRequest.body["input"], generateRequest.body["input"]) {
					t.Fatal("counted text differs from generated text")
				}
				messages := generateRequest.body["input"].([]any)
				if len(messages) != 2 {
					t.Fatal("expected exactly developer and user messages")
				}
				prefix := messages[0].(map[string]any)
				block := prefix["content"].([]any)[0].(map[string]any)
				if prefix["role"] != "developer" || block["text"] != req.Prefix || messages[1].(map[string]any)["role"] != "user" {
					t.Fatal("wrong input role mapping")
				}
				if !reflect.DeepEqual(block["prompt_cache_breakpoint"], map[string]any{"mode": "explicit"}) || !reflect.DeepEqual(generateRequest.body["prompt_cache_options"], map[string]any{"mode": "explicit", "ttl": "30m"}) {
					t.Fatal("missing explicit prefix cache policy")
				}
			} else {
				if countRequest.path != "/v1/messages/count_tokens" || generateRequest.path != "/v1/messages" {
					t.Fatal("wrong Claude endpoints")
				}
				if _, ok := countRequest.body["max_tokens"]; ok {
					t.Fatal("count request has generation limit")
				}
				if generateRequest.body["max_tokens"] != float64(64) {
					t.Fatal("generation limit missing")
				}
				if !reflect.DeepEqual(countRequest.body["messages"], generateRequest.body["messages"]) || !reflect.DeepEqual(countRequest.body["system"], generateRequest.body["system"]) {
					t.Fatal("counted text differs from generated text")
				}
				block := generateRequest.body["system"].([]any)[0].(map[string]any)
				if block["text"] != req.Prefix || !reflect.DeepEqual(block["cache_control"], map[string]any{"type": "ephemeral", "ttl": "5m"}) {
					t.Fatal("missing Claude prefix cache policy")
				}
				messages := generateRequest.body["messages"].([]any)
				if len(messages) != 1 || messages[0].(map[string]any)["role"] != "user" {
					t.Fatal("Claude must receive one user turn")
				}
			}
		})
	}
}

func TestHTTPProviderDefaultCacheLeavesProviderDefaults(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			p := testHTTPProvider(t, provider, func(w http.ResponseWriter, r *http.Request) {
				data, _ := io.ReadAll(r.Body)
				for _, forbidden := range []string{"prompt_cache_options", "prompt_cache_breakpoint", "cache_control", "prompt_cache_retention", "prompt_cache_key"} {
					if strings.Contains(string(data), forbidden) {
						t.Errorf("default request sets %s", forbidden)
					}
				}
				if provider == "openai" {
					io.WriteString(w, openAIResponseFixture)
				} else {
					io.WriteString(w, claudeResponseFixture)
				}
			})
			req := providerTestRequest(provider)
			req.CacheMode = "default"
			if _, err := p.Generate(context.Background(), req); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHTTPProviderForwardsExplicitOpenAIReasoningEffort(t *testing.T) {
	var calls atomic.Int32
	p := testHTTPProvider(t, "openai", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if !reflect.DeepEqual(body["reasoning"], map[string]any{"effort": "low"}) {
			t.Error("explicit reasoning effort was not forwarded")
		}
		if strings.HasSuffix(r.URL.Path, "input_tokens") {
			io.WriteString(w, `{"input_tokens":1000}`)
		} else {
			io.WriteString(w, openAIResponseFixture)
		}
	})
	req := providerTestRequest("openai")
	req.ReasoningEffort = "low"
	if _, err := p.CountInputTokens(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected request count")
	}
	req.ReasoningEffort = "unrecognized"
	if _, err := p.Generate(context.Background(), req); err == nil {
		t.Fatal("unsupported effort accepted")
	}
	claude := testHTTPProvider(t, "claude", func(w http.ResponseWriter, r *http.Request) { t.Error("Claude reasoning effort reached upstream") })
	req = providerTestRequest("claude")
	req.ReasoningEffort = "low"
	if _, err := claude.Generate(context.Background(), req); err == nil {
		t.Fatal("Claude accepted unsupported reasoning configuration")
	}
}

func TestHTTPProviderEndpointAndCredentialValidation(t *testing.T) {
	for _, endpoint := range []string{"http://example.com/v1", "http://127.0.0.1.example.com/v1", "ftp://127.0.0.1/v1", "/v1", "https://user:secret@example.com/v1", "https://example.com/v1?key=secret", "https://example.com/v1?", "https://example.com/v1#secret", "https://example.com/a/../v1", "https://example.com/%2Fv1", "https:///v1", "https://example.com\\v1"} {
		t.Run(endpoint, func(t *testing.T) {
			_, err := NewHTTPProvider("openai", "unit-test-key", endpoint)
			if err == nil {
				t.Fatal("unsafe base URL accepted")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), endpoint) {
				t.Fatal("base URL leaked in error")
			}
		})
	}
	for _, endpoint := range []string{"", "https://example.com/v1", "http://localhost:9000/v1", "http://127.0.0.1:9000/v1", "http://[::1]:9000/v1"} {
		p, err := NewHTTPProvider("claude", "unit-test-key", endpoint)
		if err != nil {
			t.Fatalf("valid endpoint %s: %v", endpoint, err)
		}
		if p.provider != "anthropic" || !strings.HasPrefix(p.Identity(), "anthropic:") || strings.Contains(p.Identity(), "unit-test-key") {
			t.Fatal("identity is not credential-free and canonical")
		}
		if p.client.Timeout != providerTimeout || p.client.Transport.(*http.Transport).Proxy != nil {
			t.Fatal("timeout/proxy policy missing")
		}
		p.client.CloseIdleConnections()
	}
	for _, key := range []string{"", "key\nsecret", "key secret", strings.Repeat("x", 4097)} {
		if _, err := NewHTTPProvider("openai", key, ""); err == nil {
			t.Fatal("invalid API key accepted")
		}
	}
	if _, err := NewHTTPProvider("unrecognized", "unit-test-key", ""); err == nil {
		t.Fatal("unknown provider accepted")
	}
	p1, _ := NewHTTPProvider("openai", "first-key", "https://example.com/v1/")
	p2, _ := NewHTTPProvider("openai", "second-key", "https://example.com/v1")
	p3, _ := NewHTTPProvider("openai", "first-key", "https://other.example.com/v1")
	if p1.Identity() != p2.Identity() || p1.Identity() == p3.Identity() {
		t.Fatal("identity must normalize endpoint and exclude credentials")
	}
}

func TestHTTPProviderRejectsUnsupportedCacheAndInvalidRequestsBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	p := testHTTPProvider(t, "openai", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		t.Error("invalid request reached upstream")
	})
	for _, mutate := range []func(*Request){
		func(r *Request) { r.Model = "gpt-4.1" },
		func(r *Request) { r.Model = "gpt-6-unknown" },
		func(r *Request) { r.Prefix = "" },
		func(r *Request) { r.CacheMode = "unknown" },
		func(r *Request) { r.Provider = "claude" },
		func(r *Request) { r.Model = "" },
		func(r *Request) { r.Model = "invalid\nmodel" },
		func(r *Request) { r.Input = "" },
		func(r *Request) { r.Input = string([]byte{0xff}) },
		func(r *Request) { r.Input = strings.Repeat("x", maxProviderRequestBytes+1) },
		func(r *Request) { r.Input = strings.Repeat("\x01", maxProviderRequestBytes/2) }, // JSON encoding also has a bound.
		func(r *Request) { r.MaxOutputTokens = 0 },
		func(r *Request) { r.MaxOutputTokens = 1_000_001 },
	} {
		req := providerTestRequest("openai")
		mutate(&req)
		if _, err := p.Generate(context.Background(), req); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request made an upstream call")
	}
}

func TestHTTPProviderRejectsInvalidCountResponses(t *testing.T) {
	for _, body := range []string{`{}`, `{"input_tokens":null}`, `{"input_tokens":0}`, `{"input_tokens":-1}`, `{"input_tokens":1000000001}`, `{"input_tokens":1.5}`, `{"input_tokens":"secret"}`, `{"input_tokens":2} trailing`} {
		t.Run(body, func(t *testing.T) {
			p := testHTTPProvider(t, "openai", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
			if _, err := p.CountInputTokens(context.Background(), providerTestRequest("openai")); err == nil {
				t.Fatal("invalid count accepted")
			}
		})
	}
}

func mutatedFixture(t *testing.T, fixture string, mutate func(map[string]any)) string {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(fixture), &raw); err != nil {
		t.Fatal(err)
	}
	mutate(raw)
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestHTTPProviderNeverMarksUnfinishedOrUnsupportedOutputComplete(t *testing.T) {
	tests := []struct {
		name, provider, fixture string
		mutate                  func(map[string]any)
		wantText                string
	}{
		{"openai truncated", "openai", openAIResponseFixture, func(m map[string]any) {
			m["status"] = "incomplete"
			m["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
			m["output"].([]any)[1].(map[string]any)["status"] = "incomplete"
		}, ""},
		{"openai unknown status", "openai", openAIResponseFixture, func(m map[string]any) { m["status"] = "new-status" }, "Useful answer."},
		{"openai failed", "openai", openAIResponseFixture, func(m map[string]any) { m["status"] = "failed"; m["error"] = map[string]any{"message": "private"} }, "Useful answer."},
		{"openai tool output", "openai", openAIResponseFixture, func(m map[string]any) {
			m["output"] = append(m["output"].([]any), map[string]any{"type": "function_call", "arguments": "private"})
		}, "Useful answer."},
		{"openai refusal", "openai", openAIResponseFixture, func(m map[string]any) {
			m["output"].([]any)[1].(map[string]any)["content"] = []any{map[string]any{"type": "refusal", "refusal": "Declined."}}
		}, ""},
		{"openai unfinished reasoning", "openai", openAIResponseFixture, func(m map[string]any) { m["output"].([]any)[0].(map[string]any)["status"] = "in_progress" }, "Useful answer."},
		{"openai wrong message role", "openai", openAIResponseFixture, func(m map[string]any) { m["output"].([]any)[1].(map[string]any)["role"] = "user" }, ""},
		{"openai empty text", "openai", openAIResponseFixture, func(m map[string]any) { m["output"] = []any{} }, ""},
		{"claude truncated", "claude", claudeResponseFixture, func(m map[string]any) { m["stop_reason"] = "max_tokens" }, "Useful answer."},
		{"claude tool output", "claude", claudeResponseFixture, func(m map[string]any) {
			m["content"] = append(m["content"].([]any), map[string]any{"type": "tool_use", "input": "private"})
		}, "Useful answer."},
		{"claude unknown status", "claude", claudeResponseFixture, func(m map[string]any) { m["stop_reason"] = "new-status" }, "Useful answer."},
		{"claude stop sequence", "claude", claudeResponseFixture, func(m map[string]any) { m["stop_reason"] = "stop_sequence" }, "Useful answer."},
		{"claude refusal details", "claude", claudeResponseFixture, func(m map[string]any) {
			m["stop_details"] = map[string]any{"type": "refusal", "explanation": "private"}
		}, "Useful answer."},
		{"claude unknown content", "claude", claudeResponseFixture, func(m map[string]any) { m["content"] = []any{map[string]any{"type": "new-type", "text": "private"}} }, ""},
		{"claude wrong role", "claude", claudeResponseFixture, func(m map[string]any) { m["role"] = "user" }, "Useful answer."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := mutatedFixture(t, tt.fixture, tt.mutate)
			p := testHTTPProvider(t, tt.provider, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
			result, err := p.Generate(context.Background(), providerTestRequest(tt.provider))
			if err != nil {
				t.Fatal(err)
			}
			if result.Complete || result.Text != tt.wantText {
				t.Fatalf("unexpected incomplete response: %+v", result)
			}
			if result.Usage.InputTokens != 1000 || result.Usage.OutputTokens != 20 {
				t.Fatal("incomplete usage must remain accounted")
			}
		})
	}
}

func TestHTTPProviderRejectsMissingAndImpossibleUsage(t *testing.T) {
	for _, provider := range []string{"openai", "claude"} {
		fixture := openAIResponseFixture
		if provider == "claude" {
			fixture = claudeResponseFixture
		}
		mutations := []struct {
			name   string
			mutate func(map[string]any)
		}{
			{"missing usage", func(m map[string]any) { delete(m, "usage") }},
			{"missing input", func(m map[string]any) { delete(m["usage"].(map[string]any), "input_tokens") }},
			{"missing output", func(m map[string]any) { delete(m["usage"].(map[string]any), "output_tokens") }},
			{"negative input", func(m map[string]any) { m["usage"].(map[string]any)["input_tokens"] = -1 }},
			{"negative output", func(m map[string]any) { m["usage"].(map[string]any)["output_tokens"] = -1 }},
			{"overflow input", func(m map[string]any) { m["usage"].(map[string]any)["input_tokens"] = 1_000_000_001 }},
		}
		if provider == "openai" {
			mutations = append(mutations, struct {
				name   string
				mutate func(map[string]any)
			}{"cache overlaps total", func(m map[string]any) {
				m["usage"].(map[string]any)["input_tokens_details"].(map[string]any)["cached_tokens"] = 900
			}}, struct {
				name   string
				mutate func(map[string]any)
			}{"missing cache writes", func(m map[string]any) {
				delete(m["usage"].(map[string]any)["input_tokens_details"].(map[string]any), "cache_write_tokens")
			}}, struct {
				name   string
				mutate func(map[string]any)
			}{"reasoning beyond output", func(m map[string]any) {
				m["usage"].(map[string]any)["output_tokens_details"].(map[string]any)["reasoning_tokens"] = 21
			}})
		} else {
			mutations = append(mutations, struct {
				name   string
				mutate func(map[string]any)
			}{"missing cache writes", func(m map[string]any) { delete(m["usage"].(map[string]any), "cache_creation_input_tokens") }}, struct {
				name   string
				mutate func(map[string]any)
			}{"negative cache read", func(m map[string]any) { m["usage"].(map[string]any)["cache_read_input_tokens"] = -1 }}, struct {
				name   string
				mutate func(map[string]any)
			}{"reasoning beyond output", func(m map[string]any) {
				m["usage"].(map[string]any)["output_tokens_details"].(map[string]any)["thinking_tokens"] = 21
			}})
		}
		for _, tt := range mutations {
			t.Run(provider+"/"+tt.name, func(t *testing.T) {
				body := mutatedFixture(t, fixture, tt.mutate)
				p := testHTTPProvider(t, provider, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
				if _, err := p.Generate(context.Background(), providerTestRequest(provider)); err == nil {
					t.Fatal("invalid usage accepted")
				}
			})
		}
	}
}

func TestHTTPProviderScrubsErrorsAndDoesNotRetryOrRedirect(t *testing.T) {
	for _, code := range []int{http.StatusTemporaryRedirect, http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var calls, forwarded atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
			defer target.Close()
			p := testHTTPProvider(t, "openai", func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", target.URL+"/secret")
				w.WriteHeader(code)
				io.WriteString(w, "unit-test-key private prompt secret")
			})
			_, err := p.Generate(context.Background(), providerTestRequest("openai"))
			if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "unit-test-key") || strings.Contains(err.Error(), "private") {
				t.Fatalf("unscrubbed/missing error: %v", err)
			}
			if calls.Load() != 1 || forwarded.Load() != 0 {
				t.Fatal("provider retried or followed redirect")
			}
		})
	}
	p := testHTTPProvider(t, "openai", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"private unit-test-key":"invalid"`) })
	if _, err := p.Generate(context.Background(), providerTestRequest("openai")); err == nil || strings.Contains(err.Error(), "unit-test-key") {
		t.Fatal("JSON error leaked body")
	}
}

func TestHTTPProviderHonorsContextCancellation(t *testing.T) {
	p := testHTTPProvider(t, "openai", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := p.Generate(ctx, providerTestRequest("openai"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline lost: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err = p.CountInputTokens(ctx, providerTestRequest("openai")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestHTTPProviderBoundsResponseBytes(t *testing.T) {
	for _, knownLength := range []bool{true, false} {
		t.Run(fmt.Sprint(knownLength), func(t *testing.T) {
			p := testHTTPProvider(t, "openai", func(w http.ResponseWriter, r *http.Request) {
				if knownLength {
					w.Header().Set("Content-Length", fmt.Sprint(maxProviderResponseBytes+1))
				} else {
					w.(http.Flusher).Flush()
				}
				io.WriteString(w, strings.Repeat("x", maxProviderResponseBytes+1))
			})
			if _, err := p.CountInputTokens(context.Background(), providerTestRequest("openai")); err == nil || !strings.Contains(err.Error(), "byte limit") {
				t.Fatalf("response was not bounded: %v", err)
			}
		})
	}
}
