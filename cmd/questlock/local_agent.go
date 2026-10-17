package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/onboardingclient"
)

const localAgentOllamaURL = "http://127.0.0.1:11434"

func localAgentCommand(args []string) error {
	return localAgentRun(args, os.Stdout, os.Stderr, func(ctx context.Context, model string) error {
		_, err := inference.NewOllamaProviderContext(ctx, localAgentOllamaURL, model)
		return err
	}, func(cmd *exec.Cmd) error { return cmd.Run() })
}

// Only the installed-model preflight and process runner are injected in tests.
// Neither caller arguments nor inherited environment can select a remote URL.
func localAgentRun(args []string, output, diagnostics io.Writer, check func(context.Context, string) error, run func(*exec.Cmd) error) error {
	f := flag.NewFlagSet("local-agent", flag.ContinueOnError)
	f.SetOutput(diagnostics)
	model := f.String("model", "", "required installed local Ollama model; never downloaded automatically")
	native := f.String("client-bin", "", "optional absolute Codex CLI executable")
	workspace := f.String("workspace", "", "required absolute canonical existing workspace directory")
	prompt := f.String("prompt", "", "required task text, at most 16384 UTF-8 bytes")
	sandbox := f.String("sandbox", "read-only", "read-only or workspace-write")
	timeout := f.Duration("timeout", 5*time.Minute, "run deadline, greater than zero and at most 30m")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid local-agent flags")
	}
	if f.NArg() != 0 || *model == "" || len(*model) > 128 || strings.HasPrefix(*model, "-") || *model != strings.TrimSpace(*model) {
		return errors.New("local-agent requires an explicit installed model and accepts no positional arguments")
	}
	if len(*prompt) == 0 || len(*prompt) > 16384 || strings.TrimSpace(*prompt) == "" || *prompt == "-" || strings.ContainsRune(*prompt, 0) || !utf8.ValidString(*prompt) {
		return errors.New("prompt must be nonempty UTF-8 task text of at most 16384 bytes")
	}
	if *sandbox != "read-only" && *sandbox != "workspace-write" {
		return errors.New("sandbox must be read-only or workspace-write")
	}
	if *timeout <= 0 || *timeout > 30*time.Minute {
		return errors.New("timeout must be greater than zero and at most 30m")
	}
	if !filepath.IsAbs(*workspace) || filepath.Clean(*workspace) != *workspace {
		return errors.New("workspace must be an absolute canonical existing directory")
	}
	resolved, err := filepath.EvalSymlinks(*workspace)
	if err != nil || resolved != *workspace {
		return errors.New("workspace must be canonical and must not traverse symlinks")
	}
	if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
		return errors.New("workspace must be an existing directory")
	}
	home, err := resolvedTempDir("questlock-local-agent-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(home)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	report, err := onboardingclient.Inspect(ctx, onboardingclient.Options{Client: onboardingclient.Codex, HomeDir: home, Executable: *native})
	if err != nil || !report.ClientAvailable {
		return errors.New("local-agent requires an installed Codex CLI executable")
	}
	if err := check(ctx, *model); err != nil {
		return fmt.Errorf("installed local Ollama model preflight failed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	codexHome := filepath.Join(home, ".codex")
	if err := os.Mkdir(codexHome, 0700); err != nil {
		return errors.New("cannot create isolated local-agent settings directory")
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("approval_policy = \"never\"\ncheck_for_update_on_startup = false\n"), 0600); err != nil {
		return errors.New("cannot create private local-agent settings")
	}
	command := []string{"exec", "--config", `model_provider="ollama"`, "--oss", "--local-provider", "ollama", "--model", *model, "--sandbox", *sandbox, "--skip-git-repo-check", "--json", "--cd", resolved, "--", *prompt}
	cmd := exec.CommandContext(ctx, report.Executable, command...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = resolved, output, diagnostics
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CODEX_HOME=" + codexHome, "OLLAMA_BASE_URL=" + localAgentOllamaURL}
	fmt.Fprintln(diagnostics, "Local Codex/Ollama run: JSON token counts describe the local tokenizer, not hosted Codex usage.")
	return run(cmd)
}

func resolvedTempDir(prefix string) (string, error) {
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", errors.New("cannot create private local-agent home")
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", errors.New("cannot resolve private local-agent home")
	}
	return resolved, nil
}
