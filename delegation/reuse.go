package delegation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/semantic"
)

// ControlledReuser serves only explicitly enabled, checked text answers. It
// never performs or replays an external action, cancels another worker, or
// clears a pending/uncertain claim. Instantiate one per bounded worker.
type ControlledReuser struct {
	Broker          inference.Broker
	Index           semantic.Index
	EmbedderFactory func(context.Context, semantic.Config) (semantic.Embedder, error)
	VerifierFactory func(context.Context, semantic.ReuseConfig) (semantic.Verifier, error)
	Resources       admission.Source
	once            sync.Once
	gate            chan struct{}
}

type ReuseObservation struct {
	Mode                   string                    `json:"mode"`
	Status                 string                    `json:"status"`
	Partition              string                    `json:"partition,omitempty"`
	Source                 *semantic.Source          `json:"source,omitempty"`
	Similarity             float64                   `json:"similarity,omitempty"`
	EmbeddingInputTokens   *int64                    `json:"embedding_input_tokens"`
	EmbeddingMS            float64                   `json:"embedding_ms,omitempty"`
	Searches               int                       `json:"searches"`
	WaitMS                 float64                   `json:"wait_ms"`
	Verifications          []semantic.ValidityResult `json:"verifications"`
	Rejections             []string                  `json:"rejections"`
	Indexed                bool                      `json:"indexed"`
	GenerationAvoided      bool                      `json:"generation_avoided"`
	HistoricalUsageIsProxy bool                      `json:"historical_usage_is_proxy"`
}

type reuseProof struct {
	Schema           int                   `json:"schema"`
	Question         string                `json:"question"`
	RequestSHA256    string                `json:"request_sha256"`
	Partition        string                `json:"partition"`
	EmbeddingModelID string                `json:"embedding_model_id"`
	Source           inference.ReuseSource `json:"source"`
}

type reuseRunner interface {
	RunWithResolver(context.Context, inference.Spec, inference.ReuseResolver) (inference.Result, error)
	Lookup(context.Context, inference.Spec) (inference.Result, bool, error)
	RuntimeIdentity() string
}

type controlledResolver struct {
	owner       *ControlledReuser
	parent      context.Context
	request     Request
	tier        Tier
	runner      reuseRunner
	config      semantic.ReuseConfig
	observation *ReuseObservation
	verifier    semantic.Verifier
	initialized bool
	initErr     error
	embedding   *semantic.Embedding
}

func (r *controlledResolver) close() {
	if closer, ok := r.verifier.(interface{ Close() }); ok {
		closer.Close()
	}
}

func reuseHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// ReusePartition keeps the broad retrieval contract separate from the verifier
// version. A new verifier can inspect the same original sources, while aliases
// bind its complete policy/runtime identity through ReuseResolver.Identity.
func ReusePartition(request Request, tier Tier, runtimeID string) (string, error) {
	if request.SemanticReuse == nil {
		return "", errors.New("explicit controlled policy required")
	}
	c, err := semantic.NormalizeReuseConfig(*request.SemanticReuse)
	if err != nil {
		return "", err
	}
	base := request
	c.Config.Mode = "shadow"
	base.SemanticShadow = &c.Config
	partition, err := ShadowPartition(base, tier, runtimeID)
	if err != nil {
		return "", err
	}
	return reuseHash("controlled-v1\x00" + partition + "\x00" + c.Intent), nil
}

func (o *ControlledReuser) resolver(ctx context.Context, request Request, tier Tier, runner Runner) (*controlledResolver, error) {
	r, ok := runner.(reuseRunner)
	if !ok || o == nil || o.Broker == nil || o.Index == nil || o.EmbedderFactory == nil || o.VerifierFactory == nil {
		return nil, errors.New("controlled reuse capability unavailable")
	}
	c, err := semantic.NormalizeReuseConfig(*request.SemanticReuse)
	if err != nil {
		return nil, err
	}
	partition, err := ReusePartition(request, tier, r.RuntimeIdentity())
	if err != nil {
		return nil, err
	}
	return &controlledResolver{owner: o, parent: ctx, request: request, tier: tier, runner: r, config: c, observation: &ReuseObservation{Mode: "controlled", Status: "not_called", Partition: partition, Verifications: []semantic.ValidityResult{}, Rejections: []string{}}}, nil
}

