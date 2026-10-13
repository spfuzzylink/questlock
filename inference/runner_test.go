package inference

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/protocol"
)

type testProvider struct {
	calls, counts int
	input         int64
	response      Response
	err           error
	identity      string
}

func (p *testProvider) Identity() string {
	if p.identity != "" {
		return p.identity
	}
	return "test-provider"
}
func (p *testProvider) CountInputTokens(context.Context, Request) (int64, error) {
	p.counts++
	return p.input, nil
}
func (p *testProvider) Generate(context.Context, Request) (Response, error) {
	p.calls++
	return p.response, p.err
}

func testSpec() Spec {
	return Spec{Task: "handoff", Revision: "commit-1", Provider: "openai", Model: "fixture", Instructions: "Keep it short.", Input: "Summarize current changes.", MaxInputTokens: 1000, MaxOutputTokens: 100, ReuseForSeconds: 3600}
}
func testRunner(t *testing.T) (Runner, *testProvider) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	token, err := s.CreateToken(context.Background(), "runner", "tests")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.New(s))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	p := &testProvider{input: 100, response: Response{ID: "fixture", Model: "fixture", Text: "verified summary", Complete: true, Usage: Usage{InputTokens: 100, UncachedInputTokens: 80, CacheReadTokens: 10, CacheWriteTokens: 10, OutputTokens: 25, ReasoningTokens: 5}}}
	return Runner{Broker: c, Provider: p, Now: func() time.Time { return time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC) }}, p
}

func TestPlanRequiredWholeChunksAndDeterminism(t *testing.T) {
	s := testSpec()
	s.Context = []ContextItem{{ID: "policy", Text: "required", Required: true, Stable: true}, {ID: "archive", Text: strings.Repeat("old", 100), Priority: 1}, {ID: "current", Text: "changed handler", Priority: 2}}
	s.MaxContextBytes = len(renderChunk(s.Context[0])) + len(renderChunk(s.Context[2]))
	p, err := BuildPlan(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Selected) != 2 || len(p.Omitted) != 1 || p.Omitted[0] != "archive" || strings.Contains(p.Request.Input, "oldold") {
		t.Fatalf("bad plan: %+v", p)
	}
	s.Context[0], s.Context[2] = s.Context[2], s.Context[0]
	q, err := BuildPlan(s)
	if err != nil || p.Fingerprint != q.Fingerprint {
		t.Fatalf("context order changed request: %v", err)
	}
	s.MaxContextBytes = 1
	if _, err = BuildPlan(s); err == nil {
		t.Fatal("required context was silently dropped")
	}
}

func TestReuseTTLAndInvalidation(t *testing.T) {
	r, p := testRunner(t)
	s := testSpec()
	ctx := context.Background()
	first, err := r.Run(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Run(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Reused || second.ThisRunUsage != (Usage{}) || second.Response.Usage != (Usage{}) || second.AvoidedUsage != first.GenerationUsage || p.calls != 1 || p.counts != 1 {
		t.Fatalf("reuse regenerated/charged: %+v", second)
	}
	s.Revision = "commit-2"
	if _, err = r.Run(ctx, s); err != nil {
		t.Fatal(err)
	}
	if p.calls != 2 {
		t.Fatal("revision did not invalidate reuse")
	}
	p.identity = "other-upstream"
	if _, err = r.Run(ctx, s); err != nil {
		t.Fatal(err)
	}
	if p.calls != 3 {
		t.Fatal("endpoint identity did not invalidate reuse")
	}
	r.Now = func() time.Time { return first.CreatedAt.Add(2 * time.Hour) }
	if _, err = r.Run(ctx, s); err != nil {
		t.Fatal(err)
	}
	if p.calls != 4 {
		t.Fatal("expired result reused")
	}
}

func TestUnknownOutcomeNeverRegenerates(t *testing.T) {
	r, p := testRunner(t)
	s := testSpec()
	p.err = errors.New("connection lost")
	s.Prices = &Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
	s.DailyBudgetUSD = .0003
	s.MaxInputTokens = 100
	if _, err := r.Run(context.Background(), s); err == nil {
		t.Fatal("unknown outcome accepted")
	}
	if _, err := r.Run(context.Background(), s); !errors.Is(err, ErrInFlight) {
		t.Fatalf("unknown retried: %v", err)
	}
	if p.calls != 1 {
		t.Fatal("duplicated possibly billed generation")
	}
	p.err = nil
	s.Revision = "second-job"
	if _, err := r.Run(context.Background(), s); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("unknown reservation released: %v", err)
	}
	if p.calls != 1 {
		t.Fatal("shared budget allowed excess")
	}
}

func TestCapsBlockBeforeGenerationAndCanBeCorrected(t *testing.T) {
	r, p := testRunner(t)
	s := testSpec()
	s.MaxInputTokens = 99
	if _, err := r.Run(context.Background(), s); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatal(err)
	}
	if p.calls != 0 {
		t.Fatal("generated before cap validation")
	}
	s.MaxInputTokens = 100
	if _, err := r.Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 {
		t.Fatal("preflight blocked request could not recover")
	}
}

