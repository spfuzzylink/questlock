package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/retention"
	"github.com/spfuzzylink/questlock/semantic"
)

// Retention is opt-in. NULL is an archived payload, never empty content. Old
// readers fail scanning NULL instead of silently returning an invented answer.
const nullablePayloadSchema = `
ALTER TABLE artifact_versions RENAME TO artifact_versions_legacy;
CREATE TABLE artifact_versions (
 scope TEXT NOT NULL, key TEXT NOT NULL, version INTEGER NOT NULL CHECK(version>0),
 content TEXT, updated_at TEXT NOT NULL, PRIMARY KEY(scope,key,version));
INSERT INTO artifact_versions SELECT * FROM artifact_versions_legacy;
CREATE TABLE artifacts_retention (
 scope TEXT NOT NULL, key TEXT NOT NULL, current_version INTEGER NOT NULL,
 PRIMARY KEY(scope,key), FOREIGN KEY(scope,key,current_version) REFERENCES artifact_versions(scope,key,version));
INSERT INTO artifacts_retention SELECT * FROM artifacts;
CREATE TABLE operations_retention (
 agent_id TEXT NOT NULL REFERENCES agents(agent_id), operation_id TEXT NOT NULL,
 scope TEXT NOT NULL,key TEXT NOT NULL,expected_version INTEGER NOT NULL,version INTEGER NOT NULL,
 PRIMARY KEY(agent_id,operation_id), FOREIGN KEY(scope,key,version) REFERENCES artifact_versions(scope,key,version));
INSERT INTO operations_retention SELECT * FROM operations;
DROP TABLE cold_payloads;
DROP TABLE operations;
DROP TABLE artifacts;
DROP TABLE artifact_versions_legacy;
ALTER TABLE artifacts_retention RENAME TO artifacts;
ALTER TABLE operations_retention RENAME TO operations;
CREATE TABLE cold_payloads (
 scope TEXT NOT NULL,key TEXT NOT NULL,version INTEGER NOT NULL,sha256 TEXT NOT NULL,
 size INTEGER NOT NULL CHECK(size>=0),stored_bytes INTEGER NOT NULL CHECK(stored_bytes>0),
 PRIMARY KEY(scope,key,version), FOREIGN KEY(scope,key,version) REFERENCES artifact_versions(scope,key,version));
`

// This private checkpoint is allocated only after retention is configured. It
// never names a claim owner or changes artifact identities or receipt history.
const archiveCheckpointSchema = `CREATE TABLE IF NOT EXISTS storage_archive_checkpoint (
 id INTEGER PRIMARY KEY CHECK(id=1), scope TEXT NOT NULL, key TEXT NOT NULL,
 version INTEGER NOT NULL CHECK(version>=0));`

const (
	minimumArchiveScanItems = 64
	minimumArchiveScanBytes = 64 << 10
)

