package semantic

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	DefaultMinSimilarity = 0.9
	DefaultTopK          = 3
	DefaultMaxScan       = 1000
	DefaultMaxEntries    = 1000
	DefaultMaxInputBytes = 8192
	DefaultTimeoutMS     = 10000
	MaxTopK              = 10
	MaxScan              = 10000
	MaxEntries           = 10000
	MaxInputBytes        = 32768
	MaxTimeoutMS         = 60000
	MaxDimensions        = 4096
)

// NormalizeConfig fills bounded defaults and rejects modes that could serve an
// answer. The returned bindings are a copy so later caller edits cannot alter it.
func NormalizeConfig(c Config) (Config, error) {
	if c.Mode != "shadow" {
		return Config{}, errors.New("semantic mode must be shadow")
	}
	if !validLabel(c.Family, 128) || !validLabel(c.Domain, 128) || !validLabel(c.EvidenceRevision, 128) {
		return Config{}, errors.New("semantic family, domain and evidence revision require bounded nonempty UTF-8 labels")
	}
	if !validOllamaModel(c.EmbeddingModel) {
		return Config{}, errors.New("semantic embedding model requires a valid installed local model name")
	}
	if len(c.Bindings) > 32 {
		return Config{}, errors.New("semantic bindings exceed 32 entries")
	}
	if c.Bindings != nil {
		bindings := make(map[string]string, len(c.Bindings))
		total := 0
		for key, value := range c.Bindings {
			if !validLabel(key, 64) || !validLabel(value, 256) {
				return Config{}, errors.New("semantic bindings require bounded nonempty UTF-8 keys and values")
			}
			total += len(key) + len(value)
			bindings[key] = value
		}
		if total > 8192 {
			return Config{}, errors.New("semantic bindings exceed 8192 bytes")
		}
		c.Bindings = bindings
	}
	if c.MinSimilarity == 0 {
		c.MinSimilarity = DefaultMinSimilarity
	}
	if c.TopK == 0 {
		c.TopK = DefaultTopK
	}
	if c.MaxScan == 0 {
		c.MaxScan = DefaultMaxScan
	}
	if c.MaxEntries == 0 {
		c.MaxEntries = DefaultMaxEntries
	}
	if c.MaxInputBytes == 0 {
		c.MaxInputBytes = DefaultMaxInputBytes
	}
	if c.TimeoutMS == 0 {
		c.TimeoutMS = DefaultTimeoutMS
	}
	if !validSimilarity(c.MinSimilarity) || c.TopK < 1 || c.TopK > MaxTopK || c.MaxScan < 1 || c.MaxScan > MaxScan || c.MaxEntries < 1 || c.MaxEntries > MaxEntries || c.MaxInputBytes < 1 || c.MaxInputBytes > MaxInputBytes || c.TimeoutMS < 1 || c.TimeoutMS > MaxTimeoutMS {
		return Config{}, errors.New("semantic limits exceed supported bounds")
	}
	return c, nil
}

// ValidateQuery checks the fully specified wire request; defaults belong to
// NormalizeConfig and are deliberately not inferred at the broker boundary.
func ValidateQuery(q Query) error {
	if !validHash(q.Partition) || !validHash(q.ModelID) {
		return errors.New("semantic partition and model ID require lowercase SHA-256 hashes")
	}
	if !validSimilarity(q.MinSimilarity) || q.TopK < 1 || q.TopK > MaxTopK || q.MaxScan < 1 || q.MaxScan > MaxScan {
		return errors.New("semantic query limits exceed supported bounds")
	}
	return validateVector(q.Vector)
}

// ValidatePut verifies identities, bounded vectors and the short-lived source
// pointer. The source artifact itself must also be checked by the index owner.
func ValidatePut(p PutRequest) error {
	e := p.Entry
	if !validHash(e.ID) || !validHash(e.Partition) || !validHash(e.ModelID) {
		return errors.New("semantic entry identities require lowercase SHA-256 hashes")
	}
	if p.MaxEntries < 1 || p.MaxEntries > MaxEntries {
		return errors.New("semantic entry capacity exceeds supported bounds")
	}
	if !strings.HasPrefix(e.Source.Key, "inference/v1/jobs/") || !validHash(strings.TrimPrefix(e.Source.Key, "inference/v1/jobs/")) || e.Source.Version < 1 || !validHash(e.Source.SHA256) || !validHash(e.Source.OutputSHA256) {
		return errors.New("semantic source requires an exact inference job key, version and hashes")
	}
	if e.ID != EntryID(e) {
		return errors.New("semantic entry ID does not match its partition, model and source")
	}
	if e.CreatedAt.IsZero() || e.ExpiresAt.IsZero() || !e.ExpiresAt.After(e.CreatedAt) || e.ExpiresAt.Sub(e.CreatedAt) > 24*time.Hour {
		return errors.New("semantic entry lifetime must be positive and at most 24 hours")
	}
	return validateVector(e.Vector)
}

// EntryID binds an entry to its exact source snapshot within one embedding
// partition. Vectors and expiry do not create duplicate copies of that snapshot.
func EntryID(e Entry) string {
	encoded, _ := json.Marshal(struct {
		Partition string `json:"partition"`
		ModelID   string `json:"model_id"`
		Source    Source `json:"source"`
	}{e.Partition, e.ModelID, e.Source})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// Cosine compares finite, nonzero vectors with matching bounded dimensions.
// float64 accumulation avoids overflowing when finite float32 values are large.
func Cosine(a, b []float32) (float64, error) {
	if err := validateVector(a); err != nil {
		return 0, err
	}
	if err := validateVector(b); err != nil {
		return 0, err
	}
	if len(a) != len(b) {
		return 0, errors.New("semantic vector dimensions differ")
	}
	var dot, aa, bb float64
	for i, av := range a {
		x, y := float64(av), float64(b[i])
		dot += x * y
		aa += x * x
		bb += y * y
	}
	return math.Max(-1, math.Min(1, dot/(math.Sqrt(aa)*math.Sqrt(bb)))), nil
}

func validateVector(v []float32) error {
	if len(v) < 1 || len(v) > MaxDimensions {
		return errors.New("semantic vectors require 1..4096 dimensions")
	}
	nonzero := false
	for _, value := range v {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("semantic vectors must contain finite values")
		}
		nonzero = nonzero || value != 0
	}
	if !nonzero {
		return errors.New("semantic vectors must be nonzero")
	}
	return nil
}

func validHash(value string) bool {
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

func validLabel(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, c := range value {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

func validSimilarity(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}
