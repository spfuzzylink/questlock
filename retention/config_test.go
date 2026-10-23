package retention

import (
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/admission"
)

func TestConfigDefaultsAndStrictRoundTrip(t *testing.T) {
	c := DefaultConfig(filepath.Join(t.TempDir(), "cold"))
	if c.Schema != 1 || c.HotHistoryDays != 90 || c.KeepRecentVersions != 2 || !c.ArchiveCompletedCurrent ||
		c.MaxDatabaseBytes != 256<<20 || c.MaxColdBytes != 2<<30 || c.BatchItems != 32 || c.BatchBytes != 8<<20 ||
		!c.Adaptive || c.MinFreeMemoryBytes != 256<<20 || c.MemoryPercent != 2 || c.MaxCPULoadMilli != 1200 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	c.Adaptive, c.ArchiveCompletedCurrent, c.MaxDatabaseBytes, c.HotHistoryDays = false, false, 0, 0
	data, _ := json.Marshal(c)
	decoded, err := Decode(data)
	if err != nil || decoded != c {
		t.Fatalf("explicit false/zero fields did not round trip: %+v %v", decoded, err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	for name := range fields {
		t.Run("required_"+name, func(t *testing.T) {
			copy := make(map[string]json.RawMessage, len(fields))
			for k, v := range fields {
				copy[k] = v
			}
			delete(copy, name)
			b, _ := json.Marshal(copy)
			if _, err := Decode(b); !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted missing %s: %v", name, err)
			}
			copy[name] = json.RawMessage("null")
			b, _ = json.Marshal(copy)
			if _, err := Decode(b); !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted null %s: %v", name, err)
			}
		})
	}
	for _, b := range [][]byte{
		[]byte(`{}`), []byte(`[]`), []byte(`null`), append(append([]byte(nil), data...), data...),
		[]byte(strings.Replace(string(data), `"schema":1`, `"schema":1,"schema":1`, 1)),
		[]byte(strings.Replace(string(data), `"schema":1`, `"schema":1,"\u0073chema":1`, 1)),
		[]byte(strings.Replace(string(data), `"schema":1`, `"Schema":1`, 1)),
		[]byte(strings.Replace(string(data), `"schema":1`, `"schema":1,"unknown":1`, 1)),
		[]byte(strings.Replace(string(data), `"schema":1`, `"schema":1.0`, 1)),
		[]byte(strings.Replace(string(data), `"adaptive":false`, `"adaptive":0`, 1)),
		[]byte(strings.Replace(string(data), `"cold_dir":`, `"cold_dir":"/bad\ud800", "other":`, 1)),
		append([]byte{'{', '"', 0xff}, data...), []byte(strings.Repeat(" ", maxConfigBytes+1)),
	} {
		if _, err := Decode(b); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted invalid config: %q %v", b, err)
		}
	}
}

func TestConfigBounds(t *testing.T) {
	base := DefaultConfig(filepath.Join(t.TempDir(), "cold"))
	for name, change := range map[string]func(*Config){
		"schema":            func(c *Config) { c.Schema = 2 },
		"relative":          func(c *Config) { c.ColdDir = "relative" },
		"root":              func(c *Config) { c.ColdDir = string(filepath.Separator) },
		"traversal":         func(c *Config) { c.ColdDir += "/../other" },
		"nul":               func(c *Config) { c.ColdDir += "\x00" },
		"days":              func(c *Config) { c.HotHistoryDays = -1 },
		"days_overflow":     func(c *Config) { c.HotHistoryDays = math.MaxInt },
		"recent":            func(c *Config) { c.KeepRecentVersions = 0 },
		"database":          func(c *Config) { c.MaxDatabaseBytes = 1 },
		"database_overflow": func(c *Config) { c.MaxDatabaseBytes = math.MaxInt64 },
		"cold":              func(c *Config) { c.MaxColdBytes = 0 },
		"cold_overflow":     func(c *Config) { c.MaxColdBytes = math.MaxInt64 },
		"items":             func(c *Config) { c.BatchItems = 0 },
		"items_overflow":    func(c *Config) { c.BatchItems = math.MaxInt },
		"bytes":             func(c *Config) { c.BatchBytes = 0 },
		"bytes_overflow":    func(c *Config) { c.BatchBytes = math.MaxInt64 },
		"headroom":          func(c *Config) { c.MinFreeMemoryBytes = -1 },
		"memory":            func(c *Config) { c.MemoryPercent = 26 },
		"cpu":               func(c *Config) { c.MaxCPULoadMilli = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			c := base
			change(&c)
			if !errors.Is(c.Validate(), ErrInvalid) {
				t.Fatalf("accepted invalid config: %+v", c)
			}
		})
	}
}

