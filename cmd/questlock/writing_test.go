package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/writing"
)

func writingInputs(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "approved")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	ref := filepath.Join(root, "reference.json")
	evidence := filepath.Join(root, "evidence.json")
	if err := os.WriteFile(ref, []byte(`{"schema":1,"id":"synthetic-team","revision":"one","facts":["We build support tools."],"style":["Plain language. Short paragraphs."]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidence, []byte(`{"schema":1,"id":"support","revision":"one","question":"Explain the current support pilot.","facts":["The pilot has two volunteers."],"sources":["synthetic-brief"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	return workspace, ref, evidence
}

func TestWritingCLIPrivateFreshChatPlanAndEvidenceBinding(t *testing.T) {
	workspace, ref, evidence := writingInputs(t)
	var output, diagnostics bytes.Buffer
	if err := writingCommandIO([]string{"init", "--workspace", workspace, "--reference", ref, "--model", "synthetic:1"}, &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "support tools") || strings.Contains(output.String(), "Short paragraphs") {
		t.Fatal("init echoed private reference contents")
	}
	configPath := filepath.Join(workspace, writing.ConfigName)
	data, _ := os.ReadFile(configPath)
	var config writing.Config
	if err := json.Unmarshal(data, &config); err != nil || config.Schema != 1 || config.ReferencePath != ref || config.Recipe.Model != "synthetic:1" {
		t.Fatalf("invalid discoverable config: %s", data)
	}
	info, _ := os.Stat(configPath)
	if info.Mode().Perm() != 0600 {
		t.Fatal("private config has permissive mode")
	}
	output.Reset()
	if err := writingCommandIO([]string{"plan", "--workspace", workspace, "--evidence", evidence, "--request", "first.json"}, &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	var request delegation.Request
	raw, err := os.ReadFile(filepath.Join(workspace, "first.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &request); err != nil || delegation.Validate(request, false) != nil || request.Policy.Escalation != nil || !request.TrackOutcome {
		t.Fatalf("unsafe request: %v", err)
	}
	var plan writing.Plan
	raw, _ = os.ReadFile(filepath.Join(workspace, "first.json.writing.json"))
	if err := json.Unmarshal(raw, &plan); err != nil || writing.ValidatePlan(plan) != nil || plan.RequestSHA256 != delegation.RequestDigest(request) {
		t.Fatalf("provenance mismatch: %v", err)
	}
	if strings.Contains(output.String(), "two volunteers") || strings.Contains(output.String(), "support tools") || !strings.Contains(output.String(), `"model_calls": 0`) {
		t.Fatal("plan leaked private facts or lost no-model declaration")
	}
	first := plan.RequestSHA256
	changed, _ := os.ReadFile(evidence)
	changed = bytes.Replace(changed, []byte("two volunteers"), []byte("three volunteers"), 1)
	if err := os.WriteFile(evidence, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writingCommandIO([]string{"plan", "--workspace", workspace, "--evidence", evidence, "--request", "changed.json"}, &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(workspace, "changed.json.writing.json"))
	if err := json.Unmarshal(raw, &plan); err != nil || plan.RequestSHA256 == first {
		t.Fatal("changed evidence reused old snapshot identity")
	}
	if err := os.WriteFile(ref, []byte(`{"schema":1,"id":"synthetic-team","revision":"two","facts":["Changed facts."],"style":["Plain prose."]}`), 0600); err != nil {
		t.Fatal(err)
	}
	err = writingCommandIO([]string{"plan", "--workspace", workspace, "--evidence", evidence, "--request", "drift.json"}, &output, &diagnostics)
	if err == nil || !strings.Contains(err.Error(), "reference changed") {
		t.Fatalf("profile drift accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "drift.json")); !os.IsNotExist(err) {
		t.Fatal("unapproved drift wrote request")
	}
}

func TestWritingCLIPreservesExistingFilesAndBoundaries(t *testing.T) {
	workspace, ref, evidence := writingInputs(t)
	var output, diagnostics bytes.Buffer
	args := []string{"init", "--workspace", workspace, "--reference", ref, "--model", "synthetic:1"}
	if err := writingCommandIO(args, &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(workspace, writing.ConfigName))
	if err := writingCommandIO(args, &output, &diagnostics); err == nil {
		t.Fatal("init overwrote existing operator config")
	}
	after, _ := os.ReadFile(filepath.Join(workspace, writing.ConfigName))
	if !bytes.Equal(before, after) {
		t.Fatal("operator config changed")
	}
	for _, name := range []string{"../outside.json", "/tmp/outside.json", ".hidden.json", "request\\outside.json"} {
		if err := writingCommandIO([]string{"plan", "--workspace", workspace, "--evidence", evidence, "--request", name}, &output, &diagnostics); err == nil {
			t.Errorf("unsafe name accepted: %q", name)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte("operator-owned"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "linked.json.writing.json")); err != nil {
		t.Fatal(err)
	}
	if err := writingCommandIO([]string{"plan", "--workspace", workspace, "--evidence", evidence, "--request", "linked.json"}, &output, &diagnostics); err == nil {
		t.Fatal("plan replaced symlink")
	}
	after, _ = os.ReadFile(outside)
	if string(after) != "operator-owned" {
		t.Fatal("plan changed external file")
	}
	if err := writingCommandIO([]string{"plan", "--workspace", workspace, "--evidence", evidence, "--request", "valid.json", "--model", "ignored"}, &output, &diagnostics); err == nil {
		t.Fatal("plan silently ignored changed model")
	}
	if err := writingCommandIO([]string{"record", "--workspace", workspace, "--manifest", "valid.json.writing.json", "--result", outside, "--review", "human_accepted"}, &output, &diagnostics); err == nil {
		t.Fatal("record attested human acceptance")
	}
}
