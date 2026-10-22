// Run against your own broker and installed local model. See README.md.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/execution"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/observability"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	brokerURL := flag.String("broker", "http://127.0.0.1:8080", "Questlock broker URL; HTTPS across machines")
	tokenFile := flag.String("token-file", os.Getenv("QUESTLOCK_TOKEN_FILE"), "file containing this worker's scoped broker token")
	caFile := flag.String("ca-file", "", "optional private broker CA PEM")
	specFile := flag.String("spec", "", "approved task snapshot JSON; provider and model are read unchanged")
	admissionFile := flag.String("admission", "", "optional complete local resource-admission JSON")
	endpoint := flag.String("endpoint", "", "runtime origin; defaults to local Ollama or SGLang")
	langfuseURL := flag.String("langfuse", "", "optional full Langfuse OTLP traces URL")
	flag.Parse()
	if flag.NArg() != 0 || *tokenFile == "" || *specFile == "" {
		return errors.New("set -token-file and -spec; see examples/execution/README.md")
	}
	task, err := readBounded(*specFile, execution.MaxSpecBytes)
	if err != nil {
		return err
	}
	spec, err := execution.LoadSpec(bytes.NewReader(task))
	if err != nil {
		return err
	}
	if spec.Provider != "ollama" && spec.Provider != "sglang" {
		return errors.New("this example accepts only an ollama or self-hosted sglang spec; it has no hosted fallback")
	}
	if spec.SGLang != nil && spec.SGLang.Profile == "hisparse" {
		return errors.New("this example needs a standalone dense or hicache SGLang deployment")
	}
	policy := admission.DefaultConfig()
	if *admissionFile != "" {
		data, readErr := readBounded(*admissionFile, 64<<10)
		if readErr != nil {
			return readErr
		}
		policy, err = admission.LoadConfig(bytes.NewReader(data))
		if err != nil {
			return err
		}
	}
	token, err := readBounded(*tokenFile, 4096)
	if err != nil {
		return err
	}
	var broker *client.Client
	if *caFile != "" {
		root, readErr := readBounded(*caFile, 1<<20)
		if readErr != nil {
			return readErr
		}
		broker, err = client.NewWithTLSRoots(*brokerURL, strings.TrimSpace(string(token)), root)
	} else {
		broker, err = client.New(*brokerURL, strings.TrimSpace(string(token)))
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var provider inference.Provider
	if spec.Provider == "ollama" {
		provider, err = inference.NewOllamaProviderContext(ctx, *endpoint, spec.Model)
	} else {
		provider, err = inference.NewSGLangProviderContext(ctx, os.Getenv("SGLANG_API_KEY"), *endpoint, spec.Model, *spec.SGLang)
	}
	if err != nil {
		return err
	}
	var exporter observability.Exporter
	if *langfuseURL != "" {
		exporter, err = observability.NewLangfuse(observability.Config{Endpoint: *langfuseURL,
			PublicKey: os.Getenv("LANGFUSE_PUBLIC_KEY"), SecretKey: os.Getenv("LANGFUSE_SECRET_KEY")})
		if err != nil {
			return err
		}
	}
	// Adjust the inference lane's estimates for your measured workload. One shared
	// coordinator bounds this process; the serving runtime still owns its devices.
	coordinator, err := execution.New(ctx, execution.Config{Runner: inference.Runner{Broker: broker, Provider: provider},
		Admission: policy, Exporter: exporter})
	if err != nil {
		return err
	}
	defer func() {
		shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = coordinator.Close(shutdown)
	}()
	// Wait only for initial resource samples. This makes no inference attempt.
	if err := initialReadiness(ctx, coordinator); err != nil {
		return err
	}
	for _, label := range []string{"first", "same snapshot"} {
		result, err := coordinator.Run(ctx, spec)
		if err != nil {
			if errors.Is(err, admission.ErrPaused) {
				return fmt.Errorf("local admission paused (%s); inspect resources before a deliberate submission", coordinator.Status().Reason)
			}
			if errors.Is(err, inference.ErrInFlight) {
				return errors.New("this snapshot is pending or uncertain; inspect broker state before changing revision")
			}
			return err
		}
		fmt.Printf("%s: reused=%v durable=%v new_input=%d new_output=%d telemetry=%s\n",
			label, result.Reused, result.Durable, result.ThisRunUsage.InputTokens, result.ThisRunUsage.OutputTokens, result.Telemetry.Status)
		fmt.Println(result.Response.Text)
		if result.Telemetry.Error != nil {
			fmt.Fprintln(os.Stderr, "telemetry was not confirmed; the task result is retained")
		}
	}
	return nil
}

func initialReadiness(ctx context.Context, c *execution.Coordinator) error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		s := c.Status()
		if s.Reason == "ready" && s.ExpensiveWorkerLimit > 0 {
			return nil
		}
		if !s.Snapshot.At.IsZero() && s.Reason != "recovering" {
			return fmt.Errorf("local admission paused: %s", s.Reason)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("initial resource samples were not ready within five seconds")
		case <-tick.C:
		}
	}
}

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(b) == 0 || int64(len(b)) > limit {
		return nil, errors.New("example configuration file is empty, unreadable, or too large")
	}
	return b, nil
}
