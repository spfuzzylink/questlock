package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spfuzzylink/questlock/internal/agentsetup"
	"github.com/spfuzzylink/questlock/internal/configguard"
	"github.com/spfuzzylink/questlock/internal/desktopbundle"
	"github.com/spfuzzylink/questlock/internal/localruntime"
	"github.com/spfuzzylink/questlock/internal/onboardingclient"
)

type setupFlags struct {
	Client, Home, CodexHome, State, Workspace, Native, Bundle, Restore string
	Plan, Apply, ReplaceSkills, NoOpen                                 bool
}
type setupReceipt struct {
	Client    onboardingclient.Client `json:"client"`
	Home      string                  `json:"home"`
	CodexHome string                  `json:"codex_home"`
	Native    string                  `json:"native_executable,omitempty"`
	Binary    string                  `json:"binary"`
	State     string                  `json:"state_dir"`
	Workspace string                  `json:"workspace"`
}
type setupReport struct {
	Version       string                       `json:"version"`
	Client        onboardingclient.Client      `json:"client"`
	Status        string                       `json:"status"`
	Validation    *onboardingclient.Validation `json:"validation,omitempty"`
	OriginalSHA   string                       `json:"original_sha256,omitempty"`
	CandidateSHA  string                       `json:"candidate_sha256,omitempty"`
	FirstTask     *firstTaskReport             `json:"first_task,omitempty"`
	Recovery      *configguard.Result          `json:"recovery,omitempty"`
	DesktopBundle *desktopbundle.Result        `json:"desktop_bundle,omitempty"`
	Next          []string                     `json:"next_steps,omitempty"`
}

func chooseClient() (string, error) {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return "", errors.New("choose --client codex, claude-code, or claude-desktop (interactive setup also supports a menu)")
	}
	fmt.Fprintln(os.Stderr, "Connect Questlock to: 1) Codex  2) Claude Code  3) Claude Desktop")
	fmt.Fprint(os.Stderr, "Choose [1]: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", errors.New("could not read setup selection")
	}
	switch strings.TrimSpace(line) {
	case "", "1", "codex":
		return "codex", nil
	case "2", "claude-code":
		return "claude-code", nil
	case "3", "claude-desktop":
		return "claude-desktop", nil
	default:
		return "", errors.New("client must be codex, claude-code, or claude-desktop")
	}
}

