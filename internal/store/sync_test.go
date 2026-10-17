package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/protocol"
)

func syncPage(t *testing.T, s *Store, p Principal, r protocol.SyncRequest) protocol.SyncPage {
	t.Helper()
	page, err := s.Sync(testContext, p, r)
	if err != nil {
		t.Fatal(err)
	}
	return page
}
func drainSync(t *testing.T, s *Store, p Principal, first protocol.SyncPage) (protocol.SyncPage, []protocol.SyncEntry) {
	t.Helper()
	page := first
	items := append([]protocol.SyncEntry(nil), page.Entries...)
	for page.HasMore {
		page = syncPage(t, s, p, protocol.SyncRequest{Cursor: page.Cursor, Limit: 1})
		items = append(items, page.Entries...)
	}
	return page, items
}

func TestSyncSnapshotAndChangeHorizonsExcludeConcurrentUpdates(t *testing.T) {
	s, _ := newStore(t)
	_, p := newAgent(t, s, "reader", "scope")
	_, other := newAgent(t, s, "other", "other-scope")
	a := protocol.PublishRequest{Key: "a", OperationID: "a1", Content: "a1"}
	publish(t, s, p, a)
	publish(t, s, other, protocol.PublishRequest{Key: "private", OperationID: "other", Content: "other scope"})
	publish(t, s, p, protocol.PublishRequest{Key: "b", OperationID: "b1", Content: "b1"})
	publish(t, s, p, protocol.PublishRequest{Key: "c", OperationID: "c1", Content: "c1"})
	publish(t, s, p, protocol.PublishRequest{Key: "b", OperationID: "b2", ExpectedVersion: 1, Content: "b2"})
	publish(t, s, p, a) // replay is not a new accepted publication
	_, err := s.Publish(testContext, p, protocol.PublishRequest{Key: "a", OperationID: "stale", Content: "rejected"})
	requireCode(t, err, "version_conflict")
	first := syncPage(t, s, p, protocol.SyncRequest{Limit: 1})
	if first.Kind != "snapshot" || !first.HasMore || len(first.Entries) != 1 || first.Entries[0].Artifact.Content != "a1" {
		t.Fatal(first)
	}
	for _, r := range []protocol.PublishRequest{{Key: "a", OperationID: "a2", ExpectedVersion: 1, Content: "a2"}, {Key: "b", OperationID: "b3", ExpectedVersion: 2, Content: "b3"}, {Key: "0", OperationID: "zero", Content: "new earlier key"}, {Key: "d", OperationID: "d1", Content: "d1"}} {
		publish(t, s, p, r)
	}
	last, items := drainSync(t, s, p, first)
	got := []string{}
	for _, item := range items {
		got = append(got, item.Artifact.Content)
	}
	if !reflect.DeepEqual(got, []string{"a1", "b2", "c1"}) || last.Through != first.Through {
		t.Fatalf("snapshot changed between pages: %+v", items)
	}
	changes := syncPage(t, s, p, protocol.SyncRequest{Cursor: last.Cursor, Limit: 2})
	if changes.Kind != "changes" || !changes.HasMore {
		t.Fatal(changes)
	}
	publish(t, s, p, protocol.PublishRequest{Key: "a", OperationID: "a3", ExpectedVersion: 2, Content: "a3"})
	end, deltas := drainSync(t, s, p, changes)
	got = nil
	var previous int64
	for _, item := range deltas {
		got = append(got, item.Artifact.Content)
		if item.Sequence <= previous || item.Sequence > changes.Through {
			t.Fatal("delta order or frozen horizon violated")
		}
		previous = item.Sequence
	}
	if !reflect.DeepEqual(got, []string{"a2", "b3", "new earlier key", "d1"}) {
		t.Fatal(got)
	}
	next := syncPage(t, s, p, protocol.SyncRequest{Cursor: end.Cursor})
	if len(next.Entries) != 1 || next.Entries[0].Artifact.Content != "a3" {
		t.Fatal(next)
	}
	var before, after int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM audit").Scan(&before); err != nil {
		t.Fatal(err)
	}
	empty := syncPage(t, s, p, protocol.SyncRequest{Cursor: next.Cursor})
	if len(empty.Entries) != 0 || empty.HasMore || empty.Kind != "changes" {
		t.Fatal(empty)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM audit").Scan(&after); err != nil || before != after {
		t.Fatal("read-only sync changed audit")
	}
}

