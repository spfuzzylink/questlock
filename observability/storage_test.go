package observability

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/retention"
)

func storageEvent() retention.Event {
	start := time.Unix(1_800_000_000, 0).UTC()
	return retention.Event{Operation: "archive", Outcome: "error", ErrorCode: "archive_corrupt", StartedAt: start, EndedAt: start.Add(20 * time.Millisecond), Items: 3, Bytes: 2048}
}

func TestStorageEventHasIndependentAllowlistedSpanWithoutGenerationOrContent(t *testing.T) {
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "host.name=private-storage-host")
	t.Setenv("QUESTLOCK_TOKEN", "private-storage-broker-token")
	var captured []byte
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		public, secret, ok := r.BasicAuth()
		if r.Method != http.MethodPost || r.URL.Path != "/api/public/otel/v1/traces" || !ok || public != "fixture-public" || secret != "fixture-secret" {
			t.Error("storage export changed the authenticated OTLP contract")
		}
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "{}")
	}))
	defer server.Close()
	receipt, err := exporterForServer(t, server, 0).ExportStorage(context.Background(), storageEvent())
	if err != nil || receipt.TraceID == "" || receipt.RootSpanID == "" || receipt.GenerationSpanID != "" || calls.Load() != 1 {
		t.Fatalf("storage receipt/call contract: %+v, %v, calls=%d", receipt, err, calls.Load())
	}
	spans, resource := capturedSpans(t, captured)
	if len(spans) != 1 || spans[0]["name"] != "questlock.storage" || spans[0]["parentSpanId"] != nil {
		t.Fatal("storage event created unrelated spans", spans)
	}
	attrs := attributes(t, spans[0])
	want := map[string]any{
		"langfuse.trace.name": "questlock.storage", "langfuse.observation.type": "span", "langfuse.trace.public": false,
		metadataPrefix + "storage_operation": "archive", metadataPrefix + "storage_outcome": "error",
		metadataPrefix + "storage_items": "3", metadataPrefix + "storage_bytes": "2048",
		metadataPrefix + "storage_duration_ms": float64(20), metadataPrefix + "storage_error_code": "archive_corrupt",
		"langfuse.release": "0.3.0-test", "langfuse.observation.level": "ERROR",
	}
	if len(attrs) != len(want) {
		t.Fatal("storage metadata expanded beyond its allowlist", attrs)
	}
	for name, value := range want {
		if attrs[name] != value {
			t.Fatalf("storage attribute %s=%v, want %v", name, attrs[name], value)
		}
	}
	if status := spans[0]["status"].(map[string]any); status["code"] != float64(2) || len(status) != 1 {
		t.Fatal("error span lost bounded status", status)
	}
	resourceAttrs := attributes(t, resource)
	if len(resourceAttrs) != 2 || resourceAttrs["service.name"] != "questlock" || resourceAttrs["service.version"] != "0.3.0-test" {
		t.Fatal("storage resource leaked ambient identity", resourceAttrs)
	}
	for _, marker := range []string{"fixture-public", "fixture-secret", "private-storage-host", "private-storage-broker-token", "generation", "usage_details", "cost_details", "fingerprint", "cold_dir"} {
		if strings.Contains(string(captured), marker) {
			t.Fatalf("storage export included disallowed field/content %q", marker)
		}
	}
}

func TestStorageExportRejectsUncontrolledStringsBeforeSending(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	exporter := exporterForServer(t, server, 0)
	mutations := []func(*retention.Event){
		func(e *retention.Event) { e.Operation = "/private/archive/path" },
		func(e *retention.Event) { e.Outcome = "private task label" },
		func(e *retention.Event) { e.ErrorCode = "private backend exception" },
		func(e *retention.Event) { e.Items = -1 },
		func(e *retention.Event) { e.Bytes = -1 },
		func(e *retention.Event) { e.StartedAt = time.Time{} },
		func(e *retention.Event) { e.EndedAt = e.StartedAt.Add(-time.Second) },
		func(e *retention.Event) { e.Outcome = "complete" },
	}
	for i, mutate := range mutations {
		event := storageEvent()
		mutate(&event)
		if _, err := exporter.ExportStorage(context.Background(), event); !errors.Is(err, ErrInvalidExecution) {
			t.Fatalf("invalid storage event %d accepted: %v", i, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid storage metadata reached the endpoint")
	}
}

func TestStorageExportSharedTransportAcknowledgementAndNoRetry(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"accepted", `{}`, http.StatusOK, nil},
		{"rejected", `{"partialSuccess":{"rejectedSpans":"1","errorMessage":"private-response-text"}}`, http.StatusOK, ErrRejected},
		{"unavailable", `private-response-text`, http.StatusServiceUnavailable, ErrUnavailable},
		{"malformed", `private-response-text`, http.StatusOK, ErrInvalidResponse},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				io.WriteString(w, test.body)
			}))
			defer server.Close()
			event := storageEvent()
			event.Operation, event.Outcome, event.ErrorCode = "history", "complete", ""
			receipt, err := exporterForServer(t, server, 0).ExportStorage(context.Background(), event)
			if !errors.Is(err, test.want) || receipt.RootSpanID == "" || receipt.GenerationSpanID != "" || calls.Load() != 1 {
				t.Fatalf("storage acknowledgement/retry mismatch: %+v %v calls=%d", receipt, err, calls.Load())
			}
			if err != nil && strings.Contains(err.Error(), "private-response-text") {
				t.Fatal("response text entered storage telemetry error")
			}
		})
	}
}
