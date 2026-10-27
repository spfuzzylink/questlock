package semanticquest

import (
	"context"
	"testing"
)

func TestSixShadowCasesThroughBroker(t *testing.T) {
	r, err := Run(context.Background(), nil)
	if err != nil || r.Passed != 6 || r.FixtureGenerationCalls != 18 || r.EmbeddingCalls != 18 || r.GenerationAvoidedBySemantic != 0 || r.Mode != "fixture" {
		t.Fatalf("shadow qualification: %+v %v", r, err)
	}
	if !r.Cases[5].BrokerRestarted {
		t.Fatal("lifecycle case did not reopen the broker")
	}
}
