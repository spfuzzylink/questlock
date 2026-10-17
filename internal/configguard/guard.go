// Package configguard installs complete configuration files through a detached
// rollback watchdog. The application supplies client-native, read-only
// validation; this package owns the file transaction and recovery journal.
package configguard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const maxConfig = 8 << 20

// Snapshot is a complete optimistic concurrency check, including absence and
// permission changes. Obtain it with Inspect before constructing a candidate.
type Snapshot struct {
	Exists bool   `json:"exists"`
	SHA256 string `json:"sha256,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`
}

// Options describes a complete candidate. Validate must read the complete file
// using the actual client in an isolated home. It is called for the saved
// original (when present), then the candidate. Verify must read the installed
// configuration through the live client and check every changed setting.
// Callbacks must be read-only and honor their context. They must not send model
// requests. Executable must dispatch WatchArgs followed by the manifest path to
// Watch; WatchArgs defaults to ["__config-watch"].
type Options struct {
	Target      string
	Candidate   []byte
	Expected    Snapshot
	RecoveryDir string
	Executable  string
	WatchArgs   []string
	Timeout     time.Duration
	Validate    func(context.Context, string) error
	Verify      func(context.Context, string) error
}

// Status is the durable outcome written by the sole configuration writer.
// A preserved_concurrent_change outcome always requires inspection by a human.
type Status struct {
	State      string    `json:"state"`
	Reason     string    `json:"reason,omitempty"`
	Current    Snapshot  `json:"current"`
	WatcherPID int       `json:"watcher_pid"`
	At         time.Time `json:"at"`
}

type Result struct {
	Directory string `json:"directory"`
	Status    Status `json:"status"`
}

// RestoreOptions identifies a prior committed Apply. Restore refuses to
// overwrite any change made since that candidate was installed.
type RestoreOptions struct {
	Directory  string
	Executable string
	WatchArgs  []string
	Timeout    time.Duration
	Validate   func(context.Context, string) error
	Verify     func(context.Context, string) error
}

type transaction struct {
	Schema    int       `json:"schema"`
	Operation string    `json:"operation"`
	Target    string    `json:"target"`
	Before    Snapshot  `json:"before"`
	After     Snapshot  `json:"after"`
	Deadline  time.Time `json:"deadline"`
}

func digest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func validSnapshot(s Snapshot) bool {
	if !s.Exists {
		return s.SHA256 == "" && s.Mode == 0
	}
	b, err := hex.DecodeString(s.SHA256)
	return err == nil && len(b) == 32 && s.SHA256 == strings.ToLower(s.SHA256) && s.Mode&^0777 == 0
}

// Inspect refuses symbolic links, traversal components, special files, and
// files larger than 8 MiB. Missing target files produce a zero Snapshot.
func Inspect(path string) (Snapshot, error) {
	_, s, err := inspect(path)
	return s, err
}

func inspect(path string) ([]byte, Snapshot, error) {
	if err := safePath(path, true); err != nil {
		return nil, Snapshot{}, err
	}
	b, mode, err := readRegular(path)
	if os.IsNotExist(err) {
		return nil, Snapshot{}, nil
	}
	if err != nil {
		return nil, Snapshot{}, err
	}
	return b, Snapshot{Exists: true, SHA256: digest(b), Mode: uint32(mode.Perm())}, nil
}

// Apply validates complete files before starting the watchdog. Only the
// detached watchdog can install or roll back; a stalled or dead caller cannot
// install after the watchdog exits.
func Apply(ctx context.Context, o Options) (Result, error) {
	if o.Validate == nil || o.Verify == nil {
		return Result{}, errors.New("isolated and live client verification are required")
	}
	if !validSnapshot(o.Expected) || len(o.Candidate) > maxConfig {
		return Result{}, errors.New("invalid expected snapshot or candidate size")
	}
	original, before, err := inspect(o.Target)
	if err != nil {
		return Result{}, err
	}
	if before != o.Expected {
		return Result{}, errors.New("configuration changed before validation")
	}
	mode := uint32(0600)
	if before.Exists {
		mode = before.Mode
	}
	candidate := append([]byte(nil), o.Candidate...)
	after := Snapshot{Exists: true, SHA256: digest(candidate), Mode: mode}
	return execute(ctx, o, "apply", before, after, original, candidate)
}

