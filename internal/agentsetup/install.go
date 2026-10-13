// Package agentsetup installs the running Questlock executable and embedded
// workflow skills. It never starts services or changes an assistant's settings.
package agentsetup

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed assets
var assets embed.FS

type Options struct {
	Home       string
	Executable string
	CodexHome  string // Empty uses Home/.codex, matching CODEX_HOME's default.
	Agents     []string
	Replace    bool
}

type Skill struct {
	Agent string `json:"agent"`
	Path  string `json:"path"`
}

type Result struct {
	Binary string  `json:"binary"`
	Skills []Skill `json:"skills"`
}

// Install copies the supplied real executable and self-contained skills. Home
// and Executable are injected explicitly so tests cannot change a real profile.
// Existing differing skill content requires Replace. Identical installs are
// idempotent. No destination path may traverse a symlink.
func Install(options Options) (Result, error) {
	var result Result
	if !filepath.IsAbs(options.Home) || (options.CodexHome != "" && !filepath.IsAbs(options.CodexHome)) {
		return result, errors.New("home and CODEX_HOME must be absolute paths")
	}
	if len(options.Agents) == 0 {
		return result, errors.New("at least one agent is required")
	}
	files, err := embeddedFiles()
	if err != nil {
		return result, err
	}
	result.Binary = filepath.Join(options.Home, ".local", "bin", "questlock")
	codexHome := options.CodexHome
	if codexHome == "" {
		codexHome = filepath.Join(options.Home, ".codex")
	}
	seenAgents, seenPaths := map[string]bool{}, map[string]bool{}
	var paths []string
	for _, agent := range options.Agents {
		var root string
		switch agent {
		case "codex":
			root = codexHome
		case "claude":
			root = filepath.Join(options.Home, ".claude")
		default:
			return Result{}, errors.New("agents must be codex or claude")
		}
		if seenAgents[agent] {
			return Result{}, errors.New("agent names must not be repeated")
		}
		seenAgents[agent] = true
		path := filepath.Join(root, "skills", "questlock")
		result.Skills = append(result.Skills, Skill{Agent: agent, Path: path})
		if !seenPaths[path] {
			paths = append(paths, path)
			seenPaths[path] = true
		}
	}
	// Overlapping destinations could replace a directory needed by another
	// target. Reject them before creating any directories or copying files.
	targets := append([]string{result.Binary}, paths...)
	for i, a := range targets {
		for j, b := range targets {
			if i != j && (a == b || strings.HasPrefix(b, a+string(filepath.Separator))) {
				return Result{}, errors.New("binary and skill installation destinations must not overlap")
			}
		}
	}
	if err := checkPath(result.Binary, false); err != nil {
		return Result{}, err
	}
	for _, path := range paths {
		if err := checkPath(path, true); err != nil {
			return Result{}, err
		}
		identical, err := matchesSkill(path, files)
		if err != nil {
			return Result{}, err
		}
		if !identical && exists(path) && !options.Replace {
			return Result{}, errors.New("an existing Questlock skill differs; use --replace to replace customized skill contents")
		}
	}
	// Source symlinks are resolved because os.Executable can name a launcher
	// symlink. Only the real, regular executable's bytes are copied.
	sourcePath, err := filepath.EvalSymlinks(options.Executable)
	if err != nil {
		return Result{}, errors.New("cannot resolve the running executable")
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return Result{}, errors.New("cannot open the running executable")
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Size() < 1 || info.Size() > 256<<20 {
		return Result{}, errors.New("source must be a regular executable of at most 256MiB")
	}
	for _, path := range paths {
		if err := installSkill(path, files, options.Replace); err != nil {
			return Result{}, err
		}
	}
	if err := installBinary(result.Binary, source); err != nil {
		return Result{}, err
	}
	return result, nil
}

func embeddedFiles() (map[string][]byte, error) {
	files := map[string][]byte{}
	err := fs.WalkDir(assets, "assets", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := assets.ReadFile(path)
		if err != nil {
			return err
		}
		files[strings.TrimPrefix(path, "assets/")] = data
		return nil
	})
	if err != nil {
		return nil, errors.New("cannot read embedded workflow skill")
	}
	return files, nil
}

