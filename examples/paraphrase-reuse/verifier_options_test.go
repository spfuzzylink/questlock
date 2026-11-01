package main

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestMeasurementVerifierOptionsKeepDefaultsAndBoundExplicitReasoning(t *testing.T) {
	for _, tc := range []struct {
		name       string
		think      bool
		output     int64
		timeout    int
		similarity float64
		wantOutput int64
		valid      bool
	}{
		{"old defaults", false, 128, 60000, .25, 128, true},
		{"source policy default", true, 0, 120000, .1, 4096, true},
		{"explicit short reasoning cap", true, 128, 60000, .1, 128, true},
		{"maximum reasoning cap", true, 4096, 120000, 1, 4096, true},
		{"no-think ceiling", false, 512, 60000, .1, 512, true},
		{"no-think ceiling retained", false, 513, 60000, .1, 0, false},
		{"above reasoning ceiling", true, 4097, 120000, .1, 0, false},
		{"short stage cap", true, 31, 60000, .1, 0, false},
		{"negative timeout", true, 4096, -1, .1, 0, false},
		{"above timeout", true, 4096, 120001, .1, 0, false},
		{"negative similarity", true, 4096, 120000, -.1, 0, false},
		{"NaN similarity", true, 4096, 120000, math.NaN(), 0, false},
		{"infinite similarity", true, 4096, 120000, math.Inf(1), 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := measurementVerifierConfig("fixture:local", tc.think, tc.output, tc.timeout, tc.similarity)
			if tc.valid {
				if err != nil || c.VerifierSourceThink != tc.think || c.VerifierMaxInputTokens != 16384 || c.VerifierMaxOutputTokens != tc.wantOutput || c.VerifierTimeoutMS != tc.timeout || c.MinSimilarity != tc.similarity {
					t.Fatalf("option drift: %+v %v", c, err)
				}
			} else if err == nil {
				t.Fatal("invalid options accepted")
			}
		})
	}
	thinkingDefault, thinkErr := measurementVerifierConfig("fixture:local", true, 0, 0, .1)
	if thinkErr != nil || thinkingDefault.TimeoutMS != 120000 || thinkingDefault.VerifierTimeoutMS != 120000 {
		t.Fatalf("thinking whole-path or verifier default is too short: %+v %v", thinkingDefault, thinkErr)
	}
	zero, err := measurementVerifierConfig("fixture:local", false, 128, 60000, 0)
	if err != nil || zero.MinSimilarity != .9 {
		t.Fatalf("semantic zero/default convention changed: %+v %v", zero, err)
	}
}

func TestMeasurementRejectsInvalidVerifierOptionsBeforeCredentialOrModelAccess(t *testing.T) {
	for _, extra := range [][]string{
		{"--verifier-max-output-tokens", "513"},
		{"--verifier-source-think", "--verifier-max-output-tokens", "4097"},
		{"--verifier-timeout-ms", "120001"},
		{"--min-similarity", "NaN"},
		{"--min-similarity", "1.1"},
	} {
		args := append([]string{"--live", "--broker", "http://127.0.0.1:1", "--ollama", "http://127.0.0.1:1"}, extra...)
		if err := run(args, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "invalid bounded semantic verifier options") {
			t.Fatalf("option preflight did not precede file/model access: %v", err)
		}
	}
	if err := run([]string{"--report", "missing", "--min-similarity", ".1"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "without live-run options") {
		t.Fatalf("report accepted a replacement runtime policy: %v", err)
	}
}

func replaceMeasurementHeader(t *testing.T, log string, update func(map[string]any)) string {
	t.Helper()
	end := strings.IndexByte(log, '\n')
	if end < 0 {
		t.Fatal("missing header")
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(log[:end]), &fields); err != nil {
		t.Fatal(err)
	}
	update(fields)
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw) + log[end:]
}

func TestReportAcceptsDeclaredVerifierOptionsAndPreservesOldLogs(t *testing.T) {
	log := fixtureMeasurement(t, fixtureRows(t, true), true)
	old, err := summarizeMeasurement(strings.NewReader(log))
	if err != nil {
		t.Fatal(err)
	}
	for _, update := range []func(map[string]any){
		func(h map[string]any) { h["min_similarity"] = .1 },
		func(h map[string]any) { h["min_similarity"] = 0 },
		func(h map[string]any) { h["min_similarity"] = 1 },
		func(h map[string]any) { h["verifier_source_think"] = true },
		func(h map[string]any) { h["verifier_source_think"] = true; h["semantic_timeout_ms"] = 120000 },
		func(h map[string]any) { h["verifier_source_think"] = true; h["semantic_timeout_ms"] = 32000 },
		func(h map[string]any) {
			h["verifier_source_think"] = true
			h["verifier_max_output_tokens"] = 4096
			h["verifier_timeout_ms"] = 120000
		},
		func(h map[string]any) {
			h["verifier_source_think"] = false
			h["verifier_max_output_tokens"] = 512
			h["verifier_timeout_ms"] = 60000
		},
	} {
		report, err := summarizeMeasurement(strings.NewReader(replaceMeasurementHeader(t, log, update)))
		if err != nil || !reflect.DeepEqual(old, report) {
			t.Fatalf("declared metadata changed observed work or broke old-log compatibility: %v", err)
		}
	}
	if !strings.Contains(old.Comparison, "seed indexing/registration and embedding are reuse overhead") || strings.Contains(old.Comparison, "source common cold work separate") {
		t.Fatal("seed overhead mislabeled as entirely common work")
	}
}

func TestReportRejectsMalformedOrContradictoryVerifierDeclarations(t *testing.T) {
	log := fixtureMeasurement(t, fixtureRows(t), true)
	for name, update := range map[string]func(map[string]any){
		"no-think expanded cap":       func(h map[string]any) { h["verifier_max_output_tokens"] = 513 },
		"thinking expanded cap":       func(h map[string]any) { h["verifier_source_think"] = true; h["verifier_max_output_tokens"] = 4097 },
		"zero declared cap":           func(h map[string]any) { h["verifier_max_output_tokens"] = 0 },
		"null declared cap":           func(h map[string]any) { h["verifier_max_output_tokens"] = nil },
		"stage cap too short":         func(h map[string]any) { h["verifier_source_think"] = true; h["verifier_max_output_tokens"] = 31 },
		"no-think whole-path ceiling": func(h map[string]any) { h["semantic_timeout_ms"] = 60001 },
		"thinking whole-path ceiling": func(h map[string]any) { h["verifier_source_think"] = true; h["semantic_timeout_ms"] = 120001 },
		"zero whole-path declaration": func(h map[string]any) { h["semantic_timeout_ms"] = 0 },
		"null whole-path declaration": func(h map[string]any) { h["semantic_timeout_ms"] = nil },
		"zero declared timeout":       func(h map[string]any) { h["verifier_timeout_ms"] = 0 },
		"above timeout":               func(h map[string]any) { h["verifier_timeout_ms"] = 120001 },
		"null think":                  func(h map[string]any) { h["verifier_source_think"] = nil },
		"string think":                func(h map[string]any) { h["verifier_source_think"] = "true" },
		"noncanonical think":          func(h map[string]any) { h["Verifier_Source_Think"] = true },
		"negative threshold":          func(h map[string]any) { h["min_similarity"] = -.1 },
		"above threshold":             func(h map[string]any) { h["min_similarity"] = 1.1 },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := summarizeMeasurement(strings.NewReader(replaceMeasurementHeader(t, log, update))); err == nil {
				t.Fatal("malformed or contradictory policy accepted")
			}
		})
	}
}
