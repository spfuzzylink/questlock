//go:build darwin || linux

package retention

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func coldOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func privateColdFile(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm() == 0600 &&
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 && coldOwned(info)
}

func openColdRoot(path string, create bool) (*os.File, error) {
	if !validRoot(path) {
		return nil, ErrInvalid
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, current), current) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) && create {
			if err = os.Mkdir(current, 0700); err != nil && !os.IsExist(err) {
				return nil, ErrUnavailable
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("%w: %w", ErrUnavailable, os.ErrNotExist)
			}
			return nil, ErrUnavailable
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrUnavailable
		}
		if create {
			// Another process may have created this name without persisting
			// its parent yet. Every writer establishes the whole path's
			// durability before its separate SQLite transaction can commit.
			parent, openErr := os.Open(filepath.Dir(current))
			if openErr != nil {
				return nil, ErrUnavailable
			}
			syncErr, closeErr := parent.Sync(), parent.Close()
			if syncErr != nil || closeErr != nil {
				return nil, ErrUnavailable
			}
		}
		if current == path && (info.Mode().Perm() != 0700 || !coldOwned(info) || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0) {
			return nil, ErrUnavailable
		}
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !coldOwned(info) {
		_ = f.Close()
		return nil, ErrUnavailable
	}
	return f, nil
}

func openColdFile(dir *os.File, name string, create bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	if create {
		flags = unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	}
	fd, err := unix.Openat(int(dir.Fd()), name, flags, 0600)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, os.ErrNotExist)
		}
		return nil, ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), "cold-file")
	info, err := f.Stat()
	if err != nil || !privateColdFile(info) {
		_ = f.Close()
		return nil, ErrUnavailable
	}
	return f, nil
}

func lockColdDirectory(ctx context.Context, dir *os.File) (*os.File, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	flags := unix.O_RDWR | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	fd, err := unix.Openat(int(dir.Fd()), coldLockName, flags|unix.O_CREAT|unix.O_EXCL, 0600)
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(int(dir.Fd()), coldLockName, flags, 0)
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), "cold-lock")
	info, err := f.Stat()
	if err != nil || !privateColdFile(info) || info.Size() != 0 {
		_ = f.Close()
		return nil, ErrUnavailable
	}
	for {
		if err = contextError(ctx); err != nil {
			_ = f.Close()
			return nil, err
		}
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			_ = f.Close()
			return nil, ErrUnavailable
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func linkColdFile(dir *os.File, old, next string) error {
	if err := unix.Linkat(int(dir.Fd()), old, int(dir.Fd()), next, 0); err != nil {
		return ErrUnavailable
	}
	return nil
}

func removeColdFile(dir *os.File, name string) error {
	if err := unix.Unlinkat(int(dir.Fd()), name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return ErrUnavailable
	}
	return nil
}
