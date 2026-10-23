package observability

import (
	"context"
	"encoding/json"

	"github.com/spfuzzylink/questlock/retention"
)

// ExportStorage exports one content-free maintenance or archived-read event.
// Storage spans have no generation, token usage, cost, artifact identifiers,
// filesystem paths, checksums or raw errors. Export never retries an operation.
func (l *Langfuse) ExportStorage(ctx context.Context, event retention.Event) (Receipt, error) {
	if l == nil || l.client == nil || ctx == nil {
		return Receipt{}, ErrUnavailable
	}
	if !validStorageEvent(event) {
		return Receipt{}, ErrInvalidExecution
	}
	receipt, err := newReceipt(false)
	if err != nil {
		return Receipt{}, ErrUnavailable
	}
	body, err := json.Marshal(l.storageEnvelope(event, receipt))
	if err != nil || len(body) > maxPayloadBytes {
		return receipt, ErrInvalidExecution
	}
	return receipt, l.sendTraces(ctx, body)
}

func validStorageEvent(e retention.Event) bool {
	if !validInterval(e.StartedAt, e.EndedAt) || e.Items < 0 || e.Bytes < 0 {
		return false
	}
	switch e.Operation {
	case "archive", "restore", "verify", "compact", "configure", "read", "history":
	default:
		return false
	}
	switch e.Outcome {
	case "complete", "dry_run", "deferred", "error":
	default:
		return false
	}
	switch e.ErrorCode {
	case "", "storage_error", "archive_unavailable", "archive_corrupt", "capacity", "canceled", "invalid":
	default:
		return false
	}
	return e.ErrorCode == "" || e.Outcome == "error"
}

func (l *Langfuse) storageEnvelope(e retention.Event, r Receipt) otlpEnvelope {
	attrs := []otlpAttribute{
		stringAttribute("langfuse.trace.name", "questlock.storage"),
		stringAttribute("langfuse.observation.type", "span"),
		boolAttribute("langfuse.trace.public", false),
		stringAttribute(metadataPrefix+"storage_operation", e.Operation),
		stringAttribute(metadataPrefix+"storage_outcome", e.Outcome),
		intAttribute(metadataPrefix+"storage_items", e.Items),
		intAttribute(metadataPrefix+"storage_bytes", e.Bytes),
		floatAttribute(metadataPrefix+"storage_duration_ms", milliseconds(e.StartedAt, e.EndedAt)),
	}
	if e.ErrorCode != "" {
		attrs = append(attrs, stringAttribute(metadataPrefix+"storage_error_code", e.ErrorCode))
	}
	resourceAttrs := []otlpAttribute{stringAttribute("service.name", "questlock")}
	if l.release != "" {
		attrs = append(attrs, stringAttribute("langfuse.release", l.release))
		resourceAttrs = append(resourceAttrs, stringAttribute("service.version", l.release))
	}
	outcome := Complete
	if e.Outcome == "error" {
		outcome = Failed
	} else if e.Outcome == "deferred" {
		outcome = Pending
	}
	root := span(r.TraceID, r.RootSpanID, "", "questlock.storage", 1, e.StartedAt, e.EndedAt, outcome, attrs)
	return otlpEnvelope{ResourceSpans: []otlpResourceSpans{{
		Resource:   otlpResource{Attributes: resourceAttrs},
		ScopeSpans: []otlpScopeSpans{{Scope: otlpScope{Name: "github.com/spfuzzylink/questlock/observability", Version: l.release}, Spans: []otlpSpan{root}}},
	}}}
}
