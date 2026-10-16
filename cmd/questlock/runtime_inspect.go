package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"time"

	"github.com/spfuzzylink/questlock/inference"
)

// Inspection performs read-only runtime metadata requests, without broker
// access, tokenization, model generation, or cache administration.
func modelRuntimeInspect(args []string, output, help io.Writer) error {
	f := flag.NewFlagSet("model runtime-inspect", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	path := f.String("spec", "", "SGLang task spec containing the declared deployment contract")
	endpoint := f.String("api-base-url", "", "process-authorized runtime endpoint")
	timeout := f.Duration("timeout", 15*time.Second, "read-only inspection deadline")
	f.Usage = func() { f.SetOutput(help); f.PrintDefaults(); f.SetOutput(io.Discard) }
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid runtime-inspect flags")
	}
	if f.NArg() != 0 || *path == "" || *timeout <= 0 || *timeout > time.Minute {
		return errors.New("runtime-inspect requires --spec and a positive timeout of at most one minute")
	}
	spec, err := readModelSpec(*path)
	if err != nil {
		return err
	}
	if spec.Provider != "sglang" {
		return errors.New("runtime-inspect requires the sglang provider")
	}
	if _, err = inference.BuildPlan(spec); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	provider, err := configuredProvider(ctx, spec, *endpoint)
	if err != nil {
		return err
	}
	return writeModelJSON(output, struct {
		Capabilities inference.SGLangCapabilities `json:"capabilities"`
		Evidence     string                       `json:"evidence"`
		GPUValidated bool                         `json:"gpu_validated"`
	}{provider.(*inference.SGLangProvider).Capabilities(), "runtime_reported_configuration", false})
}
