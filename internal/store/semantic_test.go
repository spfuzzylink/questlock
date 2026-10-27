package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/semantic"
)

func semanticFixture(t *testing.T, s *Store, p Principal, label string, created time.Time) semantic.PutRequest {
	t.Helper()
	fingerprint := semanticDigest(label)
	text := "synthetic completed " + label
	body, err := json.Marshal(map[string]any{"schema": 1, "fingerprint": fingerprint, "state": "complete", "created_at": created, "expires_at": created.Add(2 * time.Hour), "response": map[string]any{"complete": true, "text": text}})
	if err != nil {
		t.Fatal(err)
	}
	a := publish(t, s, p, protocol.PublishRequest{Key: "inference/v1/jobs/" + fingerprint, OperationID: label, Content: string(body)}).Artifact
	e := semantic.Entry{Partition: semanticDigest("partition"), ModelID: semanticDigest("model"), Vector: []float32{1, 0}, Source: semantic.Source{Key: a.Key, Version: a.Version, SHA256: semanticDigest(a.Content), OutputSHA256: semanticDigest(text)}, CreatedAt: created, ExpiresAt: created.Add(time.Hour)}
	e.ID = semantic.EntryID(e)
	return semantic.PutRequest{Entry: e, MaxEntries: 100}
}

func semanticQuery(e semantic.Entry) semantic.Query {
	return semantic.Query{Partition: e.Partition, ModelID: e.ModelID, Vector: []float32{1, 0}, MinSimilarity: 0.8, TopK: 10, MaxScan: 100}
}

func putSemantic(t *testing.T, s *Store, p Principal, r semantic.PutRequest) {
	t.Helper()
	if err := s.PutSemantic(t.Context(), p, r); err != nil {
		t.Fatal(err)
	}
}