func (r *controlledResolver) Identity() string {
	if !r.initialized {
		r.initialized = true
		ctx, cancel := context.WithTimeout(r.parent, time.Duration(r.config.VerifierTimeoutMS)*time.Millisecond)
		defer cancel()
		r.verifier, r.initErr = r.owner.VerifierFactory(ctx, r.config)
		if r.verifier == nil || r.initErr != nil {
			r.observation.Status = "verifier_unavailable"
			return ""
		}
	}
	if r.initErr != nil || r.verifier == nil || r.verifier.Identity() == "" {
		return ""
	}
	data, err := json.Marshal(struct {
		Schema                int
		ProtectedTermsVersion int
		Policy                semantic.ReuseConfig
		Partition, Verifier   string
	}{1, semantic.ProtectedTermsVersion, r.config, r.observation.Partition, r.verifier.Identity()})
	if err != nil {
		return ""
	}
	return reuseHash(string(data))
}

func (r *controlledResolver) Resolve(ctx context.Context, spec inference.Spec) (*inference.ReuseResolution, error) {
	o := r.owner
	obs := r.observation
	ctx, cancel := context.WithTimeout(ctx, time.Duration(r.config.TimeoutMS)*time.Millisecond)
	defer cancel()
	o.once.Do(func() { o.gate = make(chan struct{}, 1) })
	select {
	case o.gate <- struct{}{}:
		defer func() { <-o.gate }()
	default:
		obs.Status = "busy"
		return nil, nil
	}
	if o.Resources != nil {
		s, err := o.Resources.Sample(ctx)
		if err != nil || !s.Known || s.At.IsZero() || time.Since(s.At) > 5*time.Second || s.AvailableMemoryBytes < 256*admission.MiB || s.MemoryPressure != admission.Normal || s.CPUStallMilliPercent >= 20000 || (s.LoadCapacityMilli > 0 && s.LoadMilli > 2*s.LoadCapacityMilli) {
			obs.Status = "resource_pressure"
			return nil, nil
		}
	}
	embedder, err := o.EmbedderFactory(ctx, r.config.Config)
	if err != nil || embedder == nil {
		obs.Status = "embedding_unavailable"
		return nil, errors.New("controlled embedding unavailable")
	}
	if closer, ok := embedder.(interface{ Close() }); ok {
		defer closer.Close()
	}
	embedding, err := embedder.Embed(ctx, r.request.Spec.Input)
	obs.EmbeddingInputTokens, obs.EmbeddingMS = embedding.InputTokens, embedding.DurationMS
	if err != nil {
		obs.Status = "embedding_failed"
		return nil, errors.New("controlled embedding failed")
	}
	r.embedding = &embedding
	query := semantic.Query{Partition: obs.Partition, ModelID: embedding.ModelID, Vector: embedding.Vector, MinSimilarity: r.config.MinSimilarity, TopK: r.config.TopK, MaxScan: r.config.MaxScan}
	search, err := o.Index.QuerySemantic(ctx, query)
	obs.Searches++
	if err != nil {
		obs.Status = "search_failed"
		return nil, errors.New("controlled search unavailable")
	}
	if len(search.Candidates) == 0 && r.config.WaitForCandidateMS > 0 {
		started := time.Now()
		deadline := started.Add(time.Duration(r.config.WaitForCandidateMS) * time.Millisecond)
		waitCtx, stopWaiting := context.WithDeadline(ctx, deadline)
		defer stopWaiting()
		interval := time.Duration(r.config.WaitForCandidateMS) * time.Millisecond / 31
		if interval < 100*time.Millisecond {
			interval = 100 * time.Millisecond
		}
		for len(search.Candidates) == 0 && obs.Searches < 32 && time.Now().Before(deadline) {
			delay := min(interval, time.Until(deadline))
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				obs.Status = "wait_canceled"
				obs.WaitMS = float64(time.Since(started)) / float64(time.Millisecond)
				return nil, ctx.Err()
			case <-timer.C:
			}
			search, err = o.Index.QuerySemantic(waitCtx, query)
			obs.Searches++
			if err != nil {
				if ctx.Err() == nil && errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
					// This read cannot have started answer generation. Expiring the
					// optional wait permits the normal bounded fresh path.
					search.Candidates = nil
					break
				}
				obs.Status = "search_failed"
				obs.WaitMS = float64(time.Since(started)) / float64(time.Millisecond)
				return nil, errors.New("controlled peer search unavailable")
			}
		}
		obs.WaitMS = float64(time.Since(started)) / float64(time.Millisecond)
	}
	for _, candidate := range search.Candidates {
		resolution, eligible, err := r.candidate(ctx, candidate, embedding.ModelID)
		if err != nil {
			return nil, err
		}
		if eligible {
			return resolution, nil
		}
	}
	obs.Status = "miss_or_rejected"
	return nil, nil
}

