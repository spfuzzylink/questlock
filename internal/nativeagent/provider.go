// Package nativeagent runs bounded text-only roles through an isolated native
// Codex process backed by already-installed local Ollama weights. It does not
// use a user's Codex account, credentials, settings, hooks, or project files.
package nativeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spfuzzylink/questlock/inference"
)

const (
	ollamaURL      = "http://127.0.0.1:11434"
	maxPromptBytes = 512 << 10
	maxOutputBytes = 8 << 20
	maxLineBytes   = 1 << 20
	// HarnessReserve is a conservative admission estimate for native instructions
	// and tool schemas, not an exact tokenizer count or a generation-time ceiling.
	HarnessReserve int64 = 16_384
	policyVersion        = "text-only-1"
)

// Provider binds the local model snapshot and a particular native executable.
// Recreate it after changing weights or upgrading the native client.
type Provider struct {
	executable, executableDigest, version, model, identity string
	timeout                                                time.Duration
	local                                                  *inference.OllamaProvider
	refreshLocal                                           func(context.Context) (*inference.OllamaProvider, error)
}

var _ inference.Provider = (*Provider)(nil)

// New inspects local weights and the executable without performing inference.
// executable must be absolute; all process state is confined to a fresh private
// home and empty workspace. There is no remote endpoint or paid fallback option.
func New(ctx context.Context, executable, model string, timeout time.Duration) (*Provider, error) {
	local, err := inference.NewOllamaProviderContext(ctx, ollamaURL, model)
	if err != nil {
		return nil, err
	}
	return newWithLocal(ctx, executable, model, timeout, local, func(ctx context.Context) (*inference.OllamaProvider, error) {
		return inference.NewOllamaProviderContext(ctx, ollamaURL, model)
	})
}

func newWithLocal(ctx context.Context, executable, model string, timeout time.Duration, local *inference.OllamaProvider, refresh func(context.Context) (*inference.OllamaProvider, error)) (*Provider, error) {
	if !supportedPlatform() {
		return nil, errors.New("native local roles require a supported Unix process-group implementation")
	}
	if timeout < time.Second || timeout > 10*time.Minute {
		return nil, errors.New("native role timeout must be between one second and ten minutes")
	}
	path, digest, err := inspectExecutable(executable)
	if err != nil {
		return nil, err
	}
	p := &Provider{executable: path, executableDigest: digest, model: model, timeout: timeout, local: local, refreshLocal: refresh}
	versionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	version, err := p.invoke(versionCtx, []string{"--version"}, "", 0)
	if err != nil {
		return nil, errors.New("cannot inspect native client version")
	}
	p.version = strings.TrimSpace(string(version))
	if !validVersion(p.version) {
		return nil, errors.New("native local roles require a Codex CLI 0.163 version")
	}
	if err := p.checkExecutable(); err != nil {
		return nil, err
	}
	p.identity = "codex-oss:" + policyVersion + ":" + p.executable + "@" + p.executableDigest + ":" + p.version + ":" + local.Identity()
	return p, nil
}

func validVersion(s string) bool {
	const prefix = "codex-cli 0.163.0"
	if !strings.HasPrefix(s, prefix) || len(s) > 128 {
		return false
	}
	rest := strings.TrimPrefix(s, prefix)
	if rest != "" && rest[0] != '-' && rest[0] != '+' {
		return false
	}
	for _, c := range rest {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune(".-+", c)) {
			return false
		}
	}
	return true
}

func inspectExecutable(path string) (string, string, error) {
	if !filepath.IsAbs(path) {
		return "", "", errors.New("native executable must be an absolute path")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", errors.New("cannot resolve native executable")
	}
	f, err := openExecutable(canonical)
	if err != nil {
		return "", "", errors.New("cannot read native executable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", "", errors.New("native executable must be an executable regular file")
	}
	if info.Size() < 1 || info.Size() > 1<<30 {
		return "", "", errors.New("native executable size is unsupported")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, 1<<30+1))
	if err != nil || n != info.Size() {
		return "", "", errors.New("cannot fingerprint native executable")
	}
	return canonical, hex.EncodeToString(h.Sum(nil)), nil
}

