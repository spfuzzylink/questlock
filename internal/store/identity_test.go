package store

import (
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/protocol"
)

func TestPinnedIdentityIsRecheckedInPublicationTransaction(t *testing.T) {
	s, _ := newStore(t)
	_, p := newAgent(t, s, "writer", "scope")
	pin, err := s.Identity(testContext, p)
	if err != nil {
		t.Fatal(err)
	}
	// Identity preflight happened before the authority metadata changed. The
	// rejection must occur in the actual CAS transaction, not only at preflight.
	if _, err := s.db.Exec("UPDATE sync_authority SET source_id=?", strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	r := protocol.PublishRequest{Key: "artifact", OperationID: "one", Content: "one"}
	_, err = s.PublishPinned(testContext, p, r, pin)
	requireCode(t, err, "identity_conflict")
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM artifact_versions").Scan(&count); err != nil || count != 0 {
		t.Fatal("mismatched pin changed authoritative versions")
	}
	// Existing unpinned publication semantics remain untouched.
	accepted := publish(t, s, p, r)
	if accepted.Artifact.Version != 1 {
		t.Fatal(accepted)
	}
}

func TestPinnedPublicationSerializesConcurrentIdentityAndCredentialMutation(t *testing.T) {
	for _, tc := range []struct {
		name, sql, code string
		args            []any
	}{
		{name: "credential revocation", sql: "UPDATE agents SET active=0 WHERE agent_id=?", args: []any{"writer"}, code: "unauthorized"},
		{name: "authority changed", sql: "UPDATE sync_authority SET source_id=?", args: []any{strings.Repeat("0", 64)}, code: "identity_conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newStore(t)
			_, p := newAgent(t, s, "writer", "scope")
			pin, err := s.Identity(testContext, p)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := s.db.BeginTx(testContext, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = tx.Exec(tc.sql, tc.args...); err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				close(started)
				_, err := s.PublishPinned(testContext, p, protocol.PublishRequest{Key: "a", OperationID: "one", Content: "one"}, pin)
				done <- err
			}()
			<-started
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			requireCode(t, <-done, tc.code)
			var count int
			if err := s.db.QueryRow("SELECT COUNT(*) FROM operations").Scan(&count); err != nil || count != 0 {
				t.Fatal("changed identity/credential obtained a successful receipt")
			}
		})
	}
}
