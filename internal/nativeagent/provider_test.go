//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package nativeagent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/inference"
)

const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func localFixture(t *testing.T) (*inference.OllamaProvider, func(context.Context) (*inference.OllamaProvider, error), *atomic.Bool) {
	t.Helper()
	changed := new(atomic.Bool)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" {
			t.Error("credentials reached local metadata fixture")
		}
		switch r.URL.Path {
		case "/api/tags":
			d := digest
			if changed.Load() {
				d = strings.Repeat("b", 64)
			}
			io.WriteString(w, `{"models":[{"name":"qwen3.5:4b","digest":"`+d+`"}]}`)
		case "/api/show":
			io.WriteString(w, `{"template":"{{.Prompt}}","system":"","capabilities":["completion"],"model_info":{"general.architecture":"qwen","qwen.context_length":65536}}`)
		default:
			t.Error("metadata preflight attempted generation or another endpoint")
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	refresh := func(ctx context.Context) (*inference.OllamaProvider, error) {
		return inference.NewOllamaProviderContext(ctx, srv.URL, "qwen3.5:4b")
	}
	p, err := refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return p, refresh, changed
}

func fakeClient(t *testing.T, body string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "codex-fixture")
	log := filepath.Join(dir, "captured")
	// Only synthetic test paths are embedded; no shell interpolation is applied
	// to a caller's prompt or command arguments.
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf 'codex-cli 0.163.0-alpha.10.2\\n'; exit 0; fi\n" + strings.ReplaceAll(body, "CAPTURE", log)
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path, log
}

func testProvider(t *testing.T, body string) (*Provider, string, *atomic.Bool) {
	t.Helper()
	local, refresh, changed := localFixture(t)
	path, log := fakeClient(t, body)
	p, err := newWithLocal(context.Background(), path, "qwen3.5:4b", 3*time.Second, local, refresh)
	if err != nil {
		t.Fatal(err)
	}
	return p, log, changed
}

func testRequest() inference.Request {
	return inference.Request{Provider: "ollama", Model: "qwen3.5:4b", Prefix: "Use only the supplied synthetic facts.", Input: "Return the synthetic approved answer.", MaxOutputTokens: 64, ContextWindow: 32768, CacheMode: "default"}
}

func eventsScript() string {
	return "/bin/cat <<'QUESTLOCK_EVENTS'\n" + validEvents + "QUESTLOCK_EVENTS\n"
}

