package store

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/spfuzzylink/questlock/protocol"
	"strings"
	"time"
)

type syncCursor struct {
	Schema    int    `json:"schema"`
	Authority string `json:"authority"`
	Scope     string `json:"scope"`
	Kind      string `json:"kind"`
	Through   int64  `json:"through"`
	After     int64  `json:"after"`
	Key       string `json:"key,omitempty"` // base64 avoids worst-case JSON escaping
}

func initializeSync(ctx context.Context, tx *sql.Tx) error {
	var entropy [64]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO sync_authority(id, source_id, cursor_key) VALUES (1, ?, ?)", hex.EncodeToString(entropy[:32]), entropy[32:])
	return err
}
func signCursor(c syncCursor, key []byte) string {
	b, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, key)
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func openCursor(raw, authority, scope string, key []byte, maximum int64) (syncCursor, error) {
	bad := invalid("invalid sync cursor; restart with an empty cursor against the intended authority")
	var c syncCursor
	if len(raw) > protocol.MaxSyncCursorBytes {
		return c, bad
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return c, bad
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return c, bad
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return c, bad
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(b)
	if !hmac.Equal(mac.Sum(nil), sig) || protocol.DecodeSyncJSON(b, &c) != nil || c.Schema != 1 || c.Authority != authority || c.Scope != scope || c.Through < 0 || c.After < 0 || c.Through > maximum || c.After > maximum {
		return c, bad
	}
	switch c.Kind {
	case "snapshot":
		last, err := base64.RawURLEncoding.DecodeString(c.Key)
		if err != nil || len(last) > 512 || len(last) > 0 && validateKey(string(last)) != nil || c.After != 0 {
			return c, bad
		}
	case "changes":
		if c.Key != "" || c.Through != 0 && c.Through < c.After {
			return c, bad
		}
	default:
		return c, bad
	}
	return c, nil
}

// Sync reconstructs bounded historical snapshots and accepted-write deltas.
// It authenticates inside one read transaction and accepts no incoming writes.
func (s *Store) Sync(ctx context.Context, p Principal, request protocol.SyncRequest) (result protocol.SyncPage, resultErr error) {
	started := time.Now()
	defer func() { s.observeStorageFailure(ctx, "read", started, resultErr) }()
	var page protocol.SyncPage
	r, err := request.Bounds()
	if err != nil {
		return page, invalid(err.Error())
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if err = checkActive(ctx, tx, p); err != nil {
		return page, err
	}
	var authority string
	var secret []byte
	if err = tx.QueryRowContext(ctx, "SELECT source_id, cursor_key FROM sync_authority WHERE id=1").Scan(&authority, &secret); err != nil {
		return page, err
	}
	identity, identityErr := hex.DecodeString(authority)
	if identityErr != nil || len(identity) != 32 || len(secret) != 32 {
		return page, errors.New("sync authority state is invalid")
	}
	var maximum int64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(id),0) FROM audit WHERE scope=? AND outcome='accepted'", p.Scope).Scan(&maximum); err != nil {
		return page, err
	}
	c := syncCursor{Schema: 1, Authority: authority, Scope: p.Scope, Kind: "snapshot", Through: maximum}
	if r.Cursor != "" {
		c, err = openCursor(r.Cursor, authority, p.Scope, secret, maximum)
		if err != nil {
			return page, err
		}
	}
	if c.Kind == "changes" && c.Through == 0 {
		c.Through = maximum
	}
	page = protocol.SyncPage{Authority: authority, Scope: p.Scope, Kind: c.Kind, Through: c.Through, Entries: []protocol.SyncEntry{}}
	var rows *sql.Rows
	if c.Kind == "snapshot" {
		last, _ := base64.RawURLEncoding.DecodeString(c.Key)
		rows, err = tx.QueryContext(ctx, `SELECT a.id,v.scope,v.key,v.version,v.content,v.updated_at
			FROM (SELECT key,MAX(id) id FROM audit WHERE scope=? AND outcome='accepted' AND id<=? AND key>? GROUP BY key ORDER BY key LIMIT ?) latest
   JOIN audit a ON a.id=latest.id JOIN artifact_versions v ON v.scope=a.scope AND v.key=a.key AND v.version=a.current_version
			ORDER BY v.key`, p.Scope, c.Through, string(last), r.Limit+1)
	} else {
		rows, err = tx.QueryContext(ctx, `SELECT a.id,v.scope,v.key,v.version,v.content,v.updated_at
   FROM audit a JOIN artifact_versions v ON v.scope=a.scope AND v.key=a.key AND v.version=a.current_version
   WHERE a.scope=? AND a.outcome='accepted' AND a.id>? AND a.id<=? ORDER BY a.id LIMIT ?`, p.Scope, c.After, c.Through, r.Limit+1)
	}
	if err != nil {
		return page, err
	}
	defer rows.Close()
	// Reserve bounded envelope/cursor space. Count content in its actual escaped
	// JSON form, including the worst-case sixfold expansion.
	used := 2048
	for rows.Next() {
		if len(page.Entries) == r.Limit {
			page.HasMore = true
			break
		}
		var item protocol.SyncEntry
		var content sql.NullString
		if err = rows.Scan(&item.Sequence, &item.Artifact.Scope, &item.Artifact.Key, &item.Artifact.Version, &content, &item.Artifact.UpdatedAt); err != nil {
			return page, err
		}
		item.Artifact, err = s.resolveContent(ctx, tx, item.Artifact, content)
		if err != nil {
			return protocol.SyncPage{}, err
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return page, err
		}
		if used+len(encoded)+1 > r.MaxBytes {
			if len(page.Entries) == 0 {
				return page, &Error{Code: "sync_item_too_large", Message: "next sync item exceeds max_bytes; increase the page byte limit"}
			}
			page.HasMore = true
			break
		}
		used += len(encoded) + 1
		page.Entries = append(page.Entries, item)
		if c.Kind == "snapshot" {
			c.Key = base64.RawURLEncoding.EncodeToString([]byte(item.Artifact.Key))
		} else {
			c.After = item.Sequence
		}
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if err = rows.Close(); err != nil {
		return page, err
	}
	if !page.HasMore {
		c = syncCursor{Schema: 1, Authority: authority, Scope: p.Scope, Kind: "changes", After: page.Through}
	}
	page.Cursor = signCursor(c, secret)
	encoded, err := json.Marshal(page)
	if err != nil {
		return page, err
	}
	if len(page.Cursor) > protocol.MaxSyncCursorBytes || len(encoded)+1 > r.MaxBytes {
		return protocol.SyncPage{}, invalid("sync page exceeds response bound")
	}
	if err = tx.Commit(); err != nil {
		return protocol.SyncPage{}, err
	}
	return page, nil
}
