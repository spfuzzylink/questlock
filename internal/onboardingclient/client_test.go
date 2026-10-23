package onboardingclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeNative struct {
	commands        []Command
	reads           int
	mutate          func(map[string]any)
	effectiveMutate func(map[string]any)
	fail            bool
}

func (f *fakeNative) Run(_ context.Context, c Command) ([]byte, error) {
	f.commands = append(f.commands, c)
	if f.fail {
		return nil, errors.New("private-native-error sk-secret")
	}
	path := filepath.Join(envValue(c.Env, "CLAUDE_CONFIG_DIR"), ".claude.json")
	key := "mcpServers"
	if filepath.Base(c.Executable) == "codex" {
		path = filepath.Join(envValue(c.Env, "CODEX_HOME"), "config.toml")
		key = "mcp_servers"
	}
	doc, err := readJSONFile(path)
	if err != nil {
		return nil, err
	}
	servers, err := serverMap(doc, key)
	if err != nil {
		return nil, err
	}
	if len(c.Args) < 2 {
		return nil, errors.New("missing native command")
	}
	switch c.Args[1] {
	case "remove":
		delete(servers, c.Args[2])
	case "add":
		sep := -1
		for i, arg := range c.Args {
			if arg == "--" {
				sep = i
				break
			}
		}
		if sep < 3 || sep+1 >= len(c.Args) {
			return nil, errors.New("missing stdio separator")
		}
		name := c.Args[sep-1]
		args := []any{}
		for _, arg := range c.Args[sep+2:] {
			args = append(args, arg)
		}
		entry := map[string]any{"command": c.Args[sep+1], "args": args}
		if key == "mcpServers" {
			entry["type"] = "stdio"
			entry["env"] = map[string]any{}
		}
		servers[name] = entry
		if f.mutate != nil {
			f.mutate(doc)
		}
	case "get":
		if _, ok := servers[c.Args[2]]; !ok {
			return nil, errors.New("server missing")
		}
		return []byte("private-native-output sk-secret"), nil
	default:
		return nil, errors.New("unexpected native command")
	}
	data, _ := json.Marshal(doc)
	return nil, os.WriteFile(path, data, 0600)
}

func (f *fakeNative) CodexRead(_ context.Context, c Command) (CodexSnapshot, error) {
	f.reads++
	if f.fail {
		return CodexSnapshot{}, errors.New("private-native-error sk-secret")
	}
	path := filepath.Join(envValue(c.Env, "CODEX_HOME"), "config.toml")
	doc, err := readJSONFile(path)
	if err != nil {
		return CodexSnapshot{}, err
	}
	// Unknown settings deliberately disappear from the effective typed config.
	// The raw user layer, rather than Config, must drive preservation checks.
	effective := map[string]any{"mcp_servers": cloneMapValue(doc["mcp_servers"])}
	if f.effectiveMutate != nil {
		f.effectiveMutate(effective)
	}
	encoded, _ := json.Marshal(map[string]any{"config": effective, "layers": []any{map[string]any{"name": map[string]any{"type": "user", "file": path}, "config": doc}}})
	var snapshot CodexSnapshot
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	err = decoder.Decode(&snapshot)
	return snapshot, err
}

func cloneMapValue(value any) any {
	if object, ok := value.(map[string]any); ok {
		return clone(object)
	}
	return value
}

func TestCodexRejectsManagedMCPOverride(t *testing.T) {
	runner := &fakeNative{effectiveMutate: func(config map[string]any) {
		if servers, ok := config["mcp_servers"].(map[string]any); ok {
			if entry, ok := servers["questlock"].(map[string]any); ok {
				entry["enabled"] = false
			}
		}
	}}
	o := testOptions(t, Codex, runner)
	writeFixture(t, o)
	if candidate, err := Plan(context.Background(), o); err == nil {
		candidate.Close()
		t.Fatal("disabled effective MCP override accepted")
	}
}

