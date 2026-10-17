package edge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/protocol"
)

// Pull fetches exactly one page from the persisted checkpoint. A snapshot stays
// private in staging until complete; page data and its checkpoint commit in one
// SQLite transaction. Failed validation or quota checks retain the old cursor.
func (s *Store) Pull(ctx context.Context, c *client.Client, options PullOptions) (PullResult, error) {
	if err := s.enter(); err != nil {
		return PullResult{}, err
	}
	defer s.mu.Unlock()
	pinned, err := s.identity(ctx)
	if err != nil {
		return PullResult{}, err
	}
	actual, err := c.Identity(ctx)
	if err != nil {
		return PullResult{}, err
	}
	if actual != pinned {
		return PullResult{}, ErrIdentity
	}
	var cursor, phase, lastKey string
	var through, lastSequence int64
	if err = s.db.QueryRowContext(ctx, "SELECT cursor,phase,through,last_seq,last_key FROM pull_state WHERE id=1").Scan(&cursor, &phase, &through, &lastSequence, &lastKey); err != nil {
		return PullResult{}, err
	}
	page, err := c.Sync(ctx, protocol.SyncRequest{Cursor: cursor, Limit: options.Limit, MaxBytes: options.MaxBytes})
	if err != nil {
		return PullResult{}, err
	}
	if page.Authority != pinned.Authority || page.Scope != pinned.Scope {
		return PullResult{}, ErrIdentity
	}
	if page.Kind != phase || through >= 0 && page.Through != through || page.Through < lastSequence {
		return PullResult{}, errors.New("pull phase or frozen horizon changed")
	}
	if phase == "snapshot" && len(page.Entries) > 0 && page.Entries[0].Artifact.Key <= lastKey {
		return PullResult{}, errors.New("snapshot key order changed across pages")
	}
	if phase == "changes" && len(page.Entries) > 0 && page.Entries[0].Sequence <= lastSequence {
		return PullResult{}, errors.New("change sequence regressed across pages")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PullResult{}, err
	}
	defer tx.Rollback()
	kind := "visible"
	if phase == "snapshot" {
		kind = "staging"
	}
	for _, item := range page.Entries {
		a := item.Artifact
		if phase == "changes" {
			var prior int64
			err = tx.QueryRowContext(ctx, "SELECT version FROM cache WHERE kind='visible' AND key=?", a.Key).Scan(&prior)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return PullResult{}, err
			}
			if a.Version != prior+1 {
				return PullResult{}, errors.New("accepted artifact version gap or regression")
			}
		}
		raw, _ := json.Marshal(a)
		if _, err = tx.ExecContext(ctx, "INSERT INTO cache(kind,key,version,sequence,artifact) VALUES(?,?,?,?,?) ON CONFLICT(kind,key) DO UPDATE SET version=excluded.version,sequence=excluded.sequence,artifact=excluded.artifact", kind, a.Key, a.Version, item.Sequence, string(raw)); err != nil {
			return PullResult{}, err
		}
		lastKey = a.Key
		lastSequence = item.Sequence
	}
	var count, bytes int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(length(CAST(artifact AS BLOB))),0) FROM cache WHERE kind=?", kind).Scan(&count, &bytes); err != nil {
		return PullResult{}, err
	}
	if count > MaxCacheItems || bytes > MaxCacheBytes {
		return PullResult{}, ErrLimit
	}
	nextPhase := phase
	nextThrough := page.Through
	if !page.HasMore {
		if phase == "snapshot" {
			if _, err = tx.ExecContext(ctx, "DELETE FROM cache WHERE kind='visible'"); err != nil {
				return PullResult{}, err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE cache SET kind='visible' WHERE kind='staging'"); err != nil {
				return PullResult{}, err
			}
		}
		nextPhase = "changes"
		nextThrough = -1
		lastSequence = page.Through
		lastKey = ""
	}
	if phase == "changes" || !page.HasMore {
		if _, err = tx.ExecContext(ctx, "UPDATE pull_state SET complete=1,visible_through=? WHERE id=1", lastSequence); err != nil {
			return PullResult{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE pull_state SET cursor=?,phase=?,through=?,last_seq=?,last_key=? WHERE id=1", page.Cursor, nextPhase, nextThrough, lastSequence, lastKey); err != nil {
		return PullResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return PullResult{}, err
	}
	return PullResult{Kind: page.Kind, Applied: len(page.Entries), HasMore: page.HasMore, Through: page.Through}, nil
}
