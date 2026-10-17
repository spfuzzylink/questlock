package onboardingclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"time"
)

type nativeRunner struct{}

func (nativeRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command.Executable, command.Args...)
	cmd.Env, cmd.Dir = command.Env, command.Dir
	var output boundedBuffer
	cmd.Stdout = &output
	// Client output can include secrets from existing configuration. Discard
	// stderr entirely and never interpolate either stream into returned errors.
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, errors.New("native configuration command failed")
	}
	return output.Bytes(), nil
}

func (nativeRunner) CodexRead(ctx context.Context, command Command) (snapshot CodexSnapshot, err error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command.Executable, command.Args...)
	cmd.Env, cmd.Dir, cmd.Stderr = command.Env, command.Dir, io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return snapshot, errors.New("cannot open native config reader")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return snapshot, errors.New("cannot open native config reader")
	}
	if err = cmd.Start(); err != nil {
		return snapshot, errors.New("cannot start native config reader")
	}
	defer func() { _ = stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), maxConfigBytes*4)
	send := func(value any) error { return json.NewEncoder(stdin).Encode(value) }
	read := func(id int) (json.RawMessage, error) {
		for scanner.Scan() {
			var response struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(scanner.Bytes(), &response) != nil {
				return nil, errors.New("invalid native config response")
			}
			if response.ID != id {
				continue
			}
			if len(response.Error) > 0 {
				return nil, errors.New("native config RPC was rejected")
			}
			if len(response.Result) == 0 {
				return nil, errors.New("native config reader returned no result")
			}
			return response.Result, nil
		}
		return nil, errors.New("native config reader ended before validation")
	}
	if err = send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "questlock_config_validation", "version": "0.7.0"}}}); err != nil {
		return snapshot, errors.New("cannot initialize native config reader")
	}
	if _, err = read(1); err != nil {
		return snapshot, err
	}
	if err = send(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return snapshot, errors.New("cannot initialize native config reader")
	}
	if err = send(map[string]any{"id": 2, "method": "config/read", "params": map[string]any{"includeLayers": true}}); err != nil {
		return snapshot, errors.New("cannot request native config read")
	}
	result, err := read(2)
	if err != nil {
		return snapshot, err
	}
	decoder := json.NewDecoder(bytes.NewReader(result))
	decoder.UseNumber()
	if decoder.Decode(&snapshot) != nil {
		return snapshot, errors.New("invalid native config snapshot")
	}
	return snapshot, nil
}

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if b.Len()+len(data) > 1<<20 {
		return 0, errors.New("native configuration output exceeds limit")
	}
	return b.Buffer.Write(data)
}
