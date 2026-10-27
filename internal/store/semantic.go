package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/semantic"
)

const semanticSchema = `
CREATE TABLE IF NOT EXISTS semantic_entries (
    scope TEXT NOT NULL,
    id TEXT NOT NULL,
    partition TEXT NOT NULL,
    model_id TEXT NOT NULL,
    dimensions INTEGER NOT NULL CHECK (dimensions BETWEEN 1 AND 4096),
    vector BLOB NOT NULL CHECK (length(vector) = dimensions * 4),
    source_key TEXT NOT NULL,
    source_version INTEGER NOT NULL CHECK (source_version > 0),
    source_sha256 TEXT NOT NULL,
    output_sha256 TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    PRIMARY KEY (scope, id)
);
CREATE INDEX IF NOT EXISTS semantic_scan ON semantic_entries(scope, partition, model_id, dimensions, created_at DESC, id);
CREATE INDEX IF NOT EXISTS semantic_expiry ON semantic_entries(scope, expires_at);
`

// The optional derived schema is allocated only when a semantic operation is
// requested. A full pre-feature database must still open for exact reads and
// receipt replay. Failed initialization rolls back and may be retried later.
func (s *Store) ensureSemanticSchema(ctx context.Context, p Principal) (err error) {
	defer func() { err = storageError(err) }()
	s.semanticMu.Lock()
	defer s.semanticMu.Unlock()
	if err = ctx.Err(); err != nil {
		return err
	}
	if s.semanticReady {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkActive(ctx, tx, p); err != nil {
		return err
	}
	if err = s.applyStorageLimit(ctx, tx); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, semanticSchema); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.semanticReady = true
	return nil
}

