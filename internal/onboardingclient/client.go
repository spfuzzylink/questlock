package onboardingclient

import (
	"bytes"
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
	"reflect"
	"runtime"
)

const maxConfigBytes = 8 << 20

// Plan copies the complete original settings, applies only the Questlock server
// through the native CLI when available, and validates the full result. The
// caller must close the candidate even after a separate install transaction.
func Plan(ctx context.Context, options Options) (_ *Candidate, err error) {
	o, err := normalize(options, true)
	if err != nil {
		return nil, err
	}
	original, exists, err := readConfig(o.ConfigPath)
	if err != nil {
		return nil, err
	}
	if exists && o.Client != Codex && len(bytes.TrimSpace(original)) == 0 {
		return nil, errors.New("existing client settings are empty JSON")
	}
	c, err := stage(o, original)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = c.Close()
		}
	}()
	c.OriginalExists = exists
	c.OriginalSHA256 = digest(original)
	baseline, err := readDocument(ctx, c)
	if err != nil {
		return nil, err
	}
	c.original = baseline
	key := serverKey(o.Client)
	if o.Client == ClaudeDesktop {
		doc := clone(baseline)
		servers, err := serverMap(doc, key)
		if err != nil {
			return nil, err
		}
		servers["questlock"] = expectedEntry(o)
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return nil, errors.New("cannot encode complete client candidate")
		}
		if err := os.WriteFile(c.Path, append(data, '\n'), 0600); err != nil {
			return nil, errors.New("cannot write private client candidate")
		}
	} else {
		servers, err := serverMap(baseline, key)
		if err != nil {
			return nil, err
		}
		if _, present := servers["questlock"]; present {
			args := []string{"mcp", "remove", "questlock"}
			if o.Client == ClaudeCode {
				args = append(args, "--scope", "user")
			}
			if _, err := o.Runner.Run(ctx, command(c, args...)); err != nil {
				return nil, errors.New("native client could not replace Questlock in isolated settings")
			}
		}
		args := []string{"mcp", "add", "questlock"}
		if o.Client == ClaudeCode {
			args = []string{"mcp", "add", "--scope", "user", "--transport", "stdio", "questlock"}
		}
		args = append(args, "--", o.QuestlockPath)
		args = append(args, serverArgs(o)...)
		if _, err := o.Runner.Run(ctx, command(c, args...)); err != nil {
			return nil, errors.New("native client rejected isolated MCP candidate generation")
		}
	}
	c.Validation, err = validateStaged(ctx, c, true)
	if err != nil {
		return nil, err
	}
	data, _, err := readConfig(c.Path)
	if err != nil {
		return nil, err
	}
	c.CandidateSHA256 = digest(data)
	return c, nil
}

// Validate independently stages complete candidate bytes and requires the exact
// Questlock command. It does not compare a baseline; Plan/ValidateCandidate do.
func Validate(ctx context.Context, options Options, data []byte) (Validation, error) {
	return validateBytes(ctx, options, data, true)
}

// ValidateBaseline asks the native client to read a complete existing config
// without requiring a Questlock server to have already been installed.
func ValidateBaseline(ctx context.Context, options Options, data []byte) (Validation, error) {
	return validateBytes(ctx, options, data, false)
}

func validateBytes(ctx context.Context, options Options, data []byte, exact bool) (Validation, error) {
	o, err := normalize(options, exact)
	if err != nil {
		return Validation{}, err
	}
	if o.Client != ClaudeDesktop && o.Executable == "" {
		return Validation{}, errors.New("native client executable is missing; install the client before validation")
	}
	c, err := stage(o, data)
	if err != nil {
		return Validation{}, err
	}
	defer c.Close()
	if o.Client == ClaudeCode && !exact {
		return validateClaudeBaseline(ctx, c)
	}
	return validateStaged(ctx, c, exact)
}

