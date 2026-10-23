package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/retention"
)

func TestStorageDatabaseCapacityRejectsAtomicPublication(t *testing.T) {
	s, path := newStore(t)
	token, _ := newAgent(t, s, "capacity-worker", "capacity")
	other, err := Open(path) // Open before configuration to exercise stale handles.
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	c := configureRetention(t, s)
	c.MaxDatabaseBytes = 16 << 20
	if err := s.StorageConfigure(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	p, err := other.Authenticate(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	other.db.SetMaxIdleConns(0) // Every operation must survive connection replacement.
	content := strings.Repeat("synthetic capacity fixture\n", 20000)
	var accepted protocol.PublishResult
	var original protocol.PublishRequest
	failed := false
	for i := range 64 {
		r := protocol.PublishRequest{Key: fmt.Sprintf("capacity/%03d", i), OperationID: fmt.Sprintf("op-%03d", i), Content: content}
		result, err := other.Publish(t.Context(), p, r)
		if err != nil {
			requireCode(t, err, "storage_limit")
			failed = true
			if _, err := other.Get(t.Context(), p, r.Key); err == nil {
				t.Fatal("capacity failure left a partial publication")
			} else {
				requireCode(t, err, "not_found")
			}
			break
		}
		accepted, original = result, r
	}
	if !failed || accepted.Artifact.Version != 1 {
		t.Fatal("did not exercise the configured database limit")
	}
	replayed, err := other.Publish(t.Context(), p, original)
	if err != nil || !replayed.Replayed || replayed.Artifact != accepted.Artifact {
		t.Fatalf("full database lost a confirmed receipt: %+v %v", replayed, err)
	}
	var pages, size int64
	if err := other.db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if err := other.db.QueryRow("PRAGMA page_size").Scan(&size); err != nil || pages*size > c.MaxDatabaseBytes {
		t.Fatalf("main pages exceeded cap: %d * %d %v", pages, size, err)
	}
	if _, err := other.StorageVerify(t.Context()); err != nil {
		t.Fatal("capacity failure damaged retained state", err)
	}
}

func TestStorageColdCapacityReportsNoCommittedArchive(t *testing.T) {
	s, _ := newStore(t)
	_, p := newAgent(t, s, "cold-capacity", "capacity")
	r := protocol.PublishRequest{Key: "note", OperationID: "first", Content: "original content retained on cold capacity failure"}
	accepted := publish(t, s, p, r)
	publish(t, s, p, protocol.PublishRequest{Key: r.Key, OperationID: "second", ExpectedVersion: 1, Content: "current"})
	c := configureRetention(t, s)
	c.MaxColdBytes = 1
	if err := s.StorageConfigure(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	var event retention.Event
	s.SetStorageObserver(func(_ context.Context, e retention.Event) { event = e })
	report, err := s.StorageArchive(t.Context(), false, time.Now().Add(time.Hour), retention.Plan(c, structSnapshot(), time.Now()))
	requireCode(t, err, "storage_limit")
	if report.Status != "error" || report.Items != 0 || report.Bytes != 0 || event.Outcome != "error" || event.ErrorCode != "capacity" || event.Items != 0 {
		t.Fatalf("failed batch claimed committed work: %+v %+v", report, event)
	}
	replay, err := s.Publish(t.Context(), p, r)
	if err != nil || replay.Artifact != accepted.Artifact || !replay.Replayed {
		t.Fatal("cold capacity failure lost hot receipt", err)
	}
	if info, err := s.StorageInfo(t.Context()); err != nil || info.ArchivedVersions != 0 || info.ColdFiles != 0 {
		t.Fatalf("failed batch changed payload placement: %+v %v", info, err)
	}
}
