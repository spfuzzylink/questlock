package main

import (
	"bytes"
	"encoding/json"
	"github.com/spfuzzylink/questlock/semantic"
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
		{"above timeout", true, 4096, 600001, .1, 0, false},
		{"negative similarity", true, 4096, 120000, -.1, 0, false},
		{"NaN similarity", true, 4096, 120000, math.NaN(), 0, false},
		{"infinite similarity", true, 4096, 120000, math.Inf(1), 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := measurementVerifierConfig("fixture:local", tc.think, false, tc.output, tc.timeout, tc.similarity)
			if tc.valid {
				if err != nil || c.VerifierSourceThink != tc.think || c.VerifierMaxInputTokens != 16384 || c.VerifierMaxOutputTokens != tc.wantOutput || c.VerifierTimeoutMS != tc.timeout || c.MinSimilarity != tc.similarity {
					t.Fatalf("option drift: %+v %v", c, err)
				}
			} else if err == nil {
				t.Fatal("invalid options accepted")
			}
		})
	}
	thinkingDefault, thinkErr := measurementVerifierConfig("fixture:local", true, false, 0, 0, .1)
	if thinkErr != nil || thinkingDefault.TimeoutMS != 120000 || thinkingDefault.VerifierTimeoutMS != 120000 {
		t.Fatalf("thinking whole-path or verifier default is too short: %+v %v", thinkingDefault, thinkErr)
	}
	zero, err := measurementVerifierConfig("fixture:local", false, false, 128, 60000, 0)
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
		"thinking whole-path ceiling": func(h map[string]any) { h["verifier_source_think"] = true; h["semantic_timeout_ms"] = 600001 },
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

