package controlledquest

import (
	"context"
	"errors"
	"testing"
)

func TestSixControlledCasesThroughDurableBroker(t *testing.T) {
	report, err := Run(t.Context())
	if err != nil {
		t.Fatalf("controlled workflow: %v; report: %+v", err, report)
	}
	if report.Passed != 6 || report.Total != 6 || len(report.Cases) != 6 {
		t.Fatalf("six-domain coverage: %+v", report)
	}
	if report.Mode != "deterministic_workflow_fixture" || report.Verification != "declared_label_oracle_fixture" || report.PhysicalHosts != 1 || report.IsolatedClients != 2 || report.ProviderAPISpendUSD != 0 {
		t.Fatalf("fixture and topology boundaries: %+v", report)
	}
	if report.RequestRuns != 54 || report.ExactOnlyReferenceCalls != 42 || report.GenerationAvoidedBySemantic != 12 || report.GenerationAvoidedByExact != 12 {
		t.Fatalf("reference and actual call boundaries: %+v", report)
	}
	work := report.ActualFixtureWork
	if work.GenerationCalls != 30 || work.EmbeddingCalls != 42 || work.VerifierCalls < 24 {
		t.Fatalf("actual fixture work: %+v", work)
	}
	if work.GenerationInputTokens != 30*fixtureGenerationInput || work.GenerationOutputTokens != 30*fixtureGenerationOutput || work.EmbeddingInputTokens != 42*fixtureEmbeddingInput || work.VerifierInputTokens != work.VerifierCalls*fixtureVerifierInput || work.VerifierOutputTokens != work.VerifierCalls*fixtureVerifierOutput {
		t.Fatalf("fixture usage counters: %+v", work)
	}
	if report.HistoricalSourceUsageProxyTokens != 12*(fixtureGenerationInput+fixtureGenerationOutput) || report.ExactOnlyReferenceInputTokens != 42*fixtureGenerationInput || report.ExactOnlyReferenceOutputTokens != 42*fixtureGenerationOutput {
		t.Fatalf("declared reference and historical proxy usage: %+v", report)
	}
	for _, row := range report.Cases {
		if !row.Passed || !row.BrokerRestarted || len(row.Steps) != 9 {
			t.Fatalf("case boundaries: %+v", row)
		}
		for _, step := range row.Steps {
			if !step.Passed {
				t.Fatalf("case %s failed step: %+v", row.ID, step)
			}
		}
		if row.Steps[1].Work != (WorkCounts{}) || row.Steps[3].Work != (WorkCounts{}) {
			t.Fatalf("case %s exact reuse performed expensive work: %+v", row.ID, row.Steps)
		}
	}
}

func TestCanceledRunMakesNoFixtureCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	report, err := Run(ctx)
	if !errors.Is(err, context.Canceled) || report.RequestRuns != 0 || report.ActualFixtureWork != (WorkCounts{}) || report.Passed != 0 {
		t.Fatalf("canceled run: %+v, %v", report, err)
	}
}
