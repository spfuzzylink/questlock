//go:build darwin || linux

package configguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "__config-watch" {
		if err := Watch(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	if len(os.Args) == 3 && os.Args[1] == "__test-parent" {
		var o Options
		data, err := os.ReadFile(os.Args[2])
		if err != nil || json.Unmarshal(data, &o) != nil {
			os.Exit(2)
		}
		o.Validate = readable
		o.Verify = func(ctx context.Context, path string) error {
			if err := os.WriteFile(filepath.Join(o.RecoveryDir, "parent-verifying"), nil, 0600); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		}
		_, _ = Apply(context.Background(), o)
		os.Exit(3)
	}
	os.Exit(m.Run())
}

func readable(_ context.Context, path string) error { _, err := os.ReadFile(path); return err }
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func privateTemp(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	must(t, os.Chmod(path, 0700))
	return path
}
func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	return b
}
func fixture(t *testing.T, exists bool) (Options, []byte) {
	t.Helper()
	dir := privateTemp(t)
	target := filepath.Join(dir, "config.toml")
	original := []byte("notify = [\"helper path\", \"$literal\", \"a\\\"b\"]\nsetting = \"before\"\n")
	if exists {
		must(t, os.WriteFile(target, original, 0640))
	}
	expected, err := Inspect(target)
	must(t, err)
	return Options{Target: target, Candidate: bytes.ReplaceAll(original, []byte("before"), []byte("after")), Expected: expected, RecoveryDir: filepath.Join(dir, "recovery"), Executable: os.Args[0], Timeout: 5 * time.Second, Validate: readable, Verify: readable}, original
}

func TestApplyPreservesCompleteFilesModeAndPrivateRecovery(t *testing.T) {
	o, original := fixture(t, true)
	var validated [][]byte
	o.Validate = func(_ context.Context, path string) error { validated = append(validated, read(t, path)); return nil }
	result, err := Apply(context.Background(), o)
	must(t, err)
	if result.Status.State != "committed" || !bytes.Equal(read(t, o.Target), o.Candidate) {
		t.Fatalf("candidate not committed: %+v", result)
	}
	if len(validated) != 2 || !bytes.Equal(validated[0], original) || !bytes.Equal(validated[1], o.Candidate) {
		t.Fatal("both complete files were not validated")
	}
	if !bytes.Equal(read(t, filepath.Join(result.Directory, "backup")), original) {
		t.Fatal("complete backup, including notify command array, changed")
	}
	current, err := Inspect(o.Target)
	must(t, err)
	if current.Mode != 0640 {
		t.Fatalf("mode changed: %o", current.Mode)
	}
	for _, dir := range []string{o.RecoveryDir, result.Directory} {
		info, err := os.Stat(dir)
		must(t, err)
		if info.Mode().Perm() != 0700 {
			t.Fatalf("nonprivate directory: %s", dir)
		}
	}
	entries, err := os.ReadDir(result.Directory)
	must(t, err)
	for _, entry := range entries {
		info, err := entry.Info()
		must(t, err)
		if info.Mode().Perm() != 0600 {
			t.Fatalf("nonprivate journal file: %s", entry.Name())
		}
	}
}

func TestRejectedBaselineAndCandidateNeverInstall(t *testing.T) {
	for _, rejected := range []string{"backup", "candidate"} {
		t.Run(rejected, func(t *testing.T) {
			o, original := fixture(t, true)
			o.Validate = func(_ context.Context, path string) error {
				if filepath.Base(path) == rejected {
					return errors.New("client rejected configuration")
				}
				return nil
			}
			result, err := Apply(context.Background(), o)
			if err == nil {
				t.Fatal("rejected validation committed")
			}
			if !bytes.Equal(read(t, o.Target), original) {
				t.Fatal("validation failure changed target")
			}
			if _, err := os.Stat(filepath.Join(result.Directory, "ready.json")); !os.IsNotExist(err) {
				t.Fatal("watchdog armed before validation")
			}
		})
	}
}

func TestLiveFailureRollsBackCompleteOriginalAndMode(t *testing.T) {
	o, original := fixture(t, true)
	o.Verify = func(context.Context, string) error { return errors.New("native setting mismatch") }
	result, err := Apply(context.Background(), o)
	if err == nil || result.Status.State != "rolled_back" {
		t.Fatalf("live failure: %+v %v", result, err)
	}
	if !bytes.Equal(read(t, o.Target), original) {
		t.Fatal("incomplete original restore")
	}
	snapshot, err := Inspect(o.Target)
	must(t, err)
	if snapshot != o.Expected {
		t.Fatal("original mode or content changed")
	}
}

