package worker

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/revision"
)

const StatusToolName = "questlock_status"

// Status is the connected worker's operating contract, not a broker, provider,
// or native-client health check. Schema versions describe the accepted JSON:
// delegation currently has no schema field; revision requires schema: 1.
type Status struct {
	Schema          int               `json:"schema"`
	RuntimeVersion  string            `json:"runtime_version"`
	RequestRoot     string            `json:"request_root"`
	Profile         StatusProfile     `json:"profile"`
	Policy          StatusPolicy      `json:"policy"`
	RequestSchemas  StatusSchemas     `json:"request_schemas"`
	Checks          StatusChecks      `json:"checks"`
	Tools           []string          `json:"tools"`
	WorkflowConfigs map[string]string `json:"workflow_configs"`
	Readiness       StatusReadiness   `json:"readiness"`
	ReadOnly        bool              `json:"read_only"`
	ModelCalls      int               `json:"model_calls"`
}

type StatusProfile struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

type StatusPolicy struct {
	AllowPaid                          bool `json:"allow_paid"`
	PaidRequiresExplicitRequestBudgets bool `json:"paid_requires_explicit_request_budgets"`
}

type StatusSchemas struct {
	Delegation          []string `json:"delegation"`
	Revision            []int    `json:"revision"`
	SemanticShadowModes []string `json:"semantic_shadow_modes,omitempty"`
}

type StatusChecks struct {
	Schema int      `json:"schema"`
	Kinds  []string `json:"kinds"`
}

type StatusReadiness struct {
	Worker      string `json:"worker"`
	RequestRoot string `json:"request_root"`
	Broker      string `json:"broker"`
	Providers   string `json:"providers"`
}

func validProfile(id, kind string) bool {
	if kind != "" && kind != "custom" && kind != "managed" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' {
			continue
		}
		return false
	}
	return true
}

func (w *worker) addStatusTool(server *mcp.Server) {
	no := false
	server.AddTool(&mcp.Tool{
		Name: StatusToolName, Title: "Questlock worker operating contract",
		Description:  "Discover this connected worker's runtime version, declared absolute request root, supported request/check schemas, operator profile label, paid policy and bounded worker readiness. Takes no inputs and remains available while a task is busy. Reports broker and providers as not_checked: it performs no network or model calls, reads no request/content/configuration/credentials/environment, lists no files and never starts, reconfigures or writes anything. The root was opened at worker startup; readiness does not verify broker, provider or native-client connectivity.",
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
		OutputSchema: json.RawMessage(`{"type":"object","required":["schema","runtime_version","request_root","profile","policy","request_schemas","checks","tools","workflow_configs","readiness","read_only","model_calls"],"properties":{"schema":{"type":"integer","const":1},"runtime_version":{"type":"string"},"request_root":{"type":"string"},"profile":{"type":"object"},"policy":{"type":"object"},"request_schemas":{"type":"object"},"checks":{"type":"object"},"tools":{"type":"array","items":{"type":"string"}},"workflow_configs":{"type":"object"},"readiness":{"type":"object"},"read_only":{"type":"boolean","const":true},"model_calls":{"type":"integer","const":0}}}`),
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &no, IdempotentHint: true, OpenWorldHint: &no},
	}, w.callStatus)
}

func (w *worker) callStatus(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// An empty object is the complete authority. Do not echo unexpected input.
	if req == nil || req.Params == nil || !emptyStatusArguments(req.Params.Arguments) {
		return errorResult("invalid_arguments"), nil
	}
	if err := ctx.Err(); err != nil {
		return errorResult(contextCategory(err)), nil
	}
	if err := w.process.Err(); err != nil {
		return errorResult(contextCategory(err)), nil
	}
	profile := StatusProfile{ID: w.options.ProfileID, Kind: w.options.ProfileKind}
	if profile.Kind == "" {
		profile.Kind = "custom"
	}
	if profile.ID == "" {
		profile.ID = profile.Kind
	}
	version := w.options.Version
	if version == "" {
		version = "dev"
	}
	status := Status{
		Schema: 1, RuntimeVersion: version, RequestRoot: w.declaredWorkspace,
		Profile: profile, Policy: StatusPolicy{AllowPaid: w.options.AllowPaid, PaidRequiresExplicitRequestBudgets: true},
		RequestSchemas: StatusSchemas{Delegation: []string{"unversioned"}, Revision: []int{}, SemanticShadowModes: []string{"shadow"}},
		Checks:         StatusChecks{Schema: evaluation.Schema, Kinds: evaluation.SupportedCheckKinds()},
		Tools:          []string{ToolName}, WorkflowConfigs: map[string]string{"writing": "writing-workflow.json"},
		Readiness: StatusReadiness{Worker: "ready", RequestRoot: "opened", Broker: "not_checked", Providers: "not_checked"},
		ReadOnly:  true, ModelCalls: 0,
	}
	if w.options.Revise != nil {
		status.RequestSchemas.Revision = []int{revision.Schema}
		status.Tools = append(status.Tools, ReviseToolName)
	}
	status.Tools = append(status.Tools, StatusToolName)
	// Inspect only the bounded in-memory admission channel. Discovery never
	// competes for the execution slot and never touches an external service.
	if len(w.active) != 0 {
		status.Readiness.Worker = "busy"
	}
	encoded, _ := json.Marshal(status)
	return &mcp.CallToolResult{StructuredContent: status, Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil
}

func emptyStatusArguments(data json.RawMessage) bool {
	data = bytes.TrimSpace(data)
	return len(data) == 0 || len(data) >= 2 && len(data) <= 128 && data[0] == '{' && data[len(data)-1] == '}' && len(bytes.TrimSpace(data[1:len(data)-1])) == 0
}
