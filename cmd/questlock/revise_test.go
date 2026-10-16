package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spfuzzylink/questlock/worker"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/revision"
)

func saveRevisionRequest(t *testing.T, request revision.Request) string {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "approved-revision.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReviseInlinePlanNeedsNoCredentialOrNetwork(t *testing.T) {
	for _, key := range []string{"QUESTLOCK_TOKEN", "QUESTLOCK_TOKEN_FILE", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENROUTER_API_KEY", "SGLANG_API_KEY"} {
		t.Setenv(key, "")
	}
	request := reviseQuestRequest("plan", "Approved source.", "source", "result", 1)
	path := saveRevisionRequest(t, request)
	var output, diagnostics bytes.Buffer
	if err := reviseCommandIO([]string{"plan", "--request", path, "--broker", "http://127.0.0.1:1"}, &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	var result revision.Result
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "planned" || result.Durable || result.Artifact != nil || result.Text != "Approved result." || result.GenerationCalled || diagnostics.Len() != 0 {
		t.Fatalf("incorrect plan: %+v", result)
	}
}

func TestReviseCLIRunAndExactRepeatUseOnlyBroker(t *testing.T) {
	fixture, err := newModelQuestFixture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.close()
	t.Setenv("QUESTLOCK_TOKEN", fixture.tokens[0])
	t.Setenv("QUESTLOCK_TOKEN_FILE", "")
	for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENROUTER_API_KEY", "SGLANG_API_KEY"} {
		t.Setenv(key, "")
	}
	request := reviseQuestRequest("run", "Approved source.", "source", "result", 1)
	path := saveRevisionRequest(t, request)
	for invocation := 0; invocation < 2; invocation++ {
		var output, diagnostics bytes.Buffer
		if err := reviseCommandIO([]string{"run", "--request", path, "--broker", fixture.server.URL, "--timeout", "5s"}, &output, &diagnostics); err != nil {
			t.Fatal(err)
		}
		var result revision.Result
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if err := reviseQuestCompleted(result, nil); err != nil {
			t.Fatal(err)
		}
		if result.Reused != (invocation == 1) || result.Text != "Approved result." || diagnostics.Len() != 0 {
			t.Fatalf("unexpected revision %+v", result)
		}
	}
}

func TestReviseCLIWithholdsFailedCheck(t *testing.T) {
	fixture, err := newModelQuestFixture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.close()
	t.Setenv("QUESTLOCK_TOKEN", fixture.tokens[0])
	t.Setenv("QUESTLOCK_TOKEN_FILE", "")
	request := reviseQuestRequest("failed", "PRIVATE SOURCE.", "SOURCE", "FAILED_CANDIDATE_SECRET", 1)
	request.Checks[0].Text = "MISSING CHECK"
	path := saveRevisionRequest(t, request)
	var output, diagnostics bytes.Buffer
	err = reviseCommandIO([]string{"run", "--request", path, "--broker", fixture.server.URL}, &output, &diagnostics)
	if err == nil {
		t.Fatal("failed check returned successful exit")
	}
	var result revision.Result
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "quality_failed" || result.Text != "" || result.Durable || strings.Contains(output.String(), "PRIVATE") || strings.Contains(output.String(), "FAILED_CANDIDATE_SECRET") {
		t.Fatalf("failed candidate leaked: %s", output.String())
	}
}

func TestReviseCLIFlagsAreControlled(t *testing.T) {
	for _, args := range [][]string{{}, {"quest", "extra"}, {"run"}, {"run", "--request", "missing", "--timeout", "0s"}, {"run", "--request", "missing", "--timeout", "11m"}, {"run", "--PRIVATE_KEY_SECRET"}} {
		var output, diagnostics bytes.Buffer
		err := reviseCommandIO(args, &output, &diagnostics)
		if err == nil || strings.Contains(err.Error(), "PRIVATE") || output.Len() != 0 {
			t.Fatalf("bad flags accepted or echoed: %v", err)
		}
	}
}

func TestReviseQuestOffline(t *testing.T) {
	for _, key := range []string{"QUESTLOCK_TOKEN", "QUESTLOCK_TOKEN_FILE", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENROUTER_API_KEY", "SGLANG_API_KEY"} {
		t.Setenv(key, "")
	}
	var output, diagnostics bytes.Buffer
	if err := reviseCommandIO([]string{"quest"}, &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "PASS  ") != 6 || !strings.Contains(output.String(), "QUEST CLEARED") || !strings.Contains(output.String(), "host and broker costs are excluded") || diagnostics.Len() != 0 {
		t.Fatal(output.String())
	}
}

func TestWorkerServeRegistersAndExecutesCheckedRevisionWithoutProvider(t *testing.T) {
	fixture, err := newModelQuestFixture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.close()
	t.Setenv("QUESTLOCK_TOKEN", fixture.tokens[0])
	t.Setenv("QUESTLOCK_TOKEN_FILE", "")
	for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENROUTER_API_KEY", "SGLANG_API_KEY"} {
		t.Setenv(key, "")
	}
	request := reviseQuestRequest("mcp-command", "Approved source.", "source", "result", 1)
	request.TrackOutcome = true
	path := saveRevisionRequest(t, request)
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	var diagnostics bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- workerCommandIO([]string{"serve", "--workspace", filepath.Dir(path), "--broker", fixture.server.URL, "--ollama-url", "http://127.0.0.1:1", "--timeout", "5s"}, serverConn, serverConn, &diagnostics)
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "revision-cli", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: clientConn, Writer: clientConn}, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := session.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 2 {
		t.Fatalf("worker serve tools: %v", err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: worker.ReviseToolName, Arguments: map[string]any{"request_path": filepath.Base(path)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 {
		t.Fatal("missing revision result")
	}
	var output revision.Result
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &output); err != nil {
		t.Fatal(err)
	}
	if result.IsError || output.Text != "Approved result." || output.GenerationCalled || output.OutcomeTracking == nil || output.OutcomeTracking.Status != "recorded" {
		t.Fatalf("native command revision: %+v", output)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	clientConn.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("worker did not close after disconnect")
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("unexpected diagnostics: %s", diagnostics.String())
	}
}