func validateClaudeBaseline(ctx context.Context, c *Candidate) (Validation, error) {
	original, err := readJSONFile(c.Path)
	if err != nil {
		return Validation{}, err
	}
	// add/remove uses the native complete-config loader without starting any
	// pre-existing MCP server or invoking a model. Re-read the full file so a
	// native recovery/reset of malformed settings cannot pass validation.
	name := "questlock-validation-" + filepath.Base(c.IsolationDir)
	servers, err := serverMap(original, "mcpServers")
	if err != nil {
		return Validation{}, err
	}
	if _, exists := servers[name]; exists {
		return Validation{}, errors.New("native validation probe name is already configured")
	}
	args := []string{"mcp", "add", "--scope", "user", "--transport", "stdio", name, "--", c.options.Executable, "--version"}
	if _, err := c.options.Runner.Run(ctx, command(c, args...)); err != nil {
		return Validation{}, errors.New("Claude Code rejected complete isolated baseline")
	}
	if _, err := c.options.Runner.Run(ctx, command(c, "mcp", "remove", name, "--scope", "user")); err != nil {
		return Validation{}, errors.New("Claude Code could not finish complete baseline validation")
	}
	after, err := readJSONFile(c.Path)
	if err != nil || !preservesComplete(original, after, "mcpServers", ClaudeCode) {
		return Validation{}, errors.New("Claude Code changed unrelated settings while validating baseline")
	}
	originalServers, _ := serverMap(original, "mcpServers")
	afterServers, _ := serverMap(after, "mcpServers")
	if !reflect.DeepEqual(originalServers["questlock"], afterServers["questlock"]) {
		return Validation{}, errors.New("Claude Code changed existing Questlock baseline settings")
	}
	return Validation{Status: "validated", NativeValidated: true, PreservedSettings: true, NotifyPreserved: true}, nil
}

func ValidateCandidate(ctx context.Context, c *Candidate) (Validation, error) {
	if c == nil || c.options.Runner == nil || c.cleanupDir == "" || c.IsolationDir != c.cleanupDir {
		return Validation{}, errors.New("invalid client candidate")
	}
	return validateStaged(ctx, c, true)
}

func validateStaged(ctx context.Context, c *Candidate, exact bool) (Validation, error) {
	var v Validation
	doc, err := readDocument(ctx, c)
	if err != nil {
		return v, err
	}
	if c.original != nil {
		if !preservesComplete(c.original, doc, serverKey(c.Client), c.Client) {
			return v, errors.New("native client changed unrelated settings; candidate rejected")
		}
		if !reflect.DeepEqual(c.original["notify"], doc["notify"]) {
			return v, errors.New("native client changed notification command; candidate rejected")
		}
		v.PreservedSettings, v.NotifyPreserved = true, true
	}
	servers, err := serverMap(doc, serverKey(c.Client))
	if err != nil {
		return v, err
	}
	if exact {
		entry, ok := servers["questlock"].(map[string]any)
		if !ok || !equalEntry(entry, expectedEntry(c.options), c.Client) {
			return v, ErrManagedProfileMismatch
		}
		v.ExactServer = true
		if c.Client == Codex && !effectiveCodexEntry(c.effective, c.options) {
			return v, errors.New("effective Codex MCP configuration differs from the exact local Questlock command")
		}
	}
	if c.Client == ClaudeDesktop {
		v.Status, v.RequiresNativeRead = "native_validation_required", true
		return v, nil
	}
	if c.Client == ClaudeCode && exact {
		if _, err := c.options.Runner.Run(ctx, command(c, "mcp", "get", "questlock")); err != nil {
			return v, errors.New("Claude Code could not read Questlock from isolated user settings")
		}
		// The native read may rewrite its own file. Re-read and verify the entire
		// candidate rather than trusting an exit code or human-readable output.
		after, err := readJSONFile(c.Path)
		if err != nil || !preservesComplete(doc, after, "", ClaudeCode) {
			return v, errors.New("native validation changed complete Claude Code settings")
		}
	}
	v.Status, v.NativeValidated = "validated", true
	return v, nil
}

