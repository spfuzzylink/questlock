// Package edge is a bounded durable outbox and read cache for one authoritative
// Questlock broker. It never accepts peer journals or invents upstream versions.
package edge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/protocol"
	_ "modernc.org/sqlite"
)

const (
	MaxIntents     = 128
	MaxOutboxBytes = 64 << 20 // reservations include both request and eventual receipt
	MaxCacheItems  = 4096
	MaxCacheBytes  = 32 << 20 // independently bounds visible and staging snapshots
	MaxFileBytes   = 256 << 20
)

var (
	ErrBusy      = errors.New("edge is already open or has an active operation")
	ErrIdentity  = errors.New("upstream authority, scope or agent differs from the pinned identity")
	ErrUnbound   = errors.New("bind the edge to an authenticated upstream identity first")
	ErrUncertain = errors.New("publish is uncertain; retain and explicitly retry the exact intent")
	ErrBlocked   = errors.New("intent is blocked; explicitly resolve it without changing its request")
	ErrLimit     = errors.New("edge storage quota exceeded; checkpoint and request are unchanged")
	ErrNotReady  = errors.New("no complete authoritative snapshot is available")
)

type Store struct {
	db   *sql.DB
	lock *os.File
	mu   sync.Mutex
}
type Intent struct {
	Request        protocol.PublishRequest `json:"request"`
	State          string                  `json:"state"`
	Receipt        *protocol.PublishResult `json:"receipt,omitempty"`
	Code           string                  `json:"code,omitempty"`
	CurrentVersion int64                   `json:"current_version,omitempty"`
}
type Status struct {
	Identity            protocol.Identity `json:"identity"`
	SnapshotComplete    bool              `json:"snapshot_complete"`
	SnapshotInProgress  bool              `json:"snapshot_in_progress"`
	Through             int64             `json:"through"`
	CacheItems          int               `json:"cache_items"`
	PendingBytes        int64             `json:"pending_bytes"` // encoded requests, not resident memory
	OutboxReservedBytes int64             `json:"outbox_reserved_bytes"`
	Intents             map[string]int    `json:"intents"`
}
type PullOptions struct{ Limit, MaxBytes int }
type PullResult struct {
	Kind    string `json:"kind"`
	Applied int    `json:"applied"`
	HasMore bool   `json:"has_more"`
	Through int64  `json:"through"`
}

const schema = `
CREATE TABLE IF NOT EXISTS binding(id INTEGER PRIMARY KEY CHECK(id=1), identity TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS outbox(operation_id TEXT PRIMARY KEY, request TEXT NOT NULL, digest TEXT NOT NULL, reservation INTEGER NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('queued','uncertain','blocked','accepted','abandoned')), receipt TEXT NOT NULL DEFAULT '', code TEXT NOT NULL DEFAULT '', current_version INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS cache(kind TEXT NOT NULL CHECK(kind IN ('visible','staging')), key TEXT NOT NULL, version INTEGER NOT NULL, sequence INTEGER NOT NULL, artifact TEXT NOT NULL, PRIMARY KEY(kind,key));
CREATE TABLE IF NOT EXISTS pull_state(id INTEGER PRIMARY KEY CHECK(id=1),cursor TEXT NOT NULL DEFAULT '',phase TEXT NOT NULL DEFAULT 'snapshot',through INTEGER NOT NULL DEFAULT -1,last_seq INTEGER NOT NULL DEFAULT 0,last_key TEXT NOT NULL DEFAULT '',complete INTEGER NOT NULL DEFAULT 0,visible_through INTEGER NOT NULL DEFAULT 0);
INSERT OR IGNORE INTO pull_state(id) VALUES(1);
`

