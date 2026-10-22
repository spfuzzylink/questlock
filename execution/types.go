// Package execution composes bounded, process-local admission with the existing
// shared inference runner. Providers, broker credentials and optional telemetry
// are explicit dependencies; this package discovers no environment settings and
// never retries generation or selects another provider.
package execution

import (
	"errors"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/observability"
)

const MaxSpecBytes = 1 << 20

var (
	ErrConfiguration  = errors.New("execution_configuration_invalid")
	ErrClosed         = errors.New("execution_coordinator_closed")
	ErrHostedDisabled = errors.New("execution_hosted_provider_disabled")
)

// Config holds one process's coordinator. A zero Admission uses the existing
// admission defaults, including one inference worker. Source defaults to the
// native system probe. Custom providers/sources/exporters must be concurrency
// safe and honor contexts. Per-lane reservations remain estimates, not OS limits.
type Config struct {
	Runner    inference.Runner
	Admission admission.Config
	Source    admission.Source
	Exporter  observability.Exporter
	// ExportTimeout is additional to the runner's task deadline, so cancelled
	// generation can still export its outcome. Zero => 2s; explicit 1ms..5s.
	ExportTimeout time.Duration
	// AllowHosted is false by default. When false, both the spec and the provider's
	// optional inference.SelfHostedProvider assertion must declare self-hosting.
	// Assertions are trusted caller configuration, not proof of a zero invoice.
	AllowHosted bool
}

// TelemetryResult is independent of task success. Error is never serialized;
// status and span IDs can be recorded without including backend error text.
// Export failure never changes Run's error or authorizes another generation.
type TelemetryResult struct {
	Status  string                 `json:"status"` // disabled, not_attempted, acknowledged, unconfirmed, invalid_metadata
	Receipt *observability.Receipt `json:"receipt,omitempty"`
	Error   error                  `json:"-"`
}

// Result preserves the runner's result, including a nondurable partial answer
// on error. Complete and Durable are protocol evidence, not quality checks or
// human acceptance. Inspect Run's error before using any returned answer.
type Result struct {
	inference.Result
	Telemetry TelemetryResult `json:"telemetry"`
}
