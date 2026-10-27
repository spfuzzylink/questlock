// Package semanticquest exercises six shadow scenarios through the real broker.
// Generation is always a deterministic fixture, never a provider performance test.
package semanticquest

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/semantic"
)

type Case struct {
	ID         string `json:"id"`
	First      string `json:"first"`
	Paraphrase string `json:"paraphrase"`
	Answer     string `json:"answer"`
}

func Cases() []Case {
	return []Case{
		{"support-faq", "How do I reset my account password?", "How can I reset my account password?", "Use the password reset link."},
		{"internal-onboarding", "How do employees request access to the internal wiki?", "How can an employee request internal wiki access?", "Request wiki access through the team portal."},
		{"incident-brief", "Summarize the database outage in incident INC-17.", "Give me a summary of incident INC-17's database outage.", "INC-17 was a database outage resolved by rollback."},
		{"release-readiness", "Is commit abc123 ready to release after the required checks?", "Do the required checks allow releasing commit abc123?", "Commit abc123 passed all required release checks."},
		{"refund-eligibility", "Is order ORD-17 eligible for a refund within 30 days?", "Can order ORD-17 receive a refund within 30 days?", "Order ORD-17 is eligible under the 30-day policy."},
		{"shared-broker-lifecycle", "Where is the approved deployment checklist for this service?", "Where can I find this service's approved deployment checklist?", "The approved deployment checklist is in the service handbook."},
	}
}

type Row struct {
	ID                    string                `json:"id"`
	Passed                bool                  `json:"passed"`
	ExactSkippedEmbedding bool                  `json:"exact_skipped_embedding"`
	CandidateObserved     bool                  `json:"candidate_observed"`
	GuardRejected         bool                  `json:"guard_rejected"`
	BrokerRestarted       bool                  `json:"broker_restarted"`
	First                 *semantic.Observation `json:"first_shadow"`
	Paraphrase            *semantic.Observation `json:"paraphrase_shadow"`
	Negative              *semantic.Observation `json:"negative_shadow"`
}

type Report struct {
	Mode                        string `json:"mode"`
	Generation                  string `json:"generation"`
	PhysicalHosts               int    `json:"physical_hosts"`
	IsolatedClients             int    `json:"isolated_clients"`
	Passed                      int    `json:"passed"`
	Total                       int    `json:"total"`
	FixtureGenerationCalls      int64  `json:"fixture_generation_calls"`
	EmbeddingCalls              int64  `json:"embedding_calls"`
	GenerationAvoidedBySemantic int    `json:"generation_avoided_by_semantic"`
	ProviderAPISpendUSD         int    `json:"provider_api_spend_usd"`
	Cases                       []Row  `json:"cases"`
}

type fixtureProvider struct {
	answer string
	calls  *atomic.Int64
}

func (p fixtureProvider) Identity() string { return "semantic-quest-fixture-v1" }
func (p fixtureProvider) CountInputTokens(context.Context, inference.Request) (int64, error) {
	return 16, nil
}
func (p fixtureProvider) Generate(context.Context, inference.Request) (inference.Response, error) {
	p.calls.Add(1)
	return inference.Response{Model: "fixture", Text: p.answer, Complete: true, StopReason: "stop", Usage: inference.Usage{InputTokens: 16, UncachedInputTokens: 16, OutputTokens: 16}}, nil
}

type fixtureEmbedder struct{}

func (fixtureEmbedder) Embed(context.Context, string) (semantic.Embedding, error) {
	return semantic.Embedding{ModelID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Vector: []float32{1, 0, 0}}, nil
}

type countedEmbedder struct {
	inner semantic.Embedder
	calls *atomic.Int64
}

func (c countedEmbedder) Embed(ctx context.Context, input string) (semantic.Embedding, error) {
	c.calls.Add(1)
	return c.inner.Embed(ctx, input)
}

// Request creates a synthetic, explicitly bounded checked task for examples and
// repeatable validation. Its embedding threshold is a fixture setting, not a
// calibrated recommendation for other workloads.
func Request(c Case) delegation.Request {
	return delegation.Request{Spec: inference.Spec{Task: c.ID, Revision: "approved-1", Instructions: "Answer using only the approved evidence.", Input: c.First, Context: []inference.ContextItem{{ID: "approved-evidence", Text: c.Answer, Required: true, Stable: true}}}, Policy: delegation.Policy{Local: delegation.Tier{ID: "local", Spec: inference.Spec{Provider: "ollama", Model: "fixture", MaxContextBytes: 4096, MaxInputTokens: 1024, MaxOutputTokens: 128, ReuseForSeconds: 3600}}}, Checks: []evaluation.Check{{ID: "expected", Kind: "contains", Text: c.Answer}}, SemanticShadow: &semantic.Config{Mode: "shadow", Family: c.ID, Domain: "synthetic-example", EvidenceRevision: "approved-1", Bindings: map[string]string{"entity": "approved-entity", "period": "approved-period"}, EmbeddingModel: "all-minilm", MinSimilarity: 0.8, TimeoutMS: 30000}}
}

