package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
)

func approvedRequest() delegation.Request {
	return delegation.Request{
		Spec: inference.Spec{Task: "approved-task", Revision: "revision-1", Instructions: "Answer from approved input.", Input: "Return approved answer."},
		Policy: delegation.Policy{Local: delegation.Tier{ID: "local", Spec: inference.Spec{
			Provider: "ollama", Model: "fixture-local", MaxContextBytes: 1024, MaxInputTokens: 256, MaxOutputTokens: 64, ReuseForSeconds: 60,
		}}},
		Checks: []evaluation.Check{{ID: "approved", Kind: "contains", Text: "approved answer"}},
	}
}

func completedResult() delegation.Result {
	return delegation.Result{
		RequestSHA256: strings.Repeat("a", 64), Status: "completed", SelectedTier: "local",
		Response: &inference.Response{Text: "approved answer", Complete: true, Model: "fixture-local", StopReason: "stop"},
		Attempts: []delegation.Attempt{{TierID: "local", QualityPassed: true, Result: inference.Result{
			Response: inference.Response{Text: "FAILED_OUTPUT_MUST_NOT_APPEAR", ID: "UPSTREAM_ID_SECRET", Model: "UPSTREAM_MODEL_SECRET", StopReason: "UPSTREAM_STOP_SECRET"},
			Durable:  true, GenerationUsage: inference.Usage{InputTokens: 3, UncachedInputTokens: 3, OutputTokens: 2},
		}}},
	}
}

func requestFile(t *testing.T, root, name string, request delegation.Request) string {
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

type lockedBuffer struct {
	sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.buffer.Write(data)
}

func (b *lockedBuffer) String() string {
	b.Lock()
	defer b.Unlock()
	return b.buffer.String()
}

func connectWorker(t *testing.T, options Options, version string) (*mcp.ClientSession, *lockedBuffer, context.CancelFunc, <-chan error) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, serverConn, serverConn, options) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "worker-test", Version: "1"}, &mcp.ClientOptions{Logger: nil})
	log := &lockedBuffer{}
	session, err := client.Connect(t.Context(), &mcp.LoggingTransport{Transport: &mcp.IOTransport{Reader: clientConn, Writer: clientConn}, Writer: log}, &mcp.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		session.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("worker shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	})
	return session, log, cancel, done
}

func call(t *testing.T, session *mcp.ClientSession, arguments any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: ToolName, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func decodeOutput(t *testing.T, result *mcp.CallToolResult) toolOutput {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("content count = %d", len(result.Content))
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type = %T", result.Content[0])
	}
	var output toolOutput
	if err := json.Unmarshal([]byte(text.Text), &output); err != nil {
		t.Fatal(err)
	}
	structured, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var plain, structuredValue any
	if err := json.Unmarshal([]byte(text.Text), &plain); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(structured, &structuredValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plain, structuredValue) {
		t.Fatal("text and structured result differ")
	}
	return output
}

func TestSDKProtocolsDiscoverListCall(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25", "2025-06-18"} {
		t.Run(version, func(t *testing.T) {
			root := t.TempDir()
			absolute := requestFile(t, root, "requests/approved.json", approvedRequest())
			calls := 0
			session, log, _, _ := connectWorker(t, Options{Workspace: root, Version: "0.3.0", Execute: func(_ context.Context, request delegation.Request) (delegation.Result, error) {
				calls++
				if !reflect.DeepEqual(request, approvedRequest()) {
					t.Error("executor received a different request")
				}
				return completedResult(), nil
			}}, version)
			initialize := session.InitializeResult()
			if initialize.ProtocolVersion != version || initialize.ServerInfo.Name != "questlock" || initialize.ServerInfo.Version != "0.3.0" {
				t.Fatalf("unexpected server identity/version: %+v", initialize)
			}
			capabilities, _ := json.Marshal(initialize.Capabilities)
			if string(capabilities) != `{"tools":{}}` {
				t.Fatalf("unexpected capabilities %s", capabilities)
			}
			if version == "2026-07-28" && (!strings.Contains(log.String(), `"method":"server/discover"`) || strings.Contains(log.String(), `"method":"initialize"`)) {
				t.Fatal("current protocol did not use stateless discovery")
			}
			if version < "2026-07-28" && !strings.Contains(log.String(), `"method":"initialize"`) {
				t.Fatal("legacy protocol did not initialize")
			}
			listed, err := session.ListTools(t.Context(), nil)
			if err != nil || len(listed.Tools) != 2 || listed.Tools[0].Name != ToolName {
				t.Fatalf("unexpected tool list: %+v, %v", listed, err)
			}
			tool := listed.Tools[0]
			if tool.Annotations.ReadOnlyHint || tool.Annotations.IdempotentHint || tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
				t.Fatalf("inaccurate default annotations: %+v", tool.Annotations)
			}
			inputSchema := tool.InputSchema.(map[string]any)
			if inputSchema["additionalProperties"] != false || len(inputSchema["properties"].(map[string]any)) != 1 || tool.OutputSchema == nil {
				t.Fatalf("unexpected schema: %+v", tool)
			}
			for _, path := range []string{"requests/approved.json", absolute} {
				result := call(t, session, map[string]any{"request_path": path})
				output := decodeOutput(t, result)
				if result.IsError || output.Status != "completed" || output.Response == nil || output.Response.Text != "approved answer" || output.SelectedTier != "local" {
					t.Fatalf("unexpected result: %+v", output)
				}
				serialized, _ := json.Marshal(result)
				for _, secret := range []string{"FAILED_OUTPUT", "UPSTREAM_ID", "UPSTREAM_MODEL", "UPSTREAM_STOP"} {
					if strings.Contains(string(serialized), secret) {
						t.Fatal("result exposed an unselected response")
					}
				}
			}
			if calls != 2 {
				t.Fatalf("executor calls = %d", calls)
			}
		})
	}
}

