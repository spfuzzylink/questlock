package evaluation

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/inference"
)

func intBound(n int) *int { return &n }

func TestCountRules(t *testing.T) {
	for _, tc := range []struct {
		name, text                    string
		words, characters, paragraphs int
	}{
		{"empty", "", 0, 0, 0},
		{"whitespace", " \t\r\n\u00a0\n\u2003", 0, 7, 0},
		{"unicode whitespace", "first\u00a0second\u2003third\tend", 4, 22, 1},
		{"non Latin without spaces", "你好世界", 1, 4, 1},
		{"valid replacement character", "�", 1, 1, 1},
		{"code points not graphemes", "a\u0301 👩‍💻 中文", 3, 9, 1},
		{"single newlines", "one\ntwo\nthree", 3, 13, 1},
		{"two paragraphs", "one\ntwo\n\nthree\nfour", 4, 19, 2},
		{"blank lines and CRLF", "\r\n \t\r\none\r\ntwo\r\n\u00a0\r\n\r\nthree\r\n \r\n", 3, 31, 2},
		{"trailing blanks", "first\n\n\n \t\n", 1, 11, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := []Check{
				{ID: "words", Kind: "word_count", Min: intBound(tc.words), Max: intBound(tc.words)},
				{ID: "characters", Kind: "character_count", Min: intBound(tc.characters), Max: intBound(tc.characters)},
				{ID: "paragraphs", Kind: "paragraph_count", Min: intBound(tc.paragraphs), Max: intBound(tc.paragraphs)},
			}
			results, passed := CheckOutput(tc.text, checks)
			if !passed || len(results) != 3 {
				t.Fatalf("unexpected counts: %+v", results)
			}
			for i, want := range []int{tc.words, tc.characters, tc.paragraphs} {
				if results[i].Observed == nil || *results[i].Observed != want || results[i].Status != "" {
					t.Fatalf("count %d: %+v, want %d", i, results[i], want)
				}
			}
		})
	}
}

func Test193WordKeywordRichAnswerFailsMaximumWithoutInference(t *testing.T) {
	text := "company platform " + strings.Repeat("synthetic ", 95) + "\n\n" + strings.Repeat("example ", 96)
	checks := []Check{
		{ID: "company", Kind: "contains", Text: "company"},
		{ID: "platform", Kind: "contains", Text: "platform"},
		{ID: "words", Kind: "word_count", Min: intBound(130), Max: intBound(165)},
		{ID: "paragraphs", Kind: "paragraph_count", Min: intBound(2), Max: intBound(2)},
	}
	results, passed := CheckOutput(text, checks)
	if passed || !results[0].Passed || !results[1].Passed || results[2].Passed || *results[2].Observed != 193 || !results[3].Passed || *results[3].Observed != 2 {
		t.Fatalf("length failure hidden by literal checks: %+v", results)
	}
	encoded, err := json.Marshal(results)
	if err != nil || strings.Contains(string(encoded), "synthetic") || !strings.Contains(string(encoded), `"observed":193`) {
		t.Fatalf("counts missing or answer retained: %s %v", encoded, err)
	}
}

func TestCountBoundsAreInclusiveAndAllowExplicitZero(t *testing.T) {
	for _, tc := range []struct {
		text string
		min  *int
		max  *int
		pass bool
	}{
		{"", nil, intBound(0), true},
		{" ", intBound(0), nil, true},
		{"one", nil, intBound(0), false},
		{"one two", intBound(2), nil, true},
		{"one two", nil, intBound(2), true},
		{"one two", intBound(3), nil, false},
		{"one two", nil, intBound(1), false},
	} {
		results, passed := CheckOutput(tc.text, []Check{{ID: "words", Kind: "word_count", Min: tc.min, Max: tc.max}})
		if passed != tc.pass || len(results) != 1 || results[0].Observed == nil {
			t.Fatalf("bounds %+v: %+v %v", tc, results, passed)
		}
	}
}

