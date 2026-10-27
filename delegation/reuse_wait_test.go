package delegation

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/semantic"
)

// The clients have independent credentials and HTTP transports, while their
// artifacts and semantic entries share the same authenticated SQLite scope.
func controlledWaitClients(t *testing.T) (*client.Client, *client.Client) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	server := httptest.NewServer(httpapi.New(db))
	t.Cleanup(server.Close)
	clients := make([]*client.Client, 2)
	for i, name := range []string{"producer", "waiter"} {
		token, err := db.CreateToken(t.Context(), name, "controlled-wait-tests")
		if err != nil {
			t.Fatal(err)
		}
		clients[i], err = client.New(server.URL, token)
		if err != nil {
			t.Fatal(err)
		}
	}
	return clients[0], clients[1]
}

type controlledWaitProvider struct {
	started     chan struct{}
	release     <-chan struct{}
	counts      atomic.Int32
	generations atomic.Int32
	canceled    atomic.Bool
}

func (*controlledWaitProvider) Identity() string { return "controlled-wait-target-fixture" }
func (p *controlledWaitProvider) CountInputTokens(context.Context, inference.Request) (int64, error) {
	p.counts.Add(1)
	return 100, nil
}
func (p *controlledWaitProvider) Generate(ctx context.Context, _ inference.Request) (inference.Response, error) {
	if p.generations.Add(1) == 1 && p.release != nil {
		close(p.started)
		select {
		case <-p.release:
		case <-ctx.Done():
			p.canceled.Store(true)
			return inference.Response{}, ctx.Err()
		}
	}
	return knownResult(`{"ship":false}`).Response, nil
}

type controlledWaitIndex struct {
	semantic.Index
	queries atomic.Int32
	misses  chan struct{}
}

func (i *controlledWaitIndex) QuerySemantic(ctx context.Context, query semantic.Query) (semantic.SearchResult, error) {
	result, err := i.Index.QuerySemantic(ctx, query)
	i.queries.Add(1)
	if err == nil && len(result.Candidates) == 0 {
		select {
		case i.misses <- struct{}{}:
		default:
		}
	}
	return result, err
}

func controlledWaitRouter(broker *client.Client, provider *controlledWaitProvider, index semantic.Index) Router {
	return Router{
		Factory: func(Tier) (Runner, error) {
			return &inference.Runner{Broker: broker, Provider: provider}, nil
		},
		Reuse: &ControlledReuser{
			Broker: broker, Index: index,
			EmbedderFactory: func(context.Context, semantic.Config) (semantic.Embedder, error) {
				return &shadowFailureEmbedder{}, nil
			},
			VerifierFactory: func(_ context.Context, config semantic.ReuseConfig) (semantic.Verifier, error) {
				provider := &fixtureProvider{identity: "controlled-wait-verifier-fixture", response: knownResult(`{"verdict":"accept","same_intent":true,"same_facts":true,"same_constraints":true,"answer_supported":true}`).Response}
				return &LocalVerifier{runner: &inference.Runner{Broker: broker, Provider: provider}, config: config, identity: reuseHash("controlled-wait-verifier-fixture")}, nil
			},
		},
	}
}

func controlledWaitRequest(question string, waitMS int) Request {
	request := testRequest()
	request.Policy.Escalation = nil
	request.Spec.Input = question
	config := controlledTestConfig()
	config.WaitForCandidateMS = waitMS
	request.SemanticReuse = &config
	return request
}

type controlledWaitOutcome struct {
	result Result
	err    error
}

func controlledWaitRun(ctx context.Context, router Router, request Request) <-chan controlledWaitOutcome {
	done := make(chan controlledWaitOutcome, 1)
	go func() {
		result, err := router.Run(ctx, request)
		done <- controlledWaitOutcome{result, err}
	}()
	return done
}

func controlledWaitSignal(t *testing.T, ctx context.Context, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatal("fixture synchronization timed out", ctx.Err())
	}
}

func controlledWaitResult(t *testing.T, ctx context.Context, done <-chan controlledWaitOutcome) controlledWaitOutcome {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-ctx.Done():
		t.Fatal("controlled worker did not finish", ctx.Err())
		return controlledWaitOutcome{}
	}
}

