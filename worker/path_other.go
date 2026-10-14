//go:build !darwin && !linux

package worker

import "os"

// The distributed worker targets macOS and Linux. Other platforms fail closed
// rather than weakening the atomic no-symlink request-file guarantee.
func openRequestFile(_ *os.Root, _ string) (*os.File, error) {
	return nil, ErrConfiguration
}