func TestCountTextBoundsAndInvalidUTF8(t *testing.T) {
	// This word is longer than a scanner token; counting does not split it or
	// allocate a slice containing the input's words, lines, or code points.
	long := strings.Repeat("界", 30000)
	results, passed := CheckOutput(long, []Check{{ID: "word", Kind: "word_count", Max: intBound(1)}, {ID: "runes", Kind: "character_count", Max: intBound(30000)}})
	if !passed || *results[0].Observed != 1 || *results[1].Observed != 30000 {
		t.Fatalf("long Unicode word: %+v", results)
	}
	results, passed = CheckOutput(strings.Repeat("x", MaxCountTextBytes), []Check{{ID: "characters", Kind: "character_count", Max: intBound(MaxCountTextBytes)}})
	if !passed || *results[0].Observed != MaxCountTextBytes {
		t.Fatalf("supported byte limit: %+v", results)
	}
	for _, text := range []string{string([]byte{0xff}), "valid" + string([]byte{0xc0, 0xaf}), strings.Repeat("x", MaxCountTextBytes+1)} {
		for _, kind := range []string{"word_count", "character_count", "paragraph_count"} {
			results, passed := CheckOutput(text, []Check{{ID: "count", Kind: kind, Min: intBound(0)}})
			if passed || len(results) != 1 || results[0].Passed || results[0].Observed != nil {
				t.Fatalf("unsupported text yielded a count for %s: %+v", kind, results)
			}
		}
	}
}

func TestCountValidationRejectsMalformedContracts(t *testing.T) {
	invalid := []Check{
		{ID: "x", Kind: "word_count"},
		{ID: "x", Kind: "character_count", Min: intBound(-1)},
		{ID: "x", Kind: "paragraph_count", Max: intBound(-1)},
		{ID: "x", Kind: "word_count", Min: intBound(3), Max: intBound(2)},
		{ID: "x", Kind: "word_count", Max: intBound(MaxCountTextBytes + 1)},
		{ID: "x", Kind: "word_count", Min: intBound(MaxCountTextBytes + 1)},
		{ID: "x", Kind: "word_count", Max: intBound(2), Text: "text"},
		{ID: "x", Kind: "word_count", Max: intBound(2), IgnoreCase: true},
		{ID: "x", Kind: "word_count", Max: intBound(2), Pointer: "/text"},
		{ID: "x", Kind: "word_count", Max: intBound(2), Expected: json.RawMessage(`1`)},
	}
	for _, c := range []Check{{ID: "x", Kind: "json_valid"}, {ID: "x", Kind: "json_equals", Expected: json.RawMessage(`1`)}, {ID: "x", Kind: "contains", Text: "one"}, {ID: "x", Kind: "not_contains", Text: "two"}} {
		c.Min = intBound(0)
		invalid = append(invalid, c)
		c.Min, c.Max = nil, intBound(1)
		invalid = append(invalid, c)
	}
	for _, c := range invalid {
		if ValidateChecks([]Check{c}) == nil {
			t.Fatalf("invalid contract accepted: %+v", c)
		}
		if _, pass := CheckOutput("one", []Check{c}); pass {
			t.Fatalf("invalid contract passed: %+v", c)
		}
	}
	checks := make([]Check, 65)
	for i := range checks {
		checks[i] = Check{ID: strings.Repeat("x", i+1), Kind: "word_count", Max: intBound(2)}
	}
	if ValidateChecks(nil) == nil || ValidateChecks(checks) == nil {
		t.Fatal("check-count limits not enforced")
	}
	checks = checks[:64]
	if err := ValidateChecks(checks); err != nil {
		t.Fatalf("64 valid checks rejected: %v", err)
	}
	checks[1].ID = checks[0].ID
	if ValidateChecks(checks) == nil {
		t.Fatal("duplicate count check IDs accepted")
	}
}

func TestLoadCountBoundsRequiresIntegerTypesAndRejectsUnknownFields(t *testing.T) {
	suite := oneCaseSuite()
	suite.Cases[0].Checks = []Check{{ID: "words", Kind: "word_count", Min: intBound(0), Max: intBound(165)}}
	encoded, err := json.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(strings.NewReader(string(encoded))); err != nil {
		t.Fatalf("valid count suite failed load: %v", err)
	}
	for _, field := range []string{`"max":1.5`, `"max":"165"`, `"max":9223372036854775808`, `"max":-1`, `"maximum":165`} {
		invalid := strings.Replace(string(encoded), `"max":165`, field, 1)
		if _, err := Load(strings.NewReader(invalid)); err == nil {
			t.Fatalf("invalid count field accepted: %s", field)
		}
	}
}

