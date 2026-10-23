package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/revision"
)

func readWorkerStatus(t *testing.T, session *mcp.ClientSession) Status {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: StatusToolName, Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("status call: %+v %v", result, err)
	}
	var status Status
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != string(data) {
		// Text and structured maps may encode keys in different order.
		var textStatus Status
		if len(result.Content) != 1 || json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &textStatus) != nil || !reflect.DeepEqual(status, textStatus) {
			t.Fatal("status text and structured output differ")
		}
	}
	return status
}

func TestStatusDiscoversCustomContractWithoutReadingContentOrExecuting(t *testing.T) {
	root := t.TempDir()
	const private = "PRIVATE_CONFIG_CREDENTIAL_CONTENT"
	if err := os.WriteFile(filepath.Join(root, "unrelated-private-config"), []byte(private), 0000); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QUESTLOCK_STATUS_SECRET", private)
	var calls atomic.Int32
	session, _, _, _ := connectWorker(t, Options{Workspace: root, Version: "0.12.0-fixture", ProfileID: "writing-custom", AllowPaid: true, Execute: func(context.Context, delegation.Request) (delegation.Result, error) {
		calls.Add(1)
		return delegation.Result{}, errors.New(private)
	}}, "")
	status := readWorkerStatus(t, session)
	if status.Schema != 1 || status.RuntimeVersion != "0.12.0-fixture" || status.RequestRoot != root || status.Profile != (StatusProfile{ID: "writing-custom", Kind: "custom"}) || !status.Policy.AllowPaid || !status.Policy.PaidRequiresExplicitRequestBudgets {
		t.Fatalf("contract differs from process options: %+v", status)
	}
	if !reflect.DeepEqual(status.RequestSchemas.Delegation, []string{"unversioned"}) || len(status.RequestSchemas.Revision) != 0 || status.Checks.Schema != evaluation.Schema || !reflect.DeepEqual(status.Checks.Kinds, evaluation.SupportedCheckKinds()) || status.WorkflowConfigs["writing"] != "writing-workflow.json" {
		t.Fatalf("inaccurate schema/check capabilities: %+v", status)
	}
	if status.Readiness != (StatusReadiness{Worker: "ready", RequestRoot: "opened", Broker: "not_checked", Providers: "not_checked"}) || !status.ReadOnly || status.ModelCalls != 0 || calls.Load() != 0 {
		t.Fatalf("discovery inferred external readiness or executed: %+v", status)
	}
	data, _ := json.Marshal(status)
	if strings.Contains(string(data), private) || strings.Contains(string(data), "unrelated-private-config") {
		t.Fatal("status disclosed environment, file listings or content")
	}
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != StatusToolName {
			continue
		}
		a := tool.Annotations
		if !a.ReadOnlyHint || !a.IdempotentHint || a.DestructiveHint == nil || *a.DestructiveHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Fatalf("status annotations permit effects: %+v", a)
		}
		schema := tool.InputSchema.(map[string]any)
		if schema["additionalProperties"] != false || len(schema["properties"].(map[string]any)) != 0 {
			t.Fatalf("status accepts authority-bearing inputs: %+v", schema)
		}
		return
	}
	t.Fatal("status tool not discoverable")
}

func TestStatusAvailableWhileEitherTaskToolIsBusy(t *testing.T) {
	for _, tool := range []string{ToolName, ReviseToolName} {
		t.Run(tool, func(t *testing.T) {
			root := t.TempDir()
			requestFile(t, root, "delegate.json", approvedRequest())
			revisionFile(t, root, "revision.json", revisionRequestFixture())
			started, release := make(chan struct{}), make(chan struct{})
			block := func(ctx context.Context) {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			session, _, _, _ := connectWorker(t, Options{Workspace: root, Execute: func(ctx context.Context, _ delegation.Request) (delegation.Result, error) {
				block(ctx)
				return delegation.Result{}, errors.New("fixture")
			}, Revise: func(ctx context.Context, _ revision.Request) (revision.Result, error) {
				block(ctx)
				return revision.Result{}, errors.New("fixture")
			}}, "")
			path := "delegate.json"
			if tool == ReviseToolName {
				path = "revision.json"
			}
			done := make(chan error, 1)
			go func() {
				_, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: map[string]any{"request_path": path}})
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("task did not start")
			}
			defer close(release)
			status := readWorkerStatus(t, session)
			if status.Readiness.Worker != "busy" || !reflect.DeepEqual(status.RequestSchemas.Revision, []int{revision.Schema}) || len(status.Tools) != 3 {
				t.Fatalf("busy worker status unavailable/inaccurate: %+v", status)
			}
		})
	}
}

func TestStatusRejectsNonemptyArgumentsWithoutEcho(t *testing.T) {
	session, _, _, _ := connectWorker(t, Options{Workspace: t.TempDir(), Execute: func(context.Context, delegation.Request) (delegation.Result, error) {
		t.Fatal("status executed delegation")
		return delegation.Result{}, nil
	}}, "")
	for _, args := range []any{map[string]any{"request_path": "PRIVATE_PATH"}, "PRIVATE_CONTENT", []any{}, json.RawMessage(`{"PRIVATE_KEY":true}`)} {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: StatusToolName, Arguments: args})
		if err != nil {
			// SDK type validation is redacted by the transport.
			if strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("status argument error echoed caller input")
			}
			continue
		}
		data, _ := json.Marshal(result)
		if !result.IsError || strings.Contains(string(data), "PRIVATE") {
			t.Fatalf("invalid status authority accepted or echoed: %s", data)
		}
	}
}

func TestStatusProfileLabelsAreBoundedNonsecretTokens(t *testing.T) {
	for _, profile := range []StatusProfile{{ID: "https://credential@broker"}, {ID: strings.Repeat("x", 65)}, {ID: "label\ncontents"}, {Kind: "unknown"}} {
		if validProfile(profile.ID, profile.Kind) {
			t.Fatalf("accepted unbounded/non-label profile: %+v", profile)
		}
	}
}
