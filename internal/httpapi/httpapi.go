// Package httpapi exposes the storage service through a small authenticated JSON API.
package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/protocol"
)

// MaxRequestBytes includes space for JSON's worst-case escaping and metadata.
const MaxRequestBytes int64 = 6*protocol.MaxContentBytes + 64*1024

// New returns an HTTP handler. The caller owns the store's lifetime.
func New(s *store.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		var allowed string
		switch r.URL.Path {
		case "/healthz", "/v1/artifacts", "/v1/audit", "/v1/identity":
			allowed = "GET"
		case "/v1/artifact":
			allowed = "GET, POST"
		case "/v1/sync", "/v1/artifact/pinned":
			allowed = "POST"
		default:
			writeError(w, http.StatusNotFound, "not_found", "route not found", 0)
			return
		}
		if r.Method != allowed && !(allowed == "GET, POST" && (r.Method == http.MethodGet || r.Method == http.MethodPost)) {
			w.Header().Set("Allow", allowed)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", 0)
			return
		}
		if r.URL.Path == "/healthz" {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		}

		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			unauthorized(w)
			return
		}
		principal, err := s.Authenticate(r.Context(), parts[1])
		if err != nil {
			writeStoreError(w, err)
			return
		}
		switch r.URL.Path {
		case "/v1/identity":
			if r.URL.RawQuery != "" {
				writeError(w, http.StatusBadRequest, "invalid", "identity accepts no query fields", 0)
				return
			}
			i, err := s.Identity(r.Context(), principal)
			if err != nil {
				writeStoreError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, i)
		case "/v1/sync":
			if r.URL.RawQuery != "" {
				writeError(w, http.StatusBadRequest, "invalid", "sync accepts JSON fields only", 0)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, protocol.MaxSyncRequestBytes)
			body, err := io.ReadAll(r.Body)
			if err != nil {
				writeDecodeError(w, err)
				return
			}
			input, err := protocol.DecodeSyncRequest(body)
			if err != nil {
				writeDecodeError(w, err)
				return
			}
			page, err := s.Sync(r.Context(), principal, input)
			if err != nil {
				writeStoreError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, page)
		case "/v1/artifact", "/v1/artifact/pinned":
			if r.URL.Path == "/v1/artifact/pinned" && r.URL.RawQuery != "" {
				writeError(w, http.StatusBadRequest, "invalid", "pinned publication accepts no query fields", 0)
				return
			}
			if r.Method == http.MethodGet {
				artifact, err := s.Get(r.Context(), principal, r.URL.Query().Get("key"))
				if err != nil {
					writeStoreError(w, err)
					return
				}
				writeJSON(w, http.StatusOK, artifact)
				return
			}
			// A pointer distinguishes version zero (create) from omission or null.
			var input struct {
				Key             string  `json:"key"`
				OperationID     string  `json:"operation_id"`
				ExpectedVersion *int64  `json:"expected_version"`
				Content         *string `json:"content"`
			}
			r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
			body, err := io.ReadAll(r.Body)
			if err != nil {
				writeDecodeError(w, err)
				return
			}
			// encoding/json replaces malformed UTF-8 rather than rejecting it.
			// Validate the bounded raw body before decoding to avoid changing data.
			if !utf8.Valid(body) {
				writeError(w, http.StatusBadRequest, "invalid", "body must contain valid UTF-8", 0)
				return
			}
			if r.URL.Path == "/v1/artifact/pinned" && protocol.DecodeSyncJSON(body, &input) != nil {
				writeError(w, http.StatusBadRequest, "invalid", "invalid pinned publication JSON", 0)
				return
			}
			decoder := json.NewDecoder(bytes.NewReader(body))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				writeDecodeError(w, err)
				return
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				writeDecodeError(w, err)
				return
			}
			if input.ExpectedVersion == nil {
				writeError(w, http.StatusBadRequest, "invalid", "expected_version is required and must be an integer", 0)
				return
			}
			if input.Content == nil {
				writeError(w, http.StatusBadRequest, "invalid", "content is required and must be a string (empty is allowed)", 0)
				return
			}
			publication := protocol.PublishRequest{
				Key: input.Key, OperationID: input.OperationID,
				ExpectedVersion: *input.ExpectedVersion, Content: *input.Content,
			}
			var result protocol.PublishResult
			names := []string{protocol.ExpectedAuthorityHeader, protocol.ExpectedScopeHeader, protocol.ExpectedAgentHeader}
			// The dedicated route requires pins even when all headers are
			// absent. Older brokers do not recognize this route and cannot
			// silently accept an unpinned publication before rejecting it.
			pinned := r.URL.Path == "/v1/artifact/pinned"
			for _, name := range names {
				if _, ok := r.Header[http.CanonicalHeaderKey(name)]; ok {
					pinned = true
				}
			}
			if pinned {
				for _, name := range names {
					values := r.Header.Values(name)
					if len(values) != 1 || values[0] == "" {
						writeError(w, http.StatusBadRequest, "invalid", "complete unique expected identity headers required", 0)
						return
					}
				}
				expected := protocol.Identity{Authority: r.Header.Get(names[0]), Scope: r.Header.Get(names[1]), AgentID: r.Header.Get(names[2])}
				result, err = s.PublishPinned(r.Context(), principal, publication, expected)
			} else {
				result, err = s.Publish(r.Context(), principal, publication)
			}
			if err != nil {
				writeStoreError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, result)
		case "/v1/artifacts":
			artifacts, err := s.List(r.Context(), principal)
			if err != nil {
				writeStoreError(w, err)
				return
			}
			if artifacts == nil {
				artifacts = []protocol.Artifact{}
			}
			writeJSON(w, http.StatusOK, artifacts)
		case "/v1/audit":
			limit := 100
			if raw, exists := r.URL.Query()["limit"]; exists {
				if len(raw) != 1 {
					writeError(w, http.StatusBadRequest, "invalid", "limit must be an integer between 1 and 1000", 0)
					return
				}
				limit, err = strconv.Atoi(raw[0])
				if err != nil || limit < 1 || limit > 1000 {
					writeError(w, http.StatusBadRequest, "invalid", "limit must be an integer between 1 and 1000", 0)
					return
				}
			}
			events, err := s.Audit(r.Context(), principal, limit)
			if err != nil {
				writeStoreError(w, err)
				return
			}
			if events == nil {
				events = []protocol.AuditEvent{}
			}
			writeJSON(w, http.StatusOK, events)
		}
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, "unauthorized", "valid bearer token required", 0)
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "request body exceeds the size limit", 0)
		return
	}
	writeError(w, http.StatusBadRequest, "invalid", "body must contain exactly one valid JSON object with known fields", 0)
}

