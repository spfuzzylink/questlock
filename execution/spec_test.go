package execution_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/execution"
)

type failingSpecReader struct{}

func (failingSpecReader) Read([]byte) (int, error) {
	return 0, errors.New("private reader failure")
}

func TestLoadSpecRejectsAmbiguousOrUnboundedInputWithoutLeakingContent(t *testing.T) {
	valid, err := json.Marshal(fixtureSpec())
	if err != nil {
		t.Fatal(err)
	}
	text := string(valid)
	var typedNil *strings.Reader
	cases := map[string]io.Reader{
		"nil":               nil,
		"typed_nil":         typedNil,
		"empty":             strings.NewReader(""),
		"null":              strings.NewReader("null"),
		"array":             strings.NewReader("[]"),
		"string":            strings.NewReader(`"private task"`),
		"trailing":          strings.NewReader(text + `{}`),
		"reader_failure":    failingSpecReader{},
		"invalid_utf8":      bytes.NewReader(append([]byte(text), 0xff)),
		"oversized":         strings.NewReader(strings.Repeat(" ", execution.MaxSpecBytes+1)),
		"too_deep":          strings.NewReader(strings.Repeat("[", 34) + "1" + strings.Repeat("]", 34)),
		"unknown":           strings.NewReader(strings.Replace(text, `"task":`, `"private_unknown":true,"task":`, 1)),
		"duplicate":         strings.NewReader(strings.Replace(text, `"task":`, `"task":"private first","task":`, 1)),
		"escaped_duplicate": strings.NewReader(strings.Replace(text, `"task":`, `"ta\u0073k":"private first","task":`, 1)),
		"case_alias":        strings.NewReader(strings.Replace(text, `"task":`, `"Task":"private first","task":`, 1)),
		"wrong_type":        strings.NewReader(strings.Replace(text, `"max_input_tokens":256`, `"max_input_tokens":"private value"`, 1)),
		"null_optional":     strings.NewReader(strings.Replace(text, `"task":`, `"prices":null,"task":`, 1)),
		"invalid_plan":      strings.NewReader(strings.Replace(text, `"max_output_tokens":32`, `"max_output_tokens":0`, 1)),
		"nested_unknown": strings.NewReader(strings.Replace(text, `"task":`,
			`"ollama":{"format":"json","private_unknown":true},"task":`, 1)),
		"nested_duplicate": strings.NewReader(strings.Replace(text, `"task":`,
			`"ollama":{"format":"json","format":"json"},"task":`, 1)),
		"invalid_token_quota": strings.NewReader(strings.Replace(text, `"task":`,
			`"daily_token_budget":{"input_tokens":1,"output_tokens":1,"total_tokens":0},"task":`, 1)),
	}
	for name, reader := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := execution.LoadSpec(reader)
			if !errors.Is(err, execution.ErrInvalidSpec) || err.Error() != "execution_spec_invalid" {
				t.Fatalf("expected a controlled rejection, got %v", err)
			}
		})
	}
}

func TestLoadSpecAcceptsApprovedSnapshotsAndPublicExamples(t *testing.T) {
	valid, err := json.Marshal(fixtureSpec())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := execution.LoadSpec(bytes.NewReader(append(valid, '\n')))
	if err != nil || loaded.Task != fixtureSpec().Task || loaded.Input != fixtureSpec().Input {
		t.Fatalf("valid spec changed: %+v %v", loaded, err)
	}
	padded := append(append([]byte(nil), valid...), bytes.Repeat([]byte(" "), execution.MaxSpecBytes-len(valid))...)
	if _, err := execution.LoadSpec(bytes.NewReader(padded)); err != nil {
		t.Fatal("a valid snapshot at the documented byte limit was rejected")
	}
	for _, name := range []string{"local-task.json", "sglang-task.json", "hosted-budget.example.json"} {
		t.Run(name, func(t *testing.T) {
			f, err := os.Open("../examples/execution/" + name)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := execution.LoadSpec(f); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExecutionExampleAdmissionPolicyIsComplete(t *testing.T) {
	f, err := os.Open("../examples/execution/admission.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := admission.LoadConfig(f); err != nil {
		t.Fatal(err)
	}
}
