// Package controlledquest exercises the controlled reuse workflow through a real
// local broker. Its provider, embeddings, token counts, and verifier are explicit
// deterministic fixtures, not measurements of a model's answer quality or speed.
package controlledquest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/semantic"
)

const (
	fixtureGenerationInput  int64 = 16
	fixtureGenerationOutput int64 = 16
	fixtureEmbeddingInput   int64 = 8
	fixtureVerifierInput    int64 = 16
	fixtureVerifierOutput   int64 = 8
)

// WorkCounts count fixture execution, independently of reused historical usage.
// Their token values are declared fixture values, not tokenizer measurements.
type WorkCounts struct {
	GenerationCalls        int64 `json:"generation_calls"`
	GenerationInputTokens  int64 `json:"generation_input_tokens"`
	GenerationOutputTokens int64 `json:"generation_output_tokens"`
	EmbeddingCalls         int64 `json:"embedding_calls"`
	EmbeddingInputTokens   int64 `json:"embedding_input_tokens"`
	VerifierCalls          int64 `json:"verifier_calls"`
	VerifierInputTokens    int64 `json:"verifier_input_tokens"`
	VerifierOutputTokens   int64 `json:"verifier_output_tokens"`
}

type Step struct {
	Name        string                       `json:"name"`
	Passed      bool                         `json:"passed"`
	Status      string                       `json:"status"`
	Reused      bool                         `json:"reused"`
	ReuseKind   string                       `json:"reuse_kind,omitempty"`
	Work        WorkCounts                   `json:"fixture_work"`
	Observation *delegation.ReuseObservation `json:"semantic_reuse,omitempty"`
}

type Row struct {
	ID              string `json:"id"`
	Passed          bool   `json:"passed"`
	BrokerRestarted bool   `json:"broker_restarted"`
	Steps           []Step `json:"steps"`
}

type Report struct {
	Mode                             string     `json:"mode"`
	Generation                       string     `json:"generation"`
	Embedding                        string     `json:"embedding"`
	Verification                     string     `json:"verification"`
	PhysicalHosts                    int        `json:"physical_hosts"`
	IsolatedClients                  int        `json:"isolated_clients"`
	Passed                           int        `json:"passed"`
	Total                            int        `json:"total"`
	RequestRuns                      int        `json:"request_runs"`
	ActualFixtureWork                WorkCounts `json:"actual_fixture_work"`
	ExactOnlyReferenceCalls          int        `json:"exact_only_reference_generation_calls"`
	ExactOnlyReferenceInputTokens    int64      `json:"exact_only_reference_input_tokens"`
	ExactOnlyReferenceOutputTokens   int64      `json:"exact_only_reference_output_tokens"`
	ReferenceKind                    string     `json:"reference_kind"`
	GenerationAvoidedBySemantic      int        `json:"generation_avoided_by_semantic"`
	GenerationAvoidedByExact         int        `json:"generation_avoided_by_exact"`
	HistoricalSourceUsageProxyTokens int64      `json:"historical_source_usage_proxy_tokens"`
	ProviderAPISpendUSD              int        `json:"provider_api_spend_usd"`
	Limitations                      []string   `json:"limitations"`
	Cases                            []Row      `json:"cases"`
}

type fixtureCase struct {
	id, marker, first, paraphrase, afterRestart string
	changedFact, changedFactAnswer              string
	constraint, answer, evidence                string
}

