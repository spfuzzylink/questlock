package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const ollamaEmptyChunk = `{"model":"qwen3:4b","response":"","done":false}`
const ollamaContentChunk = `{"model":"qwen3:4b","response":"Useful ","done":false}`
const ollamaLastContentChunk = `{"model":"qwen3:4b","response":"local answer.","done":false}`
const ollamaTerminalChunk = `{"model":"qwen3:4b","created_at":"2026-10-14T22:00:00Z","response":"","done":true,"done_reason":"stop","prompt_eval_count":100,"prompt_eval_cached_count":40,"eval_count":20}`

func ollamaStreamProvider(t *testing.T, generation http.HandlerFunc, changed *atomic.Bool) (*OllamaProvider, *ollamaTestCalls) {
	t.Helper()
	calls := &ollamaTestCalls{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" {
			t.Error("stream request sent credentials")
		}
		switch r.URL.Path {
		case "/api/tags":
			calls.tags.Add(1)
			body := ollamaTagsFixture
			if changed != nil && changed.Load() {
				body = strings.ReplaceAll(body, ollamaDigest, strings.Repeat("b", 64))
			}
			io.WriteString(w, body)
		case "/api/show":
			calls.show.Add(1)
			io.WriteString(w, ollamaShowFixture)
		case "/api/generate":
			calls.generate.Add(1)
			generation(w, r)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	p, err := NewOllamaProvider(srv.URL, "qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.client.CloseIdleConnections)
	return p, calls
}

func streamRequest() Request {
	r := ollamaTestRequest()
	r.Stream = true
	return r
}

func writeStreamFrame(w http.ResponseWriter, body string) {
	fmt.Fprintln(w, body)
	w.(http.Flusher).Flush()
}

func TestOllamaStreamingMeasuresFirstAndLastContentNotEmptyFrames(t *testing.T) {
	p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil || request["stream"] != true || request["think"] != false || request["model"] != "qwen3:4b" || r.Header.Get("Accept") != "application/x-ndjson" {
			t.Error("stream opt-in/local policy missing")
		}
		options := request["options"].(map[string]any)
		if options["num_predict"] != float64(64) || options["num_ctx"] != float64(1024) {
			t.Error("stream request lost generation/context caps")
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		writeStreamFrame(w, ollamaEmptyChunk)
		time.Sleep(40 * time.Millisecond)
		writeStreamFrame(w, ollamaContentChunk)
		time.Sleep(40 * time.Millisecond)
		writeStreamFrame(w, ollamaLastContentChunk)
		time.Sleep(50 * time.Millisecond)
		writeStreamFrame(w, ollamaTerminalChunk)
	}, nil)
	started := time.Now()
	got, err := p.Generate(context.Background(), streamRequest())
	elapsed := float64(time.Since(started)) / float64(time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Complete || got.Text != "Useful local answer." || got.Usage.InputTokens != 100 || got.Usage.UncachedInputTokens != 60 || got.Usage.CacheReadTokens != 40 || got.Usage.OutputTokens != 20 {
		t.Fatalf("stream aggregate/usage invalid: %+v", got)
	}
	if got.Timing == nil || !got.Timing.Streaming || got.Timing.Basis != "client_observed_content_chunks" || got.Timing.FirstTokenMS == nil || got.Timing.OutputMS == nil {
		t.Fatalf("stream timing missing: %+v", got.Timing)
	}
	if *got.Timing.FirstTokenMS < 30 || *got.Timing.OutputMS < 30 || *got.Timing.FirstTokenMS+*got.Timing.OutputMS > elapsed-25 {
		t.Fatalf("empty/final chunks changed content timing: first=%f output=%f total=%f", *got.Timing.FirstTokenMS, *got.Timing.OutputMS, elapsed)
	}
	if calls.generate.Load() != 1 || calls.tags.Load() != 3 || calls.show.Load() != 3 {
		t.Fatalf("expected constructor+before/after inspection, one generation: %+v", calls)
	}
}

func TestOllamaSingleChunkHasZeroOutputIntervalAndAbsentCacheIsAllowed(t *testing.T) {
	terminal := mutatedFixture(t, ollamaTerminalChunk, func(m map[string]any) {
		m["response"] = "One chunk."
		delete(m, "prompt_eval_cached_count")
	})
	p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		// A terminal frame can carry the sole content chunk. EOF without a final
		// newline is accepted without inventing a second content arrival.
		io.WriteString(w, terminal)
	}, nil)
	got, err := p.Generate(context.Background(), streamRequest())
	if err != nil || !got.Complete || got.Text != "One chunk." || got.Timing.FirstTokenMS == nil || got.Timing.OutputMS == nil || *got.Timing.OutputMS != 0 || got.Usage.UncachedInputTokens != 100 || calls.generate.Load() != 1 {
		t.Fatalf("single final chunk failed: %+v %v", got, err)
	}
}

