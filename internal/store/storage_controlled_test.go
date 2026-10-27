package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/retention"
	"github.com/spfuzzylink/questlock/semantic"
)

func controlledStorageContent(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func controlledStorageRequests(t *testing.T) []protocol.PublishRequest {
	t.Helper()
	created := time.Now().UTC().Add(-time.Hour)
	response := map[string]any{"complete": true, "text": "Stored synthetic answer.", "model": "fixture", "stop_reason": "stop", "usage": map[string]any{"input_tokens": 16, "uncached_input_tokens": 16, "output_tokens": 8}}
	originalFingerprint := semanticDigest("original-job")
	originalPlan := semanticDigest("original-plan")
	original := protocol.PublishRequest{Key: "inference/v1/jobs/" + originalFingerprint, OperationID: "controlled-original", Content: controlledStorageContent(t, map[string]any{
		"schema": 1, "state": "complete", "fingerprint": originalFingerprint, "request_fingerprint": originalPlan, "response": response, "created_at": created, "expires_at": created.Add(2 * time.Hour),
	})}
	source := map[string]any{"key": original.Key, "version": 1, "sha256": semanticDigest(original.Content), "output_sha256": semanticDigest("Stored synthetic answer."), "request_fingerprint": originalPlan}
	aliasFingerprint := semanticDigest("resolved-job")
	alias := protocol.PublishRequest{Key: "inference/v1/jobs/" + aliasFingerprint, OperationID: "controlled-resolved", Content: controlledStorageContent(t, map[string]any{
		"schema": 1, "state": "resolved", "fingerprint": aliasFingerprint, "request_fingerprint": semanticDigest("target-plan"), "response": response, "created_at": created, "expires_at": created.Add(2 * time.Hour),
		"resolution": map[string]any{"policy_id": semanticDigest("controlled-policy"), "verification_key": "inference/v1/jobs/" + semanticDigest("verifier-job"), "source": source, "response": response},
	})}
	proof := protocol.PublishRequest{Key: "semantic/v1/provenance/" + semanticDigest(original.Content), OperationID: "controlled-provenance", Content: controlledStorageContent(t, map[string]any{
		"schema": 1, "question": "Explain the synthetic approved evidence.", "request_sha256": semanticDigest("original-request"), "partition": semanticDigest("partition"), "embedding_model_id": semanticDigest("embedding-model"), "source": source,
	})}
	return []protocol.PublishRequest{original, alias, proof}
}

func controlledStorageObject(t *testing.T, content string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(content), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func requireControlledStorageHot(t *testing.T, s *Store, a protocol.Artifact, hot bool) {
	t.Helper()
	var content sql.NullString
	if err := s.db.QueryRow("SELECT content FROM artifact_versions WHERE scope=? AND key=? AND version=?", a.Scope, a.Key, a.Version).Scan(&content); err != nil || content.Valid != hot {
		t.Fatalf("payload residency: hot=%v want=%v error=%v", content.Valid, hot, err)
	}
}

func TestStorageArchivesResolvedAliasesAndImmutableProvenanceWithoutChangingReads(t *testing.T) {
	s, path := newStore(t)
	_, p := newAgent(t, s, "controlled-worker", "scope")
	requests := controlledStorageRequests(t)
	var expected []protocol.Artifact
	for _, request := range requests {
		expected = append(expected, publish(t, s, p, request).Artifact)
	}
	var protected []protocol.Artifact
	for _, state := range []string{"pending", "uncertain", "incomplete", "limit_exceeded", "future_closed"} {
		body := controlledStorageObject(t, requests[1].Content)
		fingerprint := semanticDigest(state)
		body["state"], body["fingerprint"] = state, fingerprint
		protected = append(protected, publish(t, s, p, protocol.PublishRequest{Key: "inference/v1/jobs/" + fingerprint, OperationID: state, Content: controlledStorageContent(t, body)}).Artifact)
	}
	c := configureRetention(t, s)
	c.ArchiveCompletedCurrent = false
	if err := s.StorageConfigure(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if report := archiveBatch(t, s, c); report.Items != 0 {
		t.Fatal("disabled current-payload policy archived controlled records")
	}
	c.ArchiveCompletedCurrent = true
	if err := s.StorageConfigure(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	dry, err := s.StorageArchive(t.Context(), true, time.Now().Add(time.Hour), retention.Budget{Items: 2, Bytes: c.BatchBytes})
	if err != nil || dry.Items != 2 || !dry.DryRun {
		t.Fatalf("bounded dry run: %+v %v", dry, err)
	}
	for _, a := range expected {
		requireControlledStorageHot(t, s, a, true)
	}
	if report, err := s.StorageArchive(t.Context(), false, time.Now().Add(time.Hour), retention.Budget{Items: 1, Bytes: 1}); err != nil || report.Items != 0 {
		t.Fatalf("byte cap ignored: %+v %v", report, err)
	}
	if _, err := s.StorageArchive(t.Context(), false, time.Now().Add(time.Hour), retention.Budget{Items: c.BatchItems + 1, Bytes: c.BatchBytes}); err == nil {
		t.Fatal("item cap ignored")
	}
	if report, err := s.StorageArchive(t.Context(), false, time.Now().Add(time.Hour), retention.Budget{Items: 1, Bytes: c.BatchBytes}); err != nil || report.Items != 1 {
		t.Fatalf("bounded archive: %+v %v", report, err)
	}
	if report := archiveBatch(t, s, c); report.Items != 2 {
		t.Fatalf("remaining controlled payloads not archived: %+v", report)
	}
	if report := archiveBatch(t, s, c); report.Items != 0 {
		t.Fatal("archival replay changed payloads")
	}
	for _, a := range expected {
		requireControlledStorageHot(t, s, a, false)
		var hash string
		if err := s.db.QueryRow("SELECT sha256 FROM cold_payloads WHERE scope=? AND key=? AND version=?", a.Scope, a.Key, a.Version).Scan(&hash); err != nil || hash != semanticDigest(a.Content) {
			t.Fatalf("archival changed payload hash: %q %v", hash, err)
		}
		if got, err := s.Get(t.Context(), p, a.Key); err != nil || got != a {
			t.Fatalf("cold current read changed bytes or metadata: %+v %v", got, err)
		}
	}
	for _, a := range protected {
		requireControlledStorageHot(t, s, a, true)
		if got, err := s.Get(t.Context(), p, a.Key); err != nil || got != a {
			t.Fatal("protected state changed during archival")
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i, request := range requests {
		if got, err := s.Get(t.Context(), p, request.Key); err != nil || got != expected[i] {
			t.Fatalf("restart changed archived read: %+v %v", got, err)
		}
		if got, err := s.Publish(t.Context(), p, request); err != nil || !got.Replayed || got.Artifact != expected[i] {
			t.Fatalf("restart changed publication replay: %+v %v", got, err)
		}
		if got, err := s.StorageHistory(t.Context(), p.Scope, request.Key, expected[i].Version); err != nil || got != expected[i] {
			t.Fatalf("restart changed history identity: %+v %v", got, err)
		}
	}
	if report, err := s.StorageVerify(t.Context()); err != nil || report.Items != 3 {
		t.Fatalf("archived controlled records failed verification: %+v %v", report, err)
	}
}

func TestStorageProtectsMalformedResolvedAliasesAndUnknownProvenance(t *testing.T) {
	requests := controlledStorageRequests(t)
	tests := []struct {
		name  string
		index int
		alter func(map[string]any)
	}{
		{"missing resolution", 1, func(v map[string]any) { delete(v, "resolution") }},
		{"unknown alias metadata", 1, func(v map[string]any) { v["unknown_metadata"] = true }},
		{"alias missing dates", 1, func(v map[string]any) { delete(v, "created_at") }},
		{"alias active reservation", 1, func(v map[string]any) { v["reservation"] = "uncertain-reservation" }},
		{"alias invalid policy hash", 1, func(v map[string]any) { v["resolution"].(map[string]any)["policy_id"] = "unknown" }},
		{"alias invalid verifier reference", 1, func(v map[string]any) { v["resolution"].(map[string]any)["verification_key"] = "other/key" }},
		{"alias invalid source hash", 1, func(v map[string]any) {
			v["resolution"].(map[string]any)["source"].(map[string]any)["sha256"] = "invalid"
		}},
		{"alias points to itself", 1, func(v map[string]any) {
			v["resolution"].(map[string]any)["source"].(map[string]any)["key"] = requests[1].Key
		}},
		{"alias changes copied response", 1, func(v map[string]any) {
			v["resolution"].(map[string]any)["response"].(map[string]any)["text"] = "Changed answer."
		}},
		{"alias incorrect output hash", 1, func(v map[string]any) { v["response"].(map[string]any)["text"] = "Changed answer." }},
		{"proof unknown schema", 2, func(v map[string]any) { v["schema"] = 2 }},
		{"proof unknown fields", 2, func(v map[string]any) { v["unknown_metadata"] = true }},
		{"proof missing question", 2, func(v map[string]any) { delete(v, "question") }},
		{"proof excessive question", 2, func(v map[string]any) { v["question"] = strings.Repeat("q", semantic.MaxInputBytes+1) }},
		{"proof excessive payload", 2, func(v map[string]any) { v["question"] = strings.Repeat("q", 64<<10) }},
		{"proof invalid partition hash", 2, func(v map[string]any) { v["partition"] = "invalid" }},
		{"proof invalid model hash", 2, func(v map[string]any) { v["embedding_model_id"] = "invalid" }},
		{"proof invalid request hash", 2, func(v map[string]any) { v["request_sha256"] = "invalid" }},
		{"proof null source", 2, func(v map[string]any) { v["source"] = nil }},
		{"proof invalid source version", 2, func(v map[string]any) { v["source"].(map[string]any)["version"] = 0 }},
		{"proof invalid source key", 2, func(v map[string]any) { v["source"].(map[string]any)["key"] = "other/key" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, _ := newStore(t)
			_, p := newAgent(t, s, "controlled-worker", "scope")
			request := requests[test.index]
			body := controlledStorageObject(t, request.Content)
			test.alter(body)
			request.Content = controlledStorageContent(t, body)
			want := publish(t, s, p, request).Artifact
			c := configureRetention(t, s)
			if report := archiveBatch(t, s, c); report.Items != 0 {
				t.Fatalf("malformed or unknown current payload archived: %+v", report)
			}
			requireControlledStorageHot(t, s, want, true)
		})
	}
	for _, index := range []int{1, 2} {
		t.Run(fmt.Sprintf("duplicate members kind %d", index), func(t *testing.T) {
			s, _ := newStore(t)
			_, p := newAgent(t, s, "controlled-worker", "scope")
			request := requests[index]
			request.Content = strings.TrimSuffix(request.Content, "}") + `,"schema":1}`
			want := publish(t, s, p, request).Artifact
			c := configureRetention(t, s)
			if report := archiveBatch(t, s, c); report.Items != 0 {
				t.Fatal("ambiguous duplicate members archived")
			}
			requireControlledStorageHot(t, s, want, true)
		})
	}
	t.Run("provenance must be immutable version one", func(t *testing.T) {
		s, _ := newStore(t)
		_, p := newAgent(t, s, "controlled-worker", "scope")
		request := requests[2]
		publish(t, s, p, request)
		request.ExpectedVersion, request.OperationID = 1, "controlled-provenance-replacement"
		want := publish(t, s, p, request).Artifact
		c := configureRetention(t, s)
		c.KeepRecentVersions = 2
		if err := s.StorageConfigure(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		if report := archiveBatch(t, s, c); report.Items != 0 {
			t.Fatal("mutable current provenance archived")
		}
		requireControlledStorageHot(t, s, want, true)
	})
}

func TestStorageMalformedControlledRowsDoNotStarveLaterEligiblePayload(t *testing.T) {
	for _, index := range []int{1, 2} {
		t.Run(fmt.Sprintf("kind %d", index), func(t *testing.T) {
			s, _ := newStore(t)
			_, p := newAgent(t, s, "controlled-worker", "scope")
			requests := controlledStorageRequests(t)
			var protected []protocol.Artifact
			for i := range 20 {
				request := requests[index]
				body := controlledStorageObject(t, request.Content)
				hash := semanticDigest(fmt.Sprintf("malformed-%d", i))
				if index == 1 {
					body["fingerprint"] = hash
					body["resolution"].(map[string]any)["policy_id"] = "invalid"
					request.Key = "inference/v1/jobs/" + hash
				} else {
					body["source"].(map[string]any)["sha256"] = hash
					body["question"] = ""
					request.Key = "semantic/v1/provenance/" + hash
				}
				request.OperationID = fmt.Sprintf("malformed-%d", i)
				request.Content = controlledStorageContent(t, body)
				protected = append(protected, publish(t, s, p, request).Artifact)
			}
			target := publish(t, s, p, requests[index]).Artifact
			c := configureRetention(t, s)
			report, err := s.StorageArchive(t.Context(), false, time.Now().Add(time.Hour), retention.Budget{Items: 1, Bytes: c.BatchBytes})
			if err != nil || report.Items != 1 {
				t.Fatalf("malformed candidates starved valid payload: %+v %v", report, err)
			}
			requireControlledStorageHot(t, s, target, false)
			for _, a := range protected {
				requireControlledStorageHot(t, s, a, true)
			}
		})
	}
}

func controlledCheckpoint(t *testing.T, s *Store) archiveCheckpoint {
	t.Helper()
	var checkpoint archiveCheckpoint
	if err := s.db.QueryRow("SELECT scope,key,version FROM storage_archive_checkpoint WHERE id=1").Scan(&checkpoint.scope, &checkpoint.key, &checkpoint.version); err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

func controlledProofRequest(t *testing.T, hash, operation string, malformed bool, question string) protocol.PublishRequest {
	t.Helper()
	request := controlledStorageRequests(t)[2]
	body := controlledStorageObject(t, request.Content)
	body["source"].(map[string]any)["sha256"] = hash
	if malformed {
		body["source"].(map[string]any)["version"] = 0
	}
	if question != "" {
		body["question"] = question
	}
	request.Key, request.OperationID = "semantic/v1/provenance/"+hash, operation
	request.Content = controlledStorageContent(t, body)
	return request
}

func TestStorageBoundedCheckpointProgressSurvivesRestartAndWraps(t *testing.T) {
	s, path := newStore(t)
	_, p := newAgent(t, s, "checkpoint-worker", "scope")
	var allocated int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='storage_archive_checkpoint'").Scan(&allocated); err != nil || allocated != 0 {
		t.Fatal("checkpoint schema was allocated before retention opt-in")
	}
	for i := range 2*minimumArchiveScanItems + 7 {
		request := controlledProofRequest(t, fmt.Sprintf("%064x", i+1), fmt.Sprintf("malformed-checkpoint-%d", i), true, "")
		publish(t, s, p, request)
	}
	targetRequest := controlledProofRequest(t, strings.Repeat("f", 64), "last-valid-proof", false, "")
	target := publish(t, s, p, targetRequest).Artifact
	configureRetention(t, s)
	budget := retention.Budget{Items: 1, Bytes: 1 << 20}
	first, err := s.StorageArchive(t.Context(), false, time.Now().Add(time.Hour), budget)
	if err != nil || first.Items != 0 || first.ScannedItems != minimumArchiveScanItems || first.ScannedBytes > budget.Bytes || first.Reason != "scan_limit" {
		t.Fatalf("malformed row work was not capped: %+v %v", first, err)
	}
	checkpoint := controlledCheckpoint(t, s)
	if checkpoint.key != "semantic/v1/provenance/"+fmt.Sprintf("%064x", minimumArchiveScanItems) || checkpoint.version != 1 {
		t.Fatalf("checkpoint did not advance past rejected rows: %+v", checkpoint)
	}
	// This new eligible key falls behind the durable cursor and must be reached
	// after wrapping rather than changing any artifact's identity or version.
	earlyRequest := controlledProofRequest(t, strings.Repeat("0", 64), "new-earlier-proof", false, "")
	early := publish(t, s, p, earlyRequest).Artifact
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := controlledCheckpoint(t, s); got != checkpoint {
		t.Fatal("restart lost the archive checkpoint")
	}
	dry, err := s.StorageArchive(t.Context(), true, time.Now().Add(time.Hour), budget)
	if err != nil || dry.Items != 0 || dry.ScannedItems != minimumArchiveScanItems || controlledCheckpoint(t, s) != checkpoint {
		t.Fatalf("dry run changed progress: %+v %v", dry, err)
	}
	second, err := s.StorageArchive(t.Context(), false, time.Now().Add(time.Hour), budget)
	if err != nil || second.Items != 0 || second.ScannedItems != minimumArchiveScanItems || controlledCheckpoint(t, s).key <= checkpoint.key {
		t.Fatalf("bounded next page repeated rejected rows: %+v %v", second, err)
	}
	third, err := s.StorageArchive(t.Context(), false, time.Now().Add(time.Hour), budget)
	if err != nil || third.Items != 1 || third.ScannedItems > minimumArchiveScanItems || controlledCheckpoint(t, s) != (archiveCheckpoint{}) {
		t.Fatalf("eventual eligible payload or traversal wrap failed: %+v %v", third, err)
	}
	requireControlledStorageHot(t, s, target, false)
	fourth, err := s.StorageArchive(t.Context(), false, time.Now().Add(time.Hour), budget)
	if err != nil || fourth.Items != 1 || fourth.ScannedItems > minimumArchiveScanItems {
		t.Fatalf("wrapped traversal missed a newly inserted earlier key: %+v %v", fourth, err)
	}
	requireControlledStorageHot(t, s, early, false)
	for _, test := range []struct {
		request  protocol.PublishRequest
		artifact protocol.Artifact
	}{{targetRequest, target}, {earlyRequest, early}} {
		if got, err := s.Get(t.Context(), p, test.request.Key); err != nil || got != test.artifact {
			t.Fatal("checkpoint traversal changed artifact identity")
		}
		if got, err := s.Publish(t.Context(), p, test.request); err != nil || !got.Replayed || got.Artifact != test.artifact {
			t.Fatal("checkpoint traversal changed receipt identity")
		}
	}
}

func TestStorageBoundsInspectedPayloadBytesAndEventuallyReachesEligibleRecord(t *testing.T) {
	s, _ := newStore(t)
	_, p := newAgent(t, s, "byte-bound-worker", "scope")
	// The first row is larger than the batch's entire output budget. It must be
	// skipped without consuming the decode allowance or blocking later rows.
	publish(t, s, p, controlledProofRequest(t, fmt.Sprintf("%064x", 0), "oversized-first", true, strings.Repeat("q", 64<<10)))
	for i := range 10 {
		publish(t, s, p, controlledProofRequest(t, fmt.Sprintf("%064x", i+1), fmt.Sprintf("byte-bound-malformed-%d", i), true, strings.Repeat("q", 16<<10)))
	}
	target := publish(t, s, p, controlledProofRequest(t, strings.Repeat("f", 64), "byte-bound-target", false, "")).Artifact
	configureRetention(t, s)
	budget := retention.Budget{Items: 1, Bytes: 32 << 10}
	archived := false
	for i := range 8 {
		report, err := s.StorageArchive(t.Context(), false, time.Now().Add(time.Hour), budget)
		if err != nil || report.ScannedItems > minimumArchiveScanItems || report.ScannedBytes > minimumArchiveScanBytes || report.Bytes > budget.Bytes || report.Items > 1 {
			t.Fatalf("input or output cap exceeded: %+v %v", report, err)
		}
		if i == 0 && (report.Items != 0 || report.Reason != "scan_limit" || report.ScannedBytes == 0) {
			t.Fatalf("large malformed payloads were not bounded: %+v", report)
		}
		if report.Items == 1 {
			archived = true
			break
		}
	}
	if !archived {
		t.Fatal("bounded byte scans starved the later fitting payload")
	}
	requireControlledStorageHot(t, s, target, false)
}

func TestStorageConcurrentCheckpointsPreserveClaimsAndArchiveIdentity(t *testing.T) {
	s, path := newStore(t)
	_, p := newAgent(t, s, "concurrent-checkpoint-worker", "scope")
	pendingRequest := protocol.PublishRequest{Key: "inference/v1/jobs/" + semanticDigest("pending-checkpoint"), OperationID: "pending-checkpoint", Content: `{"schema":1,"state":"pending"}`}
	pending := publish(t, s, p, pendingRequest).Artifact
	var requests []protocol.PublishRequest
	var expected []protocol.Artifact
	for i := range 3 {
		request := controlledProofRequest(t, fmt.Sprintf("%064x", i+1), fmt.Sprintf("concurrent-proof-%d", i), false, "")
		requests = append(requests, request)
		expected = append(expected, publish(t, s, p, request).Artifact)
	}
	c := configureRetention(t, s)
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	results := make(chan StorageReport, 2)
	errors := make(chan error, 2)
	var wg sync.WaitGroup
	for _, instance := range []*Store{s, other} {
		wg.Add(1)
		go func(instance *Store) {
			defer wg.Done()
			report, err := instance.StorageArchive(t.Context(), false, time.Now().Add(time.Hour), retention.Budget{Items: 1, Bytes: c.BatchBytes})
			results <- report
			errors <- err
		}(instance)
	}
	wg.Wait()
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
		if report := <-results; report.Items != 1 || report.ScannedItems > minimumArchiveScanItems || report.Bytes > c.BatchBytes {
			t.Fatalf("concurrent archive did not respect the committed checkpoint: %+v", report)
		}
	}
	if info, err := s.StorageInfo(t.Context()); err != nil || info.ArchivedVersions != 2 {
		t.Fatalf("concurrent scans overlapped or lost progress: %+v %v", info, err)
	}
	requireControlledStorageHot(t, s, pending, true)
	if got, err := s.Publish(t.Context(), p, pendingRequest); err != nil || !got.Replayed || got.Artifact != pending {
		t.Fatal("checkpoint advancement changed a pending claim")
	}
	for i, request := range requests {
		if got, err := other.Get(t.Context(), p, request.Key); err != nil || got != expected[i] {
			t.Fatal("concurrent checkpoint changed artifact identity")
		}
		if got, err := other.Publish(t.Context(), p, request); err != nil || !got.Replayed || got.Artifact != expected[i] {
			t.Fatal("concurrent checkpoint changed operation replay")
		}
	}
}

func TestStorageLegacyConfiguredDryRunDoesNotAllocateCheckpoint(t *testing.T) {
	s, _ := newStore(t)
	_, p := newAgent(t, s, "legacy-checkpoint-worker", "scope")
	want := publish(t, s, p, controlledStorageRequests(t)[2]).Artifact
	c := configureRetention(t, s)
	// Simulate a retained policy created by a version without checkpoints.
	if _, err := s.db.Exec("DROP TABLE storage_archive_checkpoint"); err != nil {
		t.Fatal(err)
	}
	report, err := s.StorageArchive(t.Context(), true, time.Now().Add(time.Hour), retention.Budget{Items: 1, Bytes: c.BatchBytes})
	if err != nil || report.Items != 1 {
		t.Fatalf("legacy policy dry run: %+v %v", report, err)
	}
	var allocated int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='storage_archive_checkpoint'").Scan(&allocated); err != nil || allocated != 0 {
		t.Fatal("dry run persisted a checkpoint schema migration")
	}
	requireControlledStorageHot(t, s, want, true)
	if report := archiveBatch(t, s, c); report.Items != 1 {
		t.Fatal("legacy policy could not allocate its live checkpoint")
	}
	requireControlledStorageHot(t, s, want, false)
}

func TestStorageCheckpointFailureRollsBackArchiveAndReceiptState(t *testing.T) {
	s, _ := newStore(t)
	_, p := newAgent(t, s, "failed-checkpoint-worker", "scope")
	request := controlledStorageRequests(t)[2]
	want := publish(t, s, p, request).Artifact
	c := configureRetention(t, s)
	if _, err := s.db.Exec("CREATE TRIGGER fail_archive_checkpoint BEFORE INSERT ON storage_archive_checkpoint BEGIN SELECT RAISE(ABORT,'synthetic checkpoint failure'); END"); err != nil {
		t.Fatal(err)
	}
	report, err := s.StorageArchive(t.Context(), false, time.Now().Add(time.Hour), retention.Budget{Items: 1, Bytes: c.BatchBytes})
	if err == nil || report.Items != 0 || report.Bytes != 0 {
		t.Fatalf("checkpoint failure acknowledged uncommitted archival: %+v %v", report, err)
	}
	requireControlledStorageHot(t, s, want, true)
	if got, err := s.Publish(t.Context(), p, request); err != nil || !got.Replayed || got.Artifact != want {
		t.Fatal("checkpoint failure changed a confirmed operation")
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_archive_checkpoint"); err != nil {
		t.Fatal(err)
	}
	if report := archiveBatch(t, s, c); report.Items != 1 {
		t.Fatal("checkpoint recovery could not adopt the retained cold payload")
	}
}
