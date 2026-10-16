package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/revision"
)

func revisionRequestFixture() revision.Request {
	text := "Questlock reduces repeated work. #Questlock"
	return revision.Request{Schema: revision.Schema, Task: "approved-revision", Revision: "v1", Source: revision.Source{Text: &text, SHA256: revision.Digest(text)}, Edits: []revision.Edit{{Old: "Questlock reduces", New: "𝐐𝐮𝐞𝐬𝐭𝐥𝐨𝐜𝐤 reduces", ExpectedMatches: 1}}, SourceChecks: []evaluation.Check{{ID: "source", Kind: "contains", Text: "Questlock reduces"}}, Checks: []evaluation.Check{{ID: "bold", Kind: "contains", Text: "𝐐𝐮𝐞𝐬𝐭𝐥𝐨𝐜𝐤"}, {ID: "searchable-hashtag", Kind: "contains", Text: "#Questlock"}}}
}

func revisionFile(t *testing.T, root, name string, request revision.Request) string {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func revisionBroker(t *testing.T) *client.Client {
	t.Helper()
	storage, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { storage.Close() })
	token, err := storage.CreateToken(t.Context(), "revision-worker", "revision-test")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(storage))
	t.Cleanup(server.Close)
	broker, err := client.New(server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	return broker
}

func callRevision(t *testing.T, session *mcp.ClientSession, path string) (*mcp.CallToolResult, revisionOutput) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: ReviseToolName, Arguments: map[string]any{"request_path": path}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 {
		t.Fatal("missing revision content")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatal("unexpected revision content type")
	}
	var output revisionOutput
	if err := json.Unmarshal([]byte(text.Text), &output); err != nil {
		t.Fatal(err)
	}
	return result, output
}

func TestRevisionMCPProtocolsUseBrokerWithoutDelegating(t *testing.T) {
	for _, protocol := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(protocol, func(t *testing.T) {
			root := t.TempDir()
			path := revisionFile(t, root, "approved.json", revisionRequestFixture())
			runner := revision.Runner{Broker: revisionBroker(t)}
			session, _, _, _ := connectWorker(t, Options{Workspace: root, Execute: func(context.Context, delegation.Request) (delegation.Result, error) {
				t.Error("revision invoked model delegation")
				return delegation.Result{}, errors.New("must not delegate")
			}, Revise: runner.Run}, protocol)
			tools, err := session.ListTools(t.Context(), nil)
			if err != nil || len(tools.Tools) != 2 {
				t.Fatalf("revision tool discovery: %v", err)
			}
			for _, tool := range tools.Tools {
				if tool.Name == ReviseToolName && (tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint) {
					t.Fatal("checked revision advertises remote model access")
				}
			}
			for invocation := 0; invocation < 2; invocation++ {
				result, output := callRevision(t, session, path)
				if result.IsError || output.Status != "completed" || !output.Durable || output.GenerationCalled || output.Reused != (invocation == 1) || output.Text != "𝐐𝐮𝐞𝐬𝐭𝐥𝐨𝐜𝐤 reduces repeated work. #Questlock" {
					t.Fatalf("checked revision failed: %+v", output)
				}
			}
		})
	}
}