func TestCountBoundsChangeIdentityAndLegacyJSONIsStable(t *testing.T) {
	legacy := []Check{
		{ID: "json", Kind: "json_valid"},
		{ID: "value", Kind: "json_equals", Pointer: "/a", Expected: json.RawMessage(`1`)},
		{ID: "literal", Kind: "contains", Text: "one", IgnoreCase: true},
		{ID: "absent", Kind: "not_contains", Text: "two"},
	}
	encoded, err := json.Marshal(legacy)
	want := `[{"id":"json","kind":"json_valid"},{"id":"value","kind":"json_equals","pointer":"/a","expected":1},{"id":"literal","kind":"contains","text":"one","ignore_case":true},{"id":"absent","kind":"not_contains","text":"two"}]`
	if err != nil || string(encoded) != want {
		t.Fatalf("legacy check JSON changed: %s %v", encoded, err)
	}
	results, _ := CheckOutput(`{"a":1}`, legacy[:2])
	encoded, err = json.Marshal(results)
	if err != nil || string(encoded) != `[{"id":"json","kind":"json_valid","passed":true},{"id":"value","kind":"json_equals","passed":true}]` {
		t.Fatalf("legacy result JSON changed: %s %v", encoded, err)
	}
	suite := oneCaseSuite()
	suite.Cases[0].Checks = []Check{{ID: "words", Kind: "word_count", Max: intBound(165)}}
	digest := suiteDigest(suite)
	baseline := caseSpec(suite, suite.Cases[0], suite.Configurations[0], digest)
	suite.Cases[0].Checks[0].Max = intBound(166)
	changed := caseSpec(suite, suite.Cases[0], suite.Configurations[0], suiteDigest(suite))
	if baseline.Task == changed.Task || baseline.Revision == changed.Revision {
		t.Fatal("different length bound reused old identity")
	}
	suite.Cases[0].Checks[0].Max = intBound(165)
	suite.Cases[0].Checks[0].Min = intBound(0)
	if suiteDigest(suite) == digest {
		t.Fatal("explicit zero bound collapsed into omitted bound")
	}
}

func TestSupportedCheckKindsDoesNotExposeMutableState(t *testing.T) {
	want := []string{"json_valid", "json_equals", "contains", "not_contains", "word_count", "character_count", "paragraph_count"}
	got := SupportedCheckKinds()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unsupported metadata: %v", got)
	}
	got[0] = "changed"
	if !reflect.DeepEqual(SupportedCheckKinds(), want) {
		t.Fatal("caller changed supported kinds")
	}
}

func TestAbsentAnswersAreNotEvaluatedAndExactReuseIs(t *testing.T) {
	checks := []Check{{ID: "words", Kind: "word_count", Max: intBound(0)}, {ID: "absent", Kind: "not_contains", Text: "forbidden"}}
	for _, tc := range []struct {
		result inference.Result
		err    error
	}{
		{inference.Result{}, inference.ErrBudgetExceeded},
		{inference.Result{Response: inference.Response{Text: "partial", Complete: false}}, inference.ErrIncomplete},
		{inference.Result{Response: inference.Response{Text: "partial", Complete: false}}, nil},
		{inference.Result{Response: inference.Response{Text: "answer", Complete: true}}, inference.ErrInFlight},
	} {
		m := measurement(tc.result, tc.err, 1, "live", checks)
		if m.QualityPassed || !reflect.DeepEqual(m.Checks, NotEvaluated(checks)) {
			t.Fatalf("absent answer evaluated: %+v", m)
		}
		for _, c := range m.Checks {
			if c.Passed || c.Status != "not_evaluated" || c.Observed != nil {
				t.Fatalf("fabricated observed count: %+v", c)
			}
		}
	}
	checks[0].Max = intBound(1)
	m := measurement(inference.Result{Reused: true, Response: inference.Response{Text: "answer", Complete: true}}, nil, 1, "live", checks)
	if !m.QualityPassed || *m.Checks[0].Observed != 1 || m.Checks[0].Status != "" {
		t.Fatalf("complete nongenerating exact reuse skipped checks: %+v", m)
	}
	suite := oneCaseSuite()
	suite.Cases[0].Checks = checks
	report, err := Evaluate(context.Background(), suite, func(Case, Configuration) (Runner, error) { return nil, nil })
	if err != nil || report.Results[0].First.QualityPassed || !reflect.DeepEqual(report.Results[0].First.Checks, NotEvaluated(checks)) {
		t.Fatalf("factory failure evaluated absent answer: %+v %v", report, err)
	}
}
