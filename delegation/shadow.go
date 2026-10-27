package delegation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/semantic"
)

// ShadowObserver is optional, local-only opportunity measurement. One observer
// admits one bounded observation at a time and retains no waiting task history.
// Failures never change a generated answer, its accounting, or its claims.
type ShadowObserver struct {
	Broker          inference.Broker
	Index           semantic.Index
	EmbedderFactory func(context.Context, semantic.Config) (semantic.Embedder, error)
	Resources       admission.Source // nil lets a Go caller provide its own admission.
	once            sync.Once
	gate            chan struct{}
}

// ShadowPartition binds every approved non-question part of the task. Bindings
// must declare business variables embedded in the question; a partition alone
// is not a proof that two free-form questions have equivalent answers.
func ShadowPartition(request Request, tier Tier, runtimeID string) (string, error) {
	if request.SemanticShadow == nil || len(runtimeID) != 64 {
		return "", errors.New("shadow contract requires explicit policy and runtime identity")
	}
	c, err := semantic.NormalizeConfig(*request.SemanticShadow)
	if err != nil {
		return "", err
	}
	spec := tier.Spec
	spec.Task, spec.Revision = request.Spec.Task, request.Spec.Revision
	spec.Instructions, spec.Context = request.Spec.Instructions, request.Spec.Context
	spec.Input = ""
	encoded, err := json.Marshal(struct {
		Schema                           int
		Family, Domain, EvidenceRevision string
		Bindings                         map[string]string
		Spec                             inference.Spec
		Checks                           []evaluation.Check
		Runtime                          string
	}{1, c.Family, c.Domain, c.EvidenceRevision, c.Bindings, spec, request.Checks, runtimeID})
	if err != nil {
		return "", errors.New("invalid shadow contract")
	}
	h := sha256.Sum256(encoded)
	return hex.EncodeToString(h[:]), nil
}

func (o *ShadowObserver) observe(ctx context.Context, request Request, tier Tier, runner Runner, answer inference.Result) *semantic.Observation {
	started := time.Now()
	out := &semantic.Observation{Mode: "shadow", Status: "unavailable"}
	defer func() { out.TotalMS = float64(time.Since(started)) / float64(time.Millisecond) }()
	if answer.Reused {
		out.Status = "skipped_exact"
		return out
	}
	if o == nil || o.Broker == nil || o.Index == nil || o.EmbedderFactory == nil {
		return out
	}
	c, err := semantic.NormalizeConfig(*request.SemanticShadow)
	if err != nil {
		out.Status = "invalid_policy"
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutMS)*time.Millisecond)
	defer cancel()
	o.once.Do(func() { o.gate = make(chan struct{}, 1) })
	select {
	case o.gate <- struct{}{}:
		defer func() { <-o.gate }()
	default:
		out.Status = "busy"
		return out
	}
	if o.Resources != nil {
		s, err := o.Resources.Sample(ctx)
		if err != nil || !s.Known || s.At.IsZero() || time.Since(s.At) > 5*time.Second || s.AvailableMemoryBytes < 256*admission.MiB || s.MemoryPressure != admission.Normal || s.CPUStallMilliPercent >= 20000 || (s.LoadCapacityMilli > 0 && s.LoadMilli > 2*s.LoadCapacityMilli) {
			out.Status = "resource_pressure"
			return out
		}
	}
	runtime, ok := runner.(interface{ RuntimeIdentity() string })
	if !ok || runtime.RuntimeIdentity() == "" {
		out.Status = "runtime_identity_unavailable"
		return out
	}
	partition, err := ShadowPartition(request, tier, runtime.RuntimeIdentity())
	if err != nil {
		out.Status = "invalid_contract"
		return out
	}
	out.Partition = partition
	embedder, err := o.EmbedderFactory(ctx, c)
	if err != nil || embedder == nil {
		out.Status = "embedding_unavailable"
		return out
	}
	if closer, ok := embedder.(interface{ Close() }); ok {
		defer closer.Close()
	}
	embedding, err := embedder.Embed(ctx, request.Spec.Input)
	// Keep known local embedding work even when a later search/write fails.
	out.EmbeddingInputTokens, out.EmbeddingMS = embedding.InputTokens, embedding.DurationMS
	out.EmbeddingModelID = embedding.ModelID
	if err != nil {
		out.Status = "embedding_failed"
		return out
	}
	search, err := o.Index.QuerySemantic(ctx, semantic.Query{Partition: partition, ModelID: embedding.ModelID, Vector: embedding.Vector, MinSimilarity: c.MinSimilarity, TopK: c.TopK, MaxScan: c.MaxScan})
	if err != nil {
		out.Status = "search_failed"
		return out
	}
	valid := search.Candidates[:0]
	for _, candidate := range search.Candidates {
		a, err := o.Broker.Get(ctx, candidate.Source.Key)
		if err != nil {
			out.Status = "source_unavailable"
			return out
		}
		h := sha256.Sum256([]byte(a.Content))
		if a.Version != candidate.Source.Version || hex.EncodeToString(h[:]) != candidate.Source.SHA256 {
			continue
		}
		var saved struct {
			State     string              `json:"state"`
			ExpiresAt time.Time           `json:"expires_at"`
			Response  *inference.Response `json:"response"`
		}
		if json.Unmarshal([]byte(a.Content), &saved) != nil || saved.State != "complete" || saved.Response == nil || !saved.Response.Complete || !time.Now().Before(saved.ExpiresAt) {
			continue
		}
		if _, passed := evaluation.CheckOutput(saved.Response.Text, request.Checks); passed {
			valid = append(valid, candidate)
		}
	}
	search.Candidates = valid
	out.Search = &search
	a, err := o.Broker.Get(ctx, "inference/v1/jobs/"+answer.Fingerprint)
	if err != nil {
		out.Status = "source_unavailable"
		return out
	}
	h := sha256.Sum256([]byte(a.Content))
	output := sha256.Sum256([]byte(answer.Response.Text))
	entry := semantic.Entry{Partition: partition, ModelID: embedding.ModelID, Vector: embedding.Vector, Source: semantic.Source{Key: a.Key, Version: a.Version, SHA256: hex.EncodeToString(h[:]), OutputSHA256: hex.EncodeToString(output[:])}, CreatedAt: answer.CreatedAt, ExpiresAt: answer.ExpiresAt}
	entry.ID = semantic.EntryID(entry)
	if err := o.Index.PutSemantic(ctx, semantic.PutRequest{Entry: entry, MaxEntries: c.MaxEntries}); err != nil {
		out.Status = "index_failed"
		return out
	}
	out.Indexed = true
	out.Status = "miss"
	if len(valid) > 0 {
		out.Status = "candidates"
	}
	return out
}
