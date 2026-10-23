// Package retention provides explicit storage policies and an immutable local
// cold-content store. It does not choose which application state is safe to
// archive, delete archives, call providers, or schedule background work.
package retention

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/protocol"
)

var (
	ErrUnavailable = errors.New("retention_unavailable")
	ErrIntegrity   = errors.New("retention_integrity")
	ErrBudget      = errors.New("retention_budget")
	ErrInvalid     = errors.New("retention_invalid")
)

const maxConfigBytes = 64 << 10

// Config is a complete opt-in policy. MaxDatabaseBytes may be zero for an
// explicit unlimited hot database; cold storage always has a positive limit.
// Callers control when to run maintenance and whether to apply a proposed batch.
type Config struct {
	Schema                  int    `json:"schema"`
	ColdDir                 string `json:"cold_dir"`
	HotHistoryDays          int    `json:"hot_history_days"`
	KeepRecentVersions      int    `json:"keep_recent_versions"`
	ArchiveCompletedCurrent bool   `json:"archive_completed_current"`
	MaxDatabaseBytes        int64  `json:"max_database_bytes"`
	MaxColdBytes            int64  `json:"max_cold_bytes"`
	BatchItems              int    `json:"batch_items"`
	BatchBytes              int64  `json:"batch_bytes"`
	Adaptive                bool   `json:"adaptive"`
	MinFreeMemoryBytes      int64  `json:"min_free_memory_bytes"`
	MemoryPercent           int64  `json:"memory_percent"`
	MaxCPULoadMilli         int64  `json:"max_cpu_load_milli"`
}

func DefaultConfig(absColdDir string) Config {
	return Config{Schema: 1, ColdDir: absColdDir, HotHistoryDays: 90,
		KeepRecentVersions: 2, ArchiveCompletedCurrent: true,
		MaxDatabaseBytes: 256 << 20, MaxColdBytes: 2 << 30,
		BatchItems: 32, BatchBytes: 8 << 20, Adaptive: true,
		MinFreeMemoryBytes: 256 << 20, MemoryPercent: 2, MaxCPULoadMilli: 1200}
}

func validRoot(path string) bool {
	return utf8.ValidString(path) && len(path) <= 4096 && !strings.ContainsRune(path, 0) &&
		filepath.IsAbs(path) && filepath.Clean(path) == path && filepath.Dir(path) != path
}

func (c Config) Validate() error {
	if c.Schema != 1 || !validRoot(c.ColdDir) || c.HotHistoryDays < 0 || c.HotHistoryDays > 36500 ||
		c.KeepRecentVersions < 1 || c.KeepRecentVersions > 10000 ||
		c.MaxDatabaseBytes < 0 || c.MaxDatabaseBytes > 1<<50 || c.MaxDatabaseBytes != 0 && c.MaxDatabaseBytes < 16<<20 ||
		c.MaxColdBytes < 1 || c.MaxColdBytes > 1<<50 ||
		c.BatchItems < 1 || c.BatchItems > 1024 || c.BatchBytes < 1 || c.BatchBytes > 64<<20 ||
		c.MinFreeMemoryBytes < 0 || c.MinFreeMemoryBytes > 1<<50 || c.MemoryPercent < 1 || c.MemoryPercent > 25 ||
		c.MaxCPULoadMilli < 1 || c.MaxCPULoadMilli > 10000 {
		return ErrInvalid
	}
	return nil
}

// Decode accepts one complete, bounded JSON object. Duplicate, missing, unknown
// and null fields are rejected, including explicitly false policy switches.
func Decode(data []byte) (Config, error) {
	if len(data) > maxConfigBytes || !utf8.Valid(data) {
		return Config{}, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return Config{}, ErrInvalid
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || fields[key] != nil {
			return Config{}, ErrInvalid
		}
		var value json.RawMessage
		if err = d.Decode(&value); err != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return Config{}, ErrInvalid
		}
		fields[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return Config{}, ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return Config{}, ErrInvalid
	}
	shape, _ := json.Marshal(Config{})
	var required map[string]json.RawMessage
	_ = json.Unmarshal(shape, &required)
	if len(fields) != len(required) {
		return Config{}, ErrInvalid
	}
	for key := range required {
		if fields[key] == nil {
			return Config{}, ErrInvalid
		}
	}
	var c Config
	if protocol.DecodeDataJSON(data, &c) != nil || c.Validate() != nil {
		return Config{}, ErrInvalid
	}
	return c, nil
}

