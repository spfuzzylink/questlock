package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/protocol"
)

func testRequest() Request {
	local := Tier{ID: "compact", Spec: inference.Spec{Provider: "ollama", Model: "qwen3.5:4b", MaxContextBytes: 8192, MaxInputTokens: 8192, MaxOutputTokens: 128, ReuseForSeconds: 3600}}
	next := local
	next.ID, next.Spec.MaxOutputTokens = "balanced", 256
	return Request{Spec: inference.Spec{Task: "release-review", Revision: "snapshot-1", Instructions: "Return only JSON with ship false when a required gate is missing.", Input: "Unit and race passed; container has not run."},
		Policy: Policy{Local: local, Escalation: &next}, Checks: []evaluation.Check{{ID: "expected", Kind: "json_equals", Expected: json.RawMessage(`{"ship":false}`)}}}
}

func knownResult(text string) inference.Result {
	u := inference.Usage{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 20}
	return inference.Result{Fingerprint: "known-snapshot", Response: inference.Response{ID: "fixture", Model: "fixture", Text: text, Complete: true, Usage: u}, Durable: true, GenerationUsage: u, ThisRunUsage: u}
}

type scriptedRunner struct {
	result inference.Result
	err    error
	calls  int
	specs  []inference.Spec
}

func (r *scriptedRunner) Run(_ context.Context, spec inference.Spec) (inference.Result, error) {
	r.calls++
	r.specs = append(r.specs, spec)
	return r.result, r.err
}

func TestKnownQualityFailureEscalatesOnceAndOnlyPassingTextIsReturned(t *testing.T) {
	local := &scriptedRunner{result: knownResult(`{"ship":true}`)}
	next := &scriptedRunner{result: knownResult(`{"ship":false}`)}
	router := Router{Factory: func(tier Tier) (Runner, error) {
		if tier.ID == "compact" {
			return local, nil
		}
		return next, nil
	}}
	result, err := router.Run(context.Background(), testRequest())
	if err != nil || result.Status != "completed" || result.SelectedTier != "balanced" || result.Response == nil || result.Response.Text != `{"ship":false}` || local.calls != 1 || next.calls != 1 || len(result.Attempts) != 2 {
		t.Fatalf("explicit escalation failed: %v %+v", err, result)
	}
	if result.Attempts[0].QualityPassed || !result.Attempts[1].QualityPassed || !result.Attempts[0].Result.Response.Complete || !result.Attempts[0].Result.Durable {
		t.Fatal("protocol completion confused with declared quality")
	}
	for _, attempt := range result.Attempts {
		if attempt.Result.Response.Text != "" || attempt.OutputSHA256 == "" {
			t.Fatal("unapproved attempt text retained or hash missing")
		}
	}
	if local.specs[0].Task == next.specs[0].Task || local.specs[0].Model != "qwen3.5:4b" || next.specs[0].MaxOutputTokens != 256 {
		t.Fatal("tier identity/settings silently collapsed")
	}
}

func TestPassingLocalAnswerDoesNotCreateEscalationRunner(t *testing.T) {
	local := &scriptedRunner{result: knownResult(`{"ship":false}`)}
	factories := 0
	result, err := (Router{Factory: func(Tier) (Runner, error) { factories++; return local, nil }}).Run(context.Background(), testRequest())
	if err != nil || result.SelectedTier != "compact" || factories != 1 || local.calls != 1 || len(result.Attempts) != 1 {
		t.Fatalf("passing local work escalated: %v %+v calls=%d", err, result, factories)
	}
}

func TestBothKnownQualityFailuresStopWithoutRetryingEitherTier(t *testing.T) {
	request := testRequest()
	for _, escalation := range []bool{false, true} {
		request.Policy = testRequest().Policy
		if !escalation {
			request.Policy.Escalation = nil
		}
		calls := 0
		local := &scriptedRunner{result: knownResult(`{"ship":true}`)}
		result, err := (Router{Factory: func(Tier) (Runner, error) { calls++; return local, nil }}).Run(context.Background(), request)
		expected := 1
		if escalation {
			expected = 2
		}
		if !errors.Is(err, ErrQualityFailed) || result.Status != "quality_failed" || result.Response != nil || calls != expected || local.calls != expected {
			t.Fatalf("quality failure retried or promoted: %v %+v", err, result)
		}
	}
}

