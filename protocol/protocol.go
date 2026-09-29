// Package protocol defines the JSON contract between Questlock and its clients.
package protocol

const MaxContentBytes = 1 << 20

type PublishRequest struct {
	Key             string `json:"key"`
	OperationID     string `json:"operation_id"`
	ExpectedVersion int64  `json:"expected_version"`
	Content         string `json:"content"`
}

type Artifact struct {
	Scope     string `json:"scope"`
	Key       string `json:"key"`
	Version   int64  `json:"version"`
	Content   string `json:"content"`
	UpdatedAt string `json:"updated_at"`
}

type PublishResult struct {
	Artifact Artifact `json:"artifact"`
	Replayed bool     `json:"replayed"`
}

type AuditEvent struct {
	ID              int64  `json:"id"`
	AgentID         string `json:"agent_id"`
	Scope           string `json:"scope"`
	Key             string `json:"key"`
	OperationID     string `json:"operation_id"`
	Outcome         string `json:"outcome"`
	ExpectedVersion int64  `json:"expected_version"`
	CurrentVersion  int64  `json:"current_version"`
	CreatedAt       string `json:"created_at"`
}

type ErrorBody struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	CurrentVersion int64  `json:"current_version,omitempty"`
}
