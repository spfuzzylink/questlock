package delegation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/outcomes"
	"github.com/spfuzzylink/questlock/semantic"
)

type controlledStoredIndex struct{ b storedBroker }

func (i controlledStoredIndex) QuerySemantic(ctx context.Context, q semantic.Query) (semantic.SearchResult, error) {
	r, e := i.b.store.QuerySemantic(ctx, i.b.principal, q)
	return r, clientError(e)
}
func (i controlledStoredIndex) PutSemantic(ctx context.Context, p semantic.PutRequest) error {
	return clientError(i.b.store.PutSemantic(ctx, i.b.principal, p))
}

func TestControlledReuseRegistersEveryVerificationWithRealOutcomeStore(t *testing.T) {
	for _, kind := range []string{"accepted", "rejected", "uncertain"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			b := testBroker(t)
			main := &fixtureProvider{identity: "main-fixture", response: knownResult(`{"ship":false}`).Response}
			vp := &fixtureProvider{identity: "verifier-fixture", response: knownResult(`{"verdict":"accept","same_intent":true,"same_facts":true,"same_constraints":true,"answer_supported":true}`).Response}
			if kind == "rejected" {
				vp.response = knownResult(`{"verdict":"reject","same_intent":false,"same_facts":false,"same_constraints":false,"answer_supported":false}`).Response
			}
			if kind == "uncertain" {
				vp.err = errors.New("synthetic lost stream")
			}
			verifier := &LocalVerifier{runner: &inference.Runner{Broker: b, Provider: vp}, config: controlledTestConfig(), identity: strings.Repeat("a", 64)}
			embedder := &shadowFailureEmbedder{}
			reuse := &ControlledReuser{Broker: b, Index: controlledStoredIndex{b}, EmbedderFactory: func(context.Context, semantic.Config) (semantic.Embedder, error) { return embedder, nil }, VerifierFactory: func(context.Context, semantic.ReuseConfig) (semantic.Verifier, error) { return verifier, nil }}
			router := Router{Reuse: reuse, Factory: func(Tier) (Runner, error) { return &inference.Runner{Broker: b, Provider: main}, nil }}
			req := testRequest()
			req.Policy.Escalation = nil
			cfg := controlledTestConfig()
			req.SemanticReuse = &cfg
			req.Spec.Input = "How do I reset an account password?"
			first, err := router.Run(ctx, req)
			if err != nil || !first.SemanticReuse.Indexed {
				t.Fatalf("source setup failed %+v %v", first, err)
			}
			RecordOutcome(ctx, b, req, &first, "seed")
			if first.OutcomeTracking.Status != "recorded" {
				t.Fatalf("source outcome failed %+v", first.OutcomeTracking)
			}
			req.Spec.Input = "How can I reset an account password?"
			second, err := router.Run(ctx, req)
			if kind == "uncertain" {
				if err == nil || main.generations != 1 {
					t.Fatalf("uncertain verifier retried target generation %+v %v", second, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if kind == "accepted" && (main.generations != 1 || !second.SemanticReuse.GenerationAvoided) {
				t.Fatalf("semantic answer did not avoid target generation %+v", second)
			}
			if kind == "rejected" && main.generations != 2 {
				t.Fatal("known reject did not reach fresh generation")
			}
			RecordOutcome(ctx, b, req, &second, "second")
			if second.OutcomeTracking.Status != "recorded" {
				t.Fatalf("verification ledger registration failed %+v", second.OutcomeTracking)
			}
			snapshot, err := (outcomes.Store{Broker: b}).Get(ctx, outcomes.Task{ID: req.Spec.Task, Revision: req.Spec.Revision})
			if err != nil || len(snapshot.Runs) != 2 || len(snapshot.Runs[1].VerificationWork) != 1 {
				t.Fatalf("durable verifier work missing: %+v %v", snapshot, err)
			}
			work := snapshot.Runs[1].VerificationWork[0]
			if !work.GenerationCalled {
				t.Fatal("verifier generation not counted")
			}
			if kind == "uncertain" {
				if work.UsageKnown || work.Complete || work.Durable {
					t.Fatal("unknown work fabricated completion")
				}
			} else if !work.UsageKnown || work.Usage.InputTokens != 100 || work.Usage.OutputTokens != 20 || work.OutputSHA256 == "" {
				t.Fatalf("known verifier work incomplete %+v", work)
			}
			report, err := (outcomes.Store{Broker: b}).Report(ctx, []outcomes.Task{{ID: req.Spec.Task, Revision: req.Spec.Revision}}, nil)
			if err != nil || report.AttemptCount != 3 {
				t.Fatalf("report omitted auxiliary work %+v %v", report, err)
			}
			if kind == "uncertain" && report.MissingUsage != 1 {
				t.Fatal("unknown verifier usage absent from report")
			}
		})
	}
}
