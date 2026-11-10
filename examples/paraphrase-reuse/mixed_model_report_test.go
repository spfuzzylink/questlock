package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestMeasurementSeparatesMixedVerifierModels(t *testing.T) {
	rows := fixtureRows(t)
	h := measurementHeader{Schema: "questlock.paraphrase-measurement.v1", CasesSHA256: fixtureDigest(string(casesJSON)), Model: "fixture", VerifierModel: "other-tokenizer", VerifierPairModel: "pair-tokenizer", VerifierPairMaxInputTokens: 6400, VerifierPolicy: "factorized-v1", MinSimilarity: .25, DailyBudget: fixtureDailyBudget(), FreshBaseline: true}
	for _, r := range rows {
		if r.Result.SemanticReuse != nil {
			for j := range r.Result.SemanticReuse.Verifications {
				v := &r.Result.SemanticReuse.Verifications[j]
				v.MixedModelUsage = len(v.Work) == 2
				for k := range v.Work {
					w := &v.Work[k]
					w.Model, w.RuntimeIdentity = h.VerifierModel, fixtureDigest("source-runtime")
					if k == 1 {
						w.Model, w.RuntimeIdentity = h.VerifierPairModel, fixtureDigest("pair-runtime")
					}
				}
			}
		}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	if err := enc.Encode(h); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	_ = enc.Encode(map[string]any{"phase": "complete", "known_quality_or_reuse_failures": false})
	report, err := summarizeMeasurement(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	p := report.Phases["paraphrase"]
	if !p.MixedModelUsage || p.VerificationByModel[h.VerifierModel].Calls != 6 || p.VerificationByModel[h.VerifierPairModel].Calls != 12 || p.VerificationByModel[h.VerifierModel].KnownInput != 60 || p.VerificationByModel[h.VerifierPairModel].KnownInput != 120 || !strings.Contains(report.Comparison, "reservation-accounting sums") {
		t.Fatalf("mixed tokenizer work was collapsed: %+v", p)
	}
	for _, r := range rows {
		if r.Result.SemanticReuse != nil && len(r.Result.SemanticReuse.Verifications) > 0 {
			r.Result.SemanticReuse.Verifications[0].Work[1].RuntimeIdentity = fixtureDigest("source-runtime")
			break
		}
	}
	b.Reset()
	enc = json.NewEncoder(&b)
	_ = enc.Encode(h)
	for _, r := range rows {
		_ = enc.Encode(r)
	}
	if _, err = summarizeMeasurement(strings.NewReader(b.String())); err == nil {
		t.Fatal("different model names shared one runtime identity")
	}
}