// Run uses a fresh local database. A supplied embedder enables real local
// embedding validation while generation remains explicitly simulated.
func Run(ctx context.Context, embedder semantic.Embedder) (Report, error) {
	report := Report{Mode: "fixture", Generation: "deterministic_fixture", PhysicalHosts: 1, IsolatedClients: 2, Total: 6}
	if embedder == nil {
		embedder = fixtureEmbedder{}
	} else {
		report.Mode = "live_embeddings_fixture_generation"
	}
	dir, err := os.MkdirTemp("", "questlock-semantic-")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return report, err
	}
	db := filepath.Join(dir, "state.db")
	s, err := store.Open(db)
	if err != nil {
		return report, err
	}
	defer func() { _ = s.Close() }()
	tokenA, err := s.CreateToken(ctx, "node-a", "semantic-quest")
	if err != nil {
		return report, err
	}
	tokenB, err := s.CreateToken(ctx, "node-b", "semantic-quest")
	if err != nil {
		return report, err
	}
	server := httptest.NewServer(httpapi.New(s))
	defer func() { server.Close() }()
	var generations, embeddings atomic.Int64
	for _, c := range Cases() {
		row := Row{ID: c.ID}
		makeRouter := func(token string) (delegation.Router, error) {
			broker, err := client.New(server.URL, token)
			if err != nil {
				return delegation.Router{}, err
			}
			observer := &delegation.ShadowObserver{Broker: broker, Index: broker, EmbedderFactory: func(context.Context, semantic.Config) (semantic.Embedder, error) {
				return countedEmbedder{embedder, &embeddings}, nil
			}}
			return delegation.Router{Factory: func(delegation.Tier) (delegation.Runner, error) {
				return inference.Runner{Broker: broker, Provider: fixtureProvider{c.Answer, &generations}}, nil
			}, Shadow: observer}, nil
		}
		router, err := makeRouter(tokenA)
		if err != nil {
			return report, err
		}
		request := Request(c)
		first, err := router.Run(ctx, request)
		if err != nil {
			return report, err
		}
		row.First = first.SemanticShadow
		if c.ID == "shared-broker-lifecycle" {
			server.Close()
			if err = s.Close(); err != nil {
				return report, err
			}
			s, err = store.Open(db)
			if err != nil {
				return report, err
			}
			server = httptest.NewServer(httpapi.New(s))
			row.BrokerRestarted = true
		}
		router, err = makeRouter(tokenB)
		if err != nil {
			return report, err
		}
		beforeEmbedding, beforeGen := embeddings.Load(), generations.Load()
		exact, err := router.Run(ctx, request)
		if err != nil {
			return report, err
		}
		row.ExactSkippedEmbedding = exact.SemanticShadow != nil && exact.SemanticShadow.Status == "skipped_exact" && embeddings.Load() == beforeEmbedding && generations.Load() == beforeGen && len(exact.Attempts) == 1 && exact.Attempts[0].Result.Reused
		request.Spec.Input = c.Paraphrase
		paraphrase, err := router.Run(ctx, request)
		if err != nil {
			return report, err
		}
		row.Paraphrase = paraphrase.SemanticShadow
		row.CandidateObserved = paraphrase.SemanticShadow != nil && paraphrase.SemanticShadow.Status == "candidates" && paraphrase.SemanticShadow.Search != nil && len(paraphrase.SemanticShadow.Search.Candidates) > 0 && generations.Load() == beforeGen+1 && !paraphrase.SemanticShadow.GenerationAvoided
		request.Spec.Input = c.Paraphrase + " This is a different approved scope or fact."
		switch c.ID {
		case "support-faq":
			request.SemanticShadow.EvidenceRevision = "policy-2"
		case "internal-onboarding":
			request.SemanticShadow.Domain = "other-domain"
		case "incident-brief":
			request.SemanticShadow.Bindings["period"] = "new-period"
		case "release-readiness":
			request.SemanticShadow.Bindings["commit"] = "def456"
		case "refund-eligibility":
			request.SemanticShadow.Bindings["entity"] = "other-order"
		default:
			request.SemanticShadow.EvidenceRevision = "expired-contract"
		}
		negative, err := router.Run(ctx, request)
		if err != nil {
			return report, err
		}
		row.Negative = negative.SemanticShadow
		row.GuardRejected = negative.SemanticShadow != nil && negative.SemanticShadow.Status == "miss" && negative.SemanticShadow.Search != nil && len(negative.SemanticShadow.Search.Candidates) == 0
		row.Passed = first.Status == "completed" && row.First != nil && row.First.Status == "miss" && row.ExactSkippedEmbedding && row.CandidateObserved && row.GuardRejected
		if row.Passed {
			report.Passed++
		}
		report.Cases = append(report.Cases, row)
	}
	report.FixtureGenerationCalls, report.EmbeddingCalls = generations.Load(), embeddings.Load()
	if report.Passed != report.Total {
		return report, errors.New("semantic shadow qualification did not pass every case; retain the report")
	}
	return report, nil
}
