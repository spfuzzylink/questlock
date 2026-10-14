//go:build darwin || linux

package worker

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Walk from a pinned workspace descriptor with O_NOFOLLOW on each component.
// This rejects symlinks atomically, including a link swapped after validation.
func openRequestFile(root *os.Root, path string) (*os.File, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, ErrConfiguration
	}
	defer func() { directory.Close() }()
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, component := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		fd, err := unix.Openat(int(directory.Fd()), component, flags, 0)
		if err != nil {
			return nil, ErrConfiguration
		}
		file := os.NewFile(uintptr(fd), "approved_request")
		if i == len(parts)-1 {
			return file, nil
		}
		directory.Close()
		directory = file
	}
	return nil, ErrConfiguration
}