// Open owns one private edge directory for its lifetime. The directory is not
// the upstream server database and must never be shared by independent edges.
func Open(directory string) (*Store, error) {
	lock, path, err := openPrivate(directory)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Store, error) { lock.Close(); return nil, err }
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	for _, p := range []string{"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(FULL)", "foreign_keys(ON)", "wal_autocheckpoint(64)", "journal_size_limit(8388608)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return fail(err)
	}
	bad := func(err error) (*Store, error) { db.Close(); return fail(err) }
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var size int
	if err = db.QueryRow("PRAGMA page_size").Scan(&size); err != nil {
		return bad(err)
	}
	if size < 512 || size > 65536 {
		return bad(errors.New("invalid edge database page size"))
	}
	if _, err = db.Exec("PRAGMA max_page_count=" + strconv.Itoa(MaxFileBytes/size)); err != nil {
		return bad(err)
	}
	if _, err = db.Exec(schema); err != nil {
		return bad(err)
	}
	if err = checkPrivateFiles(directory); err != nil {
		return bad(err)
	}
	if err = syncDirectory(directory); err != nil {
		return bad(err)
	}
	return &Store{db: db, lock: lock}, nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return errors.Join(s.db.Close(), s.lock.Close())
}
func (s *Store) enter() error {
	if !s.mu.TryLock() {
		return ErrBusy
	}
	return nil
}
func hash(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

func (s *Store) identity(ctx context.Context) (protocol.Identity, error) {
	var raw string
	var i protocol.Identity
	err := s.db.QueryRowContext(ctx, "SELECT identity FROM binding WHERE id=1").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return i, ErrUnbound
	}
	if err != nil {
		return i, err
	}
	if protocol.DecodeSyncJSON([]byte(raw), &i) != nil || i.Validate() != nil {
		return i, errors.New("invalid edge identity state")
	}
	return i, nil
}

// Bind explicitly trusts the first authenticated identity and persists it. A
// later bind can only confirm the same authority, scope and receipt agent.
func (s *Store) Bind(ctx context.Context, c *client.Client) (protocol.Identity, error) {
	if err := s.enter(); err != nil {
		return protocol.Identity{}, err
	}
	defer s.mu.Unlock()
	i, err := c.Identity(ctx)
	if err != nil {
		return i, err
	}
	prior, err := s.identity(ctx)
	if err == nil {
		if prior != i {
			return i, ErrIdentity
		}
		return i, nil
	}
	if !errors.Is(err, ErrUnbound) {
		return i, err
	}
	raw, _ := json.Marshal(i)
	_, err = s.db.ExecContext(ctx, "INSERT INTO binding(id,identity) VALUES(1,?)", string(raw))
	return i, err
}

func validateIntent(r protocol.PublishRequest) error {
	if !utf8.ValidString(r.Key) || len(r.Key) < 1 || len(r.Key) > 512 || strings.Contains(r.Key, "\\") || !utf8.ValidString(r.OperationID) || len(r.OperationID) < 1 || len(r.OperationID) > 128 || strings.TrimSpace(r.OperationID) == "" || !utf8.ValidString(r.Content) || len(r.Content) > protocol.MaxContentBytes || r.ExpectedVersion < 0 || r.ExpectedVersion == math.MaxInt64 {
		return errors.New("invalid bounded publish intent")
	}
	for _, v := range []string{r.Key, r.OperationID} {
		for _, ch := range v {
			if unicode.IsControl(ch) {
				return errors.New("control character in intent key or operation ID")
			}
		}
	}
	for _, part := range strings.Split(r.Key, "/") {
		if part == "" || part == "." || part == ".." || strings.TrimSpace(part) == "" {
			return errors.New("invalid intent key")
		}
	}
	return nil
}
func (s *Store) intent(ctx context.Context, id string) (Intent, error) {
	var v Intent
	var raw, digest, receipt string
	err := s.db.QueryRowContext(ctx, "SELECT request,digest,state,receipt,code,current_version FROM outbox WHERE operation_id=?", id).Scan(&raw, &digest, &v.State, &receipt, &v.Code, &v.CurrentVersion)
	if err != nil {
		return v, err
	}
	if len(raw) > 6*protocol.MaxContentBytes+65536 || hash([]byte(raw)) != digest || protocol.DecodeSyncJSON([]byte(raw), &v.Request) != nil || v.Request.OperationID != id || validateIntent(v.Request) != nil {
		return Intent{}, errors.New("invalid immutable intent state")
	}
	if receipt != "" {
		var r protocol.PublishResult
		if protocol.DecodeSyncJSON([]byte(receipt), &r) != nil {
			return Intent{}, errors.New("invalid saved receipt")
		}
		v.Receipt = &r
	}
	return v, nil
}
func (s *Store) Intent(ctx context.Context, id string) (Intent, error) {
	if err := s.enter(); err != nil {
		return Intent{}, err
	}
	defer s.mu.Unlock()
	return s.intent(ctx, id)
}

