package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/protocol"
)

func quotaSpec() Spec {
	s := testSpec()
	s.MaxInputTokens, s.MaxOutputTokens = 100, 100
	s.DailyTokenBudget = &TokenBudget{InputTokens: 200, OutputTokens: 200, TotalTokens: 200}
	return s
}

func readTokenLedger(t *testing.T, r Runner, day string) tokenLedger {
	t.Helper()
	a, err := r.Broker.Get(context.Background(), "inference/v1/token-budget/"+day)
	if err != nil {
		t.Fatal(err)
	}
	var ledger tokenLedger
	if err := decodeTokenLedger(a.Content, &ledger); err != nil {
		t.Fatal(err)
	}
	return ledger
}

func TestTokenQuotaFingerprintCompatibilityAndValidation(t *testing.T) {
	s := testSpec()
	before, err := BuildPlan(s)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(s)
	if strings.Contains(string(encoded), "daily_token_budget") {
		t.Fatal("nil quota changed old spec bytes")
	}
	s.DailyTokenBudget = &TokenBudget{InputTokens: 1e12, OutputTokens: 1e12, TotalTokens: 1e12}
	after, err := BuildPlan(s)
	if err != nil || before.Fingerprint != after.Fingerprint {
		t.Fatalf("quota changed request fingerprint: %v", err)
	}
	for _, bad := range []TokenBudget{{}, {1, 1, 0}, {-1, 1, 1}, {1, 1e12 + 1, 1}, {1, 1, math.MaxInt64}} {
		s.DailyTokenBudget = &bad
		if _, err := BuildPlan(s); err == nil {
			t.Fatalf("invalid quota accepted: %+v", bad)
		}
	}
	if _, err := addTokens(TokenBudget{InputTokens: math.MaxInt64}, TokenBudget{InputTokens: 1}); err == nil {
		t.Fatal("overflow accepted")
	}
}

func TestTokenQuotaSettlesInclusiveUsageAndReuseCostsNothing(t *testing.T) {
	r, p := testRunner(t)
	s := quotaSpec()
	first, err := r.Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	ledger := readTokenLedger(t, r, "2030-01-02")
	if len(ledger.Entries) != 1 || ledger.Frozen {
		t.Fatalf("bad ledger: %+v", ledger)
	}
	for _, entry := range ledger.Entries {
		if entry.Reserved != (TokenBudget{100, 100, 200}) || entry.Actual == nil || *entry.Actual != (TokenBudget{100, 25, 125}) {
			t.Fatalf("cache/reasoning counted twice or reservation too small: %+v", entry)
		}
	}
	// Reuse has no new generation even when the current call omits its quota.
	s.DailyTokenBudget = nil
	again, err := r.Run(context.Background(), s)
	if err != nil || !again.Reused || again.Fingerprint != first.Fingerprint || p.calls != 1 {
		t.Fatalf("reused task charged: %+v, %v", again, err)
	}
	s.Revision = "missing-quota-fresh"
	if _, err := r.Run(context.Background(), s); !errors.Is(err, ErrBudgetExceeded) || p.calls != 1 {
		t.Fatalf("omitted policy bypassed quota: %v", err)
	}
	s.DailyTokenBudget = &TokenBudget{200, 200, 201}
	s.Revision = "conflicting-caps"
	if _, err := r.Run(context.Background(), s); err == nil || p.calls != 1 {
		t.Fatal("conflicting caps allowed generation")
	}
	s.DailyTokenBudget = &TokenBudget{200, 200, 200}
	s.Revision = "no-capacity"
	if _, err := r.Run(context.Background(), s); !errors.Is(err, ErrBudgetExceeded) || p.calls != 1 {
		t.Fatal("settled usage erased prior consumption")
	}
}

func TestTokenQuotaEachDimensionAndInitialPolicyRegistration(t *testing.T) {
	for _, caps := range []TokenBudget{{99, 1000, 2000}, {1000, 99, 2000}, {1000, 1000, 199}} {
		t.Run(fmt.Sprint(caps), func(t *testing.T) {
			r, p := testRunner(t)
			s := quotaSpec()
			s.DailyTokenBudget = &caps
			if _, err := r.Run(context.Background(), s); !errors.Is(err, ErrBudgetExceeded) || p.calls != 0 {
				t.Fatalf("reservation bypassed bucket cap: %v", err)
			}
			ledger := readTokenLedger(t, r, "2030-01-02")
			if ledger.Caps != caps || len(ledger.Entries) != 0 {
				t.Fatal("failed first task did not register immutable caps")
			}
			s.DailyTokenBudget = nil
			s.Revision = "try-omitting"
			if _, err := r.Run(context.Background(), s); !errors.Is(err, ErrBudgetExceeded) || p.calls != 0 {
				t.Fatalf("initial rejection could be bypassed by omission: %v", err)
			}
		})
	}
}