type storageQuerier interface {
	rowQuerier
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type StorageInfo struct {
	Policy           *retention.Config `json:"policy"`
	CurrentArtifacts int64             `json:"current_artifacts"`
	Versions         int64             `json:"versions"`
	ArchivedVersions int64             `json:"archived_versions"`
	HotPayloadBytes  int64             `json:"hot_payload_bytes"`
	ColdBytes        int64             `json:"cold_bytes"`
	DatabaseBytes    int64             `json:"database_bytes"`
	WALBytes         int64             `json:"wal_bytes"`
	FreePageBytes    int64             `json:"free_page_bytes"`
	ColdFiles        int64             `json:"cold_files"`
	TemporaryBytes   int64             `json:"temporary_bytes"`
}

type StorageReport struct {
	Operation    string `json:"operation"`
	Status       string `json:"status"`
	Items        int64  `json:"items"`
	Bytes        int64  `json:"bytes"`
	DryRun       bool   `json:"dry_run"`
	Deferred     bool   `json:"deferred"`
	Reason       string `json:"reason,omitempty"`
	ScannedItems int64  `json:"scanned_items,omitempty"`
	ScannedBytes int64  `json:"scanned_bytes,omitempty"`
}

func storagePolicy(ctx context.Context, q rowQuerier) (*retention.Config, error) {
	var data string
	err := q.QueryRowContext(ctx, "SELECT config FROM storage_policy WHERE id=1").Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c, err := retention.Decode([]byte(data))
	if err != nil {
		return nil, errors.New("stored retention policy is invalid")
	}
	return &c, nil
}

func (s *Store) applyStorageLimit(ctx context.Context, q storageQuerier) error {
	c, err := storagePolicy(ctx, q)
	if err != nil || c == nil {
		return err
	}
	var size int64
	if err = q.QueryRowContext(ctx, "PRAGMA page_size").Scan(&size); err != nil {
		return err
	}
	pages := int64(4294967294)
	if c.MaxDatabaseBytes > 0 {
		pages = c.MaxDatabaseBytes / size
	}
	var actual int64
	if err = q.QueryRowContext(ctx, fmt.Sprintf("PRAGMA max_page_count=%d", pages)).Scan(&actual); err != nil {
		return err
	}
	if c.MaxDatabaseBytes > 0 && actual > pages {
		return &Error{Code: "storage_limit", Message: "database already exceeds its configured page limit; archive and compact or raise the limit"}
	}
	_, err = q.ExecContext(ctx, "PRAGMA journal_size_limit=8388608")
	return err
}

func storageError(err error) error {
	if err == nil {
		return nil
	}
	var sqlite interface{ Code() int }
	if errors.As(err, &sqlite) && sqlite.Code()&255 == 13 {
		return &Error{Code: "storage_limit", Message: "database page capacity reached; no publication was acknowledged"}
	}
	return err
}

func coldError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, retention.ErrIntegrity) {
		return &Error{Code: "archive_corrupt", Message: "archived content failed integrity verification; retain the claim and restore a verified archive"}
	}
	if errors.Is(err, retention.ErrBudget) {
		return &Error{Code: "storage_limit", Message: "local cold storage capacity reached; archive references and claims are retained"}
	}
	return &Error{Code: "archive_unavailable", Message: "archived content is unavailable; retain the claim and restore its local archive"}
}

func (s *Store) resolveContent(ctx context.Context, q rowQuerier, a protocol.Artifact, content sql.NullString) (protocol.Artifact, error) {
	if content.Valid {
		a.Content = content.String
		return a, nil
	}
	var ref retention.BlobRef
	err := q.QueryRowContext(ctx, "SELECT sha256,size,stored_bytes FROM cold_payloads WHERE scope=? AND key=? AND version=?", a.Scope, a.Key, a.Version).Scan(&ref.Digest, &ref.Size, &ref.StoredBytes)
	if err != nil {
		return protocol.Artifact{}, coldError(retention.ErrIntegrity)
	}
	c, err := storagePolicy(ctx, q)
	if err != nil || c == nil {
		return protocol.Artifact{}, coldError(retention.ErrIntegrity)
	}
	a.Content, err = (&retention.ColdStore{Root: c.ColdDir, MaxBytes: c.MaxColdBytes}).Read(ctx, ref)
	if err != nil {
		return protocol.Artifact{}, coldError(err)
	}
	return a, nil
}

