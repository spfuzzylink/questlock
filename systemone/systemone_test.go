package systemone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/delegation"
)

func fixture() Request {
	return Request{Schema: Schema, Model: "installed:local", State: "An exact validated result exists.", Question: "Recommend an approved lane.", Choices: []Choice{{"reuse", "Read that exact validated result."}, {"local_reasoning", "Reason locally if no exact result exists."}}, Policy: &Policy{0.75, 0.2}}
}

func response() bridgeResponse {
	input, output := int64(170), int64(41)
	return bridgeResponse{Schema: "questlock.system-one.bridge.v1", Status: "ok", Model: "installed:local", AdapterVersion: AdapterVersion, Scores: map[string]float64{"reuse": 0.9, "local_reasoning": 0.1}, InputTokens: &input, OutputTokens: &output, Calls: 1, LatencyMS: 10}
}

func runner(t *testing.T, raw bridgeResponse) Runner {
	t.Helper()
	python, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Runner{Python: python, inspect: func(context.Context, string) (string, error) { return strings.Repeat("a", 64), nil }, execute: func(context.Context, string, []byte) ([]byte, error) { return json.Marshal(raw) }}
}

func TestAdvisoryChoiceAndUnknownUsage(t *testing.T) {
	raw := response()
	raw.InputTokens, raw.OutputTokens = nil, nil
	result, err := runner(t, raw).Choose(context.Background(), fixture())
	if err != nil || result.Status != "chosen" || result.ChoiceID != "reuse" || !result.AdvisoryOnly || result.RequestSHA256 == "" || result.ModelDigest == "" {
		t.Fatalf("missing typed advisory receipt: %+v %v", result, err)
	}
	if result.Usage.InputTokens != nil || result.Usage.OutputTokens != nil || result.Usage.LocalModelCalls == nil || *result.Usage.LocalModelCalls != 1 || result.Usage.ProviderCostUSD != 0 || result.Usage.HostedAssistantUsageIncluded {
		t.Fatal("unknown usage became zero or local receipt claimed hosted accounting")
	}
}

func TestScoresAbstainOnMalformedAndUncertainOutput(t *testing.T) {
	for _, test := range []struct {
		name   string
		scores map[string]float64
		want   string
	}{
		{"missing", map[string]float64{"reuse": 1}, "malformed_scores"},
		{"extra", map[string]float64{"reuse": 0.9, "unapproved_write": 0.1}, "malformed_scores"},
		{"negative", map[string]float64{"reuse": 1.1, "local_reasoning": -0.1}, "malformed_scores"},
		{"nonfinite", map[string]float64{"reuse": math.NaN(), "local_reasoning": 0.1}, "malformed_scores"},
		{"sum", map[string]float64{"reuse": 0.9, "local_reasoning": 0.9}, "malformed_scores"},
		{"low", map[string]float64{"reuse": 0.6, "local_reasoning": 0.4}, "score_below_heuristic"},
		{"tie", map[string]float64{"reuse": 0.5, "local_reasoning": 0.5}, "ambiguous_scores"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := Result{Status: "abstained", AdvisoryOnly: true}
			request := fixture()
			if test.name == "tie" {
				request.Policy.MinScore = 0.4
			}
			decide(request, &r, test.scores)
			if r.Status != "abstained" || r.ChoiceID != "" || r.Reason != test.want {
				t.Fatalf("unsafe decision: %+v", r)
			}
		})
	}
	r := Result{Status: "abstained"}
	req := fixture()
	req.Choices[1].ID = "abstain"
	decide(req, &r, map[string]float64{"reuse": 0, "abstain": 1})
	if r.Reason != "model_abstained" || r.ChoiceID != "" {
		t.Fatal("explicit abstention label became an action")
	}
}

