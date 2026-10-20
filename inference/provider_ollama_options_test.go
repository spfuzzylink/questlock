package inference

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func assertOllamaOptionsRejectedBeforeNetwork(t *testing.T, p Provider, req Request, callCount func() int) {
	t.Helper()
	before := callCount()
	temperature := 0.0
	for _, options := range []*OllamaOptions{{}, {Format: "json", Temperature: &temperature}} {
		req.Ollama = options
		count, err := p.CountInputTokens(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), "ollama settings require the ollama provider") || count != 0 {
			t.Fatalf("unsupported local options counted: %d, %v", count, err)
		}
		response, err := p.Generate(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), "ollama settings require the ollama provider") || response.Complete || response.Text != "" {
			t.Fatalf("unsupported local options generated: %+v, %v", response, err)
		}
	}
	if got := callCount(); got != before {
		t.Fatalf("unsupported local options made %d runtime requests", got-before)
	}
}

func TestNonOllamaProvidersRejectLocalGenerationOptionsBeforeNetwork(t *testing.T) {
	for _, provider := range []string{"openai", "claude", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			var calls atomic.Int32
			p := testHTTPProvider(t, provider, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			})
			count := func() int { return int(calls.Load()) }
			req := providerTestRequest(provider)
			assertOllamaOptionsRejectedBeforeNetwork(t, p, req, count)
			// Direct HTTP requests also permit the provider to be unspecified.
			req.Provider = ""
			assertOllamaOptionsRejectedBeforeNetwork(t, p, req, count)
		})
	}
	t.Run("openrouter", func(t *testing.T) {
		var calls atomic.Int32
		p := newOpenRouterFixture(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		})
		assertOllamaOptionsRejectedBeforeNetwork(t, p, openRouterTestRequest(), func() int { return int(calls.Load()) })
	})
	t.Run("sglang", func(t *testing.T) {
		fixture := newSGLangFixture(t, "dense")
		p := fixture.provider(t, "dense")
		// Constructor discovery has already finished. Rejection must precede
		// further metadata reads as well as tokenization and generation.
		assertOllamaOptionsRejectedBeforeNetwork(t, p, sglangTestRequest("dense"), fixture.callCount)
	})
}
