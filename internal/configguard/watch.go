package configguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type acknowledgment struct {
	SHA256 string `json:"candidate_sha256"`
	PID    int    `json:"watcher_pid"`
}

func readTransaction(path string) (transaction, error) {
	var tx transaction
	if filepath.Base(path) != "transaction.json" {
		return tx, errors.New("invalid transaction manifest name")
	}
	if err := readPrivateJSON(path, &tx); err != nil {
		return tx, err
	}
	if tx.Schema != 1 || tx.Operation != "apply" && tx.Operation != "restore" || !validSnapshot(tx.Before) || !validSnapshot(tx.After) || tx.Deadline.IsZero() || tx.Operation == "apply" && !tx.After.Exists {
		return tx, errors.New("invalid transaction manifest")
	}
	if err := safePath(tx.Target, true); err != nil {
		return tx, err
	}
	return tx, nil
}

func savedBytes(dir, name string, want Snapshot) ([]byte, error) {
	if name != "backup" && name != "candidate" {
		return nil, errors.New("invalid recovery file")
	}
	if err := privateDirectory(dir, false); err != nil {
		return nil, err
	}
	b, mode, err := readRegular(filepath.Join(dir, name))
	if err != nil || mode.Perm() != 0600 {
		return nil, errors.New("private complete recovery file unavailable")
	}
	if want.Exists && digest(b) != want.SHA256 || !want.Exists && len(b) != 0 {
		return nil, errors.New("recovery file changed")
	}
	return b, nil
}

// Watch is the hidden executable entry point. It requires the parent keepalive
// pipe on fd 3 and the already-held, target-specific lock on fd 4. It never
// accepts a standalone manifest as sufficient authority to change a file.
func Watch(manifest string) error {
	return watch(manifest, os.NewFile(3, "configguard-parent"), os.NewFile(4, "configguard-lock"))
}

func watch(manifest string, keepalive, lock *os.File) error {
	tx, err := readTransaction(manifest)
	if err != nil {
		return err
	}
	if keepalive == nil || lock == nil {
		return errors.New("watchdog pipe and lock authority required")
	}
	defer keepalive.Close()
	defer lock.Close()
	if err = checkAuthority(keepalive, lock, tx.Target); err != nil {
		return err
	}
	dir := filepath.Dir(manifest)
	ended := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, keepalive); close(ended) }()
	deadline := time.Now().Add(time.Until(tx.Deadline))
	stopReason := func() string {
		select {
		case <-ended:
			return "parent_disconnected"
		default:
		}
		if _, err := os.Lstat(filepath.Join(dir, "abort")); err == nil || !os.IsNotExist(err) {
			return "abort_requested"
		}
		if !time.Now().Before(deadline) {
			return "deadline_expired"
		}
		return ""
	}
	if _, err = savedBytes(dir, "backup", tx.Before); err != nil {
		return finish(dir, tx, "not_installed", "backup_changed")
	}
	if _, err = savedBytes(dir, "candidate", tx.After); err != nil {
		return finish(dir, tx, "not_installed", "candidate_changed")
	}
	if reason := stopReason(); reason != "" {
		return finish(dir, tx, "not_installed", reason)
	}
	if err = writeJSON(filepath.Join(dir, "ready.json"), acknowledgment{SHA256: tx.After.SHA256, PID: os.Getpid()}); err != nil {
		return err
	}
	installed := false
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if reason := stopReason(); reason != "" {
			return rollback(dir, tx, installed, reason)
		}
		begin, beginErr := hasMarker(filepath.Join(dir, "begin"))
		if beginErr != nil {
			return rollback(dir, tx, installed, "invalid_begin_marker")
		}
		if !installed && begin {
			candidate, err := savedBytes(dir, "candidate", tx.After)
			if err != nil {
				return finish(dir, tx, "not_installed", "candidate_changed_before_swap")
			}
			reason := "candidate_swap_failed"
			swapped, swapErr := replaceTarget(tx.Target, candidate, tx.After, func() error {
				if stopped := stopReason(); stopped != "" {
					reason = stopped
					return errors.New(reason)
				}
				current, checkErr := Inspect(tx.Target)
				if checkErr != nil || current != tx.Before {
					reason = "expected_original_changed_before_swap"
					return errors.New(reason)
				}
				if stopped := stopReason(); stopped != "" {
					reason = stopped
					return errors.New(reason)
				}
				return nil
			})
			if swapErr != nil {
				return rollback(dir, tx, swapped, reason)
			}
			installed = true
			if err = writeJSON(filepath.Join(dir, "installed.json"), acknowledgment{SHA256: tx.After.SHA256, PID: os.Getpid()}); err != nil {
				return rollback(dir, tx, true, "installed_acknowledgment_failed")
			}
		}
		if installed {
			current, checkErr := Inspect(tx.Target)
			if checkErr != nil || current != tx.After {
				return finish(dir, tx, "preserved_concurrent_change", "live_candidate_changed")
			}
		}
		commit, commitErr := hasMarker(filepath.Join(dir, "commit"))
		if commitErr != nil {
			return rollback(dir, tx, installed, "invalid_commit_marker")
		}
		if commit {
			if !installed {
				return finish(dir, tx, "not_installed", "premature_commit")
			}
			var proof acknowledgment
			if err = readPrivateJSON(filepath.Join(dir, "verification.json"), &proof); err != nil || proof.SHA256 != tx.After.SHA256 || proof.PID != os.Getpid() {
				return rollback(dir, tx, true, "live_verification_proof_missing")
			}
			if reason := stopReason(); reason != "" {
				return rollback(dir, tx, true, reason)
			}
			current, checkErr := Inspect(tx.Target)
			if checkErr != nil || current != tx.After {
				return finish(dir, tx, "preserved_concurrent_change", "commit_candidate_changed")
			}
			if err = finish(dir, tx, successState(tx.Operation), "live_client_verified"); err != nil {
				return rollback(dir, tx, true, "commit_acknowledgment_failed")
			}
			return nil
		}
		select {
		case <-ended:
		case <-ticker.C:
		}
	}
}

