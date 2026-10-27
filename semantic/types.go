// Package semantic observes possible completed-answer reuse. Shadow decisions
// are advisory: they never serve text, change claims, or authorize generation.
package semantic

import (
	"context"
	"time"
)

// Config is opt-in. Mode accepts only "shadow" in this release.
type Config struct {
	Mode string `json:"mode"`
	Family string `json:"family"`
	Domain string `json:"domain"`
	EvidenceRevision string `json:"evidence_revision"`
	Bindings map[string]string `json:"bindings,omitempty"`
	EmbeddingModel string `json:"embedding_model"`
	MinSimilarity float64 `json:"min_similarity,omitempty"`
	TopK int `json:"top_k,omitempty"`
	MaxScan int `json:"max_scan,omitempty"`
	MaxEntries int `json:"max_entries,omitempty"`
	MaxInputBytes int `json:"max_input_bytes,omitempty"`
	TimeoutMS int `json:"timeout_ms,omitempty"`
}

type Source struct {
	Key string `json:"key"`
	Version int64 `json:"version"`
	SHA256 string `json:"sha256"`
	OutputSHA256 string `json:"output_sha256"`
}

type Entry struct {
	ID string `json:"id"`
	Partition string `json:"partition"`
	ModelID string `json:"model_id"`
	Vector []float32 `json:"vector"`
	Source Source `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Query struct {
	Partition string `json:"partition"`
	ModelID string `json:"model_id"`
	Vector []float32 `json:"vector"`
	MinSimilarity float64 `json:"min_similarity"`
	TopK int `json:"top_k"`
	MaxScan int `json:"max_scan"`
}

type Candidate struct {
	Source Source `json:"source"`
	Similarity float64 `json:"similarity"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type SearchResult struct {
	Candidates []Candidate `json:"candidates"`
	Scanned int `json:"scanned"`
	Truncated bool `json:"truncated"`
}

type PutRequest struct {
	Entry Entry `json:"entry"`
	MaxEntries int `json:"max_entries"`
}

type Index interface {
	QuerySemantic(context.Context, Query) (SearchResult, error)
	PutSemantic(context.Context, PutRequest) error
}

type Embedding struct {
	ModelID string `json:"model_id"`
	Vector []float32 `json:"-"`
	InputTokens *int64 `json:"input_tokens"`
	DurationMS float64 `json:"duration_ms"`
}

type Embedder interface {
	Embed(context.Context, string) (Embedding, error)
}

// Observation deliberately has no reused/avoided tokens or alternative answer.
type Observation struct {
	Mode string `json:"mode"`
	Status string `json:"status"`
	Partition string `json:"partition,omitempty"`
	EmbeddingModelID string `json:"embedding_model_id,omitempty"`
	EmbeddingInputTokens *int64 `json:"embedding_input_tokens"`
	EmbeddingMS float64 `json:"embedding_ms,omitempty"`
	TotalMS float64 `json:"total_ms"`
	Search *SearchResult `json:"search,omitempty"`
	Indexed bool `json:"indexed"`
	GenerationAvoided bool `json:"generation_avoided"`
}