// checkPath checks every existing ancestor and the target without following a
// symlink, including when the final target does not exist yet.
func checkPath(path string, directory bool) error {
	path = filepath.Clean(path)
	var paths []string
	for current := path; ; current = filepath.Dir(current) {
		paths = append(paths, current)
		if filepath.Dir(current) == current {
			break
		}
	}
	for i := len(paths) - 1; i >= 0; i-- {
		info, err := os.Lstat(paths[i])
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return errors.New("cannot inspect installation destination")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("installation refuses symlink files or directories")
		}
		wantDirectory := i != 0 || directory
		if wantDirectory && !info.IsDir() {
			return errors.New("installation destination requires a directory")
		}
		if !wantDirectory && !info.Mode().IsRegular() {
			return errors.New("binary installation destination must be a regular file")
		}
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func matchesSkill(path string, files map[string][]byte) (bool, error) {
	if !exists(path) {
		return false, nil
	}
	seen := map[string]bool{}
	identical := true
	err := filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot inspect existing Questlock skill")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("installation refuses symlink files or directories")
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("existing skill contains an unsupported file type")
		}
		relative, err := filepath.Rel(path, current)
		if err != nil {
			return errors.New("cannot inspect existing Questlock skill")
		}
		name := filepath.ToSlash(relative)
		seen[name] = true
		want, ok := files[name]
		if !ok {
			identical = false
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return errors.New("cannot inspect existing Questlock skill file")
		}
		if info.Size() != int64(len(want)) {
			identical = false
			return nil
		}
		data, err := os.ReadFile(current)
		if err != nil {
			return errors.New("cannot read existing Questlock skill")
		}
		if !bytes.Equal(data, want) {
			identical = false
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return identical && len(seen) == len(files), nil
}

func makeDirectory(path string) error {
	if err := checkPath(path, true); err != nil {
		return err
	}
	if exists(path) {
		return nil
	}
	if err := makeDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	err := os.Mkdir(path, 0755)
	if err != nil && !os.IsExist(err) {
		return errors.New("cannot create installation directory")
	}
	if err == nil {
		if err := os.Chmod(path, 0755); err != nil {
			return errors.New("cannot set installation directory permissions")
		}
	}
	return checkPath(path, true)
}

func installSkill(path string, files map[string][]byte, replace bool) error {
	if err := checkPath(path, true); err != nil {
		return err
	}
	identical, err := matchesSkill(path, files)
	if err != nil {
		return err
	}
	if identical {
		return nil
	}
	if exists(path) && !replace {
		return errors.New("an existing Questlock skill differs; use --replace to replace customized skill contents")
	}
	parent := filepath.Dir(path)
	if err := makeDirectory(parent); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".questlock-install-")
	if err != nil {
		return errors.New("cannot stage workflow skill")
	}
	defer os.RemoveAll(stage)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		destination := filepath.Join(stage, filepath.FromSlash(name))
		if err := makeDirectory(filepath.Dir(destination)); err != nil {
			return err
		}
		// A private stage keeps incomplete assets hidden until every file has
		// been written; installed assets contain no credentials.
		if err := os.WriteFile(destination, files[name], 0600); err != nil {
			return errors.New("cannot stage workflow skill file")
		}
		if err := os.Chmod(destination, 0644); err != nil {
			return errors.New("cannot set workflow skill file permissions")
		}
	}
	if err := os.Chmod(stage, 0755); err != nil {
		return errors.New("cannot set workflow skill directory permissions")
	}
	if err := checkPath(path, true); err != nil {
		return err
	}
	backup := stage + "-previous"
	if exists(path) {
		if err := os.Rename(path, backup); err != nil {
			return errors.New("cannot preserve the previous workflow skill")
		}
	}
	if err := os.Rename(stage, path); err != nil {
		if exists(backup) {
			_ = os.Rename(backup, path)
		}
		return errors.New("cannot install workflow skill")
	}
	if exists(backup) {
		if err := os.RemoveAll(backup); err != nil {
			return errors.New("workflow skill installed; cannot remove its previous staged copy")
		}
	}
	return nil
}

func installBinary(path string, source *os.File) error {
	if err := checkPath(path, false); err != nil {
		return err
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return errors.New("cannot read the running executable")
	}
	if exists(path) {
		current, err := os.Open(path)
		if err != nil {
			return errors.New("cannot inspect installed executable")
		}
		oldHash, newHash := sha256.New(), sha256.New()
		_, oldErr := io.Copy(oldHash, current)
		_, newErr := io.Copy(newHash, source)
		info, statErr := current.Stat()
		_ = current.Close()
		if oldErr != nil || newErr != nil || statErr != nil {
			return errors.New("cannot compare installed executable")
		}
		if bytes.Equal(oldHash.Sum(nil), newHash.Sum(nil)) && info.Mode().Perm() == 0755 {
			return nil
		}
		if _, err := source.Seek(0, io.SeekStart); err != nil {
			return errors.New("cannot read the running executable")
		}
	}
	parent := filepath.Dir(path)
	if err := makeDirectory(parent); err != nil {
		return err
	}
	stage, err := os.CreateTemp(parent, ".questlock-install-")
	if err != nil {
		return errors.New("cannot stage executable")
	}
	stagePath := stage.Name()
	defer os.Remove(stagePath)
	if _, err := io.Copy(stage, source); err != nil {
		stage.Close()
		return errors.New("cannot copy the running executable")
	}
	if err := stage.Chmod(0755); err != nil {
		stage.Close()
		return errors.New("cannot set executable permissions")
	}
	if err := stage.Sync(); err != nil {
		stage.Close()
		return errors.New("cannot sync staged executable")
	}
	if err := stage.Close(); err != nil {
		return errors.New("cannot close staged executable")
	}
	if err := checkPath(path, false); err != nil {
		return err
	}
	if err := os.Rename(stagePath, path); err != nil {
		return errors.New("cannot install executable")
	}
	return nil
}
