package inference

import "time"

// Measurement describes this invocation, never the cached answer's original
// latency. It is returned to the caller and is not stored in the job, included
// in its fingerprint, or sent anywhere automatically. Complete and durable
// describe protocol state, not answer quality.
type Measurement struct {
	GenerationTiming       *GenerationTiming  `json:"generation_timing,omitempty"`
	StartedAt              time.Time          `json:"started_at"`
	EndedAt                time.Time          `json:"ended_at"`
	TotalMS                float64            `json:"total_ms"`
	PhaseMS                map[string]float64 `json:"phase_ms"`
	Outcome                string             `json:"outcome"`
	Fingerprint            string             `json:"fingerprint,omitempty"`
	ContextBytes           int                `json:"context_bytes"`
	TotalContextBytes      int                `json:"total_context_bytes"`
	SelectedContext        int                `json:"selected_context"`
	OmittedContext         int                `json:"omitted_context"`
	GenerationCalled       bool               `json:"generation_called"`
	KnownGenerationUsage   bool               `json:"known_generation_usage"`
	GenerationUsage        *Usage             `json:"generation_usage,omitempty"`
	GenerationStartedAt    time.Time          `json:"generation_started_at,omitzero"`
	GenerationEndedAt      time.Time          `json:"generation_ended_at,omitzero"`
	ProviderSetupMS        float64            `json:"provider_setup_ms,omitempty"`
	ProviderSetupStartedAt time.Time          `json:"provider_setup_started_at,omitzero"`
	ProviderSetupEndedAt   time.Time          `json:"provider_setup_ended_at,omitzero"`
}

// GenerationTiming describes streamed local content chunks observed by the
// client. FirstTokenMS includes metadata/network time from Generate entry.
// OutputMS is the first-to-last content-chunk interval, not pure compute time.
// Missing measurements are unknown; a one-chunk interval can validly be zero.
type GenerationTiming struct {
	Streaming    bool     `json:"streaming"`
	FirstTokenMS *float64 `json:"first_content_ms,omitempty"`
	OutputMS     *float64 `json:"output_content_ms,omitempty"`
	Basis        string   `json:"basis"`
}

func milliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
