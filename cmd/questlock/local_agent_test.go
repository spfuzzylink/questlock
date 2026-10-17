package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func localAgentFixture(t *testing.T) ([]string, string) {
	t.Helper()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(workspace, "codex")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return []string{"--model", "installed:small", "--client-bin", binary, "--workspace", workspace, "--prompt", "Read the fixture."}, workspace
}

func TestLocalAgentNativeCommandIsolation(t *testing.T) {
	args, workspace := localAgentFixture(t)
	args[len(args)-1] = "--model hosted --config model_provider=openai"
	for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "HTTP_PROXY", "HTTPS_PROXY", "CODEX_HOME", "OLLAMA_BASE_URL"} {
		t.Setenv(key, "must-not-inherit")
	}
	var diagnostics bytes.Buffer
	var privateHome string
	checked, ran := false, false
	err := localAgentRun(args, io.Discard, &diagnostics, func(ctx context.Context, model string) error {
		checked = true
		if model != "installed:small" {
			t.Fatal("wrong required model")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Minute {
			t.Fatal("missing bounded deadline")
		}
		return nil
	}, func(cmd *exec.Cmd) error {
		ran = true
		if !checked {
			t.Fatal("process ran before installed-model validation")
		}
		want := []string{args[3], "exec", "--config", `model_provider="ollama"`, "--oss", "--local-provider", "ollama", "--model", "installed:small", "--sandbox", "read-only", "--skip-git-repo-check", "--json", "--cd", workspace, "--", args[len(args)-1]}
		if !reflect.DeepEqual(cmd.Args, want) || cmd.Dir != workspace || cmd.Stdin != nil {
			t.Fatalf("unexpected native invocation: %q", cmd.Args)
		}
		env := map[string]string{}
		for _, entry := range cmd.Env {
			key, value, _ := strings.Cut(entry, "=")
			env[key] = value
		}
		if len(env) != 4 || env["OLLAMA_BASE_URL"] != localAgentOllamaURL || env["HOME"] == os.Getenv("HOME") {
			t.Fatalf("unexpected subprocess environment keys: %v", cmd.Env)
		}
		privateHome = env["HOME"]
		if !filepath.IsAbs(privateHome) || env["CODEX_HOME"] != filepath.Join(privateHome, ".codex") {
			t.Fatal("unisolated native home")
		}
		for path, want := range map[string]os.FileMode{privateHome: 0700, env["CODEX_HOME"]: 0700, filepath.Join(env["CODEX_HOME"], "config.toml"): 0600} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != want {
				t.Fatalf("non-private native settings: %s %v", path, err)
			}
		}
		data, _ := os.ReadFile(filepath.Join(env["CODEX_HOME"], "config.toml"))
		if strings.Contains(string(data), "notify") || strings.Contains(string(data), "auth") {
			t.Fatal("active settings imported")
		}
		return nil
	})
	if err != nil || !ran || !strings.Contains(diagnostics.String(), "local tokenizer") {
		t.Fatalf("local launch failed: %v %s", err, diagnostics.String())
	}
	if _, err := os.Stat(privateHome); !os.IsNotExist(err) {
		t.Fatal("isolated native home was not cleaned up")
	}
}

func TestLocalAgentRejectsOverridesAndInvalidBounds(t *testing.T) {
	for _, extra := range [][]string{{"--provider", "openai"}, {"--allow-paid"}, {"--sandbox", "danger-full-access"}, {"--timeout", "31m"}, {"--timeout", "0"}, {"--prompt", ""}, {"--prompt", "-"}, {"--prompt", strings.Repeat("x", 16385)}, {"--model", ""}, {"--model", "--config"}, {"--workspace", "relative"}, {"unforwarded-positional"}} {
		args, _ := localAgentFixture(t)
		args = append(args, extra...)
		called := false
		err := localAgentRun(args, io.Discard, io.Discard, func(context.Context, string) error { called = true; return nil }, func(*exec.Cmd) error { called = true; return nil })
		if err == nil || called {
			t.Fatalf("unsafe flags reached preflight/process: %q %v", extra, err)
		}
	}
}

func TestLocalAgentPreflightFailureNeverStartsClient(t *testing.T) {
	args, _ := localAgentFixture(t)
	sentinel := errors.New("local model is not installed")
	err := localAgentRun(args, io.Discard, io.Discard, func(context.Context, string) error { return sentinel }, func(*exec.Cmd) error { t.Fatal("client started after failed preflight"); return nil })
	if !errors.Is(err, sentinel) {
		t.Fatalf("preflight error lost: %v", err)
	}
}

func TestLocalAgentWorkspaceWriteAndCanonicalPath(t *testing.T) {
	args, workspace := localAgentFixture(t)
	args = append(args, "--sandbox", "workspace-write")
	if err := localAgentRun(args, io.Discard, io.Discard, func(context.Context, string) error { return nil }, func(cmd *exec.Cmd) error {
		if cmd.Args[10] != "workspace-write" {
			t.Fatal("explicit write sandbox ignored")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "link")
	if err := os.Symlink(workspace, link); err != nil {
		t.Fatal(err)
	}
	args = append(args, "--workspace", link)
	if err := localAgentRun(args, io.Discard, io.Discard, func(context.Context, string) error { t.Fatal("symlink workspace reached preflight"); return nil }, func(*exec.Cmd) error { return nil }); err == nil {
		t.Fatal("noncanonical workspace accepted")
	}
}