func TestMeasurementPairThinkingDefaultsAndHeaderCompatibility(t *testing.T) {
	c, err := measurementVerifierConfig("fixture:local", false, true, 0, 0, .1)
	if err != nil || c.VerifierSourceThink || !c.VerifierPairThink || c.TimeoutMS != 120000 || c.VerifierTimeoutMS != 120000 || c.VerifierMaxOutputTokens != 4096 {
		t.Fatalf("pair thinking normalized too late or enabledsource: %+v %v", c, err)
	}
	short, err := measurementVerifierConfig("fixture:local", false, true, 128, 750, .1)
	if err != nil || short.VerifierMaxOutputTokens != 128 || short.VerifierTimeoutMS != 750 {
		t.Fatal("explicitshorteroptionsreplaced")
	}
	log := fixtureMeasurement(t, fixtureRows(t, true), true)
	old, err := summarizeMeasurement(strings.NewReader(log))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{true, false} {
		report, err := summarizeMeasurement(strings.NewReader(replaceMeasurementHeader(t, log, func(h map[string]any) { h["verifier_pair_think"] = value })))
		if err != nil || !reflect.DeepEqual(old, report) {
			t.Fatal("optionalpairmetadatachangedactualworkoroldlogreading", err)
		}
	}
	for _, value := range []any{nil, "true"} {
		if _, err := summarizeMeasurement(strings.NewReader(replaceMeasurementHeader(t, log, func(h map[string]any) { h["verifier_pair_think"] = value }))); err == nil {
			t.Fatal("invalidpairboolaccepted")
		}
	}
	if err := run([]string{"--report", "missing", "--verifier-pair-think"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "without live-run options") {
		t.Fatal("offline report acceptedreplacementreasoningpolicy")
	}
}

func TestMeasurementSamplingHeadersAndFlagsPreserveActualWork(t *testing.T) {
	log := fixtureMeasurement(t, fixtureRows(t, true), true)
	old, err := summarizeMeasurement(strings.NewReader(log))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		temp float64
		seed int64
	}{{0, 0}, {1, 42}, {2, 2147483647}} {
		report, err := summarizeMeasurement(strings.NewReader(replaceMeasurementHeader(t, log, func(h map[string]any) { h["verifier_pair_temperature"], h["verifier_pair_seed"] = tc.temp, tc.seed })))
		if err != nil || !reflect.DeepEqual(old, report) {
			t.Fatal("sampling metadata changed actual measured work", err)
		}
	}
	for _, tc := range []struct {
		key   string
		value any
	}{{"verifier_pair_temperature", nil}, {"verifier_pair_temperature", 2.1}, {"verifier_pair_temperature", "1"}, {"verifier_pair_seed", nil}, {"verifier_pair_seed", -1}, {"verifier_pair_seed", 2147483648}, {"verifier_pair_seed", .5}, {"verifier_pair_seed", "42"}} {
		if _, err := summarizeMeasurement(strings.NewReader(replaceMeasurementHeader(t, log, func(h map[string]any) { h[tc.key] = tc.value }))); err == nil {
			t.Fatalf("invalid header accepted: %+v", tc)
		}
	}
	for _, flag := range []string{"--verifier-pair-temperature", "--verifier-pair-seed"} {
		if err := run([]string{"--report", "missing", flag, "0"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "without live-run options") {
			t.Fatal("report accepted replacement sampling policy")
		}
	}
	for _, args := range [][]string{{"--verifier-pair-temperature", "NaN"}, {"--verifier-pair-temperature", "2.1"}, {"--verifier-pair-seed", "-1"}, {"--verifier-pair-seed", "2147483648"}} {
		flags := append([]string{"--live", "--broker", "http://127.0.0.1:1", "--ollama", "http://127.0.0.1:1"}, args...)
		if err := run(flags, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "pair") {
			t.Fatalf("invalid sampling reached file/model work: %v", err)
		}
	}
}

func TestMeasurementSamplingRejectsCaseAliasesAndCollisions(t *testing.T) {
	log := fixtureMeasurement(t, fixtureRows(t, true), true)
	end := strings.IndexByte(log, '\n')
	if end < 0 {
		t.Fatal("missing header")
	}
	header := strings.TrimSuffix(log[:end], "}")
	for _, field := range []struct{ key, value string }{
		{"verifier_pair_temperature", "1"},
		{"verifier_pair_seed", "42"},
	} {
		canonical := `"` + field.key + `":` + field.value
		alias := `"` + strings.ToUpper(field.key) + `":` + field.value
		// Exercise both orders: encoding/json matches tagged struct fields
		// without regard to case, while duplicate-key checks use exact keys.
		for _, suffix := range []string{alias, canonical + "," + alias, alias + "," + canonical} {
			record := header + "," + suffix + "}" + log[end:]
			if _, err := summarizeMeasurement(strings.NewReader(record)); err == nil {
				t.Fatalf("sampling alias or collision accepted: %s", suffix)
			}
		}
	}
}

func TestMeasurementThinkingDeadlineMatchesRequestsAndOldHeaders(t *testing.T) {
	c, err := measurementVerifierConfig("fixture:local", false, true, 0, 240000, .1)
	if err != nil || c.TimeoutMS != 240000 || c.VerifierTimeoutMS != 240000 || c.VerifierSourceThink || !c.VerifierPairThink || c.VerifierMaxOutputTokens != 4096 {
		t.Fatal("measurement normalized before selecting deadline", err)
	}
	var set caseSet
	if err := json.Unmarshal(casesJSON, &set); err != nil {
		t.Fatal(err)
	}
	_, request := measurementStep(set.Revision, set.Cases[0], 1, nil)
	request.SemanticReuse.VerifierPairThink = c.VerifierPairThink
	request.SemanticReuse.VerifierTimeoutMS = c.VerifierTimeoutMS
	actual, err := semantic.NormalizeReuseConfig(*request.SemanticReuse)
	if err != nil || actual.TimeoutMS != c.TimeoutMS {
		t.Fatal("request retained an accidental earlier default deadline", err)
	}
	log := fixtureMeasurement(t, fixtureRows(t, true), true)
	old, err := summarizeMeasurement(strings.NewReader(log))
	if err != nil {
		t.Fatal(err)
	}
	for _, whole := range []int{0, 45000, 240000, 600000} {
		updated := replaceMeasurementHeader(t, log, func(h map[string]any) {
			h["verifier_pair_think"] = true
			h["verifier_timeout_ms"] = 240000
			if whole != 0 {
				h["semantic_timeout_ms"] = whole
			}
		})
		report, err := summarizeMeasurement(strings.NewReader(updated))
		if err != nil || !reflect.DeepEqual(old, report) {
			t.Fatal("valid long/short caller deadline changed actual work or old-log reading", err)
		}
	}
	for _, key := range []string{"verifier_timeout_ms", "semantic_timeout_ms"} {
		updated := replaceMeasurementHeader(t, log, func(h map[string]any) { h["verifier_pair_think"] = true; h[key] = 600001 })
		if _, err := summarizeMeasurement(strings.NewReader(updated)); err == nil {
			t.Fatal("over-ceiling thinking header accepted", key)
		}
	}
}
