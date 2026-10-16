package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/inference"
)

func modelBroker(baseURL string) (*client.Client, string, error) {
	token := os.Getenv("QUESTLOCK_TOKEN")
	if token == "" && os.Getenv("QUESTLOCK_TOKEN_FILE") != "" {
		f, err := os.Open(os.Getenv("QUESTLOCK_TOKEN_FILE"))
		if err != nil {
			return nil, "", errors.New("could not read QUESTLOCK_TOKEN_FILE")
		}
		data, err := io.ReadAll(io.LimitReader(f, 4097))
		f.Close()
		if err != nil || len(data) > 4096 {
			return nil, "", errors.New("invalid QUESTLOCK_TOKEN_FILE")
		}
		token = strings.TrimSpace(string(data))
	}
	if token == "" {
		return nil, "", errors.New("QUESTLOCK_TOKEN or QUESTLOCK_TOKEN_FILE is required")
	}
	broker, err := client.New(baseURL, token)
	if err != nil {
		return nil, "", redactModelError(err, token)
	}
	return broker, token, nil
}

func configuredProvider(ctx context.Context, spec inference.Spec, baseURL string) (inference.Provider, error) {
	if spec.Provider == "ollama" {
		return inference.NewOllamaProviderContext(ctx, baseURL, spec.Model)
	}
	if spec.Provider == "sglang" {
		if spec.SGLang == nil {
			return nil, errors.New("sglang configuration is required")
		}
		key := os.Getenv("SGLANG_API_KEY")
		p, err := inference.NewSGLangProviderContext(ctx, key, baseURL, spec.Model, *spec.SGLang)
		return p, redactModelErrorIfPresent(err, key)
	}
	keyName := "OPENAI_API_KEY"
	if spec.Provider == "anthropic" || spec.Provider == "claude" {
		keyName = "ANTHROPIC_API_KEY"
	}
	if spec.Provider == "openrouter" {
		keyName = "OPENROUTER_API_KEY"
	}
	key := os.Getenv(keyName)
	if key == "" {
		return nil, errors.New(keyName + " is required")
	}
	if spec.Provider == "openrouter" {
		if spec.OpenRouter == nil {
			return nil, errors.New("openrouter configuration is required")
		}
		p, err := inference.NewOpenRouterProvider(key, baseURL, *spec.OpenRouter)
		return p, redactModelErrorIfPresent(err, key)
	}
	p, err := inference.NewHTTPProvider(spec.Provider, key, baseURL)
	return p, redactModelErrorIfPresent(err, key)
}

func redactModelErrorIfPresent(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	return redactModelError(err, secrets...)
}

type preparedRunner struct {
	runner               inference.Runner
	setupStart, setupEnd time.Time
	setupClaimed         atomic.Bool
}

func (p *preparedRunner) Run(ctx context.Context, spec inference.Spec) (inference.Result, error) {
	result, err := p.runner.Run(ctx, spec)
	if result.Measurement != nil && p.setupClaimed.CompareAndSwap(false, true) {
		result.Measurement.ProviderSetupStartedAt = p.setupStart.UTC()
		result.Measurement.ProviderSetupEndedAt = p.setupEnd.UTC()
		result.Measurement.ProviderSetupMS = float64(p.setupEnd.Sub(p.setupStart)) / float64(time.Millisecond)
	}
	return result, err
}