// PutSemantic writes only the derived index. It cannot mutate an inference
// claim, an artifact version, a receipt, or a budget reservation.
func (s *Store) PutSemantic(ctx context.Context, p Principal, request semantic.PutRequest) (err error) {
	defer func() { err = storageError(err) }()
	if err = semantic.ValidatePut(request); err != nil {
		return invalid("invalid semantic entry")
	}
	entry := request.Entry
	now := time.Now().UTC()
	created, expiry, err := s.semanticSource(ctx, p, entry.Source, now)
	if err != nil {
		return err
	}
	entry.CreatedAt = created
	if entry.ExpiresAt.After(expiry) {
		entry.ExpiresAt = expiry
	}
	if cap := created.Add(24 * time.Hour); entry.ExpiresAt.After(cap) {
		entry.ExpiresAt = cap
	}
	if !entry.ExpiresAt.After(now) {
		return invalid("semantic entry must not be expired")
	}
	if err = s.ensureSemanticSchema(ctx, p); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkActive(ctx, tx, p); err != nil {
		return err
	}
	if err = s.applyStorageLimit(ctx, tx); err != nil {
		return err
	}
	// Source content was fetched through Get, including cold validation. Fence
	// the version once more under the writer transaction before accepting it.
	version, err := currentVersion(ctx, tx, p.Scope, entry.Source.Key)
	if err != nil {
		return err
	}
	if version != entry.Source.Version {
		return invalid("semantic source is stale")
	}
	var previousExpiry string
	err = tx.QueryRowContext(ctx, "SELECT expires_at FROM semantic_entries WHERE scope=? AND id=?", p.Scope, entry.ID).Scan(&previousExpiry)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		prior, parseErr := time.Parse(time.RFC3339Nano, previousExpiry)
		if parseErr != nil {
			return parseErr
		}
		if entry.ExpiresAt.After(prior) {
			entry.ExpiresAt = prior
		}
		if !entry.ExpiresAt.After(time.Now().UTC()) {
			return invalid("semantic entry must not be expired")
		}
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM semantic_entries WHERE scope=? AND expires_at<=?", p.Scope, semanticTime(now)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO semantic_entries
		(scope,id,partition,model_id,dimensions,vector,source_key,source_version,source_sha256,output_sha256,created_at,expires_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(scope,id) DO UPDATE SET
		dimensions=excluded.dimensions, vector=excluded.vector, expires_at=MIN(semantic_entries.expires_at,excluded.expires_at)`,
		p.Scope, entry.ID, entry.Partition, entry.ModelID, len(entry.Vector), encodeSemanticVector(entry.Vector),
		entry.Source.Key, entry.Source.Version, entry.Source.SHA256, entry.Source.OutputSHA256, semanticTime(entry.CreatedAt), semanticTime(entry.ExpiresAt))
	if err != nil {
		return err
	}
	// The cap applies to the entire authenticated scope, across all partitions,
	// embedding models and dimensions. Eviction never deletes source artifacts.
	_, err = tx.ExecContext(ctx, `DELETE FROM semantic_entries WHERE scope=? AND id IN
		(SELECT id FROM semantic_entries WHERE scope=? ORDER BY created_at DESC,id ASC LIMIT -1 OFFSET ?)`, p.Scope, p.Scope, request.MaxEntries)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// QuerySemantic scans a bounded, deterministic subset after all metadata
// filters. Vectors are decoded one at a time, never accumulated in memory.
func (s *Store) QuerySemantic(ctx context.Context, p Principal, query semantic.Query) (result semantic.SearchResult, err error) {
	result.Candidates = []semantic.Candidate{}
	if err = semantic.ValidateQuery(query); err != nil {
		return result, invalid("invalid semantic query")
	}
	if err = checkActive(ctx, s.db, p); err != nil {
		return result, err
	}
	if err = s.ensureSemanticSchema(ctx, p); err != nil {
		return result, err
	}
	now := time.Now().UTC()
	rows, err := s.db.QueryContext(ctx, `SELECT id,vector,source_key,source_version,source_sha256,output_sha256,created_at,expires_at
		FROM semantic_entries WHERE scope=? AND partition=? AND model_id=? AND dimensions=? AND expires_at>?
		ORDER BY created_at DESC,id ASC LIMIT ?`, p.Scope, query.Partition, query.ModelID, len(query.Vector), semanticTime(now), query.MaxScan+1)
	if err != nil {
		return result, err
	}
	type ranked struct {
		id        string
		candidate semantic.Candidate
	}
	var matches []ranked
	for rows.Next() {
		if result.Scanned == query.MaxScan {
			result.Truncated = true
			break
		}
		var item ranked
		var vector []byte
		var created, expiry string
		c := &item.candidate
		if err = rows.Scan(&item.id, &vector, &c.Source.Key, &c.Source.Version, &c.Source.SHA256, &c.Source.OutputSHA256, &created, &expiry); err != nil {
			break
		}
		result.Scanned++
		c.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			break
		}
		c.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiry)
		if err != nil {
			break
		}
		decoded, decodeErr := decodeSemanticVector(vector, len(query.Vector))
		if decodeErr != nil {
			err = decodeErr
			break
		}
		c.Similarity, err = semantic.Cosine(query.Vector, decoded)
		if err != nil {
			break
		}
		if c.Similarity >= query.MinSimilarity {
			matches = append(matches, item)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	closeErr := rows.Close()
	if err != nil {
		return result, err
	}
	if closeErr != nil {
		return result, closeErr
	}
	// Close rows before Get: the broker intentionally uses one SQLite connection.
	sort.Slice(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.candidate.Similarity != b.candidate.Similarity {
			return a.candidate.Similarity > b.candidate.Similarity
		}
		if !a.candidate.CreatedAt.Equal(b.candidate.CreatedAt) {
			return a.candidate.CreatedAt.After(b.candidate.CreatedAt)
		}
		return a.id < b.id
	})
	for _, item := range matches {
		if len(result.Candidates) == query.TopK {
			break
		}
		c := item.candidate
		created, expiry, sourceErr := s.semanticSource(ctx, p, c.Source, time.Now().UTC())
		if sourceErr != nil {
			var e *Error
			if errors.As(sourceErr, &e) && (e.Code == "invalid" || e.Code == "not_found") {
				continue
			}
			// Archive corruption/unavailability is an error, never a cache miss.
			return result, sourceErr
		}
		if !created.Equal(c.CreatedAt) || c.ExpiresAt.After(expiry) || !c.ExpiresAt.After(time.Now().UTC()) {
			continue
		}
		result.Candidates = append(result.Candidates, c)
	}
	return result, nil
}

func (s *Store) semanticSource(ctx context.Context, p Principal, source semantic.Source, now time.Time) (time.Time, time.Time, error) {
	artifact, err := s.Get(ctx, p, source.Key)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if artifact.Version != source.Version || semanticDigest(artifact.Content) != source.SHA256 {
		return time.Time{}, time.Time{}, invalid("semantic source is stale")
	}
	// Decode to raw fields first so duplicate source fields are rejected while
	// harmless job metadata added by a future producer remains opaque.
	var fields map[string]json.RawMessage
	if protocol.DecodeSyncJSON([]byte(artifact.Content), &fields) != nil {
		return time.Time{}, time.Time{}, invalid("semantic source is not a confirmed completed job")
	}
	var job struct {
		Schema      int       `json:"schema"`
		Fingerprint string    `json:"fingerprint"`
		State       string    `json:"state"`
		CreatedAt   time.Time `json:"created_at"`
		ExpiresAt   time.Time `json:"expires_at"`
		Response    *struct {
			Complete bool    `json:"complete"`
			Text     *string `json:"text"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(artifact.Content), &job) != nil || job.Schema != 1 || job.State != "complete" || job.Response == nil || !job.Response.Complete || job.Response.Text == nil ||
		source.Key != "inference/v1/jobs/"+job.Fingerprint || job.CreatedAt.IsZero() || job.CreatedAt.After(now) || !job.ExpiresAt.After(now) ||
		semanticDigest(*job.Response.Text) != source.OutputSHA256 {
		return time.Time{}, time.Time{}, invalid("semantic source is not a confirmed completed job")
	}
	return job.CreatedAt.UTC(), job.ExpiresAt.UTC(), nil
}

func semanticDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// Fixed-width UTC values preserve chronological ordering under SQLite TEXT.
func semanticTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func encodeSemanticVector(vector []float32) []byte {
	encoded := make([]byte, len(vector)*4)
	for i, value := range vector {
		binary.LittleEndian.PutUint32(encoded[i*4:], math.Float32bits(value))
	}
	return encoded
}

func decodeSemanticVector(encoded []byte, dimensions int) ([]float32, error) {
	if dimensions < 1 || dimensions > 4096 || len(encoded) != dimensions*4 {
		return nil, errors.New("invalid stored semantic vector")
	}
	vector := make([]float32, dimensions)
	for i := range vector {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(encoded[i*4:]))
	}
	return vector, nil
}