func (s *Store) StorageConfigure(ctx context.Context, c retention.Config) (err error) {
	started := time.Now()
	defer func() { s.emitStorage(ctx, "configure", started, StorageReport{}, err) }()
	if err = c.Validate(); err != nil {
		return err
	}
	if _, err = (&retention.ColdStore{Root: c.ColdDir, MaxBytes: c.MaxColdBytes}).Inventory(ctx); err != nil {
		return coldError(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	previous, err := storagePolicy(ctx, tx)
	if err != nil {
		return err
	}
	if previous != nil && previous.ColdDir != c.ColdDir {
		var n int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM cold_payloads").Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return invalid("restore archived payloads before changing cold_dir; archive locations are not silently rebound")
		}
	}
	var nullable int
	if err = tx.QueryRowContext(ctx, "SELECT \"notnull\" FROM pragma_table_info('artifact_versions') WHERE name='content'").Scan(&nullable); err != nil {
		return err
	}
	if nullable == 1 {
		var custom int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE tbl_name IN ('artifact_versions','artifacts','operations') AND ((type='trigger') OR (type='index' AND sql IS NOT NULL))").Scan(&custom); err != nil {
			return err
		}
		if custom > 0 {
			return invalid("review custom artifact indexes or triggers before enabling the retention schema")
		}
		if _, err = tx.ExecContext(ctx, nullablePayloadSchema); err != nil {
			return err
		}
	}
	data, _ := json.Marshal(c)
	if _, err = tx.ExecContext(ctx, archiveCheckpointSchema); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO storage_policy(id,config) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET config=excluded.config", string(data)); err != nil {
		return err
	}
	if err = s.applyStorageLimit(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) StorageInfo(ctx context.Context) (info StorageInfo, err error) {
	info.Policy, err = storagePolicy(ctx, s.db)
	if err != nil {
		return info, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM artifacts),(SELECT COUNT(*) FROM artifact_versions),(SELECT COUNT(*) FROM cold_payloads),COALESCE(SUM(length(CAST(content AS BLOB))),0) FROM artifact_versions`).Scan(&info.CurrentArtifacts, &info.Versions, &info.ArchivedVersions, &info.HotPayloadBytes); err != nil {
		return info, err
	}
	var size, free int64
	if err = s.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&size); err != nil {
		return info, err
	}
	if err = s.db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&free); err != nil {
		return info, err
	}
	info.FreePageBytes = size * free
	for path, target := range map[string]*int64{s.path: &info.DatabaseBytes, s.path + "-wal": &info.WALBytes} {
		f, e := os.Stat(path)
		if e == nil {
			*target = f.Size()
		} else if !errors.Is(e, os.ErrNotExist) {
			return info, e
		}
	}
	if info.Policy != nil {
		inv, e := (&retention.ColdStore{Root: info.Policy.ColdDir, MaxBytes: info.Policy.MaxColdBytes}).Inventory(ctx)
		if e != nil {
			return info, coldError(e)
		}
		info.ColdBytes, info.ColdFiles, info.TemporaryBytes = inv.Bytes, inv.Files, inv.TemporaryBytes
	}
	return info, nil
}

// Eligible current results are recognized conservatively. All opaque current
// values, active claims, ledgers, team state and outcome records stay hot.
func completedCurrent(a protocol.Artifact) bool {
	if strings.HasPrefix(a.Key, "inference/v1/jobs/") {
		var v struct {
			Schema      int    `json:"schema"`
			State       string `json:"state"`
			Fingerprint string `json:"fingerprint"`
			Response    *struct {
				Complete bool `json:"complete"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(a.Content), &v) != nil {
			return false
		}
		if v.State == "resolved" {
			return resolvedCurrent(a)
		}
		return v.Schema == 1 && v.State == "complete" && v.Response != nil && v.Response.Complete && a.Key == "inference/v1/jobs/"+v.Fingerprint
	}
	if strings.HasPrefix(a.Key, "revision/v1/results/") && a.Version == 1 {
		var v struct {
			Schema  int    `json:"schema"`
			Request string `json:"request_sha256"`
		}
		return json.Unmarshal([]byte(a.Content), &v) == nil && v.Schema == 1 && a.Key == "revision/v1/results/"+v.Request
	}
	if strings.HasPrefix(a.Key, "semantic/v1/provenance/") && a.Version == 1 && len(a.Content) <= 64<<10 {
		var proof struct {
			Schema           int                 `json:"schema"`
			Question         string              `json:"question"`
			RequestSHA256    string              `json:"request_sha256"`
			Partition        string              `json:"partition"`
			EmbeddingModelID string              `json:"embedding_model_id"`
			Source           *archiveReuseSource `json:"source"`
		}
		return protocol.DecodeSyncJSON([]byte(a.Content), &proof) == nil && proof.Schema == 1 && proof.Source != nil && proof.Source.valid() &&
			a.Key == "semantic/v1/provenance/"+proof.Source.SHA256 && archiveHash(proof.RequestSHA256) && archiveHash(proof.Partition) && archiveHash(proof.EmbeddingModelID) &&
			len(proof.Question) <= semantic.MaxInputBytes && utf8.ValidString(proof.Question) && strings.TrimSpace(proof.Question) != ""
	}
	return false
}

// These are shape checks for closed payload retention, not authorization to
// reuse an answer. The inference reader still validates source identity, hashes,
// checks, expiry, and policy after reading exactly the same hot or cold bytes.
type archiveReuseSource struct {
	Key                string `json:"key"`
	Version            int64  `json:"version"`
	SHA256             string `json:"sha256"`
	OutputSHA256       string `json:"output_sha256"`
	RequestFingerprint string `json:"request_fingerprint"`
}

