package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/worker"
	"github.com/spfuzzylink/questlock/writing"
)

func writingCommand(args []string) error { return writingCommandIO(args, os.Stdout, os.Stderr) }

func writingCommandIO(args []string, output, diagnostics io.Writer) error {
	usage := "Usage: questlock writing init --workspace ROOT --reference /absolute/reference.json --model MODEL\n       questlock writing plan --workspace ROOT --evidence /absolute/evidence.json --request writing-request.json\n       questlock writing record --workspace ROOT --manifest writing-request.json.writing.json --result /absolute/result.json --review assistant_reviewed --broker URL\n       questlock writing show --reference REFERENCE_ID --broker URL\nInit and plan are offline. Record and show use the authenticated broker and never generate an answer."
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(diagnostics, usage)
		return err
	}
	command := args[0]
	if command != "init" && command != "plan" && command != "record" && command != "show" {
		return errors.New("use writing init, plan, record or show; use writing --help")
	}
	f := flag.NewFlagSet("writing "+command, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	workspace := f.String("workspace", "", "request_root returned by questlock_status; must already exist")
	reference := f.String("reference", "", "init: explicit absolute approved reference JSON; show: reference ID")
	model := f.String("model", "", "init: explicitly selected installed Ollama model")
	evidence := f.String("evidence", "", "plan: explicit absolute current evidence JSON")
	requestName := f.String("request", "", "plan: new request basename within the worker root")
	manifest := f.String("manifest", "", "record: plan manifest basename within the worker root")
	resultPath := f.String("result", "", "record: explicit absolute saved worker structured result JSON")
	review := f.String("review", "", "record: unreviewed or assistant_reviewed; does not attest human acceptance")
	brokerURL := f.String("broker", "http://127.0.0.1:8080", "record/show: trusted broker endpoint; keep the scope consistent with the worker")
	f.Usage = func() { _, _ = fmt.Fprintln(diagnostics, usage) }
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid writing flags; use writing --help")
	}
	if f.NArg() != 0 {
		return errors.New("writing accepts flags only")
	}
	// Reject irrelevant flags: a misspelled workflow must not silently ignore
	// a caller's intended evidence, model or review policy.
	allowed := map[string]string{"init": " workspace reference model ", "plan": " workspace evidence request ", "record": " workspace manifest result review broker ", "show": " reference broker "}
	valid := true
	f.Visit(func(v *flag.Flag) {
		if !strings.Contains(allowed[command], " "+v.Name+" ") {
			valid = false
		}
	})
	if !valid {
		return errors.New("flag does not apply to this writing subcommand")
	}
	if command == "init" || command == "plan" || command == "record" {
		if *workspace == "" {
			return errors.New("--workspace is required; use the connected worker's declared request_root")
		}
	}
	switch command {
	case "init":
		if !filepath.IsAbs(*reference) || *model == "" {
			return errors.New("init requires an absolute --reference and explicit --model")
		}
		data, err := writingReadFile(*reference)
		if err != nil {
			return err
		}
		if _, err := writing.DecodeReference(data); err != nil {
			return errors.New("invalid approved writing reference")
		}
		config := writing.Config{Schema: 1, ReferencePath: *reference, ReferenceSHA256: writing.Hash(data), Recipe: writing.DefaultRecipe(*model)}
		// Exercise the same policy validation as plan without a model or broker.
		if _, err := writing.Build(data, []byte(`{"schema":1,"id":"validation","revision":"one","question":"Validate this recipe.","facts":["Synthetic validation only."],"sources":[]}`), config.Recipe); err != nil {
			return errors.New("invalid writing model or recipe")
		}
		if err := writingCreate(*workspace, writing.ConfigName, config); err != nil {
			return err
		}
		return writeModelJSON(output, map[string]any{"status": "initialized", "config": filepath.Join(*workspace, writing.ConfigName), "reference_sha256": config.ReferenceSHA256, "model_calls": 0, "paid_fallback": false})
	case "plan":
		if !filepath.IsAbs(*evidence) || !writingBasename(*requestName) {
			return errors.New("plan requires an absolute --evidence and a new --request basename ending in .json")
		}
		data, err := worker.ReadApprovedRequest(*workspace, writing.ConfigName)
		var config writing.Config
		if err != nil || protocol.DecodeDataJSON(data, &config) != nil || config.Schema != 1 || !filepath.IsAbs(config.ReferencePath) {
			return errors.New("invalid writing-workflow.json; initialize and review the private config first")
		}
		ref, err := writingReadFile(config.ReferencePath)
		if err != nil {
			return err
		}
		if config.ReferenceSHA256 != writing.Hash(ref) {
			return errors.New("approved reference changed; explicitly review it and update the private config hash before planning")
		}
		current, err := writingReadFile(*evidence)
		if err != nil {
			return err
		}
		plan, err := writing.Build(ref, current, config.Recipe)
		if err != nil {
			return errors.New("invalid writing evidence or recipe; no request was created")
		}
		// The request is published last. If either private write fails, inspect
		// the existing files; nothing is submitted to a model automatically.
		if err := writingCreate(*workspace, *requestName+".writing.json", plan); err != nil {
			return err
		}
		if err := writingCreate(*workspace, *requestName, plan.Request); err != nil {
			return err
		}
		return writeModelJSON(output, map[string]any{"status": "planned", "request": filepath.Join(*workspace, *requestName), "manifest": filepath.Join(*workspace, *requestName+".writing.json"), "request_sha256": plan.RequestSHA256, "reference_sha256": plan.ReferenceSHA256, "evidence_sha256": plan.EvidenceSHA256, "checks": plan.Request.Checks, "model_calls": 0, "paid_fallback": false, "next": "Review the private request before explicitly delegating it. Keep the exact snapshot for an exact repeat."})
	case "record":
		if !writingBasename(*manifest) || !filepath.IsAbs(*resultPath) || (*review != "assistant_reviewed" && *review != "unreviewed") {
			return errors.New("record requires --manifest basename, absolute --result, and --review unreviewed or assistant_reviewed")
		}
		data, err := worker.ReadApprovedRequest(*workspace, *manifest)
		var plan writing.Plan
		if err != nil || protocol.DecodeDataJSON(data, &plan) != nil || writing.ValidatePlan(plan) != nil {
			return errors.New("invalid writing manifest")
		}
		data, err = writingReadFile(*resultPath)
		var result delegation.Result
		if err != nil || protocol.DecodeDataJSON(data, &result) != nil {
			return errors.New("invalid saved worker result; save its structuredContent object directly")
		}
		broker, _, err := modelBroker(*brokerURL)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		receipt, err := writing.Record(ctx, broker, plan, result, *review)
		if err != nil {
			_ = writeModelJSON(output, map[string]any{"status": "unconfirmed", "receipt": receipt, "model_calls": 0, "next": "Reconcile the same saved result; do not regenerate."})
			return errors.New("writing index unconfirmed; inspect broker metadata and reconcile the same saved result without regenerating")
		}
		return writeModelJSON(output, map[string]any{"status": "recorded", "receipt": receipt, "human_acceptance": "not_asserted", "model_calls": 0})
	case "show":
		if *reference == "" {
			return errors.New("show requires --reference ID")
		}
		broker, _, err := modelBroker(*brokerURL)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		entries, err := writing.ReadEntries(ctx, broker, *reference)
		if err != nil {
			return errors.New("writing index unavailable or invalid; no generation was attempted")
		}
		return writeModelJSON(output, map[string]any{"schema": 1, "reference_id": *reference, "entries": entries, "model_calls": 0, "historical_reference_only": true})
	}
	return nil
}

func writingReadFile(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("writing input path must be absolute")
	}
	data, err := worker.ReadApprovedRequest(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return nil, errors.New("could not read explicitly selected regular writing input file")
	}
	return data, nil
}

func writingBasename(name string) bool {
	return len(name) > 5 && len(name) <= 160 && filepath.Base(name) == name && !strings.HasPrefix(name, ".") && !strings.ContainsAny(name, "/\\\x00") && strings.HasSuffix(name, ".json")
}

func writingCreate(workspace, name string, value any) error {
	if !writingBasename(name) {
		return errors.New("writing output must be a JSON basename inside the worker root")
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return errors.New("could not open existing writing workspace")
	}
	defer root.Close()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil || len(data) >= worker.MaxRequestBytes {
		return errors.New("writing output exceeds the request bound")
	}
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("writing output already exists or cannot be created; inspect it before choosing a new name")
	}
	_, writeErr := io.Copy(file, bytes.NewReader(append(data, '\n')))
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return errors.New("writing output write is unconfirmed; inspect the private file before continuing")
	}
	return nil
}
