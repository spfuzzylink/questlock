package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/inference"
)

func validModelSpec() inference.Spec {
	return inference.Spec{
		Task: "engineering-handoff", Revision: "test-revision", Provider: "openai", Model: "gpt-4.1-mini",
		Instructions: "Use concise prose.", Input: "Summarize these changes.",
		Context: []inference.ContextItem{
			{ID: "preferences", Text: "Use Go.", Required: true, Stable: true},
			{ID: "changes", Text: "Fixed a failing request.", Required: true},
		},
		MaxContextBytes: 1024, MaxInputTokens: 2048, MaxOutputTokens: 128,
	}
}

func saveModelSpec(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spec.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func saveValidModelSpec(t *testing.T) string {
	t.Helper()
	data, err := json.Marshal(validModelSpec())
	if err != nil {
		t.Fatal(err)
	}
	return saveModelSpec(t, data)
}

func TestModelPlanIsOfflineAndShowsEffectiveInput(t *testing.T) {
	t.Setenv("QUESTLOCK_TOKEN", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	var output, help bytes.Buffer
	if err := modelCommandIO([]string{"plan", "--spec", saveValidModelSpec(t)}, &output, &help); err != nil {
		t.Fatal(err)
	}
	var plan inference.Plan
	if err := json.Unmarshal(output.Bytes(), &plan); err != nil {
		t.Fatalf("plan did not emit valid JSON: %v", err)
	}
	if !strings.Contains(plan.Request.Prefix, "Use concise prose.") || !strings.Contains(plan.Request.Prefix, "Use Go.") {
		t.Fatalf("plan omitted instructions or stable reference: %+v", plan.Request)
	}
	if !strings.Contains(plan.Request.Input, "Fixed a failing request.") || !strings.Contains(plan.Request.Input, "Summarize these changes.") {
		t.Fatalf("plan omitted dynamic reference or task: %+v", plan.Request)
	}
	if len(plan.Selected) != 2 || plan.Fingerprint == "" || help.Len() != 0 {
		t.Fatalf("unexpected plan metadata or help output: %+v, %q", plan, help.String())
	}
}

func TestModelSpecStrictDecoding(t *testing.T) {
	valid, err := json.Marshal(validModelSpec())
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"empty":                   nil,
		"null":                    []byte("null"),
		"array":                   []byte("[]"),
		"syntax":                  []byte(`{"input": "private-unclosed`),
		"unknown top-level field": []byte(`{"api_key":"private-key"}`),
		"unknown nested field":    []byte(`{"context":[{"id":"one","api_key":"private-key"}]}`),
		"unknown pricing field":   []byte(`{"prices":{"current_api_key":"private-key"}}`),
		"wrong field type":        []byte(`{"max_input_tokens":"private-value"}`),
		"second object":           append(append([]byte{}, valid...), []byte(` {}`)...),
		"trailing garbage":        append(append([]byte{}, valid...), []byte(` private-garbage`)...),
		"invalid UTF-8":           []byte{'{', '"', 'i', 'n', 'p', 'u', 't', '"', ':', '"', 0xff, '"', '}'},
		"oversized":               []byte(strings.Repeat(" ", maxModelSpecBytes+1)),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := readModelSpec(saveModelSpec(t, data)); err == nil {
				t.Fatal("invalid spec was accepted")
			} else if strings.Contains(err.Error(), "private-") {
				t.Fatalf("error exposed spec content: %v", err)
			}
		})
	}
	spec, err := readModelSpec(saveModelSpec(t, append(valid, '\n', ' ', '\t')))
	if err != nil || spec.Task != validModelSpec().Task {
		t.Fatalf("valid spec with trailing whitespace rejected: %+v, %v", spec, err)
	}
}