func (p *Provider) checkExecutable() error {
	path, digest, err := inspectExecutable(p.executable)
	if err != nil || path != p.executable || digest != p.executableDigest {
		return errors.New("native executable identity changed; recreate the provider")
	}
	return nil
}

func (p *Provider) Identity() string       { return p.identity }
func (p *Provider) ModelDigest() string    { return p.local.ModelDigest() }
func (p *Provider) RuntimeVersion() string { return p.version }

// RefreshIdentity prevents stale accepted results from bypassing a changed
// executable or changed local model snapshot. It never updates the pinned value.
func (p *Provider) RefreshIdentity(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := p.checkExecutable(); err != nil {
		return err
	}
	current, err := p.refreshLocal(ctx)
	if err != nil {
		return err
	}
	if current.Identity() != p.local.Identity() {
		return errors.New("native local model identity changed; recreate the provider")
	}
	return nil
}

func (p *Provider) request(req inference.Request) (inference.Request, string, error) {
	if req.Stream || req.OpenRouter != nil || req.SGLang != nil {
		return req, "", errors.New("native local roles require text-only Ollama requests")
	}
	if req.Provider != "ollama" || req.Model != p.model || req.ReasoningEffort != "" || req.CacheMode != "" && req.CacheMode != "default" {
		return req, "", errors.New("native local request does not match its pinned Ollama policy")
	}
	if req.Input == "" || !utf8.ValidString(req.Prefix) || !utf8.ValidString(req.Input) || len(req.Prefix)+len(req.Input) > maxPromptBytes {
		return req, "", errors.New("native local request requires bounded valid UTF-8 input")
	}
	if req.MaxOutputTokens < 1 || req.MaxOutputTokens > 1_000_000 || req.ContextWindow < 1 || req.ContextWindow > 65_536 {
		return req, "", errors.New("native local token limits are unsupported")
	}
	data, err := json.Marshal(struct {
		Instructions string `json:"instructions"`
		Input        string `json:"input"`
	}{req.Prefix, req.Input})
	if err != nil || len(data) > maxPromptBytes {
		return req, "", errors.New("encoded native local request exceeds the byte limit")
	}
	prompt := "Answer the explicitly approved request below using only its supplied context. Produce the requested final answer directly. Do not use tools, inspect files, access networks, delegate, or change settings. The JSON separates the request instructions from its input data.\n" + string(data)
	// Reuse the existing local adapter's installed-model checks and conservative
	// byte estimate, while counting the actual serialized native prompt.
	countReq := req
	countReq.Prefix = ""
	countReq.Input = prompt
	return countReq, prompt, nil
}

// CountInputTokens is a byte-based local estimate plus HarnessReserve, not a
// native tokenizer API. Native tool-turn totals are checked after generation by
// inference.Runner; the harness does not expose a hard output-token ceiling.
func (p *Provider) CountInputTokens(ctx context.Context, req inference.Request) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	countReq, _, err := p.request(req)
	if err != nil {
		return 0, err
	}
	if err = p.checkExecutable(); err != nil {
		return 0, err
	}
	count, err := p.local.CountInputTokens(ctx, countReq)
	if err != nil {
		return 0, err
	}
	count += HarnessReserve
	if count > req.ContextWindow-req.MaxOutputTokens {
		return 0, errors.New("native harness reserve and output allowance exceed the requested context window")
	}
	return count, nil
}