func TestOllamaInvalidStreamNeverApprovesPartialAnswerOrUsage(t *testing.T) {
	for _, c := range []struct {
		name, stream, contentType string
	}{
		{"missing_final", ollamaContentChunk + "\n", "application/x-ndjson"},
		{"missing_done", ollamaContentChunk + "\n" + `{"model":"qwen3:4b","response":"later"}` + "\n", "application/x-ndjson"},
		{"missing_usage", ollamaContentChunk + "\n" + `{"model":"qwen3:4b","done":true,"done_reason":"stop"}` + "\n", "application/x-ndjson"},
		{"negative_usage", ollamaContentChunk + "\n" + strings.Replace(ollamaTerminalChunk, `"eval_count":20`, `"eval_count":-1`, 1) + "\n", "application/x-ndjson"},
		{"cache_overrun", ollamaContentChunk + "\n" + strings.Replace(ollamaTerminalChunk, `"prompt_eval_cached_count":40`, `"prompt_eval_cached_count":101`, 1) + "\n", "application/x-ndjson"},
		{"malformed", ollamaContentChunk + "\n{private-malformed-marker}\n", "application/x-ndjson"},
		{"invalid_utf8", ollamaContentChunk + "\n" + string([]byte{'{', 0xff, '}'}) + "\n", "application/x-ndjson"},
		{"raw_error", ollamaContentChunk + "\n" + `{"done":true,"error":"private-upstream-marker"}` + "\n", "application/x-ndjson"},
		{"model_drift", ollamaContentChunk + "\n" + strings.Replace(ollamaTerminalChunk, "qwen3:4b", "different:4b", 1) + "\n", "application/x-ndjson"},
		{"thinking", ollamaContentChunk + "\n" + `{"model":"qwen3:4b","done":false,"thinking":"private-thought-marker"}` + "\n", "application/x-ndjson"},
		{"tools", ollamaContentChunk + "\n" + `{"model":"qwen3:4b","done":false,"tool_calls":[{"name":"private-tool-marker"}]}` + "\n", "application/x-ndjson"},
		{"trailing_content", ollamaContentChunk + "\n" + ollamaTerminalChunk + "\n" + ollamaLastContentChunk + "\n", "application/x-ndjson"},
		{"double_terminal", ollamaContentChunk + "\n" + ollamaTerminalChunk + "\n" + ollamaTerminalChunk + "\n", "application/x-ndjson"},
		{"blank_frame", ollamaContentChunk + "\n\n" + ollamaTerminalChunk + "\n", "application/x-ndjson"},
		{"wrong_type", ollamaContentChunk + "\n" + ollamaTerminalChunk + "\n", "application/json"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", c.contentType)
				io.WriteString(w, c.stream)
			}, nil)
			got, err := p.Generate(context.Background(), streamRequest())
			if err == nil || got.Text != "" || got.Usage != (Usage{}) || got.Complete || calls.generate.Load() != 1 || got.Timing == nil || got.Timing.OutputMS != nil {
				t.Fatalf("partial stream approved or retried: %+v %v", got, err)
			}
			if strings.Contains(err.Error(), "private-") {
				t.Fatalf("stream content leaked in error: %v", err)
			}
			if c.contentType == "application/x-ndjson" && got.Timing.FirstTokenMS == nil {
				t.Fatal("observed first content lost on later uncertainty")
			}
		})
	}
}