// Inspect reads the active bytes, but native parsing happens in a private copy.
// It never runs the active app, reveals config values, or changes active files.
func Inspect(ctx context.Context, options Options) (Report, error) {
	o, err := normalize(options, false)
	if err != nil {
		return Report{Client: options.Client, Status: "invalid_options"}, err
	}
	r := Report{Client: o.Client, ConfigPath: o.ConfigPath, Executable: o.Executable, ClientAvailable: o.Executable != "", Status: "not_configured"}
	data, exists, err := readConfig(o.ConfigPath)
	if err != nil {
		r.Status = "unsafe_or_unreadable_config"
		return r, err
	}
	r.ConfigExists = exists
	if exists {
		r.ConfigSHA256 = digest(data)
	}
	if !exists {
		return r, nil
	}
	if o.Client != Codex && len(bytes.TrimSpace(data)) == 0 {
		r.Status = "invalid_config"
		return r, errors.New("existing client settings are empty JSON")
	}
	if o.Client != ClaudeDesktop && !r.ClientAvailable {
		r.Status = "client_missing"
		return r, nil
	}
	c, err := stage(o, data)
	if err != nil {
		r.Status = "invalid_config"
		return r, err
	}
	defer c.Close()
	doc, err := readDocument(ctx, c)
	if err != nil {
		r.Status = "invalid_config"
		return r, err
	}
	servers, err := serverMap(doc, serverKey(o.Client))
	if err != nil {
		r.Status = "invalid_config"
		return r, err
	}
	_, r.ServerPresent = servers["questlock"]
	r.OtherServers = len(servers)
	if r.ServerPresent {
		r.OtherServers--
	}
	if notify, ok := doc["notify"].([]any); ok {
		r.NotifyArguments = len(notify)
	}
	if !r.ServerPresent {
		return r, nil
	}
	r.Status = "configured"
	if o.QuestlockPath != "" && o.Workspace != "" && o.StateDir != "" {
		if o.Client == ClaudeCode && o.SkipNativeMCPRead {
			entry, ok := servers["questlock"].(map[string]any)
			if !ok || !equalEntry(entry, expectedEntry(o), o.Client) {
				r.Status = "configuration_mismatch"
				return r, ErrManagedProfileMismatch
			}
			r.Validation = Validation{Status: "configuration_matches_native_unchecked", ExactServer: true, RequiresNativeRead: true}
			return r, nil
		}
		r.Validation, err = validateStaged(ctx, c, true)
		if err != nil {
			r.Status = "configuration_mismatch"
			return r, err
		}
		if r.Validation.RequiresNativeRead {
			r.Status = r.Validation.Status
		}
	}
	return r, nil
}

// CheckLive asks a CLI to read the actual installed file without generating or
// installing settings. Unlike Inspect, Claude Code's native command may perform
// its own bookkeeping or start the configured local MCP server. No model turn
// or paid API is requested. Call only during an explicit config transaction.
func CheckLive(ctx context.Context, options Options) (Validation, error) {
	o, err := normalize(options, true)
	if err != nil {
		return Validation{}, err
	}
	if o.Client == ClaudeDesktop {
		return Validation{Status: "native_validation_required", RequiresNativeRead: true}, errors.New("Claude Desktop requires application connector verification")
	}
	before, _, err := readConfig(o.ConfigPath)
	if err != nil {
		return Validation{}, err
	}
	c := &Candidate{Client: o.Client, ActivePath: o.ConfigPath, Path: o.ConfigPath, IsolationDir: o.HomeDir, options: o}
	// Explicit live environment; the config path must be the native user path.
	c.options.live = true
	v, err := validateStaged(ctx, c, true)
	if err != nil {
		return v, err
	}
	after, _, err := readConfig(o.ConfigPath)
	if err != nil || !bytes.Equal(before, after) {
		return Validation{}, errors.New("live client changed settings during verification")
	}
	return v, nil
}

// CheckLiveBaseline verifies a restored complete live configuration without
// requiring the newly generated Questlock command. Codex can read the complete
// user layer directly. Claude Code and Desktop do not expose a documented
// non-mutating native whole-config reader for an arbitrary prior configuration;
// restoration must remain explicitly unverified until native evidence exists.
// In particular, this method never adds or removes a live validation probe.
func CheckLiveBaseline(ctx context.Context, options Options) (Validation, error) {
	o, err := normalize(options, false)
	if err != nil {
		return Validation{}, err
	}
	if o.Client != Codex {
		return Validation{Status: "native_validation_required", RequiresNativeRead: true}, errors.New("restored client settings require native verification")
	}
	if o.Executable == "" {
		return Validation{}, errors.New("native Codex executable is missing")
	}
	before, _, err := readConfig(o.ConfigPath)
	if err != nil {
		return Validation{}, err
	}
	c := &Candidate{Client: o.Client, ActivePath: o.ConfigPath, Path: o.ConfigPath, IsolationDir: o.HomeDir, options: o}
	c.options.live = true
	v, err := validateStaged(ctx, c, false)
	if err != nil {
		return v, err
	}
	after, _, err := readConfig(o.ConfigPath)
	if err != nil || !bytes.Equal(before, after) {
		return Validation{}, errors.New("live Codex changed restored settings during verification")
	}
	return v, nil
}

