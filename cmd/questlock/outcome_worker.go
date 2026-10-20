package main

import (
	"context"
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/outcomes"
)

func newOutcomeRunID() (string, error) { return delegation.NewOutcomeRunID() }
func recordWorkerOutcome(ctx context.Context, broker inference.Broker, request delegation.Request, result *delegation.Result, runID string) {
	delegation.RecordOutcome(ctx, broker, request, result, runID)
}
func workerOutcomeRecord(request delegation.Request, result delegation.Result, runID string) outcomes.Run {
	return delegation.OutcomeRecord(request, result, runID)
}
