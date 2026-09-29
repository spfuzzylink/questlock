// Package store provides the single-host storage and authorization boundary.
// Every publication is one SQLite transaction, including its immutable content,
// current pointer, retry receipt, and audit record.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spfuzzylink/agent-fence/protocol"
	_ "modernc.org/sqlite"
)

// Error is an expected application error, safe to return to an API client.
type Error struct {
	Code           string
	Message        string
	CurrentVersion int64
}

func (e *Error) Error() string { return e.Message }

// Principal is established by Authenticate. Store operations verify it again,
// so constructing a Principal alone does not grant access.
type Principal struct {
	AgentID   string
	Scope     string
	TokenHash string
}

type Store struct{ db *sql.DB }

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

const schema = `
CREATE TABLE IF NOT EXISTS agents (
    agent_id TEXT PRIMARY KEY,
    scope TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    active INTEGER NOT NULL CHECK (active IN (0,1)),
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS artifact_versions (
    scope TEXT NOT NULL,
    key TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    content TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (scope, key, version)
);
CREATE TABLE IF NOT EXISTS artifacts (
    scope TEXT NOT NULL,
    key TEXT NOT NULL,
    current_version INTEGER NOT NULL,
    PRIMARY KEY (scope, key),
    FOREIGN KEY (scope, key, current_version)
        REFERENCES artifact_versions(scope, key, version)
);
CREATE TABLE IF NOT EXISTS operations (
    agent_id TEXT NOT NULL REFERENCES agents(agent_id),
    operation_id TEXT NOT NULL,
    scope TEXT NOT NULL,
    key TEXT NOT NULL,
    expected_version INTEGER NOT NULL,
    version INTEGER NOT NULL,
    PRIMARY KEY (agent_id, operation_id),
    FOREIGN KEY (scope, key, version)
        REFERENCES artifact_versions(scope, key, version)
);
CREATE TABLE IF NOT EXISTS audit (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    scope TEXT NOT NULL,
    key TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    outcome TEXT NOT NULL,
    expected_version INTEGER NOT NULL,
    current_version INTEGER NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_scope_id ON audit(scope, id DESC);
`