func normalize(o Options, requireServer bool) (Options, error) {
	if o.Client != Codex && o.Client != ClaudeCode && o.Client != ClaudeDesktop {
		return o, errors.New("client must be codex, claude-code, or claude-desktop")
	}
	if o.HomeDir == "" {
		var err error
		o.HomeDir, err = os.UserHomeDir()
		if err != nil {
			return o, errors.New("cannot locate client home")
		}
	}
	if !filepath.IsAbs(o.HomeDir) {
		return o, errors.New("client home must be absolute")
	}
	if o.CodexHome == "" {
		if o.Client == Codex && o.ConfigPath != "" {
			o.CodexHome = filepath.Dir(o.ConfigPath)
		} else {
			o.CodexHome = filepath.Join(o.HomeDir, ".codex")
		}
	}
	if !filepath.IsAbs(o.CodexHome) {
		return o, errors.New("Codex home must be absolute")
	}
	if o.ConfigPath == "" {
		switch o.Client {
		case Codex:
			o.ConfigPath = filepath.Join(o.CodexHome, "config.toml")
		case ClaudeCode:
			o.ConfigPath = filepath.Join(o.HomeDir, ".claude.json")
		case ClaudeDesktop:
			if runtime.GOOS != "darwin" {
				return o, errors.New("Claude Desktop config setup currently supports macOS; use its native extension installer on other platforms")
			}
			o.ConfigPath = filepath.Join(o.HomeDir, "Library", "Application Support", "Claude", "claude_desktop_config.json")
		}
	}
	if !filepath.IsAbs(o.ConfigPath) {
		return o, errors.New("client config path must be absolute")
	}
	if o.Executable == "" {
		o.Executable = discover(o.Client, o.HomeDir)
	}
	if o.Executable != "" && !filepath.IsAbs(o.Executable) {
		return o, errors.New("native client executable must be absolute")
	}
	if o.Runner == nil && o.Executable != "" {
		if info, err := os.Stat(o.Executable); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			o.Executable = ""
		}
	}
	if o.Runner == nil {
		o.Runner = nativeRunner{}
	}
	if requireServer {
		if !filepath.IsAbs(o.QuestlockPath) || !filepath.IsAbs(o.Workspace) || !filepath.IsAbs(o.StateDir) {
			return o, errors.New("Questlock executable, workspace, and state directory must be absolute")
		}
		if o.Client != ClaudeDesktop && o.Executable == "" {
			return o, errors.New("native client executable is missing; install the client before setup")
		}
	}
	return o, nil
}

func discover(client Client, home string) string {
	name := "codex"
	if client == ClaudeCode {
		name = "claude"
	}
	if client != ClaudeDesktop {
		if path, err := exec.LookPath(name); err == nil {
			if full, err := filepath.Abs(path); err == nil {
				return full
			}
		}
	}
	if runtime.GOOS != "darwin" {
		return ""
	}
	var candidates []string
	if client == Codex {
		candidates = []string{"/Applications/Codex.app/Contents/Resources/codex", "/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex", filepath.Join(home, "Applications/Codex.app/Contents/Resources/codex")}
	}
	if client == ClaudeDesktop {
		candidates = []string{"/Applications/Claude.app/Contents/MacOS/Claude", filepath.Join(home, "Applications/Claude.app/Contents/MacOS/Claude")}
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return path
		}
	}
	return ""
}

