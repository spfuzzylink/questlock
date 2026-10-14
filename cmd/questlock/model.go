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
		_, err := fmt.Fprintln(helpOutput, "Usage: questlock model plan --spec PATH\n       questlock model run --spec PATH [--broker http://127.0.0.1:8080] [--api-base-url URL] [--timeout 2m]\n       questlock model evaluate [--suite PATH] [--live --broker URL] [--report PATH] [--langfuse]\n       questlock model quest\n       questlock model install [--agents codex,claude] [--replace]")
		return err
	}
	if len(args) != 0 && args[0] == "evaluate" {
		return modelEvaluateCommand(args[1:], output, helpOutput)
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
		return errors.New("use model plan, model run, model evaluate, model quest, or model install; use model --help")
	}
	command := args[0]
	f := flag.NewFlagSet("model "+command, flag.ContinueOnError)
	// flag's default errors can echo arbitrary flag values; print only controlled
	// help text and return a static parse error instead.
	f.SetOutput(io.Discard)
	specPath := f.String("spec", "", "path to a UTF-8 JSON task spec (maximum 1MiB)")
	var brokerURL, apiBaseURL *string
	var timeout *time.Duration
	var langfuse *bool
	var langfuseTimeout *time.Duration
	if command == "run" {
		brokerURL = f.String("broker", "http://127.0.0.1:8080", "Questlock broker URL")
		apiBaseURL = f.String("api-base-url", "", "provider API base URL; empty uses its official API or local Ollama on 127.0.0.1:11434")
		timeout = f.Duration("timeout", 2*time.Minute, "positive time limit for the entire run")
		langfuse = f.Bool("langfuse", false, "export metadata to explicitly configured Langfuse after inference")
		langfuseTimeout = f.Duration("langfuse-timeout", 2*time.Second, "bounded timeout per Langfuse request (1ms..5s)")
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
	exporter, err := langfuseFromEnvironment(*langfuse, *langfuseTimeout)
	if err != nil {
		return err
	}
	broker, token, err := modelBroker(*brokerURL)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	setupStarted := time.Now()
	provider, err := configuredProvider(ctx, spec, *apiBaseURL)
	setupEnded := time.Now()
	if err != nil {
		return redactModelError(err, token)
	}
	runner := &preparedRunner{runner: inference.Runner{Broker: broker, Provider: provider}, setupStart: setupStarted, setupEnd: setupEnded}
	result, err := runner.Run(ctx, spec)
	// A known paid answer can be returned even if publication, output, or budget
	// settlement fails. Preserve inspection usage; the command remains nonzero.
	// Callers must check successful exit, response.complete, and durable.
	if result.Fingerprint != "" || result.Measurement != nil {
		if writeErr := writeModelJSON(output, result); writeErr != nil {
			return writeErr
		}
	}
	exportTelemetry(exporter, []telemetryRecord{{execution: observation(spec, result)}}, helpOutput)
	if err != nil {
		return redactModelError(err, token, os.Getenv("OPENAI_API_KEY"), os.Getenv("ANTHROPIC_API_KEY"))
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
