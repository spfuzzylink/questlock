package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/observability"
)

type telemetryRecord struct {
	execution observability.Execution
	quality   *bool
}

func langfuseFromEnvironment(enabled bool, timeout time.Duration) (*observability.Langfuse, error) {
	if !enabled {
		return nil, nil
	}
	endpoint := os.Getenv("QUESTLOCK_LANGFUSE_ENDPOINT")
	if endpoint == "" {
		base := os.Getenv("LANGFUSE_BASE_URL")
		if base == "" {
			return nil, errors.New("LANGFUSE_BASE_URL or QUESTLOCK_LANGFUSE_ENDPOINT is required with --langfuse")
		}
		endpoint = strings.TrimRight(base, "/") + "/api/public/otel/v1/traces"
	}
	return observability.NewLangfuse(observability.Config{Endpoint: endpoint, PublicKey: os.Getenv("LANGFUSE_PUBLIC_KEY"), SecretKey: os.Getenv("LANGFUSE_SECRET_KEY"), Timeout: timeout, Release: version})
}

func observation(spec inference.Spec, result inference.Result) observability.Execution {
	execution, _ := observability.FromInference(spec, result)
	return execution
}
func observedUsage(u inference.Usage) observability.Usage {
	return observability.Usage{InputTokens: u.InputTokens, UncachedInputTokens: u.UncachedInputTokens, CacheReadTokens: u.CacheReadTokens, CacheWriteTokens: u.CacheWriteTokens, OutputTokens: u.OutputTokens, ReasoningTokens: u.ReasoningTokens}
}

// Export happens after execution accounting and output. It never changes a
// model result, error or durable record. Reports contain IDs and controlled
// statuses, not backend errors, URLs, task labels or credential values.
func exportTelemetry(exporter *observability.Langfuse, records []telemetryRecord, output io.Writer) {
	exportTelemetryContext(context.Background(), exporter, records, output)
}

func exportTelemetryContext(parent context.Context, exporter *observability.Langfuse, records []telemetryRecord, output io.Writer) {
	if exporter == nil {
		return
	}
	for _, record := range records {
		if parent.Err() != nil {
			return
		}
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		receipt, err := exporter.Export(ctx, record.execution)
		status := "acknowledged"
		if err != nil {
			status = "unconfirmed"
		}
		scoreStatus := "not_requested"
		if err == nil && record.quality != nil {
			value := float64(0)
			if *record.quality {
				value = 1
			}
			scoreStatus = "acknowledged"
			if exporter.Score(ctx, receipt, value) != nil {
				scoreStatus = "unconfirmed"
			}
		}
		cancel()
		_ = writeModelJSON(output, struct {
			Langfuse struct {
				Status      string                `json:"status"`
				ScoreStatus string                `json:"score_status"`
				Receipt     observability.Receipt `json:"receipt"`
			} `json:"langfuse"`
		}{Langfuse: struct {
			Status      string                `json:"status"`
			ScoreStatus string                `json:"score_status"`
			Receipt     observability.Receipt `json:"receipt"`
		}{status, scoreStatus, receipt}})
	}
}