func (s archiveReuseSource) valid() bool {
	return archiveJobKey(s.Key) && s.Version > 0 && archiveHash(s.SHA256) && archiveHash(s.OutputSHA256) && archiveHash(s.RequestFingerprint)
}

func archiveHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func archiveJobKey(key string) bool {
	const prefix = "inference/v1/jobs/"
	return strings.HasPrefix(key, prefix) && archiveHash(strings.TrimPrefix(key, prefix))
}

func resolvedCurrent(a protocol.Artifact) bool {
	var job struct {
		Schema             int             `json:"schema"`
		State              string          `json:"state"`
		Fingerprint        string          `json:"fingerprint"`
		RequestFingerprint string          `json:"request_fingerprint"`
		CreatedAt          time.Time       `json:"created_at"`
		ExpiresAt          time.Time       `json:"expires_at"`
		Response           json.RawMessage `json:"response"`
		BudgetKey          string          `json:"budget_key,omitempty"`
		Reservation        string          `json:"reservation,omitempty"`
		ReservedMicros     int64           `json:"reserved_micros,omitempty"`
		TokenBudgetKey     string          `json:"token_budget_key,omitempty"`
		Resolution         *struct {
			PolicyID        string             `json:"policy_id"`
			Source          archiveReuseSource `json:"source"`
			Response        json.RawMessage    `json:"response"`
			VerificationKey string             `json:"verification_key"`
		} `json:"resolution"`
	}
	if protocol.DecodeSyncJSON([]byte(a.Content), &job) != nil || job.Schema != 1 || job.State != "resolved" || !archiveHash(job.Fingerprint) || !archiveHash(job.RequestFingerprint) || a.Key != "inference/v1/jobs/"+job.Fingerprint || job.CreatedAt.IsZero() || !job.ExpiresAt.After(job.CreatedAt) || job.Reservation != "" || job.ReservedMicros != 0 || job.Resolution == nil {
		return false
	}
	r := job.Resolution
	if !archiveHash(r.PolicyID) || !archiveJobKey(r.VerificationKey) || !r.Source.valid() || r.Source.Key == a.Key {
		return false
	}
	var response struct {
		Complete bool   `json:"complete"`
		Text     string `json:"text"`
	}
	if json.Unmarshal(job.Response, &response) != nil || !response.Complete || response.Text == "" || semanticDigest(response.Text) != r.Source.OutputSHA256 {
		return false
	}
	// A resolved job stores the same original response in both locations. Decode
	// with exact JSON numbers so field ordering cannot affect this comparison.
	var outer, inner any
	aDecoder := json.NewDecoder(strings.NewReader(string(job.Response)))
	bDecoder := json.NewDecoder(strings.NewReader(string(r.Response)))
	aDecoder.UseNumber()
	bDecoder.UseNumber()
	return aDecoder.Decode(&outer) == nil && bDecoder.Decode(&inner) == nil && reflect.DeepEqual(outer, inner)
}

type archiveCheckpoint struct {
	scope, key string
	version    int64
}

func loadArchiveCheckpoint(ctx context.Context, tx *sql.Tx) (archiveCheckpoint, error) {
	var checkpoint archiveCheckpoint
	err := tx.QueryRowContext(ctx, "SELECT scope,key,version FROM storage_archive_checkpoint WHERE id=1").Scan(&checkpoint.scope, &checkpoint.key, &checkpoint.version)
	if errors.Is(err, sql.ErrNoRows) {
		return archiveCheckpoint{}, nil
	}
	if err == nil && checkpoint != (archiveCheckpoint{}) && (!identifier.MatchString(checkpoint.scope) || validateKey(checkpoint.key) != nil || checkpoint.version < 1) {
		return archiveCheckpoint{}, invalid("stored archive checkpoint is invalid")
	}
	return checkpoint, err
}

type archiveScanItem struct {
	artifact protocol.Artifact
	bytes    sql.NullInt64
	current  int64
}