func TestControlledReuseWaitsForNewlyIndexedPeerSourceOverHTTP(t *testing.T) {
	producerClient, waiterClient := controlledWaitClients(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	release := make(chan struct{})
	producer := &controlledWaitProvider{started: make(chan struct{}), release: release}
	producerRequest := controlledWaitRequest("How do I reset an account password?", 0)
	producerDone := controlledWaitRun(ctx, controlledWaitRouter(producerClient, producer, producerClient), producerRequest)
	controlledWaitSignal(t, ctx, producer.started)

	waiter := &controlledWaitProvider{}
	index := &controlledWaitIndex{Index: waiterClient, misses: make(chan struct{}, 1)}
	waiterRequest := controlledWaitRequest("How can I reset an account password?", 2000)
	waiterDone := controlledWaitRun(ctx, controlledWaitRouter(waiterClient, waiter, index), waiterRequest)
	controlledWaitSignal(t, ctx, index.misses)
	close(release)
	first := controlledWaitResult(t, ctx, producerDone)
	second := controlledWaitResult(t, ctx, waiterDone)
	if first.err != nil || first.result.SemanticReuse == nil || !first.result.SemanticReuse.Indexed || first.result.Response == nil {
		t.Fatalf("producer did not complete and index its original source: %+v %v", first.result, first.err)
	}
	obs := second.result.SemanticReuse
	if second.err != nil || obs == nil || obs.Status != "reused" || !obs.GenerationAvoided || !obs.HistoricalUsageIsProxy || second.result.Response == nil || second.result.Response.Text != first.result.Response.Text {
		t.Fatalf("waiter did not reuse the completed peer: %+v %v", second.result, second.err)
	}
	if obs.Searches < 2 || obs.Searches > 32 || obs.Searches != int(index.queries.Load()) || obs.WaitMS <= 0 || len(obs.Verifications) != 1 || !obs.Verifications[0].Accepted() {
		t.Fatalf("wait did not report bounded polling and verification: %+v", obs)
	}
	sourceKey := "inference/v1/jobs/" + first.result.Attempts[0].Result.Fingerprint
	source, err := waiterClient.Get(ctx, sourceKey)
	if err != nil || obs.Source == nil || obs.Source.Key != sourceKey || obs.Source.Version != source.Version || obs.Source.SHA256 != reuseHash(source.Content) || obs.Source.OutputSHA256 != reuseHash(first.result.Response.Text) {
		t.Fatalf("waiter source does not pin the producer's exact completed artifact: %+v %v", obs.Source, err)
	}
	answer := second.result.Attempts[0].Result
	if producer.generations.Load() != 1 || waiter.generations.Load() != 0 || waiter.counts.Load() != 0 || !answer.Reused || answer.ReuseKind != "semantic" || answer.Measurement == nil || answer.Measurement.GenerationCalled || answer.ThisRunUsage != (inference.Usage{}) {
		t.Fatalf("peer reuse performed target work: producer=%d waiter=%d counts=%d %+v", producer.generations.Load(), waiter.generations.Load(), waiter.counts.Load(), answer)
	}
}

func TestControlledReuseCancelingWaiterLeavesProducerRunning(t *testing.T) {
	producerClient, waiterClient := controlledWaitClients(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	release := make(chan struct{})
	producer := &controlledWaitProvider{started: make(chan struct{}), release: release}
	producerRequest := controlledWaitRequest("How do I reset an account password?", 0)
	producerDone := controlledWaitRun(ctx, controlledWaitRouter(producerClient, producer, producerClient), producerRequest)
	controlledWaitSignal(t, ctx, producer.started)

	waiter := &controlledWaitProvider{}
	index := &controlledWaitIndex{Index: waiterClient, misses: make(chan struct{}, 1)}
	waiterCtx, cancelWaiter := context.WithCancel(ctx)
	defer cancelWaiter()
	waiterRequest := controlledWaitRequest("How can I reset an account password?", 2000)
	waiterDone := controlledWaitRun(waiterCtx, controlledWaitRouter(waiterClient, waiter, index), waiterRequest)
	controlledWaitSignal(t, ctx, index.misses)
	cancelWaiter()
	second := controlledWaitResult(t, ctx, waiterDone)
	if !errors.Is(second.err, context.Canceled) || second.result.Status != "stopped" || second.result.SemanticReuse == nil || second.result.SemanticReuse.Status != "wait_canceled" || waiter.generations.Load() != 0 || waiter.counts.Load() != 0 {
		t.Fatalf("canceling waiter did not stop its own target work: %+v %v", second.result, second.err)
	}
	select {
	case result := <-producerDone:
		t.Fatalf("waiter cancellation ended the held producer: %+v %v", result.result, result.err)
	default:
	}
	close(release)
	first := controlledWaitResult(t, ctx, producerDone)
	if first.err != nil || first.result.Status != "completed" || first.result.SemanticReuse == nil || !first.result.SemanticReuse.Indexed || producer.canceled.Load() || producer.generations.Load() != 1 {
		t.Fatalf("producer could not complete after waiter cancellation: %+v %v", first.result, first.err)
	}
}

func TestControlledReuseCandidateWaitExpiresToFreshGenerationWithinSearchBound(t *testing.T) {
	_, waiterClient := controlledWaitClients(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	provider := &controlledWaitProvider{}
	index := &controlledWaitIndex{Index: waiterClient, misses: make(chan struct{}, 1)}
	request := controlledWaitRequest("How can I reset an account password?", 3100)
	started := time.Now()
	result, err := controlledWaitRouter(waiterClient, provider, index).Run(ctx, request)
	obs := result.SemanticReuse
	if err != nil || result.Status != "completed" || obs == nil || obs.Status != "miss_or_rejected" || !obs.Indexed || obs.GenerationAvoided || len(obs.Verifications) != 0 {
		t.Fatalf("expired candidate wait did not complete fresh work: %+v %v", result, err)
	}
	if obs.Searches < 2 || obs.Searches > 32 || obs.Searches != int(index.queries.Load()) || obs.WaitMS < float64(request.SemanticReuse.WaitForCandidateMS) || time.Since(started) >= 9*time.Second {
		t.Fatalf("candidate polling exceeded its search or time bounds: %+v", obs)
	}
	answer := result.Attempts[0].Result
	if provider.generations.Load() != 1 || provider.counts.Load() != 1 || answer.Reused || answer.Measurement == nil || !answer.Measurement.GenerationCalled || answer.ThisRunUsage != answer.GenerationUsage {
		t.Fatalf("expired wait did not perform exactly one fresh generation: calls=%d counts=%d %+v", provider.generations.Load(), provider.counts.Load(), answer)
	}
}

// The initial query reaches the real HTTP/SQLite index. A later read stalls
// until its supplied context ends, exposing which deadline bounds peer polling.
type controlledSlowWaitIndex struct {
	semantic.Index
	queries atomic.Int32
	waiting chan context.Context
}

func (i *controlledSlowWaitIndex) QuerySemantic(ctx context.Context, query semantic.Query) (semantic.SearchResult, error) {
	if i.queries.Add(1) == 1 {
		return i.Index.QuerySemantic(ctx, query)
	}
	select {
	case i.waiting <- ctx:
	default:
	}
	<-ctx.Done()
	return semantic.SearchResult{}, ctx.Err()
}

func TestControlledReuseSlowPeerSearchHonorsWaitAndCallerCancellation(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		name := "wait_expiry"
		if cancelParent {
			name = "parent_cancellation"
		}
		t.Run(name, func(t *testing.T) {
			_, broker := controlledWaitClients(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			t.Cleanup(cancel)
			runCtx, cancelRun := context.WithCancel(ctx)
			defer cancelRun()
			provider := &controlledWaitProvider{}
			index := &controlledSlowWaitIndex{Index: broker, waiting: make(chan context.Context, 1)}
			waitMS := 300
			if cancelParent {
				waitMS = 2000
			}
			request := controlledWaitRequest("How can I reset an account password?", waitMS)
			done := controlledWaitRun(runCtx, controlledWaitRouter(broker, provider, index), request)
			var queryCtx context.Context
			select {
			case queryCtx = <-index.waiting:
			case <-ctx.Done():
				t.Fatal("repeated search did not start", ctx.Err())
			}
			deadline, ok := queryCtx.Deadline()
			if !ok || time.Until(deadline) > time.Duration(waitMS)*time.Millisecond {
				t.Fatalf("slow repeated search lacks the candidate wait deadline: deadline=%v wait_ms=%d", deadline, waitMS)
			}
			if cancelParent {
				cancelRun()
			}
			outcome := controlledWaitResult(t, ctx, done)
			obs := outcome.result.SemanticReuse
			if obs == nil || obs.Searches != 2 || index.queries.Load() != 2 || obs.WaitMS <= 0 || obs.GenerationAvoided || len(obs.Verifications) != 0 {
				t.Fatalf("slow repeated search lost bounded observation: %+v %v", outcome.result, outcome.err)
			}
			if cancelParent {
				if !errors.Is(queryCtx.Err(), context.Canceled) || !errors.Is(outcome.err, ErrStopped) || outcome.result.Status != "stopped" || outcome.result.Response != nil || obs.Indexed || provider.counts.Load() != 0 || provider.generations.Load() != 0 {
					t.Fatalf("caller cancellation during search authorized fresh work: %+v %v", outcome.result, outcome.err)
				}
				return
			}
			if !errors.Is(queryCtx.Err(), context.DeadlineExceeded) || runCtx.Err() != nil || outcome.err != nil || outcome.result.Status != "completed" || obs.Status != "miss_or_rejected" || !obs.Indexed || obs.WaitMS < float64(waitMS) || obs.WaitMS > float64(waitMS+1000) || provider.counts.Load() != 1 || provider.generations.Load() != 1 {
				t.Fatalf("slow peer query outlasted candidate wait or blocked fresh work: %+v %v", outcome.result, outcome.err)
			}
			answer := outcome.result.Attempts[0].Result
			if answer.Reused || answer.Measurement == nil || !answer.Measurement.GenerationCalled {
				t.Fatalf("wait expiry did not record fresh generation: %+v", answer)
			}
		})
	}
}
