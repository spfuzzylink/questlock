//go:build !darwin && !linux

package retention

import (
	"context"
	"os"
)

// Unsupported platforms fail closed rather than publishing without durable
// directory sync, no-follow file operations and a cross-process quota lock.
func privateColdFile(os.FileInfo) bool                              { return false }
func openColdRoot(string, bool) (*os.File, error)                   { return nil, ErrUnavailable }
func openColdFile(*os.File, string, bool) (*os.File, error)         { return nil, ErrUnavailable }
func lockColdDirectory(context.Context, *os.File) (*os.File, error) { return nil, ErrUnavailable }
func linkColdFile(*os.File, string, string) error                   { return ErrUnavailable }
func removeColdFile(*os.File, string) error                         { return ErrUnavailable }
