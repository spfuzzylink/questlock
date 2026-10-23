//go:build darwin || linux

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestFirstRequestConcurrentColdStart(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	onboardingCLIMust(t, err)
	workspace := filepath.Join(root, "requests")
	onboardingCLIMust(t, os.Mkdir(workspace, 0700))
	start := make(chan struct{})
	errors := make(chan error, 64)
	var writers sync.WaitGroup
	for i := 0; i < cap(errors); i++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			_, err := writeFirstRequest(workspace)
			errors <- err
		}()
	}
	close(start)
	writers.Wait()
	close(errors)
	for err := range errors {
		onboardingCLIMust(t, err)
	}
	expected, _ := json.MarshalIndent(firstRequest(), "", "  ")
	if string(readOnboardingCLIFile(t, filepath.Join(workspace, "first-task.json"))) != string(append(expected, '\n')) {
		t.Fatal("cold-start fixture was incomplete")
	}
	staged, err := filepath.Glob(filepath.Join(workspace, ".first-task-*"))
	onboardingCLIMust(t, err)
	if len(staged) != 0 {
		t.Fatal("fixture staging files were not cleaned up")
	}
}

func TestPluginCLIIntegration(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	onboardingCLIMust(t, err)
	binary := filepath.Join(root, "questlock")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-race", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build plugin runtime: %v\n%s", err, out)
	}
	t.Run("shared checked work without client config or model access", func(t *testing.T) {
		f := newOnboardingCLIFixture(t, binary)
		var firstArtifact json.RawMessage
		for i, client := range []string{"codex", "claude-code"} {
			var got struct {
				Status           string          `json:"status"`
				First            firstTaskReport `json:"first_task"`
				Repeat           firstTaskReport `json:"repeat_task"`
				Paid             bool            `json:"paid_fallback_enabled"`
				NativeConnection bool            `json:"native_client_connection_verified"`
				ModelCalls       int             `json:"model_calls"`
			}
			onboardingCLIMust(t, json.Unmarshal(f.command(t, "plugin", "try", "--client", client, "--state-dir", f.state, "--workspace", f.workspace), &got))
			if got.Status != "completed" || got.Paid || got.NativeConnection || got.ModelCalls != 0 || !got.Repeat.Result.Reused || got.First.Result.Reused != (i == 1) {
				t.Fatalf("unexpected plugin proof: %+v", got)
			}
			if !got.First.MCPProtocolSelfTest || !got.Repeat.MCPProtocolSelfTest || got.First.Result.Artifact == nil || got.Repeat.Result.Artifact == nil || *got.First.Result.Artifact != *got.Repeat.Result.Artifact {
				t.Fatal("both MCP runs must return the same durable artifact")
			}
			artifact, _ := json.Marshal(got.First.Result.Artifact)
			if i == 1 && string(artifact) != string(firstArtifact) {
				t.Fatal("the two client labels failed to share the exact result")
			}
			firstArtifact = artifact
		}
		var doctor struct {
			Status   string `json:"status"`
			ReadOnly bool   `json:"read_only"`
			Ready    bool   `json:"first_task_ready"`
		}
		onboardingCLIMust(t, json.Unmarshal(f.command(t, "plugin", "doctor", "--state-dir", f.state, "--workspace", f.workspace), &doctor))
		if doctor.Status != "ready" || !doctor.ReadOnly || !doctor.Ready {
			t.Fatalf("doctor: %+v", doctor)
		}
		for _, dir := range []string{".codex", ".claude"} {
			if _, err := os.Stat(filepath.Join(f.home, dir)); !os.IsNotExist(err) {
				t.Fatal("plugin wrote native client settings")
			}
		}
		path := filepath.Join(f.workspace, "first-task.json")
		onboardingCLIMust(t, os.WriteFile(path, []byte("operator's different fixture"), 0600))
		out, _, err := f.run(10*time.Second, "plugin", "doctor", "--state-dir", f.state, "--workspace", f.workspace)
		if err == nil {
			t.Fatal("doctor accepted a changed first-task fixture")
		}
		onboardingCLIMust(t, json.Unmarshal(out, &doctor))
		if doctor.Ready || doctor.Status != "attention_required" {
			t.Fatalf("changed doctor: %+v", doctor)
		}
		_, _, err = f.run(10*time.Second, "plugin", "serve", "--state-dir", f.state, "--workspace", f.workspace)
		if err == nil || string(readOnboardingCLIFile(t, path)) != "operator's different fixture" {
			t.Fatal("startup replaced operator data")
		}
	})
	t.Run("unsupported paid escalation and unsafe paths stop before startup", func(t *testing.T) {
		f := newOnboardingCLIFixture(t, binary)
		for _, args := range [][]string{
			{"serve", "--allow-paid"}, {"serve", "--client", "other"},
			{"try", "--workspace", "relative"}, {"serve", "unexpected"},
		} {
			if _, _, err := f.run(10*time.Second, append([]string{"plugin"}, args...)...); err == nil {
				t.Fatalf("accepted %v", args)
			}
		}
		if _, err := os.Stat(filepath.Join(f.home, ".local")); !os.IsNotExist(err) {
			t.Fatal("invalid command created plugin state")
		}
		var doctor map[string]any
		out, _, err := f.run(10*time.Second, "plugin", "doctor")
		if err == nil {
			t.Fatal("fresh doctor should require setup")
		}
		onboardingCLIMust(t, json.Unmarshal(out, &doctor))
		if doctor["read_only"] != true {
			t.Fatal("doctor is not read-only")
		}
		if _, err := os.Stat(filepath.Join(f.home, ".local")); !os.IsNotExist(err) {
			t.Fatal("doctor created state")
		}
	})
}
