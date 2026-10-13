package agentsetup

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/inference"
)

func setupOptions(t *testing.T) Options {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(home, "source-questlock")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf 'fixture executable\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return Options{Home: home, Executable: executable, Agents: []string{"codex", "claude"}}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestInstallCopiesSameExecutableAndSelfContainedSkills(t *testing.T) {
	options := setupOptions(t)
	settings := filepath.Join(options.Home, ".codex", "config.toml")
	if err := os.Mkdir(filepath.Dir(settings), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte("model = \"user-choice\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := Install(options)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, result.Binary), readFile(t, options.Executable)) || len(result.Skills) != 2 {
		t.Fatalf("running executable or both skills missing: %+v", result)
	}
	if string(readFile(t, settings)) != "model = \"user-choice\"\n" {
		t.Fatal("installer modified the assistant's model settings")
	}
	files, err := embeddedFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, skill := range result.Skills {
		for name, want := range files {
			path := filepath.Join(skill.Path, filepath.FromSlash(name))
			if !bytes.Equal(readFile(t, path), want) {
				t.Fatalf("embedded asset %s differs", name)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0644 {
				t.Fatalf("skill file does not use 0644: %s %v", name, err)
			}
		}
		info, err := os.Stat(skill.Path)
		if err != nil || info.Mode().Perm() != 0755 {
			t.Fatalf("skill directory does not use 0755: %v", err)
		}
	}
	info, err := os.Stat(result.Binary)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("installed executable does not use 0755: %v", err)
	}
	if _, err := os.Stat(filepath.Join(options.Home, ".local", "share", "questlock")); !os.IsNotExist(err) {
		t.Fatal("installer unexpectedly provisioned broker state")
	}
}

func TestInstallIdenticalIsIdempotent(t *testing.T) {
	options := setupOptions(t)
	result, err := Install(options)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{result.Binary, filepath.Join(result.Skills[0].Path, "SKILL.md"), filepath.Join(result.Skills[1].Path, "scripts", "run.sh")}
	previous := make([]os.FileInfo, len(paths))
	for i, path := range paths {
		previous[i], err = os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Install(options); err != nil {
		t.Fatal(err)
	}
	for i, path := range paths {
		current, err := os.Stat(path)
		if err != nil || !os.SameFile(previous[i], current) || previous[i].ModTime() != current.ModTime() {
			t.Fatalf("identical reinstall replaced %s: %v", path, err)
		}
	}
}

func TestInstallPreservesCustomizedSkillUnlessReplace(t *testing.T) {
	options := setupOptions(t)
	result, err := Install(options)
	if err != nil {
		t.Fatal(err)
	}
	oldBinary := readFile(t, result.Binary)
	customSkill := filepath.Join(result.Skills[1].Path, "SKILL.md")
	custom := []byte("my customized workflow\n")
	if err := os.WriteFile(customSkill, custom, 0644); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(result.Skills[1].Path, "notes.txt")
	if err := os.WriteFile(extra, []byte("my private custom notes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(options.Executable, []byte("#!/bin/sh\nprintf 'updated fixture\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(options); err == nil || !strings.Contains(err.Error(), "--replace") {
		t.Fatalf("customized workflow silently replaced: %v", err)
	}
	if !bytes.Equal(readFile(t, customSkill), custom) || !bytes.Equal(readFile(t, result.Binary), oldBinary) {
		t.Fatal("rejected installation mutated the executable or custom skill")
	}
	options.Replace = true
	if _, err := Install(options); err != nil {
		t.Fatal(err)
	}
	files, _ := embeddedFiles()
	if !bytes.Equal(readFile(t, customSkill), files["SKILL.md"]) || !bytes.Equal(readFile(t, result.Binary), readFile(t, options.Executable)) {
		t.Fatal("explicit replacement did not install the embedded skill and current executable")
	}
	if _, err := os.Stat(extra); !os.IsNotExist(err) {
		t.Fatal("explicit replacement retained unknown custom files")
	}
}

func TestInstallCodexHomeOverrideAndSingleAgent(t *testing.T) {
	options := setupOptions(t)
	options.Agents = []string{"codex"}
	options.CodexHome = filepath.Join(options.Home, "alternate-codex")
	result, err := Install(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skills) != 1 || result.Skills[0].Path != filepath.Join(options.CodexHome, "skills", "questlock") {
		t.Fatalf("CODEX_HOME override ignored: %+v", result)
	}
	for _, name := range []string{".codex", ".claude"} {
		if _, err := os.Stat(filepath.Join(options.Home, name)); !os.IsNotExist(err) {
			t.Fatalf("single-agent install created unrelated %s directory", name)
		}
	}
}

func TestInstallRefusesSymlinkDestinationsIncludingReplace(t *testing.T) {
	for _, kind := range []string{"home", "codex-home", "skill", "asset", "binary"} {
		t.Run(kind, func(t *testing.T) {
			options := setupOptions(t)
			options.Replace = true
			outside := filepath.Join(options.Home, "outside")
			if err := os.Mkdir(outside, 0755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(outside, "marker")
			if err := os.WriteFile(marker, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			var link, target string
			switch kind {
			case "home":
				link, target = filepath.Join(options.Home, "linked-home"), outside
				options.Home = link
			case "codex-home":
				link, target = filepath.Join(options.Home, ".codex"), outside
			case "skill":
				link, target = filepath.Join(options.Home, ".codex", "skills", "questlock"), outside
			case "asset":
				link, target = filepath.Join(options.Home, ".codex", "skills", "questlock", "SKILL.md"), marker
			case "binary":
				link, target = filepath.Join(options.Home, ".local", "bin", "questlock"), marker
			}
			if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			if _, err := Install(options); err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("symlink accepted: %v", err)
			}
			if string(readFile(t, marker)) != "preserve" {
				t.Fatal("symlink target was modified")
			}
		})
	}
}

func TestInstallRejectsInvalidInputBeforeMutations(t *testing.T) {
	for _, kind := range []string{"missing executable", "nonexecutable", "invalid agent", "duplicate agent", "no agents", "relative home", "relative codex home", "overlap"} {
		t.Run(kind, func(t *testing.T) {
			options := setupOptions(t)
			originalHome := options.Home
			switch kind {
			case "missing executable":
				options.Executable += "-missing"
			case "nonexecutable":
				if err := os.Chmod(options.Executable, 0644); err != nil {
					t.Fatal(err)
				}
			case "invalid agent":
				options.Agents = []string{"other"}
			case "duplicate agent":
				options.Agents = []string{"codex", "codex"}
			case "no agents":
				options.Agents = nil
			case "relative home":
				options.Home = "relative"
			case "relative codex home":
				options.CodexHome = "relative"
			case "overlap":
				options.CodexHome = filepath.Join(options.Home, ".local", "bin", "questlock")
			}
			if _, err := Install(options); err == nil {
				t.Fatal("invalid installation accepted")
			}
			for _, name := range []string{".local", ".codex", ".claude"} {
				if _, err := os.Stat(filepath.Join(originalHome, name)); !os.IsNotExist(err) {
					t.Fatalf("rejected install created %s", name)
				}
			}
		})
	}
}

func TestEmbeddedSpecsBuildOffline(t *testing.T) {
	files, err := embeddedFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"references/openai-summary.json", "references/claude-summary.json", "references/local-summary.json"} {
		var spec inference.Spec
		decoder := json.NewDecoder(bytes.NewReader(files[name]))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&spec); err != nil {
			t.Fatal(err)
		}
		if _, err := inference.BuildPlan(spec); err != nil {
			t.Fatalf("embedded task example %s is invalid: %v", name, err)
		}
	}
}

func TestInstalledShellHelperTokenHandlingWithoutPaidCalls(t *testing.T) {
	options := setupOptions(t)
	stub := "#!/bin/sh\nprintf '%s\\n' \"$@\"\nif [ -n \"${QUESTLOCK_TOKEN:-}\" ]; then printf 'token-present\\n'; else printf 'token-absent\\n'; fi\nprintf 'invoked\\n' >> \"$HOME/calls.log\"\n"
	if err := os.WriteFile(options.Executable, []byte(stub), 0755); err != nil {
		t.Fatal(err)
	}
	result, err := Install(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", options.Home)
	t.Setenv("QUESTLOCK_BIN", "")
	t.Setenv("QUESTLOCK_TOKEN", "")
	t.Setenv("QUESTLOCK_TOKEN_FILE", "")
	helper := filepath.Join(result.Skills[0].Path, "scripts", "run.sh")
	invoke := func(args ...string) (string, error) {
		t.Helper()
		command := exec.Command("sh", append([]string{helper}, args...)...)
		data, err := command.CombinedOutput()
		if strings.Contains(string(data), "fixture-broker-token") {
			t.Fatal("helper printed a credential")
		}
		return string(data), err
	}
	output, err := invoke("plan", "--spec", "example.json")
	if err != nil || output != "model\nplan\n--spec\nexample.json\ntoken-absent\n" {
		t.Fatalf("offline helper unexpectedly needed/read a token: %q %v", output, err)
	}
	if _, err := invoke("run", "--spec", "example.json"); err == nil {
		t.Fatal("missing token did not stop run")
	}
	tokenFile := filepath.Join(options.Home, ".local", "share", "questlock", "agent-workflows", "worker.token")
	if err := os.MkdirAll(filepath.Dir(tokenFile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenFile, []byte("fixture-broker-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output, err = invoke("run", "--spec", "example.json")
	if err != nil || !strings.Contains(output, "token-present") {
		t.Fatalf("token file was not used: %q %v", output, err)
	}
	t.Setenv("QUESTLOCK_TOKEN", "fixture-broker-token")
	t.Setenv("QUESTLOCK_TOKEN_FILE", tokenFile+"-missing")
	if _, err := invoke("run", "--spec", "example.json"); err != nil {
		t.Fatalf("explicit environment token did not override missing token file: %v", err)
	}
	if _, err := invoke("unknown"); err == nil {
		t.Fatal("helper accepted an unsupported command")
	}
	if strings.Count(string(readFile(t, filepath.Join(options.Home, "calls.log"))), "invoked") != 3 {
		t.Fatal("helper retried or invoked a rejected command")
	}
}
