package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"time"
	"unicode/utf8"

	"github.com/spfuzzylink/questlock/outcomes"
)

const outcomeUsage = `Usage: questlock outcome record --task ID --revision REV --run PATH [--broker URL]
       questlock outcome feedback --task ID --revision REV --expected-version N --event PATH [--broker URL]
       questlock outcome show --task ID --revision REV [--broker URL]
       questlock outcome report (--cohort PATH | --task ID --revision REV) [--hourly-rate-usd N] [--broker URL]
       questlock outcome quest
Feedback is an explicit operator report; passing model checks never implies human acceptance.`

func outcomeCommand(args []string) error {
	return outcomeCommandIO(args, os.Stdout, os.Stderr)
}

func outcomeCommandIO(args []string, output, diagnostics io.Writer) error {
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(diagnostics, outcomeUsage)
		return err
	}
	if len(args) == 1 && args[0] == "quest" {
		return outcomeQuestIO(output)
	}
	if len(args) == 0 {
		return errors.New("use outcome --help")
	}
	command := args[0]
	if command != "record" && command != "feedback" && command != "show" && command != "report" {
		return errors.New("unknown outcome command; use outcome --help")
	}
	f := flag.NewFlagSet("outcome "+command, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	taskID := f.String("task", "", "stable work item identifier")
	revision := f.String("revision", "", "explicit task snapshot revision")
	brokerURL := f.String("broker", "http://127.0.0.1:8080", "authenticated broker endpoint")
	timeout := f.Duration("timeout", 15*time.Second, "bounded broker operation; maximum one minute")
	var runPath, eventPath, cohortPath *string
	var expectedVersion *int64
	var hourlyRate *float64
	switch command {
	case "record":
		runPath = f.String("run", "", "content-free caller-reported run JSON")
	case "feedback":
		eventPath = f.String("event", "", "explicit feedback JSON")
		expectedVersion = f.Int64("expected-version", -1, "version from outcome show; feedback never rebases")
	case "report":
		cohortPath = f.String("cohort", "", "JSON array of explicit task/revision pairs")
		hourlyRate = f.Float64("hourly-rate-usd", 0, "explicit valuation of recorded review/correction minutes")
	}
	f.Usage = func() { fmt.Fprintln(diagnostics, outcomeUsage) }
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid outcome flags; use outcome --help")
	}
	if f.NArg() != 0 || *timeout <= 0 || *timeout > time.Minute {
		return errors.New("outcome accepts flags only; timeout must be positive and at most one minute")
	}
	var tasks []outcomes.Task
	if cohortPath != nil && *cohortPath != "" {
		if *taskID != "" || *revision != "" {
			return errors.New("use either --cohort or --task and --revision")
		}
		if err := readOutcomeJSON(*cohortPath, &tasks); err != nil {
			return err
		}
	} else {
		if *taskID == "" || *revision == "" {
			return errors.New("--task and --revision are required")
		}
		tasks = []outcomes.Task{{ID: *taskID, Revision: *revision}}
	}
	var run outcomes.Run
	var feedback outcomes.Feedback
	if runPath != nil {
		if *runPath == "" {
			return errors.New("--run is required")
		}
		if err := readOutcomeJSON(*runPath, &run); err != nil {
			return err
		}
	}
	if eventPath != nil {
		if *eventPath == "" || *expectedVersion < 1 {
			return errors.New("--event and positive --expected-version are required")
		}
		if err := readOutcomeJSON(*eventPath, &feedback); err != nil {
			return err
		}
	}
	var rate *float64
	f.Visit(func(value *flag.Flag) {
		if value.Name == "hourly-rate-usd" {
			rate = hourlyRate
		}
	})
	if rate != nil && (math.IsNaN(*rate) || math.IsInf(*rate, 0) || *rate < 0) {
		return errors.New("hourly rate must be a finite nonnegative USD amount")
	}
	broker, _, err := modelBroker(*brokerURL)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	store := outcomes.Store{Broker: broker}
	var result any
	switch command {
	case "record":
		result, err = store.Record(ctx, tasks[0], run)
	case "feedback":
		result, err = store.Feedback(ctx, tasks[0], *expectedVersion, feedback)
	case "show":
		result, err = store.Get(ctx, tasks[0])
	case "report":
		result, err = store.Report(ctx, tasks, rate)
	}
	if err != nil {
		// Broker errors may contain its endpoint or caller-controlled response.
		// Local CLI output stays content-free; original scope records remain.
		if errors.Is(err, outcomes.ErrConflict) || errors.Is(err, outcomes.ErrIDConflict) {
			return errors.New("outcome version or event/run ID conflict; inspect outcome show before a deliberate new event")
		}
		return errors.New("outcome operation failed; inspect input and authenticated broker state before retrying the exact metadata request")
	}
	return writeModelJSON(output, result)
}

func readOutcomeJSON(path string, target any) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("could not open outcome input JSON")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return errors.New("outcome input must be a regular file of at most 1MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) > 1<<20 || !utf8.Valid(data) || uniqueOutcomeJSON(data) != nil {
		return errors.New("outcome input must be UTF-8 JSON without duplicate keys, excessive nesting, or trailing data")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("outcome input has an unknown field or invalid type")
	}
	return nil
}

// encoding/json otherwise silently accepts duplicate keys. Validate the full
// token stream before decoding a typed, explicitly supplied metadata snapshot.
func uniqueOutcomeJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return errors.New("nesting limit")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("duplicate key")
				}
				seen[name] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
				return errors.New("invalid object")
			}
		case '[':
			for decoder.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
				return errors.New("invalid array")
			}
		default:
			return errors.New("unexpected delimiter")
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
