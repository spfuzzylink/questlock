package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/inference"
)

func TestMeasurementCustomCasesAreExplicitPrivateAndBounded(t *testing.T) {
	data, set, err := loadMeasurementCases("")
	if err != nil || !bytes.Equal(data, casesJSON) || len(set.Cases) != 6 {
		t.Fatal("embedded default changed", err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cases.json")
	custom := bytes.Replace(casesJSON, []byte(`"semantic-business-measurement-1"`), []byte(`"custom-approved-snapshot"`), 1)
	if err := os.WriteFile(path, custom, 0600); err != nil {
		t.Fatal(err)
	}
	got, loaded, err := loadMeasurementCases(path)
	if err != nil || !bytes.Equal(got, custom) || loaded.Revision != "custom-approved-snapshot" {
		t.Fatal("custom case data lost", err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err == nil {
		if _, _, err := loadMeasurementCases(link); err == nil {
			t.Fatal("symlink accepted")
		}
	}
	if _, _, err := loadMeasurementCases("relative.json"); err == nil {
		t.Fatal("relative path accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadMeasurementCases(path); err == nil {
		t.Fatal("public file accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte(" "), (1<<20)+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadMeasurementCases(path); err == nil {
		t.Fatal("oversized case file accepted")
	}
	// Help/default execution must not open even an explicitly supplied bad path.
	if err := run([]string{"--cases-file", "/unavailable/cases.json"}, &bytes.Buffer{}); err != nil {
		t.Fatal("default path performed file access", err)
	}
}

func TestMeasurementCustomCasesRequireWholeBusinessChecks(t *testing.T) {
	for _, mutate := range []func(*caseSet){
		func(s *caseSet) { s.Schema = 2 },
		func(s *caseSet) { s.Revision = "" },
		func(s *caseSet) { s.Cases = s.Cases[:5] },
		func(s *caseSet) { s.Cases[1].ID = s.Cases[0].ID },
		func(s *caseSet) { s.Cases[0].Paraphrases = s.Cases[0].Paraphrases[:1] },
		func(s *caseSet) { s.Cases[0].Paraphrases[0] = s.Cases[0].OriginalQuestion },
		func(s *caseSet) { s.Cases[0].Checks[1].Kind = "word_count" },
		func(s *caseSet) { s.Cases[0].Checks[1].Pointer = "/next_action" },
		func(s *caseSet) { s.Cases[0].Checks[1].Expected = json.RawMessage(`null`) },
		func(s *caseSet) { delete(s.Cases[0].OutputTypes, "account_id") },
		func(s *caseSet) { s.Cases[0].OutputTypes["account_id"] = "integer" },
		func(s *caseSet) { s.Cases[0].OutputTypes["account_id"] = "array" },
	} {
		var set caseSet
		if err := json.Unmarshal(casesJSON, &set); err != nil {
			t.Fatal(err)
		}
		mutate(&set)
		data, _ := json.Marshal(set)
		if _, err := decodeMeasurementCases(data); err == nil {
			t.Fatal("invalid business snapshot accepted")
		}
	}
	for _, data := range [][]byte{
		bytes.Replace(casesJSON, []byte(`"schema": 1`), []byte(`"schema": 1, "schema": 1`), 1),
		append(append([]byte{}, casesJSON...), []byte(` {}`)...),
	} {
		if _, err := decodeMeasurementCases(data); err == nil {
			t.Fatal("ambiguous/trailing case data accepted")
		}
	}
}

func TestMeasurementBudgetsRejectBeforeCredentialsCasesOrModels(t *testing.T) {
	base := []string{"--live", "--broker", "http://127.0.0.1:1", "--ollama", "http://127.0.0.1:1", "--cases-file", "/unavailable/cases.json"}
	for _, args := range [][]string{
		{"--daily-input-tokens", "0"}, {"--daily-output-tokens", "-1"}, {"--daily-total-tokens", "1000000000001"},
		{"--max-duration", "0s"}, {"--max-duration", "2h1ms"}, {"--max-duration", "1ns"}, {"--max-duration", "1.5ms"},
		{"--answer-max-output-tokens", "0"}, {"--answer-max-output-tokens", "4097"},
	} {
		err := run(append(append([]string{}, base...), args...), &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "invalid measurement budget") {
			t.Fatal("invalid bounds reached file/provider access", args, err)
		}
	}
	for _, key := range []string{"--daily-input-tokens", "--daily-output-tokens", "--daily-total-tokens", "--max-duration", "--answer-max-output-tokens"} {
		value := "1"
		if key == "--max-duration" {
			value = "1s"
		}
		if err := run([]string{"--report", "/unavailable/log", key, value}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "without live-run options") {
			t.Fatal("report accepted replacement live limits", key, err)
		}
	}
}

func TestMeasurementAnswerCeilingMatchesAllPhasesAndPreservesDefault(t *testing.T) {
	set, err := decodeMeasurementCases(casesJSON)
	if err != nil {
		t.Fatal(err)
	}
	budget := &inference.TokenBudget{InputTokens: 600000, OutputTokens: 80000, TotalTokens: 680000}
	for _, c := range set.Cases {
		for step := 0; step < 9; step++ {
			phase, old := measurementStep(set.Revision, c, step, budget)
			newPhase, unchanged := measurementStepWithAnswerLimit(set.Revision, c, step, budget, 256)
			if phase != newPhase || !reflect.DeepEqual(old, unchanged) {
				t.Fatal("default request identity changed")
			}
			_, longer := measurementStepWithAnswerLimit(set.Revision, c, step, budget, 2048)
			if longer.Policy.Local.Spec.MaxOutputTokens != 2048 || longer.Policy.Local.Spec.DailyTokenBudget != budget || !reflect.DeepEqual(longer.Checks, old.Checks) || !reflect.DeepEqual(longer.Spec, old.Spec) {
				t.Fatal("long source/control drift")
			}
		}
	}
}

func TestMeasurementReportBindsCustomCasesAndExplicitBounds(t *testing.T) {
	log := fixtureMeasurement(t, fixtureRows(t, true), true)
	custom := append([]byte(" "), casesJSON...)
	customLog := replaceMeasurementHeader(t, log, func(h map[string]any) {
		h["cases_sha256"] = fixtureDigest(string(custom))
		h["max_duration_ms"] = 7200000
		h["answer_max_output_tokens"] = 2048
		h["daily_budget"] = inference.TokenBudget{InputTokens: 600000, OutputTokens: 80000, TotalTokens: 680000}
	})
	if _, err := summarizeMeasurementWithCases(strings.NewReader(customLog), custom); err != nil {
		t.Fatal("declared private suite/limits rejected", err)
	}
	if _, err := summarizeMeasurement(strings.NewReader(customLog)); err == nil {
		t.Fatal("custom report accepted against wrong suite")
	}
	for _, key := range []string{"max_duration_ms", "answer_max_output_tokens"} {
		for _, value := range []any{nil, 0, -1, "2048", 7200001} {
			bad := replaceMeasurementHeader(t, log, func(h map[string]any) { h[key] = value })
			if _, err := summarizeMeasurement(strings.NewReader(bad)); err == nil {
				t.Fatal("invalid explicit bounds accepted", key, value)
			}
		}
	}
}

func TestMeasurementReportRejectsMissingAndInvalidDeclaredBudget(t *testing.T) {
	log := fixtureMeasurement(t, fixtureRows(t, true), true)
	for _, value := range []any{
		nil,
		inference.TokenBudget{},
		map[string]any{"input_tokens": 524288, "output_tokens": 16384},
		map[string]any{"input_tokens": 524288, "output_tokens": nil, "total_tokens": 540672},
		map[string]any{"input_tokens": -1, "output_tokens": 16384, "total_tokens": 540672},
	} {
		bad := replaceMeasurementHeader(t, log, func(h map[string]any) { h["daily_budget"] = value })
		if _, err := summarizeMeasurement(strings.NewReader(bad)); err == nil {
			t.Fatal("invalid declared budget accepted", value)
		}
	}
	missing := replaceMeasurementHeader(t, log, func(h map[string]any) { delete(h, "daily_budget") })
	if _, err := summarizeMeasurement(strings.NewReader(missing)); err == nil {
		t.Fatal("missing required budget accepted")
	}
}
