//go:build !darwin && !linux

package main

import (
	"errors"
	"os"
)

func openSystemOneRequest(_ string) (*os.File, error) {
	return nil, errors.New("bounded typed-choice request files require macOS or Linux")
}