func TestSyncCursorSurvivesRestartAndRejectsScopeAuthorityTampering(t *testing.T) {
	s, path := newStore(t)
	token, p := newAgent(t, s, "reader", "scope")
	_, same := newAgent(t, s, "same-scope", "scope")
	_, other := newAgent(t, s, "other", "other")
	for _, key := range []string{"a", "b"} {
		publish(t, s, p, protocol.PublishRequest{Key: key, OperationID: key, Content: key})
	}
	first := syncPage(t, s, p, protocol.SyncRequest{Limit: 1})
	expected := syncPage(t, s, p, protocol.SyncRequest{Cursor: first.Cursor, Limit: 1})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	p, err = reopened.Authenticate(testContext, token)
	if err != nil {
		t.Fatal(err)
	}
	actual := syncPage(t, reopened, p, protocol.SyncRequest{Cursor: first.Cursor, Limit: 1})
	if !reflect.DeepEqual(actual, expected) {
		t.Fatal("cursor/replay page changed after restart")
	}
	if page := syncPage(t, reopened, same, protocol.SyncRequest{Cursor: first.Cursor, Limit: 1}); !reflect.DeepEqual(page, expected) {
		t.Fatal("same-scope principal could not resume")
	}
	_, err = reopened.Sync(testContext, other, protocol.SyncRequest{Cursor: first.Cursor})
	requireCode(t, err, "invalid")
	forged := p
	forged.Scope = other.Scope
	_, err = reopened.Sync(testContext, forged, protocol.SyncRequest{})
	requireCode(t, err, "unauthorized")
	different, _ := newStore(t)
	_, dp := newAgent(t, different, "reader", "scope")
	_, err = different.Sync(testContext, dp, protocol.SyncRequest{Cursor: first.Cursor})
	requireCode(t, err, "invalid")
	for _, cursor := range []string{"garbage", first.Cursor + "x", strings.Repeat("x", protocol.MaxSyncCursorBytes+1), "e30.bad"} {
		_, err = reopened.Sync(testContext, p, protocol.SyncRequest{Cursor: cursor})
		requireCode(t, err, "invalid")
	}
	if err := reopened.RevokeAgent(testContext, p.AgentID); err != nil {
		t.Fatal(err)
	}
	_, err = reopened.Sync(testContext, p, protocol.SyncRequest{Cursor: first.Cursor})
	requireCode(t, err, "unauthorized")
}

func TestSyncByteLimitsDoNotSkipLargeEscapedItems(t *testing.T) {
	s, _ := newStore(t)
	_, p := newAgent(t, s, "reader", "scope")
	content := strings.Repeat("\x00", protocol.MaxContentBytes)
	for _, key := range []string{strings.Repeat("<", 512), "z"} {
		publish(t, s, p, protocol.PublishRequest{Key: key, OperationID: key[:1], Content: content})
	}
	_, err := s.Sync(testContext, p, protocol.SyncRequest{MaxBytes: protocol.MinSyncBytes})
	requireCode(t, err, "sync_item_too_large")
	first := syncPage(t, s, p, protocol.SyncRequest{Limit: 128})
	if len(first.Entries) != 1 || !first.HasMore || first.Entries[0].Artifact.Content != content {
		t.Fatal("large escaped item skipped or changed")
	}
	encoded, _ := json.Marshal(first)
	if len(encoded)+1 > protocol.MaxSyncBytes {
		t.Fatal("escaped response exceeds wire bound")
	}
	second := syncPage(t, s, p, protocol.SyncRequest{Cursor: first.Cursor})
	if len(second.Entries) != 1 || second.HasMore || second.Entries[0].Artifact.Key != "z" {
		t.Fatal("byte-limited continuation lost item")
	}
	for _, r := range []protocol.SyncRequest{{Limit: -1}, {Limit: 129}, {MaxBytes: 1}, {MaxBytes: protocol.MaxSyncBytes + 1}} {
		_, err := s.Sync(testContext, p, r)
		requireCode(t, err, "invalid")
	}
}

