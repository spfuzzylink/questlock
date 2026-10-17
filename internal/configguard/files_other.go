//go:build !darwin && !linux

package configguard

import (
	"errors"
	"os"
	"os/exec"
)

var errUnsupported = errors.New("safe configuration transactions require macOS or Linux")

func detach(*exec.Cmd)                                            {}
func safePath(string, bool) error                                 { return errUnsupported }
func privateDirectory(string, bool) error                         { return errUnsupported }
func readRegular(string) ([]byte, os.FileMode, error)             { return nil, 0, errUnsupported }
func acquireLock(string) (*os.File, error)                        { return nil, errUnsupported }
func checkAuthority(*os.File, *os.File, string) error             { return errUnsupported }
func syncDir(string) error                                        { return errUnsupported }
func writeAtomic(string, []byte, os.FileMode, func() error) error { return errUnsupported }
func replaceTarget(string, []byte, Snapshot, func() error) (bool, error) {
	return false, errUnsupported
}