func writeStoreError(w http.ResponseWriter, err error) {
	var apiError *store.Error
	if errors.As(err, &apiError) {
		var status int
		switch apiError.Code {
		case "unauthorized":
			unauthorized(w)
			return
		case "invalid":
			status = http.StatusBadRequest
		case "not_found":
			status = http.StatusNotFound
		case "archive_unavailable":
			status = http.StatusServiceUnavailable
		case "archive_corrupt":
			status = http.StatusInternalServerError
		case "storage_limit":
			status = http.StatusInsufficientStorage
		case "sync_item_too_large":
			status = http.StatusRequestEntityTooLarge
		case "version_conflict", "operation_conflict", "agent_exists", "identity_conflict":
			status = http.StatusConflict
		default:
			writeError(w, http.StatusInternalServerError, "internal", "internal server error", 0)
			return
		}
		writeError(w, status, apiError.Code, apiError.Message, apiError.CurrentVersion)
		return
	}
	// Database and filesystem errors may contain private paths or other internals.
	writeError(w, http.StatusInternalServerError, "internal", "internal server error", 0)
}

func writeError(w http.ResponseWriter, status int, code, message string, currentVersion int64) {
	writeJSON(w, status, protocol.ErrorBody{Code: code, Message: message, CurrentVersion: currentVersion})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