// Budget limits the next maintenance batch only. It does not change the
// eligibility rules for content or represent an OS resource reservation.
type Budget struct {
	Items    int    `json:"items"`
	Bytes    int64  `json:"bytes"`
	Deferred bool   `json:"deferred"`
	Reason   string `json:"reason"`
}

// Plan fails closed for invalid or stale adaptive signals. CPU load is
// normalized by the probe's load denominator, not mistaken for CPU percent.
// Memory estimates reserve headroom and allow overhead for compression and
// copies; they cannot guarantee allocation or enforce OS resource limits.
func Plan(c Config, x admission.Snapshot, now time.Time) Budget {
	deferWith := func(reason string) Budget { return Budget{Deferred: true, Reason: reason} }
	if c.Validate() != nil {
		return deferWith("invalid_configuration")
	}
	b := Budget{Items: c.BatchItems, Bytes: c.BatchBytes, Reason: "fixed_policy"}
	if !c.Adaptive {
		return b
	}
	if !x.Known || x.At.IsZero() || x.At.After(now) || x.CPUCapacityMilli < 1 || x.CPUCapacityMilli > 1e9 ||
		x.LoadCapacityMilli < 1 || x.LoadCapacityMilli > 1e9 || x.LoadMilli < 0 || x.LoadMilli > 1e12 ||
		x.AvailableMemoryBytes < 0 || x.CPUStallMilliPercent < 0 || x.CPUStallMilliPercent > 100000 ||
		x.MemoryStallMilliPercent < 0 || x.MemoryStallMilliPercent > 100000 ||
		(x.MemoryPressure != admission.Normal && x.MemoryPressure != admission.High && x.MemoryPressure != admission.Critical) {
		return deferWith("signals_unavailable")
	}
	if now.Sub(x.At) > 5*time.Second {
		return deferWith("signals_stale")
	}
	if x.MemoryPressure == admission.Critical || x.CPUStallMilliPercent >= 50000 || x.MemoryStallMilliPercent >= 10000 {
		return deferWith("critical_pressure")
	}
	if x.AvailableMemoryBytes <= c.MinFreeMemoryBytes {
		return deferWith("memory_headroom")
	}
	// Divide before multiplication to remain safe even for adversarial signals.
	spare := x.AvailableMemoryBytes - c.MinFreeMemoryBytes
	memory := (spare/100*c.MemoryPercent + spare%100*c.MemoryPercent/100) / 4
	if memory < 1 {
		return deferWith("memory_headroom")
	}
	b.Bytes = min(b.Bytes, memory)
	b.Reason = "adaptive_policy"
	if b.Bytes < c.BatchBytes {
		b.Items = max(1, int(int64(c.BatchItems)*b.Bytes/c.BatchBytes))
		b.Reason = "memory_scaled"
	}
	load := x.LoadMilli * 1000 / x.LoadCapacityMilli
	if load >= c.MaxCPULoadMilli*4 {
		return deferWith("cpu_pressure")
	}
	if load > c.MaxCPULoadMilli {
		b.Items = max(1, int(int64(b.Items)*c.MaxCPULoadMilli/load))
		b.Bytes = max(1, b.Bytes*c.MaxCPULoadMilli/load)
		b.Reason = "cpu_scaled"
	}
	if x.MemoryPressure == admission.High || x.CPUStallMilliPercent >= 20000 || x.MemoryStallMilliPercent >= 1000 {
		b.Items, b.Bytes = max(1, b.Items/2), max(1, b.Bytes/2)
		b.Reason = "pressure_scaled"
	}
	return b
}

// Event carries maintenance measurements only. It deliberately has no paths,
// content, digests, keys, tokens, exception messages or provider data.
type Event struct {
	Operation string    `json:"operation"`
	Outcome   string    `json:"outcome"`
	ErrorCode string    `json:"error_code,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	Items     int64     `json:"items"`
	Bytes     int64     `json:"bytes"`
}