// These workflow fixtures are separate from the frozen labelled challenge
// population. Small wording changes deliberately keep its protected terms fixed
// so an oracle verdict can exercise the real resolver and durable source proof.
func cases() []fixtureCase {
	return []fixtureCase{
		{
			id: "support-faq", marker: "password",
			first:             "How do customers reset a forgotten account password?",
			paraphrase:        "How can customers reset a forgotten account password?",
			afterRestart:      "Tell me how customers reset a forgotten account password.",
			changedFact:       "How do employees reset a forgotten account password?",
			changedFactAnswer: "Employees reset their password through the staff console.",
			constraint:        "Write bullet points explaining how customers reset a forgotten account password.",
			answer:            "Customers choose Forgot password on the sign-in page, open their registered email's reset link, and set a new password within 24 hours.",
			evidence:          "Customers with access to their registered email use Forgot password on the sign-in page and the emailed link to set a new password within 24 hours. Employees use the staff console.",
		},
		{
			id: "internal-onboarding", marker: "wiki",
			first:             "How do employees request internal wiki access?",
			paraphrase:        "How can employees request internal wiki access?",
			afterRestart:      "Tell me how employees request internal wiki access.",
			changedFact:       "How do contractors request internal wiki access?",
			changedFactAnswer: "Contractors request wiki access through their project sponsor, followed by Security approval.",
			constraint:        "Write bullet points explaining how employees request internal wiki access.",
			answer:            "Employees submit a wiki access request in the Team Portal for their manager to approve.",
			evidence:          "Employees request standard internal wiki access in the Team Portal, and their manager approves it. Contractors request access through their project sponsor, followed by Security approval.",
		},
		{
			id: "incident-brief", marker: "incident",
			first:             "Summarize database incident INC-17.",
			paraphrase:        "Describe database incident INC-17.",
			afterRestart:      "Explain database incident INC-17.",
			changedFact:       "Summarize database incident INC-18.",
			changedFactAnswer: "Incident INC-18 was a cache failure causing seven minutes of search timeouts, resolved by restarting cache workers.",
			constraint:        "Write bullet points summarizing database incident INC-17.",
			answer:            "Database incident INC-17 was caused by a faulty connection-pool rollout; checkout failed in the West region for 18 minutes before rollback restored service.",
			evidence:          "INC-17 was a database outage caused by a faulty connection-pool rollout. Checkout failed in the West region for 18 minutes, and rollback restored service. INC-18 was a cache failure causing seven minutes of search timeouts and was resolved by restarting cache workers.",
		},
		{
			id: "release-readiness", marker: "commit",
			first:             "Is commit abc123 ready for release?",
			paraphrase:        "Can commit abc123 be considered ready for release?",
			afterRestart:      "Assess commit abc123 readiness for release.",
			changedFact:       "Is commit def456 ready for release?",
			changedFactAnswer: "Commit def456 is not ready for release because integration tests failed.",
			constraint:        "Write bullet points assessing commit abc123 readiness for release.",
			answer:            "Commit abc123 passed unit tests, integration tests, and its current security scan, so it is ready for release.",
			evidence:          "Release requires passing unit tests, integration tests, and a current security scan. Commit abc123 passed all three. Commit def456 failed integration tests. A readiness assessment does not execute a deployment.",
		},
		{
			id: "refund-eligibility", marker: "order",
			first:             "Is order ORD-17 eligible for refund?",
			paraphrase:        "Can order ORD-17 qualify for refund?",
			afterRestart:      "Assess refund eligibility for order ORD-17.",
			changedFact:       "Is order ORD-18 eligible for refund?",
			changedFactAnswer: "Order ORD-18 is ineligible because delivery was 35 days ago, outside the 30-day window.",
			constraint:        "Write bullet points assessing refund eligibility for order ORD-17.",
			answer:            "Order ORD-17 is eligible because it is unused and was delivered 12 days ago, within the 30-day refund window.",
			evidence:          "Unused merchandise qualifies for a standard refund within 30 days of delivery. ORD-17 is unused and was delivered 12 days ago. ORD-18 is unused and was delivered 35 days ago. Neither has an approved exception.",
		},
		{
			id: "shared-broker-lifecycle", marker: "broker",
			first:             "Explain broker historical payload storage and backup requirements.",
			paraphrase:        "Describe broker historical payload storage and backup requirements.",
			afterRestart:      "Summarize broker historical payload storage and backup requirements.",
			changedFact:       "Explain broker deleted payload recovery and backup requirements.",
			changedFactAnswer: "The broker cannot recover deleted archived payloads from a database-only backup when all cold files are lost.",
			constraint:        "Write bullet points explaining broker historical payload storage and backup requirements.",
			answer:            "With local cold retention enabled, the broker moves superseded payloads to trusted local gzip files, retains identities and audit metadata in SQLite, and requires consistent backups of the database and all cold files.",
			evidence:          "Local cold retention is enabled. The broker can move superseded payloads to trusted local gzip files while identities, receipts, and audit history remain in SQLite. Backups need a consistent database and every cold file. A database-only backup cannot recover deleted archive files. These notes authorize no configuration mutation or deletion.",
		},
	}
}

