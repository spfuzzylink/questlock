//go:build darwin || linux

package configguard

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func safePath(path string, missingLeaf bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("absolute configuration path without traversal required")
	}
	current := string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(path, current), current)
	for index, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) && missingLeaf && index == len(parts)-1 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("configuration path unavailable: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symbolic links in configuration paths are refused")
		}
		if index != len(parts)-1 && !info.IsDir() {
			return errors.New("configuration parent must be a directory")
		}
	}
	return nil
}

func privateDirectory(path string, create bool) error {
	if err := safePath(path, create); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && create {
		if err = os.Mkdir(path, 0700); err != nil {
			return err
		}
		if err = syncDir(filepath.Dir(path)); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 || !owned(info) {
		return errors.New("recovery directory must be owned by this user with mode 0700")
	}
	return nil
}

func owned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func readRegular(path string) ([]byte, os.FileMode, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, 0, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), "configuration")
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxConfig || !owned(info) || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return nil, 0, errors.New("configuration must be an owned regular file of at most 8 MiB")
	}
	b, err := io.ReadAll(io.LimitReader(file, maxConfig+1))
	if err != nil || len(b) > maxConfig {
		return nil, 0, errors.New("configuration read failed or exceeds 8 MiB")
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(info, after) || info.ModTime() != after.ModTime() || info.Size() != after.Size() || info.Mode() != after.Mode() {
		return nil, 0, errors.New("configuration changed while being read")
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(after, current) || current.Mode() != after.Mode() {
		return nil, 0, errors.New("configuration replaced while being read")
	}
	return b, info.Mode(), nil
}

func lockPath(target string) string {
	return filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".questlock.lock")
}

func acquireLock(target string) (*os.File, error) {
	if err := safePath(target, true); err != nil {
		return nil, err
	}
	fd, err := unix.Open(lockPath(target), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, fmt.Errorf("configuration lock unavailable: %w", err)
	}
	file := os.NewFile(uintptr(fd), "configuration-lock")
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !owned(info) {
		file.Close()
		return nil, errors.New("configuration lock must be a private owned regular file")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("another configuration transaction is active")
	}
	return file, nil
}

func checkAuthority(pipe, lock *os.File, target string) error {
	pipeInfo, err := pipe.Stat()
	if err != nil || pipeInfo.Mode()&os.ModeNamedPipe == 0 {
		return errors.New("watchdog parent pipe unavailable")
	}
	lockInfo, err := lock.Stat()
	if err != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm() != 0600 || !owned(lockInfo) {
		return errors.New("watchdog lock unavailable")
	}
	pathInfo, err := os.Lstat(lockPath(target))
	if err != nil || !os.SameFile(lockInfo, pathInfo) || pathInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("watchdog lock does not belong to target")
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.New("watchdog configuration lock is held by another transaction")
	}
	return nil
}

func syncDir(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func writeAtomic(path string, b []byte, mode os.FileMode, check func() error) error {
	_, err := atomicChecked(path, b, mode, check)
	return err
}

// The lock serializes cooperating Questlock processes. POSIX has no portable
// content-and-mode compare-and-rename operation: a noncooperating writer can
// still race the final check and rename. Checks are repeated immediately before
// rename, and later divergent edits are always preserved during rollback.
func atomicChecked(path string, b []byte, mode os.FileMode, check func() error) (bool, error) {
	if err := safePath(path, true); err != nil {
		return false, err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".questlock-stage-")
	if err != nil {
		return false, err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(mode); err == nil {
		_, err = file.Write(b)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return false, errors.Join(err, closeErr)
	}
	if err = safePath(path, true); err != nil {
		return false, err
	}
	if check != nil {
		if err = check(); err != nil {
			return false, err
		}
	}
	if err = os.Rename(name, path); err != nil {
		return false, err
	}
	return true, syncDir(filepath.Dir(path))
}

func replaceTarget(path string, b []byte, after Snapshot, check func() error) (bool, error) {
	if after.Exists {
		return atomicChecked(path, b, os.FileMode(after.Mode), check)
	}
	if err := safePath(path, true); err != nil {
		return false, err
	}
	if err := check(); err != nil {
		return false, err
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, syncDir(filepath.Dir(path))
}
