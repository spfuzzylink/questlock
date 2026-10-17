// Run from a source checkout: go run ./examples/edge
// Downloaded binaries expose the same fixture as questlock edge quest.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/spfuzzylink/questlock/internal/edgequest"
	"os"
	"time"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := edgequest.Run(ctx)
	if err == nil {
		err = json.NewEncoder(os.Stdout).Encode(report)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
