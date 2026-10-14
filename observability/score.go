package observability

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"mime"
	"net/http"
	"strings"
)

// Score attaches an explicitly supplied numeric quality value to the root
// observation using Langfuse's current batch Scores API. It performs no model
// evaluation and sends no comments, outputs, or identity fields. Acknowledgement
// means asynchronous API acceptance; the score may not be visible immediately.
// A caller must only score a Receipt from a successfully acknowledged export.
func (l *Langfuse) Score(ctx context.Context, receipt Receipt, value float64) error {
	if l == nil || l.client == nil || ctx == nil {
		return ErrUnavailable
	}
	if !validID(receipt.TraceID, 16) || !validID(receipt.RootSpanID, 8) || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
		return ErrInvalidExecution
	}
	request := []struct {
		TraceID       string  `json:"traceId"`
		ObservationID string  `json:"observationId"`
		Name          string  `json:"name"`
		Value         float64 `json:"value"`
		DataType      string  `json:"dataType"`
	}{{TraceID: receipt.TraceID, ObservationID: receipt.RootSpanID, Name: "questlock.quality", Value: value, DataType: "NUMERIC"}}
	body, err := json.Marshal(request)
	if err != nil || len(body) > maxPayloadBytes {
		return ErrInvalidExecution
	}
	endpoint := strings.TrimSuffix(l.endpoint, "/otel/v1/traces") + "/scores"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ErrUnavailable
	}
	req.SetBasicAuth(l.publicKey, l.secretKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "questlock-otlp")
	response, err := l.client.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted && response.StatusCode != http.StatusMultiStatus {
		return ErrUnavailable
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" || response.ContentLength > maxResponseBytes {
		return ErrInvalidResponse
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return ErrInvalidResponse
	}
	if response.StatusCode == http.StatusAccepted {
		var ack struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &ack) != nil || ack.Message != "Accepted" {
			return ErrInvalidResponse
		}
		return nil
	}
	var ack struct {
		Accepted *int              `json:"accepted"`
		Rejected *int              `json:"rejected"`
		Errors   []json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(data, &ack) != nil || ack.Accepted == nil || ack.Rejected == nil || *ack.Accepted < 0 || *ack.Rejected < 0 || *ack.Accepted+*ack.Rejected != 1 {
		return ErrInvalidResponse
	}
	if *ack.Rejected != 0 {
		return ErrRejected
	}
	return nil
}

func validID(s string, bytes int) bool {
	digest, err := hex.DecodeString(s)
	if err != nil || len(digest) != bytes {
		return false
	}
	for _, b := range digest {
		if b != 0 {
			return true
		}
	}
	return false
}
