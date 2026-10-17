package desktopbundle

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testOptions(t *testing.T) Options {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("MCPB Go binaries currently support darwin and linux")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Options{Executable: executable, Output: filepath.Join(dir, "questlock.mcpb"), Version: "v0.7.0"}
}

func TestCreateCompleteDeterministicBundle(t *testing.T) {
	o := testOptions(t)
	first, err := Create(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if first.Path != o.Output || first.Platform != runtime.GOOS || first.Architecture != runtime.GOARCH || !first.NativeValidationRequired || first.Status != "native_validation_required" || first.DesktopNativeSupported != (runtime.GOOS == "darwin") {
		t.Fatalf("incorrect build evidence: %+v", first)
	}
	data, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	if first.SHA256 != hex.EncodeToString(hash[:]) {
		t.Fatal("bundle hash differs from completed output")
	}
	o.Output = filepath.Join(filepath.Dir(o.Output), "second.mcpb")
	second, err := Create(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if second.SHA256 != first.SHA256 {
		t.Fatal("identical source produced non-reproducible bundle")
	}
	archive, err := zip.OpenReader(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if len(archive.File) != 5 {
		t.Fatalf("unexpected private or extra bundle files: %d", len(archive.File))
	}
	entries := map[string]*zip.File{}
	for _, file := range archive.File {
		if _, exists := entries[file.Name]; exists {
			t.Fatal("duplicate archive path")
		}
		entries[file.Name] = file
		if file.Modified.UTC() != time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC) {
			t.Fatalf("source timestamp leaked: %v", file.Modified)
		}
		if strings.HasPrefix(file.Name, "/") || strings.Contains(file.Name, "..") || strings.Contains(file.Name, "\\") {
			t.Fatalf("unsafe archive path: %q", file.Name)
		}
		wantMode := os.FileMode(0644)
		if file.Name == "server/questlock" {
			wantMode = 0755
		}
		if file.Mode().Perm() != wantMode || !file.Mode().IsRegular() {
			t.Fatalf("incorrect archive file mode: %s %v", file.Name, file.Mode())
		}
	}
	var m manifest
	if err := json.Unmarshal(readEntry(t, entries["manifest.json"]), &m); err != nil {
		t.Fatal(err)
	}
	if m.ManifestVersion != "0.3" || m.Name != "questlock" || m.Version != "0.7.0" || m.License != "MIT" || m.Author.Name == "" || m.Description == "" {
		t.Fatalf("required manifest metadata missing: %+v", m)
	}
	if m.Server.Type != "binary" || m.Server.EntryPoint != "server/questlock" || m.Server.MCPConfig.Command != "${__dirname}/server/questlock" {
		t.Fatal("manifest does not launch bundled binary")
	}
	wantArgs := []string{"worker", "local-serve", "--client", "claude-desktop", "--state-dir", "${user_config.state_dir}", "--workspace", "${user_config.workspace}"}
	if !reflect.DeepEqual(m.Server.MCPConfig.Args, wantArgs) {
		t.Fatalf("unexpected server arguments: %q", m.Server.MCPConfig.Args)
	}
	if !reflect.DeepEqual(m.Compatibility.Platforms, []string{runtime.GOOS}) {
		t.Fatal("bundle advertised incompatible operating systems")
	}
	for key, suffix := range map[string]string{"state_dir": "local", "workspace": "requests"} {
		setting, ok := m.UserConfig[key]
		if !ok || setting.Type != "directory" || !setting.Required || setting.Default != "${HOME}/.local/share/questlock/"+suffix {
			t.Fatalf("unsafe directory default: %+v", setting)
		}
	}
	meta := m.Meta["io.github.spfuzzylink.questlock"]
	if meta.GOOS != runtime.GOOS || meta.GOARCH != runtime.GOARCH || !meta.NativeValidationRequired {
		t.Fatalf("missing binary architecture evidence: %+v", meta)
	}
	if strings.Contains(string(readEntry(t, entries["manifest.json"])), filepath.Dir(o.Output)) {
		t.Fatal("private generation path leaked into manifest")
	}
	executable, err := os.ReadFile(o.Executable)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readEntry(t, entries["server/questlock"]), executable) {
		t.Fatal("bundled executable differs from complete source")
	}
	for _, name := range []string{"LICENSE", "ATTRIBUTION.md", "THIRD_PARTY_NOTICES.md"} {
		canonical, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(readEntry(t, entries[name]), canonical) {
			t.Fatalf("bundled notice differs from canonical %s", name)
		}
	}
}

func readEntry(t *testing.T, file *zip.File) []byte {
	t.Helper()
	if file == nil {
		t.Fatal("missing bundle file")
	}
	reader, err := file.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestEmbeddedLegalNoticesMatchCanonical(t *testing.T) {
	for _, name := range []string{"LICENSE", "ATTRIBUTION.md", "THIRD_PARTY_NOTICES.md"} {
		embedded, err := legalassets.ReadFile("legalassets/" + name)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(embedded, canonical) {
			t.Fatalf("refresh internal/desktopbundle/legalassets/%s from canonical repository file", name)
		}
	}
}

func TestRefusesExistingOutputAndSymlinks(t *testing.T) {
	for _, kind := range []string{"file", "directory", "symlink", "ancestor"} {
		t.Run(kind, func(t *testing.T) {
			o := testOptions(t)
			sentinel := []byte("existing human file")
			switch kind {
			case "file":
				if err := os.WriteFile(o.Output, sentinel, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(o.Output, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(filepath.Dir(o.Output), "missing"), o.Output); err != nil {
					t.Fatal(err)
				}
			case "ancestor":
				realDir := filepath.Join(filepath.Dir(o.Output), "real")
				if err := os.Mkdir(realDir, 0700); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(filepath.Dir(o.Output), "link")
				if err := os.Symlink(realDir, link); err != nil {
					t.Fatal(err)
				}
				o.Output = filepath.Join(link, "questlock.mcpb")
			}
			if _, err := Create(context.Background(), o); err == nil {
				t.Fatal("unsafe existing output was overwritten")
			}
			if kind == "file" {
				after, _ := os.ReadFile(o.Output)
				if !bytes.Equal(after, sentinel) {
					t.Fatal("existing human file changed")
				}
			}
		})
	}
}

func TestSourceExecutableChecksAndLauncherSymlink(t *testing.T) {
	o := testOptions(t)
	link := filepath.Join(filepath.Dir(o.Output), "launcher")
	if err := os.Symlink(o.Executable, link); err != nil {
		t.Fatal(err)
	}
	o.Executable = link
	if _, err := Create(context.Background(), o); err != nil {
		t.Fatal("intentional executable launcher symlink rejected:", err)
	}
	for _, kind := range []string{"non_executable", "script", "directory", "empty", "relative"} {
		t.Run(kind, func(t *testing.T) {
			o := testOptions(t)
			source := filepath.Join(filepath.Dir(o.Output), "source")
			switch kind {
			case "non_executable":
				if err := os.WriteFile(source, []byte("not executable"), 0600); err != nil {
					t.Fatal(err)
				}
			case "script":
				if err := os.WriteFile(source, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(source, 0700); err != nil {
					t.Fatal(err)
				}
			case "empty":
				if err := os.WriteFile(source, nil, 0700); err != nil {
					t.Fatal(err)
				}
			case "relative":
				source = "source"
			}
			o.Executable = source
			if _, err := Create(context.Background(), o); err == nil {
				t.Fatal("invalid executable source accepted")
			}
			if _, err := os.Lstat(o.Output); !os.IsNotExist(err) {
				t.Fatal("failed preflight left output behind")
			}
		})
	}
}

func TestContextAndVersionValidation(t *testing.T) {
	o := testOptions(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Create(ctx, o); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
	if _, err := os.Stat(o.Output); !os.IsNotExist(err) {
		t.Fatal("cancelled build created output")
	}
	for _, version := range []string{"", "dev", "0.7", "01.0.0", "0.7.0-01", "0.7.0\nprivate"} {
		o.Version = version
		if _, err := Create(context.Background(), o); err == nil {
			t.Fatalf("invalid manifest version accepted: %q", version)
		}
	}
	for _, version := range []string{"0.7.0", "v0.7.0", "0.7.0-beta.1", "0.7.0+build.2"} {
		if _, err := normalizeVersion(version); err != nil {
			t.Fatalf("valid semantic version rejected: %q", version)
		}
	}
}

func TestPlatformManifestBoundary(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		for _, goarch := range []string{"amd64", "arm64"} {
			m, err := newManifest("0.7.0", goos, goarch)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(m.Compatibility.Platforms, []string{goos}) || m.Meta["io.github.spfuzzylink.questlock"].DesktopNativeSupported != (goos == "darwin") {
				t.Fatal("platform compatibility is inaccurate")
			}
		}
	}
	for _, target := range [][2]string{{"windows", "amd64"}, {"darwin", "386"}, {"linux", "riscv64"}} {
		if _, err := newManifest("0.7.0", target[0], target[1]); err == nil {
			t.Fatalf("unsupported target advertised: %v", target)
		}
	}
}