func (r *controlledResolver) candidate(ctx context.Context, candidate semantic.Candidate, modelID string) (*inference.ReuseResolution, bool, error) {
	obs := r.observation
	proofArtifact, err := r.owner.Broker.Get(ctx, "semantic/v1/provenance/"+candidate.Source.SHA256)
	if err != nil {
		var e *client.Error
		if errors.As(err, &e) && e.Code == "not_found" {
			obs.Rejections = append(obs.Rejections, "missing_provenance")
			return nil, false, nil
		}
		obs.Status = "provenance_unavailable"
		return nil, false, errors.New("controlled provenance unavailable")
	}
	var proof reuseProof
	decoder := json.NewDecoder(bytes.NewReader([]byte(proofArtifact.Content)))
	decoder.DisallowUnknownFields()
	if len(proofArtifact.Content) > 64<<10 || uniqueJSON([]byte(proofArtifact.Content)) != nil || decoder.Decode(&proof) != nil || proof.Schema != 1 || proof.Partition != obs.Partition || proof.EmbeddingModelID != modelID || len(proof.Question) > r.config.MaxInputBytes || proof.Question == "" || proof.Source.Key != candidate.Source.Key || proof.Source.Version != candidate.Source.Version || proof.Source.SHA256 != candidate.Source.SHA256 || proof.Source.OutputSHA256 != candidate.Source.OutputSHA256 {
		obs.Status = "invalid_provenance"
		return nil, false, errors.New("controlled provenance invalid")
	}
	original := r.request
	original.Spec.Input = proof.Question
	digest := requestDigest(original)
	originalSpec := tierSpec(original, r.tier, digest)
	plan, err := inference.BuildPlan(originalSpec)
	if err != nil || digest != proof.RequestSHA256 || plan.Fingerprint != proof.Source.RequestFingerprint {
		obs.Status = "invalid_provenance"
		return nil, false, errors.New("controlled provenance does not bind source question")
	}
	source, hit, err := r.runner.Lookup(ctx, originalSpec)
	if err != nil {
		obs.Status = "source_unavailable"
		return nil, false, errors.New("controlled original source unavailable")
	}
	if !hit || "inference/v1/jobs/"+source.Fingerprint != proof.Source.Key || !knownComplete(source, originalSpec) {
		obs.Rejections = append(obs.Rejections, "source_not_eligible")
		return nil, false, nil
	}
	guards := semantic.CheckProtectedTerms(proof.Question, r.request.Spec.Input)
	if len(guards) > 0 {
		obs.Rejections = append(obs.Rejections, guards...)
		return nil, false, nil
	}
	if _, passed := evaluation.CheckOutput(source.Response.Text, r.request.Checks); !passed {
		obs.Rejections = append(obs.Rejections, "current_checks_failed")
		return nil, false, nil
	}
	var contextText string
	for _, c := range r.request.Spec.Context {
		contextText += c.ID + "\n" + c.Text + "\n"
	}
	verdict, err := r.verifier.Verify(ctx, semantic.ValidityRequest{OriginalQuestion: proof.Question, Question: r.request.Spec.Input, Instructions: r.request.Spec.Instructions, Context: contextText, CachedAnswer: source.Response.Text, Bindings: r.config.Bindings, Intent: r.config.Intent})
	obs.Verifications = append(obs.Verifications, verdict)
	if err != nil {
		obs.Status = "verification_failed"
		return nil, false, errors.New("controlled verification outcome unconfirmed")
	}
	if !verdict.Complete || !verdict.Durable || verdict.InputTokens == nil || verdict.OutputTokens == nil || *verdict.InputTokens < 0 || *verdict.OutputTokens < 0 || *verdict.InputTokens > r.config.VerifierMaxInputTokens || *verdict.OutputTokens > r.config.VerifierMaxOutputTokens || verdict.ReceiptKey == "" {
		obs.Status = "verification_unconfirmed"
		return nil, false, errors.New("controlled verification evidence is incomplete")
	}
	if !verdict.Accepted() {
		obs.Rejections = append(obs.Rejections, "verifier_rejected_or_abstained")
		return nil, false, nil
	}
	artifact, err := r.owner.Broker.Get(ctx, proof.Source.Key)
	if err != nil || artifact.Version != proof.Source.Version || reuseHash(artifact.Content) != proof.Source.SHA256 {
		obs.Status = "source_changed"
		return nil, false, errors.New("controlled source changed after verification")
	}
	var saved struct {
		Response *inference.Response `json:"response"`
	}
	if json.Unmarshal([]byte(artifact.Content), &saved) != nil || saved.Response == nil {
		return nil, false, errors.New("controlled source response invalid")
	}
	ref := candidate.Source
	obs.Source = &ref
	obs.Similarity = candidate.Similarity
	obs.Status = "verified_candidate"
	return &inference.ReuseResolution{PolicyID: r.Identity(), Source: proof.Source, Response: *saved.Response, VerificationKey: verdict.ReceiptKey}, true, nil
}

