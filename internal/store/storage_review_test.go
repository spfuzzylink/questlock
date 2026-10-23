package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/retention"
)

func TestStorageReviewAdministrativeWritesRefreshCapacityOnExistingAndReplacementConnections(t *testing.T) {
	s, path := newStore(t)
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	_, principal := newAgent(t, s, "existing-worker", "scope")
	config := configureRetention(t, s)
	config.MaxDatabaseBytes = 16 << 20
	if err := s.StorageConfigure(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	var pageSize, prior int64
	if err := other.db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	if err := other.db.QueryRow("PRAGMA max_page_count").Scan(&prior); err != nil {
		t.Fatal(err)
	}
	want := config.MaxDatabaseBytes / pageSize
	if prior <= want {
		t.Fatalf("fixture did not retain a stale connection limit: %d <= %d", prior, want)
	}
	assertLimit := func(t *testing.T) {
		t.Helper()
		var actual int64
		if err := other.db.QueryRow("PRAGMA max_page_count").Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if actual != want {
			t.Fatalf("write allowed %d pages after policy configured %d", actual, want)
		}
	}
	if _, err := other.CreateToken(t.Context(), "after-configure", "scope"); err != nil {
		t.Fatal(err)
	}
	assertLimit(t)
	// Closing the idle connection forces the next operation through the DSN
	// again, where max_page_count is not itself a persistent database setting.
	other.db.SetMaxIdleConns(0)
	other.db.SetMaxIdleConns(1)
	if _, err := other.CreateToken(t.Context(), "after-reconnect", "scope"); err != nil {
		t.Fatal(err)
	}
	assertLimit(t)
	other.db.SetMaxIdleConns(0)
	other.db.SetMaxIdleConns(1)
	if err := other.RevokeAgent(t.Context(), principal.AgentID); err != nil {
		t.Fatal(err)
	}
	assertLimit(t)
}

func TestStorageReviewSkippedCandidatesDoNotStarveFittingPayloads(t *testing.T) {
	for _, mode := range []string{"oversized_history", "pending_current"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := newStore(t)
			_, p := newAgent(t, s, "worker", "scope")
			for i := 0; i < 20; i++ {
				key := fmt.Sprintf("a%02d", i)
				if mode == "oversized_history" {
					publish(t, s, p, protocol.PublishRequest{Key: key, OperationID: key + "-old", Content: "xx"})
					publish(t, s, p, protocol.PublishRequest{Key: key, OperationID: key + "-head", ExpectedVersion: 1, Content: "head"})
				} else {
					publish(t, s, p, protocol.PublishRequest{Key: "inference/v1/jobs/" + key, OperationID: key, Content: fmt.Sprintf(`{"schema":1,"fingerprint":%q,"state":"pending"}`, key)})
				}
			}
			var target protocol.Artifact
			budget := retention.Budget{Items: 1, Bytes: 1}
			if mode == "oversized_history" {
				target = publish(t, s, p, protocol.PublishRequest{Key: "z", OperationID: "z-old", Content: "x"}).Artifact
				publish(t, s, p, protocol.PublishRequest{Key: "z", OperationID: "z-head", ExpectedVersion: 1, Content: "head"})
			} else {
				target = publish(t, s, p, protocol.PublishRequest{Key: "inference/v1/jobs/z", OperationID: "z-complete", Content: `{"schema":1,"fingerprint":"z","state":"complete","response":{"complete":true}}`}).Artifact
				budget.Bytes = 1024
			}
			configureRetention(t, s)
			report, err := s.StorageArchive(t.Context(), false, time.Now().Add(time.Hour), budget)
			if err != nil || report.Items != 1 {
				t.Fatalf("later fitting payload starved: %+v %v", report, err)
			}
			var inline sql.NullString
			if err := s.db.QueryRow("SELECT content FROM artifact_versions WHERE scope=? AND key=? AND version=?", target.Scope, target.Key, target.Version).Scan(&inline); err != nil || inline.Valid {
				t.Fatalf("target did not move: valid=%v error=%v", inline.Valid, err)
			}
			if got, err := s.StorageHistory(t.Context(), target.Scope, target.Key, target.Version); err != nil || got != target {
				t.Fatalf("target changed after archival: %+v %v", got, err)
			}
			if mode == "pending_current" {
				var protected int
				if err := s.db.QueryRow("SELECT COUNT(*) FROM artifact_versions WHERE key LIKE 'inference/v1/jobs/a%' AND content IS NOT NULL").Scan(&protected); err != nil || protected != 20 {
					t.Fatalf("pending claims moved: hot=%d error=%v", protected, err)
				}
			}
		})
	}
}

