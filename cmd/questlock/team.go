package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/localruntime"
	"github.com/spfuzzylink/questlock/internal/nativeagent"
	"github.com/spfuzzylink/questlock/internal/onboardingclient"
	"github.com/spfuzzylink/questlock/internal/teamquest"
	"github.com/spfuzzylink/questlock/team"
	"github.com/spfuzzylink/questlock/worker"
)

func teamCommand(args []string) error { return teamCommandIO(args, os.Stdout, os.Stderr) }
func teamCommandIO(args []string, out, diag io.Writer) error {
	if len(args) == 1 && args[0] == "quest" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		r, err := teamquest.Run(ctx)
		if writeErr := writeModelJSON(out, r); writeErr != nil {
			return writeErr
		}
		return err
	}
	if len(args) == 0 {
		return errors.New("use team init, plan, run, status, or benchmark")
	}
	if args[0] == "benchmark" {
		return teamBenchmarkCommand(args[1:], out, diag)
	}
	if args[0] == "baseline-record" {
		return teamBaselineRecordCommand(args[1:], out, diag)
	}
	if args[0] != "init" && args[0] != "plan" && args[0] != "run" && args[0] != "status" && args[0] != "suite" && args[0] != "prompt" {
		return errors.New("unknown team command")
	}
	f := flag.NewFlagSet("team "+args[0], flag.ContinueOnError)
	f.SetOutput(io.Discard)
	requestPath := f.String("request", "", "explicit approved task JSON")
	workspace := f.String("workspace", "", "request file boundary; default current directory")
	brokerURL := f.String("broker", "", "explicit shared broker; default managed local broker")
	caFile := f.String("ca-file", "", "explicit private CA PEM for HTTPS broker")
	role := f.String("role", "all", "all, master, or declared reviewer ID")
	workerID := f.String("worker-id", "", "operator worker identity; defaults to hostname")
	clientBin := f.String("client-bin", "", "absolute native Codex executable for codex-oss roles")
	model := f.String("model", "", "installed model tag for init; no automatic download")
	driver := f.String("driver", "ollama", "init role driver: ollama or codex-oss")
	state := f.String("state-dir", "", "private managed local broker directory")
	configPath := f.String("config", "", "explicit complete resource admission JSON")
	timeout := f.Duration("timeout", 5*time.Minute, "whole invocation deadline, at most 30m")
	f.Usage = func() {
		io.WriteString(diag, "Usage: questlock team init --model NAME [--driver ollama|codex-oss]\n       questlock team plan|run|status --request PATH [--workspace DIR] [--broker HTTPS_URL --ca-file PEM] [--role ROLE --worker-id HOST]\n")
		f.SetOutput(diag)
		f.PrintDefaults()
		f.SetOutput(io.Discard)
	}
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid team flags; use team run --help")
	}
	if f.NArg() != 0 || *timeout <= 0 || *timeout > 30*time.Minute {
		return errors.New("team commands take flags only and need a bounded positive timeout")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if args[0] == "init" || args[0] == "suite" {
		p, err := inference.NewOllamaProviderContext(ctx, "", *model)
		if err != nil {
			return errors.New("team init requires an installed local model")
		}
		inputLimit := int64(8192)
		if *driver == "codex-oss" {
			inputLimit = 32768
		}
		r := team.Request{Schema: 1, Task: "my-project/first-team-task", Revision: "snapshot-1", Instructions: "Return only a JSON object with issue and deadline strings copied from the input.", Input: "Ticket OPS-17 must be completed on 2030-04-03.", ReuseForSeconds: 86400, Checks: []evaluation.Check{{ID: "expected-values", Kind: "json_equals", Expected: json.RawMessage(`{"issue":"OPS-17","deadline":"2030-04-03"}`)}}}
		r.Master = team.Role{ID: "master", Driver: *driver, Model: *model, ModelDigest: p.ModelDigest(), Instructions: "Solve the approved task. Return only the requested answer, without Markdown fences.", MaxInputTokens: inputLimit, MaxOutputTokens: 512}
		correct, risk := r.Master, r.Master
		correct.ID = "review-correctness"
		correct.Instructions = "Check that every requirement and source fact is satisfied. Reject unsupported or incorrect statements."
		risk.ID = "review-risk"
		risk.Instructions = "Check boundary conditions, missing requirements and misleading claims. Reject any concrete defect."
		r.Reviewers = []team.Role{correct, risk}
		if err := team.Validate(r); err != nil {
			return err
		}
		if args[0] == "suite" {
			return writeModelJSON(out, team.DefaultSuite(r))
		}
		return writeModelJSON(out, r)
	}
	if *workspace == "" {
		var err error
		*workspace, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	b, err := worker.ReadApprovedRequest(*workspace, *requestPath)
	if err != nil {
		return err
	}
	r, err := team.Load(bytes.NewReader(b))
	if err != nil {
		return err
	}
	if args[0] == "prompt" {
		p, e := team.MasterPrompt(r)
		if e != nil {
			return e
		}
		return writeModelJSON(out, map[string]any{"task_sha256": team.TaskDigest(r), "prompt": p})
	}
	if args[0] == "plan" {
		d, err := team.Describe(r)
		if err != nil {
			return err
		}
		return writeModelJSON(out, d)
	}
	if *workerID == "" {
		*workerID, _ = os.Hostname()
	}
	var broker *client.Client
	if *brokerURL != "" {
		var token string
		broker, token, err = modelBroker(*brokerURL)
		if err != nil {
			return err
		}
		if *caFile != "" {
			abs, e := filepath.Abs(*caFile)
			if e != nil {
				return e
			}
			pem, e := worker.ReadApprovedRequest(filepath.Dir(abs), abs)
			if e != nil {
				return errors.New("cannot read explicit CA PEM")
			}
			broker, err = client.NewWithTLSRoots(*brokerURL, token, pem)
		}
	} else {
		if *caFile != "" {
			return errors.New("ca-file requires an explicit HTTPS broker")
		}
		_, defaultState, _, self, e := onboardingDefaults()
		if e != nil {
			return e
		}
		if *state == "" {
			*state = defaultState
		}
		h, e := localruntime.Ensure(ctx, localruntime.Options{StateDir: *state, Executable: self, Version: version})
		if e != nil {
			return e
		}
		broker = h.Client
	}
	if err != nil {
		return err
	}
	e := team.Engine{Broker: broker, WorkerID: *workerID}
	e.Factory = func(roleCtx context.Context, role team.Role) (delegation.Runner, error) {
		var provider inference.Provider
		if role.Driver == "ollama" {
			p, e := inference.NewOllamaProviderContext(roleCtx, "", role.Model)
			if e != nil {
				return nil, e
			}
			if p.ModelDigest() != role.ModelDigest {
				return nil, errors.New("installed model digest differs from approved role")
			}
			provider = p
		} else {
			home, e := resolvedTempDir("questlock-team-native-inspect-")
			if e != nil {
				return nil, e
			}
			defer os.RemoveAll(home)
			d, e := onboardingclient.Inspect(roleCtx, onboardingclient.Options{Client: onboardingclient.Codex, HomeDir: home, Executable: *clientBin})
			if e != nil || !d.ClientAvailable {
				return nil, errors.New("installed native Codex required for codex-oss role")
			}
			roleTimeout := *timeout
			if roleTimeout > 10*time.Minute {
				roleTimeout = 10 * time.Minute
			}
			p, e := nativeagent.New(roleCtx, d.Executable, role.Model, roleTimeout)
			if e != nil {
				return nil, e
			}
			if p.ModelDigest() != role.ModelDigest {
				return nil, errors.New("installed model digest differs from approved role")
			}
			provider = p
		}
		return inference.Runner{Broker: broker, Provider: provider}, nil
	}
	c, err := loadAdmissionConfig(*configPath)
	if err != nil {
		return err
	}
	e.Admit = func(callCtx context.Context, perform func(context.Context) error) error {
		controller, e := admission.New(c, func(jobCtx context.Context, _ admission.Job) error { return perform(jobCtx) })
		if e != nil {
			return e
		}
		runCtx, stopRun := context.WithCancel(callCtx)
		defer stopRun()
		done := make(chan error, 1)
		go func() { done <- controller.Run(runCtx, admission.SystemSource()) }()
		defer func() { stopRun(); <-done }()
		if e = waitAdmissionReady(callCtx, controller); e != nil {
			return e
		}
		future, e := controller.Submit(callCtx, admission.Job{ID: "approved-team-role", Lane: admission.LaneInference})
		if e != nil {
			return e
		}
		result := <-future
		return result.Err
	}
	selected := *role
	if args[0] == "status" {
		selected = "status"
	}
	report, runErr := e.Run(ctx, r, selected)
	if err := writeModelJSON(out, report); err != nil {
		return err
	}
	return runErr
}