// Restore starts another detached transaction using the complete saved
// original, including its permissions or original absence. Divergent edits
// are preserved. Recovery files remain available after restoration.
func Restore(ctx context.Context, o RestoreOptions) (Result, error) {
	if o.Validate == nil || o.Verify == nil {
		return Result{}, errors.New("isolated and live client verification are required for restore")
	}
	tx, err := readTransaction(filepath.Join(o.Directory, "transaction.json"))
	if err != nil {
		return Result{}, err
	}
	status, err := ReadStatus(o.Directory)
	if err != nil || tx.Operation != "apply" || status.State != "committed" {
		return Result{}, errors.New("restore requires a committed configuration transaction")
	}
	original, err := savedBytes(o.Directory, "backup", tx.Before)
	if err != nil {
		return Result{}, err
	}
	current, snapshot, err := inspect(tx.Target)
	if err != nil || snapshot != tx.After {
		return Result{}, errors.New("configuration changed since installation; restore refused")
	}
	apply := Options{Target: tx.Target, Expected: tx.After, RecoveryDir: filepath.Dir(o.Directory), Executable: o.Executable, WatchArgs: o.WatchArgs, Timeout: o.Timeout, Validate: o.Validate, Verify: o.Verify}
	return execute(ctx, apply, "restore", tx.After, tx.Before, current, original)
}

