package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
)

func TestModelEvaluateOfflineCLISelectsBalancedAndCreatesPrivateReport(t *testing.T) {
	t.Setenv("QUESTLOCK_TOKEN", "")
	t.Setenv("QUESTLOCK_TOKEN_FILE", filepath.Join(t.TempDir(), "missing-token"))
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	// Disabled telemetry must not inspect or validate environment configuration.
	t.Setenv("QUESTLOCK_LANGFUSE_ENDPOINT", "invalid-private-endpoint")
	reportPath := filepath.Join(t.TempDir(), "evaluation.json")
	var output, diagnostics bytes.Buffer
	if err := modelCommandIO([]string{"evaluate", "--report", reportPath}, &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	var report struct {
		evaluation.Report
		Environment evaluationEnvironment `json:"environment"`
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("command did not emit one report: %v", err)
	}
	if report.Mode != "fixture" || report.Selection == nil || report.Selection.ConfigurationID != "balanced-256" || report.Selection.Scope != "simulated_fixture_suite_only" {
		t.Fatalf("fixture selection was misrepresented: %+v", report.Selection)
	}
	if len(report.Results) != 12 || len(report.Candidates) != 2 || report.Candidates[0].CasesPassed != 4 || report.Candidates[0].Eligible || report.Candidates[1].CasesPassed != 6 || !report.Candidates[1].Eligible {
		t.Fatalf("negative controls did not gate selection: %+v", report.Candidates)
	}
	negativeControls := 0
	for _, row := range report.Results {
		if row.First.TimingSource != "simulated_fixture" || row.First.Inference != nil || row.First.CostUSD != nil {
			t.Fatalf("fixture row claimed live measurements: %+v", row)
		}
		if row.ConfigurationID == "compact-64" && row.CaseID == "incident-handoff" {
			negativeControls++
			if row.First.ErrorCategory != "incomplete" || row.Eligible || row.Reuse != nil {
				t.Fatalf("incomplete negative control was retried or eligible: %+v", row)
			}
		}
		if row.ConfigurationID == "compact-64" && row.CaseID == "release-readiness" {
			negativeControls++
			if !row.First.Complete || !row.First.Durable || row.First.QualityPassed || row.Eligible || row.Reuse == nil || !row.Reuse.Reused {
				t.Fatalf("complete wrong answer passed the quality gate: %+v", row)
			}
		}
		if row.ConfigurationID == "balanced-256" && (row.Reuse == nil || row.Reuse.ThisRunUsage != (inference.Usage{}) || row.Reuse.AvoidedUsage != row.First.ThisRunUsage || row.Reuse.OutputSHA256 != row.First.OutputSHA256) {
			t.Fatalf("CLI report lost exact reuse accounting: %+v", row)
		}
	}
	if negativeControls != 2 || report.Environment.OS == "" || report.Environment.Architecture == "" || report.Environment.CPUs < 1 || report.Environment.GoVersion == "" || report.Environment.LoadState != "not_controlled" || len(report.Environment.LocalModelDigests) != 0 || diagnostics.Len() != 0 {
		t.Fatalf("unexpected CLI environment or diagnostics: %+v, %q", report.Environment, diagnostics.String())
	}
	for _, c := range evaluation.DefaultSuite().Cases {
		for _, fixture := range c.Fixtures {
			if strings.Contains(output.String(), fixture.Response.Text) {
				t.Fatal("evaluation report retained fixture response text")
			}
		}
	}
	if strings.Contains(output.String(), `"text"`) || strings.Contains(output.String(), `"response"`) {
		t.Fatal("evaluation report retained prompt/output body fields")
	}
	saved, err := os.ReadFile(reportPath)
	if err != nil || !bytes.Equal(saved, output.Bytes()) {
		t.Fatalf("private report differed from emitted report: %v", err)
	}
	info, err := os.Stat(reportPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("report is not private: %v, %v", info, err)
	}
	output.Reset()
	if err := modelCommandIO([]string{"evaluate", "--report", reportPath}, &output, &diagnostics); err == nil || output.Len() != 0 {
		t.Fatalf("existing report path was overwritten or emitted a new report: %v", err)
	}
	after, err := os.ReadFile(reportPath)
	if err != nil || !bytes.Equal(after, saved) {
		t.Fatalf("exclusive report creation altered the existing file: %v", err)
	}
}

func TestModelEvaluateCLIFailedQualityStillEmitsContentFreeReport(t *testing.T) {
	const privateText = "private-approved-evaluation-material"
	suite := evaluation.DefaultSuite()
	suite.Cases = suite.Cases[:1]
	suite.Configurations = suite.Configurations[:1]
	suite.Cases[0].Instructions = privateText
	suite.Cases[0].Input = privateText
	fixture := suite.Cases[0].Fixtures[suite.Configurations[0].ID]
	fixture.Response.Text = `{"issue":"WRONG","deadline":"2030-04-03","private":"` + privateText + `"}`
	suite.Cases[0].Fixtures = map[string]evaluation.Fixture{suite.Configurations[0].ID: fixture}
	data, err := json.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	suitePath := filepath.Join(t.TempDir(), "suite.json")
	if err := os.WriteFile(suitePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(t.TempDir(), "report.json")
	var output, diagnostics bytes.Buffer
	err = modelCommandIO([]string{"evaluate", "--suite", suitePath, "--report", reportPath}, &output, &diagnostics)
	if err == nil || !strings.Contains(err.Error(), "no configuration met") {
		t.Fatalf("known wrong answer received a successful CLI exit: %v", err)
	}
	var report evaluation.Report
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Selection != nil || len(report.Results) != 1 || report.Results[0].Eligible || report.Results[0].First.QualityPassed || !report.Results[0].First.Complete || !report.Results[0].First.Durable || report.Results[0].Reuse == nil {
		t.Fatalf("quality failure did not retain inspectable protocol/reuse metadata: %+v", report)
	}
	saved, readErr := os.ReadFile(reportPath)
	if readErr != nil || !bytes.Equal(saved, output.Bytes()) || strings.Contains(output.String(), privateText) || strings.Contains(err.Error(), privateText) || diagnostics.Len() != 0 {
		t.Fatalf("failed evaluation lost report or exposed approved material: %v", readErr)
	}
}

func TestModelEvaluateCLIRejectsFixtureTelemetryBeforeCreatingReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "must-not-exist.json")
	var output, diagnostics bytes.Buffer
	err := modelCommandIO([]string{"evaluate", "--langfuse", "--report", path}, &output, &diagnostics)
	if err == nil || !strings.Contains(err.Error(), "requires --live") || output.Len() != 0 || diagnostics.Len() != 0 {
		t.Fatalf("simulated fixture telemetry was accepted: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid evaluation created a report: %v", err)
	}
}
