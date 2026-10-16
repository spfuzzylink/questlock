package observability

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRuntimeCacheIsComputeMetadata(t *testing.T) {
	e := sampleExecution()
	e.Provider = "sglang"
	count := int64(50)
	e.Generation.RuntimeCache = &RuntimeCache{Profile: "hicache", CachedTokens: &count}
	if err := validateExecution(e); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal((&Langfuse{}).envelope(e, Receipt{}))
	if err != nil {
		t.Fatal(err)
	}
	spans, _ := capturedSpans(t, data)
	attrs := attributes(t, spans[1])
	if attrs[metadataPrefix+"runtime_cache_cached_tokens"] != "50" || attrs[metadataPrefix+"runtime_cache_host_known"] != false {
		t.Fatal("unknown tier should remain unknown")
	}
	if _, ok := attrs[metadataPrefix+"runtime_cache_host_tokens"]; ok {
		t.Fatal("invented host usage")
	}
	if attrs["langfuse.observation.usage_details"] == nil || !strings.Contains(attrs["langfuse.observation.usage_details"].(string), `"input_cached_tokens":30`) {
		t.Fatal("runtime cache changed billing buckets")
	}
}

func TestRuntimeCacheRejectsInvalidMetadata(t *testing.T) {
	for _, name := range []string{"profile", "negative", "over-input", "over-total", "nil-total-over-input", "full-breakdown-mismatch", "provider", "unknown-usage"} {
		t.Run(name, func(t *testing.T) {
			e := sampleExecution()
			e.Provider = "sglang"
			cached, host := int64(40), int64(20)
			e.Generation.RuntimeCache = &RuntimeCache{Profile: "hicache", CachedTokens: &cached, HostTokens: &host}
			switch name {
			case "profile":
				e.Generation.RuntimeCache.Profile = "private-marker"
			case "negative":
				host = -1
			case "over-input":
				cached = 101
			case "over-total":
				host = 41
			case "nil-total-over-input":
				e.Generation.RuntimeCache.CachedTokens = nil
				device := int64(100)
				e.Generation.RuntimeCache.DeviceTokens = &device
			case "full-breakdown-mismatch":
				device, storage := int64(1), int64(1)
				e.Generation.RuntimeCache.DeviceTokens, e.Generation.RuntimeCache.StorageTokens = &device, &storage
			case "provider":
				e.Provider = "openrouter"
			case "unknown-usage":
				e.Generation.KnownUsage = false
			}
			if err := validateExecution(e); err == nil {
				t.Fatal("invalid runtime evidence admitted")
			}
		})
	}
}
