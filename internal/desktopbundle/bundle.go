// Package desktopbundle creates a self-contained MCPB archive for native
// installation in Claude Desktop. Creating an archive does not install or
// validate it in the Desktop application and never changes client settings.
package desktopbundle

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"debug/macho"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// legalassets are exact copies of the public repository notices. The regression
// test requires updating these copies whenever their canonical files change.
//
//go:embed legalassets/LICENSE legalassets/ATTRIBUTION.md legalassets/THIRD_PARTY_NOTICES.md
var legalassets embed.FS

type Options struct {
	Executable string
	Output     string
	Version    string
}

type Result struct {
	Path                     string `json:"path"`
	SHA256                   string `json:"sha256"`
	Platform                 string `json:"platform"`
	Architecture             string `json:"architecture"`
	Status                   string `json:"status"`
	NativeValidationRequired bool   `json:"native_validation_required"`
	DesktopNativeSupported   bool   `json:"desktop_native_supported"`
}

// Create writes a new MCPB 0.3 ZIP containing only the supplied Go executable,
// fixed public metadata, and public license notices. Existing output paths and
// symlink destinations are refused. A launcher symlink for the executable is
// intentionally resolved, then the actual opened regular executable is copied.
// The completed archive is published with an exclusive hard link so incomplete
// output and existing files cannot be mistaken for a successfully built bundle.
func Create(ctx context.Context, options Options) (result Result, err error) {
	if err := ctx.Err(); err != nil {
		return result, err
	}
	version, err := normalizeVersion(options.Version)
	if err != nil {
		return result, err
	}
	if options.Output == "" || filepath.Ext(options.Output) != ".mcpb" {
		return result, errors.New("bundle output must be a new .mcpb path")
	}
	output, err := filepath.Abs(options.Output)
	if err != nil {
		return result, errors.New("cannot resolve bundle output path")
	}
	if err := checkOutput(output); err != nil {
		return result, err
	}
	if !filepath.IsAbs(options.Executable) {
		return result, errors.New("bundle source executable must be an absolute path")
	}
	sourcePath, err := filepath.EvalSymlinks(options.Executable)
	if err != nil {
		return result, errors.New("cannot resolve bundle source executable")
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return result, errors.New("cannot open bundle source executable")
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Size() == 0 || info.Size() > 256<<20 {
		return result, errors.New("bundle source must be a regular executable of at most 256MiB")
	}
	goos, goarch, err := executableTarget(source)
	if err != nil {
		return result, err
	}
	if goos != runtime.GOOS || goarch != runtime.GOARCH {
		return result, errors.New("bundle source executable must match the current operating system and architecture")
	}
	manifest, err := newManifest(version, goos, goarch)
	if err != nil {
		return result, err
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return result, errors.New("cannot encode bundle manifest")
	}
	manifestData = append(manifestData, '\n')
	temp, err := os.CreateTemp(filepath.Dir(output), ".questlock-mcpb-*")
	if err != nil {
		return result, errors.New("cannot create private bundle staging file")
	}
	tempPath := temp.Name()
	defer func() { _ = temp.Close(); _ = os.Remove(tempPath) }()
	hash := sha256.New()
	archive := zip.NewWriter(io.MultiWriter(temp, hash))
	if err := writeEntry(ctx, archive, "manifest.json", 0644, bytes.NewReader(manifestData), int64(len(manifestData))); err != nil {
		_ = archive.Close()
		return result, err
	}
	if err := writeEntry(ctx, archive, "server/questlock", 0755, io.NewSectionReader(source, 0, info.Size()), info.Size()); err != nil {
		_ = archive.Close()
		return result, err
	}
	for _, name := range []string{"LICENSE", "ATTRIBUTION.md", "THIRD_PARTY_NOTICES.md"} {
		data, err := legalassets.ReadFile("legalassets/" + name)
		if err != nil {
			_ = archive.Close()
			return result, errors.New("cannot read embedded public license notice")
		}
		if err := writeEntry(ctx, archive, name, 0644, bytes.NewReader(data), int64(len(data))); err != nil {
			_ = archive.Close()
			return result, err
		}
	}
	if err := archive.Close(); err != nil {
		return result, errors.New("cannot finish bundle archive")
	}
	if err := temp.Sync(); err != nil {
		return result, errors.New("cannot persist complete bundle archive")
	}
	if err := temp.Close(); err != nil {
		return result, errors.New("cannot close complete bundle archive")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if after, err := source.Stat(); err != nil || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return result, errors.New("bundle source executable changed while it was copied")
	}
	// Recheck the path before publishing. Link itself refuses any existing final
	// path, including a symlink created after the initial preflight check.
	if err := checkOutput(output); err != nil {
		return result, err
	}
	if err := os.Link(tempPath, output); err != nil {
		return result, errors.New("cannot publish bundle: output must be a new file in an existing directory")
	}
	return Result{Path: output, SHA256: hex.EncodeToString(hash.Sum(nil)), Platform: goos, Architecture: goarch, Status: "native_validation_required", NativeValidationRequired: true, DesktopNativeSupported: goos == "darwin"}, nil
}

func writeEntry(ctx context.Context, archive *zip.Writer, name string, mode os.FileMode, source io.Reader, size int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(mode)
	// A fixed DOS-era timestamp makes archives reproducible and contains no
	// source filesystem metadata or build machine timestamps.
	header.SetModTime(time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC))
	entry, err := archive.CreateHeader(header)
	if err != nil {
		return errors.New("cannot create bundle archive entry")
	}
	if copied, err := io.Copy(entry, contextReader{ctx, source}); err != nil || copied != size {
		return errors.New("cannot copy complete bundle archive entry")
	}
	return nil
}