func TestTimeoutRollsBackWhileVerifierIsStalled(t *testing.T) {
	o, original := fixture(t, true)
	o.Timeout = 400 * time.Millisecond
	blocked := make(chan struct{})
	defer close(blocked)
	o.Verify = func(context.Context, string) error { <-blocked; return nil }
	result, err := Apply(context.Background(), o)
	if err == nil || result.Status.State != "rolled_back" {
		t.Fatalf("stalled verifier: %+v %v", result, err)
	}
	if !bytes.Equal(read(t, o.Target), original) {
		t.Fatal("timeout did not restore original")
	}
	// A late successful verifier has no authority to write the configuration.
}

func TestHumanRepairDuringLiveFailureIsPreserved(t *testing.T) {
	o, _ := fixture(t, true)
	repair := []byte("notify = [\"human repair\"]\n")
	o.Verify = func(_ context.Context, path string) error {
		if err := os.WriteFile(path, repair, 0600); err != nil {
			return err
		}
		return errors.New("verification failed")
	}
	result, err := Apply(context.Background(), o)
	if err == nil || result.Status.State != "preserved_concurrent_change" {
		t.Fatalf("repair: %+v %v", result, err)
	}
	if !bytes.Equal(read(t, o.Target), repair) {
		t.Fatal("human repair was overwritten")
	}
}

func TestNewFileRollbackAndExplicitRestorePreserveAbsence(t *testing.T) {
	t.Run("automatic rollback", func(t *testing.T) {
		o, _ := fixture(t, false)
		o.Verify = func(context.Context, string) error { return errors.New("rejected") }
		result, err := Apply(context.Background(), o)
		if err == nil || result.Status.State != "rolled_back" {
			t.Fatalf("new file: %+v %v", result, err)
		}
		if _, err := os.Lstat(o.Target); !os.IsNotExist(err) {
			t.Fatal("new file was not removed")
		}
	})
	t.Run("explicit restore", func(t *testing.T) {
		o, _ := fixture(t, false)
		result, err := Apply(context.Background(), o)
		must(t, err)
		restored, err := Restore(context.Background(), RestoreOptions{Directory: result.Directory, Executable: os.Args[0], Validate: readable, Verify: func(_ context.Context, path string) error {
			_, err := os.Lstat(path)
			if os.IsNotExist(err) {
				return nil
			}
			return errors.New("expected original absence")
		}})
		must(t, err)
		if restored.Status.State != "restored" {
			t.Fatal(restored)
		}
		if _, err := os.Lstat(o.Target); !os.IsNotExist(err) {
			t.Fatal("explicit restore did not remove newly installed file")
		}
	})
}

func TestExplicitRestoreValidatesAndRefusesDivergentEdits(t *testing.T) {
	o, original := fixture(t, true)
	installed, err := Apply(context.Background(), o)
	must(t, err)
	options := RestoreOptions{Directory: installed.Directory, Executable: os.Args[0], Validate: readable, Verify: readable}
	bad := options
	bad.Validate = func(context.Context, string) error { return errors.New("client refused saved config") }
	if _, err := Restore(context.Background(), bad); err == nil {
		t.Fatal("restore skipped validation")
	}
	if !bytes.Equal(read(t, o.Target), o.Candidate) {
		t.Fatal("failed restore changed live config")
	}
	restored, err := Restore(context.Background(), options)
	must(t, err)
	if restored.Status.State != "restored" || !bytes.Equal(read(t, o.Target), original) {
		t.Fatal("saved original not restored")
	}
	if _, err := Restore(context.Background(), options); err == nil {
		t.Fatal("restore overwrote divergent current state")
	}
}

func TestRestoreLiveFailureRestoresInstalledCandidate(t *testing.T) {
	o, _ := fixture(t, true)
	installed, err := Apply(context.Background(), o)
	must(t, err)
	result, err := Restore(context.Background(), RestoreOptions{Directory: installed.Directory, Executable: os.Args[0], Validate: readable, Verify: func(context.Context, string) error { return errors.New("native restore rejected") }})
	if err == nil || result.Status.State != "rolled_back" {
		t.Fatalf("unverified restore outcome: %+v %v", result, err)
	}
	if !bytes.Equal(read(t, o.Target), o.Candidate) {
		t.Fatal("failed restore did not restore the installed candidate")
	}
}

func TestExpectedSnapshotDetectsContentModeAndUnexpectedFile(t *testing.T) {
	for _, change := range []string{"content", "mode", "unexpected file"} {
		t.Run(change, func(t *testing.T) {
			o, _ := fixture(t, change != "unexpected file")
			if change == "mode" {
				must(t, os.Chmod(o.Target, 0600))
			} else {
				must(t, os.WriteFile(o.Target, []byte("human change"), 0600))
			}
			if _, err := Apply(context.Background(), o); err == nil {
				t.Fatal("stale baseline accepted")
			}
		})
	}
}

