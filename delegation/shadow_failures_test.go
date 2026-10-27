package delegation

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/semantic"
)

type shadowFailureEmbedder struct {
	err        error
	during     func(context.Context)
	calls      atomic.Int32
	closeCalls atomic.Int32
}

func (e *shadowFailureEmbedder) Embed(ctx context.Context, _ string) (semantic.Embedding, error) {
	e.calls.Add(1)
	if e.during != nil {
		e.during(ctx)
	}
	tokens := int64(7)
	embedding := semantic.Embedding{ModelID: strings.Repeat("a", 64), Vector: []float32{1, 0}, InputTokens: &tokens, DurationMS: 2.5}
	if ctx.Err() != nil {
		return embedding, ctx.Err()
	}
	return embedding, e.err
}

func (e *shadowFailureEmbedder) Close() { e.closeCalls.Add(1) }

type shadowFailureIndex struct {
	queryErr   error
	putErr     error
	queryCalls atomic.Int32
	putCalls   atomic.Int32
}

func (i *shadowFailureIndex) QuerySemantic(ctx context.Context, q semantic.Query) (semantic.SearchResult, error) {
	i.queryCalls.Add(1)
	if err := semantic.ValidateQuery(q); err != nil {
		return semantic.SearchResult{}, err
	}
	if ctx.Err() != nil {
		return semantic.SearchResult{}, ctx.Err()
	}
	return semantic.SearchResult{}, i.queryErr
}

func (i *shadowFailureIndex) PutSemantic(ctx context.Context, p semantic.PutRequest) error {
	i.putCalls.Add(1)
	if err := semantic.ValidatePut(p); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return i.putErr
}

func shadowFailureRequest() Request {
	r := testRequest()
	r.SemanticShadow = &semantic.Config{Mode: "shadow", Family: "release-check", Domain: "fixture", EvidenceRevision: "r1", EmbeddingModel: "fixture:local"}
	return r
}