func setupCommand(args []string) error {
	home, state, workspace, self, err := onboardingDefaults()
	if err != nil {
		return err
	}
	f := flag.NewFlagSet("setup", flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	var o setupFlags
	f.StringVar(&o.Client, "client", "", "codex, claude-code, or claude-desktop; omit for interactive menu")
	f.StringVar(&o.Home, "home", home, "target user home (isolated home for validation)")
	f.StringVar(&o.CodexHome, "codex-home", "", "explicit Codex config home; otherwise CODEX_HOME or target .codex")
	f.StringVar(&o.State, "state-dir", "", "private managed local broker state")
	f.StringVar(&o.Workspace, "workspace", "", "approved request directory")
	f.StringVar(&o.Native, "client-bin", "", "absolute native coding-client executable")
	f.StringVar(&o.Bundle, "bundle", "", "new Claude Desktop .mcpb output path")
	f.StringVar(&o.Restore, "restore", "", "restore a prior committed setup recovery directory")
	f.BoolVar(&o.Plan, "plan", false, "validate a complete isolated candidate without installing client settings")
	f.BoolVar(&o.Apply, "apply", false, "explicit spelling of the default guarded install")
	f.BoolVar(&o.ReplaceSkills, "replace-skills", false, "replace a differing existing Questlock skill; preserve custom edits first")
	f.BoolVar(&o.NoOpen, "no-open", false, "print Desktop native-install steps without opening the bundle")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 || o.Plan && o.Apply {
		return errors.New("setup accepts flags only; --plan and --apply are alternatives")
	}
	if o.Restore != "" {
		return restoreSetup(o.Restore, self)
	}
	if o.Client == "" {
		o.Client, err = chooseClient()
		if err != nil {
			return err
		}
	}
	if o.Client != "codex" && o.Client != "claude-code" && o.Client != "claude-desktop" {
		return errors.New("client must be codex, claude-code, or claude-desktop")
	}
	if o.Home != home {
		state = filepath.Join(o.Home, ".local", "share", "questlock", "local")
		workspace = filepath.Join(o.Home, ".local", "share", "questlock", "requests")
	}
	if o.State == "" {
		o.State = state
	}
	if o.Workspace == "" {
		o.Workspace = workspace
	}
	if o.CodexHome == "" {
		o.CodexHome = filepath.Join(o.Home, ".codex")
		if o.Home == home && os.Getenv("CODEX_HOME") != "" {
			o.CodexHome = os.Getenv("CODEX_HOME")
		}
	}
	for _, path := range []string{o.Home, o.CodexHome, o.State, o.Workspace} {
		if err := localruntime.CheckPath(path); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if o.Client == "claude-desktop" {
		return desktopSetup(ctx, o, self)
	}
	agent := "codex"
	if o.Client == "claude-code" {
		agent = "claude"
	}
	// Check the native prerequisite before installing a binary or skills.
	preflight, err := onboardingclient.Inspect(ctx, onboardingclient.Options{Client: onboardingclient.Client(o.Client), HomeDir: o.Home, CodexHome: o.CodexHome, Executable: o.Native})
	if err != nil {
		return err
	}
	if !preflight.ClientAvailable {
		return errors.New("native coding client is missing; install it first or pass --client-bin")
	}
	installed := filepath.Join(o.Home, ".local", "bin", "questlock")
	if !o.Plan {
		if _, err := agentsetup.Install(agentsetup.Options{Home: o.Home, CodexHome: o.CodexHome, Executable: self, Agents: []string{agent}, Replace: o.ReplaceSkills}); err != nil {
			return err
		}
	} else {
		installed = self
	}
	opts := onboardingclient.Options{Client: onboardingclient.Client(o.Client), HomeDir: o.Home, CodexHome: o.CodexHome, Executable: o.Native, QuestlockPath: installed, Workspace: o.Workspace, StateDir: o.State}
	candidate, err := onboardingclient.Plan(ctx, opts)
	if err != nil {
		return err
	}
	defer candidate.Close()
	report := setupReport{Version: version, Client: opts.Client, Status: "planned", Validation: &candidate.Validation, OriginalSHA: candidate.OriginalSHA256, CandidateSHA: candidate.CandidateSHA256}
	if !candidate.Validation.NativeValidated || !candidate.Validation.PreservedSettings || !candidate.Validation.ExactServer {
		return errors.New("native complete-candidate validation was not confirmed")
	}
	if o.Plan {
		return writeModelJSON(os.Stdout, report)
	}
	first, err := runFirstTask(ctx, installed, o.State, o.Workspace)
	if err != nil {
		return err
	}
	report.FirstTask = &first
	if err := localruntime.CheckPath(filepath.Dir(candidate.ActivePath)); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(candidate.ActivePath), 0700); err != nil {
		return errors.New("cannot create native settings directory")
	}
	before, err := configguard.Inspect(candidate.ActivePath)
	if err != nil {
		return err
	}
	if before.Exists != candidate.OriginalExists || before.Exists && before.SHA256 != candidate.OriginalSHA256 {
		return errors.New("client settings changed while preparing setup; concurrent changes preserved")
	}
	data, err := os.ReadFile(candidate.Path)
	if err != nil {
		return errors.New("complete candidate became unavailable")
	}
	validate := func(ctx context.Context, path string) error {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var v onboardingclient.Validation
		if sha(b) == candidate.CandidateSHA256 {
			v, err = onboardingclient.Validate(ctx, opts, b)
		} else if sha(b) == candidate.OriginalSHA256 {
			v, err = onboardingclient.ValidateBaseline(ctx, opts, b)
		} else {
			return errors.New("unexpected complete validation input")
		}
		if err != nil {
			return err
		}
		if !v.NativeValidated {
			return errors.New("native isolated validation is required")
		}
		return nil
	}
	verify := func(ctx context.Context, path string) error {
		if path != candidate.ActivePath {
			return errors.New("unexpected live client path")
		}
		v, err := onboardingclient.CheckLive(ctx, opts)
		if err != nil {
			return err
		}
		if !v.NativeValidated || !v.ExactServer {
			return errors.New("live native client did not confirm the complete candidate")
		}
		return nil
	}
	recoveryRoot := filepath.Join(o.Home, ".local", "share", "questlock", "config-recovery")
	if err := localruntime.CheckPath(recoveryRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(recoveryRoot, 0700); err != nil {
		return errors.New("cannot create private config recovery directory")
	}
	if err := localruntime.CheckPath(recoveryRoot); err != nil {
		return err
	}
	transaction, err := configguard.Apply(ctx, configguard.Options{Target: candidate.ActivePath, Candidate: data, Expected: before, RecoveryDir: recoveryRoot, Executable: self, Timeout: 90 * time.Second, Validate: validate, Verify: verify})
	report.Recovery = &transaction
	if err != nil {
		report.Status = "not_committed"
		_ = writeModelJSON(os.Stdout, report)
		return err
	}
	receipt := setupReceipt{Client: opts.Client, Home: o.Home, CodexHome: o.CodexHome, Native: o.Native, Binary: installed, State: o.State, Workspace: o.Workspace}
	if err := savePrivateJSON(filepath.Join(transaction.Directory, "setup.json"), receipt); err != nil {
		return errors.New("configuration committed; recovery directory exists but setup receipt could not be saved")
	}
	report.Status = "configured"
	report.Next = []string{"Open a fresh local client session and confirm questlock_revise and questlock_delegate are available.", "Ask the assistant to run questlock_revise with request_path first-task.json; this repeats the checked task without a model call.", "Run questlock doctor --client " + o.Client + " to inspect readiness. Local model generation is optional and needs installed Ollama weights."}
	return writeModelJSON(os.Stdout, report)
}

func desktopSetup(ctx context.Context, o setupFlags, self string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("Claude Desktop bundle setup currently supports macOS; native app installation and connection checks are separate")
	}
	if o.Plan {
		return writeModelJSON(os.Stdout, setupReport{Version: version, Client: onboardingclient.ClaudeDesktop, Status: "native_installer_required", Next: []string{"Setup generates a native binary .mcpb extension; the Claude Desktop installer performs native integration.", "No Desktop JSON configuration is overwritten. A protocol self-test is separate from native Desktop connection verification."}})
	}
	if err := ensureRequestWorkspace(o.Workspace); err != nil {
		return err
	}
	first, err := runFirstTask(ctx, self, o.State, o.Workspace)
	if err != nil {
		return err
	}
	if o.Bundle == "" {
		dir := filepath.Join(o.State, "bundles")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		o.Bundle = filepath.Join(dir, "questlock-"+version+"-"+runtime.GOOS+"_"+runtime.GOARCH+".mcpb")
	}
	bundle, err := desktopbundle.Create(ctx, desktopbundle.Options{Executable: self, Output: o.Bundle, Version: version})
	if err != nil {
		return err
	}
	report := setupReport{Version: version, Client: onboardingclient.ClaudeDesktop, Status: "native_install_required", DesktopBundle: &bundle, FirstTask: &first, Next: []string{"In Claude Desktop: Settings > Extensions > Advanced settings > Install Extension, select the generated .mcpb.", "Select private state directory " + o.State + " and approved request directory " + o.Workspace + " in the native installer.", "Confirm the extension is enabled and both Questlock tools are connected. Manifest/protocol checks do not establish native Desktop validation.", "Use Claude Desktop's native extension controls to disable or uninstall it; existing Desktop JSON is unchanged by this command."}}
	if !o.NoOpen {
		if opener, err := exec.LookPath("open"); err == nil {
			_ = exec.CommandContext(ctx, opener, "-a", "Claude", o.Bundle).Run()
		}
	}
	return writeModelJSON(os.Stdout, report)
}

func restoreSetup(directory, self string) error {
	if err := localruntime.CheckPath(directory); err != nil {
		return err
	}
	if _, err := configguard.ReadStatus(directory); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(directory, "setup.json"))
	if err != nil {
		return errors.New("recovery directory has no setup receipt")
	}
	var saved setupReceipt
	if json.Unmarshal(data, &saved) != nil || (saved.Client != onboardingclient.Codex && saved.Client != onboardingclient.ClaudeCode) {
		return errors.New("invalid setup recovery receipt")
	}
	if saved.Client != onboardingclient.Codex {
		return errors.New("Claude Code restore requires native verification that this CLI cannot perform without changing unrelated settings; active config is unchanged, use the private backup with your native client's recovery workflow")
	}
	opts := onboardingclient.Options{Client: saved.Client, HomeDir: saved.Home, CodexHome: saved.CodexHome, Executable: saved.Native, QuestlockPath: saved.Binary, Workspace: saved.Workspace, StateDir: saved.State}
	validate := func(ctx context.Context, path string) error {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		v, err := onboardingclient.ValidateBaseline(ctx, opts, b)
		if err != nil {
			return err
		}
		if !v.NativeValidated {
			return errors.New("native baseline validation required")
		}
		return nil
	}
	verify := func(ctx context.Context, _ string) error {
		v, err := onboardingclient.CheckLiveBaseline(ctx, opts)
		if err != nil {
			return err
		}
		if !v.NativeValidated {
			return errors.New("restored native settings were not confirmed")
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := configguard.Restore(ctx, configguard.RestoreOptions{Directory: directory, Executable: self, Timeout: 90 * time.Second, Validate: validate, Verify: verify})
	if result.Directory != "" {
		_ = writeModelJSON(os.Stdout, result)
	}
	return err
}

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func savePrivateJSON(path string, value any) error {
	if err := localruntime.CheckPath(path); err != nil {
		return err
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func doctorCommand(args []string) error {
	home, defaultState, defaultWorkspace, _, err := onboardingDefaults()
	if err != nil {
		return err
	}
	f := flag.NewFlagSet("doctor", flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	clientName := f.String("client", "codex", "codex, claude-code, or claude-desktop")
	state := f.String("state-dir", defaultState, "private local state to inspect; never auto-started")
	workspace := f.String("workspace", defaultWorkspace, "approved request directory")
	homeFlag := f.String("home", home, "client home to inspect")
	codexHome := f.String("codex-home", "", "explicit Codex home")
	native := f.String("client-bin", "", "absolute native coding-client executable")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return errors.New("doctor accepts flags only")
	}
	if *homeFlag != home {
		if *state == defaultState {
			*state = filepath.Join(*homeFlag, ".local", "share", "questlock", "local")
		}
		if *workspace == defaultWorkspace {
			*workspace = filepath.Join(*homeFlag, ".local", "share", "questlock", "requests")
		}
	}
	if *codexHome == "" {
		*codexHome = filepath.Join(*homeFlag, ".codex")
		if *homeFlag == home && os.Getenv("CODEX_HOME") != "" {
			*codexHome = os.Getenv("CODEX_HOME")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	// Inspect compares the exact command in private copies without starting MCP services.
	clientReport, clientErr := onboardingclient.Inspect(ctx, onboardingclient.Options{Client: onboardingclient.Client(*clientName), HomeDir: *homeFlag, CodexHome: *codexHome, Executable: *native, QuestlockPath: filepath.Join(*homeFlag, ".local", "bin", "questlock"), StateDir: *state, Workspace: *workspace, SkipNativeMCPRead: true})
	brokerReport, brokerErr := localruntime.Inspect(ctx, *state)
	workspaceReady := false
	if localruntime.CheckPath(*workspace) == nil {
		if info, err := os.Stat(*workspace); err == nil {
			workspaceReady = info.IsDir() && info.Mode().Perm()&0022 == 0
		}
	}
	status := "attention_required"
	if clientErr == nil && brokerErr == nil && brokerReport.Status == "ready" && workspaceReady && clientReport.ServerPresent && clientReport.Validation.ExactServer {
		status = "configuration_present"
	}
	next := []string{}
	if *clientName == "claude-desktop" {
		status = "native_validation_required"
		next = append(next, "Check the native Claude Desktop extension connection and both tools; personal Claude Code skills are a separate integration.")
	}
	if brokerReport.Status != "ready" {
		next = append(next, "Run questlock setup --client "+*clientName+" or questlock try to initialize the managed local workflow.")
	}
	if !clientReport.ServerPresent {
		next = append(next, "No raw Questlock server entry was found. Native Desktop extension entries may live outside the raw JSON configuration.")
	}
	report := map[string]any{"version": version, "status": status, "client": clientReport, "local_broker": brokerReport, "request_workspace_ready": workspaceReady, "model_required_for_first_task": false, "native_client_connection_verified": false, "model_calls": 0, "read_only": true, "next_steps": next}
	if err := writeModelJSON(os.Stdout, report); err != nil {
		return err
	}
	if clientErr != nil || brokerErr != nil {
		return errors.New("doctor found an unsafe, invalid, missing, or unavailable component; inspect redacted checks")
	}
	if status != "configuration_present" {
		return errors.New("onboarding is not ready; run setup for the chosen client")
	}
	return nil
}