func TestConcurrentChangeDuringPrevalidationIsPreserved(t *testing.T) {
	o, _ := fixture(t, true)
	repair := []byte("human changed during isolated validation")
	o.Validate = func(_ context.Context, path string) error {
		if filepath.Base(path) == "candidate" {
			return os.WriteFile(o.Target, repair, 0600)
		}
		return nil
	}
	if _, err := Apply(context.Background(), o); err == nil {
		t.Fatal("prevalidation race accepted")
	}
	if !bytes.Equal(read(t, o.Target), repair) {
		t.Fatal("prevalidation repair overwritten")
	}
}

func TestPathsRefuseSymlinksTraversalAndUnsafeRecovery(t *testing.T) {
	o, original := fixture(t, true)
	link := filepath.Join(filepath.Dir(o.Target), "alias")
	must(t, os.Symlink(o.Target, link))
	if _, err := Inspect(link); err == nil {
		t.Fatal("symlink target accepted")
	}
	linkDir := filepath.Join(filepath.Dir(o.Target), "alias-dir")
	must(t, os.Symlink(filepath.Dir(o.Target), linkDir))
	if _, err := Inspect(filepath.Join(linkDir, "config.toml")); err == nil {
		t.Fatal("symlink parent accepted")
	}
	if _, err := Inspect(filepath.Dir(o.Target) + "/../" + filepath.Base(filepath.Dir(o.Target)) + "/config.toml"); err == nil {
		t.Fatal("traversal accepted")
	}
	must(t, os.Mkdir(o.RecoveryDir, 0755))
	if _, err := Apply(context.Background(), o); err == nil {
		t.Fatal("public recovery directory accepted")
	}
	if !bytes.Equal(read(t, o.Target), original) {
		t.Fatal("path rejection changed target")
	}
}

func TestMandatoryVerificationCallbacks(t *testing.T) {
	o, _ := fixture(t, true)
	o.Verify = nil
	if _, err := Apply(context.Background(), o); err == nil {
		t.Fatal("unverified apply allowed")
	}
	if _, err := Restore(context.Background(), RestoreOptions{}); err == nil {
		t.Fatal("unverified restore allowed")
	}
}

func waitPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("marker %s did not appear", filepath.Base(path))
}

func TestActualParentDeathRollsBackIndependently(t *testing.T) {
	o, original := fixture(t, true)
	o.Timeout = 10 * time.Second
	must(t, os.Mkdir(o.RecoveryDir, 0700))
	optionsPath := filepath.Join(filepath.Dir(o.Target), "parent-options.json")
	data, err := json.Marshal(struct {
		Target, RecoveryDir, Executable string
		Candidate                       []byte
		Expected                        Snapshot
		Timeout                         time.Duration
	}{o.Target, o.RecoveryDir, o.Executable, o.Candidate, o.Expected, o.Timeout})
	must(t, err)
	must(t, os.WriteFile(optionsPath, data, 0600))
	parent := exec.Command(os.Args[0], "__test-parent", optionsPath)
	var output bytes.Buffer
	parent.Stdout, parent.Stderr = &output, &output
	must(t, parent.Start())
	t.Cleanup(func() { _ = parent.Process.Kill() })
	waitPath(t, filepath.Join(o.RecoveryDir, "parent-verifying"))
	if !bytes.Equal(read(t, o.Target), o.Candidate) {
		t.Fatal("test parent did not install")
	}
	must(t, parent.Process.Kill())
	_ = parent.Wait()
	directories, err := filepath.Glob(filepath.Join(o.RecoveryDir, "change-*"))
	must(t, err)
	if len(directories) != 1 {
		t.Fatalf("missing journal: %s", output.String())
	}
	waitPath(t, filepath.Join(directories[0], "status.json"))
	status, err := ReadStatus(directories[0])
	must(t, err)
	if status.State != "rolled_back" || status.Reason != "parent_disconnected" {
		t.Fatalf("independent rollback: %+v", status)
	}
	if !bytes.Equal(read(t, o.Target), original) {
		t.Fatal("parent death did not restore complete original")
	}
}

