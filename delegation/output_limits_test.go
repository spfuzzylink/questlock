package delegation

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/inference"
)

func TestLargerOutputAllowanceRequiresExplicitNonstreamOllamaThinking(t *testing.T) {
	yes, no := true, false
	for _, profile := range []struct {
		name    string
		options *inference.OllamaOptions
		stream  bool
		limit   int64
	}{
		{"omitted", nil, false, 4096},
		{"unset", &inference.OllamaOptions{}, false, 4096},
		{"false", &inference.OllamaOptions{Think: &no}, false, 4096},
		{"true", &inference.OllamaOptions{Think: &yes}, false, 8192},
		{"stream without thinking", nil, true, 4096},
		{"stream with thinking", &inference.OllamaOptions{Think: &yes}, true, 0},
	} {
		for _, tokens := range []int64{-1, 15, 16, 4096, 4097, 8192, 8193} {
			t.Run(profile.name+"/"+strconv.FormatInt(tokens, 10), func(t *testing.T) {
				request := testRequest()
				request.Policy.Escalation = nil
				request.Policy.Local.Spec.Ollama = profile.options
				request.Policy.Local.Spec.Stream = profile.stream
				request.Policy.Local.Spec.MaxOutputTokens = tokens
				valid := tokens >= 16 && tokens <= profile.limit
				if err := Validate(request, false); (err == nil) != valid {
					t.Fatalf("explicit local reasoning output allowance %d: %v", tokens, err)
				}
				if !valid {
					factories := 0
					_, err := (Router{Factory: func(Tier) (Runner, error) { factories++; return nil, nil }}).Run(context.Background(), request)
					if err == nil || factories != 0 {
						t.Fatalf("invalid output allowance constructed a provider: %v, %d calls", err, factories)
					}
				}
			})
		}
	}
}

func TestOtherProvidersRetainDelegationOutputLimit(t *testing.T) {
	for _, provider := range []string{"openai", "claude", "anthropic", "sglang", "codex-oss"} {
		for _, think := range []bool{false, true} {
			t.Run(provider+"/think="+strconv.FormatBool(think), func(t *testing.T) {
				request := paidRequest()
				if provider == "sglang" || provider == "codex-oss" {
					request.Policy.Escalation = nil
					request.Policy.Local.Spec.Provider = provider
					request.Policy.Local.Spec.MaxOutputTokens = 4097
					if provider == "sglang" {
						request.Policy.Local.Spec.SGLang = &inference.SGLangConfig{DeploymentID: "fixture", CacheNamespace: "test", Profile: "dense", SelfHosted: true, MaxInputTokens: request.Policy.Local.Spec.MaxInputTokens}
					}
					if think {
						request.Policy.Local.Spec.Ollama = &inference.OllamaOptions{Think: &think}
					}
				} else {
					request.Policy.Escalation.Spec.Provider = provider
					request.Policy.Escalation.Spec.MaxOutputTokens = 4097
					if think {
						request.Policy.Escalation.Spec.Ollama = &inference.OllamaOptions{Think: &think}
					}
				}
				if err := Validate(request, true); err == nil || (provider != "codex-oss" && !strings.Contains(err.Error(), "4096 output tokens")) {
					t.Fatalf("non-Ollama provider acquired a larger output allowance: %v", err)
				}
				factories := 0
				_, err := (Router{AllowPaid: true, Factory: func(Tier) (Runner, error) { factories++; return nil, nil }}).Run(context.Background(), request)
				if err == nil || factories != 0 {
					t.Fatalf("non-Ollama overrun constructed a provider: %v, %d calls", err, factories)
				}
			})
		}
	}
}