func TestUnsafeOutcomesNeverEscalateEvenIfChecksPass(t *testing.T) {
	for _, kind := range []string{"pending", "uncertain", "incomplete", "budget_blocked", "publication_unconfirmed", "settlement_unconfirmed", "complete_false", "durable_false", "usage_invalid", "usage_over_cap", "reused_charged", "metadata_uncertain"} {
		t.Run(kind, func(t *testing.T) {
			answer := knownResult(`{"ship":false}`)
			var runErr error
			switch kind {
			case "pending":
				runErr = inference.ErrInFlight
			case "uncertain":
				runErr = errors.New("sensitive-provider-error-body")
			case "incomplete":
				runErr = inference.ErrIncomplete
			case "budget_blocked":
				runErr = inference.ErrBudgetExceeded
			case "publication_unconfirmed", "settlement_unconfirmed":
				runErr = errors.New(kind)
			case "complete_false":
				answer.Response.Complete = false
			case "durable_false":
				answer.Durable = false
			case "usage_invalid":
				answer.GenerationUsage.InputTokens = -1
			case "usage_over_cap":
				answer.GenerationUsage.OutputTokens = 129
				answer.ThisRunUsage, answer.Response.Usage = answer.GenerationUsage, answer.GenerationUsage
			case "reused_charged":
				answer.Reused = true
			case "metadata_uncertain":
				answer.Measurement = &inference.Measurement{Outcome: "uncertain", GenerationCalled: true}
			}
			local := &scriptedRunner{result: answer, err: runErr}
			factories := 0
			result, err := (Router{Factory: func(Tier) (Runner, error) { factories++; return local, nil }}).Run(context.Background(), testRequest())
			if !errors.Is(err, ErrStopped) || result.Status != "stopped" || result.Response != nil || factories != 1 || local.calls != 1 || len(result.Attempts) != 1 {
				t.Fatalf("unsafe escalation/selection: %v %+v calls=%d", err, result, factories)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "sensitive-provider-error-body") || strings.Contains(err.Error(), "sensitive-provider-error-body") {
				t.Fatal("unsafe provider error leaked")
			}
		})
	}
}

func TestEscalationErrorStopsWithoutASelectedAnswerOrRetry(t *testing.T) {
	local := &scriptedRunner{result: knownResult(`{"ship":true}`)}
	next := &scriptedRunner{result: knownResult(`{"ship":false}`), err: errors.New("settlement_unconfirmed")}
	result, err := (Router{Factory: func(tier Tier) (Runner, error) {
		if tier.ID == "compact" {
			return local, nil
		}
		return next, nil
	}}).Run(context.Background(), testRequest())
	if !errors.Is(err, ErrStopped) || result.Status != "stopped" || result.Response != nil || next.calls != 1 || len(result.Attempts) != 2 {
		t.Fatalf("unsafe escalation result selected: %v %+v", err, result)
	}
}

func paidRequest() Request {
	request := testRequest()
	tier := request.Policy.Escalation
	tier.Spec.Provider, tier.Spec.Model = "openai", "explicit-fixture-model"
	tier.Spec.Prices = &inference.Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
	tier.Spec.MaxCostUSD, tier.Spec.DailyBudgetUSD = .02, .1
	return request
}