func stage(o Options, data []byte) (*Candidate, error) {
	if len(data) > maxConfigBytes {
		return nil, errors.New("client config exceeds 8MiB limit")
	}
	dir, err := os.MkdirTemp("", "questlock-client-")
	if err != nil {
		return nil, errors.New("cannot create private client home")
	}
	// macOS /var and /tmp are system symlinks; use the canonical private root.
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	c := &Candidate{Client: o.Client, ActivePath: o.ConfigPath, IsolationDir: dir, options: o, cleanupDir: dir}
	var path string
	if o.Client == Codex {
		path = filepath.Join(dir, ".codex", "config.toml")
	} else if o.Client == ClaudeCode {
		path = filepath.Join(dir, ".claude.json")
	} else {
		path = filepath.Join(dir, "Library", "Application Support", "Claude", "claude_desktop_config.json")
	}
	c.Path = path
	for _, folder := range []string{filepath.Dir(path), filepath.Join(dir, ".claude"), filepath.Join(dir, ".config"), filepath.Join(dir, "tmp")} {
		if err := os.MkdirAll(folder, 0700); err != nil {
			_ = c.Close()
			return nil, errors.New("cannot create isolated client directories")
		}
	}
	if len(data) == 0 && o.Client != Codex {
		data = []byte("{}\n")
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		_ = c.Close()
		return nil, errors.New("cannot write private complete settings copy")
	}
	return c, nil
}

func serverArgs(o Options) []string {
	return []string{"worker", "local-serve", "--state-dir", o.StateDir, "--workspace", o.Workspace, "--client", string(o.Client)}
}
func expectedEntry(o Options) map[string]any {
	args := []any{}
	for _, arg := range serverArgs(o) {
		args = append(args, arg)
	}
	entry := map[string]any{"command": o.QuestlockPath, "args": args}
	if o.Client == ClaudeCode {
		entry["type"] = "stdio"
		entry["env"] = map[string]any{}
	}
	return entry
}

func equalEntry(got, want map[string]any, client Client) bool {
	got = clone(got)
	// Native clients may omit an empty env or a redundant stdio marker. No
	// arbitrary environment, transport, flags, or other entry settings pass.
	if env, ok := got["env"].(map[string]any); ok && len(env) == 0 {
		delete(got, "env")
	}
	if typ, ok := got["type"]; ok && typ == "stdio" {
		delete(got, "type")
	}
	want = clone(want)
	delete(want, "env")
	delete(want, "type")
	return reflect.DeepEqual(got, want)
}

