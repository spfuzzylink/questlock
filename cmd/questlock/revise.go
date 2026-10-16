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

	"github.com/spfuzzylink/questlock/revision"
)

func reviseCommand(args []string) error { return reviseCommandIO(args, os.Stdout, os.Stderr) }

func reviseCommandIO(args []string, output, diagnostics io.Writer) error {
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(diagnostics, "Usage: questlock revise plan --request PATH [--broker URL]\n       questlock revise run --request PATH [--broker URL] [--timeout 2m]\n       questlock revise quest\nLiteral checked revisions call no model. Plan does not publish; run requires a broker credential.")
		return err
	}
	if len(args) == 1 && args[0] == "quest" {
		return reviseQuest(output)
	}
	if len(args) == 0 || (args[0] != "plan" && args[0] != "run") {
		return errors.New("use revise plan, revise run or revise quest; use revise --help")
	}
	command := args[0]
	f := flag.NewFlagSet("revise "+command, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	path := f.String("request", "", "approved checked-revision JSON")
	brokerURL := f.String("broker", "http://127.0.0.1:8080", "trusted broker endpoint")
	timeout := f.Duration("timeout", 2*time.Minute, "request bound, maximum 10 minutes")
	f.Usage = func() {
		fmt.Fprintf(diagnostics, "Usage: questlock revise %s --request PATH [flags]\n", command)
		f.SetOutput(diagnostics)
		f.PrintDefaults()
		f.SetOutput(io.Discard)
	}
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid revise flags; use revise --help")
	}
	if f.NArg() != 0 || *path == "" || *timeout <= 0 || *timeout > 10*time.Minute {
		return errors.New("revise requires --request, no positional arguments, and a positive timeout of at most 10 minutes")
	}
	file, err := os.Open(*path)
	if err != nil {
		return errors.New("could not open approved revision request")
	}
	request, err := revision.Load(file)
	file.Close()
	if err != nil {
		return errors.New("invalid approved revision request")
	}
	if err := revision.Validate(request); err != nil {
		return errors.New("invalid revision policy")
	}
	runner := revision.Runner{}
	if command == "run" || request.Source.Text == nil {
		broker, _, err := modelBroker(*brokerURL)
		if err != nil {
			return err
		}
		runner.Broker = broker
	}
	process, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(process, *timeout)
	defer cancel()
	var result revision.Result
	var runErr error
	if command == "plan" {
		result, runErr = runner.Plan(ctx, request)
	} else {
		result, runErr = runner.Run(ctx, request)
	}
	if err := writeModelJSON(output, result); err != nil {
		return err
	}
	if result.OutcomeTracking != nil && result.OutcomeTracking.Status != "recorded" {
		return errors.New("outcome registration is unconfirmed; recover outcome_tracking.record without rerunning the revision")
	}
	if runErr != nil {
		return errors.New("revision did not complete; inspect status and broker records before deliberately changing the request")
	}
	return nil
}
