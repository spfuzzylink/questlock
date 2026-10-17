package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/spfuzzylink/questlock/protocol"
)

func (c *Client) Identity(ctx context.Context) (protocol.Identity, error) {
	var raw json.RawMessage
	var i protocol.Identity
	if err := c.doMax(ctx, http.MethodGet, "/v1/identity", nil, nil, &raw, 4096); err != nil {
		return i, err
	}
	if protocol.DecodeSyncJSON(raw, &i) != nil || i.Validate() != nil {
		return protocol.Identity{}, errors.New("questlock: invalid authenticated identity")
	}
	return i, nil
}

// PublishPinned preserves Publish's exact request/receipt semantics and adds
// an identity precondition checked in the authoritative SQLite transaction.
// Its dedicated route makes older brokers reject it before any publication.
func (c *Client) PublishPinned(ctx context.Context, expected protocol.Identity, r protocol.PublishRequest) (protocol.PublishResult, error) {
	var result protocol.PublishResult
	if err := expected.Validate(); err != nil {
		return result, err
	}
	if !utf8.ValidString(r.Key) || !utf8.ValidString(r.OperationID) || !utf8.ValidString(r.Content) {
		return result, errors.New("questlock: publish strings must be valid UTF-8")
	}
	var raw json.RawMessage
	err := c.doHeaders(ctx, http.MethodPost, "/v1/artifact/pinned", nil, r, &raw, MaxResponseBytes, map[string]string{protocol.ExpectedAuthorityHeader: expected.Authority, protocol.ExpectedScopeHeader: expected.Scope, protocol.ExpectedAgentHeader: expected.AgentID})
	if err != nil {
		return result, err
	}
	var fields map[string]json.RawMessage
	var artifactFields map[string]json.RawMessage
	if protocol.DecodeSyncJSON(raw, &result) != nil || json.Unmarshal(raw, &fields) != nil || len(fields) != 2 || json.Unmarshal(fields["artifact"], &artifactFields) != nil || len(artifactFields) != 5 || bytes.Equal(bytes.TrimSpace(fields["replayed"]), []byte("null")) {
		return protocol.PublishResult{}, errors.New("questlock: invalid pinned publication receipt")
	}
	for _, v := range artifactFields {
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return protocol.PublishResult{}, errors.New("questlock: null pinned artifact field")
		}
	}
	a := result.Artifact
	_, timeErr := time.Parse(time.RFC3339Nano, a.UpdatedAt)
	if a.Scope != expected.Scope || a.Key != r.Key || a.Content != r.Content || a.Version < 1 || a.Version != r.ExpectedVersion+1 || len(a.UpdatedAt) > 64 || timeErr != nil {
		return protocol.PublishResult{}, errors.New("questlock: invalid pinned publication receipt")
	}
	return result, nil
}

// A malformed negative envelope must not classify uncertain intent as a known
// rejection. Unpinned Publish keeps its historical response decoding behavior.
func decodePinnedError(data []byte, body *protocol.ErrorBody) error {
	if err := protocol.DecodeSyncJSON(data, body); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if len(fields["code"]) == 0 || len(fields["message"]) == 0 {
		return errors.New("incomplete pinned error")
	}
	for _, raw := range fields {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return errors.New("null pinned error field")
		}
	}
	return nil
}
