package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/team"
)

func TestTeamQuestExercisesThreeHTTPSWorkersWithoutModels(t *testing.T) {
	var out, diag bytes.Buffer
	if err := teamCommandIO([]string{"quest"}, &out, &diag); err != nil {
		t.Fatal(err)
	}
	var r struct {
		Passed     bool `json:"passed"`
		ModelCalls int  `json:"actual_model_calls"`
		Workers    int  `json:"worker_identities"`
		Cases      []struct {
			Passed bool `json:"passed"`
			Reuse  int  `json:"reuse_scripted_provider_calls"`
		} `json:"cases"`
	}
	if json.Unmarshal(out.Bytes(), &r) != nil || !r.Passed || r.ModelCalls != 0 || r.Workers != 3 || len(r.Cases) != 6 {
		t.Fatalf("invalid fixture report %s", out.String())
	}
	for _, c := range r.Cases {
		if !c.Passed || c.Reuse != 0 {
			t.Fatal("failed check or replay")
		}
	}
}

func TestTeamBenchmarkKeepsMissingFilesAsFailedRows(t *testing.T) {
	dir := t.TempDir()
	role := team.Role{ID: "master", Driver: "ollama", Model: "fixture:local", ModelDigest: strings.Repeat("a", 64), Instructions: "Return JSON.", MaxInputTokens: 4096, MaxOutputTokens: 256}
	reviewer := role
	reviewer.ID = "review-correctness"
	suite := team.DefaultSuite(team.Request{Schema: 1, Master: role, Reviewers: []team.Role{reviewer}, ReuseForSeconds: 3600})
	b, err := json.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "suite.json")
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	err = teamCommandIO([]string{"benchmark", "--suite", path, "--local-dir", filepath.Join(dir, "missing-local"), "--baseline-dir", filepath.Join(dir, "missing-baseline")}, &out, &diag)
	if err == nil {
		t.Fatal("missing reports succeeded")
	}
	var report team.Comparison
	if json.Unmarshal(out.Bytes(), &report) != nil || report.Eligible || report.ReductionPercent != nil || report.Target99Established || len(report.Cases) != 6 {
		t.Fatalf("missing cases dropped %s", out.String())
	}
}

func TestTeamGenerationPolicyFlagsCannotBeSilentlyIgnored(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--json-output"},
		{"run", "--canonical-prompt"},
		{"status", "--canonical-prompt=false"},
		{"plan", "--release-model"},
		{"status", "--policy-revision", "new-policy"},
		{"status", "--think"},
		{"status", "--think=false"},
		{"run", "--output-tokens", "2048"},
		{"run", "--output-tokens", "512"},
		{"init", "--output-tokens", "0"},
		{"suite", "--output-tokens", "8193"},
		{"init", "--driver", "codex-oss", "--json-output"},
		{"init", "--driver", "codex-oss", "--canonical-prompt"},
		{"suite", "--driver", "codex-oss", "--release-model"},
		{"init", "--driver", "codex-oss", "--think"},
	} {
		var out, diag bytes.Buffer
		if err := teamCommandIO(args, &out, &diag); err == nil || out.Len() != 0 {
			t.Fatal("inapplicable generation policy flags were ignored", args, err)
		}
	}
}