func (p *Provider) Generate(ctx context.Context, req inference.Request) (inference.Response, error) {
	_, prompt, err := p.request(req)
	if err != nil {
		return inference.Response{}, err
	}
	if _, err = p.CountInputTokens(ctx, req); err != nil {
		return inference.Response{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	data, err := p.invoke(ctx, nativeArguments(p.model), prompt, req.ContextWindow)
	if err != nil {
		return inference.Response{}, err
	}
	response, err := parseEvents(data, p.model)
	if err != nil {
		return response, err
	}
	if err = p.RefreshIdentity(ctx); err != nil {
		response.Complete = false
		response.StopReason = "identity_changed"
		return response, err
	}
	if response.Usage.OutputTokens > req.MaxOutputTokens {
		response.Complete = false
		response.StopReason = "output_limit_exceeded"
	}
	return response, nil
}

func nativeArguments(model string) []string {
	args := []string{"exec", "--oss", "--local-provider", "ollama", "--model", model, "--sandbox", "read-only", "--skip-git-repo-check", "--json", "--ephemeral", "--ignore-rules", "--strict-config"}
	for _, feature := range []string{"shell_tool", "unified_exec", "apps", "plugins", "hooks", "multi_agent", "browser_use", "computer_use", "image_generation", "view_image", "in_app_browser", "workspace_dependencies", "unbounded_connection_retries"} {
		args = append(args, "--disable", feature)
	}
	args = append(args, "--enable", "skip_host_skill_discovery")
	return args
}

func (p *Provider) invoke(ctx context.Context, args []string, prompt string, contextWindow int64) ([]byte, error) {
	root, err := os.MkdirTemp("", "questlock-native-")
	if err != nil {
		return nil, errors.New("cannot create isolated native home")
	}
	defer os.RemoveAll(root)
	home, codeHome, workspace := filepath.Join(root, "home"), filepath.Join(root, "codex"), filepath.Join(root, "workspace")
	for _, dir := range []string{home, codeHome, workspace} {
		if err = os.Mkdir(dir, 0700); err != nil {
			return nil, errors.New("cannot create isolated native directories")
		}
	}
	config := "approval_policy = \"never\"\ncheck_for_update_on_startup = false\nproject_doc_max_bytes = 0\nweb_search = \"disabled\"\n"
	if contextWindow > 0 {
		config = "model_context_window = " + strconv.FormatInt(contextWindow, 10) + "\n" + config
	}
	if err = os.WriteFile(filepath.Join(codeHome, "config.toml"), []byte(config), 0600); err != nil {
		return nil, errors.New("cannot write isolated native policy")
	}
	if prompt != "" {
		args = append(args, "--cd", workspace, "--", "-")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.executable, args...)
	cmd.Dir = workspace
	// Deliberately omit credentials, proxy variables, user configuration roots,
	// shell startup settings and existing MCP/plugin configuration.
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "CODEX_HOME=" + codeHome, "OLLAMA_BASE_URL=" + ollamaURL}
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stderr = io.Discard
	output := &boundedOutput{cancel: cancel}
	cmd.Stdout = output
	configureProcess(cmd)
	output.cancel = func() { cancel(); _ = cmd.Cancel() }
	defer stopProcess(cmd)
	if err = cmd.Run(); err != nil {
		if output.exceeded {
			return nil, errors.New("native client output exceeds the byte or line limit")
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("native local role interrupted: %w", ctx.Err())
		}
		return nil, errors.New("native local role failed; output and stderr withheld")
	}
	return output.data, nil
}

type boundedOutput struct {
	data     []byte
	line     int
	exceeded bool
	cancel   context.CancelFunc
}

func (b *boundedOutput) Write(data []byte) (int, error) {
	if len(b.data)+len(data) > maxOutputBytes {
		b.exceeded = true
		b.cancel()
		return 0, errors.New("output limit")
	}
	for _, c := range data {
		if c == '\n' {
			b.line = 0
		} else {
			b.line++
			if b.line > maxLineBytes {
				b.exceeded = true
				b.cancel()
				return 0, errors.New("line limit")
			}
		}
	}
	b.data = append(b.data, data...)
	return len(data), nil
}
