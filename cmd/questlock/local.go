package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/internal/localruntime"
	"github.com/spfuzzylink/questlock/revision"
)

func onboardingDefaults() (home, state, workspace, self string, err error) {
	home, err = os.UserHomeDir()
	if err != nil {
		return
	}
	state = filepath.Join(home, ".local", "share", "questlock", "local")
	workspace = filepath.Join(home, ".local", "share", "questlock", "requests")
	self, err = os.Executable()
	if err == nil {
		self, err = filepath.EvalSymlinks(self)
	}
	return
}

func localBrokerChild(args []string) error {
	f := flag.NewFlagSet("__local-broker", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	state := f.String("state-dir", "", "")
	if f.Parse(args) != nil || f.NArg() != 0 || *state == "" {
		return errors.New("invalid local broker arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return localruntime.Serve(ctx, localruntime.Options{StateDir: *state, Version: version})
}

func localWorkerCommand(args []string, input io.ReadCloser, output, diagnostics io.Writer) error {
	_, defaultState, defaultWorkspace, self, err := onboardingDefaults()
	if err != nil {
		return errors.New("cannot determine local workflow paths")
	}
	f := flag.NewFlagSet("worker local-serve", flag.ContinueOnError)
	f.SetOutput(diagnostics)
	state := f.String("state-dir", defaultState, "private shared local broker state")
	workspace := f.String("workspace", defaultWorkspace, "approved request file boundary")
	clientName := f.String("client", "codex", "client label: codex, claude-code, or claude-desktop")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid local worker flags")
	}
	if f.NArg() != 0 || (*clientName != "codex" && *clientName != "claude-code" && *clientName != "claude-desktop") {
		return errors.New("invalid local client or positional arguments")
	}
	if err := ensureRequestWorkspace(*workspace); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	h, err := localruntime.Ensure(ctx, localruntime.Options{StateDir: *state, Executable: self, Version: version})
	if err != nil {
		return err
	}
	// Model provider construction still occurs only for an explicit bounded tool
	// request. Paid escalation is never enabled by the onboarding launcher.
	return workerCommandBroker([]string{"serve", "--workspace", *workspace, "--broker", h.URL, "--timeout", "45s"}, input, output, diagnostics, h.Client)
}

func localCommand(args []string) error {
	_, defaultState, _, self, err := onboardingDefaults()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("use local start, status, or stop")
	}
	f := flag.NewFlagSet("local "+args[0], flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	state := f.String("state-dir", defaultState, "private shared local broker state")
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return errors.New("local accepts flags only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	switch args[0] {
	case "start":
		if _, err := localruntime.Ensure(ctx, localruntime.Options{StateDir: *state, Executable: self, Version: version}); err != nil {
			return err
		}
	case "stop":
		if err := localruntime.Stop(ctx, *state); err != nil {
			return err
		}
		return writeModelJSON(os.Stdout, map[string]any{"status": "stop_requested", "state_dir": *state, "data_removed": false})
	case "status":
	default:
		return errors.New("use local start, status, or stop")
	}
	report, err := localruntime.Inspect(ctx, *state)
	if outErr := writeModelJSON(os.Stdout, report); outErr != nil {
		return outErr
	}
	return err
}

func ensureRequestWorkspace(path string) error {
	if err := localruntime.CheckPath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return errors.New("cannot create approved request workspace")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return errors.New("request workspace must be a directory without group/other write access")
	}
	return localruntime.CheckPath(path)
}

func firstRequest() revision.Request {
	source := `{"status":"pending","model_calls":0}`
	h := sha256.Sum256([]byte(source))
	return revision.Request{Schema: 1, Task: "onboarding/first-task", Revision: "checked-json-1", Source: revision.Source{Text: &source, SHA256: hex.EncodeToString(h[:])}, Edits: []revision.Edit{{Old: `"pending"`, New: `"ready"`, ExpectedMatches: 1}}, SourceChecks: []evaluation.Check{{ID: "source", Kind: "json_equals", Expected: json.RawMessage(source)}}, Checks: []evaluation.Check{{ID: "ready", Kind: "json_equals", Expected: json.RawMessage(`{"status":"ready","model_calls":0}`)}}, TrackOutcome: true}
}

// writeFirstRequest never replaces an existing user's file, even during updates.
func writeFirstRequest(workspace string) (string, error) {
	if err := ensureRequestWorkspace(workspace); err != nil {
		return "", err
	}
	path := filepath.Join(workspace, "first-task.json")
	if err := localruntime.CheckPath(path); err != nil {
		return "", err
	}
	data, _ := json.MarshalIndent(firstRequest(), "", "  ")
	data = append(data, '\n')
	readExisting := func() (string, error) {
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return "", errors.New("existing first-task file is not private regular data")
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(existing, data) {
			return "", errors.New("existing first-task.json differs; preserve it and choose a separate request workspace")
		}
		return path, nil
	}
	if _, err := os.Lstat(path); err == nil {
		return readExisting()
	} else if !os.IsNotExist(err) {
		return "", errors.New("cannot inspect first-task request")
	}
	// Multiple native clients can cold-start together. Publish only a fully
	// synced fixture; a reader must never mistake a half-written file for the
	// operator's different request. A hard link is atomic and never replaces it.
	f, err := os.CreateTemp(workspace, ".first-task-*")
	if err != nil {
		return "", errors.New("cannot save first-task request")
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return "", errors.New("could not durably save first-task request")
	}
	if err := os.Link(f.Name(), path); os.IsExist(err) {
		return readExisting()
	} else if err != nil {
		return "", errors.New("cannot publish first-task request without replacing existing data")
	}
	dir, err := os.Open(workspace)
	if err != nil {
		return "", errors.New("cannot sync request workspace")
	}
	err = dir.Sync()
	closeErr = dir.Close()
	if err != nil || closeErr != nil {
		return "", errors.New("could not durably publish first-task request")
	}
	return path, nil
}

type firstTaskReport struct {
	Status               string          `json:"status"`
	RequestPath          string          `json:"request_path"`
	MCPTools             []string        `json:"mcp_tools"`
	MCPProtocolSelfTest  bool            `json:"mcp_protocol_self_test"`
	NativeClientVerified bool            `json:"native_client_verified"`
	Result               revision.Result `json:"result"`
}

func runFirstTask(ctx context.Context, self, state, workspace string) (firstTaskReport, error) {
	var report firstTaskReport
	path, err := writeFirstRequest(workspace)
	if err != nil {
		return report, err
	}
	report.RequestPath = path
	command := exec.Command(self, "worker", "local-serve", "--state-dir", state, "--workspace", workspace, "--client", "codex")
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	command.Stderr = io.Discard
	c := mcp.NewClient(&mcp.Implementation{Name: "questlock-onboarding-self-test", Version: version}, nil)
	session, err := c.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		return report, errors.New("local MCP startup failed; run doctor or inspect private broker.log")
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return report, errors.New("local MCP tool discovery failed")
	}
	for _, tool := range tools.Tools {
		report.MCPTools = append(report.MCPTools, tool.Name)
	}
	if len(report.MCPTools) != 2 || !containsName(report.MCPTools, "questlock_delegate") || !containsName(report.MCPTools, "questlock_revise") {
		return report, errors.New("local MCP capabilities did not match this release")
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "questlock_revise", Arguments: map[string]any{"request_path": "first-task.json"}})
	if err != nil || result.IsError {
		return report, errors.New("first task failed; no model fallback was attempted")
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil || json.Unmarshal(data, &report.Result) != nil {
		return report, errors.New("first task returned invalid structured data")
	}
	r := report.Result
	if r.Status != "completed" || !r.Durable || r.GenerationCalled || r.Artifact == nil || r.Text != `{"status":"ready","model_calls":0}` || r.ThisRunUsage.InputTokens != 0 || r.ThisRunUsage.OutputTokens != 0 || r.ProviderCostUSD != 0 || r.OutcomeTracking == nil || r.OutcomeTracking.Status != "recorded" {
		return report, errors.New("first task did not meet zero-generation durable accounting checks")
	}
	for _, check := range append(r.SourceChecks, r.Checks...) {
		if !check.Passed {
			return report, errors.New("first task checks failed")
		}
	}
	report.Status = "completed"
	report.MCPProtocolSelfTest = true
	return report, nil
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func tryCommand(args []string) error {
	_, defaultState, defaultWorkspace, self, err := onboardingDefaults()
	if err != nil {
		return err
	}
	f := flag.NewFlagSet("try", flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	state := f.String("state-dir", defaultState, "private managed local broker state")
	workspace := f.String("workspace", defaultWorkspace, "approved request workspace")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return errors.New("try accepts flags only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	report, err := runFirstTask(ctx, self, *state, *workspace)
	if report.Status != "" {
		if outErr := writeModelJSON(os.Stdout, report); outErr != nil {
			return outErr
		}
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "First task passed: durable checked edit, zero model calls. Local service stays available; use questlock local stop to stop it.")
	return nil
}
