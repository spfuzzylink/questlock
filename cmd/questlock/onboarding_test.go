//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spfuzzylink/questlock/revision"
	"github.com/spfuzzylink/questlock/worker"
	"golang.org/x/sys/unix"
)

type onboardingCLIFixture struct {
	binary, root, home, state, workspace, tmp, bin string
}

func onboardingCLIMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func newOnboardingCLIFixture(t *testing.T, binary string) onboardingCLIFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	onboardingCLIMust(t, err)
	f := onboardingCLIFixture{binary: binary, root: root, home: filepath.Join(root, "home"), state: filepath.Join(root, "state"), workspace: filepath.Join(root, "requests"), tmp: filepath.Join(root, "tmp"), bin: filepath.Join(root, "empty-bin")}
	for _, path := range []string{f.home, f.tmp, f.bin} {
		onboardingCLIMust(t, os.Mkdir(path, 0700))
	}
	t.Cleanup(func() {
		_, _, _ = f.run(3*time.Second, "local", "stop", "--state-dir", f.state)
		// Stop normally reaps the detached service. Exact private-fixture process
		// matching also cleans up a service/worker left by a failed startup test.
		out, _ := exec.Command("/bin/ps", "-axo", "pid=,command=").Output()
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 5 || fields[1] != binary {
				continue
			}
			broker := fields[2] == "__local-broker" && fields[3] == "--state-dir" && fields[4] == f.state
			worker := len(fields) >= 6 && fields[2] == "worker" && fields[3] == "local-serve" && fields[4] == "--state-dir" && fields[5] == f.state
			if broker || worker {
				if pid, err := strconv.Atoi(fields[0]); err == nil {
					if process, err := os.FindProcess(pid); err == nil {
						_ = process.Kill()
					}
				}
			}
		}
	})
	return f
}

func (f onboardingCLIFixture) run(timeout time.Duration, args ...string) ([]byte, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.binary, args...)
	cmd.Dir = f.root
	// No host credentials, real client discovery, or model endpoint settings.
	cmd.Env = []string{"HOME=" + f.home, "CODEX_HOME=" + filepath.Join(f.home, ".codex"), "PATH=" + f.bin, "TMPDIR=" + f.tmp, "GORACE=atexit_sleep_ms=0"}
	cmd.WaitDelay = 3 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		err = fmt.Errorf("CLI deadline: %w", ctx.Err())
	}
	return stdout.Bytes(), stderr.Bytes(), err
}

func (f onboardingCLIFixture) command(t *testing.T, args ...string) []byte {
	t.Helper()
	stdout, stderr, err := f.run(20*time.Second, args...)
	if err != nil {
		t.Fatalf("%v failed: %v\n%s\n%s", args, err, stdout, stderr)
	}
	return stdout
}

func readOnboardingCLIFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	onboardingCLIMust(t, err)
	return b
}