func TestHostedPolicyNeedsCallerPermissionAndBothExplicitBudgetsBeforeAnyCall(t *testing.T) {
	for _, kind := range []string{"no_permission", "no_prices", "no_per_call_cap", "no_daily_cap", "invalid_prices", "too_small_cap", "silent_base_override", "tier_prompt", "zero_ttl", "duplicate_id", "hosted_local", "invalid_check"} {
		t.Run(kind, func(t *testing.T) {
			request, allow := paidRequest(), true
			switch kind {
			case "no_permission":
				allow = false
			case "no_prices":
				request.Policy.Escalation.Spec.Prices = nil
			case "no_per_call_cap":
				request.Policy.Escalation.Spec.MaxCostUSD = 0
			case "no_daily_cap":
				request.Policy.Escalation.Spec.DailyBudgetUSD = 0
			case "invalid_prices":
				request.Policy.Escalation.Spec.Prices.Output = math.NaN()
			case "too_small_cap":
				request.Policy.Escalation.Spec.MaxCostUSD = .0000001
			case "silent_base_override":
				request.Spec.Model = "ignored-model"
			case "tier_prompt":
				request.Policy.Escalation.Spec.Input = "hidden-input"
			case "zero_ttl":
				request.Policy.Local.Spec.ReuseForSeconds = 0
			case "duplicate_id":
				request.Policy.Escalation.ID = request.Policy.Local.ID
			case "hosted_local":
				request.Policy.Local.Spec.Provider = "openai"
			case "invalid_check":
				request.Checks[0].Kind = "llm_judge"
			}
			factories := 0
			_, err := (Router{AllowPaid: allow, Factory: func(Tier) (Runner, error) { factories++; return nil, nil }}).Run(context.Background(), request)
			if err == nil || factories != 0 {
				t.Fatalf("unauthorized/invalid policy called provider: %v calls=%d", err, factories)
			}
		})
	}
	request := paidRequest()
	if err := Validate(request, true); err != nil {
		t.Fatalf("explicit hosted budgets rejected: %v", err)
	}
	local := &scriptedRunner{result: knownResult(`{"ship":true}`)}
	nextAnswer := knownResult(`{"ship":false}`)
	price := float64(.00012)
	nextAnswer.CostUSD = &price
	next := &scriptedRunner{result: nextAnswer}
	result, err := (Router{AllowPaid: true, Factory: func(tier Tier) (Runner, error) {
		if tier.ID == "compact" {
			return local, nil
		}
		return next, nil
	}}).Run(context.Background(), request)
	if err != nil || result.SelectedTier != "balanced" || next.calls != 1 {
		t.Fatalf("explicit budgeted escalation did not run: %v %+v", err, result)
	}
}

func TestPolicyChecksAndTaskRevisionParticipateInBothTierIdentities(t *testing.T) {
	baseline := testRequest()
	digest := requestDigest(baseline)
	for _, kind := range []string{"task", "revision", "input", "check", "tier_cap", "escalation_model", "reuse_ttl"} {
		changed := testRequest()
		switch kind {
		case "task":
			changed.Spec.Task += "-other"
		case "revision":
			changed.Spec.Revision += "-other"
		case "input":
			changed.Spec.Input += " Another missing gate."
		case "check":
			changed.Checks[0].Expected = json.RawMessage(`{"ship":true}`)
		case "tier_cap":
			changed.Policy.Local.Spec.MaxInputTokens++
		case "escalation_model":
			changed.Policy.Escalation.Spec.Model += "-other"
		case "reuse_ttl":
			changed.Policy.Escalation.Spec.ReuseForSeconds++
		}
		for i, tier := range tiers(baseline.Policy) {
			original := tierSpec(baseline, tier, digest)
			altered := tierSpec(changed, tiers(changed.Policy)[i], requestDigest(changed))
			if original.Task == altered.Task || original.Revision == altered.Revision {
				t.Fatalf("policy identity unchanged by %s tier %d", kind, i)
			}
		}
	}
	copy := testRequest()
	if digest != requestDigest(copy) {
		t.Fatal("identical repeat policy lost stable identity")
	}
}

type storedBroker struct {
	store     *store.Store
	principal store.Principal
}

func (b storedBroker) Get(ctx context.Context, key string) (protocol.Artifact, error) {
	artifact, err := b.store.Get(ctx, b.principal, key)
	return artifact, clientError(err)
}

func (b storedBroker) Publish(ctx context.Context, request protocol.PublishRequest) (protocol.PublishResult, error) {
	result, err := b.store.Publish(ctx, b.principal, request)
	return result, clientError(err)
}

func clientError(err error) error {
	var expected *store.Error
	if errors.As(err, &expected) {
		return &client.Error{Code: expected.Code, Message: expected.Message, CurrentVersion: expected.CurrentVersion}
	}
	return err
}

type fixtureProvider struct {
	response    inference.Response
	identity    string
	err         error
	generations int
	countCalls  int
}

func (p *fixtureProvider) Identity() string { return p.identity }
func (p *fixtureProvider) CountInputTokens(context.Context, inference.Request) (int64, error) {
	p.countCalls++
	return p.response.Usage.InputTokens, nil
}
func (p *fixtureProvider) Generate(context.Context, inference.Request) (inference.Response, error) {
	p.generations++
	return p.response, p.err
}

