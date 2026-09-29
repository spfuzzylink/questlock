// Run against a local broker: AGENT_FENCE_TOKEN=... go run ./examples/publish
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"github.com/spfuzzylink/agent-fence/client"
	"github.com/spfuzzylink/agent-fence/protocol"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	base := os.Getenv("AGENT_FENCE_URL")
	if base == "" {
		base = "http://127.0.0.1:8080"
	}
	c, err := client.New(base, os.Getenv("AGENT_FENCE_TOKEN"))
	if err != nil {
		return err
	}
	ctx := context.Background()
	key := "memory/example.txt"
	current, err := c.Get(ctx, key)
	var apiError *client.Error
	if err != nil && !(errors.As(err, &apiError) && apiError.Code == "not_found") {
		return err
	}
	op := make([]byte, 16)
	if _, err := rand.Read(op); err != nil {
		return err
	}
	request := protocol.PublishRequest{Key: key, ExpectedVersion: current.Version,
		OperationID: hex.EncodeToString(op), Content: "A result from a cooperating agent."}
	// Persist request in the worker's own scratch space before publishing if the
	// worker needs to retry after a crash. Reuse this exact request for a retry.
	result, err := c.Publish(ctx, request)
	if err != nil {
		return err
	} // On 409, re-read and reconcile; don't blindly overwrite.
	fmt.Printf("Published %s at version %d\n", result.Artifact.Key, result.Artifact.Version)
	retry, err := c.Publish(ctx, request)
	if err != nil {
		return err
	}
	fmt.Printf("Same operation retried: replayed=%v, version=%d\n", retry.Replayed, retry.Artifact.Version)
	return nil
}