func TestBridgeScoresCannotTurnNullIntoZero(t *testing.T) {
	for _, bad := range []string{"null", `"0"`, "false"} {
		t.Run(bad, func(t *testing.T) {
			r := runner(t, response())
			r.execute = func(context.Context, string, []byte) ([]byte, error) {
				data, _ := json.Marshal(response())
				data = bytes.Replace(data, []byte(`"reuse":0.9`), []byte(`"reuse":1`), 1)
				return bytes.Replace(data, []byte(`"local_reasoning":0.1`), []byte(`"local_reasoning":`+bad), 1), nil
			}
			result, err := r.Choose(context.Background(), fixture())
			if err != nil || result.Status != "abstained" || result.ChoiceID != "" {
				t.Fatalf("nonnumeric score became an advisory choice: %+v %v", result, err)
			}
		})
	}
}

func TestRequestUnicodeEscapesMustPreserveExactText(t *testing.T) {
	data, _ := json.Marshal(fixture())
	for _, raw := range []string{`\ud800`, `\udc00`, `\ud800a`} {
		changed := strings.Replace(string(data), "An exact validated result exists.", raw, 1)
		if _, err := Load(strings.NewReader(changed)); err == nil {
			t.Fatalf("unpaired surrogate was normalized: %s", raw)
		}
	}
	for raw, want := range map[string]string{`\ud83d\ude00`: "😀", `\\ud800`: `\ud800`} {
		changed := strings.Replace(string(data), "An exact validated result exists.", raw, 1)
		request, err := Load(strings.NewReader(changed))
		if err != nil || request.State != want {
			t.Fatalf("valid escaped text changed: %q %v", request.State, err)
		}
	}
}

func TestRunnerRejectsUnsupportedOrChangedEvidence(t *testing.T) {
	for _, kind := range []string{"retry", "extra_call", "version", "model", "negative_usage", "oversized_usage", "incomplete", "changed_weights", "unknown_field", "duplicate_key", "missing_receipt", "null_receipt", "timeout", "missing_model"} {
		t.Run(kind, func(t *testing.T) {
			raw := response()
			switch kind {
			case "retry":
				raw.Retries = 1
			case "extra_call":
				raw.Calls = 2
			case "version":
				raw.AdapterVersion = "future"
			case "model":
				raw.Model = "different:tag"
			case "negative_usage":
				n := int64(-1)
				raw.InputTokens = &n
			case "oversized_usage":
				n := int64(257)
				raw.OutputTokens = &n
			case "incomplete":
				raw.Status = "abstained"
			}
			r := runner(t, raw)
			switch kind {
			case "changed_weights":
				calls := 0
				r.inspect = func(context.Context, string) (string, error) { calls++; return fmt.Sprint(calls), nil }
			case "unknown_field":
				r.execute = func(context.Context, string, []byte) ([]byte, error) { return []byte(`{"unknown":true}`), nil }
			case "duplicate_key":
				r.execute = func(context.Context, string, []byte) ([]byte, error) {
					return []byte(`{"status":"ok","status":"abstained"}`), nil
				}
			case "missing_receipt", "null_receipt":
				r.execute = func(context.Context, string, []byte) ([]byte, error) {
					data, _ := json.Marshal(raw)
					var fields map[string]any
					_ = json.Unmarshal(data, &fields)
					if kind == "missing_receipt" {
						delete(fields, "retries")
					} else {
						fields["retries"] = nil
					}
					return json.Marshal(fields)
				}
			case "timeout":
				r.execute = func(ctx context.Context, _ string, _ []byte) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() }
				r.Timeout = time.Millisecond
			case "missing_model":
				r.inspect = func(context.Context, string) (string, error) { return "", errors.New("private upstream details") }
				r.execute = func(context.Context, string, []byte) ([]byte, error) {
					t.Fatal("missing model started generation")
					return nil, nil
				}
			}
			result, err := r.Choose(context.Background(), fixture())
			if err != nil || result.Status != "abstained" || result.ChoiceID != "" {
				t.Fatalf("unsupported response selected an action: %+v %v", result, err)
			}
			data, _ := json.Marshal(result)
			if strings.Contains(string(data), "private upstream") {
				t.Fatal("private provider error leaked")
			}
			if kind == "timeout" && result.Usage.LocalModelCalls != nil {
				t.Fatal("interrupted request claimed known zero model calls")
			}
		})
	}
}