func TestRequestArgumentsAreClosedAndErrorsControlled(t *testing.T) {
	root := t.TempDir()
	requestFile(t, root, "approved.json", approvedRequest())
	var calls atomic.Int32
	session, _, _, _ := connectWorker(t, Options{Workspace: root, Execute: func(context.Context, delegation.Request) (delegation.Result, error) {
		calls.Add(1)
		return completedResult(), nil
	}}, "")
	arguments := []any{
		map[string]any{}, map[string]any{"request_path": nil}, map[string]any{"request_path": 1}, "RAW_INPUT_SECRET",
		map[string]any{"request_path": "approved.json", "RAW_KEY_SECRET": "RAW_VALUE_SECRET"},
		json.RawMessage(`{"request_path":"approved.json","request_path":"RAW_PATH_SECRET"}`),
		map[string]any{"request_path": ""}, map[string]any{"request_path": strings.Repeat("x", 4097)},
		map[string]any{"request_path": "nul\x00secret"},
	}
	for _, input := range arguments {
		result := call(t, session, input)
		output := decodeOutput(t, result)
		if !result.IsError || output.ErrorCategory != "invalid_arguments" || output.Response != nil {
			t.Fatalf("invalid input accepted: %+v", output)
		}
		serialized, _ := json.Marshal(result)
		if strings.Contains(string(serialized), "RAW_") {
			t.Fatal("tool error echoed input")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid arguments executed")
	}
}

func TestFileConfinementAndBoundedStrictJSON(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	approved := requestFile(t, root, "nested/approved.json", approvedRequest())
	outsideFile := requestFile(t, outside, "OUTSIDE_PATH_SECRET.json", approvedRequest())
	if err := os.Symlink(approved, filepath.Join(root, "link.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nested", filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "huge.json"), bytes.Repeat([]byte(" "), MaxRequestBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string][]byte{
		"unknown.json":   []byte(`{"RAW_SECRET_KEY":"RAW_CONTENT_SECRET"}`),
		"duplicate.json": []byte(`{"spec":{},"spec":{}}`), "trailing.json": []byte(`{} {}`),
		"utf8.json": {0xff}, "malformed.json": []byte(`{"secret":`),
	} {
		if err := os.WriteFile(filepath.Join(root, name), value, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int32
	session, _, _, _ := connectWorker(t, Options{Workspace: root, Execute: func(context.Context, delegation.Request) (delegation.Result, error) {
		calls.Add(1)
		return completedResult(), nil
	}}, "")
	for _, path := range []string{outsideFile, "../OUTSIDE_PATH_SECRET.json", "nested/../nested/approved.json", "nested", ".", "link.json", "linkdir/approved.json", "huge.json", "missing.json", "unknown.json", "duplicate.json", "trailing.json", "utf8.json", "malformed.json"} {
		result := call(t, session, map[string]any{"request_path": path})
		output := decodeOutput(t, result)
		if !result.IsError || output.ErrorCategory != "invalid_request" {
			t.Fatalf("unsafe path/content accepted: category=%q", output.ErrorCategory)
		}
		serialized, _ := json.Marshal(result)
		if strings.Contains(string(serialized), "SECRET") || strings.Contains(string(serialized), root) || strings.Contains(string(serialized), outside) {
			t.Fatal("request error echoed private content/path")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request executed")
	}
}

func TestWorkspaceSymlinkSelectedByOperator(t *testing.T) {
	root, base := t.TempDir(), t.TempDir()
	requestFile(t, root, "approved.json", approvedRequest())
	selected := filepath.Join(base, "workspace")
	if err := os.Symlink(root, selected); err != nil {
		t.Fatal(err)
	}
	session, _, _, _ := connectWorker(t, Options{Workspace: selected, Execute: func(context.Context, delegation.Request) (delegation.Result, error) {
		return completedResult(), nil
	}}, "")
	if call(t, session, map[string]any{"request_path": "approved.json"}).IsError {
		t.Fatal("explicit operator workspace symlink failed")
	}
}

func TestPaidAuthorityRequiresProcessAndPolicy(t *testing.T) {
	for _, test := range []struct {
		name      string
		allowPaid bool
		budgets   bool
		wantError bool
	}{{"no-process-permission", false, true, true}, {"no-dollar-policy", true, false, true}, {"explicit-authority", true, true, false}} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			request := approvedRequest()
			tier := request.Policy.Local
			tier.ID, tier.Spec.Provider, tier.Spec.Model = "paid", "openai", "fixture-paid"
			if test.budgets {
				tier.Spec.Prices = &inference.Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
				tier.Spec.MaxCostUSD, tier.Spec.DailyBudgetUSD = 0.1, 1
			}
			request.Policy.Escalation = &tier
			requestFile(t, root, "paid.json", request)
			calls := 0
			session, _, _, _ := connectWorker(t, Options{Workspace: root, AllowPaid: test.allowPaid, Execute: func(context.Context, delegation.Request) (delegation.Result, error) {
				calls++
				return completedResult(), nil
			}}, "")
			listed, err := session.ListTools(t.Context(), nil)
			if err != nil || *listed.Tools[0].Annotations.OpenWorldHint != test.allowPaid {
				t.Fatal("paid annotation did not follow process authority")
			}
			result := call(t, session, map[string]any{"request_path": "paid.json"})
			output := decodeOutput(t, result)
			if result.IsError != test.wantError || (test.wantError && (calls != 0 || output.ErrorCategory != "invalid_policy")) || (!test.wantError && calls != 1) {
				t.Fatalf("authority result: error=%v category=%q calls=%d", result.IsError, output.ErrorCategory, calls)
			}
		})
	}
}

func TestFailedCallsNeverExposeResponseOrRawError(t *testing.T) {
	for _, test := range []struct {
		name, status, category string
		err                    error
	}{{"provider", "stopped", "stopped", errors.New("API_KEY_SECRET PATH_SECRET RAW_CONTENT_SECRET")}, {"quality", "quality_failed", "quality_failed", delegation.ErrQualityFailed}, {"deadline", "stopped", "deadline_exceeded", context.DeadlineExceeded}, {"cancel", "stopped", "canceled", context.Canceled}, {"missing-pass", "completed", "stopped", nil}} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			requestFile(t, root, "approved.json", approvedRequest())
			session, _, _, _ := connectWorker(t, Options{Workspace: root, Execute: func(context.Context, delegation.Request) (delegation.Result, error) {
				result := completedResult()
				result.Response.Text = "FAILED_CONTENT_SECRET"
				result.Status = test.status
				if test.name == "missing-pass" {
					result.Attempts[0].QualityPassed = false
				}
				return result, test.err
			}}, "")
			result := call(t, session, map[string]any{"request_path": "approved.json"})
			output := decodeOutput(t, result)
			if !result.IsError || output.ErrorCategory != test.category || output.Response != nil || output.SelectedTier != "" {
				t.Fatalf("unexpected failed result: %+v", output)
			}
			data, _ := json.Marshal(result)
			if strings.Contains(string(data), "SECRET") || strings.Contains(string(data), "FAILED_OUTPUT") {
				t.Fatal("failure exposed raw error or response")
			}
		})
	}
}

