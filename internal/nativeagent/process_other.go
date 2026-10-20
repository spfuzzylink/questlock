//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package nativeagent

import (
	"errors"
	"os"
	"os/exec"
)

func supportedPlatform() bool { return false }
func openExecutable(string) (*os.File, error) {
	return nil, errors.New("unsupported native process platform")
}
func configureProcess(*exec.Cmd) {}
func stopProcess(*exec.Cmd)      {}
