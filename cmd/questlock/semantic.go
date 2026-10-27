package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spfuzzylink/questlock/internal/semanticquest"
	"github.com/spfuzzylink/questlock/semantic"
)

func semanticCommand(args []string) error { return semanticCommandIO(args, os.Stdout, os.Stderr) }

func semanticCommandIO(args []string, output, diagnostics io.Writer) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "help")) {
		_, err := io.WriteString(diagnostics, "Usage: questlock semantic init [--model all-minilm] [--ollama-url URL] [--timeout 5m]\n       questlock semantic quest [--live --embedding-model all-minilm --ollama-url URL]\nInit explicitly downloads a missing local embedding model and checks it. Shadow never serves an alternative answer.\n")
		return err
	}
	if args[0] != "init" && args[0] != "quest" {
		return errors.New("use semantic init or semantic quest; use semantic --help")
	}
	f := flag.NewFlagSet("semantic "+args[0], flag.ContinueOnError)
	f.SetOutput(diagnostics)
	base := f.String("ollama-url", "http://127.0.0.1:11434", "literal-loopback Ollama runtime")
	timeout := f.Duration("timeout", 5*time.Minute, "explicit setup/qualification deadline, at most 10m")
	model := f.String("model", "all-minilm", "embedding model for explicit init")
	embeddingModel := f.String("embedding-model", "all-minilm", "installed embedding model for a live quest")
	live := f.Bool("live", false, "real local embeddings; generation remains a deterministic fixture")
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid semantic flags")
	}
	if f.NArg() != 0 || *timeout <= 0 || *timeout > 10*time.Minute {
		return errors.New("semantic accepts flags only; timeout must be positive and at most 10m")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if args[0] == "quest" {
		var embedder semantic.Embedder
		if *live {
			p, err := semantic.NewOllamaEmbedderContext(ctx, *base, *embeddingModel, 8192)
			if err != nil {
				return err
			}
			defer p.Close()
			embedder = p
		}
		report, err := semanticquest.Run(ctx, embedder)
		if writeErr := writeModelJSON(output, report); writeErr != nil {
			return writeErr
		}
		return err
	}
	config, err := semantic.NormalizeConfig(semantic.Config{Mode: "shadow", Family: "approved-task-family", Domain: "approved-domain", EvidenceRevision: "approved-evidence-1", EmbeddingModel: *model})
	if err != nil {
		return err
	}
	if err = ensureSemanticModel(ctx, *base, *model); err != nil {
		return err
	}
	p, err := semantic.NewOllamaEmbedderContext(ctx, *base, *model, config.MaxInputBytes)
	if err != nil {
		return err
	}
	defer p.Close()
	e, err := p.Embed(ctx, "Local embedding readiness check.")
	if err != nil {
		return err
	}
	return writeModelJSON(output, struct {
		Status              string          `json:"status"`
		Config              semantic.Config `json:"semantic_shadow"`
		ModelID             string          `json:"embedding_model_id"`
		Dimensions          int             `json:"dimensions"`
		InputTokens         *int64          `json:"embedding_input_tokens"`
		PaidFallback        bool            `json:"paid_fallback_enabled"`
		ClientConfigChanged bool            `json:"client_config_changed"`
	}{"local_embedding_ready", config, e.ModelID, len(e.Vector), e.InputTokens, false, false})
}

// Setup alone may pull an absent model. Existing installed weights are not
// replaced; workers never pull models. Plaintext is limited to literal loopback.
func ensureSemanticModel(ctx context.Context, base, model string) error {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		return errors.New("embedding setup requires a literal-loopback HTTP Ollama URL without credentials, path, query or fragment")
	}
	t := &http.Transport{Proxy: nil}
	defer t.CloseIdleConnections()
	c := &http.Client{Transport: t, Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(path string, body []byte) ([]byte, error) {
		method := http.MethodGet
		if body != nil {
			method = http.MethodPost
		}
		r, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, bytes.NewReader(body))
		if err != nil {
			return nil, errors.New("embedding setup request failed")
		}
		r.Header.Set("Content-Type", "application/json")
		response, err := c.Do(r)
		if err != nil {
			return nil, errors.New("local embedding setup runtime unavailable")
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		if err != nil || len(raw) > 2<<20 || response.StatusCode != 200 {
			return nil, errors.New("local embedding setup request was not confirmed")
		}
		return raw, nil
	}
	raw, err := do("/api/tags", nil)
	if err != nil {
		return err
	}
	var tags struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if json.Unmarshal(raw, &tags) != nil || tags.Models == nil {
		return errors.New("local embedding model inventory is invalid")
	}
	normalize := func(name string) string {
		if !strings.Contains(name, ":") {
			return name + ":latest"
		}
		return name
	}
	for _, m := range tags.Models {
		if m.Name == "" && m.Model == "" {
			return errors.New("local embedding model inventory is invalid")
		}
		if normalize(m.Name) == normalize(model) || normalize(m.Model) == normalize(model) {
			return nil
		}
	}
	body, _ := json.Marshal(map[string]any{"model": model, "stream": false})
	raw, err = do("/api/pull", body)
	if err != nil {
		return err
	}
	var pulled struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if json.Unmarshal(raw, &pulled) != nil || pulled.Status != "success" || pulled.Error != "" {
		return errors.New("local embedding model download was not confirmed")
	}
	return nil
}