func TestBurstCallsFailFastWithoutExecutionOrRetry(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			root := t.TempDir()
			requestFile(t, root, "approved.json", approvedRequest())
			started, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			session, _, _, _ := connectWorker(t, Options{Workspace: root, Execute: func(ctx context.Context, _ delegation.Request) (delegation.Result, error) {
				if calls.Add(1) == 1 {
					close(started)
					select {
					case <-release:
					case <-ctx.Done():
						return delegation.Result{}, ctx.Err()
					}
				}
				return completedResult(), nil
			}}, version)
			first := make(chan error, 1)
			go func() {
				_, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolName, Arguments: map[string]any{"request_path": "approved.json"}})
				first <- err
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("first call did not start")
			}
			type outcome struct {
				result *mcp.CallToolResult
				err    error
			}
			const burst = 48
			excess := make(chan outcome, burst)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			for range burst {
				go func() {
					result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: ToolName, Arguments: map[string]any{"request_path": "approved.json"}})
					excess <- outcome{result, err}
				}()
			}
			for range burst {
				select {
				case result := <-excess:
					if result.err != nil {
						t.Fatalf("excess call waited instead of rejecting: %v", result.err)
					}
					output := decodeOutput(t, result.result)
					if !result.result.IsError || output.ErrorCategory != "worker_busy" || output.Status != "stopped" || output.Response != nil || len(output.Attempts) != 0 {
						t.Fatalf("unexpected busy result: %+v", output)
					}
				case <-ctx.Done():
					t.Fatal("excess calls accumulated while the accepted execution was blocked")
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("busy calls executed: %d executions", calls.Load())
			}
			if _, err := session.ListTools(ctx, nil); err != nil {
				t.Fatalf("discovery blocked behind execution: %v", err)
			}
			close(release)
			select {
			case err := <-first:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("first call did not complete")
			}
			if calls.Load() != 1 {
				t.Fatalf("busy calls retried after release: %d executions", calls.Load())
			}
		})
	}
}

