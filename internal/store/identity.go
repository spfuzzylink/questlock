package store

import (
	"context"
	"database/sql"

	"github.com/spfuzzylink/questlock/protocol"
)

func identityIn(ctx context.Context, q rowQuerier, p Principal) (protocol.Identity, error) {
	i := protocol.Identity{Scope: p.Scope, AgentID: p.AgentID}
	if err := checkActive(ctx, q, p); err != nil {
		return i, err
	}
	if err := q.QueryRowContext(ctx, "SELECT source_id FROM sync_authority WHERE id=1").Scan(&i.Authority); err != nil {
		return i, err
	}
	return i, i.Validate()
}

func (s *Store) Identity(ctx context.Context, p Principal) (protocol.Identity, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return protocol.Identity{}, err
	}
	defer tx.Rollback()
	i, err := identityIn(ctx, tx, p)
	if err != nil {
		return protocol.Identity{}, err
	}
	return i, tx.Commit()
}
