package revision

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/evaluation"
)

func countBound(n int) *int { return &n }

func TestCountChecksSurviveCheckedRevisionPublicationAndReference(t *testing.T) {
	request := fixtureRequest()
	request.SourceChecks = []evaluation.Check{{ID: "source-words", Kind: "word_count", Min: countBound(6), Max: countBound(6)}}
	request.Checks = []evaluation.Check{
		{ID: "words", Kind: "word_count", Min: countBound(6), Max: countBound(6)},
		{ID: "paragraphs", Kind: "paragraph_count", Min: countBound(1), Max: countBound(1)},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(strings.NewReader(string(encoded)))
	if err != nil {
		t.Fatalf("length contract failed strict request load: %v", err)
	}
	broker := newMemoryBroker()
	runner := Runner{Broker: broker}
	first, err := runner.Run(testContext, loaded)
	if err != nil || !first.Durable || first.Checks[0].Observed == nil || *first.Checks[0].Observed != 6 {
		t.Fatalf("checked publication: %+v %v", first, err)
	}
	second, err := runner.Run(testContext, loaded)
	if err != nil || !second.Reused || second.Text != first.Text {
		t.Fatalf("counted exact revision reuse: %+v %v", second, err)
	}
	child := loaded
	child.Revision = "child-1"
	child.Source = Source{Key: first.Artifact.Key, Version: 1, SHA256: first.OutputSHA256}
	child.Edits = []Edit{{Old: "works.", New: "helps.", ExpectedMatches: 1}}
	third, err := runner.Run(testContext, child)
	if err != nil || !third.Durable || third.Text == first.Text {
		t.Fatalf("count evidence could not be read from parent: %+v %v", third, err)
	}
	changed := loaded
	changed.Checks = append([]evaluation.Check(nil), loaded.Checks...)
	changed.Checks[0].Max = countBound(7)
	digest, err := RequestDigest(changed)
	if err != nil || digest == first.RequestSHA256 {
		t.Fatal("count bound change did not change revision identity")
	}
}

func TestStoredCountEvidenceRequiresObservedCountAndEvaluatedStatus(t *testing.T) {
	for _, check := range []evaluation.CheckResult{
		{ID: "x", Kind: "word_count", Passed: true},
		{ID: "x", Kind: "word_count", Passed: true, Observed: countBound(-1)},
		{ID: "x", Kind: "word_count", Passed: true, Observed: countBound(evaluation.MaxCountTextBytes + 1)},
		{ID: "x", Kind: "word_count", Passed: true, Observed: countBound(1), Status: "not_evaluated"},
		{ID: "x", Kind: "contains", Passed: true, Observed: countBound(1)},
	} {
		if passingEvidence([]evaluation.CheckResult{check}) {
			t.Fatalf("malformed stored evidence accepted: %+v", check)
		}
	}
	for _, kind := range []string{"word_count", "character_count", "paragraph_count"} {
		if !passingEvidence([]evaluation.CheckResult{{ID: "x", Kind: kind, Passed: true, Observed: countBound(0)}}) {
			t.Fatalf("explicit observed zero rejected for %s", kind)
		}
	}
}
