package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/inference"
)

const maxModelSpecBytes = 1 << 20

func modelCommand(args []string) error {
	return modelCommandIO(args, os.Stdout, os.Stderr)
}

func modelCommandIO(args []string, output, helpOutput io.Writer) error {
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		_, err := fmt.Fprintln(helpOutput, "Usage: questlock model plan --spec PATH\n       questlock model run --spec PATH [--broker http://127.0.0.1:8080] [--api-base-url URL] [--timeout 2m]\n       questlock model quest\n       questlock model install [--agents codex,claude] [--replace]")
		return err
	}
	if len(args) != 0 && args[0] == "install" {
		return modelInstallCommand(args[1:], output, helpOutput)
	}
	if len(args) != 0 && args[0] == "quest" {
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			_, err := fmt.Fprintln(helpOutput, "Usage: questlock model quest\nRuns six offline inference scenarios without API credentials.")
			return err
		}
		if len(args) != 1 {
			return errors.New("model quest takes no arguments")
		}
		return modelQuestIO(output)
	}
	if len(args) == 0 || (args[0] != "plan" && args[0] != "run") {
		return errors.New("use model plan, model run, model quest, or model install; use model --help")
	}
	command := args[0]
	f := flag.NewFlagSet("model "+command, flag.ContinueOnError)
	// flag's default errors can echo arbitrary flag values; print only controlled
	// help text and return a static parse error instead.
	f.SetOutput(io.Discard)
	specPath := f.String("spec", "", "path to a UTF-8 JSON task spec (maximum 1MiB)")
	var brokerURL, apiBaseURL *string
	var timeout *time.Duration
	if command == "run" {
		brokerURL = f.String("broker", "http://127.0.0.1:8080", "Questlock broker URL")
		apiBaseURL = f.String("api-base-url", "", "provider API base URL; empty uses its official API or local Ollama on 127.0.0.1:11434")
		timeout = f.Duration("timeout", 2*time.Minute, "positive time limit for the entire run")
	}
	f.Usage = func() {
		f.SetOutput(helpOutput)
		fmt.Fprintf(helpOutput, "Usage: questlock model %s --spec PATH [flags]\n", command)
		f.PrintDefaults()
		f.SetOutput(io.Discard)
	}
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid model flags; use model " + command + " --help")
	}
	if f.NArg() != 0 {
		return errors.New("model commands do not accept positional arguments")
	}
	if strings.TrimSpace(*specPath) == "" {
		return errors.New("--spec is required")
	}
	if timeout != nil && *timeout <= 0 {
		return errors.New("--timeout must be positive")
	}
	spec, err := readModelSpec(*specPath)
	if err != nil {
		return err
	}
	// Validate and show the exact bounded request without opening the broker or
	// reading provider credentials. The plan intentionally contains task input.
	plan, err := inference.BuildPlan(spec)
	if err != nil {
		return fmt.Errorf("invalid model spec: %w", err)
	}
	if command == "plan" {
		return writeModelJSON(output, plan)
	}
	token := os.Getenv("QUESTLOCK_TOKEN")
	if token == "" {
		return errors.New("QUESTLOCK_TOKEN is required for model run")
	}
	var keyName string
	switch spec.Provider {
	case "openai":
		keyName = "OPENAI_API_KEY"
	case "anthropic", "claude":
		keyName = "ANTHROPIC_API_KEY"
	case "ollama":
		// An installed local model uses no provider API credential.
	default:
		return errors.New("model provider must be openai, anthropic, claude, or ollama")
	}
	var apiKey string
	if keyName != "" {
		apiKey = os.Getenv(keyName)
		if apiKey == "" {
			return errors.New(keyName + " is required for model run")
		}
	}
	broker, err := client.New(*brokerURL, token)
	if err != nil {
		return redactModelError(err, token, apiKey)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	var provider inference.Provider
	if spec.Provider == "ollama" {
		provider, err = inference.NewOllamaProviderContext(ctx, *apiBaseURL, spec.Model)
	} else {
		provider, err = inference.NewHTTPProvider(spec.Provider, apiKey, *apiBaseURL)
	}
	if err != nil {
		return redactModelError(err, token, apiKey)
	}
	runner := inference.Runner{Broker: broker, Provider: provider}
	result, err := runner.Run(ctx, spec)
	// A known paid answer can be returned even if publication, output, or budget
	// settlement fails. Preserve inspection usage; the command remains nonzero.
	// Callers must check successful exit, response.complete, and durable.
	if result.Fingerprint != "" {
		if writeErr := writeModelJSON(output, result); writeErr != nil {
			return writeErr
		}
	}
	if err != nil {
		return redactModelError(err, token, apiKey)
	}
	return nil
}

func readModelSpec(path string) (inference.Spec, error) {
	var spec inference.Spec
	f, err := os.Open(path)
	if err != nil {
		return spec, errors.New("could not open model spec")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxModelSpecBytes+1))
	if err != nil {
		return spec, errors.New("could not read model spec")
	}
	if len(data) > maxModelSpecBytes {
		return spec, errors.New("model spec exceeds 1MiB limit")
	}
	if !utf8.Valid(data) {
		return spec, errors.New("model spec must contain valid UTF-8")
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return spec, errors.New("model spec must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		// Do not echo unknown field names or malformed task content.
		return inference.Spec{}, errors.New("model spec has invalid JSON, invalid field types, or unknown fields")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return inference.Spec{}, errors.New("model spec must contain exactly one JSON object")
	}
	return spec, nil
}

func writeModelJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return errors.New("could not write model JSON output")
	}
	return nil
}

func redactModelError(err error, secrets ...string) error {
	// A custom broker can echo request data in an error body. Keep status for
	// diagnosis, but do not put remote messages, codes, or URLs on stderr.
	var brokerError *client.Error
	if errors.As(err, &brokerError) {
		return fmt.Errorf("broker request failed (HTTP %d); inspect broker state before retrying", brokerError.Status)
	}
	var transportError *url.Error
	if errors.As(err, &transportError) {
		if errors.Is(err, context.DeadlineExceeded) {
			return errors.New("broker request timed out; inspect broker state before retrying")
		}
		if errors.Is(err, context.Canceled) {
			return errors.New("broker request interrupted; inspect broker state before retrying")
		}
		return errors.New("broker request failed; inspect broker state before retrying")
	}
	message := err.Error()
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return errors.New(message)
}