type counters struct {
	generation atomic.Int64
	embedding  atomic.Int64
	verifier   atomic.Int64
}

func (c *counters) work() WorkCounts {
	g, e, v := c.generation.Load(), c.embedding.Load(), c.verifier.Load()
	return WorkCounts{g, g * fixtureGenerationInput, g * fixtureGenerationOutput, e, e * fixtureEmbeddingInput, v, v * fixtureVerifierInput, v * fixtureVerifierOutput}
}

func (w WorkCounts) minus(before WorkCounts) WorkCounts {
	return WorkCounts{w.GenerationCalls - before.GenerationCalls, w.GenerationInputTokens - before.GenerationInputTokens, w.GenerationOutputTokens - before.GenerationOutputTokens, w.EmbeddingCalls - before.EmbeddingCalls, w.EmbeddingInputTokens - before.EmbeddingInputTokens, w.VerifierCalls - before.VerifierCalls, w.VerifierInputTokens - before.VerifierInputTokens, w.VerifierOutputTokens - before.VerifierOutputTokens}
}

type scriptedProvider struct {
	c     fixtureCase
	calls *counters
}

func (scriptedProvider) Identity() string { return "controlled-quest-scripted-provider-v1" }
func (scriptedProvider) CountInputTokens(context.Context, inference.Request) (int64, error) {
	return fixtureGenerationInput, nil
}
func (p scriptedProvider) Generate(ctx context.Context, request inference.Request) (inference.Response, error) {
	if err := ctx.Err(); err != nil {
		return inference.Response{}, err
	}
	p.calls.generation.Add(1)
	answer := p.c.answer
	if strings.HasSuffix(request.Input, "\n\nTask:\n"+p.c.changedFact) {
		answer = p.c.changedFactAnswer
	} else if strings.HasSuffix(request.Input, "\n\nTask:\n"+p.c.constraint) {
		answer = "- " + answer
	}
	return inference.Response{Model: "scripted-fixture", Text: answer, Complete: true, StopReason: "stop", Usage: inference.Usage{InputTokens: fixtureGenerationInput, UncachedInputTokens: fixtureGenerationInput, OutputTokens: fixtureGenerationOutput}}, nil
}

type constantEmbedder struct{ calls *counters }

func (e constantEmbedder) Embed(ctx context.Context, _ string) (semantic.Embedding, error) {
	if err := ctx.Err(); err != nil {
		return semantic.Embedding{}, err
	}
	e.calls.embedding.Add(1)
	tokens := fixtureEmbeddingInput
	return semantic.Embedding{ModelID: digest("controlled-quest-constant-embedding-v1"), Vector: []float32{1, 0, 0}, InputTokens: &tokens}, nil
}

// oracleVerifier uses declared fixture labels. It tests integration and rejection
// control flow; it is deliberately incapable of establishing model accuracy.
type oracleVerifier struct {
	c     fixtureCase
	calls *counters
}

func (oracleVerifier) Identity() string { return "controlled-quest-oracle-verifier-v1" }
func (v oracleVerifier) Verify(ctx context.Context, request semantic.ValidityRequest) (semantic.ValidityResult, error) {
	if err := ctx.Err(); err != nil {
		return semantic.ValidityResult{}, err
	}
	v.calls.verifier.Add(1)
	accepted := request.CachedAnswer == v.c.answer && (request.Question == v.c.paraphrase || request.Question == v.c.afterRestart)
	verdict := "reject"
	if accepted {
		verdict = "accept"
	}
	in, out := fixtureVerifierInput, fixtureVerifierOutput
	return semantic.ValidityResult{Verdict: verdict, SameIntent: accepted, SameFacts: accepted, SameConstraints: accepted, AnswerSupported: accepted, InputTokens: &in, OutputTokens: &out, GenerationCalled: true, Complete: true, Durable: true, ReceiptKey: "fixture-verification-" + digest(request.OriginalQuestion + "\x00" + request.Question)[:32]}, nil
}

func digest(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}

