package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestMeasurementAssessmentOptionsAndHeaderCompatibility(t *testing.T) {
	c, err := measurementVerifierConfigWithAssessment("fixture:local", false, false, true, 0, 60000, .1)
	if err != nil || !c.VerifierPairAssessment || c.VerifierPairThink || c.VerifierMaxOutputTokens != 512 || c.VerifierTimeoutMS != 60000 {
		t.Fatalf("assessment options lost: %+v %v", c, err)
	}
	if _, err := measurementVerifierConfigWithAssessment("fixture:local", false, true, true, 0, 60000, .1); err == nil {
		t.Fatal("double pair reasoning accepted")
	}
	log := fixtureMeasurement(t, fixtureRows(t, true), true)
	old, err := summarizeMeasurement(strings.NewReader(log))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{true, false} {
		report, err := summarizeMeasurement(strings.NewReader(replaceMeasurementHeader(t, log, func(h map[string]any) { h["verifier_pair_assessment"] = value })))
		if err != nil || !reflect.DeepEqual(report, old) {
			t.Fatal("assessment metadata altered recorded work", err)
		}
	}
	for _, value := range []any{nil, "true", 1} {
		if _, err := summarizeMeasurement(strings.NewReader(replaceMeasurementHeader(t, log, func(h map[string]any) { h["verifier_pair_assessment"] = value }))); err == nil {
			t.Fatal("invalid assessment bool accepted")
		}
	}
	if err := run([]string{"--report", "missing", "--verifier-pair-assessment"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "without live-run options") {
		t.Fatal("offline report accepted replacement assessment policy")
	}
	args := []string{"--live", "--broker", "http://127.0.0.1:1", "--ollama", "http://127.0.0.1:1", "--verifier-pair-assessment", "--verifier-pair-think"}
	if err := run(args, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "invalid bounded semantic verifier options") {
		t.Fatal("invalid assessment policy accessed credentials/models")
	}
}