func TestExistingEmptyJSONRejected(t *testing.T) {
	o := testOptions(t, ClaudeCode, &fakeNative{})
	if err := os.WriteFile(o.ConfigPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if c, err := Plan(context.Background(), o); err == nil {
		c.Close()
		t.Fatal("existing empty config accepted")
	}
	if _, err := Inspect(context.Background(), o); err == nil {
		t.Fatal("doctor accepted empty JSON")
	}
}

func TestRollbackNativeBoundary(t *testing.T) {
	for _, client := range []Client{Codex, ClaudeCode, ClaudeDesktop} {
		t.Run(string(client), func(t *testing.T) {
			runner := &fakeNative{}
			o := testOptions(t, client, runner)
			original := writeFixture(t, o)
			v, err := CheckLiveBaseline(context.Background(), o)
			if client == Codex {
				if err != nil || !v.NativeValidated || v.ExactServer {
					t.Fatalf("restored arbitrary Codex baseline rejected: %+v %v", v, err)
				}
			} else {
				if err == nil || v.NativeValidated || !v.RequiresNativeRead || len(runner.commands) != 0 {
					t.Fatalf("unsupported rollback claimed native proof: %+v %v", v, err)
				}
			}
			after, _ := os.ReadFile(o.ConfigPath)
			if !bytes.Equal(original, after) {
				t.Fatal("rollback verification changed active baseline")
			}
		})
	}
}

func TestClaudeNativeBookkeepingBoundary(t *testing.T) {
	for _, key := range []string{"firstStartTime", "arbitraryNewSetting"} {
		t.Run(key, func(t *testing.T) {
			runner := &fakeNative{mutate: func(doc map[string]any) { doc[key] = "native-value" }}
			o := testOptions(t, ClaudeCode, runner)
			writeFixture(t, o)
			c, err := Plan(context.Background(), o)
			if key == "firstStartTime" {
				if err != nil {
					t.Fatal(err)
				}
				c.Close()
			} else if err == nil {
				c.Close()
				t.Fatal("arbitrary new native setting accepted")
			}
		})
	}
}

func TestStaticDoctorDoesNotStartNativeMCP(t *testing.T) {
	runner := &fakeNative{}
	o := testOptions(t, ClaudeCode, runner)
	doc := map[string]any{"mcpServers": map[string]any{"questlock": expectedEntry(o)}}
	data, _ := json.Marshal(doc)
	if err := os.WriteFile(o.ConfigPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	o.SkipNativeMCPRead = true
	r, err := Inspect(context.Background(), o)
	if err != nil || !r.Validation.ExactServer || r.Validation.NativeValidated || len(runner.commands) > 0 {
		t.Fatalf("static doctor made native claims or ran MCP: %+v %v", r, err)
	}
	doc["mcpServers"].(map[string]any)["questlock"].(map[string]any)["command"] = "/legacy/questlock"
	data, _ = json.Marshal(doc)
	os.WriteFile(o.ConfigPath, data, 0600)
	if _, err := Inspect(context.Background(), o); err == nil {
		t.Fatal("static doctor accepted legacy command")
	}
}

func envValue(env []string, key string) string {
	for _, value := range env {
		if strings.HasPrefix(value, key+"=") {
			return strings.TrimPrefix(value, key+"=")
		}
	}
	return ""
}

func testOptions(t *testing.T, client Client, runner Runner) Options {
	t.Helper()
	home := t.TempDir()
	var err error
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	name := "claude"
	if client == Codex {
		name = "codex"
	}
	path := filepath.Join(home, ".claude.json")
	if client == Codex {
		path = filepath.Join(home, ".codex", "config.toml")
	}
	if client == ClaudeDesktop {
		path = filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
	}
	return Options{Client: client, HomeDir: home, ConfigPath: path, Executable: filepath.Join(home, "bin", name), QuestlockPath: filepath.Join(home, "bin", "questlock"), Workspace: filepath.Join(home, "requests"), StateDir: filepath.Join(home, "state"), Runner: runner}
}

func writeFixture(t *testing.T, o Options) []byte {
	t.Helper()
	data := []byte(`{"notify":["/usr/bin/notify","--message","two words"],"privateExtension":{"unknown":[1,{"number":9007199254740993,"secret":"sk-secret"}],"flag":false},"` + serverKey(o.Client) + `":{"other":{"command":"/existing/server","env":{"TOKEN":"private-token"}},"questlock":{"command":"/old/questlock","args":["old"]}}}`)
	if err := os.MkdirAll(filepath.Dir(o.ConfigPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.ConfigPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPlanPreservesCompleteSettings(t *testing.T) {
	for _, client := range []Client{Codex, ClaudeCode, ClaudeDesktop} {
		t.Run(string(client), func(t *testing.T) {
			runner := &fakeNative{}
			o := testOptions(t, client, runner)
			original := writeFixture(t, o)
			t.Setenv("OPENAI_API_KEY", "must-not-inherit")
			t.Setenv("ANTHROPIC_API_KEY", "must-not-inherit")
			t.Setenv("QUESTLOCK_TOKEN", "must-not-inherit")
			t.Setenv("CLAUDE_CONFIG_DIR", "/must-not-use")
			candidate, err := Plan(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			defer candidate.Close()
			if !candidate.Validation.PreservedSettings || !candidate.Validation.NotifyPreserved || !candidate.Validation.ExactServer {
				t.Fatalf("incomplete validation: %+v", candidate.Validation)
			}
			if client == ClaudeDesktop {
				if candidate.Validation.NativeValidated || !candidate.Validation.RequiresNativeRead || len(runner.commands) > 0 {
					t.Fatalf("Desktop claimed native validation: %+v", candidate.Validation)
				}
			} else if !candidate.Validation.NativeValidated {
				t.Fatal("missing native client read")
			}
			for _, c := range runner.commands {
				for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "QUESTLOCK_TOKEN"} {
					if envValue(c.Env, key) != "" {
						t.Fatalf("inherited %s", key)
					}
				}
				if envValue(c.Env, "HOME") == o.HomeDir {
					t.Fatal("native candidate command used active home")
				}
				if envValue(c.Env, "CLAUDE_CONFIG_DIR") != candidate.IsolationDir {
					t.Fatal("Claude config selector not isolated consistently")
				}
			}
			after, err := os.ReadFile(o.ConfigPath)
			if err != nil || string(after) != string(original) {
				t.Fatal("active settings changed")
			}
			for path, want := range map[string]os.FileMode{candidate.IsolationDir: 0700, candidate.Path: 0600} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != want {
					t.Fatalf("private mode for %s: %v", path, err)
				}
			}
			if _, err := ValidateCandidate(context.Background(), candidate); err != nil {
				t.Fatal(err)
			}
			if err := candidate.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(candidate.IsolationDir); !os.IsNotExist(err) {
				t.Fatal("private candidate remained after close")
			}
		})
	}
}

func TestPlanRejectsUnrelatedNativeChanges(t *testing.T) {
	for _, client := range []Client{Codex, ClaudeCode} {
		for _, field := range []string{"notify", "privateExtension", serverKey(client)} {
			t.Run(string(client)+"/"+field, func(t *testing.T) {
				runner := &fakeNative{mutate: func(doc map[string]any) {
					if field == serverKey(client) {
						delete(doc[field].(map[string]any), "other")
					} else {
						delete(doc, field)
					}
				}}
				o := testOptions(t, client, runner)
				original := writeFixture(t, o)
				candidate, err := Plan(context.Background(), o)
				if err == nil {
					candidate.Close()
					t.Fatal("loss of unrelated setting accepted")
				}
				if strings.Contains(err.Error(), "sk-secret") {
					t.Fatal("native details leaked")
				}
				after, _ := os.ReadFile(o.ConfigPath)
				if string(after) != string(original) {
					t.Fatal("active settings changed after rejected generation")
				}
			})
		}
	}
}

func TestValidateRejectsUnexpectedMCPOptions(t *testing.T) {
	for _, client := range []Client{Codex, ClaudeCode, ClaudeDesktop} {
		for _, change := range []string{"provider_env", "extra_arg", "wrong_workspace", "disabled"} {
			t.Run(string(client)+"/"+change, func(t *testing.T) {
				o := testOptions(t, client, &fakeNative{})
				entry := expectedEntry(o)
				switch change {
				case "provider_env":
					entry["env"] = map[string]any{"OPENAI_API_KEY": "sk-secret"}
				case "extra_arg":
					entry["args"] = append(entry["args"].([]any), "--allow-paid")
				case "wrong_workspace":
					entry["args"].([]any)[5] = "/other/workspace"
				case "disabled":
					entry["enabled"] = false
				}
				data, _ := json.Marshal(map[string]any{serverKey(client): map[string]any{"questlock": entry}})
				if _, err := Validate(context.Background(), o, data); err == nil {
					t.Fatal("unexpected server settings accepted")
				}
			})
		}
	}
}

func TestValidateBaselineUsesNativeWholeConfigWithoutStartingServers(t *testing.T) {
	runner := &fakeNative{}
	o := testOptions(t, ClaudeCode, runner)
	data := writeFixture(t, o)
	v, err := ValidateBaseline(context.Background(), o, data)
	if err != nil {
		t.Fatal(err)
	}
	if !v.NativeValidated || !v.PreservedSettings {
		t.Fatalf("baseline not checked: %+v", v)
	}
	if len(runner.commands) != 2 || runner.commands[0].Args[1] != "add" || runner.commands[1].Args[1] != "remove" {
		t.Fatalf("baseline started a server: %+v", runner.commands)
	}
	for _, bad := range []string{`{"x":1,"x":2}`, `null`, `{"mcpServers":[]}`, `{"unfinished":`} {
		if _, err := ValidateBaseline(context.Background(), o, []byte(bad)); err == nil {
			t.Fatalf("bad baseline accepted: %s", bad)
		}
	}
}

func TestInspectRedactsConfigAndNativeOutput(t *testing.T) {
	runner := &fakeNative{}
	o := testOptions(t, ClaudeCode, runner)
	writeFixture(t, o)
	candidate, err := Plan(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	data, _ := os.ReadFile(candidate.Path)
	if err := os.WriteFile(o.ConfigPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Inspect(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(r)
	if strings.Contains(string(encoded), "sk-secret") || strings.Contains(string(encoded), "private-token") || strings.Contains(string(encoded), "privateExtension") {
		t.Fatalf("doctor leaked settings: %s", encoded)
	}
	if !r.ServerPresent || r.OtherServers != 1 || r.NotifyArguments != 3 || r.Status != "configured" {
		t.Fatalf("wrong diagnostic evidence: %+v", r)
	}
	after, _ := os.ReadFile(o.ConfigPath)
	if !reflect.DeepEqual(data, after) {
		t.Fatal("doctor changed active settings")
	}
}

func TestInspectCustomProfileMismatchIsDistinctFromNativeFailure(t *testing.T) {
	for _, client := range []Client{Codex, ClaudeCode} {
		t.Run(string(client), func(t *testing.T) {
			runner := &fakeNative{}
			o := testOptions(t, client, runner)
			o.SkipNativeMCPRead = true
			before := writeFixture(t, o)
			r, err := Inspect(t.Context(), o)
			if !errors.Is(err, ErrManagedProfileMismatch) || !r.ServerPresent || r.Validation.ExactServer || r.Status != "configuration_mismatch" {
				t.Fatalf("custom command lost its specific diagnostic: %+v %v", r, err)
			}
			after, err := os.ReadFile(o.ConfigPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("read-only custom profile inspection changed configuration")
			}
			if len(runner.commands) != 0 {
				t.Fatal("custom profile inspection tried to start an MCP server")
			}
			if client == Codex {
				runner.fail = true
				_, err := Inspect(t.Context(), o)
				if err == nil || errors.Is(err, ErrManagedProfileMismatch) {
					t.Fatal("native parser failure was classified as a custom profile")
				}
			}
		})
	}
}

func TestRejectSymlinkConfigAndAncestor(t *testing.T) {
	for _, ancestor := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "ancestor"}[ancestor], func(t *testing.T) {
			o := testOptions(t, ClaudeCode, &fakeNative{})
			target := filepath.Join(o.HomeDir, "real")
			if err := os.MkdirAll(target, 0700); err != nil {
				t.Fatal(err)
			}
			if ancestor {
				link := filepath.Join(o.HomeDir, "link")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				o.ConfigPath = filepath.Join(link, ".claude.json")
			} else {
				if err := os.WriteFile(filepath.Join(target, ".claude.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(target, ".claude.json"), o.ConfigPath); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Plan(context.Background(), o); err == nil {
				t.Fatal("symlink config accepted")
			}
			if _, err := Inspect(context.Background(), o); err == nil {
				t.Fatal("doctor followed symlink")
			}
		})
	}
}

func TestMissingNativeClientAndConfig(t *testing.T) {
	o := testOptions(t, ClaudeCode, nil)
	o.Executable = filepath.Join(o.HomeDir, "missing")
	r, err := Inspect(context.Background(), o)
	if err != nil || r.ClientAvailable || r.ConfigExists {
		t.Fatalf("wrong absent client report: %+v %v", r, err)
	}
	if _, err := Plan(context.Background(), o); err == nil {
		t.Fatal("setup accepted missing native client")
	}
	if _, err := ValidateBaseline(context.Background(), o, []byte("{}")); err == nil {
		t.Fatal("baseline claimed native read without client")
	}
}

// Optional real-client smoke: it uses only a temporary home and config RPCs,
// with no authentication copy, MCP startup, provider request, or active writes.
func TestNativeCodexIsolated(t *testing.T) {
	if os.Getenv("QUESTLOCK_TEST_NATIVE_CODEX") != "1" {
		t.Skip("opt in to isolated installed-client verification")
	}
	o := testOptions(t, Codex, nil)
	o.Executable = discover(Codex, o.HomeDir)
	if o.Executable == "" {
		t.Skip("Codex unavailable")
	}
	data := []byte("notify = [\"/usr/bin/notify\", \"two words\"]\nprivate_unknown = { value = [1, 2], flag = false }\n[mcp_servers.other]\ncommand = \"/usr/bin/false\"\n")
	if err := os.MkdirAll(filepath.Dir(o.ConfigPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.ConfigPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	candidate, err := Plan(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	if !candidate.Validation.NativeValidated || !candidate.Validation.PreservedSettings || !candidate.Validation.NotifyPreserved {
		t.Fatalf("real validation failed: %+v", candidate.Validation)
	}
	if _, err := ValidateBaseline(context.Background(), o, data); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(context.Background(), o, []byte("notify = [\"unterminated\"")); err == nil {
		t.Fatal("native parser accepted invalid TOML")
	}
	after, _ := os.ReadFile(o.ConfigPath)
	if string(after) != string(data) {
		t.Fatal("active test home modified")
	}
}

func TestNativeClaudeIsolated(t *testing.T) {
	client := os.Getenv("QUESTLOCK_TEST_NATIVE_CLAUDE")
	questlock := os.Getenv("QUESTLOCK_TEST_QUESTLOCK")
	if client == "" || questlock == "" {
		t.Skip("opt in with private native Claude and Questlock executables")
	}
	o := testOptions(t, ClaudeCode, nil)
	o.Executable = client
	o.QuestlockPath = questlock
	defer func() {
		_, _ = (nativeRunner{}).Run(context.Background(), Command{Executable: questlock, Args: []string{"local", "stop", "--state-dir", o.StateDir}, Env: []string{"HOME=" + o.HomeDir, "PATH=" + os.Getenv("PATH")}, Dir: o.HomeDir})
	}()
	if err := os.MkdirAll(o.Workspace, 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"notify":["/usr/bin/false","preserve this"],"privateExtension":{"nested":[1,{"enabled":false}],"value":"fixture-only"},"mcpServers":{"other":{"command":"/usr/bin/false","args":[]}}}`)
	if err := os.WriteFile(o.ConfigPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateBaseline(context.Background(), o, data); err != nil {
		t.Fatal(err)
	}
	candidate, err := Plan(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	if !candidate.Validation.NativeValidated || !candidate.Validation.PreservedSettings || !candidate.Validation.NotifyPreserved {
		t.Fatalf("incomplete native Claude proof: %+v", candidate.Validation)
	}
	prepared, err := os.ReadFile(candidate.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.ConfigPath, prepared, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckLive(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	o.live = true
	c := &Candidate{Client: ClaudeCode, Path: o.ConfigPath, IsolationDir: o.HomeDir, options: o}
	output, err := (nativeRunner{}).Run(context.Background(), command(c, "mcp", "get", "questlock"))
	if err != nil || !strings.Contains(string(output), "Connected") {
		t.Fatal("real Claude MCP get did not report Connected")
	}
}