func request(c fixtureCase) delegation.Request {
	return delegation.Request{
		Spec:          inference.Spec{Task: c.id, Revision: "approved-1", Instructions: "Answer the read-only question using only the approved evidence. Honor the requested answer format.", Input: c.first, Context: []inference.ContextItem{{ID: "approved-evidence", Text: c.evidence, Required: true, Stable: true}}},
		Policy:        delegation.Policy{Local: delegation.Tier{ID: "fixture-local", Spec: inference.Spec{Provider: "ollama", Model: "scripted-fixture", MaxContextBytes: 4096, MaxInputTokens: 1024, MaxOutputTokens: 128, ReuseForSeconds: 3600}}},
		Checks:        []evaluation.Check{{ID: "domain-marker", Kind: "contains", Text: c.marker, IgnoreCase: true}},
		SemanticReuse: &semantic.ReuseConfig{Config: semantic.Config{Mode: "controlled", Family: c.id, Domain: "synthetic-controlled-quest", EvidenceRevision: "approved-1", Bindings: map[string]string{"scope": "approved-scope"}, EmbeddingModel: "constant-fixture", MinSimilarity: 0.9, TopK: 8}, ReadOnly: true, Intent: "explain-approved-evidence", VerifierModel: "oracle-fixture"},
	}
}

type environment struct {
	path   string
	store  *store.Store
	server *httptest.Server
	tokens [2]string
}

func (e *environment) open() error {
	s, err := store.Open(e.path)
	if err != nil {
		return err
	}
	e.store = s
	e.server = httptest.NewServer(httpapi.New(s))
	return nil
}

func (e *environment) close() {
	if e.server != nil {
		e.server.Close()
		e.server = nil
	}
	if e.store != nil {
		_ = e.store.Close()
		e.store = nil
	}
}

func (e *environment) router(c fixtureCase, counts *counters, actor int) (delegation.Router, *client.Client, error) {
	broker, err := client.New(e.server.URL, e.tokens[actor])
	if err != nil {
		return delegation.Router{}, nil, err
	}
	provider := scriptedProvider{c: c, calls: counts}
	reuse := &delegation.ControlledReuser{Broker: broker, Index: broker, EmbedderFactory: func(context.Context, semantic.Config) (semantic.Embedder, error) {
		return constantEmbedder{calls: counts}, nil
	}, VerifierFactory: func(context.Context, semantic.ReuseConfig) (semantic.Verifier, error) {
		return oracleVerifier{c: c, calls: counts}, nil
	}}
	return delegation.Router{Factory: func(delegation.Tier) (delegation.Runner, error) {
		return &inference.Runner{Broker: broker, Provider: provider}, nil
	}, Reuse: reuse}, broker, nil
}

func complete(result delegation.Result) bool {
	return result.Status == "completed" && result.Response != nil && len(result.Attempts) == 1 && result.Attempts[0].QualityPassed
}

func fresh(result delegation.Result, work WorkCounts) bool {
	return complete(result) && !result.Attempts[0].Result.Reused && work.GenerationCalls == 1 && work.EmbeddingCalls == 1 && result.SemanticReuse != nil && result.SemanticReuse.Indexed
}

func sourcePinned(result delegation.Result, source inference.ReuseSource) bool {
	if !complete(result) {
		return false
	}
	r := result.Attempts[0].Result
	return r.Reused && r.ReuseKind == "semantic" && r.Resolution != nil && r.Resolution.Source == source && r.ThisRunUsage == (inference.Usage{}) && r.Response.Usage == (inference.Usage{}) && r.GenerationUsage.InputTokens == fixtureGenerationInput && r.AvoidedUsage == r.GenerationUsage
}

func originalSource(ctx context.Context, broker inference.Broker, result delegation.Result) (inference.ReuseSource, error) {
	if !complete(result) {
		return inference.ReuseSource{}, errors.New("fixture original generation did not complete")
	}
	a, err := broker.Get(ctx, "inference/v1/jobs/"+result.Attempts[0].Result.Fingerprint)
	if err != nil {
		return inference.ReuseSource{}, err
	}
	var record struct {
		RequestFingerprint string `json:"request_fingerprint"`
	}
	if err := json.Unmarshal([]byte(a.Content), &record); err != nil {
		return inference.ReuseSource{}, err
	}
	return inference.ReuseSource{Key: a.Key, Version: a.Version, SHA256: digest(a.Content), OutputSHA256: digest(result.Response.Text), RequestFingerprint: record.RequestFingerprint}, nil
}

