package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spfuzzylink/questlock/internal/localruntime"
)

// Plugins use a separate private profile. They never install skills or rewrite
// a client's config; the host owns plugin installation and enablement.
func pluginCommand(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stdout, "questlock plugin serve|try|doctor|stop [--client codex|claude-code] [--state-dir PATH] [--workspace PATH]")
		return nil
	}
	action := args[0]
	if action != "serve" && action != "try" && action != "doctor" && action != "stop" {
		return errors.New("use plugin serve, try, doctor, or stop")
	}
	home, _, _, self, err := onboardingDefaults()
	if err != nil {
		return err
	}
	root := filepath.Join(home, ".local", "share", "questlock", "plugins")
	f := flag.NewFlagSet("plugin "+action, flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	client := f.String("client", "codex", "client label: codex or claude-code")
	state := f.String("state-dir", filepath.Join(root, "local"), "private plugin broker state")
	workspace := f.String("workspace", filepath.Join(root, "requests"), "approved plugin request directory")
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 || (*client != "codex" && *client != "claude-code") {
		return errors.New("plugin requires codex or claude-code and accepts flags only")
	}
	for _, path := range []string{*state, *workspace} {
		if err := localruntime.CheckPath(path); err != nil {
			return err
		}
	}
	switch action {
	case "serve":
		if _, err := writeFirstRequest(*workspace); err != nil {
			return err
		}
		// The existing local worker cannot enable hosted escalation. Its stdout
		// remains exclusively MCP protocol traffic.
		return localWorkerCommand([]string{"--client", *client, "--state-dir", *state, "--workspace", *workspace}, os.Stdin, os.Stdout, os.Stderr)
	case "stop":
		return localCommand([]string{"stop", "--state-dir", *state})
	case "doctor":
		return pluginDoctor(*client, *state, *workspace)
	case "try":
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		first, err := runFirstTask(ctx, self, *state, *workspace)
		if err != nil {
			return err
		}
		repeat, err := runFirstTask(ctx, self, *state, *workspace)
		if err != nil {
			return err
		}
		if !repeat.Result.Reused || first.Result.Artifact == nil || repeat.Result.Artifact == nil || *first.Result.Artifact != *repeat.Result.Artifact {
			return errors.New("plugin first-task exact reuse was not confirmed")
		}
		return writeModelJSON(os.Stdout, map[string]any{"version": version, "status": "completed", "client": *client, "first_task": first, "repeat_task": repeat, "paid_fallback_enabled": false, "model_calls": 0, "native_client_connection_verified": false})
	}
	return nil
}

func pluginDoctor(client, state, workspace string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	broker, brokerErr := localruntime.Inspect(ctx, state)
	workspaceReady := false
	if info, err := os.Stat(workspace); err == nil {
		workspaceReady = info.IsDir() && info.Mode().Perm()&0022 == 0
	}
	fixtureReady := false
	fixturePath := filepath.Join(workspace, "first-task.json")
	if localruntime.CheckPath(fixturePath) == nil {
		if info, err := os.Lstat(fixturePath); err == nil {
			if info.Mode().IsRegular() && info.Mode().Perm()&0077 == 0 && info.Size() < 65536 {
				expected, _ := json.MarshalIndent(firstRequest(), "", "  ")
				data, readErr := os.ReadFile(fixturePath)
				fixtureReady = readErr == nil && bytes.Equal(data, append(expected, '\n'))
			}
		}
	}
	status := "attention_required"
	if brokerErr == nil && broker.Status == "ready" && workspaceReady && fixtureReady {
		status = "ready"
	}
	if err := writeModelJSON(os.Stdout, map[string]any{"version": version, "status": status, "client": client, "state_dir": state, "workspace": workspace, "local_broker": broker, "request_workspace_ready": workspaceReady, "first_task_ready": fixtureReady, "read_only": true, "paid_fallback_enabled": false, "model_calls": 0, "native_client_connection_verified": false}); err != nil {
		return err
	}
	if status != "ready" {
		return errors.New("plugin runtime needs attention; run the packaged setup or try command")
	}
	return nil
}
