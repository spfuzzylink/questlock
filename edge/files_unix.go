//go:build darwin || linux

package edge

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func checkPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return errors.New("absolute canonical edge path required")
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, current), current) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("edge paths must not contain symlinks")
		}
		if current != path && !info.IsDir() {
			return errors.New("edge parent must be a directory")
		}
	}
	return nil
}
func owned(info os.FileInfo) bool {
	v, ok := info.Sys().(*syscall.Stat_t)
	return ok && v.Uid == uint32(os.Geteuid())
}
func privateFile(info os.FileInfo) bool {
	v, ok := info.Sys().(*syscall.Stat_t)
	return ok && owned(info) && v.Nlink == 1 && info.Mode().IsRegular() && info.Mode().Perm() == 0600 && info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 && info.Size() <= MaxFileBytes
}
func checkPrivateFiles(directory string) error {
	if err := checkPath(directory); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || !owned(info) || info.Mode().Perm() != 0700 {
		return errors.New("edge directory must be owned and mode 0700")
	}
	for _, name := range []string{"edge.lock", "edge.db", "edge.db-wal", "edge.db-shm", "edge.db-journal"} {
		info, err := os.Lstat(filepath.Join(directory, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !privateFile(info) {
			return errors.New("edge files must be private owned regular files with one link and bounded size")
		}
	}
	return nil
}
func openPrivate(directory string) (*os.File, string, error) {
	if err := checkPath(directory); err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, "", err
	}
	if err := checkPrivateFiles(directory); err != nil {
		return nil, "", err
	}
	path := filepath.Join(directory, "edge.db")
	fd, err := unix.Open(filepath.Join(directory, "edge.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, "", err
	}
	lock := os.NewFile(uintptr(fd), filepath.Join(directory, "edge.lock"))
	bad := func(err error) (*os.File, string, error) { lock.Close(); return nil, "", err }
	info, err := lock.Stat()
	if err != nil || !privateFile(info) {
		return bad(errors.New("unsafe edge lock"))
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return bad(ErrBusy)
	}
	dbFD, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return bad(err)
	}
	file := os.NewFile(uintptr(dbFD), path)
	info, err = file.Stat()
	file.Close()
	if err != nil || !privateFile(info) {
		return bad(errors.New("unsafe edge database"))
	}
	return lock, path, nil
}
func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