func TestShadowFailuresPreservePassingGenerationAndExactReuse(t *testing.T) {
	for _, stage := range []string{"factory_error", "nil_factory_result", "embed_error", "query_error", "put_error", "canceled_embedding"} {
		t.Run(stage, func(t *testing.T) {
			broker := testBroker(t)
			provider := &fixtureProvider{response: knownResult(`{"ship":false}`).Response, identity: "shadow-failure-fixture"}
			index := &shadowFailureIndex{}
			embedder := &shadowFailureEmbedder{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wantStatus := "embedding_unavailable"
			switch stage {
			case "embed_error":
				embedder.err, wantStatus = errors.New("synthetic embedding failure"), "embedding_failed"
			case "query_error":
				index.queryErr, wantStatus = errors.New("synthetic index read failure"), "search_failed"
			case "put_error":
				index.putErr, wantStatus = errors.New("synthetic index write failure"), "index_failed"
			case "canceled_embedding":
				embedder.during, wantStatus = func(context.Context) { cancel() }, "embedding_failed"
			}
			var shadowFactories atomic.Int32
			observer := &ShadowObserver{Broker: broker, Index: index, EmbedderFactory: func(context.Context, semantic.Config) (semantic.Embedder, error) {
				shadowFactories.Add(1)
				switch stage {
				case "factory_error":
					return nil, errors.New("synthetic factory failure")
				case "nil_factory_result":
					return nil, nil
				default:
					return embedder, nil
				}
			}}
			runnerFactories := 0
			router := Router{Shadow: observer, Factory: func(tier Tier) (Runner, error) {
				runnerFactories++
				if tier.ID != "compact" {
					t.Fatal("shadow failure escalated a passing answer")
				}
				return inference.Runner{Broker: broker, Provider: provider}, nil
			}}
			request := shadowFailureRequest()
			first, err := router.Run(ctx, request)
			if err != nil || first.Status != "completed" || first.SelectedTier != "compact" || first.Response == nil || first.Response.Text != provider.response.Text || first.Response.Usage != provider.response.Usage || len(first.Attempts) != 1 || !first.Attempts[0].QualityPassed {
				t.Fatalf("shadow failure changed passing answer: %+v, %v", first, err)
			}
			if first.SemanticShadow == nil || first.SemanticShadow.Status != wantStatus || first.SemanticShadow.GenerationAvoided || first.SemanticShadow.Indexed {
				t.Fatalf("wrong failure observation: %+v", first.SemanticShadow)
			}
			generated := first.Attempts[0].Result
			if generated.Reused || generated.GenerationUsage != provider.response.Usage || generated.ThisRunUsage != provider.response.Usage || generated.AvoidedUsage != (inference.Usage{}) || provider.generations != 1 || provider.countCalls != 1 || runnerFactories != 1 || shadowFactories.Load() != 1 {
				t.Fatalf("shadow changed generation or accounting: %+v", generated)
			}
			wantClose := int32(1)
			if stage == "factory_error" || stage == "nil_factory_result" {
				wantClose = 0
			} else if first.SemanticShadow.EmbeddingInputTokens == nil || *first.SemanticShadow.EmbeddingInputTokens != 7 || first.SemanticShadow.EmbeddingMS != 2.5 {
				t.Fatalf("known embedding work lost on failure: %+v", first.SemanticShadow)
			}
			if embedder.closeCalls.Load() != wantClose {
				t.Fatalf("embedder closed %d times, want %d", embedder.closeCalls.Load(), wantClose)
			}
			queries, puts := index.queryCalls.Load(), index.putCalls.Load()
			second, err := router.Run(context.Background(), request)
			if err != nil || second.Status != "completed" || second.Response == nil || second.Response.Text != first.Response.Text || second.Response.Usage != (inference.Usage{}) || len(second.Attempts) != 1 || second.SemanticShadow == nil || second.SemanticShadow.Status != "skipped_exact" {
				t.Fatalf("shadow failure affected exact repeat: %+v, %v", second, err)
			}
			reused := second.Attempts[0].Result
			if !reused.Reused || reused.Fingerprint != generated.Fingerprint || reused.GenerationUsage != generated.GenerationUsage || reused.ThisRunUsage != (inference.Usage{}) || reused.AvoidedUsage != generated.GenerationUsage || provider.generations != 1 || provider.countCalls != 1 || shadowFactories.Load() != 1 || index.queryCalls.Load() != queries || index.putCalls.Load() != puts || embedder.closeCalls.Load() != wantClose {
				t.Fatalf("exact repeat performed shadow/generation work or changed accounting: %+v", reused)
			}
		})
	}
}

func TestShadowNeverStartsForPendingOrQualityFailedOutcomes(t *testing.T) {
	for _, pending := range []bool{false, true} {
		request := shadowFailureRequest()
		request.Policy.Escalation = nil
		runner := &scriptedRunner{result: knownResult(`{"ship":true}`)}
		wantError := ErrQualityFailed
		if pending {
			runner.err, wantError = inference.ErrInFlight, ErrStopped
		}
		var factories atomic.Int32
		observer := &ShadowObserver{Broker: testBroker(t), Index: &shadowFailureIndex{}, EmbedderFactory: func(context.Context, semantic.Config) (semantic.Embedder, error) {
			factories.Add(1)
			return &shadowFailureEmbedder{}, nil
		}}
		got, err := (Router{Shadow: observer, Factory: func(Tier) (Runner, error) { return runner, nil }}).Run(context.Background(), request)
		if !errors.Is(err, wantError) || got.Response != nil || got.SemanticShadow != nil || factories.Load() != 0 || runner.calls != 1 {
			t.Fatalf("unsafe outcome started shadow work: pending=%v %+v, %v", pending, got, err)
		}
	}
}

func TestShadowDefersBusyAndResourcePressureWithoutStartingEmbeddings(t *testing.T) {
	broker := testBroker(t)
	provider := &fixtureProvider{response: knownResult(`{"ship":false}`).Response, identity: "shadow-resource-fixture"}
	runner := inference.Runner{Broker: broker, Provider: provider}
	request := shadowFailureRequest()
	answer := knownResult(`{"ship":false}`)
	var factories atomic.Int32
	observer := &ShadowObserver{Broker: broker, Index: &shadowFailureIndex{}, EmbedderFactory: func(context.Context, semantic.Config) (semantic.Embedder, error) {
		factories.Add(1)
		return &shadowFailureEmbedder{}, nil
	}}
	observer.Resources = admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		return admission.Snapshot{At: time.Now(), Known: true, AvailableMemoryBytes: 128 * admission.MiB, MemoryPressure: admission.High}, nil
	})
	if got := observer.observe(context.Background(), request, request.Policy.Local, runner, answer); got.Status != "resource_pressure" || factories.Load() != 0 || got.GenerationAvoided {
		t.Fatalf("resource pressure started embedding: %+v", got)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	observer.Resources = admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		close(entered)
		<-release
		return admission.Snapshot{}, errors.New("synthetic sampler unavailable")
	})
	done := make(chan *semantic.Observation, 1)
	go func() { done <- observer.observe(context.Background(), request, request.Policy.Local, runner, answer) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("first observation did not enter admission")
	}
	busy := observer.observe(context.Background(), request, request.Policy.Local, runner, answer)
	close(release)
	if busy.Status != "busy" || factories.Load() != 0 || busy.GenerationAvoided {
		t.Fatalf("concurrent shadow observation queued or embedded: %+v", busy)
	}
	select {
	case first := <-done:
		if first.Status != "resource_pressure" {
			t.Fatalf("unexpected first observation: %+v", first)
		}
	case <-time.After(time.Second):
		t.Fatal("deferred observation did not finish")
	}
}