func TestSyncConcurrentWritersAndSnapshotPagination(t *testing.T) {
	s, path := newStore(t)
	token, p := newAgent(t, s, "reader", "scope")
	const count = 30
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("key-%03d", i)
		publish(t, s, p, protocol.PublishRequest{Key: key, OperationID: key, Content: "original"})
	}
	first := syncPage(t, s, p, protocol.SyncRequest{Limit: 1})
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	wp, err := writer.Authenticate(testContext, token)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for i := 0; i < count; i++ {
			key := fmt.Sprintf("key-%03d", i)
			if _, err := writer.Publish(context.Background(), wp, protocol.PublishRequest{Key: key, OperationID: "update-" + key, ExpectedVersion: 1, Content: "updated"}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	last, items := drainSync(t, s, p, first)
	if len(items) != count {
		t.Fatalf("snapshot count: %d", len(items))
	}
	for _, item := range items {
		if item.Artifact.Version != 1 || item.Artifact.Content != "original" {
			t.Fatal("concurrent write leaked into frozen snapshot")
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_, updates := drainSync(t, s, p, syncPage(t, s, p, protocol.SyncRequest{Cursor: last.Cursor, Limit: 1}))
	if len(updates) != count {
		t.Fatalf("missing deltas: %d", len(updates))
	}
	for _, item := range updates {
		if item.Artifact.Version != 2 || item.Artifact.Content != "updated" {
			t.Fatal(item)
		}
	}
}

func TestSyncEmptyScopeAndCorruptAuthorityFailClosed(t *testing.T) {
	s, _ := newStore(t)
	_, p := newAgent(t, s, "reader", "scope")
	first := syncPage(t, s, p, protocol.SyncRequest{})
	if first.Kind != "snapshot" || first.Through != 0 || len(first.Entries) != 0 || first.HasMore {
		t.Fatal(first)
	}
	next := syncPage(t, s, p, protocol.SyncRequest{Cursor: first.Cursor})
	if next.Kind != "changes" || next.Through != 0 || len(next.Entries) != 0 {
		t.Fatal(next)
	}
	if _, err := s.db.Exec("UPDATE sync_authority SET cursor_key=x'00'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sync(testContext, p, protocol.SyncRequest{}); err == nil {
		t.Fatal("corrupt signing key accepted")
	}
}

func TestSyncAdditiveMigrationPreservesVersionsAndReceipts(t *testing.T) {
	s, path := newStore(t)
	token, p := newAgent(t, s, "writer", "scope")
	original := protocol.PublishRequest{Key: "artifact", OperationID: "one", Content: "original"}
	first := publish(t, s, p, original)
	publish(t, s, p, protocol.PublishRequest{Key: "artifact", OperationID: "two", ExpectedVersion: 1, Content: "current"})
	// Model the pre-SecondGate schema; all existing publication tables and rows
	// remain exactly as they were. Reopening must add only sync metadata/indexes.
	if _, err := s.db.Exec("DROP TABLE sync_authority"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	p, err = reopened.Authenticate(testContext, token)
	if err != nil {
		t.Fatal(err)
	}
	page := syncPage(t, reopened, p, protocol.SyncRequest{})
	if len(page.Entries) != 1 || page.Entries[0].Artifact.Version != 2 || page.Entries[0].Artifact.Content != "current" {
		t.Fatal(page)
	}
	replay := publish(t, reopened, p, original)
	if !replay.Replayed || !reflect.DeepEqual(replay.Artifact, first.Artifact) {
		t.Fatal("migration rewrote immutable version or operation receipt")
	}
}

func TestSyncRestoredSQLiteSafeBackupRejectsAheadCursor(t *testing.T) {
	s, _ := newStore(t)
	token, p := newAgent(t, s, "writer", "scope")
	original := protocol.PublishRequest{Key: "artifact", OperationID: "one", Content: "original"}
	second := protocol.PublishRequest{Key: "artifact", OperationID: "two", ExpectedVersion: 1, Content: "backed up"}
	firstResult := publish(t, s, p, original)
	secondResult := publish(t, s, p, second)
	checkpoint := syncPage(t, s, p, protocol.SyncRequest{})
	backup := filepath.Join(t.TempDir(), "safe-backup.db")
	// VACUUM INTO produces a consistent SQLite backup, including live WAL
	// contents and cursor identity. Never copy a live database file directly.
	if _, err := s.db.ExecContext(testContext, "VACUUM INTO ?", backup); err != nil {
		t.Fatal(err)
	}
	publish(t, s, p, protocol.PublishRequest{Key: "artifact", OperationID: "three", ExpectedVersion: 2, Content: "after backup"})
	ahead := syncPage(t, s, p, protocol.SyncRequest{})
	if ahead.Through <= checkpoint.Through {
		t.Fatal("test did not advance beyond the backup watermark")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Replace the original authority with the older backup; do not run both
	// database copies as independent writers.
	restored, err := Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	p, err = restored.Authenticate(testContext, token)
	if err != nil {
		t.Fatal(err)
	}
	before := syncPage(t, restored, p, protocol.SyncRequest{})
	if before.Authority != ahead.Authority || before.Through != checkpoint.Through {
		t.Fatal("safe backup did not retain the same authority and older watermark")
	}
	_, err = restored.Sync(testContext, p, protocol.SyncRequest{Cursor: ahead.Cursor})
	requireCode(t, err, "invalid")
	for _, table := range []string{"artifact_versions", "operations", "audit"} {
		var count int
		if err := restored.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 2 {
			t.Fatalf("ahead-cursor rejection changed backed-up %s: count %d, error %v", table, count, err)
		}
	}
	current, err := restored.Get(testContext, p, "artifact")
	if err != nil || !reflect.DeepEqual(current, secondResult.Artifact) {
		t.Fatalf("restored current artifact changed: %+v %v", current, err)
	}
	resumed := syncPage(t, restored, p, protocol.SyncRequest{Cursor: checkpoint.Cursor})
	if resumed.Kind != "changes" || len(resumed.Entries) != 0 || resumed.HasMore {
		t.Fatal("a checkpoint present in the backup could not resume normally")
	}
	for _, pair := range []struct {
		request protocol.PublishRequest
		result  protocol.PublishResult
	}{{original, firstResult}, {second, secondResult}} {
		replayed := publish(t, restored, p, pair.request)
		if !replayed.Replayed || !reflect.DeepEqual(replayed.Artifact, pair.result.Artifact) {
			t.Fatal("safe backup lost an immutable version or operation receipt")
		}
	}
}
