package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	MaxSyncItems        = 128
	MaxSyncBytes        = 8 << 20
	MinSyncBytes        = 4096
	MaxSyncCursorBytes  = 2048
	MaxSyncRequestBytes = 4096
)

// SyncRequest requests read-only pages in the token's scope. Zero limits select
// defaults. Cursor is opaque; persist it without editing. No writes are accepted.
type SyncRequest struct {
	Cursor   string `json:"cursor,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	MaxBytes int    `json:"max_bytes,omitempty"`
}

func (r SyncRequest) Bounds() (SyncRequest, error) {
	if r.Limit == 0 {
		r.Limit = 32
	}
	if r.MaxBytes == 0 {
		r.MaxBytes = MaxSyncBytes
	}
	if len(r.Cursor) > MaxSyncCursorBytes || r.Limit < 1 || r.Limit > MaxSyncItems || r.MaxBytes < MinSyncBytes || r.MaxBytes > MaxSyncBytes {
		return r, errors.New("invalid sync cursor or page limits")
	}
	return r, nil
}

// SyncEntry contains raw immutable artifact content. Sequence is an accepted
// publication ID, never a client-writable version or scope-wide CAS version.
type SyncEntry struct {
	Sequence int64    `json:"sequence"`
	Artifact Artifact `json:"artifact"`
}
type SyncPage struct {
	Authority string      `json:"authority"`
	Scope     string      `json:"scope"`
	Kind      string      `json:"kind"`
	Through   int64       `json:"through"`
	Entries   []SyncEntry `json:"entries"`
	Cursor    string      `json:"cursor"`
	HasMore   bool        `json:"has_more"`
}

// DecodeSyncRequest rejects unknown, duplicate, null and trailing fields.
func DecodeSyncRequest(data []byte) (SyncRequest, error) {
	var r SyncRequest
	if len(data) > MaxSyncRequestBytes {
		return r, errors.New("sync request exceeds size limit")
	}
	var fields map[string]json.RawMessage
	if err := DecodeSyncJSON(data, &fields); err != nil {
		return r, err
	}
	for name, raw := range fields {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return r, errors.New("null sync field")
		}
		var err error
		switch name {
		case "cursor":
			err = json.Unmarshal(raw, &r.Cursor)
		case "limit":
			err = json.Unmarshal(raw, &r.Limit)
			if r.Limit == 0 {
				return r, errors.New("limit must be positive")
			}
		case "max_bytes":
			err = json.Unmarshal(raw, &r.MaxBytes)
			if r.MaxBytes == 0 {
				return r, errors.New("max_bytes must be positive")
			}
		default:
			return r, errors.New("unknown sync field")
		}
		if err != nil {
			return r, errors.New("invalid sync field type")
		}
	}
	return r.Bounds()
}

// DecodeSyncJSON strictly decodes a bounded object; callers set the byte limit.
// Nested duplicate members, malformed UTF-8, unknown struct fields, trailing
// values and excessive nesting are rejected.
func DecodeSyncJSON(data []byte, value any) error {
	if !utf8.Valid(data) {
		return errors.New("sync JSON must be valid UTF-8")
	}
	if !validSyncEscapes(data) {
		return errors.New("sync JSON contains an unpaired Unicode surrogate")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("sync JSON must be one object")
	}
	if err = walkSyncJSON(d, token, 0); err != nil {
		return err
	}
	if _, err = d.Token(); err != io.EOF {
		return errors.New("trailing sync JSON")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(value)
}

func validSyncEscapes(data []byte) bool {
	for i := 0; i+1 < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if data[i] != 'u' || i+4 >= len(data) {
			continue
		}
		n, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
func walkSyncJSON(d *json.Decoder, token json.Token, depth int) error {
	if depth > 16 {
		return errors.New("sync JSON nesting exceeds limit")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	names := map[string]bool{}
	items := 0
	for d.More() {
		items++
		if delim == '[' && items > MaxSyncItems || delim == '{' && items > 32 {
			return errors.New("sync JSON container exceeds item limit")
		}
		if delim == '{' {
			name, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := name.(string)
			if !ok || key != strings.ToLower(key) || names[key] {
				return errors.New("duplicate sync JSON field")
			}
			names[key] = true
		}
		next, err := d.Token()
		if err != nil {
			return err
		}
		if err = walkSyncJSON(d, next, depth+1); err != nil {
			return err
		}
	}
	end, err := d.Token()
	if err != nil || delim == '{' && end != json.Delim('}') || delim == '[' && end != json.Delim(']') {
		return errors.New("invalid sync JSON structure")
	}
	return nil
}

// DecodeSyncPage requires every canonical response field, including false,
// zero, and intentionally empty content. Omission/null is never a default.
// Semantic scope, sequence, and requested-limit checks remain with the client.
func DecodeSyncPage(data []byte) (SyncPage, error) {
	var page SyncPage
	if len(data) > MaxSyncBytes || DecodeSyncJSON(data, &page) != nil {
		return SyncPage{}, errors.New("invalid sync page JSON")
	}
	envelope, err := requiredSyncFields(data, "authority", "scope", "kind", "through", "entries", "cursor", "has_more")
	if err != nil {
		return SyncPage{}, err
	}
	var entries []json.RawMessage
	if err = json.Unmarshal(envelope["entries"], &entries); err != nil {
		return SyncPage{}, err
	}
	for _, raw := range entries {
		entry, err := requiredSyncFields(raw, "sequence", "artifact")
		if err != nil {
			return SyncPage{}, err
		}
		if _, err = requiredSyncFields(entry["artifact"], "scope", "key", "version", "content", "updated_at"); err != nil {
			return SyncPage{}, err
		}
	}
	return page, nil
}

func requiredSyncFields(data []byte, names ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if len(fields) != len(names) {
		return nil, errors.New("incomplete sync response object")
	}
	for _, name := range names {
		raw, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, errors.New("missing or null sync response field")
		}
	}
	return fields, nil
}
