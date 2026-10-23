package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/retention"
	"modernc.org/sqlite"
)

// The child exits from a test-only SQLite trigger after production Put has
// fsynced the cold file and directory, but before payload/reference commit.
// No application cleanup, transaction rollback, or deferred Close can run.
func TestStorageCrashChild(t *testing.T) {
	path := os.Getenv("QUESTLOCK_STORAGE_CRASH_DATABASE")
	if path == "" {
		return
	}
	if err := sqlite.RegisterScalarFunction("questlock_test_crash", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		os.Exit(23)
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER crash_before_archive BEFORE UPDATE OF content ON artifact_versions WHEN NEW.content IS NULL BEGIN SELECT questlock_test_crash(); END`); err != nil {
		t.Fatal(err)
	}
	c, err := storagePolicy(t.Context(), s.db)
	if err != nil || c == nil {
		t.Fatal("missing policy", err)
	}
	_, err = s.StorageArchive(t.Context(), false, time.Now().Add(time.Hour), retention.Plan(*c, admission.Snapshot{}, time.Now()))
	t.Fatal("crash point was not reached", err)
}

func TestStorageProcessCrashPreservesHotPayloadAndReceipt(t *testing.T) {
	s, path := newStore(t)
	token, p := newAgent(t, s, "crash-worker", "crash-scope")
	r := protocol.PublishRequest{Key: "crash-note", OperationID: "original", Content: "exact bytes before interrupted archive"}
	accepted := publish(t, s, p, r)
	publish(t, s, p, protocol.PublishRequest{Key: r.Key, OperationID: "next", ExpectedVersion: 1, Content: "current protected head"})
	c := configureRetention(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStorageCrashChild$")
	child.Env = append(os.Environ(), "QUESTLOCK_STORAGE_CRASH_DATABASE="+path)
	output, err := child.CombinedOutput()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 23 {
		t.Fatalf("child did not terminate at durable-write crash point: %v %s", err, output)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err = s.Authenticate(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	var hot string
	if err := s.db.QueryRow("SELECT content FROM artifact_versions WHERE key=? AND version=1", r.Key).Scan(&hot); err != nil || hot != r.Content {
		t.Fatalf("crash lost hot payload: %v", err)
	}
	info, err := s.StorageInfo(t.Context())
	if err != nil || info.ArchivedVersions != 0 || info.ColdFiles != 1 {
		t.Fatalf("expected retained orphan and no committed cold reference: %+v %v", info, err)
	}
	replay, err := s.Publish(t.Context(), p, r)
	if err != nil || !replay.Replayed || replay.Artifact != accepted.Artifact {
		t.Fatalf("crash lost exact receipt: %+v %v", replay, err)
	}
	if _, err := s.db.Exec("DROP TRIGGER crash_before_archive"); err != nil {
		t.Fatal(err)
	}
	if archived := archiveBatch(t, s, c); archived.Items != 1 {
		t.Fatalf("orphan could not be adopted safely: %+v", archived)
	}
	if _, err := s.StorageVerify(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestStorageMaintenanceOptInDeferralAndStop(t *testing.T) {
	s, _ := newStore(t)
	_, p := newAgent(t, s, "maintenance-worker", "maintenance")
	for i, content := range []string{"older", "current"} {
		publish(t, s, p, protocol.PublishRequest{Key: "note", OperationID: content, ExpectedVersion: int64(i), Content: content})
	}
	probe := admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		t.Error("fixed or absent policy called a resource probe")
		return admission.Snapshot{}, nil
	})
	if err := s.storageMaintainOnce(t.Context(), probe); err != nil {
		t.Fatal(err)
	}
	c := configureRetention(t, s)
	c.Adaptive = true
	if err := s.StorageConfigure(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	var events []retention.Event
	s.SetStorageObserver(func(_ context.Context, e retention.Event) { events = append(events, e) })
	unknown := admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		return admission.Snapshot{}, errors.New("unavailable")
	})
	if err := s.storageMaintainOnce(t.Context(), unknown); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Outcome != "deferred" || events[0].Items != 0 {
		t.Fatalf("adaptive pressure did not defer: %+v", events)
	}
	c.Adaptive = false
	if err := s.StorageConfigure(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if err := s.storageMaintainOnce(t.Context(), probe); err != nil {
		t.Fatal(err)
	}
	info, err := s.StorageInfo(t.Context())
	if err != nil || info.ArchivedVersions != 1 {
		t.Fatalf("bounded maintenance did not archive: %+v %v", info, err)
	}
	for _, interval := range []time.Duration{0, time.Millisecond, 25 * time.Hour} {
		if stop, err := s.StartStorageMaintenance(t.Context(), interval, nil); err == nil || stop != nil {
			t.Fatal("invalid interval scheduled maintenance")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	stop, err := s.StartStorageMaintenance(ctx, time.Hour, probe)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	stop()
}

func TestStorageInventoryDoesNotCreateColdFiles(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cold := retention.ColdStore{Root: filepath.Join(parent, "cold"), MaxBytes: 1 << 20}
	if _, err := cold.Inventory(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cold.Root); !os.IsNotExist(err) {
		t.Fatal("inventory created absent root")
	}
	if err := os.Mkdir(cold.Root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := cold.Inventory(t.Context()); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(cold.Root)
	if err != nil || len(files) != 0 {
		t.Fatal("inventory created a lock file", err)
	}
}
