// Package outcomes records human acceptance separately from deterministic
// inference checks. Records contain hashes and accounting metadata, never
// prompts, generated answers, reviewer names, or free-form feedback.
package outcomes

import (
	"errors"
	"time"

	"github.com/spfuzzylink/questlock/inference"
)

const (
	MaxRuns        = 256
	MaxEvents      = 256
	MaxCohortTasks = 256
)

var (
	ErrInvalid    = errors.New("invalid outcome record")
	ErrConflict   = errors.New("outcome version conflict")
	ErrIDConflict = errors.New("outcome ID already has a different payload")
	ErrLimit      = errors.New("outcome ledger limit reached")
)

// Task revisions are independent cohorts. Changing a revision never carries
// human acceptance from the earlier task artifact.
type Task struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

// Attempt describes only the current invocation's usage and charge. A reused
// result has zero new usage/charge; its original cost is never added again.
// EstimatedCostUSD is a caller estimate, not a verified provider invoice.
type Attempt struct {
	TierID           string          `json:"tier_id,omitempty"`
	Fingerprint      string          `json:"fingerprint,omitempty"`
	OutputSHA256     string          `json:"output_sha256,omitempty"`
	CheckPassed      bool            `json:"check_passed"` // Observed checks, independently of run admission/durability.
	Complete         bool            `json:"complete"`
	Durable          bool            `json:"durable"`
	Reused           bool            `json:"reused"`
	GenerationCalled bool            `json:"generation_called"`
	UsageKnown       bool            `json:"usage_known"`
	Usage            inference.Usage `json:"usage"`
	EstimatedCostUSD *float64        `json:"estimated_cost_usd"`
	CostBasis        string          `json:"cost_basis"`
}

type Run struct {
	ID            string    `json:"id"`
	RequestDigest string    `json:"request_digest"`
	Status        string    `json:"status"` // completed, quality_failed, stopped
	Attempts      []Attempt `json:"attempts"`
}

// Feedback is explicitly human-asserted. A final accepted artifact can differ
// from the generated output after human correction. HumanMinutes is incremental
// effort for this event: nil means unknown, zero means explicitly reported zero.
type Feedback struct {
	ID                  string   `json:"id"`
	RunID               string   `json:"run_id"`
	Kind                string   `json:"kind"`             // accepted, rewritten, rejected, reopened
	Reason              string   `json:"reason,omitempty"` // rewrite: correctness, style, requirements, other
	FinalArtifactSHA256 string   `json:"final_artifact_sha256,omitempty"`
	HumanMinutes        *float64 `json:"human_minutes"`
}

type Receipt struct {
	Task      Task      `json:"task"`
	Key       string    `json:"key"`
	ID        string    `json:"id"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
}

type RecordedRun struct {
	Run
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
}

type RecordedFeedback struct {
	Feedback
	ExpectedVersion int64     `json:"expected_version"`
	Version         int64     `json:"version"`
	CreatedAt       time.Time `json:"created_at"`
}

type State struct {
	Status              string `json:"status"` // unreviewed, accepted, rewritten, rejected, reopened
	AcceptedRunID       string `json:"accepted_run_id,omitempty"`
	FinalArtifactSHA256 string `json:"final_artifact_sha256,omitempty"`
}

type Snapshot struct {
	Task      Task               `json:"task"`
	Key       string             `json:"key"`
	Version   int64              `json:"version"`
	CreatedAt time.Time          `json:"created_at"`
	Runs      []RecordedRun      `json:"runs"`
	Events    []RecordedFeedback `json:"events"`
	State     State              `json:"state"`
}

type Store struct {
	Broker inference.Broker
}

type TaskSummary struct {
	Task                      Task    `json:"task"`
	Version                   int64   `json:"version"`
	State                     State   `json:"state"`
	RunCount                  int     `json:"run_count"`
	AttemptCount              int     `json:"attempt_count"`
	KnownInferenceSubtotalUSD float64 `json:"known_inference_subtotal_usd"`
	MissingInferenceCosts     int     `json:"missing_inference_costs"`
	MissingUsage              int     `json:"missing_usage"`
	RecordedHumanMinutes      float64 `json:"recorded_human_minutes"`
	MissingHumanEffort        int     `json:"missing_human_effort"`
}

// Report covers only the explicit cohort's recorded attempts and feedback.
// Nullable metrics remain unknown when a contributing value is unknown; no
// external work, hardware cost, or surrounding assistant usage is inferred.
// Cohort reads are individually versioned, not one atomic multi-task snapshot.
type Report struct {
	Tasks                           []TaskSummary `json:"tasks"`
	TaskCount                       int           `json:"task_count"`
	AcceptedTasks                   int           `json:"accepted_tasks"`
	RunCount                        int           `json:"run_count"`
	AttemptCount                    int           `json:"attempt_count"`
	KnownInferenceSubtotalUSD       float64       `json:"known_inference_subtotal_usd"`
	MissingInferenceCosts           int           `json:"missing_inference_costs"`
	MissingUsage                    int           `json:"missing_usage"`
	RecordedHumanMinutes            float64       `json:"recorded_human_minutes"`
	MissingHumanEffort              int           `json:"missing_human_effort"`
	HourlyRateUSD                   *float64      `json:"hourly_rate_usd"`
	InferenceCostUSD                *float64      `json:"inference_cost_usd"`
	HumanCostUSD                    *float64      `json:"human_cost_usd"`
	TotalCostUSD                    *float64      `json:"total_cost_usd"`
	CostPerAcceptedTaskUSD          *float64      `json:"cost_per_accepted_task_usd"`
	InferenceCostPerAcceptedTaskUSD *float64      `json:"inference_cost_per_accepted_task_usd"`
	GeneratedAt                     time.Time     `json:"generated_at"`
	Basis                           string        `json:"basis"`
	Coverage                        string        `json:"coverage"`
}
