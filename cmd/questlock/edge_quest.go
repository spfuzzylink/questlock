package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/spfuzzylink/questlock/internal/edgequest"
	"os"
	"time"
)

func edgeCommand(args []string) error {
	if len(args) != 1 || args[0] != "quest" {
		return errors.New("usage: questlock edge quest")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := edgequest.Run(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}