func healthySnapshot(at time.Time) admission.Snapshot {
	return admission.Snapshot{At: at, Known: true, CPUCapacityMilli: 8000, LoadCapacityMilli: 8000,
		LoadMilli: 1000, AvailableMemoryBytes: 8 << 30, MemoryPressure: admission.Normal}
}

func TestPlanFixedAdaptiveAndDefer(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c := DefaultConfig(filepath.Join(t.TempDir(), "cold"))
	x := healthySnapshot(now)
	full := Plan(c, x, now)
	if full.Deferred || full.Items != c.BatchItems || full.Bytes != c.BatchBytes {
		t.Fatal(full)
	}
	c.Adaptive = false
	if b := Plan(c, admission.Snapshot{}, now); b.Deferred || b.Items != c.BatchItems || b.Bytes != c.BatchBytes || b.Reason != "fixed_policy" {
		t.Fatal(b)
	}
	c.Adaptive = true
	for name, edit := range map[string]func(*admission.Snapshot){
		"unknown":      func(x *admission.Snapshot) { x.Known = false },
		"stale":        func(x *admission.Snapshot) { x.At = now.Add(-6 * time.Second) },
		"future":       func(x *admission.Snapshot) { x.At = now.Add(time.Nanosecond) },
		"zero":         func(x *admission.Snapshot) { x.At = time.Time{} },
		"critical":     func(x *admission.Snapshot) { x.MemoryPressure = admission.Critical },
		"headroom":     func(x *admission.Snapshot) { x.AvailableMemoryBytes = c.MinFreeMemoryBytes },
		"load":         func(x *admission.Snapshot) { x.LoadMilli = x.LoadCapacityMilli * 8 },
		"denominator":  func(x *admission.Snapshot) { x.LoadCapacityMilli = 0 },
		"capacity":     func(x *admission.Snapshot) { x.CPUCapacityMilli = 0 },
		"negative":     func(x *admission.Snapshot) { x.LoadMilli = -1 },
		"overflow":     func(x *admission.Snapshot) { x.LoadMilli = math.MaxInt64 },
		"pressure":     func(x *admission.Snapshot) { x.MemoryPressure = "invalid" },
		"cpu_stall":    func(x *admission.Snapshot) { x.CPUStallMilliPercent = 50000 },
		"memory_stall": func(x *admission.Snapshot) { x.MemoryStallMilliPercent = 10000 },
	} {
		t.Run(name, func(t *testing.T) {
			x := healthySnapshot(now)
			edit(&x)
			b := Plan(c, x, now)
			if !b.Deferred || b.Items != 0 || b.Bytes != 0 || b.Reason == "" {
				t.Fatal(b)
			}
		})
	}
	for name, edit := range map[string]func(*admission.Snapshot){
		"memory":   func(x *admission.Snapshot) { x.AvailableMemoryBytes = c.MinFreeMemoryBytes + 16<<20 },
		"cpu":      func(x *admission.Snapshot) { x.LoadMilli = x.LoadCapacityMilli * 2 },
		"pressure": func(x *admission.Snapshot) { x.MemoryPressure = admission.High },
	} {
		t.Run(name, func(t *testing.T) {
			x := healthySnapshot(now)
			edit(&x)
			b := Plan(c, x, now)
			if b.Deferred || b.Items < 1 || b.Items >= c.BatchItems || b.Bytes < 1 || b.Bytes >= c.BatchBytes {
				t.Fatal(b)
			}
		})
	}
	// A small effective cgroup CPU quota must not accidentally normalize host
	// load by that quota instead of the host load denominator.
	x.CPUCapacityMilli = 250
	if b := Plan(c, x, now); b.Deferred || b.Items != full.Items || b.Bytes != full.Bytes {
		t.Fatal(b)
	}
	x.AvailableMemoryBytes = math.MaxInt64
	if b := Plan(c, x, now); b.Deferred || b.Bytes > c.BatchBytes || b.Items > c.BatchItems {
		t.Fatal(b)
	}
	c.MemoryPercent = 0
	if b := Plan(c, x, now); !b.Deferred {
		t.Fatal("invalid policy obtained budget")
	}
}
