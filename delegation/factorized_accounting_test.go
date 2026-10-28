package delegation

import (
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/semantic"
)

func TestFactorizedAccountingRejectsRepeatedGenerationAcrossDecisions(t *testing.T) {
	one, zero := int64(1), int64(0)
	work := func(stage, id string) semantic.VerificationWork {
		return semantic.VerificationWork{Stage: stage, ReceiptKey: "inference/v1/jobs/" + strings.Repeat(id, 64), OutputSHA256: strings.Repeat("f", 64), GenerationCalled: true, Complete: true, Durable: true, InputTokens: &one, OutputTokens: &one}
	}
	first := semantic.ValidityResult{Work: []semantic.VerificationWork{work("source", "a"), work("equivalence", "b")}}
	if validOutcomeVerificationWork([]semantic.ValidityResult{first, first}) {
		t.Fatal("one generation was charged twice through separate decisions")
	}
	cachedSource := first.Work[0]
	cachedSource.GenerationCalled, cachedSource.Reused = false, true
	cachedSource.InputTokens, cachedSource.OutputTokens = &zero, &zero
	second := semantic.ValidityResult{Work: []semantic.VerificationWork{cachedSource, work("equivalence", "c")}}
	if !validOutcomeVerificationWork([]semantic.ValidityResult{first, second}) {
		t.Fatal("a real zero-usage certificate hit was rejected")
	}
}