// Enqueue commits the complete immutable request before any publish can occur.
// Reusing an operation ID with another body is refused, even after abandonment.
func (s *Store) Enqueue(ctx context.Context, r protocol.PublishRequest) (Intent, error) {
	if err := s.enter(); err != nil {
		return Intent{}, err
	}
	defer s.mu.Unlock()
	if _, err := s.identity(ctx); err != nil {
		return Intent{}, err
	}
	if err := validateIntent(r); err != nil {
		return Intent{}, err
	}
	raw, _ := json.Marshal(r)
	if old, err := s.intent(ctx, r.OperationID); err == nil {
		if old.Request != r {
			return Intent{}, errors.New("operation ID already binds a different immutable intent")
		}
		return old, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Intent{}, err
	}
	var count, reserved int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(reservation),0) FROM outbox").Scan(&count, &reserved); err != nil {
		return Intent{}, err
	}
	reservation := 2*len(raw) + 2048
	if count >= MaxIntents || reserved+reservation > MaxOutboxBytes {
		return Intent{}, ErrLimit
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO outbox(operation_id,request,digest,reservation,state) VALUES(?,?,?,?,'queued')", r.OperationID, string(raw), hash(raw), reservation)
	return Intent{Request: r, State: "queued"}, err
}

func validReceipt(i protocol.Identity, r protocol.PublishRequest, v protocol.PublishResult) bool {
	a := v.Artifact
	_, err := time.Parse(time.RFC3339Nano, a.UpdatedAt)
	return a.Scope == i.Scope && a.Key == r.Key && a.Content == r.Content && a.Version == r.ExpectedVersion+1 && len(a.UpdatedAt) <= 64 && err == nil
}

// Push makes exactly one attempt of this persisted request. An uncertain intent
// is an explicit retry of the same operation ID/body; it never rebases a version.
// Accepted receipts never update the read cache: replay may return an old head.
func (s *Store) Push(ctx context.Context, c *client.Client, id string) (Intent, error) {
	if err := s.enter(); err != nil {
		return Intent{}, err
	}
	defer s.mu.Unlock()
	i, err := s.identity(ctx)
	if err != nil {
		return Intent{}, err
	}
	v, err := s.intent(ctx, id)
	if err != nil {
		return v, err
	}
	if v.State == "accepted" {
		return v, nil
	}
	if v.State != "queued" && v.State != "uncertain" {
		return v, ErrBlocked
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE outbox SET state='uncertain',code='',current_version=0 WHERE operation_id=?", id); err != nil {
		return v, err
	}
	wasUncertain := v.State == "uncertain"
	v.State = "uncertain"
	v.Code = ""
	v.CurrentVersion = 0
	receipt, sendErr := c.PublishPinned(ctx, i, v.Request)
	if sendErr != nil {
		var api *client.Error
		// A negative response cannot disprove earlier possible acceptance. In
		// particular, a different authority cannot resolve the original attempt.
		if !wasUncertain && errors.As(sendErr, &api) && api.Status == 409 && (api.Code == "version_conflict" || api.Code == "operation_conflict") && api.CurrentVersion >= 0 {
			if _, err = s.db.ExecContext(ctx, "UPDATE outbox SET state='blocked',code=?,current_version=? WHERE operation_id=?", api.Code, api.CurrentVersion, id); err != nil {
				return v, errors.Join(ErrUncertain, err)
			}
			v.State = "blocked"
			v.Code = api.Code
			v.CurrentVersion = api.CurrentVersion
			return v, ErrBlocked
		}
		return v, ErrUncertain
	}
	if !validReceipt(i, v.Request, receipt) {
		return v, ErrUncertain
	}
	raw, _ := json.Marshal(receipt)
	if _, err = s.db.ExecContext(ctx, "UPDATE outbox SET state='accepted',receipt=? WHERE operation_id=?", string(raw), id); err != nil {
		return v, errors.Join(ErrUncertain, err)
	}
	v.State = "accepted"
	v.Receipt = &receipt
	return v, nil
}

