package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/protocol"
)

// demo uses real subprocesses and a temporary on-disk database. The intentionally
// stalled worker persists its intended write, is killed, and resumes that stale
// request after another worker has published. It also hard-kills the broker.
func demo() error {
	fmt.Println("QUEST 001 — Keep a stale worker from rewriting the present")
	fmt.Println("Challenge: worker crash → newer write → stale retry → broker crash")
	dir, err := os.MkdirTemp("", "questlock-demo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db := filepath.Join(dir, "state.db")
	s, err := store.Open(db)
	if err != nil {
		return err
	}
	tokenA, err := s.CreateToken(context.Background(), "worker-a", "demo")
	if err != nil {
		s.Close()
		return err
	}
	tokenB, err := s.CreateToken(context.Background(), "worker-b", "demo")
	if err != nil {
		s.Close()
		return err
	}
	if err := s.Close(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	server, baseURL, err := startDemoServer(exe, db)
	if err != nil {
		return err
	}
	defer func() { stopDemoProcess(server) }()
	b, err := client.New(baseURL, tokenB)
	if err != nil {
		return err
	}
	ctx := context.Background()
	seed := protocol.PublishRequest{Key: "memory/summary.txt", ExpectedVersion: 0, OperationID: "seed", Content: "first result"}
	if _, err = b.Publish(ctx, seed); err != nil {
		return err
	}
	fmt.Println("PASS  Created shared artifact at version 1")

	snapshot := filepath.Join(dir, "worker-a.json")
	worker := exec.Command(exe, "_demo-worker", "snapshot", snapshot)
	worker.Env = demoEnvironment("QUESTLOCK_DEMO_URL="+baseURL, "QUESTLOCK_DEMO_TOKEN="+tokenA)
	worker.Stderr = os.Stderr
	out, err := worker.StdoutPipe()
	if err != nil {
		return err
	}
	input, err := worker.StdinPipe()
	if err != nil {
		return err
	}
	defer input.Close()
	if err = worker.Start(); err != nil {
		return err
	}
	defer stopDemoProcess(worker)
	ready := make(chan bool, 1)
	go func() { scanner := bufio.NewScanner(out); ready <- scanner.Scan() && scanner.Text() == "READY" }()
	select {
	case ok := <-ready:
		if !ok {
			return errors.New("worker failed to save its pending write")
		}
	case <-time.After(10 * time.Second):
		return errors.New("worker startup timed out")
	}
	stopDemoProcess(worker)
	fmt.Println("PASS  Killed worker A after it saved a write based on version 1")

	newer := protocol.PublishRequest{Key: seed.Key, ExpectedVersion: 1, OperationID: "worker-b-update", Content: "newer result from worker B"}
	result, err := b.Publish(ctx, newer)
	if err != nil {
		return err
	}
	if result.Artifact.Version != 2 {
		return errors.New("expected version 2")
	}
	fmt.Println("PASS  Worker B published version 2")
	resume := exec.Command(exe, "_demo-worker", "resume", snapshot)
	resume.Env = worker.Env
	if output, err := resume.CombinedOutput(); err != nil {
		return fmt.Errorf("resumed worker: %w: %s", err, output)
	}
	fmt.Println("PASS  Restarted worker A; its stale publish was rejected with HTTP 409")

	stopDemoProcess(server)
	server, baseURL, err = startDemoServer(exe, db)
	if err != nil {
		return err
	}
	b, err = client.New(baseURL, tokenB)
	if err != nil {
		return err
	}
	got, err := b.Get(ctx, seed.Key)
	if err != nil {
		return err
	}
	if got.Version != 2 || got.Content != newer.Content {
		return errors.New("artifact did not survive broker restart")
	}
	fmt.Println("PASS  Hard-killed and restarted broker; version 2 survived")
	replayed, err := b.Publish(ctx, newer)
	if err != nil {
		return err
	}
	if !replayed.Replayed || replayed.Artifact != result.Artifact {
		return errors.New("retry did not recover original result")
	}
	fmt.Println("PASS  Retried the acknowledged operation; original result returned without version 3")
	events, err := b.Audit(ctx, 100)
	if err != nil {
		return err
	}
	outcomes := map[string]int{}
	for _, event := range events {
		outcomes[event.Outcome]++
	}
	if outcomes["accepted"] != 2 || outcomes["version_conflict"] != 1 || outcomes["replayed"] != 1 {
		return fmt.Errorf("unexpected durable journal: %v", outcomes)
	}
	fmt.Println("PASS  Durable journal contains 2 accepted, 1 rejected, and 1 replayed publish")
	fmt.Println("QUEST CLEARED — seven checks passed. Temporary state removed; no model API required.")
	return nil
}

func demoWorker(args []string) error {
	if len(args) != 2 {
		return errors.New("invalid internal demo invocation")
	}
	c, err := client.New(os.Getenv("QUESTLOCK_DEMO_URL"), os.Getenv("QUESTLOCK_DEMO_TOKEN"))
	if err != nil {
		return err
	}
	ctx := context.Background()
	if args[0] == "snapshot" {
		a, err := c.Get(ctx, "memory/summary.txt")
		if err != nil {
			return err
		}
		r := protocol.PublishRequest{Key: a.Key, ExpectedVersion: a.Version, OperationID: "worker-a-update", Content: "stale result from worker A"}
		data, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if err := os.WriteFile(args[1], data, 0600); err != nil {
			return err
		}
		fmt.Println("READY")
		_, err = io.Copy(io.Discard, os.Stdin)
		return err
	}
	if args[0] != "resume" {
		return errors.New("unknown internal demo action")
	}
	data, err := os.ReadFile(args[1])
	if err != nil {
		return err
	}
	var r protocol.PublishRequest
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	_, err = c.Publish(ctx, r)
	var apiErr *client.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "version_conflict" || apiErr.Status != 409 {
		return fmt.Errorf("expected version_conflict, got %v", err)
	}
	return nil
}

func startDemoServer(exe, db string) (*exec.Cmd, string, error) {
	cmd := exec.Command(exe, "serve", "--db", db, "--listen", "127.0.0.1:0")
	cmd.Env = demoEnvironment()
	pipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, "", err
	}
	if err := cmd.Start(); err != nil {
		return nil, "", err
	}
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(pipe)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "Questlock listening on ") {
				ready <- strings.TrimPrefix(scanner.Text(), "Questlock listening on ")
				return
			}
		}
		ready <- ""
	}()
	var base string
	select {
	case base = <-ready:
	case <-time.After(15 * time.Second):
		stopDemoProcess(cmd)
		return nil, "", errors.New("broker startup timed out")
	}
	if base == "" {
		stopDemoProcess(cmd)
		return nil, "", errors.New("broker failed to start")
	}
	httpClient := &http.Client{Timeout: time.Second}
	for i := 0; i < 40; i++ {
		resp, err := httpClient.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return cmd, base, nil
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	stopDemoProcess(cmd)
	return nil, "", errors.New("broker never became healthy")
}

func stopDemoProcess(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil && cmd.ProcessState == nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}

// demoEnvironment deliberately excludes the parent shell's cloud/API credentials.
// Only the Go test child selector and race-runtime options accompany fixtures.
func demoEnvironment(extra ...string) []string {
	env := []string{"PATH=/usr/bin:/bin", "LANG=C"}
	for _, key := range []string{"QUESTLOCK_TEST_PROCESS", "GORACE"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return append(env, extra...)
}
