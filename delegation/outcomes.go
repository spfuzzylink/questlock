package delegation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/outcomes"
)

func NewOutcomeRunID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "run-" + hex.EncodeToString(random[:]), nil
}

func RecordOutcome(ctx context.Context, broker inference.Broker, request Request, result *Result, runID string) {
	task := outcomes.Task{ID: request.Spec.Task, Revision: request.Spec.Revision}
	if result.SemanticReuse != nil {
		for _, verification := range result.SemanticReuse.Verifications {
			if verification.SharedWork {
				result.OutcomeTracking = &OutcomeTracking{Status: "failed", RunID: runID, Task: task, ErrorCategory: "shared_work_requires_batch_accounting"}
				return
			}
		}
	}
	record := OutcomeRecord(request, *result, runID)
	tracking := &OutcomeTracking{Status: "failed", RunID: runID, Task: task, Record: &record}
	result.OutcomeTracking = tracking
	// Keep a bounded metadata reconciliation opportunity even when the model
	// deadline or caller cancellation has elapsed. No model calls occur here.
	registration, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	receipt, err := (outcomes.Store{Broker: broker}).Record(registration, task, record)
	if err == nil {
		tracking.Status, tracking.Receipt, tracking.Record = "recorded", &receipt, nil
	}
}

func OutcomeRecord(request Request, result Result, runID string) outcomes.Run {
	record := outcomes.Run{ID: runID, RequestDigest: result.RequestSHA256, Status: result.Status, Attempts: []outcomes.Attempt{}}
	for _, attempt := range result.Attempts {
		answer := attempt.Result
		item := outcomes.Attempt{TierID: attempt.TierID, Fingerprint: answer.Fingerprint, OutputSHA256: attempt.OutputSHA256,
			CheckPassed: attempt.QualityPassed, Complete: answer.Response.Complete, Durable: answer.Durable, Reused: answer.Reused,
			CostBasis: "unknown"}
		var spec inference.Spec
		if request.Policy.Local.ID == attempt.TierID {
			spec = request.Policy.Local.Spec
		} else if request.Policy.Escalation != nil && request.Policy.Escalation.ID == attempt.TierID {
			spec = request.Policy.Escalation.Spec
		}
		zero := float64(0)
		if answer.Measurement != nil {
			measurement := answer.Measurement
			item.GenerationCalled = measurement.GenerationCalled
			if item.Fingerprint == "" {
				item.Fingerprint = measurement.Fingerprint
			}
			if measurement.KnownGenerationUsage && measurement.GenerationUsage != nil {
				item.UsageKnown, item.Usage = true, *measurement.GenerationUsage
			}
		}
		switch {
		case answer.Reused:
			item.GenerationCalled, item.UsageKnown, item.Usage = false, true, inference.Usage{}
			item.EstimatedCostUSD, item.CostBasis = &zero, "reuse_zero"
		case !item.GenerationCalled:
			item.UsageKnown, item.Usage = true, inference.Usage{}
			item.EstimatedCostUSD, item.CostBasis = &zero, "no_generation"
		case inference.SelfHosted(spec):
			item.EstimatedCostUSD, item.CostBasis = &zero, "local_token_charge"
		case item.UsageKnown && spec.Prices != nil:
			// Match Runner's conservative micro-dollar rounding and exclusive
			// cache categories. Reasoning is already included in output tokens.
			price := spec.Prices
			usage := item.Usage
			micros := float64(usage.UncachedInputTokens)*price.Input + float64(usage.CacheReadTokens)*price.CacheRead +
				float64(usage.CacheWriteTokens)*price.CacheWrite + float64(usage.OutputTokens)*price.Output
			if !math.IsNaN(micros) && !math.IsInf(micros, 0) && micros >= 0 && micros <= float64(math.MaxInt64)/2 {
				cost := math.Ceil(micros) / 1000000
				item.EstimatedCostUSD, item.CostBasis = &cost, "caller_estimate"
			}
		}
		record.Attempts = append(record.Attempts, item)
	}
	if result.SemanticReuse != nil {
		for i, verification := range result.SemanticReuse.Verifications {
			if verification.SharedWork {
				// Even a cancellation before a receipt is available must fail
				// registration through this per-member helper.
				record.VerificationWork = append(record.VerificationWork, outcomes.Attempt{TierID: "reuse-verifier-shared", CostBasis: "shared_work_requires_batch_accounting"})
				continue
			}
			fingerprint := strings.TrimPrefix(verification.ReceiptKey, "inference/v1/jobs/")
			if fingerprint == "" {
				continue
			} // No started inference job to register.
			zero := float64(0)
			item := outcomes.Attempt{TierID: "reuse-verifier-" + strconv.Itoa(i+1), Fingerprint: fingerprint, GenerationCalled: verification.GenerationCalled, Reused: verification.Reused, CostBasis: "local_token_charge", EstimatedCostUSD: &zero, Complete: verification.Complete, Durable: verification.Durable, CheckPassed: verification.Accepted(), OutputSHA256: verification.OutputSHA256}
			if verification.InputTokens != nil && verification.OutputTokens != nil {
				item.UsageKnown = true
				item.Usage = inference.Usage{InputTokens: *verification.InputTokens, UncachedInputTokens: *verification.InputTokens, OutputTokens: *verification.OutputTokens}
			}
			if verification.Reused {
				item.CostBasis = "reuse_zero"
			}
			if !verification.GenerationCalled && !verification.Reused {
				item.UsageKnown = true
				item.Usage = inference.Usage{}
				item.CostBasis = "no_generation"
			}
			record.VerificationWork = append(record.VerificationWork, item)
		}
	}
	return record
}