func effectiveCodexEntry(config map[string]any, o Options) bool {
	servers, ok := config["mcp_servers"].(map[string]any)
	if !ok {
		return false
	}
	entry, ok := servers["questlock"].(map[string]any)
	if !ok {
		return false
	}
	want := expectedEntry(o)
	if !reflect.DeepEqual(entry["command"], want["command"]) || !reflect.DeepEqual(entry["args"], want["args"]) {
		return false
	}
	if enabled, ok := entry["enabled"]; ok && enabled != true {
		return false
	}
	if id, ok := entry["environment_id"]; ok && id != "local" && id != nil {
		return false
	}
	for _, key := range []string{"env", "env_vars", "url", "bearer_token_env_var", "http_headers", "env_http_headers", "disabled_tools"} {
		if value, ok := entry[key]; ok && value != nil {
			switch value := value.(type) {
			case map[string]any:
				if len(value) > 0 {
					return false
				}
			case []any:
				if len(value) > 0 {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}
func serverKey(c Client) string {
	if c == Codex {
		return "mcp_servers"
	}
	return "mcpServers"
}
func serverMap(doc map[string]any, key string) (map[string]any, error) {
	v, ok := doc[key]
	if !ok {
		servers := map[string]any{}
		doc[key] = servers
		return servers, nil
	}
	servers, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("MCP server collection must be an object")
	}
	return servers, nil
}
func withoutQuestlock(doc map[string]any, key string) map[string]any {
	doc = clone(doc)
	if servers, ok := doc[key].(map[string]any); ok {
		delete(servers, "questlock")
		if len(servers) == 0 {
			delete(doc, key)
		}
	}
	return doc
}

func preservesComplete(before, after map[string]any, key string, client Client) bool {
	before = withoutQuestlock(before, key)
	after = withoutQuestlock(after, key)
	if client == ClaudeCode {
		// Claude Code 2.1.302 initializes these bookkeeping fields while reading
		// a fresh profile. Only newly added fields are allowed: existing values,
		// unknown settings, notify arrays, and other MCP entries must stay exact.
		for _, name := range []string{"firstStartTime", "firstStartVersion", "machineID", "userID", "opusProMigrationComplete", "sonnet1m45MigrationComplete", "seenNotifications", "hasResetAutoModeOptInForDefaultOffer", "migrationVersion"} {
			if _, exists := before[name]; !exists {
				delete(after, name)
			}
		}
	}
	return reflect.DeepEqual(before, after)
}
func clone(doc map[string]any) map[string]any {
	out := make(map[string]any, len(doc))
	for key, value := range doc {
		if object, ok := value.(map[string]any); ok {
			value = clone(object)
		}
		out[key] = value
	}
	return out
}
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func readConfig(path string) ([]byte, bool, error) {
	if err := checkPath(path); err != nil {
		return nil, false, err
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, errors.New("cannot read client config")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxConfigBytes {
		return nil, true, errors.New("client config must be a regular file of at most 8MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil || len(data) > maxConfigBytes {
		return nil, true, errors.New("cannot read bounded client config")
	}
	return data, true, nil
}

func checkPath(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("config path must be absolute")
	}
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return errors.New("cannot inspect client config path")
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return errors.New("client settings refuse symlink files or directories")
		}
		if err == nil && current != filepath.Clean(path) && !info.IsDir() {
			return errors.New("client config ancestor is not a directory")
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}

func readJSONFile(path string) (map[string]any, error) {
	data, exists, err := readConfig(path)
	if err != nil || !exists {
		return nil, errors.New("cannot read complete JSON client settings")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeUnique(decoder)
	if err != nil {
		return nil, errors.New("client settings contain invalid or duplicate-key JSON")
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, errors.New("client settings contain trailing JSON data")
	}
	doc, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("complete client settings must be a JSON object")
	}
	return doc, nil
}

func decodeUnique(d *json.Decoder) (any, error) {
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		doc := map[string]any{}
		for d.More() {
			token, err := d.Token()
			if err != nil {
				return nil, err
			}
			key, ok := token.(string)
			if !ok {
				return nil, errors.New("invalid object key")
			}
			if _, ok := doc[key]; ok {
				return nil, errors.New("duplicate object key")
			}
			value, err := decodeUnique(d)
			if err != nil {
				return nil, err
			}
			doc[key] = value
		}
		_, err = d.Token()
		return doc, err
	case '[':
		values := []any{}
		for d.More() {
			value, err := decodeUnique(d)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		_, err = d.Token()
		return values, err
	}
	return nil, fmt.Errorf("unexpected JSON delimiter")
}

func readDocument(ctx context.Context, c *Candidate) (map[string]any, error) {
	if c.Client != Codex {
		return readJSONFile(c.Path)
	}
	if c.options.Executable == "" {
		return nil, errors.New("native Codex executable is missing")
	}
	snapshot, err := c.options.Runner.CodexRead(ctx, command(c, "app-server"))
	if err != nil {
		return nil, errors.New("Codex rejected complete isolated configuration")
	}
	c.effective = snapshot.Config
	for _, layer := range snapshot.Layers {
		if layer.Name.Type == "user" && filepath.Clean(layer.Name.File) == filepath.Clean(c.Path) {
			if layer.Config == nil {
				return nil, errors.New("Codex returned an invalid complete user layer")
			}
			return layer.Config, nil
		}
	}
	return nil, errors.New("Codex did not return the complete user configuration layer")
}

func command(c *Candidate, args ...string) Command {
	home := c.IsolationDir
	codexHome := filepath.Join(home, ".codex")
	claudeConfigDir := home
	tmpDir := filepath.Join(home, "tmp")
	if c.options.live {
		home = c.options.HomeDir
		codexHome = c.options.CodexHome
		claudeConfigDir = filepath.Dir(c.Path)
		tmpDir = os.TempDir()
	}
	env := []string{"HOME=" + home, "USERPROFILE=" + home, "CODEX_HOME=" + codexHome, "CLAUDE_CONFIG_DIR=" + claudeConfigDir, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "TMPDIR=" + tmpDir, "PATH=" + os.Getenv("PATH"), "NO_COLOR=1", "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1", "DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"}
	// Never inherit provider credentials, config selectors, or MCP auth tokens.
	return Command{Executable: c.options.Executable, Args: args, Env: env, Dir: home}
}