func rollback(dir string, tx transaction, installed bool, reason string) error {
	if !installed {
		return finish(dir, tx, "not_installed", reason)
	}
	current, err := Inspect(tx.Target)
	if err != nil || current != tx.After {
		return finish(dir, tx, "preserved_concurrent_change", reason)
	}
	original, err := savedBytes(dir, "backup", tx.Before)
	if err != nil {
		return finish(dir, tx, "rollback_requires_attention", "backup_changed")
	}
	changed := false
	_, err = replaceTarget(tx.Target, original, tx.Before, func() error {
		current, checkErr := Inspect(tx.Target)
		if checkErr != nil || current != tx.After {
			changed = true
			return errors.New("candidate changed before restore")
		}
		return nil
	})
	if changed {
		return finish(dir, tx, "preserved_concurrent_change", reason)
	}
	if err != nil {
		return finish(dir, tx, "rollback_requires_attention", "restore_failed")
	}
	return finish(dir, tx, "rolled_back", reason)
}

func finish(dir string, tx transaction, state, reason string) error {
	current, _ := Inspect(tx.Target)
	return writeJSON(filepath.Join(dir, "status.json"), Status{State: state, Reason: reason, Current: current, WatcherPID: os.Getpid(), At: time.Now().UTC()})
}

func hasMarker(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != 0 {
		return false, errors.New("invalid transaction marker")
	}
	return true, nil
}

func marker(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = file.Sync()
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return syncDir(filepath.Dir(path))
}

func waitMarker(ctx context.Context, path string, done <-chan struct{}) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return errors.New("watchdog exited before acknowledgment")
		default:
		}
		present, err := hasRegularAcknowledgment(path)
		if err != nil {
			return err
		}
		if present {
			return nil
		}
		select {
		case <-done:
			return errors.New("watchdog exited before acknowledgment")
		case <-ctx.Done():
			return fmt.Errorf("watchdog acknowledgment: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func hasRegularAcknowledgment(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return false, errors.New("invalid watchdog acknowledgment")
	}
	return true, nil
}
