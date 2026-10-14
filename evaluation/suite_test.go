package evaluation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestLoadRejectsUnknownDuplicateAndOversizedJSON(t *testing.T) {
	valid, _ := json.Marshal(DefaultSuite())
	for _, text := range []string{
		strings.Replace(string(valid), `"schema":1`, `"schema":1,"unexpected":true`, 1),
		strings.Replace(string(valid), `"schema":1`, `"schema":1,"schema":1`, 1),
		string(valid) + `{}`,
		strings.Repeat(" ", 4<<20) + string(valid),
		strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66),
		string([]byte{'"', 0xff, '"'}),
	} {
		if _, err := Load(strings.NewReader(text)); err == nil {
			t.Fatal("invalid suite JSON accepted")
		}
	}
	if _, err := Load(strings.NewReader(string(valid))); err != nil {
		t.Fatal(err)
	}
}

func TestSuiteValidationHappensBeforeRunnerCreation(t *testing.T) {
	for _, kind := range []string{"revision", "duplicate_case", "duplicate_config", "bad_check", "bad_pointer", "blank_literal", "bad_expected", "zero_ttl", "silent_prompt_override", "priced_without_prices", "negative_latency", "missing_fixture"} {
		t.Run(kind, func(t *testing.T) {
			s := DefaultSuite()
			switch kind {
			case "revision":
				s.Revision = "has spaces"
			case "duplicate_case":
				s.Cases[1].ID = s.Cases[0].ID
			case "duplicate_config":
				s.Configurations[1].ID = s.Configurations[0].ID
			case "bad_check":
				s.Cases[0].Checks[0].Kind = "llm_judge"
			case "bad_pointer":
				s.Cases[0].Checks[1].Pointer = "/bad~2escape"
			case "blank_literal":
				s.Cases[0].Checks[2].Text = " "
			case "bad_expected":
				s.Cases[0].Checks[1].Expected = json.RawMessage(`{"x":1,"x":2}`)
			case "zero_ttl":
				s.Configurations[0].Spec.ReuseForSeconds = 0
			case "silent_prompt_override":
				s.Configurations[0].Spec.Input = "should not disappear"
			case "priced_without_prices":
				s.Objective = "priced_cost"
			case "negative_latency":
				s.Configurations[0].MaxReuseMS = -1
			case "missing_fixture":
				delete(s.Cases[0].Fixtures, "compact-64")
			}
			calls := 0
			if kind == "missing_fixture" {
				if _, err := RunFixtures(context.Background(), s); err == nil {
					t.Fatal("missing fixture silently became network generation")
				}
				return
			}
			_, err := Evaluate(context.Background(), s, func(Case, Configuration) (Runner, error) { calls++; return nil, nil })
			if err == nil || calls != 0 {
				t.Fatalf("invalid suite ran: %v calls=%d", err, calls)
			}
		})
	}
}

func TestCanceledEvaluationNeverCreatesRunner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := Evaluate(ctx, DefaultSuite(), func(Case, Configuration) (Runner, error) { calls++; return nil, nil })
	if err == nil || calls != 0 {
		t.Fatalf("canceled evaluation started: %v %d", err, calls)
	}
}