func watchFixture(t *testing.T, timeout time.Duration) (transaction, string) {
	t.Helper()
	o, original := fixture(t, true)
	must(t, os.Mkdir(o.RecoveryDir, 0700))
	dir := filepath.Join(o.RecoveryDir, "change-test")
	must(t, os.Mkdir(dir, 0700))
	tx := transaction{Schema: 1, Operation: "apply", Target: o.Target, Before: o.Expected, After: Snapshot{Exists: true, SHA256: digest(o.Candidate), Mode: o.Expected.Mode}, Deadline: time.Now().Add(timeout)}
	must(t, writeAtomic(filepath.Join(dir, "backup"), original, 0600, nil))
	must(t, writeAtomic(filepath.Join(dir, "candidate"), o.Candidate, 0600, nil))
	manifest := filepath.Join(dir, "transaction.json")
	must(t, writeJSON(manifest, tx))
	return tx, manifest
}

func startWatcher(t *testing.T, manifest string) (*os.File, <-chan struct{}) {
	t.Helper()
	tx, err := readTransaction(manifest)
	must(t, err)
	lock, err := acquireLock(tx.Target)
	must(t, err)
	reader, writer, err := os.Pipe()
	must(t, err)
	cmd := exec.Command(os.Args[0], "__config-watch", manifest)
	cmd.ExtraFiles = []*os.File{reader, lock}
	detach(cmd)
	must(t, cmd.Start())
	reader.Close()
	lock.Close()
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		writer.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	return writer, done
}

func TestReadyDeadlineAndLateBeginCannotInstall(t *testing.T) {
	tx, manifest := watchFixture(t, 400*time.Millisecond)
	_, done := startWatcher(t, manifest)
	dir := filepath.Dir(manifest)
	waitPath(t, filepath.Join(dir, "ready.json"))
	current, err := Inspect(tx.Target)
	must(t, err)
	if current != tx.Before {
		t.Fatal("watchdog installed before begin")
	}
	waitPath(t, filepath.Join(dir, "status.json"))
	<-done
	must(t, marker(filepath.Join(dir, "begin")))
	time.Sleep(30 * time.Millisecond)
	current, err = Inspect(tx.Target)
	must(t, err)
	if current != tx.Before {
		t.Fatal("late begin installed after watchdog exited")
	}
	status, err := ReadStatus(dir)
	must(t, err)
	if status.State != "not_installed" || status.Reason != "deadline_expired" {
		t.Fatal(status)
	}
}

func TestWatcherCASAndLockPreservePreinstallRepair(t *testing.T) {
	tx, manifest := watchFixture(t, 5*time.Second)
	_, done := startWatcher(t, manifest)
	dir := filepath.Dir(manifest)
	waitPath(t, filepath.Join(dir, "ready.json"))
	if lock, err := acquireLock(tx.Target); err == nil {
		lock.Close()
		t.Fatal("detached watcher lost target lock")
	}
	repair := []byte("repaired while parent paused")
	must(t, os.WriteFile(tx.Target, repair, 0600))
	must(t, marker(filepath.Join(dir, "begin")))
	waitPath(t, filepath.Join(dir, "status.json"))
	<-done
	status, err := ReadStatus(dir)
	must(t, err)
	if status.State != "not_installed" || !strings.Contains(status.Reason, "original_changed") {
		t.Fatal(status)
	}
	if !bytes.Equal(read(t, tx.Target), repair) {
		t.Fatal("pre-install repair overwritten")
	}
}

func TestCommitRequiresLiveVerificationProof(t *testing.T) {
	tx, manifest := watchFixture(t, 5*time.Second)
	_, done := startWatcher(t, manifest)
	dir := filepath.Dir(manifest)
	waitPath(t, filepath.Join(dir, "ready.json"))
	must(t, marker(filepath.Join(dir, "begin")))
	waitPath(t, filepath.Join(dir, "installed.json"))
	must(t, marker(filepath.Join(dir, "commit")))
	waitPath(t, filepath.Join(dir, "status.json"))
	<-done
	status, err := ReadStatus(dir)
	must(t, err)
	if status.State != "rolled_back" || status.Reason != "live_verification_proof_missing" {
		t.Fatal(status)
	}
	current, err := Inspect(tx.Target)
	must(t, err)
	if current != tx.Before {
		t.Fatal("unverified commit changed complete original")
	}
}

func TestAlreadyExpiredWatcherCannotArm(t *testing.T) {
	tx, manifest := watchFixture(t, -time.Second)
	_, done := startWatcher(t, manifest)
	dir := filepath.Dir(manifest)
	waitPath(t, filepath.Join(dir, "status.json"))
	<-done
	if _, err := os.Lstat(filepath.Join(dir, "ready.json")); !os.IsNotExist(err) {
		t.Fatal("expired watchdog armed")
	}
	must(t, marker(filepath.Join(dir, "begin")))
	current, err := Inspect(tx.Target)
	must(t, err)
	if current != tx.Before {
		t.Fatal("expired watchdog installed")
	}
}