func TestTeamOutputTokenFlagsUseDriverBoundsBeforeMetadata(t *testing.T) {
	for _, command := range []string{"init", "suite"} {
		for _, profile := range []struct{ driver, think string }{{"ollama", ""}, {"ollama", "false"}, {"ollama", "true"}, {"codex-oss", ""}} {
			for _, tokens := range []int64{-1, 15, 16, 4096, 4097, 8192, 8193} {
				t.Run(command+"/"+profile.driver+"/think="+profile.think+"/"+strconv.FormatInt(tokens, 10), func(t *testing.T) {
					var out, diag bytes.Buffer
					// A cloud marker is rejected by the local constructor before
					// metadata HTTP, even when the output allowance is valid.
					args := []string{command, "--driver", profile.driver, "--output-tokens", strconv.FormatInt(tokens, 10), "--model", "fixture:cloud"}
					if profile.think != "" {
						args = append(args, "--think="+profile.think)
					}
					err := teamCommandIO(args, &out, &diag)
					valid := tokens >= 16 && tokens <= 8192 && (tokens <= 4096 || (profile.driver == "ollama" && profile.think == "true"))
					if err == nil || out.Len() != 0 {
						t.Fatalf("fixture unexpectedly prepared a team: %v", err)
					}
					if valid {
						if err.Error() != "team init requires an installed local model" {
							t.Fatalf("valid allowance rejected before constructor validation: %v", err)
						}
					} else if !strings.Contains(err.Error(), "output-tokens between 16 and 4096, or up to 8192 only with direct ollama --think") {
						t.Fatalf("invalid allowance reached model discovery: %v", err)
					}
				})
			}
		}
	}
}

func TestTeamOutputTokenHelpKeepsDefaultAndDriverBounds(t *testing.T) {
	for _, command := range []string{"init", "suite"} {
		var out, diag bytes.Buffer
		if err := teamCommandIO([]string{command, "--help"}, &out, &diag); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(diag.String(), "16..4096 normally, 16..8192 only with --driver ollama --think (default 512)") {
			t.Fatalf("output allowance default or driver bounds missing: %s", diag.String())
		}
	}
}

func TestTeamPlanAndPromptDiscloseOnlyExplicitRuntimeOutputTypes(t *testing.T) {
	role := team.Role{ID: "master", Driver: "ollama", Model: "fixture:local", ModelDigest: strings.Repeat("a", 64), Instructions: "Return the requested answer.", MaxInputTokens: 4096, MaxOutputTokens: 256}
	reviewer := role
	reviewer.ID = "review-correctness"
	r := team.Request{Schema: 1, Task: "fixture/typed", Revision: "one", Instructions: "Return JSON with waiting_count. Subtract running from total.", Input: "There are 9 jobs total and 3 running.", Master: role, Reviewers: []team.Role{reviewer}, Checks: []evaluation.Check{{ID: "expected", Kind: "json_equals", Expected: json.RawMessage(`{"waiting_count":6}`)}}, ReuseForSeconds: 3600}
	r.Master.Ollama = &inference.OllamaOptions{Format: "json", PromptMode: "bounded_text"}
	canonicalPrompt, err := team.MasterPrompt(r)
	if err != nil {
		t.Fatal(err)
	}
	canonicalTask := team.TaskDigest(r)
	for _, typed := range []bool{false, true} {
		if typed {
			r.Master.Ollama.OutputTypes = map[string]string{"waiting_count": "integer"}
		}
		dir := t.TempDir()
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "task.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
		for _, command := range []string{"plan", "prompt"} {
			var out, diag bytes.Buffer
			if err := teamCommandIO([]string{command, "--workspace", dir, "--request", "task.json"}, &out, &diag); err != nil {
				t.Fatal(command, typed, err)
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(out.Bytes(), &fields) != nil {
				t.Fatal("invalid offline team metadata", out.String())
			}
			contract, present := fields["runtime_output_types"]
			var declared map[string]string
			if present != typed || typed && (json.Unmarshal(contract, &declared) != nil || len(declared) != 1 || declared["waiting_count"] != "integer") {
				t.Fatal("declared type map hidden or invented", command, typed, out.String())
			}
			if command == "prompt" {
				var prompt, task string
				if json.Unmarshal(fields["prompt"], &prompt) != nil || json.Unmarshal(fields["task_sha256"], &task) != nil || prompt != canonicalPrompt || task != canonicalTask {
					t.Fatal("runtime disclosure changed canonical baseline prompt or task", out.String())
				}
			}
		}
	}
}