func TestModelHelpNeedsNoSpecOrCredentials(t *testing.T) {
	t.Setenv("QUESTLOCK_TOKEN", "")
	t.Setenv("OPENAI_API_KEY", "")
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}, {"plan", "--help"}, {"plan", "-h"}, {"run", "--help"}, {"run", "-h"}, {"quest", "--help"}, {"quest", "-h"}} {
		var output, help bytes.Buffer
		if err := modelCommandIO(args, &output, &help); err != nil {
			t.Errorf("%v: %v", args, err)
		}
		if help.Len() == 0 || output.Len() != 0 {
			t.Errorf("%v: expected help on help stream only", args)
		}
	}
}

func TestModelRejectsInvalidFlagsBeforeReadingSpec(t *testing.T) {
	for _, args := range [][]string{
		nil, {"unknown"}, {"plan"}, {"run"},
		{"quest", "positional"},
		{"plan", "--spec", "missing.json", "positional"},
		{"run", "--spec", "missing.json", "positional"},
		{"plan", "--spec", "missing.json", "--broker", "http://127.0.0.1:8080"},
		{"plan", "--api-key=private-key"},
		{"run", "--timeout=private-value"},
		{"run", "--spec", "missing.json", "--timeout=0s"},
		{"run", "--spec", "missing.json", "--timeout=-1s"},
	} {
		var output, help bytes.Buffer
		if err := modelCommandIO(args, &output, &help); err == nil {
			t.Errorf("%v: invalid arguments accepted", args)
		} else if strings.Contains(err.Error(), "private-") || strings.Contains(help.String(), "private-") {
			t.Errorf("%v: error exposed flag content", args)
		}
		if output.Len() != 0 {
			t.Errorf("%v: failed command wrote JSON", args)
		}
	}
}

func TestModelRunRequiresEnvironmentCredentials(t *testing.T) {
	t.Setenv("QUESTLOCK_TOKEN", "")
	t.Setenv("OPENAI_API_KEY", "")
	path := saveValidModelSpec(t)
	var output, help bytes.Buffer
	if err := modelCommandIO([]string{"run", "--spec", path}, &output, &help); err == nil || !strings.Contains(err.Error(), "QUESTLOCK_TOKEN") {
		t.Fatalf("missing broker credential accepted: %v", err)
	}
	t.Setenv("QUESTLOCK_TOKEN", "test-token")
	if err := modelCommandIO([]string{"run", "--spec", path}, &output, &help); err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("missing API credential accepted: %v", err)
	}
	if output.Len() != 0 {
		t.Fatal("failed run wrote a result")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	for _, alias := range []string{"anthropic", "claude"} {
		spec := validModelSpec()
		spec.Provider = alias
		spec.Model = "claude-sonnet-4-5"
		data, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if err := modelCommandIO([]string{"run", "--spec", saveModelSpec(t, data)}, &output, &help); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
			t.Fatalf("provider %q did not select Anthropic's environment key: %v", alias, err)
		}
	}
}

