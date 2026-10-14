package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/observability"
	"github.com/spfuzzylink/questlock/worker"
)

func workerCommand(args []string) error { return workerCommandIO(args, os.Stdin, os.Stdout, os.Stderr) }

func workerCommandIO(args []string, input io.ReadCloser, output, diagnostics io.Writer) error {
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(diagnostics, "Usage: questlock worker run --request PATH [--broker URL] [--allow-paid] [--langfuse]\n       questlock worker serve --workspace ROOT [--broker URL] [--allow-paid] [--langfuse]")
		return err
	}
	if len(args) == 0 || (args[0] != "run" && args[0] != "serve") {
		return errors.New("use worker run or worker serve; use worker --help")
	}
	command := args[0]
	f := flag.NewFlagSet("worker "+command, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	requestPath := f.String("request", "", "explicit approved request JSON for worker run")
	workspace := f.String("workspace", ".", "MCP request file boundary; server reads only explicitly named files")
	brokerURL := f.String("broker", "http://127.0.0.1:8080", "broker endpoint for this worker's trusted scope")
	ollamaURL := f.String("ollama-url", "", "local literal-loopback Ollama endpoint; default 127.0.0.1:11434")
	allowPaid := f.Bool("allow-paid", false, "permit one explicitly budgeted hosted escalation after known quality failure")
	timeout := f.Duration("timeout", 2*time.Minute, "per-task bound, maximum 10 minutes")
	lfEnabled := f.Bool("langfuse", false, "opt-in content-free metadata; MCP uses a bounded best-effort queue")
	lfTimeout := f.Duration("langfuse-timeout", 2*time.Second, "bounded timeout per Langfuse request")
	f.Usage = func() {
		fmt.Fprintf(diagnostics, "Usage: questlock worker %s [flags]\n", command)
		f.SetOutput(diagnostics)
		f.PrintDefaults()
		f.SetOutput(io.Discard)
	}
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid worker flags; use worker --help")
	}
	if f.NArg() != 0 || *timeout <= 0 || *timeout > 10*time.Minute {
		return errors.New("worker accepts flags only and timeout must be positive and at most 10 minutes")
	}
	if command == "run" && *requestPath == "" {
		return errors.New("--request is required for worker run")
	}
	if command == "serve" && *requestPath != "" {
		return errors.New("worker serve gets approved request paths from MCP tools")
	}
	exporter, err := langfuseFromEnvironment(*lfEnabled, *lfTimeout)
	if err != nil {
		return err
	}
	broker, _, err := modelBroker(*brokerURL)
	if err != nil {
		return err
	}
	process, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	run := func(ctx context.Context, request delegation.Request) (delegation.Result, []telemetryRecord, error) {
		if err := delegation.Validate(request, *allowPaid); err != nil {
			return delegation.Result{}, nil, err
		}
		var outcomeRunID string
		if request.TrackOutcome {
			var err error
			outcomeRunID, err = newOutcomeRunID()
			if err != nil {
				return delegation.Result{}, nil, errors.New("could not allocate outcome run identity; no model call was made")
			}
		}
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		var records []telemetryRecord
		factory := func(tier delegation.Tier) (delegation.Runner, error) {
			base := ""
			if tier.Spec.Provider == "ollama" {
				base = *ollamaURL
			}
			started := time.Now()
			provider, err := configuredProvider(ctx, tier.Spec, base)
			ended := time.Now()
			if err != nil {
				return nil, err
			}
			var target *[]telemetryRecord
			if exporter != nil {
				target = &records
			}
			return collectedRunner{inner: &preparedRunner{runner: inference.Runner{Broker: broker, Provider: provider}, setupStart: started, setupEnd: ended}, checks: request.Checks, records: target}, nil
		}
		router := delegation.Router{Factory: factory, AllowPaid: *allowPaid}
		result, err := router.Run(ctx, request)
		if request.TrackOutcome {
			recordWorkerOutcome(ctx, broker, request, &result, outcomeRunID)
		}
		return result, records, err
	}
	if command == "run" {
		file, err := os.Open(*requestPath)
		if err != nil {
			return errors.New("could not open approved worker request")
		}
		request, err := delegation.Load(file)
		file.Close()
		if err != nil {
			return err
		}
		result, records, runErr := run(process, request)
		if result.Status != "" {
			if err := writeModelJSON(output, result); err != nil {
				return err
			}
		}
		exportTelemetry(exporter, records, diagnostics)
		if result.OutcomeTracking != nil && result.OutcomeTracking.Status != "recorded" {
			return errors.New("outcome registration is unconfirmed; recover outcome_tracking.record without regenerating the model answer")
		}
		if runErr != nil {
			return errors.New("worker did not produce an approved answer; inspect status and retained broker records before retrying")
		}
		return nil
	}
	queue := newWorkerTelemetryQueue(exporter, diagnostics)
	defer queue.close()
	execute := func(ctx context.Context, request delegation.Request) (delegation.Result, error) {
		result, records, err := run(ctx, request)
		queue.submit(records)
		return result, err
	}
	return worker.Serve(process, input, output, worker.Options{Workspace: *workspace, Version: version, AllowPaid: *allowPaid, Execute: execute})
}

// One background exporter handles only bounded content-free records. Saturated
// queues drop telemetry instead of delaying MCP work. Shutdown has a time bound;
// no retry or durable-export guarantee is implied. Background telemetry writes
// no diagnostics: an unread stderr pipe cannot delay worker shutdown.
type workerTelemetryQueue struct {
	records chan []telemetryRecord
	done    chan struct{}
	cancel  context.CancelFunc
}

func newWorkerTelemetryQueue(exporter *observability.Langfuse, _ io.Writer) *workerTelemetryQueue {
	if exporter == nil {
		return &workerTelemetryQueue{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := &workerTelemetryQueue{records: make(chan []telemetryRecord, 16), done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(q.done)
		for {
			select {
			case <-ctx.Done():
				return
			case records, ok := <-q.records:
				if !ok {
					return
				}
				exportTelemetryContext(ctx, exporter, records, io.Discard)
			}
		}
	}()
	return q
}
func (q *workerTelemetryQueue) submit(records []telemetryRecord) {
	if q.records == nil || len(records) == 0 {
		return
	}
	select {
	case q.records <- records:
	default:
	}
}
func (q *workerTelemetryQueue) close() {
	if q.records == nil {
		return
	}
	close(q.records)
	select {
	case <-q.done:
		q.cancel()
		return
	case <-time.After(5 * time.Second):
		q.cancel()
	}
	// HTTP exports honor cancellation; wait for the writer to stop before its
	// owner can reuse the diagnostics sink.
	<-q.done
}
