package worker

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/semantic"
)

func TestWorkerAcceptsPairAssessmentWithoutEnablingNativeThinking(t *testing.T) {
	root := t.TempDir()
	request := approvedRequest()
	request.SemanticReuse = &semantic.ReuseConfig{Config: semantic.Config{Mode: "controlled", Family: "worker-assessment-test", Domain: "approved", EvidenceRevision: "revision-1", EmbeddingModel: "all-minilm", MinSimilarity: .9}, ReadOnly: true, Intent: "approved-read-only", VerifierModel: "fixture:local", VerifierPolicy: "factorized-v1", VerifierPairAssessment: true}
	requestFile(t, root, "assessment.json", request)
	calls := 0
	session, _, _, _ := connectWorker(t, Options{Workspace: root, Execute: func(_ context.Context, r delegation.Request) (delegation.Result, error) {
		calls++
		if r.SemanticReuse == nil || !r.SemanticReuse.VerifierPairAssessment || r.SemanticReuse.VerifierPairThink || r.SemanticReuse.VerifierSourceThink {
			t.Error("MCP ignored assessment or enabled native thinking")
		}
		return completedResult(), nil
	}}, "")
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolName, Arguments: map[string]any{"request_path": "assessment.json"}})
	if err != nil || result.IsError || calls != 1 {
		t.Fatalf("explicit assessment request rejected: %+v %v calls=%d", result, err, calls)
	}
}