func TestNativeClientIsIsolatedAndPinned(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "synthetic-must-not-leak")
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-must-not-leak")
	t.Setenv("HTTP_PROXY", "synthetic-must-not-leak")
	t.Setenv("CODEX_HOME", t.TempDir())
	body := "/usr/bin/env > 'CAPTURE.env'\nprintf '%s\\n' \"$@\" > 'CAPTURE.args'\n/bin/cat > 'CAPTURE.stdin'\n/bin/cat \"$CODEX_HOME/config.toml\" > 'CAPTURE.config'\n" + eventsScript()
	p, log, _ := testProvider(t, body)
	req := testRequest()
	count, err := p.CountInputTokens(context.Background(), req)
	if err != nil || count < HarnessReserve || count > req.ContextWindow {
		t.Fatalf("count=%d %v", count, err)
	}
	r, err := p.Generate(context.Background(), req)
	if err != nil || !r.Complete || r.Usage.OutputTokens != 20 {
		t.Fatalf("response=%+v %v", r, err)
	}
	read := func(suffix string) string {
		t.Helper()
		data, err := os.ReadFile(log + suffix)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	env, args, prompt, config := read(".env"), read(".args"), read(".stdin"), read(".config")
	for _, forbidden := range []string{"OPENAI", "ANTHROPIC", "PROXY", "synthetic-must-not-leak"} {
		if strings.Contains(env, forbidden) {
			t.Fatalf("inherited sensitive environment key: %s", forbidden)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(env), "\n") {
		key := strings.SplitN(line, "=", 2)[0]
		if key != "PATH" && key != "HOME" && key != "CODEX_HOME" && key != "OLLAMA_BASE_URL" && key != "PWD" && key != "SHLVL" && key != "_" {
			t.Fatalf("unexpected child environment key %q", key)
		}
	}
	for _, arg := range []string{"--oss\n", "--local-provider\nollama\n", "--sandbox\nread-only\n", "--skip-git-repo-check\n", "--json\n", "--ephemeral\n", "--ignore-rules\n", "--strict-config\n", "--disable\nshell_tool\n", "--disable\nunbounded_connection_retries\n", "--enable\nskip_host_skill_discovery\n", "--\n-\n"} {
		if !strings.Contains(args, arg) {
			t.Fatalf("missing native argument %q", arg)
		}
	}
	if strings.Contains(args, req.Input) || !strings.Contains(prompt, req.Input) || !strings.Contains(prompt, `"instructions":`) {
		t.Fatal("prompt was not sent solely through structured stdin")
	}
	for _, setting := range []string{`approval_policy = "never"`, `check_for_update_on_startup = false`, `suppress_unstable_features_warning = true`, `project_doc_max_bytes = 0`, `web_search = "disabled"`, `model_context_window = 32768`} {
		if !strings.Contains(config, setting) {
			t.Fatalf("missing policy %q", setting)
		}
	}
	for _, line := range strings.Split(env, "\n") {
		if strings.HasPrefix(line, "HOME=") || strings.HasPrefix(line, "CODEX_HOME=") {
			path := strings.SplitN(line, "=", 2)[1]
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("private native directory was retained")
			}
		}
	}
	if p.ModelDigest() != digest || p.RuntimeVersion() != "codex-cli 0.163.0-alpha.10.2" || !strings.Contains(p.Identity(), p.executableDigest) || !strings.Contains(p.Identity(), p.local.Identity()) {
		t.Fatal("native/model identity is incomplete")
	}
}

func TestNativePreflightRejectsUnsupportedRequestsWithoutInvokingClient(t *testing.T) {
	p, log, _ := testProvider(t, "printf called > 'CAPTURE.called'\n"+eventsScript())
	for name, mutate := range map[string]func(*inference.Request){
		"hosted": func(r *inference.Request) { r.Provider = "openai" }, "model": func(r *inference.Request) { r.Model = "other:4b" }, "stream": func(r *inference.Request) { r.Stream = true }, "reasoning": func(r *inference.Request) { r.ReasoningEffort = "high" }, "cache": func(r *inference.Request) { r.CacheMode = "prefix" }, "empty": func(r *inference.Request) { r.Input = "" }, "invalid UTF8": func(r *inference.Request) { r.Prefix = "\xff" }, "huge": func(r *inference.Request) { r.Input = strings.Repeat("x", maxPromptBytes+1) }, "output": func(r *inference.Request) { r.MaxOutputTokens = 0 }, "context": func(r *inference.Request) { r.ContextWindow = 65537 }, "harness reserve": func(r *inference.Request) { r.ContextWindow = 8192 }, "hosted options": func(r *inference.Request) { r.OpenRouter = &inference.OpenRouterConfig{} },
	} {
		t.Run(name, func(t *testing.T) {
			req := testRequest()
			mutate(&req)
			if _, err := p.CountInputTokens(context.Background(), req); err == nil {
				t.Fatal("preflight accepted invalid request")
			}
			if _, err := p.Generate(context.Background(), req); err == nil {
				t.Fatal("generation accepted invalid request")
			}
		})
	}
	if _, err := os.Stat(log + ".called"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid request invoked native client")
	}
}