// StorageArchive reads at most max(64, budget.Items*16) metadata rows and
// max(64 KiB, budget.Bytes) payload bytes per invocation. Artifact output remains
// bounded by budget.Items and budget.Bytes. A private SQLite checkpoint advances
// rejected rows atomically with archival and wraps after exhausting the keyspace;
// dry runs preserve it. The checkpoint schema is allocated only with retention.
func (s *Store) StorageArchive(ctx context.Context, dryRun bool, now time.Time, budget retention.Budget) (report StorageReport, err error) {
	report = StorageReport{Operation: "archive", Status: "complete", DryRun: dryRun}
	started := time.Now()
	defer func() {
		err = storageError(err)
		if err != nil {
			report.Status = "error"
			report.Items, report.Bytes = 0, 0
		}
		s.emitStorage(ctx, "archive", started, report, err)
	}()
	if budget.Deferred {
		report.Status = "deferred"
		report.Deferred = true
		report.Reason = budget.Reason
		return report, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	c, err := storagePolicy(ctx, tx)
	if err != nil {
		return report, err
	}
	if c == nil {
		return report, invalid("configure a retention policy before archival")
	}
	if err = s.applyStorageLimit(ctx, tx); err != nil {
		return report, err
	}
	if budget.Items < 1 || budget.Items > c.BatchItems || budget.Bytes < 1 || budget.Bytes > c.BatchBytes {
		return report, invalid("archive batch exceeds the configured resource bounds")
	}
	// Older configured stores acquire the checkpoint lazily. Dry runs roll this
	// transaction back, including any first-time checkpoint schema allocation.
	if _, err = tx.ExecContext(ctx, archiveCheckpointSchema); err != nil {
		return report, err
	}
	checkpoint, err := loadArchiveCheckpoint(ctx, tx)
	if err != nil {
		return report, err
	}
	cutoff := now.UTC().Add(-time.Duration(c.HotHistoryDays) * 24 * time.Hour)
	// Page metadata in primary-key order before fetching or decoding content.
	// Both row work and inspected payload bytes have explicit per-call bounds;
	// rejected records advance the durable cursor instead of starving later rows.
	scanItems := max(minimumArchiveScanItems, budget.Items*16)
	scanBytes := max(int64(minimumArchiveScanBytes), budget.Bytes)
	rows, err := tx.QueryContext(ctx, `SELECT v.scope,v.key,v.version,length(CAST(v.content AS BLOB)),v.updated_at,a.current_version
 FROM artifact_versions v JOIN artifacts a ON a.scope=v.scope AND a.key=v.key
 WHERE (v.scope,v.key,v.version)>(?,?,?)
 ORDER BY v.scope,v.key,v.version LIMIT ?`, checkpoint.scope, checkpoint.key, checkpoint.version, scanItems)
	if err != nil {
		return report, err
	}
	page := make([]archiveScanItem, 0, scanItems)
	for rows.Next() {
		var item archiveScanItem
		a := &item.artifact
		if err = rows.Scan(&a.Scope, &a.Key, &a.Version, &item.bytes, &a.UpdatedAt, &item.current); err != nil {
			break
		}
		page = append(page, item)
	}
	if e := rows.Err(); err == nil {
		err = e
	}
	if e := rows.Close(); err == nil {
		err = e
	}
	if err != nil {
		return report, err
	}
	report.ScannedItems = int64(len(page))
	items := []protocol.Artifact{}
	next := checkpoint
	processed := 0
	for i, item := range page {
		a := item.artifact
		when, parseErr := time.Parse(time.RFC3339Nano, a.UpdatedAt)
		potentialCurrent := c.ArchiveCompletedCurrent && a.Version == item.current && (strings.HasPrefix(a.Key, "inference/v1/jobs/") || strings.HasPrefix(a.Key, "revision/v1/results/") || strings.HasPrefix(a.Key, "semantic/v1/provenance/"))
		inspect := item.bytes.Valid && item.bytes.Int64 > 0 && item.bytes.Int64 <= budget.Bytes && parseErr == nil && when.Before(cutoff) && (a.Version <= item.current-int64(c.KeepRecentVersions) || potentialCurrent)
		if inspect && item.bytes.Int64 > scanBytes-report.ScannedBytes {
			break // Do not advance past a payload that the next call can inspect.
		}
		next, processed = archiveCheckpoint{a.Scope, a.Key, a.Version}, i+1
		if !inspect {
			continue
		}
		if err = tx.QueryRowContext(ctx, "SELECT content FROM artifact_versions WHERE scope=? AND key=? AND version=?", a.Scope, a.Key, a.Version).Scan(&a.Content); err != nil {
			return report, err
		}
		report.ScannedBytes += int64(len(a.Content))
		if a.Version == item.current && !completedCurrent(a) {
			continue
		}
		if int64(len(a.Content)) > budget.Bytes-report.Bytes {
			continue
		}
		items = append(items, a)
		report.Items++
		report.Bytes += int64(len(a.Content))
		if len(items) == budget.Items {
			break
		}
	}
	if processed == len(page) && len(page) < scanItems {
		next = archiveCheckpoint{} // Wrap once the current traversal is exhausted.
	} else if report.Items < int64(budget.Items) {
		report.Reason = "scan_limit"
	}
	if dryRun {
		report.Status = "dry_run"
		return report, nil
	}
	cs := &retention.ColdStore{Root: c.ColdDir, MaxBytes: c.MaxColdBytes}
	for _, a := range items {
		ref, e := cs.Put(ctx, a.Content)
		if e != nil {
			return report, coldError(e)
		}
		// NULL and its reference commit together, only after cold bytes are durable.
		if _, err = tx.ExecContext(ctx, "UPDATE artifact_versions SET content=NULL WHERE scope=? AND key=? AND version=? AND content=?", a.Scope, a.Key, a.Version, a.Content); err != nil {
			return report, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO cold_payloads(scope,key,version,sha256,size,stored_bytes) VALUES(?,?,?,?,?,?)", a.Scope, a.Key, a.Version, ref.Digest, ref.Size, ref.StoredBytes); err != nil {
			return report, err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO storage_archive_checkpoint(id,scope,key,version) VALUES(1,?,?,?) ON CONFLICT(id) DO UPDATE SET scope=excluded.scope,key=excluded.key,version=excluded.version", next.scope, next.key, next.version); err != nil {
		return report, err
	}
	if err = tx.Commit(); err != nil {
		return report, err
	}
	return report, nil
}

func (s *Store) StorageRestore(ctx context.Context, maxItems int) (report StorageReport, err error) {
	report = StorageReport{Operation: "restore", Status: "complete"}
	started := time.Now()
	defer func() {
		err = storageError(err)
		if err != nil {
			report.Status = "error"
			report.Items, report.Bytes = 0, 0
		}
		s.emitStorage(ctx, "restore", started, report, err)
	}()
	if maxItems < 1 || maxItems > 1024 {
		return report, invalid("restore batch must contain 1..1024 items")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	if err = s.applyStorageLimit(ctx, tx); err != nil {
		return report, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT scope,key,version,updated_at FROM artifact_versions WHERE content IS NULL ORDER BY scope,key,version LIMIT ?", maxItems)
	if err != nil {
		return report, err
	}
	var items []protocol.Artifact
	for rows.Next() {
		var a protocol.Artifact
		if err = rows.Scan(&a.Scope, &a.Key, &a.Version, &a.UpdatedAt); err != nil {
			break
		}
		items = append(items, a)
	}
	if e := rows.Err(); err == nil {
		err = e
	}
	if e := rows.Close(); err == nil {
		err = e
	}
	if err != nil {
		return report, err
	}
	for _, a := range items {
		a, err = s.resolveContent(ctx, tx, a, sql.NullString{})
		if err != nil {
			return report, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE artifact_versions SET content=? WHERE scope=? AND key=? AND version=? AND content IS NULL", a.Content, a.Scope, a.Key, a.Version); err != nil {
			return report, err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM cold_payloads WHERE scope=? AND key=? AND version=?", a.Scope, a.Key, a.Version); err != nil {
			return report, err
		}
		report.Items++
		report.Bytes += int64(len(a.Content))
	}
	if err = tx.Commit(); err != nil {
		return report, err
	}
	return report, nil
}

func (s *Store) StorageHistory(ctx context.Context, scope, key string, version int64) (a protocol.Artifact, err error) {
	started := time.Now()
	defer func() { s.observeStorageFailure(ctx, "history", started, err) }()
	if !identifier.MatchString(scope) || validateKey(key) != nil || version < 1 {
		return a, invalid("history requires a valid scope, key and positive version")
	}
	var content sql.NullString
	err = s.db.QueryRowContext(ctx, "SELECT scope,key,version,content,updated_at FROM artifact_versions WHERE scope=? AND key=? AND version=?", scope, key, version).Scan(&a.Scope, &a.Key, &a.Version, &content, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, &Error{Code: "not_found", Message: "historical artifact not found"}
	}
	if err != nil {
		return a, err
	}
	return s.resolveContent(ctx, s.db, a, content)
}

func (s *Store) StorageVerify(ctx context.Context) (report StorageReport, err error) {
	report = StorageReport{Operation: "verify", Status: "complete"}
	started := time.Now()
	defer func() {
		if err != nil {
			report.Status = "error"
		}
		s.emitStorage(ctx, "verify", started, report, err)
	}()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	var check string
	if err = tx.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil {
		return report, err
	}
	if check != "ok" {
		return report, errors.New("database integrity check failed")
	}
	foreign, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return report, err
	}
	brokenForeign := foreign.Next()
	foreignErr := foreign.Err()
	closeErr := foreign.Close()
	if brokenForeign {
		return report, coldError(retention.ErrIntegrity)
	}
	if foreignErr != nil {
		return report, foreignErr
	}
	if closeErr != nil {
		return report, closeErr
	}
	var broken int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_versions v LEFT JOIN cold_payloads c ON c.scope=v.scope AND c.key=v.key AND c.version=v.version WHERE (v.content IS NULL AND c.sha256 IS NULL) OR (v.content IS NOT NULL AND c.sha256 IS NOT NULL)`).Scan(&broken); err != nil {
		return report, err
	}
	if broken != 0 {
		return report, coldError(retention.ErrIntegrity)
	}
	var afterScope, afterKey string
	var afterVersion int64
	for {
		var a protocol.Artifact
		err = tx.QueryRowContext(ctx, `SELECT scope,key,version,updated_at FROM artifact_versions WHERE content IS NULL AND (scope,key,version)>(?,?,?) ORDER BY scope,key,version LIMIT 1`, afterScope, afterKey, afterVersion).Scan(&a.Scope, &a.Key, &a.Version, &a.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
			break
		}
		if err != nil {
			return report, err
		}
		a, err = s.resolveContent(ctx, tx, a, sql.NullString{})
		if err != nil {
			return report, err
		}
		report.Items++
		report.Bytes += int64(len(a.Content))
		afterScope, afterKey, afterVersion = a.Scope, a.Key, a.Version
	}
	if err = tx.Commit(); err != nil {
		return report, err
	}
	return report, nil
}

func (s *Store) StorageCompact(ctx context.Context) (err error) {
	started := time.Now()
	defer func() { s.emitStorage(ctx, "compact", started, StorageReport{}, err) }()
	var busy, log, done int
	if err = s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &done); err != nil {
		return err
	}
	if busy != 0 {
		return errors.New("checkpoint is busy; close other database readers before compaction")
	}
	if _, err = s.db.ExecContext(ctx, "VACUUM"); err != nil {
		return storageError(err)
	}
	return s.applyStorageLimit(ctx, s.db)
}

func (s *Store) SetStorageObserver(f func(context.Context, retention.Event)) {
	s.observerMu.Lock()
	s.observer = f
	s.observerMu.Unlock()
}
func storageCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	var e *Error
	if errors.As(err, &e) {
		switch e.Code {
		case "archive_corrupt", "archive_unavailable":
			return e.Code
		case "storage_limit":
			return "capacity"
		case "invalid":
			return "invalid"
		}
	}
	return "storage_error"
}
func (s *Store) observeStorageFailure(ctx context.Context, op string, started time.Time, err error) {
	var e *Error
	if errors.As(err, &e) && (e.Code == "archive_corrupt" || e.Code == "archive_unavailable") {
		s.emitStorage(ctx, op, started, StorageReport{}, err)
	}
}
func (s *Store) emitStorage(ctx context.Context, op string, started time.Time, r StorageReport, err error) {
	s.observerMu.RLock()
	f := s.observer
	s.observerMu.RUnlock()
	if f == nil {
		return
	}
	outcome := "complete"
	if r.DryRun {
		outcome = "dry_run"
	}
	if r.Deferred {
		outcome = "deferred"
	}
	if err != nil {
		outcome = "error"
	}
	e := retention.Event{Operation: op, Outcome: outcome, ErrorCode: storageCode(err), StartedAt: started, EndedAt: time.Now(), Items: r.Items, Bytes: r.Bytes}
	// Observers are caller-owned, content-free and best effort. A telemetry panic
	// must not undo a committed archive or turn a failed read into a model retry.
	func() { defer func() { _ = recover() }(); f(ctx, e) }()
}
