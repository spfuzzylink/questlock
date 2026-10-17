package admission

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"regexp"
	"time"
)

var laneName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

func LoadConfig(r io.Reader) (Config, error) {
	b, err := io.ReadAll(io.LimitReader(r, (64<<10)+1))
	if err != nil || len(b) > 64<<10 {
		return Config{}, ErrConfiguration
	}
	unique := json.NewDecoder(bytes.NewReader(b))
	unique.UseNumber()
	value, err := configValue(unique, 0)
	if err != nil {
		return Config{}, ErrConfiguration
	}
	object, ok := value.(map[string]any)
	if !ok || !completeObject(object, Config{}) {
		return Config{}, ErrConfiguration
	}
	lanes, ok := object["lanes"].([]any)
	if !ok {
		return Config{}, ErrConfiguration
	}
	for _, lane := range lanes {
		object, ok := lane.(map[string]any)
		if !ok || !completeObject(object, Lane{}) {
			return Config{}, ErrConfiguration
		}
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	var c Config
	if d.Decode(&c) != nil {
		return Config{}, ErrConfiguration
	}
	var extra any
	if d.Decode(&extra) != io.EOF || c.Validate() != nil {
		return Config{}, ErrConfiguration
	}
	return c, nil
}

func completeObject(object map[string]any, shape any) bool {
	b, _ := json.Marshal(shape)
	expected := map[string]json.RawMessage{}
	_ = json.Unmarshal(b, &expected)
	if len(object) != len(expected) {
		return false
	}
	for key := range expected {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	return true
}

func configValue(d *json.Decoder, depth int) (any, error) {
	if depth > 16 {
		return nil, ErrConfiguration
	}
	token, err := d.Token()
	if err != nil || token == nil {
		return nil, ErrConfiguration
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		object := map[string]any{}
		for d.More() {
			token, err := d.Token()
			if err != nil {
				return nil, err
			}
			key, ok := token.(string)
			if !ok {
				return nil, ErrConfiguration
			}
			if _, ok := object[key]; ok {
				return nil, ErrConfiguration
			}
			v, err := configValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			object[key] = v
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, ErrConfiguration
		}
		return object, nil
	case '[':
		array := []any{}
		for d.More() {
			v, err := configValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, v)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, ErrConfiguration
		}
		return array, nil
	}
	return nil, ErrConfiguration
}

func (c Config) Validate() error {
	if c.MinWorkers < 0 || c.MaxWorkers < 1 || c.MaxWorkers > 1024 || c.MinWorkers > c.MaxWorkers ||
		c.MaxQueueRows < 2 || c.MaxQueueRows > 4096 || c.MaxQueueBytes < 1024 || c.MaxQueueBytes > 1<<30 ||
		c.ProtectedQueueRows < 1 || c.ProtectedQueueRows >= c.MaxQueueRows || c.ProtectedQueueBytes < QueueEntryBytes || c.ProtectedQueueBytes >= c.MaxQueueBytes ||
		c.ProtectedCPUMilli < 1 || c.ProtectedCPUMilli > 1e9 || c.ProtectedMemoryBytes < 1 || c.MemoryHeadroomBytes < 0 || c.MemoryHeadroomBytes > math.MaxInt64-c.ProtectedMemoryBytes ||
		c.QueueMemoryPercent < 1 || c.QueueMemoryPercent > 25 || c.QueueRowsPerWorker < 1 || c.QueueRowsPerWorker > 64 ||
		c.RecoverySamples < 1 || c.RecoverySamples > 100 || c.SampleInterval < 10*time.Millisecond || c.SampleInterval > time.Minute ||
		c.RampInterval < c.SampleInterval || c.RampInterval > 10*time.Minute || c.MaxSampleAge < c.SampleInterval || c.MaxSampleAge > 10*time.Minute ||
		c.ProbeTimeout <= 0 || c.ProbeTimeout > 30*time.Second || c.TaskTimeout <= 0 || c.TaskTimeout > 10*time.Minute ||
		c.CPULoadLowMilli < 0 || c.CPULoadHighMilli <= c.CPULoadLowMilli || c.CPULoadHighMilli > 10000 ||
		!thresholds(c.CPUStallLowMilliPercent, c.CPUStallHighMilliPercent) || !thresholds(c.MemoryStallLowMilliPercent, c.MemoryStallHighMilliPercent) || len(c.Lanes) < 2 || len(c.Lanes) > 16 {
		return ErrConfiguration
	}
	seen := map[string]bool{}
	protectedWorkers := 0
	var cpu, mem int64
	for _, l := range c.Lanes {
		if !laneName.MatchString(l.Name) || seen[l.Name] || l.CPUMilli < 1 || l.CPUMilli > 1e9 || l.MemoryBytes < 1 || l.MemoryBytes > math.MaxInt64/1024 || l.MaxWorkers < 1 || l.MaxWorkers > c.MaxWorkers {
			return ErrConfiguration
		}
		seen[l.Name] = true
		if l.Protected {
			protectedWorkers += l.MaxWorkers
			if cpu > math.MaxInt64-l.CPUMilli*int64(l.MaxWorkers) || mem > math.MaxInt64-l.MemoryBytes*int64(l.MaxWorkers) {
				return ErrConfiguration
			}
			cpu += l.CPUMilli * int64(l.MaxWorkers)
			mem += l.MemoryBytes * int64(l.MaxWorkers)
		}
	}
	if protectedWorkers < 1 || protectedWorkers >= c.MaxWorkers || c.MinWorkers > c.MaxWorkers-protectedWorkers || cpu > c.ProtectedCPUMilli || mem > c.ProtectedMemoryBytes-c.ProtectedQueueBytes {
		return ErrConfiguration
	}
	return nil
}
func thresholds(low, high int64) bool { return low >= 0 && high > low && high <= 100000 }