func execute(ctx context.Context, o Options, operation string, before, after Snapshot, original, candidate []byte) (result Result, err error) {
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if o.Timeout <= 0 {
		o.Timeout = 45 * time.Second
	}
	if o.Timeout > 10*time.Minute {
		return result, errors.New("watchdog timeout cannot exceed ten minutes")
	}
	if o.Executable == "" {
		o.Executable, err = os.Executable()
		if err != nil {
			return result, err
		}
	}
	if len(o.WatchArgs) == 0 {
		o.WatchArgs = []string{"__config-watch"}
	}
	root := o.RecoveryDir
	if root == "" {
		root = filepath.Join(filepath.Dir(o.Target), ".questlock-recovery")
	}
	if err = privateDirectory(root, true); err != nil {
		return result, err
	}
	lock, err := acquireLock(o.Target)
	if err != nil {
		return result, err
	}
	defer lock.Close()
	if current, checkErr := Inspect(o.Target); checkErr != nil || current != before {
		return result, errors.New("configuration changed before transaction")
	}
	dir, err := os.MkdirTemp(root, "change-")
	if err != nil {
		return result, err
	}
	result.Directory = dir
	started := false
	defer func() {
		if err != nil && !started {
			current, _ := Inspect(o.Target)
			status := Status{State: "not_installed", Reason: "preparation_or_validation_failed", Current: current, At: time.Now().UTC()}
			if writeErr := writeJSON(filepath.Join(dir, "status.json"), status); writeErr == nil {
				result.Status = status
			} else {
				err = errors.Join(err, writeErr)
			}
		}
	}()
	if err = syncDir(root); err != nil {
		return result, err
	}
	if err = writeAtomic(filepath.Join(dir, "backup"), original, 0600, nil); err != nil {
		return result, err
	}
	if err = writeAtomic(filepath.Join(dir, "candidate"), candidate, 0600, nil); err != nil {
		return result, err
	}
	{
		validationCtx, cancel := context.WithTimeout(ctx, o.Timeout)
		defer cancel()
		if before.Exists {
			if err = callVerifier(validationCtx, o.Validate, filepath.Join(dir, "backup")); err != nil {
				return result, fmt.Errorf("saved original failed isolated validation: %w", err)
			}
		}
		if after.Exists {
			if err = callVerifier(validationCtx, o.Validate, filepath.Join(dir, "candidate")); err != nil {
				return result, fmt.Errorf("candidate failed isolated validation: %w", err)
			}
		}
	}
	if current, checkErr := Inspect(o.Target); checkErr != nil || current != before {
		return result, errors.New("configuration changed during validation")
	}
	if _, err = savedBytes(dir, "backup", before); err != nil {
		return result, err
	}
	if _, err = savedBytes(dir, "candidate", after); err != nil {
		return result, err
	}
	tx := transaction{Schema: 1, Operation: operation, Target: o.Target, Before: before, After: after, Deadline: time.Now().Add(o.Timeout).UTC()}
	manifest := filepath.Join(dir, "transaction.json")
	if err = writeJSON(manifest, tx); err != nil {
		return result, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return result, err
	}
	defer reader.Close()
	defer writer.Close()
	log, err := os.OpenFile(filepath.Join(dir, "watchdog.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, err
	}
	defer log.Close()
	args := append(append([]string(nil), o.WatchArgs...), manifest)
	cmd := exec.Command(o.Executable, args...)
	cmd.ExtraFiles = []*os.File{reader, lock}
	cmd.Stdout, cmd.Stderr = log, log
	detach(cmd)
	if err = cmd.Start(); err != nil {
		return result, fmt.Errorf("watchdog could not start; configuration untouched: %w", err)
	}
	started = true
	reader.Close()
	lock.Close()
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	finished := false
	defer func() {
		if !finished {
			// Closing the pipe requests rollback even if writing abort fails.
			_ = marker(filepath.Join(dir, "abort"))
			writer.Close()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				err = errors.Join(err, errors.New("watchdog has not acknowledged rollback; inspect recovery status"))
			}
		}
		if status, readErr := ReadStatus(dir); readErr == nil {
			result.Status = status
		}
	}()
	watchCtx, cancel := context.WithDeadline(ctx, tx.Deadline)
	defer cancel()
	if err = waitMarker(watchCtx, filepath.Join(dir, "ready.json"), done); err != nil {
		return result, err
	}
	var ready acknowledgment
	if err = readPrivateJSON(filepath.Join(dir, "ready.json"), &ready); err != nil || ready.SHA256 != after.SHA256 || ready.PID != cmd.Process.Pid {
		return result, errors.New("watchdog readiness acknowledgment invalid")
	}
	if err = watchCtx.Err(); err != nil {
		return result, err
	}
	if err = marker(filepath.Join(dir, "begin")); err != nil {
		return result, err
	}
	if err = waitMarker(watchCtx, filepath.Join(dir, "installed.json"), done); err != nil {
		return result, err
	}
	if current, checkErr := Inspect(o.Target); checkErr != nil || current != after {
		return result, errors.New("installed configuration changed before verification")
	}
	if err = callVerifier(watchCtx, o.Verify, o.Target); err != nil {
		return result, fmt.Errorf("live client verification failed; rollback requested: %w", err)
	}
	select {
	case <-done:
		return result, errors.New("watchdog exited during live verification")
	default:
	}
	if err = watchCtx.Err(); err != nil {
		return result, err
	}
	if current, checkErr := Inspect(o.Target); checkErr != nil || current != after {
		return result, errors.New("configuration changed during live verification")
	}
	if err = writeJSON(filepath.Join(dir, "verification.json"), acknowledgment{SHA256: after.SHA256, PID: cmd.Process.Pid}); err != nil {
		return result, err
	}
	if err = marker(filepath.Join(dir, "commit")); err != nil {
		return result, err
	}
	select {
	case <-done:
	case <-watchCtx.Done():
		return result, errors.New("watchdog commit unconfirmed")
	}
	status, err := ReadStatus(dir)
	if err != nil || status.State != successState(operation) {
		return result, errors.New("watchdog did not commit configuration; inspect recovery status")
	}
	finished = true
	result.Status = status
	return result, nil
}

func callVerifier(ctx context.Context, fn func(context.Context, string) error, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- fn(ctx, path) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func successState(operation string) string {
	if operation == "restore" {
		return "restored"
	}
	return "committed"
}

// ReadStatus reads a private transaction's durable terminal status.
func ReadStatus(directory string) (Status, error) {
	var status Status
	err := readPrivateJSON(filepath.Join(directory, "status.json"), &status)
	return status, err
}

func writeJSON(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeAtomic(path, append(b, '\n'), 0600, nil)
}

func readPrivateJSON(path string, value any) error {
	if err := privateDirectory(filepath.Dir(path), false); err != nil {
		return err
	}
	b, mode, err := readRegular(path)
	if err != nil {
		return err
	}
	if mode.Perm() != 0600 {
		return errors.New("transaction file must have mode 0600")
	}
	return json.Unmarshal(b, value)
}
