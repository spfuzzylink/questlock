package observability

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestQualityScoreCurrentBatchContract(t *testing.T) {
	var captured []byte
	var calls atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/hosted/api/public/scores" {
			t.Errorf("unexpected score request %s %s", r.Method, r.URL.Path)
		}
		public, secret, ok := r.BasicAuth()
		if !ok || public != "fixture-public" || secret != "fixture-secret" {
			t.Error("score missing project auth")
		}
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"message":"Accepted"}`)
	}))
	defer s.Close()
	l, err := NewLangfuse(Config{Endpoint: s.URL + "/hosted/api/public/otel/v1/traces", PublicKey: "fixture-public", SecretKey: "fixture-secret"})
	if err != nil {
		t.Fatal(err)
	}
	receipt := Receipt{TraceID: strings.Repeat("a", 32), RootSpanID: strings.Repeat("b", 16)}
	if err := l.Score(context.Background(), receipt, 0.85); err != nil {
		t.Fatal(err)
	}
	var body []map[string]any
	if err := json.Unmarshal(captured, &body); err != nil || len(body) != 1 {
		t.Fatalf("scores request must be a single-element array: %s", captured)
	}
	if len(body[0]) != 5 || body[0]["name"] != "questlock.quality" || body[0]["traceId"] != receipt.TraceID || body[0]["observationId"] != receipt.RootSpanID || body[0]["dataType"] != "NUMERIC" || body[0]["value"] != 0.85 || calls.Load() != 1 {
		t.Fatalf("unexpected score payload: %s", captured)
	}
	if strings.Contains(string(captured), "fixture-secret") {
		t.Fatal("score payload leaked auth")
	}
}

func TestQualityScoreFailuresAreBoundedAndNeverRetried(t *testing.T) {
	for _, c := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"accepted", `{"message":"Accepted"}`, 202, nil},
		{"multistatus_accepted", `{"accepted":1,"rejected":0,"errors":[]}`, 207, nil},
		{"multistatus_rejected", `{"accepted":0,"rejected":1,"errors":[{"message":"private-score-error"}]}`, 207, ErrRejected},
		{"missing_count", `{"accepted":1}`, 207, ErrInvalidResponse},
		{"wrong_count", `{"accepted":0,"rejected":0,"errors":[]}`, 207, ErrInvalidResponse},
		{"wrong_ack", `{"message":"private-score-error"}`, 202, ErrInvalidResponse},
		{"oversized", strings.Repeat("x", maxResponseBytes+1), 202, ErrInvalidResponse},
		{"throttled", `{"message":"private-score-error"}`, 429, ErrUnavailable},
		{"legacy_status", `{"id":"legacy"}`, 200, ErrUnavailable},
	} {
		t.Run(c.name, func(t *testing.T) {
			var calls atomic.Int64
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(c.status)
				io.WriteString(w, c.body)
			}))
			defer s.Close()
			err := exporterForServer(t, s, 0).Score(context.Background(), Receipt{TraceID: strings.Repeat("a", 32), RootSpanID: strings.Repeat("b", 16)}, 1)
			if !errors.Is(err, c.want) || calls.Load() != 1 {
				t.Fatalf("err=%v want=%v requests=%d", err, c.want, calls.Load())
			}
			if err != nil && strings.Contains(err.Error(), "private-score-error") {
				t.Fatal("score error text leaked")
			}
		})
	}
}

func TestInvalidQualityAndReceiptNeverSent(t *testing.T) {
	var calls atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer s.Close()
	l := exporterForServer(t, s, 0)
	r := Receipt{TraceID: strings.Repeat("a", 32), RootSpanID: strings.Repeat("b", 16)}
	for _, value := range []float64{-0.1, 1.1, math.NaN(), math.Inf(1)} {
		if err := l.Score(context.Background(), r, value); !errors.Is(err, ErrInvalidExecution) {
			t.Fatalf("invalid value acknowledged: %v", err)
		}
	}
	for _, bad := range []Receipt{{}, {TraceID: "raw-user-marker", RootSpanID: r.RootSpanID}, {TraceID: r.TraceID, RootSpanID: strings.Repeat("0", 16)}} {
		if err := l.Score(context.Background(), bad, 0.5); !errors.Is(err, ErrInvalidExecution) {
			t.Fatalf("invalid receipt acknowledged: %v", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid score left worker")
	}
}
