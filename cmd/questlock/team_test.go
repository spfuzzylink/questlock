package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
