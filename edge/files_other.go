//go:build !darwin && !linux

package edge

import (
	"errors"
	"os"
)

var unsupported = errors.New("durable edge files currently require macOS or Linux")

func openPrivate(string) (*os.File, string, error) { return nil, "", unsupported }
func checkPrivateFiles(string) error               { return unsupported }
func syncDirectory(string) error                   { return unsupported }