// Resolve is an explicit caller decision for a known conflict (retry the same
// bytes or abandon), or abandonment of an unsent queued intent. Uncertain work
// cannot be abandoned because the authority may already have accepted it.
func (s *Store) Resolve(ctx context.Context, id, decision string) error {
	if err := s.enter(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	v, err := s.intent(ctx, id)
	if err != nil {
		return err
	}
	if v.State == "uncertain" {
		return ErrUncertain
	}
	state := ""
	if decision == "retry" && v.State == "blocked" {
		state = "queued"
	}
	if decision == "abandon" && (v.State == "queued" || v.State == "blocked") {
		state = "abandoned"
	}
	if state == "" {
		return errors.New("decision is not valid for this intent state")
	}
	_, err = s.db.ExecContext(ctx, "UPDATE outbox SET state=? WHERE operation_id=?", state, id)
	return err
}

func (s *Store) Get(ctx context.Context, key string) (protocol.Artifact, error) {
	if err := s.enter(); err != nil {
		return protocol.Artifact{}, err
	}
	defer s.mu.Unlock()
	var ready int
	if err := s.db.QueryRowContext(ctx, "SELECT complete FROM pull_state WHERE id=1").Scan(&ready); err != nil {
		return protocol.Artifact{}, err
	}
	if ready != 1 {
		return protocol.Artifact{}, ErrNotReady
	}
	var raw string
	var a protocol.Artifact
	if err := s.db.QueryRowContext(ctx, "SELECT artifact FROM cache WHERE kind='visible' AND key=?", key).Scan(&raw); err != nil {
		return a, err
	}
	i, err := s.identity(ctx)
	if err != nil {
		return a, err
	}
	if len(raw) > 6*protocol.MaxContentBytes+65536 || protocol.DecodeSyncJSON([]byte(raw), &a) != nil || a.Scope != i.Scope || a.Key != key || a.Version < 1 || len(a.Content) > protocol.MaxContentBytes {
		return protocol.Artifact{}, errors.New("invalid scope-qualified edge cache entry")
	}
	return a, nil
}

func (s *Store) Status(ctx context.Context) (Status, error) {
	if err := s.enter(); err != nil {
		return Status{}, err
	}
	defer s.mu.Unlock()
	var v Status
	var err error
	v.Identity, err = s.identity(ctx)
	if err != nil {
		return v, err
	}
	var ready int
	var phase string
	if err = s.db.QueryRowContext(ctx, "SELECT complete,phase,visible_through FROM pull_state WHERE id=1").Scan(&ready, &phase, &v.Through); err != nil {
		return v, err
	}
	v.SnapshotComplete = ready == 1
	v.SnapshotInProgress = phase == "snapshot"
	v.Intents = map[string]int{}
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM cache WHERE kind='visible'").Scan(&v.CacheItems); err != nil {
		return v, err
	}
	if err = s.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(CASE WHEN state IN ('queued','uncertain','blocked') THEN length(CAST(request AS BLOB)) ELSE 0 END),0),COALESCE(SUM(reservation),0) FROM outbox").Scan(&v.PendingBytes, &v.OutboxReservedBytes); err != nil {
		return v, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT state,COUNT(*) FROM outbox GROUP BY state")
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return v, err
		}
		v.Intents[state] = count
	}
	return v, rows.Err()
}

// ResetPull explicitly starts a fresh staged snapshot, retaining the prior
// complete cache until promotion. It never changes intents or upstream state.
func (s *Store) ResetPull(ctx context.Context) error {
	if err := s.enter(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM cache WHERE kind='staging'"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE pull_state SET cursor='',phase='snapshot',through=-1,last_seq=0,last_key='' WHERE id=1"); err != nil {
		return err
	}
	return tx.Commit()
}