func TestOllamaStreamRetainsValidFinalIncompleteAndPolicyOverrunUsage(t *testing.T) {
	for _, reason := range []string{"length", "new_reason", "overrun", "empty"} {
		t.Run(reason, func(t *testing.T) {
			terminal := mutatedFixture(t, ollamaTerminalChunk, func(m map[string]any) {
				if reason == "overrun" {
					m["eval_count"] = 65
				} else if reason != "empty" {
					m["done_reason"] = reason
				}
			})
			p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/x-ndjson")
				if reason != "empty" {
					writeStreamFrame(w, ollamaContentChunk)
				}
				writeStreamFrame(w, terminal)
			}, nil)
			got, err := p.Generate(context.Background(), streamRequest())
			if err != nil || got.Complete || got.Usage.InputTokens != 100 || calls.generate.Load() != 1 {
				t.Fatalf("known final usage lost: %+v %v", got, err)
			}
			if reason == "empty" && (got.Timing.FirstTokenMS != nil || got.Timing.OutputMS != nil) {
				t.Fatal("empty generation fabricated first/output content timings")
			}
			if reason == "overrun" && (got.StopReason != "usage_limit_exceeded" || got.Usage.OutputTokens != 65) {
				t.Fatal("stream policy overrun lost")
			}
		})
	}
}

func TestOllamaStreamByteAndFrameCaps(t *testing.T) {
	for _, mode := range []string{"declared_body", "long_frame", "total_body", "many_frames"} {
		t.Run(mode, func(t *testing.T) {
			p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/x-ndjson")
				switch mode {
				case "declared_body":
					w.Header().Set("Content-Length", fmt.Sprint(maxProviderResponseBytes+1))
					writeStreamFrame(w, ollamaContentChunk)
				case "long_frame":
					io.WriteString(w, `{"model":"qwen3:4b","response":"`+strings.Repeat("x", maxOllamaStreamFrameBytes)+`","done":false}`+"\n")
				case "total_body":
					frame := `{"model":"qwen3:4b","response":"` + strings.Repeat("x", 20000) + `","done":false}` + "\n"
					for i := 0; i < maxProviderResponseBytes/len(frame)+2; i++ {
						if _, err := io.WriteString(w, frame); err != nil {
							return
						}
					}
				case "many_frames":
					for i := 0; i <= maxOllamaStreamFrames; i++ {
						if _, err := io.WriteString(w, ollamaEmptyChunk+"\n"); err != nil {
							return
						}
					}
				}
			}, nil)
			got, err := p.Generate(context.Background(), streamRequest())
			if err == nil || got.Text != "" || got.Usage != (Usage{}) || got.Timing.OutputMS != nil || calls.generate.Load() != 1 {
				t.Fatalf("unbounded stream accepted or regenerated: %+v %v", got, err)
			}
		})
	}
}

func TestOllamaStreamCancellationPreservesOnlyObservedTiming(t *testing.T) {
	ready := make(chan struct{})
	p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/x-ndjson")
		writeStreamFrame(w, ollamaContentChunk)
		close(ready)
		<-r.Context().Done()
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type generated struct {
		result Response
		err    error
	}
	done := make(chan generated, 1)
	go func() {
		result, err := p.Generate(ctx, streamRequest())
		done <- generated{result, err}
	}()
	<-ready
	// Allow the client to observe the flushed first content before cancellation.
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) || got.result.Text != "" || got.result.Usage != (Usage{}) || got.result.Timing == nil || got.result.Timing.FirstTokenMS == nil || got.result.Timing.OutputMS != nil || calls.generate.Load() != 1 {
			t.Fatalf("cancelled stream approved/retried/lost first arrival: %+v %v", got.result, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream reader ignored cancellation")
	}
}

