package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
)

type collectedRunner struct {
	inner   *preparedRunner
	checks  []evaluation.Check
	records *[]telemetryRecord
}

func (r collectedRunner) Run(ctx context.Context, spec inference.Spec) (inference.Result, error) {
	result, err := r.inner.Run(ctx, spec)
	if r.records != nil && result.Measurement != nil {
		record := telemetryRecord{execution: observation(spec, result)}
		if err == nil && result.Response.Complete && result.Durable && result.Response.Text != "" {
			_, pass := evaluation.CheckOutput(result.Response.Text, r.checks)
			record.quality = &pass
		}
		*r.records = append(*r.records, record)
	}
	return result, err
}

type evaluationEnvironment struct {
	OS                string            `json:"os"`
	Architecture      string            `json:"architecture"`
	CPUs              int               `json:"cpus"`
	GoVersion         string            `json:"go_version"`
	LocalModelDigests map[string]string `json:"local_model_digests,omitempty"`
	LoadState         string            `json:"load_state"`
}

func modelEvaluateCommand(args []string, output, diagnostics io.Writer) error {
	f := flag.NewFlagSet("model evaluate", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	suitePath := f.String("suite", "", "optional approved JSON suite; default is six public scenarios")
	reportPath := f.String("report", "", "create a private report file; refuses existing paths")
	live := f.Bool("live", false, "run explicitly configured models; default uses simulated fixtures")
	brokerURL := f.String("broker", "http://127.0.0.1:8080", "broker in an explicitly fresh evaluation scope")
	apiBaseURL := f.String("api-base-url", "", "explicit provider base URL; default uses official APIs/local Ollama")
	timeout := f.Duration("timeout", 10*time.Minute, "positive bound for the complete evaluation")
	langfuse := f.Bool("langfuse", false, "opt-in metadata and quality scores after live evaluation")
	lfTimeout := f.Duration("langfuse-timeout", 2*time.Second, "bounded timeout per Langfuse request")
	f.Usage = func() {
		fmt.Fprintln(diagnostics, "Usage: questlock model evaluate [--suite PATH] [--live --broker URL] [--report PATH] [--langfuse]")
		f.SetOutput(diagnostics)
		f.PrintDefaults()
		f.SetOutput(io.Discard)
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid evaluation flags; use model evaluate --help")
	}
	if f.NArg() != 0 || *timeout <= 0 {
		return errors.New("evaluation takes flags only and requires a positive timeout")
	}
	if *langfuse && !*live {
		return errors.New("--langfuse requires --live; fixture timings are simulations")
	}
	suite := evaluation.DefaultSuite()
	if *suitePath != "" {
		file, err := os.Open(*suitePath)
		if err != nil {
			return errors.New("could not open evaluation suite")
		}
		suite, err = evaluation.Load(file)
		file.Close()
		if err != nil {
			return err
		}
	}
	if err := evaluation.Validate(suite); err != nil {
		return err
	}
	exporter, err := langfuseFromEnvironment(*langfuse, *lfTimeout)
	if err != nil {
		return err
	}
	var reportFile *os.File
	if *reportPath != "" {
		reportFile, err = os.OpenFile(*reportPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return errors.New("could not create report; choose a new private path")
		}
		defer reportFile.Close()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	var report evaluation.Report
	var records []telemetryRecord
	environment := evaluationEnvironment{OS: runtime.GOOS, Architecture: runtime.GOARCH, CPUs: runtime.NumCPU(), GoVersion: runtime.Version(), LoadState: "not_controlled"}
	if !*live {
		report, err = evaluation.RunFixtures(ctx, suite)
	} else {
		broker, _, brokerErr := modelBroker(*brokerURL)
		if brokerErr != nil {
			return brokerErr
		}
		environment.LocalModelDigests = make(map[string]string)
		factory := func(c evaluation.Case, config evaluation.Configuration) (evaluation.Runner, error) {
			started := time.Now()
			provider, err := configuredProvider(ctx, config.Spec, *apiBaseURL)
			ended := time.Now()
			if err != nil {
				return nil, err
			}
			if local, ok := provider.(*inference.OllamaProvider); ok {
				environment.LocalModelDigests[config.Spec.Model] = local.ModelDigest()
			}
			var recordTarget *[]telemetryRecord
			if exporter != nil {
				recordTarget = &records
			}
			return collectedRunner{inner: &preparedRunner{runner: inference.Runner{Broker: broker, Provider: provider}, setupStart: started, setupEnd: ended}, checks: c.Checks, records: recordTarget}, nil
		}
		report, err = evaluation.Evaluate(ctx, suite, factory)
	}
	result := struct {
		evaluation.Report
		Environment evaluationEnvironment `json:"environment"`
	}{report, environment}
	if report.Schema != 0 {
		if writeErr := writeModelJSON(output, result); writeErr != nil {
			return writeErr
		}
		if reportFile != nil {
			if writeErr := writeModelJSON(reportFile, result); writeErr != nil {
				return writeErr
			}
		}
	}
	exportTelemetry(exporter, records, diagnostics)
	if err != nil {
		return errors.New("evaluation stopped; inspect recorded attempts before deliberately starting a new scope")
	}
	if report.Selection == nil {
		return errors.New("no configuration met every declared evaluation gate; inspect the report")
	}
	return nil
}
