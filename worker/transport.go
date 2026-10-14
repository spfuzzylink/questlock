package worker

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The SDK owns protocol framing and lifecycle. Only replace diagnostic errors:
// SDK errors may echo invalid params, unknown tool names, or client metadata.
type safeTransport struct{ mcp.Transport }

func (t *safeTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	connection, err := t.Transport.Connect(ctx)
	if err != nil {
		return nil, ErrTransport
	}
	return safeConnection{connection}, nil
}

type safeConnection struct{ mcp.Connection }

func (c safeConnection) Write(ctx context.Context, message jsonrpc.Message) error {
	if response, ok := message.(*jsonrpc.Response); ok && response.Error != nil {
		copy := *response
		copy.Error = safeProtocolError(response.Error)
		message = &copy
	}
	return c.Connection.Write(ctx, message)
}

func safeProtocolError(err error) *jsonrpc.Error {
	code, label := int64(jsonrpc.CodeInternalError), "internal_error"
	var original *jsonrpc.Error
	if errors.As(err, &original) {
		switch original.Code {
		case jsonrpc.CodeParseError:
			code, label = original.Code, "parse_error"
		case jsonrpc.CodeInvalidRequest:
			code, label = original.Code, "invalid_request"
		case jsonrpc.CodeMethodNotFound:
			code, label = original.Code, "method_not_found"
		case jsonrpc.CodeInvalidParams:
			code, label = original.Code, "invalid_params"
		case mcp.CodeUnsupportedProtocolVersion:
			// Preserve version negotiation, without echoing the requested value.
			data, _ := json.Marshal(map[string]any{"supported": mcp.SupportedProtocolVersions()})
			return &jsonrpc.Error{Code: original.Code, Message: "unsupported_protocol_version", Data: data}
		}
	}
	return &jsonrpc.Error{Code: code, Message: label}
}