func testBroker(t *testing.T) storedBroker {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	token, err := db.CreateToken(context.Background(), "delegation", "tests")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	return storedBroker{store: db, principal: principal}
}

func TestNativeUnknownGenerationOutcomeCannotEscalateOrRegenerate(t *testing.T) {
	broker := testBroker(t)
	provider := &fixtureProvider{response: knownResult(`{"ship":true}`).Response, identity: "unknown-fixture", err: errors.New("upstream outcome lost")}
	factories := 0
	router := Router{Factory: func(tier Tier) (Runner, error) {
		factories++
		if tier.ID != "compact" {
			t.Fatal("unknown local outcome escalated")
		}
		return inference.Runner{Broker: broker, Provider: provider}, nil
	}}
	for run := 0; run < 2; run++ {
		result, err := router.Run(context.Background(), testRequest())
		if !errors.Is(err, ErrStopped) || result.Response != nil || result.Status != "stopped" || len(result.Attempts) != 1 {
			t.Fatalf("unknown outcome promoted/retried: %v %+v", err, result)
		}
	}
	if factories != 2 || provider.generations != 1 || provider.countCalls != 1 {
		t.Fatalf("unknown generation duplicated: factory%d generate%d count%d", factories, provider.generations, provider.countCalls)
	}
}

func TestNativeHostedBudgetRejectionStopsBeforeGeneration(t *testing.T) {
	broker := testBroker(t)
	request := paidRequest()
	request.Policy.Escalation.Spec.MaxCostUSD = .000001
	local := &fixtureProvider{response: knownResult(`{"ship":true}`).Response, identity: "budget-local-fixture"}
	next := &fixtureProvider{response: knownResult(`{"ship":false}`).Response, identity: "budget-hosted-fixture"}
	router := Router{AllowPaid: true, Factory: func(tier Tier) (Runner, error) {
		provider := local
		if tier.ID == "balanced" {
			provider = next
		}
		return inference.Runner{Broker: broker, Provider: provider}, nil
	}}
	result, err := router.Run(context.Background(), request)
	if !errors.Is(err, ErrStopped) || !errors.Is(err, inference.ErrBudgetExceeded) || result.Status != "stopped" || result.Response != nil || len(result.Attempts) != 2 || local.generations != 1 || next.generations != 0 || next.countCalls != 1 {
		t.Fatalf("budget rejection generated/selected: %v %+v counts=%d generation=%d", err, result, next.countCalls, next.generations)
	}
}

func TestRepeatedPolicyReusesBothDurableJobsWithoutRegeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	token, err := db.CreateToken(context.Background(), "delegation", "tests")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	broker := storedBroker{store: db, principal: principal}
	local := &fixtureProvider{response: knownResult(`{"ship":true}`).Response, identity: "local-fixture"}
	next := &fixtureProvider{response: knownResult(`{"ship":false}`).Response, identity: "next-fixture"}
	router := Router{Factory: func(tier Tier) (Runner, error) {
		provider := local
		if tier.ID == "balanced" {
			provider = next
		}
		return inference.Runner{Broker: broker, Provider: provider}, nil
	}}
	for run := 0; run < 2; run++ {
		if run == 1 {
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			principal, err = db.Authenticate(context.Background(), token)
			if err != nil {
				t.Fatal(err)
			}
			broker = storedBroker{store: db, principal: principal}
		}
		result, err := router.Run(context.Background(), testRequest())
		if err != nil || result.SelectedTier != "balanced" || len(result.Attempts) != 2 {
			t.Fatalf("policy run %d failed: %v %+v", run, err, result)
		}
		for _, attempt := range result.Attempts {
			if attempt.Result.Reused != (run == 1) || (run == 1 && attempt.Result.ThisRunUsage != (inference.Usage{})) || attempt.Result.Response.Text != "" {
				t.Fatalf("repeat regenerated or leaked: %+v", attempt)
			}
		}
	}
	if local.generations != 1 || next.generations != 1 || local.countCalls != 1 || next.countCalls != 1 {
		t.Fatalf("duplicate model work: local %d/%d next %d/%d", local.generations, local.countCalls, next.generations, next.countCalls)
	}
}