func TestModelExampleSpecsKeepCurrentFactsAndOmitArchive(t *testing.T) {
	for _, name := range []string{"openai-handoff.json", "claude-handoff.json", "local-handoff.json"} {
		spec, err := readModelSpec(filepath.Join("..", "..", "examples", "model-workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		plan, err := inference.BuildPlan(spec)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		selected := make(map[string]bool)
		for _, id := range plan.Selected {
			selected[id] = true
		}
		if !selected["preferences"] || !selected["changed-files"] || !selected["validation"] || selected["archived-notes"] {
			t.Fatalf("%s: unexpected selection: %+v", name, plan.Selected)
		}
		if spec.ReuseForSeconds != 3600 || plan.ContextBytes > spec.MaxContextBytes || len(plan.Omitted) != 1 || plan.Omitted[0] != "archived-notes" {
			t.Fatalf("%s: examples do not match documented bounded reuse: %+v", name, plan)
		}
	}
}

func TestModelRunJSONReuseAndIncompleteUsageWithLocalProvider(t *testing.T) {
	ctx := context.Background()
	broker, err := newModelQuestFixture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.close()
	const fakeAPIKey = "fixture-provider-key"
	t.Setenv("QUESTLOCK_TOKEN", broker.tokens[0])
	t.Setenv("OPENAI_API_KEY", fakeAPIKey)
	var counts, generations atomic.Int64
	var incomplete atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+fakeAPIKey {
			t.Error("provider request did not use its environment credential")
			http.Error(w, "unauthorized fixture request", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/responses/input_tokens":
			counts.Add(1)
			fmt.Fprintln(w, `{"input_tokens":100}`)
		case "/responses":
			generations.Add(1)
			status := "completed"
			var details any
			if incomplete.Load() {
				status = "incomplete"
				details = map[string]string{"reason": "max_output_tokens"}
			}
			body := map[string]any{
				"id": "fixture-response", "model": "gpt-4.1-mini", "status": status,
				"incomplete_details": details,
				"output":             []any{map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]string{"type": "output_text", "text": "A fixture handoff."}}}},
				"usage":              map[string]any{"input_tokens": 100, "output_tokens": 128, "input_tokens_details": map[string]int{"cached_tokens": 0}, "output_tokens_details": map[string]int{"reasoning_tokens": 0}},
			}
			if err := json.NewEncoder(w).Encode(body); err != nil {
				t.Error(err)
			}
		default:
			t.Errorf("unexpected provider route %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	spec := validModelSpec()
	spec.ReuseForSeconds = 3600
	invoke := func(wantError bool) inference.Result {
		t.Helper()
		data, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		var output, help bytes.Buffer
		err = modelCommandIO([]string{"run", "--spec", saveModelSpec(t, data), "--broker", broker.server.URL, "--api-base-url", provider.URL, "--timeout", "5s"}, &output, &help)
		if (err != nil) != wantError {
			t.Fatalf("run error = %v; want error %v", err, wantError)
		}
		if strings.Contains(output.String(), fakeAPIKey) || strings.Contains(output.String(), broker.tokens[0]) {
			t.Fatal("result exposed a credential")
		}
		var result inference.Result
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatalf("run did not emit a result JSON: %v", err)
		}
		return result
	}
	first := invoke(false)
	second := invoke(false)
	if first.Reused || !first.Response.Complete || !second.Reused || second.ThisRunUsage != (inference.Usage{}) || counts.Load() != 1 || generations.Load() != 1 {
		t.Fatalf("CLI did not coordinate one generation and reuse: first=%+v second=%+v counts=%d generations=%d", first, second, counts.Load(), generations.Load())
	}
	incomplete.Store(true)
	spec.Revision = "incomplete-revision"
	partial := invoke(true)
	if partial.Response.Complete || partial.ThisRunUsage.OutputTokens != 128 || counts.Load() != 2 || generations.Load() != 2 {
		t.Fatalf("incomplete CLI run lost saved usage or completion state: %+v", partial)
	}
	artifacts, err := broker.clients[0].List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range artifacts {
		if strings.Contains(artifact.Content, fakeAPIKey) || strings.Contains(artifact.Content, broker.tokens[0]) {
			t.Fatal("broker artifact exposed a credential")
		}
	}
}

func TestModelErrorsRedactCredentials(t *testing.T) {
	err := redactModelError(errors.New("private-token and private-key"), "private-token", "private-key", "")
	if strings.Contains(err.Error(), "private-") || strings.Count(err.Error(), "[redacted]") != 2 {
		t.Fatalf("credentials were exposed: %v", err)
	}
	for _, upstream := range []error{
		&client.Error{Code: "private-code", Message: "private-task-content", Status: 403},
		fmt.Errorf("result publication failed: %w", &client.Error{Code: "private-code", Message: "private-task-content", Status: 500}),
		&url.Error{Op: "POST", URL: "https://broker.example/private-path", Err: errors.New("private-transport-content")},
	} {
		if err := redactModelError(upstream); strings.Contains(err.Error(), "private-") || !strings.Contains(err.Error(), "broker") {
			t.Fatalf("remote error exposed data: %v", err)
		}
	}
}
