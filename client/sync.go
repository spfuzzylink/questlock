package client

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/spfuzzylink/questlock/protocol"
)

// Sync reads one bounded snapshot/change page. It never retries, applies writes
// or changes local authority. The normal client TLS, proxy and redirect rules
// apply. Persist a page's cursor only after durably consuming all its entries.
func (c *Client) Sync(ctx context.Context, request protocol.SyncRequest) (protocol.SyncPage, error) {
	var page protocol.SyncPage
	r, err := request.Bounds()
	if err != nil {
		return page, err
	}
	var raw json.RawMessage
	if err = c.doMax(ctx, http.MethodPost, "/v1/sync", nil, r, &raw, r.MaxBytes); err != nil {
		return page, err
	}
	if len(raw)+1 > r.MaxBytes {
		return protocol.SyncPage{}, errors.New("questlock: oversized sync response")
	}
	page, err = protocol.DecodeSyncPage(raw)
	if err != nil {
		return protocol.SyncPage{}, errors.New("questlock: invalid or oversized sync response")
	}
	authority, err := hex.DecodeString(page.Authority)
	if err != nil || len(authority) != 32 || page.Scope == "" || len(page.Scope) > 64 || page.Through < 0 || page.Kind != "snapshot" && page.Kind != "changes" || page.Entries == nil || len(page.Entries) > r.Limit || page.HasMore && len(page.Entries) == 0 || len(page.Cursor) == 0 || len(page.Cursor) > protocol.MaxSyncCursorBytes {
		return protocol.SyncPage{}, errors.New("questlock: invalid sync response envelope")
	}
	var previous protocol.SyncEntry
	for i, item := range page.Entries {
		a := item.Artifact
		if item.Sequence < 1 || item.Sequence > page.Through || a.Scope != page.Scope || a.Version < 1 || a.Key == "" || len(a.Key) > 512 || len(a.Content) > protocol.MaxContentBytes || a.UpdatedAt == "" || i > 0 && (page.Kind == "snapshot" && a.Key <= previous.Artifact.Key || page.Kind == "changes" && item.Sequence <= previous.Sequence) {
			return protocol.SyncPage{}, errors.New("questlock: invalid sync response entry")
		}
		previous = item
	}
	return page, nil
}