// Open creates or opens a local SQLite database. New directories are private;
// existing parent directories retain their permissions. SQLite must reside on
// a local filesystem, not a shared network volume.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, invalid("database path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	f, err := os.OpenFile(abs, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		err = f.Close()
	} else if errors.Is(err, os.ErrExist) {
		var info os.FileInfo
		info, err = os.Lstat(abs)
		if err == nil && !info.Mode().IsRegular() {
			return nil, invalid("database path must be a regular file, not a symlink")
		}
	}
	if err != nil {
		return nil, fmt.Errorf("prepare database file: %w", err)
	}
	if err = os.Chmod(abs, 0600); err != nil {
		return nil, fmt.Errorf("protect database file: %w", err)
	}
	dsn := url.URL{Scheme: "file", Path: abs}
	q := dsn.Query()
	// DSN pragmas apply to every new connection, including reconnects. The
	// driver implements _txlock=immediate as BEGIN IMMEDIATE, so writers acquire
	// their lock before reading the current version or retry receipt.
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Set("_txlock", "immediate")
	dsn.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	fail := func(err error) (*Store, error) {
		_ = db.Close()
		return nil, err
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return fail(fmt.Errorf("initialize database: %w", err))
	}
	defer tx.Rollback()
	if _, err = tx.Exec(schema); err != nil {
		_ = tx.Rollback()
		return fail(fmt.Errorf("initialize schema: %w", err))
	}
	if err = tx.Commit(); err != nil {
		return fail(fmt.Errorf("commit schema: %w", err))
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// CreateToken creates a new immutable agent identity. The plaintext token is
// returned once; only its SHA-256 digest is persisted. Revoked agent IDs cannot
// be recycled, preserving the meaning of audit and idempotency records.
func (s *Store) CreateToken(ctx context.Context, agentID, scope string) (string, error) {
	if !identifier.MatchString(agentID) || !identifier.MatchString(scope) {
		return "", invalid("agent ID and scope must be 1–64 ASCII letters, digits, dots, underscores, or hyphens, beginning with a letter or digit")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	token := "af_" + base64.RawURLEncoding.EncodeToString(secret)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var exists int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM agents WHERE agent_id = ?", agentID).Scan(&exists)
	if err == nil {
		return "", &Error{Code: "agent_exists", Message: "agent ID already exists; use a new identity"}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO agents(agent_id, scope, token_hash, active, created_at) VALUES (?, ?, ?, 1, ?)", agentID, scope, tokenHash(token), timestamp())
	if err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Store) RevokeAgent(ctx context.Context, agentID string) error {
	if !identifier.MatchString(agentID) {
		return invalid("invalid agent ID")
	}
	result, err := s.db.ExecContext(ctx, "UPDATE agents SET active = 0 WHERE agent_id = ?", agentID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return &Error{Code: "not_found", Message: "agent not found"}
	}
	return nil
}

func (s *Store) Authenticate(ctx context.Context, token string) (Principal, error) {
	var p Principal
	if len(token) != 46 || !strings.HasPrefix(token, "af_") {
		return p, unauthorized()
	}
	err := s.db.QueryRowContext(ctx, "SELECT agent_id, scope, token_hash FROM agents WHERE token_hash = ? AND active = 1", tokenHash(token)).Scan(&p.AgentID, &p.Scope, &p.TokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, unauthorized()
	}
	return p, err
}

// Publish applies compare-and-swap within the authenticated scope. A successful
// operation ID is permanently bound to the complete original request. Retries
// return that original version even after newer versions have been published.
func (s *Store) Publish(ctx context.Context, p Principal, r protocol.PublishRequest) (protocol.PublishResult, error) {
	var result protocol.PublishResult
	if err := validateRequest(r); err != nil {
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = checkActive(ctx, tx, p); err != nil {
		return result, err
	}
	var original protocol.Artifact
	var originalExpected int64
	err = tx.QueryRowContext(ctx, `SELECT v.scope, v.key, v.version, v.content, v.updated_at, o.expected_version
		FROM operations o JOIN artifact_versions v
		ON v.scope = o.scope AND v.key = o.key AND v.version = o.version
		WHERE o.agent_id = ? AND o.operation_id = ?`, p.AgentID, r.OperationID).
		Scan(&original.Scope, &original.Key, &original.Version, &original.Content, &original.UpdatedAt, &originalExpected)
	if err == nil {
		if original.Scope != p.Scope || original.Key != r.Key || originalExpected != r.ExpectedVersion || original.Content != r.Content {
			current, currentErr := currentVersion(ctx, tx, p.Scope, r.Key)
			if currentErr != nil {
				return result, currentErr
			}
			return result, reject(ctx, tx, p, r, "operation_conflict", "operation ID was already used with a different request", current)
		}
		current, currentErr := currentVersion(ctx, tx, p.Scope, r.Key)
		if currentErr != nil {
			return result, currentErr
		}
		if err = record(ctx, tx, p, r, "replayed", current); err != nil {
			return result, err
		}
		if err = tx.Commit(); err != nil {
			return result, err
		}
		return protocol.PublishResult{Artifact: original, Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	current, err := currentVersion(ctx, tx, p.Scope, r.Key)
	if err != nil {
		return result, err
	}
	if current != r.ExpectedVersion {
		return result, reject(ctx, tx, p, r, "version_conflict", "expected version does not match current version", current)
	}
	if current == math.MaxInt64 {
		return result, reject(ctx, tx, p, r, "invalid", "version limit reached", current)
	}
	a := protocol.Artifact{Scope: p.Scope, Key: r.Key, Version: current + 1, Content: r.Content, UpdatedAt: timestamp()}
	if _, err = tx.ExecContext(ctx, "INSERT INTO artifact_versions(scope, key, version, content, updated_at) VALUES (?, ?, ?, ?, ?)", a.Scope, a.Key, a.Version, a.Content, a.UpdatedAt); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO artifacts(scope, key, current_version) VALUES (?, ?, ?)
		ON CONFLICT(scope, key) DO UPDATE SET current_version = excluded.current_version`, a.Scope, a.Key, a.Version); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(agent_id, operation_id, scope, key, expected_version, version) VALUES (?, ?, ?, ?, ?, ?)", p.AgentID, r.OperationID, p.Scope, r.Key, r.ExpectedVersion, a.Version); err != nil {
		return result, err
	}
	if err = record(ctx, tx, p, r, "accepted", a.Version); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return protocol.PublishResult{Artifact: a}, nil
}

func (s *Store) Get(ctx context.Context, p Principal, key string) (protocol.Artifact, error) {
	var a protocol.Artifact
	if err := validateKey(key); err != nil {
		return a, err
	}
	if err := checkActive(ctx, s.db, p); err != nil {
		return a, err
	}
	err := s.db.QueryRowContext(ctx, `SELECT v.scope, v.key, v.version, v.content, v.updated_at
		FROM artifacts a JOIN artifact_versions v
		ON v.scope = a.scope AND v.key = a.key AND v.version = a.current_version
		JOIN agents g ON g.scope = a.scope AND g.agent_id = ? AND g.token_hash = ? AND g.active = 1
		WHERE a.scope = ? AND a.key = ?`, p.AgentID, p.TokenHash, p.Scope, key).
		Scan(&a.Scope, &a.Key, &a.Version, &a.Content, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, &Error{Code: "not_found", Message: "artifact not found"}
	}
	return a, err
}

// List returns metadata for at most the first 1000 current artifacts ordered by
// key. Content is deliberately omitted to keep list responses bounded; Get
// fetches an individual artifact's content.
func (s *Store) List(ctx context.Context, p Principal) ([]protocol.Artifact, error) {
	if err := checkActive(ctx, s.db, p); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT v.scope, v.key, v.version, v.updated_at
		FROM artifacts a JOIN artifact_versions v
		ON v.scope = a.scope AND v.key = a.key AND v.version = a.current_version
		JOIN agents g ON g.scope = a.scope AND g.agent_id = ? AND g.token_hash = ? AND g.active = 1
		WHERE a.scope = ? ORDER BY a.key LIMIT 1000`, p.AgentID, p.TokenHash, p.Scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]protocol.Artifact, 0)
	for rows.Next() {
		var a protocol.Artifact
		if err = rows.Scan(&a.Scope, &a.Key, &a.Version, &a.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return items, rows.Err()
}

// Audit returns at most 1000 events in the principal's scope, newest first.
// Nonpositive limits use the default of 100.
func (s *Store) Audit(ctx context.Context, p Principal, limit int) ([]protocol.AuditEvent, error) {
	if err := checkActive(ctx, s.db, p); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.agent_id, a.scope, a.key, a.operation_id,
		a.outcome, a.expected_version, a.current_version, a.created_at
		FROM audit a JOIN agents g ON g.scope = a.scope AND g.agent_id = ? AND g.token_hash = ? AND g.active = 1
		WHERE a.scope = ? ORDER BY a.id DESC LIMIT ?`, p.AgentID, p.TokenHash, p.Scope, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]protocol.AuditEvent, 0)
	for rows.Next() {
		var e protocol.AuditEvent
		if err = rows.Scan(&e.ID, &e.AgentID, &e.Scope, &e.Key, &e.OperationID, &e.Outcome, &e.ExpectedVersion, &e.CurrentVersion, &e.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, e)
	}
	return items, rows.Err()
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func checkActive(ctx context.Context, q rowQuerier, p Principal) error {
	var active int
	err := q.QueryRowContext(ctx, "SELECT 1 FROM agents WHERE agent_id = ? AND scope = ? AND token_hash = ? AND active = 1", p.AgentID, p.Scope, p.TokenHash).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return unauthorized()
	}
	return err
}

func currentVersion(ctx context.Context, q rowQuerier, scope, key string) (int64, error) {
	var current int64
	err := q.QueryRowContext(ctx, "SELECT current_version FROM artifacts WHERE scope = ? AND key = ?", scope, key).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return current, err
}

func record(ctx context.Context, tx *sql.Tx, p Principal, r protocol.PublishRequest, outcome string, current int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO audit(agent_id, scope, key, operation_id, outcome, expected_version, current_version, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, p.AgentID, p.Scope, r.Key, r.OperationID, outcome, r.ExpectedVersion, current, timestamp())
	return err
}

func reject(ctx context.Context, tx *sql.Tx, p Principal, r protocol.PublishRequest, code, message string, current int64) error {
	if err := record(ctx, tx, p, r, code, current); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return &Error{Code: code, Message: message, CurrentVersion: current}
}

func validateRequest(r protocol.PublishRequest) error {
	if err := validateKey(r.Key); err != nil {
		return err
	}
	if len(r.OperationID) == 0 || len(r.OperationID) > 128 || !utf8.ValidString(r.OperationID) || strings.TrimSpace(r.OperationID) == "" {
		return invalid("operation ID must contain 1–128 bytes of valid, nonblank UTF-8")
	}
	for _, c := range r.OperationID {
		if unicode.IsControl(c) {
			return invalid("operation ID must not contain control characters")
		}
	}
	if r.ExpectedVersion < 0 || r.ExpectedVersion == math.MaxInt64 {
		return invalid("expected version must be between 0 and 9223372036854775806")
	}
	if len(r.Content) > protocol.MaxContentBytes {
		return invalid("content exceeds the 1 MiB limit")
	}
	if !utf8.ValidString(r.Content) {
		return invalid("content must be valid UTF-8")
	}
	return nil
}

func validateKey(key string) error {
	if len(key) == 0 || len(key) > 512 || !utf8.ValidString(key) || strings.Contains(key, "\\") {
		return invalid("key must contain 1–512 bytes of valid UTF-8 without backslashes")
	}
	for _, c := range key {
		if unicode.IsControl(c) {
			return invalid("key must not contain control characters")
		}
	}
	for _, component := range strings.Split(key, "/") {
		if component == "" || component == "." || component == ".." || strings.TrimSpace(component) == "" {
			return invalid("key must be a relative logical path without empty, dot, or parent components")
		}
	}
	return nil
}

func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func invalid(message string) error { return &Error{Code: "invalid", Message: message} }
func unauthorized() error {
	return &Error{Code: "unauthorized", Message: "invalid or revoked agent token"}
}