func TestCanceledAdmissionHasPriorityOverBusy(t *testing.T) {
	for _, test := range []struct {
		name          string
		wire, process bool
	}{{"wire", true, false}, {"process", false, true}, {"both", true, true}} {
		t.Run(test.name, func(t *testing.T) {
			wire, cancelWire := context.WithCancel(t.Context())
			process, cancelProcess := context.WithCancel(t.Context())
			defer cancelWire()
			defer cancelProcess()
			if test.wire {
				cancelWire()
			}
			if test.process {
				cancelProcess()
			}
			w := worker{process: process, active: make(chan struct{}, 1), options: Options{Execute: func(context.Context, delegation.Request) (delegation.Result, error) {
				t.Fatal("canceled admission executed")
				return delegation.Result{}, nil
			}}}
			w.active <- struct{}{}
			result, err := w.call(wire, nil)
			if err != nil {
				t.Fatal(err)
			}
			output := decodeOutput(t, result)
			if !result.IsError || output.ErrorCategory != "canceled" || len(w.active) != 1 {
				t.Fatalf("canceled call was admitted or reported busy: %+v", output)
			}
		})
	}
}

func TestCanceledActiveCallReleasesAdmission(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			root := t.TempDir()
			requestFile(t, root, "approved.json", approvedRequest())
			later := approvedRequest()
			later.Spec.Revision = "revision-2"
			requestFile(t, root, "later.json", later)
			started := make(chan struct{})
			var calls atomic.Int32
			session, log, _, _ := connectWorker(t, Options{Workspace: root, Execute: func(ctx context.Context, _ delegation.Request) (delegation.Result, error) {
				if calls.Add(1) == 1 {
					close(started)
					<-ctx.Done()
					return delegation.Result{}, ctx.Err()
				}
				return completedResult(), nil
			}}, version)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			first := make(chan error, 1)
			go func() {
				_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: ToolName, Arguments: map[string]any{"request_path": "approved.json"}})
				first <- err
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("accepted call did not start")
			}
			busy := decodeOutput(t, call(t, session, map[string]any{"request_path": "later.json"}))
			if busy.ErrorCategory != "worker_busy" {
				t.Fatalf("expected busy while active: %+v", busy)
			}
			cancel()
			select {
			case err := <-first:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("call cancellation: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("client did not cancel")
			}
			// The SDK sends cancellation asynchronously. The server result is
			// observed after the handler has returned and released admission.
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			wait, stop := context.WithTimeout(t.Context(), 3*time.Second)
			defer stop()
			for !strings.Contains(log.String(), `"error_category":"canceled"`) {
				select {
				case <-ticker.C:
				case <-wait.Done():
					t.Fatal("server did not finish cancellation")
				}
			}
			result := call(t, session, map[string]any{"request_path": "later.json"})
			if result.IsError || decodeOutput(t, result).Status != "completed" || calls.Load() != 2 {
				t.Fatal("new explicit call was not admitted after cancellation")
			}
		})
	}
}

