package semantic

import (
	"math"
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{Mode: "shadow", Family: "synthetic-classification", Domain: "fixture", EvidenceRevision: "r1", EmbeddingModel: "fixture:local"}
}

func validEntry() Entry {
	e := Entry{
		Partition: strings.Repeat("a", 64), ModelID: strings.Repeat("b", 64), Vector: []float32{1, 0},
		Source:    Source{Key: "inference/v1/jobs/" + strings.Repeat("c", 64), Version: 1, SHA256: strings.Repeat("d", 64), OutputSHA256: strings.Repeat("e", 64)},
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	e.ExpiresAt = e.CreatedAt.Add(time.Hour)
	e.ID = EntryID(e)
	return e
}

func TestConfigDefaultsAndBoundedBindings(t *testing.T) {
	c := validConfig()
	c.Bindings = map[string]string{"policy": "stable"}
	got, err := NormalizeConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	if got.MinSimilarity != .9 || got.TopK != 3 || got.MaxScan != 1000 || got.MaxEntries != 1000 || got.MaxInputBytes != 8192 || got.TimeoutMS != 10000 {
		t.Fatalf("wrong defaults: %+v", got)
	}
	c.Bindings["policy"] = "mutated"
	if got.Bindings["policy"] != "stable" {
		t.Fatal("normalized bindings alias caller's map")
	}
	cases := map[string]func(*Config){
		"serving mode":    func(c *Config) { c.Mode = "serve" },
		"empty mode":      func(c *Config) { c.Mode = "" },
		"empty family":    func(c *Config) { c.Family = "" },
		"oversize domain": func(c *Config) { c.Domain = strings.Repeat("x", 129) },
		"invalid UTF8":    func(c *Config) { c.EvidenceRevision = string([]byte{0xff}) },
		"control label":   func(c *Config) { c.Family = "a\nb" },
		"padded label":    func(c *Config) { c.Domain = " padded" },
		"cloud model":     func(c *Config) { c.EmbeddingModel = "fixture:cloud" },
		"threshold":       func(c *Config) { c.MinSimilarity = 1.1 },
		"NaN threshold":   func(c *Config) { c.MinSimilarity = math.NaN() },
		"topK":            func(c *Config) { c.TopK = 11 },
		"maxScan":         func(c *Config) { c.MaxScan = 10001 },
		"maxEntries":      func(c *Config) { c.MaxEntries = 10001 },
		"input limit":     func(c *Config) { c.MaxInputBytes = 32769 },
		"timeout":         func(c *Config) { c.TimeoutMS = 60001 },
		"negative limit":  func(c *Config) { c.TopK = -1 },
		"binding key":     func(c *Config) { c.Bindings = map[string]string{"": "value"} },
		"binding value":   func(c *Config) { c.Bindings = map[string]string{"key": strings.Repeat("x", 257)} },
		"binding count": func(c *Config) {
			c.Bindings = make(map[string]string)
			for i := range 33 {
				c.Bindings[string(rune('A'+i))] = "value"
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := validConfig()
			mutate(&c)
			if _, err := NormalizeConfig(c); err == nil {
				t.Fatal("invalid semantic config accepted")
			}
		})
	}
}

func TestCosineScoresNearMatchesAndRejectsInvalidVectors(t *testing.T) {
	near, err := Cosine([]float32{1, 0, 0}, []float32{.98, .1, 0})
	if err != nil || near < .99 || near >= 1 {
		t.Fatalf("near match = %v, %v", near, err)
	}
	far, err := Cosine([]float32{1, 0}, []float32{0, 1})
	if err != nil || far != 0 {
		t.Fatalf("orthogonal = %v, %v", far, err)
	}
	negative, err := Cosine([]float32{1}, []float32{-1})
	if err != nil || negative != -1 {
		t.Fatalf("opposite = %v, %v", negative, err)
	}
	large, err := Cosine([]float32{math.MaxFloat32, math.MaxFloat32}, []float32{math.MaxFloat32, math.MaxFloat32})
	if err != nil || math.Abs(large-1) > 1e-15 {
		t.Fatalf("large finite vector = %v, %v", large, err)
	}
	for _, vector := range [][]float32{nil, {0, 0}, {float32(math.NaN())}, {float32(math.Inf(1))}, make([]float32, 4097), {1, 0}} {
		if _, err := Cosine([]float32{1}, vector); err == nil {
			t.Fatalf("invalid/different vector accepted: dimension %d", len(vector))
		}
	}
}

func TestQueryAndSourceSnapshotValidation(t *testing.T) {
	e := validEntry()
	q := Query{Partition: e.Partition, ModelID: e.ModelID, Vector: e.Vector, MinSimilarity: .9, TopK: 3, MaxScan: 1000}
	if err := ValidateQuery(q); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Query){
		func(q *Query) { q.Partition = strings.ToUpper(q.Partition) },
		func(q *Query) { q.ModelID = "model" },
		func(q *Query) { q.TopK = 0 },
		func(q *Query) { q.MaxScan = 10001 },
		func(q *Query) { q.MinSimilarity = math.Inf(1) },
		func(q *Query) { q.Vector = []float32{0} },
	} {
		invalid := q
		mutate(&invalid)
		if err := ValidateQuery(invalid); err == nil {
			t.Fatal("invalid query accepted")
		}
	}
	if err := ValidatePut(PutRequest{Entry: e, MaxEntries: 1000}); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Entry){
		func(e *Entry) { e.ID = strings.Repeat("f", 64) },
		func(e *Entry) { e.Source.Key += "/child" },
		func(e *Entry) { e.Source.Version = 0 },
		func(e *Entry) { e.Source.OutputSHA256 = "bad" },
		func(e *Entry) { e.CreatedAt = time.Time{} },
		func(e *Entry) { e.ExpiresAt = e.CreatedAt },
		func(e *Entry) { e.ExpiresAt = e.CreatedAt.Add(24*time.Hour + time.Nanosecond) },
		func(e *Entry) { e.Vector = []float32{0} },
	} {
		invalid := e
		mutate(&invalid)
		if err := ValidatePut(PutRequest{Entry: invalid, MaxEntries: 1000}); err == nil {
			t.Fatal("invalid source snapshot accepted")
		}
	}
	changed := e
	changed.Source.Version++
	if EntryID(changed) == e.ID {
		t.Fatal("source version not bound to entry identity")
	}
	changed = e
	changed.Vector = []float32{.5, .5}
	changed.ExpiresAt = changed.CreatedAt.Add(2 * time.Hour)
	if EntryID(changed) != e.ID {
		t.Fatal("vector/expiry incorrectly create distinct source identity")
	}
}