func durableAlias(ctx context.Context, broker inference.Broker, result delegation.Result, source inference.ReuseSource) bool {
	if !complete(result) {
		return false
	}
	a, err := broker.Get(ctx, "inference/v1/jobs/"+result.Attempts[0].Result.Fingerprint)
	if err != nil {
		return false
	}
	var record struct {
		State      string                     `json:"state"`
		Resolution *inference.ReuseResolution `json:"resolution"`
	}
	return json.Unmarshal([]byte(a.Content), &record) == nil && record.State == "resolved" && record.Resolution != nil && record.Resolution.Source == source
}

func runCase(ctx context.Context, env *environment, c fixtureCase, counts *counters, report *Report) (Row, error) {
	row := Row{ID: c.id, Steps: []Step{}}
	router, broker, err := env.router(c, counts, 0)
	if err != nil {
		return row, err
	}
	run := func(name string, req delegation.Request, check func(delegation.Result, WorkCounts) bool) (delegation.Result, error) {
		before := counts.work()
		result, runErr := router.Run(ctx, req)
		work := counts.work().minus(before)
		report.RequestRuns++
		step := Step{Name: name, Status: result.Status, Work: work, Observation: result.SemanticReuse}
		if len(result.Attempts) == 1 {
			step.Reused, step.ReuseKind = result.Attempts[0].Result.Reused, result.Attempts[0].Result.ReuseKind
		}
		step.Passed = runErr == nil && check(result, work)
		row.Steps = append(row.Steps, step)
		if runErr != nil {
			return result, fmt.Errorf("%s: %w", name, runErr)
		}
		if !step.Passed {
			return result, fmt.Errorf("%s: controlled workflow fixture expectation failed", name)
		}
		return result, nil
	}

	base := request(c)
	first, err := run("first_fresh_and_indexed", base, func(result delegation.Result, work WorkCounts) bool {
		return fresh(result, work) && work.VerifierCalls == 0 && result.Response.Text == c.answer
	})
	if err != nil {
		return row, err
	}
	source, err := originalSource(ctx, broker, first)
	if err != nil {
		return row, err
	}
	partition := first.SemanticReuse.Partition
	if _, err = run("exact_skips_embedding_and_verifier", base, func(result delegation.Result, work WorkCounts) bool {
		return complete(result) && result.Attempts[0].Result.Reused && work == (WorkCounts{}) && result.SemanticReuse != nil && result.SemanticReuse.Status == "skipped_exact"
	}); err != nil {
		return row, err
	}
	report.GenerationAvoidedByExact++
	paraphrase := request(c)
	paraphrase.Spec.Input = c.paraphrase
	if _, err = run("paraphrase_reuses_pinned_source", paraphrase, func(result delegation.Result, work WorkCounts) bool {
		return sourcePinned(result, source) && work.GenerationCalls == 0 && work.EmbeddingCalls == 1 && work.VerifierCalls > 0 && result.SemanticReuse.GenerationAvoided && durableAlias(ctx, broker, result, source)
	}); err != nil {
		return row, err
	}
	report.GenerationAvoidedBySemantic++
	report.HistoricalSourceUsageProxyTokens += fixtureGenerationInput + fixtureGenerationOutput
	if _, err = run("verified_paraphrase_exact_skips_expensive_work", paraphrase, func(result delegation.Result, work WorkCounts) bool {
		return sourcePinned(result, source) && work == (WorkCounts{})
	}); err != nil {
		return row, err
	}
	report.GenerationAvoidedByExact++
	changedFact := request(c)
	changedFact.Spec.Input = c.changedFact
	if _, err = run("same_partition_changed_fact_rejected", changedFact, func(result delegation.Result, work WorkCounts) bool {
		return fresh(result, work) && result.SemanticReuse.Partition == partition && len(result.SemanticReuse.Rejections) > 0 && result.Response.Text == c.changedFactAnswer
	}); err != nil {
		return row, err
	}
	constraint := request(c)
	constraint.Spec.Input = c.constraint
	if _, err = run("same_partition_changed_format_rejected", constraint, func(result delegation.Result, work WorkCounts) bool {
		return fresh(result, work) && result.SemanticReuse.Partition == partition && len(result.SemanticReuse.Rejections) > 0 && result.Response.Text == "- "+c.answer
	}); err != nil {
		return row, err
	}
	contextChanged := request(c)
	contextChanged.Spec.Input = c.paraphrase
	contextChanged.Spec.Context[0].Text += " This evidence includes a newly approved annotation."
	if _, err = run("changed_bound_context_misses", contextChanged, func(result delegation.Result, work WorkCounts) bool {
		return fresh(result, work) && work.VerifierCalls == 0 && result.SemanticReuse.Partition != partition
	}); err != nil {
		return row, err
	}
	evidenceChanged := request(c)
	evidenceChanged.Spec.Input = c.afterRestart + " Explain briefly."
	evidenceChanged.SemanticReuse.EvidenceRevision = "approved-2"
	if _, err = run("changed_bound_evidence_revision_misses", evidenceChanged, func(result delegation.Result, work WorkCounts) bool {
		return fresh(result, work) && work.VerifierCalls == 0 && result.SemanticReuse.Partition != partition
	}); err != nil {
		return row, err
	}
	env.close()
	if err = env.open(); err != nil {
		return row, err
	}
	row.BrokerRestarted = true
	router, broker, err = env.router(c, counts, 1)
	if err != nil {
		return row, err
	}
	afterRestart := request(c)
	afterRestart.Spec.Input = c.afterRestart
	if _, err = run("second_client_reuses_after_database_reopen", afterRestart, func(result delegation.Result, work WorkCounts) bool {
		return sourcePinned(result, source) && work.GenerationCalls == 0 && work.EmbeddingCalls == 1 && work.VerifierCalls > 0 && durableAlias(ctx, broker, result, source)
	}); err != nil {
		return row, err
	}
	report.GenerationAvoidedBySemantic++
	report.HistoricalSourceUsageProxyTokens += fixtureGenerationInput + fixtureGenerationOutput
	row.Passed = true
	return row, nil
}