func TestOllamaStreamDigestChangedDuringGenerationNeverApprovesOutput(t *testing.T) {
	var changed atomic.Bool
	p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		writeStreamFrame(w, ollamaContentChunk)
		changed.Store(true)
		writeStreamFrame(w, ollamaTerminalChunk)
	}, &changed)
	got, err := p.Generate(context.Background(), streamRequest())
	if err == nil || !strings.Contains(err.Error(), "digest changed") || got.Text != "" || got.Usage != (Usage{}) || got.Timing.FirstTokenMS == nil || got.Timing.OutputMS != nil || calls.generate.Load() != 1 {
		t.Fatalf("stream accepted old snapshot or retried: %+v %v", got, err)
	}
}

func TestOllamaLegacyTimingMetadataCannotChangeNonstreamParsing(t *testing.T) {
	legacy := mutatedFixture(t, ollamaGenerateFixture, func(m map[string]any) {
		m["total_duration"] = "invalid-optional-duration"
		m["load_duration"] = -100
		m["eval_duration"] = map[string]any{"unexpected": "shape"}
		m["prompt_eval_duration"] = nil
	})
	p, calls := ollamaFixtureProvider(t, legacy)
	got, err := p.Generate(context.Background(), ollamaTestRequest())
	if err != nil || !got.Complete || got.Timing != nil || got.Text != "Useful local answer." || got.Usage.InputTokens != 100 || calls.generate.Load() != 1 {
		t.Fatalf("optional legacy timing metadata broke inference: %+v %v", got, err)
	}
}

func TestRunnerLostOllamaStreamKeepsClaimAndDoesNotGenerateAgain(t *testing.T) {
	p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		writeStreamFrame(w, ollamaContentChunk)
		// The runtime closes before terminal usage arrives. The observed chunk
		// establishes first-arrival timing, never an approved/reusable answer.
	}, nil)
	r, _ := testRunner(t)
	r.Provider = p
	spec := testSpec()
	spec.Provider, spec.Model, spec.Stream = "ollama", "qwen3:4b", true
	first, err := r.Run(context.Background(), spec)
	if err == nil || first.Measurement == nil || first.Measurement.Outcome != "uncertain" || first.Measurement.KnownGenerationUsage || first.Measurement.GenerationTiming == nil || first.Measurement.GenerationTiming.FirstTokenMS == nil || first.Measurement.GenerationTiming.OutputMS != nil || first.Response.Text != "" {
		t.Fatalf("lost stream approved or observation lost: %+v %v", first, err)
	}
	second, err := r.Run(context.Background(), spec)
	if !errors.Is(err, ErrInFlight) || calls.generate.Load() != 1 || second.Measurement.GenerationCalled || second.Measurement.GenerationTiming != nil {
		t.Fatalf("pending local stream regenerated or invented timing: %+v %v", second, err)
	}
}

func TestRunnerReusedOllamaStreamSeparatesHistoricalAndCurrentTiming(t *testing.T) {
	p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		writeStreamFrame(w, ollamaContentChunk)
		writeStreamFrame(w, ollamaTerminalChunk)
	}, nil)
	r, _ := testRunner(t)
	r.Provider = p
	spec := testSpec()
	spec.Provider, spec.Model, spec.Stream = "ollama", "qwen3:4b", true
	first, err := r.Run(context.Background(), spec)
	if err != nil || first.GenerationTiming == nil || first.Response.Timing == nil || first.Measurement.GenerationTiming == nil {
		t.Fatalf("fresh stream timing lost: %+v %v", first, err)
	}
	second, err := r.Run(context.Background(), spec)
	if err != nil || !second.Reused || second.ThisRunUsage != (Usage{}) || second.Response.Timing != nil || second.Measurement.GenerationTiming != nil || second.Measurement.GenerationCalled || second.GenerationTiming == nil || second.GenerationTiming.FirstTokenMS == nil || calls.generate.Load() != 1 {
		t.Fatalf("reuse fabricated fresh timing or lost historic timing: %+v %v", second, err)
	}
}