type quotaProvider struct {
	calls    atomic.Int64
	generate func(context.Context, Request) (Response, error)
}

func (*quotaProvider) Identity() string                                         { return "synthetic-quota-provider" }
func (*quotaProvider) CountInputTokens(context.Context, Request) (int64, error) { return 100, nil }
func (p *quotaProvider) Generate(ctx context.Context, req Request) (Response, error) {
	p.calls.Add(1)
	return p.generate(ctx, req)
}

func TestTokenQuotaConcurrentReservationsAndScopeAcrossModels(t *testing.T) {
	r, base := testRunner(t)
	entered, release := make(chan struct{}), make(chan struct{})
	p := &quotaProvider{generate: func(context.Context, Request) (Response, error) { close(entered); <-release; return base.response, nil }}
	r.Provider = p
	s := quotaSpec()
	done := make(chan error, 1)
	go func() { _, err := r.Run(context.Background(), s); done <- err }()
	<-entered
	other := s
	other.Model, other.Task = "another-model", "another-task"
	if _, err := r.Run(context.Background(), other); !errors.Is(err, ErrBudgetExceeded) || p.calls.Load() != 1 {
		t.Fatalf("concurrent models exceeded scope cap: %v", err)
	}
	ledger := readTokenLedger(t, r, "2030-01-02")
	for _, e := range ledger.Entries {
		if e.Actual != nil {
			t.Fatal("in-flight reservation prematurely settled")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTokenQuotaUnknownIncompleteAndMidnightSettlement(t *testing.T) {
	for _, mode := range []string{"uncertain", "incomplete", "midnight"} {
		t.Run(mode, func(t *testing.T) {
			r, base := testRunner(t)
			s := quotaSpec()
			var now atomic.Int64
			now.Store(time.Date(2030, 1, 2, 23, 59, 59, 0, time.UTC).Unix())
			r.Now = func() time.Time { return time.Unix(now.Load(), 0) }
			r.Provider = &quotaProvider{generate: func(context.Context, Request) (Response, error) {
				if mode == "uncertain" {
					return Response{}, errors.New("synthetic lost generation acknowledgement")
				}
				if mode == "midnight" {
					now.Add(2)
				}
				response := base.response
				if mode == "incomplete" {
					response.Complete = false
				}
				return response, nil
			}}
			result, err := r.Run(context.Background(), s)
			if mode == "midnight" && err != nil {
				t.Fatal(err)
			}
			if mode != "midnight" && err == nil {
				t.Fatal("failed generation accepted")
			}
			ledger := readTokenLedger(t, r, "2030-01-02")
			for _, e := range ledger.Entries {
				if mode == "uncertain" && e.Actual != nil {
					t.Fatal("unknown released its reservation")
				}
				if mode != "uncertain" && (e.Actual == nil || e.Actual.TotalTokens != 125) {
					t.Fatal("known incomplete/normal usage not charged")
				}
			}
			if mode == "midnight" {
				if _, err := r.Broker.Get(context.Background(), "inference/v1/token-budget/2030-01-03"); !brokerCode(err, "not_found") {
					t.Fatal("settlement created new-day capacity")
				}
				if !result.Durable {
					t.Fatal("midnight result not durable")
				}
			} else {
				want := ErrInFlight
				if mode == "incomplete" {
					want = ErrIncomplete
				}
				if _, err := r.Run(context.Background(), s); !errors.Is(err, want) {
					t.Fatalf("failed attempt retried: %v", err)
				}
			}
		})
	}
}

func TestTokenQuotaOverrunFreezesAndPublicationFailureStillSettles(t *testing.T) {
	for _, rejectResult := range []bool{false, true} {
		t.Run(fmt.Sprint(rejectResult), func(t *testing.T) {
			r, p := testRunner(t)
			s := quotaSpec()
			s.DailyTokenBudget = &TokenBudget{1000, 1000, 2000}
			p.response.Usage = Usage{InputTokens: 101, UncachedInputTokens: 81, CacheReadTokens: 10, CacheWriteTokens: 10, OutputTokens: 25, ReasoningTokens: 5}
			if rejectResult {
				r.Broker = rejectPublishBroker{Broker: r.Broker, suffix: "-result"}
			}
			result, err := r.Run(context.Background(), s)
			if !errors.Is(err, ErrBudgetExceeded) || result.Response.Complete || (rejectResult && result.Durable) {
				t.Fatalf("overrun reusable: %+v, %v", result, err)
			}
			ledger := readTokenLedger(t, r, "2030-01-02")
			if !ledger.Frozen {
				t.Fatal("reported overrun failed to freeze ledger")
			}
			for _, e := range ledger.Entries {
				if e.Actual == nil || e.Actual.InputTokens != 101 {
					t.Fatal("known overrun usage lost")
				}
			}
			s.Revision = "fresh-after-overrun"
			if _, err := r.Run(context.Background(), s); !errors.Is(err, ErrBudgetExceeded) || p.calls != 1 {
				t.Fatalf("frozen scope generated again: %v", err)
			}
		})
	}
}

type tokenFaultBroker struct {
	Broker
	mode     string
	attempts atomic.Int64
}

func (b *tokenFaultBroker) Publish(ctx context.Context, req protocol.PublishRequest) (protocol.PublishResult, error) {
	reserve := strings.Contains(req.OperationID, "-tokens-")
	settle := strings.Contains(req.OperationID, "-token-settle-")
	if b.mode == "contend" && reserve {
		b.attempts.Add(1)
		return protocol.PublishResult{}, &client.Error{Code: "version_conflict"}
	}
	if (b.mode == "lost-reserve" || b.mode == "lost-rollback") && reserve {
		_, _ = b.Broker.Publish(ctx, req)
		return protocol.PublishResult{}, errors.New("synthetic lost reserve acknowledgement")
	}
	if b.mode == "lost-rollback" && settle {
		return protocol.PublishResult{}, errors.New("synthetic unavailable rollback")
	}
	if b.mode == "dollar-rollback" && strings.Contains(req.OperationID, "-settle-") {
		return protocol.PublishResult{}, errors.New("synthetic unavailable dollar rollback")
	}
	return b.Broker.Publish(ctx, req)
}

func TestTokenQuotaDualLedgerRollbackAndAmbiguousAcknowledgements(t *testing.T) {
	for _, mode := range []string{"reject", "lost-reserve", "lost-rollback", "dollar-rollback", "contend"} {
		t.Run(mode, func(t *testing.T) {
			r, p := testRunner(t)
			s := quotaSpec()
			s.Prices = &Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
			s.DailyBudgetUSD = 0.001
			if mode == "reject" || mode == "dollar-rollback" {
				s.DailyTokenBudget = &TokenBudget{99, 100, 199}
			}
			fault := &tokenFaultBroker{Broker: r.Broker, mode: mode}
			r.Broker = fault
			if _, err := r.Run(context.Background(), s); err == nil || p.calls != 0 {
				t.Fatalf("reservation failure generated: %v", err)
			}
			a, err := r.Broker.Get(context.Background(), "inference/v1/budget/2030-01-02")
			if err != nil {
				t.Fatal(err)
			}
			var dollar budgetLedger
			if err := decodeLedger(a.Content, &dollar); err != nil {
				t.Fatal(err)
			}
			for _, n := range dollar.Entries {
				if mode != "dollar-rollback" && n != 0 {
					t.Fatal("known-not-generated dollar capacity leaked")
				}
			}
			if mode == "lost-rollback" || mode == "dollar-rollback" {
				if _, err := r.Run(context.Background(), s); !errors.Is(err, ErrInFlight) {
					t.Fatalf("uncertain rollback retried: %v", err)
				}
			}
			if mode == "lost-reserve" {
				ledger := readTokenLedger(t, r, "2030-01-02")
				for _, e := range ledger.Entries {
					if e.Actual == nil || *e.Actual != (TokenBudget{}) {
						t.Fatal("ambiguous committed reservation not released")
					}
				}
			}
			if mode == "contend" && fault.attempts.Load() != 32 {
				t.Fatalf("CAS work unbounded or skipped: %d", fault.attempts.Load())
			}
		})
	}
}

func TestTokenQuotaMalformedLedgerAndEntryBoundFailClosed(t *testing.T) {
	valid := tokenLedger{Schema: 1, Caps: TokenBudget{1000, 1000, 2000}, Entries: map[string]tokenEntry{"synthetic": {Reserved: TokenBudget{100, 100, 200}}}}
	data, _ := json.Marshal(valid)
	for _, bad := range []string{
		string(data) + " {}",
		strings.Replace(string(data), `"schema":1`, `"schema":1,"schema":1`, 1),
		strings.Replace(string(data), `"caps"`, `"Caps"`, 1),
		strings.Replace(string(data), `"input_tokens":1000`, `"input_tokens":9223372036854775807`, 1),
		strings.Replace(string(data), `"reserved":`, `"actual":{},"reserved":`, 1),
		strings.Replace(string(data), `"schema":1`, `"schema":1,"unrecognized":1`, 1),
		strings.Replace(string(data), `"entries":{`, `"entries":null,"unused":{`, 1),
	} {
		var ledger tokenLedger
		if err := decodeTokenLedger(bad, &ledger); err == nil {
			t.Fatalf("malformed token ledger accepted: %s", bad)
		}
	}
	r, p := testRunner(t)
	s := quotaSpec()
	full := tokenLedger{Schema: 1, Caps: *s.DailyTokenBudget, Entries: map[string]tokenEntry{}}
	zero := TokenBudget{}
	for i := range 1000 {
		full.Entries[fmt.Sprintf("synthetic-%d", i)] = tokenEntry{Reserved: TokenBudget{100, 100, 200}, Actual: &zero}
	}
	if _, err := r.publish(context.Background(), "inference/v1/token-budget/2030-01-02", 0, "seed-full-quota", full); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), s); err == nil || p.calls != 0 {
		t.Fatal("full ledger allowed new reservation")
	}
}

func TestTokenQuotaReservationIDSurvivesReservedRecordFailure(t *testing.T) {
	r, p := testRunner(t)
	r.Broker = rejectPublishBroker{Broker: r.Broker, suffix: "-reserved"}
	result, err := r.Run(context.Background(), quotaSpec())
	if err == nil || p.calls != 0 || result.Measurement == nil || result.Measurement.GenerationCalled {
		t.Fatalf("unpersisted reservation generated: %+v, %v", result, err)
	}
	a, err := r.Broker.Get(context.Background(), "inference/v1/jobs/"+result.Measurement.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	var record jobRecord
	if json.Unmarshal([]byte(a.Content), &record) != nil || record.State != "pending" || record.Reservation == "" {
		t.Fatal("initial pending job lost its recovery ID")
	}
	ledger := readTokenLedger(t, r, "2030-01-02")
	if entry, ok := ledger.Entries[record.Reservation]; !ok || entry.Actual != nil {
		t.Fatal("pending reservation cannot be recovered by job ID")
	}
	if _, err := r.Run(context.Background(), quotaSpec()); !errors.Is(err, ErrInFlight) {
		t.Fatalf("retained reservation retried: %v", err)
	}
}

type tokenReadBroker struct {
	Broker
	reads atomic.Int64
	fail  bool
}

func (b *tokenReadBroker) Get(ctx context.Context, key string) (protocol.Artifact, error) {
	if strings.HasPrefix(key, "inference/v1/token-budget/") {
		b.reads.Add(1)
		if b.fail {
			return protocol.Artifact{}, errors.New("synthetic unavailable token ledger")
		}
	}
	return b.Broker.Get(ctx, key)
}

func TestTokenQuotaFreshOmissionProbeAndReuseReadOverhead(t *testing.T) {
	r, p := testRunner(t)
	probe := &tokenReadBroker{Broker: r.Broker}
	r.Broker = probe
	if _, err := r.Run(context.Background(), testSpec()); err != nil {
		t.Fatal(err)
	}
	if probe.reads.Load() != 1 {
		t.Fatalf("fresh unbudgeted probe count: %d", probe.reads.Load())
	}
	if _, err := r.Run(context.Background(), testSpec()); err != nil {
		t.Fatal(err)
	}
	if probe.reads.Load() != 1 || p.calls != 1 {
		t.Fatal("completed reuse contacted token ledger or generated")
	}
	probe.fail = true
	s := testSpec()
	s.Revision = "fresh-unavailable"
	if _, err := r.Run(context.Background(), s); err == nil || p.calls != 1 {
		t.Fatal("unknown token ledger allowed generation")
	}
	if _, err := r.Run(context.Background(), s); !errors.Is(err, ErrInFlight) {
		t.Fatalf("unconfirmed quota state retried: %v", err)
	}
}