func TestStorageReviewMigrationRefusalAndLateFailurePreserveOriginalSchema(t *testing.T) {
	for _, mode := range []string{"custom_index", "policy_insert_failure"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := newStore(t)
			_, p := newAgent(t, s, "worker", "scope")
			request := protocol.PublishRequest{Key: "report", OperationID: "original", Content: "synthetic immutable text"}
			want := publish(t, s, p, request).Artifact
			parent, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			config := retention.DefaultConfig(filepath.Join(parent, "cold"))
			setup, cleanup := "CREATE INDEX review_custom_content ON artifact_versions(content)", "DROP INDEX review_custom_content"
			if mode == "policy_insert_failure" {
				setup = "CREATE TRIGGER review_fail_policy BEFORE INSERT ON storage_policy BEGIN SELECT RAISE(ABORT,'synthetic policy failure'); END"
				cleanup = "DROP TRIGGER review_fail_policy"
			}
			if _, err := s.db.Exec(setup); err != nil {
				t.Fatal(err)
			}
			if err := s.StorageConfigure(t.Context(), config); err == nil {
				t.Fatal("migration ignored synthetic refusal/failure")
			}
			var notNull, policies, leftovers int
			if err := s.db.QueryRow("SELECT \"notnull\" FROM pragma_table_info('artifact_versions') WHERE name='content'").Scan(&notNull); err != nil || notNull != 1 {
				t.Fatalf("failed migration changed nullable schema: %d %v", notNull, err)
			}
			if err := s.db.QueryRow("SELECT COUNT(*) FROM storage_policy").Scan(&policies); err != nil || policies != 0 {
				t.Fatalf("failed migration persisted policy: %d %v", policies, err)
			}
			if err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name IN ('artifact_versions_legacy','artifacts_retention','operations_retention')").Scan(&leftovers); err != nil || leftovers != 0 {
				t.Fatalf("failed migration left partial tables: %d %v", leftovers, err)
			}
			if got, err := s.Publish(t.Context(), p, request); err != nil || !got.Replayed || got.Artifact != want {
				t.Fatalf("failed migration changed original receipt: %+v %v", got, err)
			}
			foreign, err := s.db.Query("PRAGMA foreign_key_check")
			if err != nil {
				t.Fatal(err)
			}
			broken := foreign.Next()
			foreignErr := foreign.Err()
			closeErr := foreign.Close()
			if broken || foreignErr != nil || closeErr != nil {
				t.Fatalf("failed migration damaged foreign keys: broken=%v error=%v close=%v", broken, foreignErr, closeErr)
			}
			if _, err := s.db.Exec(cleanup); err != nil {
				t.Fatal(err)
			}
			if err := s.StorageConfigure(t.Context(), config); err != nil {
				t.Fatalf("recovered migration failed: %v", err)
			}
			if _, err := s.StorageVerify(t.Context()); err != nil {
				t.Fatalf("recovered migration failed verification: %v", err)
			}
		})
	}
}

func TestStorageReviewVerifyRejectsForeignKeyDamageWithoutColdPayloads(t *testing.T) {
	s, _ := newStore(t)
	_, p := newAgent(t, s, "worker", "scope")
	publish(t, s, p, protocol.PublishRequest{Key: "report", OperationID: "original", Content: "synthetic text"})
	if _, err := s.db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DELETE FROM agents WHERE agent_id=?", p.AgentID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	_, err := s.StorageVerify(t.Context())
	requireCode(t, err, "archive_corrupt")
}