// Run creates its own temporary SQLite broker and two same-scope clients. No
// external generation, embedding, or verifier endpoint is contacted. The exact-
// only baseline is a declared count of distinct fresh requests, not an executed
// comparison or a measured token-saving claim.
func Run(ctx context.Context) (report Report, err error) {
	fixtures := cases()
	report = Report{Mode: "deterministic_workflow_fixture", Generation: "scripted_fixture", Embedding: "constant_vector_fixture", Verification: "declared_label_oracle_fixture", PhysicalHosts: 1, IsolatedClients: 2, Total: len(fixtures), ReferenceKind: "declared_exact_only_reference_not_executed", Cases: []Row{}, Limitations: []string{"Passing these fixtures establishes only the declared workflow expectations, not model accuracy, calibration, human acceptance, or general semantic equivalence.", "Fixture token counts are declared values. Historical source usage is a proxy and does not measure target generation savings.", "Two authenticated clients share one local broker on one physical host; this is not a multi-host test."}}
	if err = ctx.Err(); err != nil {
		return report, err
	}
	dir, err := os.MkdirTemp("", "questlock-controlled-")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return report, err
	}
	env := &environment{path: filepath.Join(dir, "state.db")}
	if err = env.open(); err != nil {
		return report, err
	}
	defer env.close()
	for i := range env.tokens {
		env.tokens[i], err = env.store.CreateToken(ctx, fmt.Sprintf("fixture-client-%d", i), "controlled-quest")
		if err != nil {
			return report, err
		}
	}
	counts := &counters{}
	defer func() { report.ActualFixtureWork = counts.work() }()
	// Per case: first, paraphrase, changed fact, changed format, changed bound
	// context, changed evidence revision, and a new paraphrase after reopening.
	report.ExactOnlyReferenceCalls = len(fixtures) * 7
	report.ExactOnlyReferenceInputTokens = int64(report.ExactOnlyReferenceCalls) * fixtureGenerationInput
	report.ExactOnlyReferenceOutputTokens = int64(report.ExactOnlyReferenceCalls) * fixtureGenerationOutput
	for _, c := range fixtures {
		row, runErr := runCase(ctx, env, c, counts, &report)
		report.Cases = append(report.Cases, row)
		if runErr != nil {
			return report, fmt.Errorf("controlled quest %s: %w", c.id, runErr)
		}
		if row.Passed {
			report.Passed++
		}
	}
	return report, nil
}
