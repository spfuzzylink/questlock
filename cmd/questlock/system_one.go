package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spfuzzylink/questlock/systemone"
)

func systemOneCommand(args []string) error { return systemOneCommandIO(args, os.Stdout, os.Stderr) }

func systemOneCommandIO(args []string, output, diagnostics io.Writer) error {
	if len(args) == 0 || args[0] != "choose" {
		return errors.New("use system-one choose --request PATH --python ABSOLUTE_PINNED_VENV_PYTHON")
	}
	f := flag.NewFlagSet("system-one choose", flag.ContinueOnError)
	f.SetOutput(diagnostics)
	path := f.String("request", "", "bounded typed-choice JSON request with an explicit local model")
	python := f.String("python", "", "absolute Python interpreter from the optional pinned environment")
	timeout := f.Duration("timeout", 60*time.Second, "total deadline, at most 120s")
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid typed-choice flags")
	}
	if f.NArg() != 0 || *path == "" || *python == "" {
		return errors.New("request and explicit pinned-environment Python interpreter are required")
	}
	file, err := openSystemOneRequest(*path)
	if err != nil {
		return err
	}
	request, err := systemone.Load(file)
	_ = file.Close()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, runErr := (systemone.Runner{Python: *python, Timeout: *timeout}).Choose(ctx, request)
	if err := json.NewEncoder(output).Encode(result); err != nil {
		return err
	}
	return runErr
}
