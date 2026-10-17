//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

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
		if first.Status != "completed" || !first.MCPProtocolSelfTest || first.NativeClientVerified || len(first.MCPTools) != 2 || !containsName(first.MCPTools, "questlock_revise") || !containsName(first.MCPTools, "questlock_delegate") {
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