func TestIncompleteCannotBecomeReusable(t *testing.T) {
	r, p := testRunner(t)
	s := testSpec()
	p.response.Complete = false
	p.response.StopReason = "max_output_tokens"
	got, err := r.Run(context.Background(), s)
	if !errors.Is(err, ErrIncomplete) || got.Reused || got.ThisRunUsage.OutputTokens != 25 {
		t.Fatalf("missing original partial accounting: %+v %v", got, err)
	}
	got, err = r.Run(context.Background(), s)
	if !errors.Is(err, ErrIncomplete) || got.Reused || got.Response.Text != "" || p.calls != 1 {
		t.Fatalf("partial reused: %+v %v", got, err)
	}
}

type lostReceiptBroker struct {
	Broker
	lost bool
}

func (b *lostReceiptBroker) Publish(ctx context.Context, p protocol.PublishRequest) (protocol.PublishResult, error) {
	result, err := b.Broker.Publish(ctx, p)
	if err == nil && strings.Contains(p.OperationID, "-result") && !b.lost {
		b.lost = true
		return protocol.PublishResult{}, errors.New("lost acknowledgement")
	}
	return result, err
}

func TestLostResultPublicationRecoversExactReceipt(t *testing.T) {
	r, p := testRunner(t)
	b := &lostReceiptBroker{Broker: r.Broker}
	r.Broker = b
	first, err := r.Run(context.Background(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Run(context.Background(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if !b.lost || p.calls != 1 || !second.Reused || first.Response.ID != second.Response.ID {
		t.Fatal("lost publish triggered another generation")
	}
	a, err := r.Broker.Get(context.Background(), "inference/v1/jobs/"+first.Fingerprint)
	if err != nil || a.Version != 2 {
		t.Fatalf("retry created another artifact version: %d %v", a.Version, err)
	}
}

func TestPricingPartitionsDoNotDoubleChargeReasoning(t *testing.T) {
	u := Usage{InputTokens: 100, UncachedInputTokens: 80, CacheReadTokens: 10, CacheWriteTokens: 10, OutputTokens: 25, ReasoningTokens: 5}
	got, err := costMicros(u, Prices{Input: 2, CacheRead: .2, CacheWrite: 2.5, Output: 10})
	if err != nil || got != 437 {
		t.Fatalf("cost = %d %v", got, err)
	}
	r, p := testRunner(t)
	s := testSpec()
	s.Prices = &Prices{Input: 2, CacheRead: .2, CacheWrite: 2.5, Output: 10}
	first, err := r.Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if first.CostUSD == nil || *first.CostUSD != .000437 || second.CostUSD == nil || *second.CostUSD != 0 || second.AvoidedCostUSD == nil || *second.AvoidedCostUSD != .000437 || p.calls != 1 {
		t.Fatalf("bad reused cost accounting: %+v", second)
	}
}

func TestScopeCannotDropExistingBudget(t *testing.T) {
	r, _ := testRunner(t)
	s := testSpec()
	s.Prices = &Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
	s.DailyBudgetUSD = .01
	if _, err := r.Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	s.Revision = "unbudgeted"
	s.DailyBudgetUSD = 0
	s.Prices = nil
	if _, err := r.Run(context.Background(), s); err == nil {
		t.Fatal("omitting budget bypassed existing ledger")
	}
}

func TestEstimatedCountsCannotTurnLimitViolationsIntoCacheHits(t *testing.T) {
	for _, kind := range []string{"input", "cost"} {
		t.Run(kind, func(t *testing.T) {
			r, p := testRunner(t)
			s := testSpec()
			p.response.Usage = Usage{InputTokens: 101, UncachedInputTokens: 101, OutputTokens: 100}
			if kind == "input" {
				s.MaxInputTokens = 100
			} else {
				s.MaxInputTokens = 100
				s.Prices = &Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
				s.MaxCostUSD = .0002
			}
			got, err := r.Run(context.Background(), s)
			if !errors.Is(err, ErrBudgetExceeded) || got.Response.Complete {
				t.Fatalf("actual violation accepted: %+v %v", got, err)
			}
			got, err = r.Run(context.Background(), s)
			if !errors.Is(err, ErrBudgetExceeded) || got.Reused || p.calls != 1 {
				t.Fatalf("cache erased violation: %+v %v", got, err)
			}
		})
	}
}

func TestMeasuredCostOverrunFreezesSharedScope(t *testing.T) {
	r, p := testRunner(t)
	s := testSpec()
	s.MaxInputTokens = 100
	s.Prices = &Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
	s.DailyBudgetUSD = .01
	p.response.Usage = Usage{InputTokens: 101, UncachedInputTokens: 101, OutputTokens: 100}
	got, err := r.Run(context.Background(), s)
	if !errors.Is(err, ErrBudgetExceeded) || got.Response.Complete || !got.Durable {
		t.Fatalf("overrun not saved as violation: %+v %v", got, err)
	}
	s.Revision = "new-job"
	if _, err = r.Run(context.Background(), s); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("overrun did not freeze ledger: %v", err)
	}
	if p.calls != 1 {
		t.Fatal("frozen scope launched generation")
	}
}

type rejectPublishBroker struct {
	Broker
	suffix string
}

func (b rejectPublishBroker) Publish(ctx context.Context, req protocol.PublishRequest) (protocol.PublishResult, error) {
	if strings.Contains(req.OperationID, b.suffix) {
		return protocol.PublishResult{}, errors.New("publication unavailable")
	}
	return b.Broker.Publish(ctx, req)
}

func TestKnownPaidAnswerSurvivesPublicationFailureInMemory(t *testing.T) {
	r, p := testRunner(t)
	r.Broker = rejectPublishBroker{Broker: r.Broker, suffix: "-result"}
	got, err := r.Run(context.Background(), testSpec())
	if err == nil || got.Response.Text != "verified summary" || got.ThisRunUsage.OutputTokens != 25 || got.Durable || got.Reused {
		t.Fatalf("paid answer lost or falsely durable: %+v %v", got, err)
	}
	if _, err = r.Run(context.Background(), testSpec()); !errors.Is(err, ErrInFlight) {
		t.Fatalf("unconfirmed publish allowed regeneration: %v", err)
	}
	if p.calls != 1 {
		t.Fatal("paid call repeated")
	}
}

func TestFailedOverrunSettlementKeepsJobNonreusable(t *testing.T) {
	r, p := testRunner(t)
	s := testSpec()
	s.MaxInputTokens = 100
	s.Prices = &Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
	s.DailyBudgetUSD = .01
	p.response.Usage = Usage{InputTokens: 101, UncachedInputTokens: 101, OutputTokens: 100}
	r.Broker = rejectPublishBroker{Broker: r.Broker, suffix: "-settle-"}
	got, err := r.Run(context.Background(), s)
	if !errors.Is(err, ErrBudgetExceeded) || got.Response.Complete || !got.Durable {
		t.Fatalf("violation hidden by failed settlement: %+v %v", got, err)
	}
	if _, err = r.Run(context.Background(), s); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("cache hid unsettled violation: %v", err)
	}
	if p.calls != 1 {
		t.Fatal("violating request repeated")
	}
}

func TestResultPublicationFailureStillFreezesKnownBudgetOverrun(t *testing.T) {
	r, p := testRunner(t)
	s := testSpec()
	s.MaxInputTokens = 100
	s.Prices = &Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
	s.DailyBudgetUSD = .000401
	p.response.Usage = Usage{InputTokens: 101, UncachedInputTokens: 101, OutputTokens: 100}
	r.Broker = rejectPublishBroker{Broker: r.Broker, suffix: "-result"}
	got, err := r.Run(context.Background(), s)
	if !errors.Is(err, ErrBudgetExceeded) || got.Durable || got.Response.Complete || got.ThisRunUsage.InputTokens != 101 {
		t.Fatalf("publication hid overrun: %+v %v", got, err)
	}
	s.Revision = "second-job"
	if _, err = r.Run(context.Background(), s); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("writable ledger was not frozen: %v", err)
	}
	if p.calls != 1 {
		t.Fatal("unpublished overrun permitted a second paid call")
	}
}

func TestHTTPAdapterOutputOverrunRetainsUsageAndFreezesBudget(t *testing.T) {
	for _, provider := range []string{"openai", "claude"} {
		t.Run(provider, func(t *testing.T) {
			r, _ := testRunner(t)
			s := testSpec()
			s.Provider = provider
			s.Model = "gpt-5.6"
			fixture := openAIResponseFixture
			if provider == "claude" {
				s.Model = "claude-fixture"
				fixture = claudeResponseFixture
			}
			s.MaxInputTokens = 1000
			s.MaxOutputTokens = 64
			s.Prices = &Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
			s.DailyBudgetUSD = .003
			body := mutatedFixture(t, fixture, func(m map[string]any) { m["usage"].(map[string]any)["output_tokens"] = 65 })
			var calls atomic.Int32
			p := testHTTPProvider(t, provider, func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "input_tokens") || strings.HasSuffix(req.URL.Path, "count_tokens") {
					io.WriteString(w, `{"input_tokens":1000}`)
					return
				}
				calls.Add(1)
				io.WriteString(w, body)
			})
			r.Provider = p
			got, err := r.Run(context.Background(), s)
			if !errors.Is(err, ErrBudgetExceeded) || got.Response.Complete || got.ThisRunUsage.OutputTokens != 65 || !got.Durable {
				t.Fatalf("known adapter overrun lost: %+v %v", got, err)
			}
			if _, err = r.Run(context.Background(), s); !errors.Is(err, ErrBudgetExceeded) {
				t.Fatalf("violating result reused: %v", err)
			}
			s.Revision = "another-task"
			if _, err = r.Run(context.Background(), s); !errors.Is(err, ErrBudgetExceeded) {
				t.Fatalf("daily budget not frozen: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatal("frozen budget allowed another paid call")
			}
		})
	}
}