func TestSemanticPersistenceScopeAndMetadataFiltersBeforeScan(t *testing.T) {
	s, path := newStore(t)
	_, p := newAgent(t, s, "semantic-a", "scope-a")
	_, other := newAgent(t, s, "semantic-b", "scope-b")
	created := time.Now().UTC().Add(-time.Minute)
	want := semanticFixture(t, s, p, "wanted", created)
	putSemantic(t, s, p, want)
	for i := range 8 {
		r := semanticFixture(t, s, other, fmt.Sprintf("other-%d", i), created.Add(time.Second))
		putSemantic(t, s, other, r)
		r = semanticFixture(t, s, p, fmt.Sprintf("filtered-%d", i), created.Add(time.Second))
		switch i % 3 {
		case 0:
			r.Entry.Partition = semanticDigest("different partition")
		case 1:
			r.Entry.ModelID = semanticDigest("different model")
		case 2:
			r.Entry.Vector = []float32{1, 0, 0}
		}
		r.Entry.ID = semantic.EntryID(r.Entry)
		putSemantic(t, s, p, r)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	q := semanticQuery(want.Entry)
	q.MaxScan = 1
	result, err := s.QuerySemantic(t.Context(), p, q)
	if err != nil || result.Scanned != 1 || result.Truncated || len(result.Candidates) != 1 || result.Candidates[0].Source != want.Entry.Source {
		t.Fatalf("scope/metadata filters or restart: %+v %v", result, err)
	}
	forged := p
	forged.Scope = other.Scope
	_, err = s.QuerySemantic(t.Context(), forged, q)
	requireCode(t, err, "unauthorized")
	requireCode(t, s.PutSemantic(t.Context(), forged, want), "unauthorized")
	if err := s.RevokeAgent(t.Context(), p.AgentID); err != nil {
		t.Fatal(err)
	}
	_, err = s.QuerySemantic(t.Context(), p, q)
	requireCode(t, err, "unauthorized")
}

func TestSemanticScopeCapDeterministicScanAndNoArtifactMutation(t *testing.T) {
	s, _ := newStore(t)
	_, p := newAgent(t, s, "semantic", "scope")
	created := time.Now().UTC().Add(-time.Minute)
	var entries []semantic.PutRequest
	for i := range 6 {
		r := semanticFixture(t, s, p, fmt.Sprintf("entry-%d", i), created.Add(time.Duration(i)*time.Second))
		r.MaxEntries = 3
		if i%2 == 1 {
			r.Entry.Partition = semanticDigest("other family")
			r.Entry.ModelID = semanticDigest("other model")
			r.Entry.ID = semantic.EntryID(r.Entry)
		}
		entries = append(entries, r)
	}
	before, _ := s.Audit(t.Context(), p, 100)
	for _, r := range entries {
		putSemantic(t, s, p, r)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM semantic_entries WHERE scope=?", p.Scope).Scan(&count); err != nil || count != 3 {
		t.Fatalf("cap bypassed across family/model: %d %v", count, err)
	}
	after, _ := s.Audit(t.Context(), p, 100)
	if len(after) != len(before) {
		t.Fatal("index changed publication audit")
	}
	for _, r := range entries {
		a, err := s.Get(t.Context(), p, r.Entry.Source.Key)
		if err != nil || a.Version != 1 || semanticDigest(a.Content) != r.Entry.Source.SHA256 {
			t.Fatal("eviction changed source", err)
		}
	}
	q := semanticQuery(entries[4].Entry)
	q.MaxScan = 1
	result, err := s.QuerySemantic(t.Context(), p, q)
	if err != nil || result.Scanned != 1 || result.Truncated || len(result.Candidates) != 1 {
		t.Fatalf("bounded scan: %+v %v", result, err)
	}
	// A separate partition contains enough matches to exercise the max+1 probe.
	for i := range 3 {
		r := semanticFixture(t, s, p, fmt.Sprintf("tie-%d", i), created)
		putSemantic(t, s, p, r)
	}
	q.MaxScan = 2
	result, err = s.QuerySemantic(t.Context(), p, q)
	if err != nil || result.Scanned != 2 || !result.Truncated || len(result.Candidates) != 2 {
		t.Fatalf("max+1 probe: %+v %v", result, err)
	}
	again, err := s.QuerySemantic(t.Context(), p, q)
	a, _ := json.Marshal(result)
	b, _ := json.Marshal(again)
	if err != nil || string(a) != string(b) {
		t.Fatal("query order nondeterministic", err)
	}
}

func TestSemanticIDBindingDuplicateAndExpiryCannotExtend(t *testing.T) {
	s, _ := newStore(t)
	_, p := newAgent(t, s, "semantic", "scope")
	r := semanticFixture(t, s, p, "expiry", time.Now().UTC().Add(-time.Minute))
	r.Entry.ExpiresAt = time.Now().UTC().Add(10 * time.Minute)
	putSemantic(t, s, p, r)
	original := r.Entry.ExpiresAt
	r.Entry.ExpiresAt = r.Entry.CreatedAt.Add(90 * time.Minute)
	r.Entry.CreatedAt = time.Now().UTC() // Broker derives trusted source creation.
	putSemantic(t, s, p, r)
	var count int
	var expiry, created string
	if err := s.db.QueryRow("SELECT COUNT(*),expires_at,created_at FROM semantic_entries WHERE scope=?", p.Scope).Scan(&count, &expiry, &created); err != nil || count != 1 || expiry != semanticTime(original) {
		t.Fatalf("duplicate or expiry extended: %d %s %v", count, expiry, err)
	}
	if created == semanticTime(r.Entry.CreatedAt) {
		t.Fatal("caller controlled creation order")
	}
	r.Entry.ID = semanticDigest("arbitrary duplicate")
	requireCode(t, s.PutSemantic(t.Context(), p, r), "invalid")
	r.Entry.ID = semantic.EntryID(r.Entry)
	r.Entry.Source.OutputSHA256 = semanticDigest("different output")
	r.Entry.ID = semantic.EntryID(r.Entry)
	requireCode(t, s.PutSemantic(t.Context(), p, r), "invalid")
	// Expired rows do not consume a scan slot and are pruned on a later put.
	if _, err := s.db.Exec("UPDATE semantic_entries SET expires_at=?", semanticTime(time.Now().Add(-time.Second))); err != nil {
		t.Fatal(err)
	}
	result, err := s.QuerySemantic(t.Context(), p, semanticQuery(r.Entry))
	if err != nil || result.Scanned != 0 || len(result.Candidates) != 0 {
		t.Fatal(result, err)
	}
	next := semanticFixture(t, s, p, "next", time.Now().UTC().Add(-time.Minute))
	putSemantic(t, s, p, next)
	if err := s.db.QueryRow("SELECT COUNT(*) FROM semantic_entries").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestSemanticRejectsUnconfirmedStaleExpiredAndMalformedSources(t *testing.T) {
	for _, state := range []string{"pending", "uncertain", "failed", "incomplete", "unconfirmed", "unknown_future_state"} {
		t.Run(state, func(t *testing.T) {
			s, _ := newStore(t)
			_, p := newAgent(t, s, "semantic", "scope")
			r := semanticFixture(t, s, p, "state", time.Now().UTC().Add(-time.Minute))
			putSemantic(t, s, p, r)
			a, _ := s.Get(t.Context(), p, r.Entry.Source.Key)
			var body map[string]any
			json.Unmarshal([]byte(a.Content), &body)
			body["state"] = state
			encoded, _ := json.Marshal(body)
			changed := publish(t, s, p, protocol.PublishRequest{Key: a.Key, ExpectedVersion: 1, OperationID: "change", Content: string(encoded)}).Artifact
			result, err := s.QuerySemantic(t.Context(), p, semanticQuery(r.Entry))
			if err != nil || result.Scanned != 1 || len(result.Candidates) != 0 {
				t.Fatal("stale source returned", result, err)
			}
			r.Entry.Source.Version = changed.Version
			r.Entry.Source.SHA256 = semanticDigest(changed.Content)
			r.Entry.ID = semantic.EntryID(r.Entry)
			requireCode(t, s.PutSemantic(t.Context(), p, r), "invalid")
		})
	}
	for _, change := range []string{"schema", "fingerprint", "complete", "response", "text", "expires_at", "duplicate"} {
		t.Run(change, func(t *testing.T) {
			s, _ := newStore(t)
			_, p := newAgent(t, s, "semantic", "scope")
			r := semanticFixture(t, s, p, "malformed", time.Now().UTC().Add(-time.Minute))
			a, _ := s.Get(t.Context(), p, r.Entry.Source.Key)
			var body map[string]any
			json.Unmarshal([]byte(a.Content), &body)
			switch change {
			case "schema":
				body["schema"] = 2
			case "fingerprint":
				body["fingerprint"] = semanticDigest("wrong")
			case "complete":
				body["response"].(map[string]any)["complete"] = false
			case "response":
				delete(body, "response")
			case "text":
				delete(body["response"].(map[string]any), "text")
			case "expires_at":
				body["expires_at"] = time.Now().Add(-time.Second)
			}
			encoded, _ := json.Marshal(body)
			if change == "duplicate" {
				encoded = []byte(strings.TrimSuffix(string(encoded), "}") + `,"state":"complete"}`)
			}
			changed := publish(t, s, p, protocol.PublishRequest{Key: a.Key, ExpectedVersion: 1, OperationID: "change", Content: string(encoded)}).Artifact
			r.Entry.Source.Version = changed.Version
			r.Entry.Source.SHA256 = semanticDigest(changed.Content)
			r.Entry.ID = semantic.EntryID(r.Entry)
			requireCode(t, s.PutSemantic(t.Context(), p, r), "invalid")
		})
	}
}

func TestSemanticConcurrentPutQueryAndEviction(t *testing.T) {
	s, path := newStore(t)
	_, p := newAgent(t, s, "semantic", "scope")
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	var entries []semantic.PutRequest
	for i := range 24 {
		r := semanticFixture(t, s, p, fmt.Sprintf("parallel-%d", i), time.Now().UTC().Add(-time.Minute))
		r.MaxEntries = 5
		entries = append(entries, r)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 48)
	for i, r := range entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			target := s
			if i%2 == 1 {
				target = other
			}
			errs <- target.PutSemantic(ctx, p, r)
			_, err := target.QuerySemantic(ctx, p, semanticQuery(r.Entry))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM semantic_entries").Scan(&count); err != nil || count != 5 {
		t.Fatal(count, err)
	}
}

func TestSemanticColdArchiveRestoreAndFailuresPreserveSource(t *testing.T) {
	s, _ := newStore(t)
	_, p := newAgent(t, s, "semantic", "scope")
	r := semanticFixture(t, s, p, "cold", time.Now().UTC().Add(-time.Minute))
	putSemantic(t, s, p, r)
	c := configureRetention(t, s)
	if report := archiveBatch(t, s, c); report.Items != 1 {
		t.Fatal(report)
	}
	for range 2 {
		putSemantic(t, s, p, r)
		result, err := s.QuerySemantic(t.Context(), p, semanticQuery(r.Entry))
		if err != nil || len(result.Candidates) != 1 {
			t.Fatal(result, err)
		}
	}
	file := filepath.Join(c.ColdDir, r.Entry.Source.SHA256+".gzip")
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, []byte("broken gzip"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = s.QuerySemantic(t.Context(), p, semanticQuery(r.Entry))
	requireCode(t, err, "archive_corrupt")
	requireCode(t, s.PutSemantic(t.Context(), p, r), "archive_corrupt")
	if err = os.Remove(file); err != nil {
		t.Fatal(err)
	}
	_, err = s.QuerySemantic(t.Context(), p, semanticQuery(r.Entry))
	requireCode(t, err, "archive_unavailable")
	if err = os.WriteFile(file, original, 0600); err != nil {
		t.Fatal(err)
	}
	if report, err := s.StorageRestore(t.Context(), 10); err != nil || report.Items != 1 {
		t.Fatal(report, err)
	}
	result, err := s.QuerySemantic(t.Context(), p, semanticQuery(r.Entry))
	if err != nil || len(result.Candidates) != 1 {
		t.Fatal(result, err)
	}
	a, err := s.Get(t.Context(), p, r.Entry.Source.Key)
	if err != nil || a.Version != 1 || semanticDigest(a.Content) != r.Entry.Source.SHA256 {
		t.Fatal("source mutated", a, err)
	}
}

func TestSemanticRanksValidCandidatesAndBoundsSourceLifetime(t *testing.T) {
	s, _ := newStore(t)
	_, p := newAgent(t, s, "semantic", "scope")
	created := time.Now().UTC().Add(-time.Minute)
	high := semanticFixture(t, s, p, "highest", created)
	mid := semanticFixture(t, s, p, "middle", created)
	low := semanticFixture(t, s, p, "lowest", created)
	mid.Entry.Vector = []float32{0.9, 0.1}
	low.Entry.Vector = []float32{0.8, 0.2}
	// Caller TTL can shorten the source but cannot extend its freshness.
	mid.Entry.ExpiresAt = created.Add(3 * time.Hour)
	for _, r := range []semantic.PutRequest{high, mid, low} {
		putSemantic(t, s, p, r)
	}
	var expiry string
	if err := s.db.QueryRow("SELECT expires_at FROM semantic_entries WHERE scope=? AND id=?", p.Scope, mid.Entry.ID).Scan(&expiry); err != nil || expiry != semanticTime(created.Add(2*time.Hour)) {
		t.Fatal("index extended source freshness", expiry, err)
	}
	q := semanticQuery(high.Entry)
	q.TopK = 1
	q.MinSimilarity = 0.99
	result, err := s.QuerySemantic(t.Context(), p, q)
	if err != nil || len(result.Candidates) != 1 || result.Candidates[0].Source != high.Entry.Source {
		t.Fatal("highest similarity lost", result, err)
	}
	// Invalidating the best match must still allow a lower, valid match within
	// the bounded scan to appear; source validation precedes the top-K cutoff.
	a, _ := s.Get(t.Context(), p, high.Entry.Source.Key)
	publish(t, s, p, protocol.PublishRequest{Key: a.Key, ExpectedVersion: 1, OperationID: "invalidate-highest", Content: strings.Replace(a.Content, `"state":"complete"`, `"state":"uncertain"`, 1)})
	result, err = s.QuerySemantic(t.Context(), p, q)
	if err != nil || result.Scanned != 3 || len(result.Candidates) != 1 || result.Candidates[0].Source != mid.Entry.Source {
		t.Fatal("stale best match hid valid candidate", result, err)
	}
	for _, change := range []string{"version", "sha256"} {
		r := low
		if change == "version" {
			r.Entry.Source.Version++
		} else {
			r.Entry.Source.SHA256 = semanticDigest("wrong bytes")
		}
		r.Entry.ID = semantic.EntryID(r.Entry)
		requireCode(t, s.PutSemantic(t.Context(), p, r), "invalid")
	}
}

func TestSemanticDatabasePageCapacityIsAtomic(t *testing.T) {
	s, path := newStore(t)
	_, p := newAgent(t, s, "semantic", "scope")
	r := semanticFixture(t, s, p, "capacity-source", time.Now().UTC().Add(-time.Minute))
	r.Entry.Vector = make([]float32, semantic.MaxDimensions)
	r.Entry.Vector[0] = 1
	if _, err := s.QuerySemantic(t.Context(), p, semanticQuery(r.Entry)); err != nil {
		t.Fatal(err)
	}
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	c := configureRetention(t, s)
	c.MaxDatabaseBytes = 16 << 20
	if err = s.StorageConfigure(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	// Fill most pages with ordinary published data, then exercise vector writes
	// on a handle opened before the page-cap configuration.
	for i := range 32 {
		_, err = other.Publish(t.Context(), p, protocol.PublishRequest{Key: fmt.Sprintf("fill/%d", i), OperationID: fmt.Sprintf("fill-%d", i), Content: strings.Repeat("x", protocol.MaxContentBytes)})
		if err != nil {
			requireCode(t, err, "storage_limit")
			break
		}
	}
	before, _ := s.Audit(t.Context(), p, 100)
	accepted := 0
	for i := range 128 {
		r.Entry.Partition = semanticDigest(fmt.Sprintf("partition-%d", i))
		r.Entry.ID = semantic.EntryID(r.Entry)
		err = other.PutSemantic(t.Context(), p, r)
		if err != nil {
			requireCode(t, err, "storage_limit")
			break
		}
		accepted++
	}
	if err == nil {
		t.Fatal("did not exercise page-cap rejection")
	}
	var count int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM semantic_entries").Scan(&count); err != nil || count != accepted {
		t.Fatal("partial semantic write", count, accepted, err)
	}
	after, _ := s.Audit(t.Context(), p, 100)
	if len(before) != len(after) {
		t.Fatal("failed index write changed audit")
	}
	a, err := s.Get(t.Context(), p, r.Entry.Source.Key)
	if err != nil || a.Version != r.Entry.Source.Version || semanticDigest(a.Content) != r.Entry.Source.SHA256 {
		t.Fatal("capacity failure damaged source", a, err)
	}
	var pages, size int64
	if err = s.db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow("PRAGMA page_size").Scan(&size); err != nil || pages*size > c.MaxDatabaseBytes {
		t.Fatal("page cap exceeded", pages, size, err)
	}
}

func TestSemanticLazySchemaDoesNotBlockFullLegacyDatabaseExactReads(t *testing.T) {
	s, path := newStore(t)
	_, p := newAgent(t, s, "legacy-worker", "legacy-scope")
	r := semanticFixture(t, s, p, "legacy-source", time.Now().UTC().Add(-time.Minute))
	want, err := s.Get(t.Context(), p, r.Entry.Source.Key)
	if err != nil {
		t.Fatal(err)
	}
	original := protocol.PublishRequest{Key: want.Key, OperationID: "legacy-source", Content: want.Content}
	assertNoSemanticSchema := func(s *Store) {
		t.Helper()
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE name LIKE 'semantic_%'").Scan(&count); err != nil || count != 0 {
			t.Fatal("optional schema allocated", count, err)
		}
	}
	assertNoSemanticSchema(s)
	c := configureRetention(t, s)
	c.MaxDatabaseBytes = 16 << 20
	if err = s.StorageConfigure(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	// Simulate an older database whose existing application state occupies its
	// page budget. This table is deliberately unrelated to optional indexing.
	if _, err = s.db.Exec("CREATE TABLE legacy_capacity (payload BLOB NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	var pages, size int64
	if err = s.db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow("PRAGMA page_size").Scan(&size); err != nil {
		t.Fatal(err)
	}
	bulkBytes := c.MaxDatabaseBytes - (pages+32)*size
	if _, err = s.db.Exec("INSERT INTO legacy_capacity(payload) VALUES(zeroblob(?))", bulkBytes); err != nil {
		t.Fatal(err)
	}
	full := false
	for range 128 {
		if _, err = s.db.Exec("INSERT INTO legacy_capacity(payload) VALUES(zeroblob(?))", size); err != nil {
			requireCode(t, storageError(err), "storage_limit")
			full = true
			break
		}
	}
	if !full {
		t.Fatal("fixture failed to fill the legacy page budget")
	}
	stateCounts := func(s *Store) [4]int {
		t.Helper()
		var counts [4]int
		if err := s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM artifact_versions),(SELECT COUNT(*) FROM operations),(SELECT COUNT(*) FROM audit),(SELECT COUNT(*) FROM legacy_capacity)`).Scan(&counts[0], &counts[1], &counts[2], &counts[3]); err != nil {
			t.Fatal(err)
		}
		return counts
	}
	before := stateCounts(s)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal("full legacy database could not reopen for exact use", err)
	}
	defer s.Close()
	assertNoSemanticSchema(s)
	if got, err := s.Get(t.Context(), p, want.Key); err != nil || got != want {
		t.Fatal("exact read regressed", got, err)
	}
	for range 2 {
		_, err = s.QuerySemantic(t.Context(), p, semanticQuery(r.Entry))
		requireCode(t, err, "storage_limit")
		requireCode(t, s.PutSemantic(t.Context(), p, r), "storage_limit")
		assertNoSemanticSchema(s)
		if s.semanticReady {
			t.Fatal("failed initialization was marked ready")
		}
		if after := stateCounts(s); after != before {
			t.Fatal("optional init changed durable state", before, after)
		}
		if got, err := s.Publish(t.Context(), p, original); err != nil || !got.Replayed || got.Artifact != want {
			t.Fatal("exact receipt replay regressed", got, err)
		}
		if got, err := s.Get(t.Context(), p, want.Key); err != nil || got != want {
			t.Fatal("failed optional init damaged exact source", got, err)
		}
		// Existing exact-replay behavior appends its own audit event.
		before[2]++
		if after := stateCounts(s); after != before {
			t.Fatal("exact replay changed more than its audit event", before, after)
		}
	}
	// A failed attempt is not sticky: operator-provided headroom permits a later
	// semantic operation, without reopening or rewriting the exact source.
	c.MaxDatabaseBytes = 32 << 20
	if err = s.StorageConfigure(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	putSemantic(t, s, p, r)
	result, err := s.QuerySemantic(t.Context(), p, semanticQuery(r.Entry))
	if err != nil || len(result.Candidates) != 1 || !s.semanticReady {
		t.Fatal("lazy schema did not recover after headroom", result, err)
	}
	if after := stateCounts(s); after != before {
		t.Fatal("successful optional init changed exact state", before, after)
	}
}