func TestRevisionMCPRejectsUnsafePathsAndMalformedRequests(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	approved := revisionFile(t, root, "nested/approved.json", revisionRequestFixture())
	outsidePath := revisionFile(t, outside, "OUTSIDE_SECRET.json", revisionRequestFixture())
	if err := os.Symlink(approved, filepath.Join(root, "link.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nested", filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bad.json"), []byte(`{"SECRET":"PRIVATE"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var executed atomic.Int32
	session, _, _, _ := connectWorker(t, Options{Workspace: root, Execute: func(context.Context, delegation.Request) (delegation.Result, error) {
		t.Error("delegation executed")
		return delegation.Result{}, nil
	}, Revise: func(context.Context, revision.Request) (revision.Result, error) {
		executed.Add(1)
		return revision.Result{}, nil
	}}, "")
	for _, path := range []string{outsidePath, "../OUTSIDE_SECRET.json", "nested/../nested/approved.json", "link.json", "linkdir/approved.json", "bad.json", "missing.json", "nested", "."} {
		result, output := callRevision(t, session, path)
		if !result.IsError || output.ErrorCategory != "invalid_request" {
			t.Fatalf("unsafe request accepted: %+v", output)
		}
		data, _ := json.Marshal(result)
		if strings.Contains(string(data), "SECRET") || strings.Contains(string(data), root) || strings.Contains(string(data), outside) {
			t.Fatal("unsafe request error echoed data")
		}
	}
	if executed.Load() != 0 {
		t.Fatal("unsafe revision executed")
	}
}

func TestRevisionMCPFailureWithholdsCandidateAndRawErrors(t *testing.T) {
	for _, fail := range []string{"quality", "error", "not-durable", "bad-digest", "bad-check", "bad-version", "charged", "canceled"} {
		t.Run(fail, func(t *testing.T) {
			root := t.TempDir()
			path := revisionFile(t, root, "approved.json", revisionRequestFixture())
			runner := revision.Runner{Broker: revisionBroker(t)}
			session, _, _, _ := connectWorker(t, Options{Workspace: root, Execute: func(context.Context, delegation.Request) (delegation.Result, error) {
				t.Error("delegation executed")
				return delegation.Result{}, nil
			}, Revise: func(ctx context.Context, request revision.Request) (revision.Result, error) {
				result, err := runner.Run(ctx, request)
				if err != nil {
					return result, err
				}
				switch fail {
				case "quality":
					result.Status = "quality_failed"
					result.Text = "FAILED_CONTENT_SECRET"
				case "error":
					return result, errors.New("RAW_KEY_SECRET")
				case "not-durable":
					result.Durable = false
				case "bad-digest":
					result.OutputSHA256 = strings.Repeat("a", 64)
				case "bad-check":
					result.SourceChecks[0].Passed = false
				case "bad-version":
					result.Artifact.Version = 2
				case "charged":
					result.ThisRunUsage.InputTokens = 1
				case "canceled":
					return result, context.Canceled
				}
				return result, nil
			}}, "")
			result, output := callRevision(t, session, path)
			if !result.IsError || output.Text != "" {
				t.Fatalf("failed result exposed text: %+v", output)
			}
			data, _ := json.Marshal(result)
			if strings.Contains(string(data), "SECRET") {
				t.Fatal("failed result exposed candidate or error")
			}
		})
	}
}

func TestRevisionAndDelegationShareAdmission(t *testing.T) {
	root := t.TempDir()
	revisionPath := revisionFile(t, root, "revise.json", revisionRequestFixture())
	requestFile(t, root, "delegate.json", approvedRequest())
	started, release := make(chan struct{}), make(chan struct{})
	var revisionCalls atomic.Int32
	session, _, _, _ := connectWorker(t, Options{Workspace: root, Execute: func(ctx context.Context, _ delegation.Request) (delegation.Result, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return delegation.Result{}, ctx.Err()
		}
		return completedResult(), nil
	}, Revise: func(context.Context, revision.Request) (revision.Result, error) {
		revisionCalls.Add(1)
		return revision.Result{}, nil
	}}, "")
	done := make(chan error, 1)
	go func() {
		_, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolName, Arguments: map[string]any{"request_path": "delegate.json"}})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("delegate did not start")
	}
	result, output := callRevision(t, session, revisionPath)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !result.IsError || output.ErrorCategory != "worker_busy" || revisionCalls.Load() != 0 {
		t.Fatal("revision bypassed shared admission")
	}
}

func TestRevisionWireCancellationReleasesSharedAdmission(t *testing.T) {
	root := t.TempDir()
	path := revisionFile(t, root, "revise.json", revisionRequestFixture())
	started := make(chan struct{})
	stopped := make(chan struct{})
	var calls atomic.Int32
	session, log, _, _ := connectWorker(t, Options{Workspace: root, Execute: func(context.Context, delegation.Request) (delegation.Result, error) {
		t.Error("revision delegated")
		return delegation.Result{}, nil
	}, Revise: func(ctx context.Context, _ revision.Request) (revision.Result, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			close(stopped)
			return revision.Result{}, ctx.Err()
		}
		return revision.Result{}, errors.New("controlled failure")
	}}, "2026-07-28")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: ReviseToolName, Arguments: map[string]any{"request_path": path}})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("revision did not start")
	}
	busy, output := callRevision(t, session, path)
	if !busy.IsError || output.ErrorCategory != "worker_busy" {
		t.Fatal("revision did not share admission")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("wire cancellation did not reach revision")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected cancellation: %v", err)
	}
	wait, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !strings.Contains(log.String(), `"error_category":"canceled"`) {
		select {
		case <-ticker.C:
		case <-wait.Done():
			t.Fatal("revision handler did not finish cancellation")
		}
	}
	result, output := callRevision(t, session, path)
	if !result.IsError || output.ErrorCategory != "stopped" || calls.Load() != 2 {
		t.Fatal("shared admission was not released after cancellation")
	}
}