func TestOnboardingCLIIntegration(t *testing.T) {
	buildDir, err := filepath.EvalSymlinks(t.TempDir())
	onboardingCLIMust(t, err)
	binary := filepath.Join(buildDir, "questlock")
	// One real, race-instrumented release-shaped executable for the whole suite;
	// all workers and detached brokers execute this same binary.
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-race", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}

	t.Run("durable edge quest has six offline checks", func(t *testing.T) {
		f := newOnboardingCLIFixture(t, binary)
		var report struct {
			Evidence             string `json:"evidence"`
			ModelCalls           int    `json:"model_calls"`
			RemoteHostsValidated bool   `json:"remote_hosts_validated"`
			Cases                []struct {
				ID         string `json:"id"`
				Complexity string `json:"complexity"`
				Passed     bool   `json:"passed"`
			} `json:"cases"`
		}
		onboardingCLIMust(t, json.Unmarshal(f.command(t, "edge", "quest"), &report))
		if report.Evidence != "loopback_http_and_private_sqlite_fixture" || report.ModelCalls != 0 || report.RemoteHostsValidated || len(report.Cases) != 6 {
			t.Fatalf("invalid evidence: %+v", report)
		}
		counts := map[string]int{}
		ids := map[string]bool{}
		for _, c := range report.Cases {
			if !c.Passed || c.ID == "" || ids[c.ID] {
				t.Fatalf("invalid fixture case: %+v", c)
			}
			ids[c.ID] = true
			counts[c.Complexity]++
		}
		if counts["simple"] != 3 || counts["complex"] != 3 {
			t.Fatal(counts)
		}
		if entries, err := os.ReadDir(f.tmp); err != nil || len(entries) != 0 {
			t.Fatalf("fixture did not clean private state: %v %v", entries, err)
		}
	})

	t.Run("try and exact durable reuse", func(t *testing.T) {
		f := newOnboardingCLIFixture(t, binary)
		var first firstTaskReport
		onboardingCLIMust(t, json.Unmarshal(f.command(t, "try", "--state-dir", f.state, "--workspace", f.workspace), &first))
		if first.Status != "completed" || !first.MCPProtocolSelfTest || first.NativeClientVerified || len(first.MCPTools) != 3 || !containsName(first.MCPTools, "questlock_status") || !containsName(first.MCPTools, "questlock_revise") || !containsName(first.MCPTools, "questlock_delegate") {
			t.Fatalf("first task capabilities/report: %+v", first)
		}
		r := first.Result
		if r.Status != "completed" || r.Reused || !r.Durable || r.GenerationCalled || r.Text != `{"status":"ready","model_calls":0}` || r.Artifact == nil || r.ThisRunUsage.InputTokens != 0 || r.ThisRunUsage.OutputTokens != 0 || r.ProviderCostUSD != 0 || r.OutcomeTracking == nil || r.OutcomeTracking.Status != "recorded" {
			t.Fatalf("first task not durable and zero-generation: %+v", r)
		}
		for _, check := range append(r.SourceChecks, r.Checks...) {
			if !check.Passed {
				t.Fatal("first task declared check failed")
			}
		}
		requestBefore := readOnboardingCLIFile(t, filepath.Join(f.workspace, "first-task.json"))
		metadataBefore := readOnboardingCLIFile(t, filepath.Join(f.state, "broker.json"))
		tokenBefore := readOnboardingCLIFile(t, filepath.Join(f.state, "worker.token"))
		var repeated firstTaskReport
		onboardingCLIMust(t, json.Unmarshal(f.command(t, "try", "--state-dir", f.state, "--workspace", f.workspace), &repeated))
		if !repeated.Result.Reused || repeated.Result.Artifact == nil || *repeated.Result.Artifact != *r.Artifact || repeated.Result.GenerationCalled || repeated.Result.ProviderCostUSD != 0 || repeated.Result.ThisRunUsage.InputTokens != 0 || repeated.Result.ThisRunUsage.OutputTokens != 0 {
			t.Fatalf("repeat did not reuse durable result: %+v", repeated.Result)
		}
		if !bytes.Equal(requestBefore, readOnboardingCLIFile(t, filepath.Join(f.workspace, "first-task.json"))) || !bytes.Equal(metadataBefore, readOnboardingCLIFile(t, filepath.Join(f.state, "broker.json"))) || !bytes.Equal(tokenBefore, readOnboardingCLIFile(t, filepath.Join(f.state, "worker.token"))) {
			t.Fatal("repeat replaced request, shared broker, or credentials")
		}
		if _, err := os.Lstat(filepath.Join(f.home, ".codex")); !os.IsNotExist(err) {
			t.Fatal("try changed native client home")
		}
	})

	t.Run("different first request remains untouched", func(t *testing.T) {
		f := newOnboardingCLIFixture(t, binary)
		onboardingCLIMust(t, os.Mkdir(f.workspace, 0700))
		path := filepath.Join(f.workspace, "first-task.json")
		original := []byte("user-authored request must survive byte for byte\n")
		onboardingCLIMust(t, os.WriteFile(path, original, 0600))
		stdout, stderr, err := f.run(5*time.Second, "try", "--state-dir", f.state, "--workspace", f.workspace)
		if err == nil || !strings.Contains(string(stderr), "first-task.json differs") || len(stdout) != 0 {
			t.Fatalf("differing request accepted or unclear refusal: %v %s %s", err, stdout, stderr)
		}
		if !bytes.Equal(readOnboardingCLIFile(t, path), original) {
			t.Fatal("user's first request was overwritten")
		}
		if _, err := os.Lstat(f.state); !os.IsNotExist(err) {
			t.Fatal("refused request started a broker")
		}
	})

	t.Run("managed local start status stop restart", func(t *testing.T) {
		f := newOnboardingCLIFixture(t, binary)
		var report map[string]any
		onboardingCLIMust(t, json.Unmarshal(f.command(t, "local", "status", "--state-dir", f.state), &report))
		if report["status"] != "not_started" {
			t.Fatal(report)
		}
		if _, err := os.Lstat(f.state); !os.IsNotExist(err) {
			t.Fatal("status provisioned missing runtime")
		}
		onboardingCLIMust(t, json.Unmarshal(f.command(t, "local", "start", "--state-dir", f.state), &report))
		if report["status"] != "ready" || report["loopback"] != true || report["credentials_private"] != true {
			t.Fatal(report)
		}
		metadata := readOnboardingCLIFile(t, filepath.Join(f.state, "broker.json"))
		token := readOnboardingCLIFile(t, filepath.Join(f.state, "worker.token"))
		output := f.command(t, "local", "status", "--state-dir", f.state)
		if bytes.Contains(output, bytes.TrimSpace(token)) {
			t.Fatal("status printed worker credential")
		}
		onboardingCLIMust(t, json.Unmarshal(f.command(t, "local", "start", "--state-dir", f.state), &report))
		if !bytes.Equal(metadata, readOnboardingCLIFile(t, filepath.Join(f.state, "broker.json"))) {
			t.Fatal("local start replaced healthy owner")
		}
		onboardingCLIMust(t, json.Unmarshal(f.command(t, "local", "stop", "--state-dir", f.state), &report))
		if report["status"] != "stop_requested" || report["data_removed"] != false {
			t.Fatal(report)
		}
		waitOnboardingCLIStopped(t, f.state)
		if _, err := os.Stat(filepath.Join(f.state, "state.db")); err != nil {
			t.Fatal("local stop deleted persistent store")
		}
		f.command(t, "local", "start", "--state-dir", f.state)
		if !bytes.Equal(token, readOnboardingCLIFile(t, filepath.Join(f.state, "worker.token"))) {
			t.Fatal("local restart rotated credential")
		}
	})

	t.Run("fresh custom profile discovers root and runs checked task without managed defaults", func(t *testing.T) {
		f := newOnboardingCLIFixture(t, binary)
		broker, err := newModelQuestFixture(t.Context())
		onboardingCLIMust(t, err)
		defer broker.close()
		var brokerCalls atomic.Int32
		endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			brokerCalls.Add(1)
			broker.server.Config.Handler.ServeHTTP(w, r)
		}))
		defer endpoint.Close()
		onboardingCLIMust(t, os.Mkdir(f.workspace, 0700))
		// The fixture native binary must not run: doctor statically inspects
		// Claude Code in isolation and never asks MCP get to start this entry.
		native := filepath.Join(f.bin, "fixture-claude")
		onboardingCLIMust(t, os.WriteFile(native, []byte("#!/bin/sh\nexit 97\n"), 0700))
		type serverEntry struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			Type    string            `json:"type"`
		}
		entry := serverEntry{Command: binary, Args: []string{"worker", "serve", "--workspace", f.workspace, "--broker", endpoint.URL, "--profile-id", "approved-custom-writing"}, Env: map[string]string{"QUESTLOCK_TOKEN": broker.tokens[0]}, Type: "stdio"}
		config := struct {
			Servers map[string]serverEntry `json:"mcpServers"`
			Private string                 `json:"private_sentinel"`
		}{Servers: map[string]serverEntry{"questlock": entry}, Private: "PRIVATE_UNRELATED_CONFIG"}
		original, err := json.Marshal(config)
		onboardingCLIMust(t, err)
		configPath := filepath.Join(f.home, ".claude.json")
		onboardingCLIMust(t, os.WriteFile(configPath, original, 0600))
		beforeSHA := sha(original)
		stdout, stderr, err := f.run(8*time.Second, "doctor", "--client", "claude-code", "--home", f.home, "--client-bin", native)
		if err == nil {
			t.Fatal("doctor claimed to verify the configured custom profile")
		}
		var doctor struct {
			Status  string `json:"status"`
			Scope   string `json:"diagnostic_scope"`
			Managed struct {
				Status string `json:"status"`
			} `json:"managed_profile"`
			Client struct {
				Status string `json:"status"`
			} `json:"client"`
			Next []string `json:"next_steps"`
		}
		onboardingCLIMust(t, json.Unmarshal(stdout, &doctor))
		if doctor.Status != "configured_custom_profile_unverified" || doctor.Client.Status != doctor.Status || doctor.Managed.Status != "managed_profile_not_initialized" || doctor.Scope != "selected_managed_profile" {
			t.Fatalf("custom and managed states were conflated: %+v", doctor)
		}
		for _, step := range doctor.Next {
			if strings.Contains(step, "questlock setup") || strings.Contains(step, "questlock try") {
				t.Fatal("doctor prescribed replacing a custom profile")
			}
		}
		if bytes.Contains(stdout, []byte(config.Private)) || bytes.Contains(stderr, []byte(config.Private)) || bytes.Contains(stdout, []byte(broker.tokens[0])) {
			t.Fatal("doctor disclosed configuration content or a credential")
		}
		// A fresh caller uses only the chosen server's exact command/args and
		// status response; no default broker/root or client-config dump is used.
		var installed struct {
			Servers map[string]serverEntry `json:"mcpServers"`
		}
		onboardingCLIMust(t, json.Unmarshal(readOnboardingCLIFile(t, configPath), &installed))
		selected := installed.Servers["questlock"]
		command := exec.Command(selected.Command, selected.Args...)
		command.Dir = f.root
		command.Env = []string{"HOME=" + f.home, "PATH=" + f.bin, "TMPDIR=" + f.tmp, "GORACE=atexit_sleep_ms=0"}
		for key, value := range selected.Env {
			command.Env = append(command.Env, key+"="+value)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
		defer cancel()
		c := mcp.NewClient(&mcp.Implementation{Name: "fresh-chat-fixture", Version: "1"}, nil)
		session, err := c.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
		onboardingCLIMust(t, err)
		defer session.Close()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: worker.StatusToolName, Arguments: map[string]any{}})
		onboardingCLIMust(t, err)
		var status worker.Status
		data, err := json.Marshal(result.StructuredContent)
		onboardingCLIMust(t, err)
		onboardingCLIMust(t, json.Unmarshal(data, &status))
		if result.IsError || status.RequestRoot != f.workspace || status.RuntimeVersion != version || status.Profile.ID != "approved-custom-writing" || status.Profile.Kind != "custom" || status.Policy.AllowPaid || status.Readiness.Broker != "not_checked" || status.ModelCalls != 0 || brokerCalls.Load() != 0 {
			t.Fatalf("discovery did not identify the actual custom contract without HTTP calls: %+v calls=%d", status, brokerCalls.Load())
		}
		request, err := json.Marshal(firstRequest())
		onboardingCLIMust(t, err)
		approvedPath := filepath.Join(status.RequestRoot, "fresh-chat-approved.json")
		onboardingCLIMust(t, os.WriteFile(approvedPath, request, 0600))
		result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: worker.ReviseToolName, Arguments: map[string]any{"request_path": approvedPath}})
		onboardingCLIMust(t, err)
		data, err = json.Marshal(result.StructuredContent)
		onboardingCLIMust(t, err)
		var revised revision.Result
		onboardingCLIMust(t, json.Unmarshal(data, &revised))
		if result.IsError || revised.Status != "completed" || !revised.Durable || revised.GenerationCalled || revised.ProviderCostUSD != 0 || revised.ThisRunUsage.InputTokens != 0 || revised.ThisRunUsage.OutputTokens != 0 || brokerCalls.Load() == 0 {
			t.Fatalf("bounded custom task did not use the configured broker without generation: %+v", revised)
		}
		if beforeSHA != sha(readOnboardingCLIFile(t, configPath)) {
			t.Fatal("custom configuration changed during discovery, doctor or task execution")
		}
		for _, absent := range []string{f.state, filepath.Join(f.home, ".local"), filepath.Join(f.home, ".codex")} {
			if _, err := os.Lstat(absent); !os.IsNotExist(err) {
				t.Fatal("custom workflow created managed-default state or another client configuration")
			}
		}
	})

	t.Run("doctor is read only before and after runtime startup", func(t *testing.T) {
		f := newOnboardingCLIFixture(t, binary)
		codexHome := filepath.Join(f.home, ".codex")
		onboardingCLIMust(t, os.Mkdir(codexHome, 0700))
		onboardingCLIMust(t, os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("notify = [\"private-hook\", \"must-not-print-config-value\"]\n"), 0600))
		for _, started := range []bool{false, true} {
			if started {
				f.command(t, "local", "start", "--state-dir", f.state)
			}
			before := snapshotOnboardingCLITree(t, f.home)
			var brokerBefore, tokenBefore []byte
			if started {
				brokerBefore = readOnboardingCLIFile(t, filepath.Join(f.state, "broker.json"))
				tokenBefore = readOnboardingCLIFile(t, filepath.Join(f.state, "worker.token"))
			}
			stdout, stderr, err := f.run(8*time.Second, "doctor", "--home", f.home, "--codex-home", codexHome, "--state-dir", f.state, "--workspace", f.workspace, "--client-bin", filepath.Join(f.bin, "no-native-client"))
			if err == nil {
				t.Fatal("doctor declared missing native integration ready")
			}
			var report map[string]any
			onboardingCLIMust(t, json.Unmarshal(stdout, &report))
			if report["read_only"] != true || report["model_calls"] != float64(0) || report["model_required_for_first_task"] != false || report["native_client_connection_verified"] != false {
				t.Fatal(report)
			}
			if bytes.Contains(stdout, []byte("must-not-print-config-value")) || bytes.Contains(stderr, []byte("must-not-print-config-value")) {
				t.Fatal("doctor revealed native settings values")
			}
			if !reflect.DeepEqual(before, snapshotOnboardingCLITree(t, f.home)) {
				t.Fatal("doctor changed client home")
			}
			if _, err := os.Lstat(f.workspace); !os.IsNotExist(err) {
				t.Fatal("doctor created request workspace")
			}
			if started {
				if !bytes.Equal(brokerBefore, readOnboardingCLIFile(t, filepath.Join(f.state, "broker.json"))) || !bytes.Equal(tokenBefore, readOnboardingCLIFile(t, filepath.Join(f.state, "worker.token"))) {
					t.Fatal("doctor replaced broker or credential")
				}
			} else if _, err := os.Lstat(f.state); !os.IsNotExist(err) {
				t.Fatal("doctor started local runtime")
			}
		}
	})
}

func waitOnboardingCLIStopped(t *testing.T, state string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		file, err := os.OpenFile(filepath.Join(state, "owner.lock"), os.O_RDWR, 0600)
		if err == nil {
			err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
			file.Close()
			if err == nil {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("local stop did not release broker owner lock")
}

func snapshotOnboardingCLITree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	onboardingCLIMust(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := fmt.Sprintf("%v:%v", info.Mode(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += ":" + string(b)
		}
		result[path] = value
		return nil
	}))
	return result
}
