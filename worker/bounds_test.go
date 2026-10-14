package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spfuzzylink/questlock/delegation"
)

func TestFrameBoundReturnsControlledTransportFailure(t *testing.T) {
	input := io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("X"), MaxRequestBytes+1)))
	var output bytes.Buffer
	err := Serve(context.Background(), input, &output, Options{Workspace: t.TempDir(), Execute: func(context.Context, delegation.Request) (delegation.Result, error) {
		t.Fatal("oversized frame executed")
		return delegation.Result{}, nil
	}})
	if !errors.Is(err, ErrTransport) || err.Error() != "worker_transport_failed" || output.Len() != 0 {
		t.Fatalf("unsafe oversized-frame outcome: error=%v output length=%d", err, output.Len())
	}
}

func TestProtocolErrorLabelsAndNegotiation(t *testing.T) {
	for code, label := range map[int64]string{
		jsonrpc.CodeParseError: "parse_error", jsonrpc.CodeInvalidRequest: "invalid_request", jsonrpc.CodeMethodNotFound: "method_not_found", jsonrpc.CodeInvalidParams: "invalid_params", jsonrpc.CodeInternalError: "internal_error", 123: "internal_error",
	} {
		safe := safeProtocolError(&jsonrpc.Error{Code: code, Message: "RAW_ERROR_SECRET", Data: json.RawMessage(`{"RAW_KEY_SECRET":"RAW_VALUE_SECRET"}`)})
		if safe.Message != label || len(safe.Data) != 0 {
			t.Fatal("unsafe error label or data")
		}
	}
	safe := safeProtocolError(&jsonrpc.Error{Code: mcp.CodeUnsupportedProtocolVersion, Message: "RAW_ERROR_SECRET", Data: json.RawMessage(`{"requested":"RAW_VERSION_SECRET"}`)})
	var data mcp.UnsupportedProtocolVersionData
	if err := json.Unmarshal(safe.Data, &data); err != nil || data.Requested != "" || len(data.Supported) == 0 || safe.Message != "unsupported_protocol_version" {
		t.Fatal("safe version renegotiation missing")
	}
}
