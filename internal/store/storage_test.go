package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/retention"
)

func configureRetention(t *testing.T, s *Store) retention.Config {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := retention.DefaultConfig(filepath.Join(parent, "cold"))
	c.Adaptive = false
	c.HotHistoryDays = 0
	c.KeepRecentVersions = 1
	if err := s.StorageConfigure(t.Context(), c); err != nil {
		t.Fatal("configure retention:", err)
	}
	return c
}
func structSnapshot() admission.Snapshot { return admission.Snapshot{} }
func archiveBatch(t *testing.T, s *Store, c retention.Config) StorageReport {
	t.Helper()
	r, err := s.StorageArchive(t.Context(), false, time.Now().Add(time.Hour), retention.Plan(c, structSnapshot(), time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestStorageArchiveReplayRestoreAndEmptyContent(t *testing.T) {
	s, path := newStore(t)
	_, p := newAgent(t, s, "archiver", "scope")
	requests := []protocol.PublishRequest{{Key: "notes", OperationID: "first", Content: "original text"}, {Key: "notes", OperationID: "second", ExpectedVersion: 1, Content: ""}, {Key: "notes", OperationID: "third", ExpectedVersion: 2, Content: "current text"}}
	var results []protocol.PublishResult
	for _, r := range requests {
		results = append(results, publish(t, s, p, r))
	}
	c := configureRetention(t, s)
	dry, err := s.StorageArchive(t.Context(), true, time.Now().Add(time.Hour), retention.Plan(c, structSnapshot(), time.Now()))
	if err != nil || dry.Items != 1 {
		t.Fatalf("dryrun: %+v %v", dry, err)
	}
	if info, _ := s.StorageInfo(t.Context()); info.ArchivedVersions != 0 {
		t.Fatal("dryrun modified payloads")
	}
	if r := archiveBatch(t, s, c); r.Items != 1 {
		t.Fatalf("archived %+v", r)
	}
	if r := archiveBatch(t, s, c); r.Items != 0 {
		t.Fatal("archival was not idempotent")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	replayed, err := s.Publish(t.Context(), p, requests[0])
	if err != nil || !replayed.Replayed || replayed.Artifact != results[0].Artifact {
		t.Fatalf("exact replay: %+v %v", replayed, err)
	}
	changed := requests[0]
	changed.Content = "different"
	_, err = s.Publish(t.Context(), p, changed)
	requireCode(t, err, "operation_conflict")
	for i, want := range results {
		got, err := s.StorageHistory(t.Context(), p.Scope, "notes", int64(i+1))
		if err != nil || got != want.Artifact {
			t.Fatalf("history v%d: %+v %v", i+1, got, err)
		}
	}
	if head, err := s.Get(t.Context(), p, "notes"); err != nil || head != results[2].Artifact {
		t.Fatal("head changed", head, err)
	}
	if v, err := s.StorageVerify(t.Context()); err != nil || v.Items != 1 {
		t.Fatal(v, err)
	}
	if r, err := s.StorageRestore(t.Context(), 32); err != nil || r.Items != 1 {
		t.Fatal(r, err)
	}
	var nulls int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM artifact_versions WHERE content IS NULL").Scan(&nulls); err != nil || nulls != 0 {
		t.Fatal(nulls, err)
	}
	if got, err := s.Publish(t.Context(), p, requests[0]); err != nil || got.Artifact != results[0].Artifact {
		t.Fatal(got, err)
	}
}

func TestStorageColdFailuresAreNotMissingAndPreserveClaims(t *testing.T) {
	s, _ := newStore(t)
	_, p := newAgent(t, s, "worker", "scope")
	c := configureRetention(t, s)
	for _, state := range []string{"pending", "uncertain", "incomplete", "limit_exceeded"} {
		body, _ := json.Marshal(map[string]any{"schema": 1, "fingerprint": state, "state": state})
		publish(t, s, p, protocol.PublishRequest{Key: "inference/v1/jobs/" + state, OperationID: state, Content: string(body)})
	}
	request := protocol.PublishRequest{Key: "inference/v1/jobs/closed", OperationID: "completed", Content: `{"schema":1,"fingerprint":"closed","state":"complete","response":{"complete":true,"text":"saved answer"}}`}
	want := publish(t, s, p, request).Artifact
	if r := archiveBatch(t, s, c); r.Items != 1 {
		t.Fatal(r)
	}
	for _, state := range []string{"pending", "uncertain", "incomplete", "limit_exceeded"} {
		var content sql.NullString
		if err := s.db.QueryRow("SELECT content FROM artifact_versions WHERE scope=? AND key=?", p.Scope, "inference/v1/jobs/"+state).Scan(&content); err != nil || !content.Valid {
			t.Fatal("protected state archived", state, err)
		}
	}
	if got, err := s.Get(t.Context(), p, request.Key); err != nil || got != want {
		t.Fatal("cold current read", got, err)
	}
	var digest string
	if err := s.db.QueryRow("SELECT sha256 FROM cold_payloads WHERE key=?", request.Key).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(c.ColdDir, digest+".gzip")
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var events []retention.Event
	s.SetStorageObserver(func(_ context.Context, e retention.Event) { events = append(events, e) })
	for _, data := range [][]byte{[]byte("broken gzip"), original[:len(original)/2]} {
		if err = os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
		_, err = s.Get(t.Context(), p, request.Key)
		if err == nil {
			t.Fatal("corrupt archive accepted")
		}
		var e *Error
		if !errors.As(err, &e) || e.Code == "not_found" {
			t.Fatal(err)
		}
		if _, err = s.StorageVerify(t.Context()); err == nil {
			t.Fatal("verification accepted corruption")
		}
	}
	if err = os.Remove(file); err != nil {
		t.Fatal(err)
	}
	_, err = s.Publish(t.Context(), p, request)
	requireCode(t, err, "archive_unavailable")
	if len(events) == 0 || events[0].Operation != "read" || events[0].Outcome != "error" {
		t.Fatal("missing content-free error observation", events)
	}
	if err = os.WriteFile(file, original, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(t.Context(), p, request.Key); err != nil || got != want {
		t.Fatal("recovered archive", got, err)
	}
}

func TestStorageMigrationRollbackAndArchiveCommitFailure(t *testing.T) {
	s, _ := newStore(t)
	_, p := newAgent(t, s, "worker", "scope")
	r1 := protocol.PublishRequest{Key: "report", OperationID: "old", Content: strings.Repeat("data", 1000)}
	first := publish(t, s, p, r1)
	publish(t, s, p, protocol.PublishRequest{Key: "report", OperationID: "new", ExpectedVersion: 1, Content: "latest"})
	c := configureRetention(t, s)
	if _, err := s.db.Exec("CREATE TRIGGER fail_cold BEFORE INSERT ON cold_payloads BEGIN SELECT RAISE(ABORT,'synthetic cold reference failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StorageArchive(t.Context(), false, time.Now().Add(time.Hour), retention.Plan(c, structSnapshot(), time.Now())); err == nil {
		t.Fatal("injected failure ignored")
	}
	var refs int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM cold_payloads").Scan(&refs); err != nil || refs != 0 {
		t.Fatal(refs, err)
	}
	if got, err := s.Publish(t.Context(), p, r1); err != nil || got.Artifact != first.Artifact {
		t.Fatal("failed archive damaged original", got, err)
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_cold"); err != nil {
		t.Fatal(err)
	}
	if r := archiveBatch(t, s, c); r.Items != 1 {
		t.Fatal(r)
	}
	if v, err := s.StorageVerify(t.Context()); err != nil || v.Items != 1 {
		t.Fatal(v, err)
	}
}

func TestStorageArchivePreservesFrozenSyncAndConcurrentCAS(t *testing.T) {
	s, path := newStore(t)
	_, p := newAgent(t, s, "worker", "scope")
	first := publish(t, s, p, protocol.PublishRequest{Key: "a", OperationID: "a1", Content: "old a"}).Artifact
	second := publish(t, s, p, protocol.PublishRequest{Key: "b", OperationID: "b1", Content: "old b"}).Artifact
	page, err := s.Sync(t.Context(), p, protocol.SyncRequest{Limit: 1})
	if err != nil || !page.HasMore {
		t.Fatal(page, err)
	}
	publish(t, s, p, protocol.PublishRequest{Key: "a", OperationID: "a2", ExpectedVersion: 1, Content: "new a"})
	publish(t, s, p, protocol.PublishRequest{Key: "b", OperationID: "b2", ExpectedVersion: 1, Content: "new b"})
	c := configureRetention(t, s)
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	failures := make(chan error, 3)
	for _, instance := range []*Store{s, other} {
		wg.Add(1)
		go func(instance *Store) {
			defer wg.Done()
			_, err := instance.StorageArchive(t.Context(), false, time.Now().Add(time.Hour), retention.Plan(c, structSnapshot(), time.Now()))
			failures <- err
		}(instance)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := other.Publish(t.Context(), p, protocol.PublishRequest{Key: "a", OperationID: "a3", ExpectedVersion: 2, Content: "newest a"})
		failures <- err
	}()
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	next, err := s.Sync(t.Context(), p, protocol.SyncRequest{Limit: 1, Cursor: page.Cursor})
	if err != nil || next.HasMore || len(next.Entries) != 1 || next.Entries[0].Artifact != second {
		t.Fatal("frozen snapshot changed", next, err)
	}
	if page.Entries[0].Artifact != first {
		t.Fatal(page)
	}
	delta, err := s.Sync(t.Context(), p, protocol.SyncRequest{Cursor: next.Cursor})
	if err != nil || len(delta.Entries) != 3 {
		t.Fatal(delta, err)
	}
	if !reflect.DeepEqual([]string{delta.Entries[0].Artifact.Content, delta.Entries[1].Artifact.Content, delta.Entries[2].Artifact.Content}, []string{"new a", "new b", "newest a"}) {
		t.Fatal("delta content/order changed", delta)
	}
}