func (r *controlledResolver) finish(ctx context.Context, answer inference.Result) {
	obs := r.observation
	if answer.Reused {
		if answer.ReuseKind == "semantic" {
			obs.Status = "reused"
			obs.GenerationAvoided = true
			obs.HistoricalUsageIsProxy = true
			if answer.Resolution != nil {
				s := answer.Resolution.Source
				obs.Source = &semantic.Source{Key: s.Key, Version: s.Version, SHA256: s.SHA256, OutputSHA256: s.OutputSHA256}
			}
		} else {
			obs.Status = "skipped_exact"
		}
		return
	}
	if r.embedding == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(r.config.TimeoutMS)*time.Millisecond)
	defer cancel()
	a, err := r.owner.Broker.Get(ctx, "inference/v1/jobs/"+answer.Fingerprint)
	if err != nil {
		obs.Status = "index_source_unavailable"
		return
	}
	spec := tierSpec(r.request, r.tier, requestDigest(r.request))
	plan, err := inference.BuildPlan(spec)
	if err != nil {
		obs.Status = "index_plan_invalid"
		return
	}
	source := inference.ReuseSource{Key: a.Key, Version: a.Version, SHA256: reuseHash(a.Content), OutputSHA256: reuseHash(answer.Response.Text), RequestFingerprint: plan.Fingerprint}
	proof := reuseProof{Schema: 1, Question: r.request.Spec.Input, RequestSHA256: requestDigest(r.request), Partition: obs.Partition, EmbeddingModelID: r.embedding.ModelID, Source: source}
	data, _ := json.Marshal(proof)
	key := "semantic/v1/provenance/" + source.SHA256
	existing, readErr := r.owner.Broker.Get(ctx, key)
	if readErr == nil {
		if existing.Content != string(data) {
			obs.Status = "index_provenance_conflict"
			return
		}
	} else {
		var e *client.Error
		if !errors.As(readErr, &e) || e.Code != "not_found" {
			obs.Status = "index_provenance_unavailable"
			return
		}
		if _, err = r.owner.Broker.Publish(ctx, protocol.PublishRequest{Key: key, ExpectedVersion: 0, OperationID: "reuse-proof-" + reuseHash(string(data))[:32], Content: string(data)}); err != nil {
			obs.Status = "index_provenance_unconfirmed"
			return
		}
	}
	entry := semantic.Entry{Partition: obs.Partition, ModelID: r.embedding.ModelID, Vector: r.embedding.Vector, Source: semantic.Source{Key: source.Key, Version: source.Version, SHA256: source.SHA256, OutputSHA256: source.OutputSHA256}, CreatedAt: answer.CreatedAt, ExpiresAt: answer.ExpiresAt}
	entry.ID = semantic.EntryID(entry)
	if err := r.owner.Index.PutSemantic(ctx, semantic.PutRequest{Entry: entry, MaxEntries: r.config.MaxEntries}); err != nil {
		obs.Status = "index_failed"
		return
	}
	obs.Indexed = true
}
