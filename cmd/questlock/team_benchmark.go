package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"strings"

	"github.com/spfuzzylink/questlock/internal/nativeagent"
	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/team"
	"github.com/spfuzzylink/questlock/worker"
)

func readTeamFile(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("explicit file path required")
	}
	abs, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	return worker.ReadApprovedRequest(filepath.Dir(abs), abs)
}
func teamBaselineRecordCommand(args []string, out, diag io.Writer) error {
	f := flag.NewFlagSet("team baseline-record", flag.ContinueOnError)
	f.SetOutput(diag)
	path := f.String("request", "", "exact approved request used for baseline prompt")
	events := f.String("codex-events", "", "actual native Codex subscription JSONL file; no model is started")
	model := f.String("model", "", "explicit hosted Codex model used by operator")
	if e := f.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid baseline record flags")
	}
	if f.NArg() != 0 || strings.TrimSpace(*model) == "" {
		return errors.New("baseline record needs explicit request, Codex events and model")
	}
	b, e := readTeamFile(*path)
	if e != nil {
		return e
	}
	r, e := team.Load(bytes.NewReader(b))
	if e != nil {
		return e
	}
	b, e = readTeamFile(*events)
	if e != nil {
		return e
	}
	response, decodeErr := nativeagent.DecodeEvents(b, *model)
	prompt, e := team.MasterPrompt(r)
	if e != nil {
		return e
	}
	h := sha256.Sum256([]byte(prompt))
	result := team.Baseline{Schema: 1, TaskSHA256: team.TaskDigest(r), PromptSHA256: hex.EncodeToString(h[:]), Model: *model, Source: "operator_recorded_codex_subscription_jsonl", Complete: response.Complete, UsageKnown: decodeErr == nil, Usage: response.Usage, Text: response.Text}
	if decodeErr != nil {
		result.ErrorCategory = "native_baseline_incomplete_or_invalid"
	}
	if e = writeModelJSON(out, result); e != nil {
		return e
	}
	if decodeErr != nil {
		return errors.New("baseline failed; receipt retained without another model call")
	}
	return nil
}

func teamBenchmarkCommand(args []string, out, diag io.Writer) error {
	f := flag.NewFlagSet("team benchmark", flag.ContinueOnError)
	f.SetOutput(diag)
	suitePath := f.String("suite", "", "explicit same-task JSON suite")
	localDir := f.String("local-dir", "", "private directory of case-ID.json local reports")
	baselineDir := f.String("baseline-dir", "", "private directory of case-ID.json Codex baseline receipts")
	if e := f.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid benchmark flags")
	}
	if f.NArg() != 0 || *localDir == "" || *baselineDir == "" {
		return errors.New("benchmark requires explicit suite, local-dir and baseline-dir")
	}
	b, e := readTeamFile(*suitePath)
	if e != nil {
		return e
	}
	var suite team.Suite
	if protocol.DecodeDataJSON(b, &suite) != nil || suite.Schema != 1 || len(suite.Cases) < 1 || len(suite.Cases) > 32 {
		return errors.New("invalid bounded benchmark suite")
	}
	pairs := make([]team.Pair, 0, len(suite.Cases))
	for _, c := range suite.Cases {
		// Case IDs are filenames, never paths. A missing report is a failed row,
		// not permission to drop a case from the comparison denominator.
		if c.ID == "" || strings.ContainsAny(c.ID, "/\\.") || len(c.ID) > 128 {
			return errors.New("invalid case filename")
		}
		p := team.Pair{Case: c}
		for i, dir := range []string{*localDir, *baselineDir} {
			data, err := readTeamFile(filepath.Join(dir, c.ID+".json"))
			if err != nil {
				continue
			}
			if i == 0 {
				if protocol.DecodeDataJSON(data, &p.Local) != nil {
					return errors.New("invalid local benchmark report")
				}
			} else if protocol.DecodeDataJSON(data, &p.Baseline) != nil {
				return errors.New("invalid Codex baseline receipt")
			}
		}
		pairs = append(pairs, p)
	}
	report, e := team.Compare(pairs)
	if e != nil {
		return e
	}
	if e = writeModelJSON(out, report); e != nil {
		return e
	}
	if !report.Eligible {
		return errors.New("comparison is not eligible; keep failed and missing cases")
	}
	return nil
}