func TestNativeRejectsExecutableAndModelChanges(t *testing.T) {
	p, _, changed := testProvider(t, eventsScript())
	changed.Store(true)
	if err := p.RefreshIdentity(context.Background()); err == nil {
		t.Fatal("model mutation accepted")
	}
	changed.Store(false)
	if err := os.WriteFile(p.executable, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := p.RefreshIdentity(context.Background()); err == nil {
		t.Fatal("executable mutation accepted")
	}
	if _, err := p.Generate(context.Background(), testRequest()); err == nil {
		t.Fatal("changed executable invoked")
	}
}

func TestNativeExecutableBoundaryDoesNotBlockOnFIFO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(path, 0700); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := inspectExecutable(path); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("executable inspection blocked on FIFO")
	}
	for _, path := range []string{"relative-codex", dir} {
		if _, _, err := inspectExecutable(path); err == nil {
			t.Fatal("non-executable path accepted")
		}
	}
	regular := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(regular, []byte("text"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := inspectExecutable(regular); err == nil {
		t.Fatal("non-executable regular file accepted")
	}
	for _, version := range []string{"codex-cli 0.164.0", "codex-cli 0.163.01", "codex-cli 0.163.0\nsecret", "other-client 0.163.0"} {
		if validVersion(version) {
			t.Fatal("unsupported native version accepted")
		}
	}
}

func TestNativeZeroExitWithoutFinalFailsAndActualOutputCapIsReported(t *testing.T) {
	p, _, _ := testProvider(t, "printf '{\"type\":\"thread.started\",\"thread_id\":\"t\"}\\n'\n")
	if r, err := p.Generate(context.Background(), testRequest()); err == nil || r.Complete {
		t.Fatal("zero exit without final answer accepted")
	}
	p, _, _ = testProvider(t, eventsScript())
	req := testRequest()
	req.MaxOutputTokens = 10
	r, err := p.Generate(context.Background(), req)
	if err != nil || r.Complete || r.StopReason != "output_limit_exceeded" || r.Usage.OutputTokens != 20 {
		t.Fatalf("actual overrun lost: %+v %v", r, err)
	}
}

func TestNativeCancellationKillsDescendants(t *testing.T) {
	p, log, _ := testProvider(t, "(/bin/sleep 0.5; printf leaked > 'CAPTURE.late') &\nprintf ready > 'CAPTURE.ready'\nwait\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := p.Generate(ctx, testRequest()); done <- err }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(log + ".ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake client never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not stop native process group")
	}
	time.Sleep(650 * time.Millisecond)
	if _, err := os.Stat(log + ".late"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("late descendant escaped cancellation")
	}
}

func TestNativeOutputLimitsStopTheProcess(t *testing.T) {
	for name, body := range map[string]string{"line": "/usr/bin/head -c 1048578 /dev/zero | /usr/bin/tr '\\000' x\n/bin/sleep 10\n", "total": "/usr/bin/yes '{\"type\":\"turn.started\"}'\n"} {
		t.Run(name, func(t *testing.T) {
			p, _, _ := testProvider(t, body)
			p.timeout = 10 * time.Second
			started := time.Now()
			if _, err := p.Generate(context.Background(), testRequest()); err == nil || !strings.Contains(err.Error(), "limit") {
				t.Fatalf("limit=%v", err)
			}
			// Pipe teardown can consume the explicit two-second WaitDelay,
			// particularly with instrumented I/O under the race detector.
			if time.Since(started) > 5*time.Second {
				t.Fatal("output overflow waited for native timeout")
			}
		})
	}
}

// Opt-in native syntax validation performs no model turn, login, config swap,
// or network request. Ordinary tests use only the synthetic executable above.
func TestInstalledNativePolicySyntax(t *testing.T) {
	path := os.Getenv("QUESTLOCK_TEST_NATIVE_CODEX")
	if path == "" {
		t.Skip("native syntax validation is explicit")
	}
	p := &Provider{executable: path}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := p.invoke(ctx, []string{"mcp", "list", "--json"}, "", 32768); err != nil {
		t.Fatal(err)
	}
	args := append(nativeArguments("qwen3.5:4b"), "--help")
	if _, err := p.invoke(ctx, args, "", 32768); err != nil {
		t.Fatal(err)
	}
}