func TestLoadRejectsAmbiguousOrUnboundedRequests(t *testing.T) {
	data, _ := json.Marshal(fixture())
	if _, err := Load(bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{string(data) + ` {}`, `{"model":"a:1","model":"b:1"}`, `{"model":"a:1","Model":"b:1"}`, strings.Replace(string(data), `"schema"`, `"Schema"`, 1), strings.Replace(string(data), "exact", "\xff", 1), strings.Repeat("x", MaxRequestBytes+1), `null`} {
		if _, err := Load(strings.NewReader(bad)); err == nil {
			t.Fatal("ambiguous request accepted")
		}
	}
	for _, mutate := range []func(*Request){func(r *Request) { r.Model = "model-cloud:v1" }, func(r *Request) { r.Model = "model" }, func(r *Request) { r.State = strings.Repeat("x", MaxContextBytes) }, func(r *Request) { r.Choices[1].ID = r.Choices[0].ID }, func(r *Request) { r.Policy = nil }, func(r *Request) { r.Policy.MinMargin = 0 }, func(r *Request) { r.State = "\xff" }} {
		request := fixture()
		mutate(&request)
		if request.Validate() == nil {
			t.Fatal("invalid bounded request accepted")
		}
	}
}

func TestDocumentedExamplesStayBoundedAndLocalOnly(t *testing.T) {
	for _, name := range []string{"reuse.json", "route.json"} {
		file, err := os.Open(filepath.Join("..", "optional", "system-one", name))
		if err != nil {
			t.Fatal(err)
		}
		_, err = Load(file)
		file.Close()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	file, err := os.Open(filepath.Join("..", "optional", "system-one", "approved-local-task.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	request, err := delegation.Load(file)
	if err != nil || delegation.Validate(request, false) != nil || request.Policy.Escalation != nil || request.Policy.Local.Spec.Provider != "ollama" {
		t.Fatalf("example no longer declares one approved local task: %v", err)
	}
}

// The Go test executable acts as a fake interpreter only in a subprocess. It
// proves process boundaries without installing Python or contacting a model.
func init() {
	if len(os.Args) > 3 && os.Args[1] == "-I" && os.Args[2] == "-B" {
		info, _ := os.Stat(os.Getenv("HOME"))
		json.NewEncoder(os.Stdout).Encode(map[string]any{"env": os.Environ(), "args": os.Args[1:4], "home_mode": info.Mode().Perm()})
		os.Exit(0)
	}
}

func TestBridgeProcessIsolatesCredentialsAndHome(t *testing.T) {
	for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "TYPESAFE_API_KEY", "HTTP_PROXY", "HTTPS_PROXY", "PYTHONPATH"} {
		t.Setenv(key, "must-not-import")
	}
	python, _ := os.Executable()
	output, err := runBridge(context.Background(), python, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var proof struct {
		Env  []string `json:"env"`
		Args []string `json:"args"`
		Mode uint32   `json:"home_mode"`
	}
	if json.Unmarshal(output, &proof) != nil || len(proof.Env) != 2 || proof.Mode != 0700 {
		t.Fatalf("unexpected adapter isolation: %s", output)
	}
	for _, entry := range proof.Env {
		key, value, _ := strings.Cut(entry, "=")
		if key != "HOME" && key != "PATH" {
			t.Fatal("inherited credentials or config selectors")
		}
		if key == "HOME" {
			if value == os.Getenv("HOME") {
				t.Fatal("active home reused")
			}
			if _, err := os.Stat(value); !os.IsNotExist(err) {
				t.Fatal("temporary home not removed")
			}
		}
		if key == "PATH" && value != filepath.Dir(python) {
			t.Fatal("unexpected executable search path")
		}
	}
	if strings.Join(proof.Args, " ") != "-I -B -c" {
		t.Fatal("Python isolation flags missing")
	}
}
