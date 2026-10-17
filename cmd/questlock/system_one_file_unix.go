//go:build darwin || linux

package main

import (
	"errors"
	"os"

	"github.com/spfuzzylink/questlock/systemone"
	"golang.org/x/sys/unix"
)

func openSystemOneRequest(path string) (*os.File, error) {
	invalid := errors.New("typed-choice request must be an unchanged regular file of at most 16384 bytes")
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > systemone.MaxRequestBytes {
		return nil, invalid
	}
	// A replacement FIFO cannot block open, and a replacement symlink cannot
	// redirect the read. The opened inode must still match the inspected file.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, invalid
	}
	file := os.NewFile(uintptr(fd), "typed_choice_request")
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || after.Size() > systemone.MaxRequestBytes || !os.SameFile(before, after) {
		file.Close()
		return nil, invalid
	}
	return file, nil
}