func TestWireAndProcessCancellationStopActiveExecution(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, source := range []string{"wire", "process"} {
			t.Run(version+"/"+source, func(t *testing.T) {
				root := t.TempDir()
				requestFile(t, root, "approved.json", approvedRequest())
				started, stopped := make(chan struct{}), make(chan struct{})
				var calls atomic.Int32
				session, _, stopProcess, _ := connectWorker(t, Options{Workspace: root, Execute: func(ctx context.Context, _ delegation.Request) (delegation.Result, error) {
					calls.Add(1)
					close(started)
					<-ctx.Done()
					close(stopped)
					return delegation.Result{}, ctx.Err()
				}}, version)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: ToolName, Arguments: map[string]any{"request_path": "approved.json"}})
					done <- err
				}()
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					t.Fatal("executor did not start")
				}
				if source == "wire" {
					cancel()
				} else {
					stopProcess()
				}
				select {
				case <-stopped:
				case <-time.After(3 * time.Second):
					t.Fatal("execution did not receive cancellation")
				}
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("client call did not terminate")
				}
				if calls.Load() != 1 {
					t.Fatal("canceled execution was regenerated")
				}
			})
		}
	}
}

func TestProtocolErrorsDoNotEchoParams(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	defer clientConn.Close()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, serverConn, serverConn, Options{Workspace: t.TempDir(), Execute: func(context.Context, delegation.Request) (delegation.Result, error) {
			t.Error("unexpected execution")
			return delegation.Result{}, nil
		}})
	}()
	scanner := bufio.NewScanner(clientConn)
	meta := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`
	for _, params := range []string{`"name":{"RAW_KEY_SECRET":"RAW_VALUE_SECRET"},"arguments":{"request_path":"RAW_PATH_SECRET"}`, `"name":"RAW_TOOL_SECRET","arguments":{}`} {
		frame := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{%s,%s}}`+"\n", params, meta)
		if _, err := io.WriteString(clientConn, frame); err != nil {
			t.Fatal(err)
		}
		if !scanner.Scan() {
			t.Fatalf("missing protocol response: %v", scanner.Err())
		}
		var response struct {
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil || response.Error == nil || bytes.Contains(scanner.Bytes(), []byte("RAW_")) {
			t.Fatalf("unsafe protocol error: %s", scanner.Bytes())
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("raw worker did not stop")
	}
}

func TestRealStdioProcess(t *testing.T) {
	if os.Getenv("QUESTLOCK_WORKER_TEST_CHILD") == "1" {
		err := Serve(context.Background(), os.Stdin, os.Stdout, Options{Workspace: os.Getenv("QUESTLOCK_WORKER_TEST_ROOT"), Version: "fixture", Execute: func(context.Context, delegation.Request) (delegation.Result, error) { return completedResult(), nil }})
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	root := t.TempDir()
	requestFile(t, root, "approved.json", approvedRequest())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRealStdioProcess$")
	command.Env = append(os.Environ(), "QUESTLOCK_WORKER_TEST_CHILD=1", "QUESTLOCK_WORKER_TEST_ROOT="+root)
	var stderr lockedBuffer
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "process-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := session.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 2 {
		t.Fatalf("stdio tools: %+v, %v", listed, err)
	}
	result := call(t, session, map[string]any{"request_path": "approved.json"})
	if result.IsError || decodeOutput(t, result).Response.Text != "approved answer" {
		t.Fatal("stdio call failed")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if stderr.String() != "" {
		t.Fatalf("worker wrote stderr: %q", stderr.String())
	}
}