type contextReader struct {
	context context.Context
	source  io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.context.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(data)
}

func checkOutput(path string) error {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return errors.New("cannot inspect bundle output path")
		}
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("bundle output refuses symlink files or directories")
			}
			if current == filepath.Clean(path) {
				return errors.New("bundle output already exists; choose a new path")
			}
			if !info.IsDir() {
				return errors.New("bundle output ancestor must be a directory")
			}
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}

func executableTarget(source *os.File) (string, string, error) {
	if _, err := buildinfo.Read(source); err != nil {
		return "", "", errors.New("bundle source must be a compiled Go executable")
	}
	if binary, err := macho.NewFile(source); err == nil {
		if binary.Type != macho.TypeExec {
			return "", "", errors.New("bundle source is not an executable Mach-O image")
		}
		switch binary.Cpu {
		case macho.CpuAmd64:
			return "darwin", "amd64", nil
		case macho.CpuArm64:
			return "darwin", "arm64", nil
		}
	}
	if binary, err := elf.NewFile(source); err == nil {
		if binary.Type != elf.ET_EXEC && binary.Type != elf.ET_DYN {
			return "", "", errors.New("bundle source is not an executable ELF image")
		}
		switch binary.Machine {
		case elf.EM_X86_64:
			return "linux", "amd64", nil
		case elf.EM_AARCH64:
			return "linux", "arm64", nil
		}
	}
	return "", "", errors.New("bundle source must target supported darwin or linux amd64/arm64 platforms")
}

var semverPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

func normalizeVersion(version string) (string, error) {
	version = strings.TrimPrefix(version, "v")
	parts := semverPattern.FindStringSubmatch(version)
	if len(version) > 128 || parts == nil {
		return "", errors.New("bundle version must be a semantic version such as 0.7.0")
	}
	for _, part := range strings.Split(parts[4], ".") {
		if len(part) > 1 && part[0] == '0' && strings.Trim(part, "0123456789") == "" {
			return "", errors.New("bundle prerelease version must not contain leading-zero numeric identifiers")
		}
	}
	return version, nil
}
