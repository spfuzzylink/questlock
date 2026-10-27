package delegation

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/semantic"
)

// These are explicit application contracts, not semantic-equivalence labels.
// Every nonlisted question must take the normal fresh-work path even if it
// looks similar. Scripted fresh outputs only identify that path; they do not
// establish the quality of an answer to an unapproved question.
type approvedIntegrationCase struct {
	family    string
	tenant    string
	evidence  string
	canonical string
	forms     [2]string
	answer    string
	unknown   [6]string
}

func approvedIntegrationCases() []approvedIntegrationCase {
	return []approvedIntegrationCase{
		{
			family: "support-faq", tenant: "astrel",
			evidence:  "Astrel's CSV export request SV-615 is pending. Support owner Zela Knox handles it. Each report is capped at 23,400 rows and its download link lasts 31 minutes.",
			canonical: "Explain Astrel's pending CSV export, naming its support owner, row cap, and download-link duration.",
			forms: [2]string{
				"Give Astrel the full support summary for its queued CSV download, including who owns it and both limits.",
				"What should the customer know about SV-615's current state, responsible specialist, maximum rows, and link lifetime?",
			},
			answer: `{"tenant":"astrel","state":"pending","owner":"Zela Knox","row_cap":23400,"link_minutes":31}`,
			unknown: [6]string{
				"Explain Astrel's pending CSV export with Runi Vale as the support owner, including its limits.",
				"Explain Astrel's completed CSV export and give its completion receipt and owner.",
				"Explain why Astrel is not permitted to export CSV reports.",
				"Explain the pending export under a new cap of 24,600 rows, with the other facts unchanged.",
				"Create Astrel's CSV export now and return the download URL.",
				"Return Astrel's full pending-export overview only as YAML, with no JSON.",
			},
		},
		{
			family: "onboarding", tenant: "northfenn",
			evidence:  "Northfenn employee Ena Ko has a pending training-access review. Steward Derin Voss owns approval. A failed exercise requires an 18-hour wait before another attempt. Production access is not yet enabled.",
			canonical: "Summarize Ena Ko's pending onboarding review, its steward, retry delay, and production-access state.",
			forms: [2]string{
				"Give the complete access-onboarding status for Northfenn's new hire, including approval ownership and the exercise retry rule.",
				"Who handles Ena's training review, what is still pending, how long must a retry wait, and is live access enabled?",
			},
			answer: `{"tenant":"northfenn","employee":"Ena Ko","review":"pending","steward":"Derin Voss","retry_hours":18,"production_access":false}`,
			unknown: [6]string{
				"Summarize Ena Ko's onboarding with Sela Tenn replacing Derin Voss as steward.",
				"Summarize Ena Ko's completed onboarding review and the enabled production access.",
				"Explain the policy that no exercise retry is allowed after failure.",
				"Summarize onboarding with a revised 26-hour retry wait.",
				"Approve Ena Ko's review and create production access now.",
				"Return the full onboarding status as CSV only, with a header and one row.",
			},
		},
		{
			family: "incident-response", tenant: "cobaltine",
			evidence:  "Cobaltine incident IR-482 is pending resolution. Commander Yara Penn moved 16% of read-only traffic from bay-3 to sand-9. Customer writes were not moved. The cause is unconfirmed.",
			canonical: "Brief the team on IR-482's pending state, commander, read-routing mitigation, write scope, and cause confidence.",
			forms: [2]string{
				"Give the full handoff for Cobaltine's unresolved event, including who leads it and what traffic changed.",
				"Explain the current incident status, response owner, source and destination of the read shift, write handling, and what is known of the cause.",
			},
			answer: `{"tenant":"cobaltine","incident":"IR-482","state":"pending","commander":"Yara Penn","read_percent":16,"from":"bay-3","to":"sand-9","writes_moved":false,"cause":"unconfirmed"}`,
			unknown: [6]string{
				"Brief the team on IR-482 with Nilo Renn as commander instead of Yara Penn.",
				"Brief the team on IR-482's completed resolution and confirmed root cause.",
				"Explain why no read traffic was moved during IR-482.",
				"Brief the team using a corrected read-routing share of 29%.",
				"Post the incident resolution to the customer status page now and return its URL.",
				"Output the complete incident brief only as a Markdown table, without JSON.",
			},
		},
		{
			family: "release-readiness", tenant: "trellisdock",
			evidence:  "TrellisDock candidate 2.8.19 has 47 of 49 required gates passing. Security review is pending with owner Milo Senn. Public release is blocked until all gates pass.",
			canonical: "Explain candidate 2.8.19's readiness, gate counts, pending security owner, and public-release condition.",
			forms: [2]string{
				"Give the full go/no-go note for TrellisDock's current candidate, naming the reviewer and remaining release boundary.",
				"What is the shipping status for 2.8.19, how many gates pass, who owns the unfinished security review, and when may it go public?",
			},
			answer: `{"tenant":"trellisdock","candidate":"2.8.19","passed":47,"required":49,"security":"pending","owner":"Milo Senn","public_release":"blocked-until-all-gates-pass"}`,
			unknown: [6]string{
				"Explain candidate 2.8.19's readiness with Nera Vale now owning security review.",
				"Explain candidate 2.8.19 after security review and all 49 gates have completed successfully.",
				"Explain why security review is not required before this public release.",
				"Explain readiness using an updated result of 48 passing gates out of 49.",
				"Publish candidate 2.8.19 to the public release channel now and return the artifact URL.",
				"Return the full readiness note only as YAML, not JSON.",
			},
		},
		{
			family: "refunds", tenant: "petrilamp",
			evidence:  "Petrilamp order PL-963 has an approved $84.35 card refund consisting of $79.15 merchandise and $5.20 shipping. Specialist Rai Venn owns submission. Payout is pending; no refund has been submitted. It must use the original card.",
			canonical: "Explain PL-963's approved refund amount and components, submission owner, pending state, and payment destination.",
			forms: [2]string{
				"Give the complete customer refund status for Petrilamp's order, including the charge breakdown and who must submit it.",
				"What money is approved for PL-963, who handles it, has it been sent yet, and where must it be paid?",
			},
			answer: `{"tenant":"petrilamp","order":"PL-963","approved":84.35,"merchandise":79.15,"shipping":5.20,"owner":"Rai Venn","state":"pending","destination":"original-card"}`,
			unknown: [6]string{
				"Explain PL-963's refund with Teva Noll replacing Rai Venn as submission owner.",
				"Explain PL-963's completed refund, including the submitted processor confirmation.",
				"Explain why PL-963 is not eligible for any refund.",
				"Explain a revised approval of $94.35 for PL-963.",
				"Issue PL-963's $84.35 refund to the original card now and return the confirmation.",
				"Return the complete refund explanation as one CSV row with a header, not JSON.",
			},
		},
		{
			family: "broker-lifecycle", tenant: "thistle-relay",
			evidence:  "Thistle Relay ticket TR-581 is owned by Tali Orr. The provider outcome is unknown, reconciliation is pending, and 2,317 work units remain reserved. No automatic retry or release is permitted before reconciliation.",
			canonical: "Explain TR-581's owner, pending reconciliation, provider uncertainty, held units, and retry/release restriction.",
			forms: [2]string{
				"Give the full broker-ticket handoff for Thistle Relay, including claim ownership, the unsettled outcome, and the reservation boundary.",
				"Who owns TR-581, what remains unresolved, how many units are held, and what must wait for reconciliation?",
			},
			answer: `{"tenant":"thistle-relay","ticket":"TR-581","owner":"Tali Orr","provider":"unknown","reconciliation":"pending","reserved_units":2317,"automatic_retry":false,"release_before_reconciliation":false}`,
			unknown: [6]string{
				"Explain TR-581 with Bora Senn as its owner instead of Tali Orr.",
				"Explain TR-581's completed reconciliation and settled provider outcome.",
				"Explain why TR-581 has no reserved work units.",
				"Explain TR-581 with an updated reservation of 2,417 units.",
				"Release all TR-581 reserved units now and return the settlement receipt.",
				"Return the entire broker-ticket handoff only as a Markdown table, not JSON.",
			},
		},
	}
}

func approvedIntegrationRequest(c approvedIntegrationCase) Request {
	return Request{
		Spec: inference.Spec{
			Task: "approved-forms-" + c.family, Revision: "facts-r19",
			Instructions: "Use the approved tenant snapshot for the complete read-only overview. Return JSON. Do not perform external actions.",
			Input:        c.canonical,
			Context:      []inference.ContextItem{{ID: "tenant-facts", Text: c.evidence, Required: true, Stable: true}},
		},
		Policy: Policy{Local: Tier{ID: "local", Spec: inference.Spec{
			Provider: "ollama", Model: "offline-scripted", MaxContextBytes: 8192,
			MaxInputTokens: 512, MaxOutputTokens: 128, ReuseForSeconds: 3600,
		}}},
		Checks: []evaluation.Check{{ID: "json-output", Kind: "json_valid"}},
	}
}

func approvedIntegrationSet(c approvedIntegrationCase) ApprovedQuestionSet {
	return ApprovedQuestionSet{
		ReadOnly: true, Revision: "app-review-r7", Intent: c.family + "-overview",
		Bindings:          map[string]string{"tenant": c.tenant, "snapshot": "facts-r19"},
		CanonicalQuestion: c.canonical,
		Forms:             []string{c.canonical, c.forms[0], c.forms[1]},
	}
}

func approvedIntegrationClients(t *testing.T) []*client.Client {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "approved-forms.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	server := httptest.NewServer(httpapi.New(db))
	t.Cleanup(server.Close)
	clients := make([]*client.Client, 3)
	for i, scope := range []string{"approved-contracts", "approved-contracts", "other-contracts"} {
		token, err := db.CreateToken(t.Context(), fmt.Sprintf("approved-worker-%d", i), scope)
		if err != nil {
			t.Fatal(err)
		}
		clients[i], err = client.New(server.URL, token)
		if err != nil {
			t.Fatal(err)
		}
	}
	return clients
}

type approvedIntegrationProvider struct {
	identity    string
	text        string
	err         error
	started     chan struct{}
	release     <-chan struct{}
	counts      atomic.Int32
	generations atomic.Int32
}

func (p *approvedIntegrationProvider) Identity() string { return p.identity }
func (p *approvedIntegrationProvider) CountInputTokens(context.Context, inference.Request) (int64, error) {
	p.counts.Add(1)
	return 100, nil
}
func (p *approvedIntegrationProvider) Generate(ctx context.Context, _ inference.Request) (inference.Response, error) {
	if p.generations.Add(1) == 1 && p.release != nil {
		close(p.started)
		select {
		case <-p.release:
		case <-ctx.Done():
			return inference.Response{}, ctx.Err()
		}
	}
	if p.err != nil {
		return inference.Response{}, p.err
	}
	return knownResult(p.text).Response, nil
}

type approvedIntegrationOptionalCalls struct {
	embeddings atomic.Int32
	verifiers  atomic.Int32
}

func (c *approvedIntegrationOptionalCalls) assertZero(t *testing.T) {
	t.Helper()
	if c.embeddings.Load() != 0 || c.verifiers.Load() != 0 {
		t.Fatalf("explicit approved forms invoked optional model work: embeddings=%d verifiers=%d", c.embeddings.Load(), c.verifiers.Load())
	}
}

func approvedIntegrationRouter(broker *client.Client, provider *approvedIntegrationProvider, optional *approvedIntegrationOptionalCalls, now func() time.Time) Router {
	embedder := func(context.Context, semantic.Config) (semantic.Embedder, error) {
		optional.embeddings.Add(1)
		return nil, errors.New("unexpected embedding work in explicit question contract")
	}
	return Router{
		Factory: func(Tier) (Runner, error) {
			return &inference.Runner{Broker: broker, Provider: provider, Now: now}, nil
		},
		Shadow: &ShadowObserver{Broker: broker, Index: broker, EmbedderFactory: embedder},
		Reuse: &ControlledReuser{
			Broker: broker, Index: broker, EmbedderFactory: embedder,
			VerifierFactory: func(context.Context, semantic.ReuseConfig) (semantic.Verifier, error) {
				optional.verifiers.Add(1)
				return nil, errors.New("unexpected verifier work in explicit question contract")
			},
		},
	}
}

func approvedIntegrationApply(t *testing.T, raw Request, approved ApprovedQuestionSet, question string, wantMatch bool) Request {
	t.Helper()
	raw.Spec.Input = question
	prepared, decision, err := ApplyApprovedQuestions(raw, approved)
	if err != nil || decision.Matched != wantMatch {
		t.Fatalf("approved form decision: match=%v want=%v error=%v", decision.Matched, wantMatch, err)
	}
	if !wantMatch {
		if !reflect.DeepEqual(prepared, raw) || !reflect.DeepEqual(decision, ApprovedQuestionDecision{}) {
			t.Fatal("unlisted question changed the request or acquired an approved decision")
		}
	} else if prepared.Spec.Input != approved.CanonicalQuestion || prepared.Spec.Revision == raw.Spec.Revision || decision.PolicySHA256 == "" {
		t.Fatal("approved question did not acquire the canonical input and a distinct versioned contract")
	}
	return prepared
}

func approvedIntegrationCompleted(t *testing.T, ctx context.Context, router Router, request Request, text string, reused bool) Result {
	t.Helper()
	result, err := router.Run(ctx, request)
	if err != nil || result.Status != "completed" || result.Response == nil || result.Response.Text != text || len(result.Attempts) != 1 {
		t.Fatalf("scripted integration run did not complete: %+v %v", result, err)
	}
	answer := result.Attempts[0].Result
	if answer.Reused != reused || answer.Fingerprint == "" || !answer.Durable {
		t.Fatalf("unexpected durable reuse state: %+v", answer)
	}
	if reused && answer.ThisRunUsage != (inference.Usage{}) {
		t.Fatalf("exact replay charged new model usage: %+v", answer)
	}
	if !reused && answer.ThisRunUsage == (inference.Usage{}) {
		t.Fatal("fresh scripted work lost its measured usage")
	}
	return result
}

func TestApprovedQuestionsBusinessContractsUseExactBrokerReplay(t *testing.T) {
	for _, business := range approvedIntegrationCases() {
		t.Run(business.family, func(t *testing.T) {
			clients := approvedIntegrationClients(t)
			provider := &approvedIntegrationProvider{identity: "approved-business-fixture", text: business.answer}
			optional := &approvedIntegrationOptionalCalls{}
			router := approvedIntegrationRouter(clients[0], provider, optional, nil)
			peerRouter := approvedIntegrationRouter(clients[1], provider, optional, nil)
			raw, policy := approvedIntegrationRequest(business), approvedIntegrationSet(business)
			canonical := approvedIntegrationApply(t, raw, policy, business.canonical, true)
			seed := approvedIntegrationCompleted(t, t.Context(), router, canonical, business.answer, false)
			fingerprint := seed.Attempts[0].Result.Fingerprint
			key := "inference/v1/jobs/" + fingerprint
			source, err := clients[1].Get(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			for _, form := range business.forms {
				prepared := approvedIntegrationApply(t, raw, policy, form, true)
				if prepared.Spec.Revision != canonical.Spec.Revision {
					t.Fatal("approved forms did not share one versioned contract")
				}
				replay := approvedIntegrationCompleted(t, t.Context(), peerRouter, prepared, business.answer, true)
				if replay.Attempts[0].Result.Fingerprint != fingerprint || provider.generations.Load() != 1 || provider.counts.Load() != 1 {
					t.Fatal("approved form did not replay the original exact artifact without provider work")
				}
			}
			approvedIntegrationCompleted(t, t.Context(), router, canonical, business.answer, true)
			provider.text = `{"fresh_question_required":true}`
			for i, question := range business.unknown {
				t.Run([]string{"owner", "completed", "negation", "quantity", "action", "format"}[i], func(t *testing.T) {
					prepared := approvedIntegrationApply(t, raw, policy, question, false)
					before := provider.generations.Load()
					fresh := approvedIntegrationCompleted(t, t.Context(), router, prepared, provider.text, false)
					if provider.generations.Load() != before+1 || fresh.Attempts[0].Result.Fingerprint == fingerprint {
						t.Fatal("unlisted nearby question replayed the canonical artifact")
					}
				})
			}
			// Unapproved requests must neither replace nor mutate the canonical source.
			provider.text = business.answer
			last := approvedIntegrationApply(t, raw, policy, business.forms[0], true)
			approvedIntegrationCompleted(t, t.Context(), peerRouter, last, business.answer, true)
			after, err := clients[0].Get(t.Context(), key)
			if err != nil || after.Version != source.Version || after.Content != source.Content {
				t.Fatal("nearby fresh work changed the canonical broker source", err)
			}
			if provider.generations.Load() != 7 || provider.counts.Load() != 7 {
				t.Fatalf("unexpected model work: generations=%d counts=%d", provider.generations.Load(), provider.counts.Load())
			}
			optional.assertZero(t)
		})
	}
}

func TestApprovedQuestionsPreserveFullContractAndBrokerBoundaries(t *testing.T) {
	for _, drift := range []string{"policy-revision", "intent", "binding", "task-revision", "instructions", "context", "checks", "runtime", "scope", "ttl", "input-budget"} {
		t.Run(drift, func(t *testing.T) {
			business := approvedIntegrationCases()[4]
			clients := approvedIntegrationClients(t)
			provider := &approvedIntegrationProvider{identity: "approved-drift-runtime-a", text: business.answer}
			optional := &approvedIntegrationOptionalCalls{}
			now := time.Date(2038, 4, 17, 10, 20, 0, 0, time.UTC)
			clock := func() time.Time { return now }
			router := approvedIntegrationRouter(clients[0], provider, optional, clock)
			raw, policy := approvedIntegrationRequest(business), approvedIntegrationSet(business)
			seedRequest := approvedIntegrationApply(t, raw, policy, business.canonical, true)
			seed := approvedIntegrationCompleted(t, t.Context(), router, seedRequest, business.answer, false)
			changed, changedPolicy := approvedIntegrationRequest(business), approvedIntegrationSet(business)
			switch drift {
			case "policy-revision":
				changedPolicy.Revision = "app-review-r8"
			case "intent":
				changedPolicy.Intent = "refund-audit-overview"
			case "binding":
				changedPolicy.Bindings["snapshot"] = "facts-r20"
			case "task-revision":
				changed.Spec.Revision = "facts-r20"
			case "instructions":
				changed.Spec.Instructions += " Include the approved component names."
			case "context":
				changed.Spec.Context[0].Text += " Audit note: this is the revised reviewed snapshot."
			case "checks":
				changed.Checks = append(changed.Checks, evaluation.Check{ID: "refund-owner", Kind: "contains", Text: "Rai Venn"})
			case "runtime":
				provider.identity = "approved-drift-runtime-b"
			case "scope":
				router = approvedIntegrationRouter(clients[2], provider, optional, clock)
			case "ttl":
				now = seed.Attempts[0].Result.CreatedAt.Add(2 * time.Hour)
			case "input-budget":
				changed.Policy.Local.Spec.MaxInputTokens = 99
			}
			prepared := approvedIntegrationApply(t, changed, changedPolicy, business.forms[1], true)
			if drift == "input-budget" {
				result, err := router.Run(t.Context(), prepared)
				if !errors.Is(err, ErrStopped) || !errors.Is(err, inference.ErrBudgetExceeded) || result.Response != nil || provider.generations.Load() != 1 {
					t.Fatalf("approved form bypassed changed input budget: %+v %v", result, err)
				}
			} else {
				fresh := approvedIntegrationCompleted(t, t.Context(), router, prepared, business.answer, false)
				if provider.generations.Load() != 2 || provider.counts.Load() != 2 {
					t.Fatal("contract or broker-boundary drift replayed old model work")
				}
				if drift != "ttl" && drift != "scope" && fresh.Attempts[0].Result.Fingerprint == seed.Attempts[0].Result.Fingerprint {
					t.Fatal("changed contract or runtime kept the old inference identity")
				}
			}
			optional.assertZero(t)
		})
	}
}

func TestApprovedQuestionsCannotEscapeExistingTokenQuota(t *testing.T) {
	business := approvedIntegrationCases()[4]
	clients := approvedIntegrationClients(t)
	provider := &approvedIntegrationProvider{identity: "approved-quota-fixture", text: business.answer}
	optional := &approvedIntegrationOptionalCalls{}
	router := approvedIntegrationRouter(clients[0], provider, optional, nil)
	raw, policy := approvedIntegrationRequest(business), approvedIntegrationSet(business)
	raw.Policy.Local.Spec.MaxInputTokens = 100
	raw.Policy.Local.Spec.MaxOutputTokens = 20
	raw.Policy.Local.Spec.DailyTokenBudget = &inference.TokenBudget{InputTokens: 100, OutputTokens: 20, TotalTokens: 120}
	seed := approvedIntegrationApply(t, raw, policy, business.canonical, true)
	approvedIntegrationCompleted(t, t.Context(), router, seed, business.answer, false)
	for _, form := range business.forms {
		prepared := approvedIntegrationApply(t, raw, policy, form, true)
		approvedIntegrationCompleted(t, t.Context(), router, prepared, business.answer, true)
	}
	unknown := approvedIntegrationApply(t, raw, policy, business.unknown[3], false)
	result, err := router.Run(t.Context(), unknown)
	if !errors.Is(err, ErrStopped) || !errors.Is(err, inference.ErrBudgetExceeded) || result.Response != nil || provider.generations.Load() != 1 {
		t.Fatalf("unlisted form escaped the consumed shared token quota: %+v %v", result, err)
	}
	policy.Revision = "app-review-r8"
	newContract := approvedIntegrationApply(t, raw, policy, business.forms[0], true)
	result, err = router.Run(t.Context(), newContract)
	if !errors.Is(err, ErrStopped) || !errors.Is(err, inference.ErrBudgetExceeded) || result.Response != nil || provider.generations.Load() != 1 {
		t.Fatalf("new approved contract escaped the existing token quota: %+v %v", result, err)
	}
	approvedIntegrationCompleted(t, t.Context(), router, seed, business.answer, true)
	optional.assertZero(t)
}

func TestApprovedQuestionsKeepPendingCanonicalClaimFencedAcrossForms(t *testing.T) {
	business := approvedIntegrationCases()[5]
	clients := approvedIntegrationClients(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	producer := &approvedIntegrationProvider{identity: "approved-pending-fixture", text: business.answer, started: make(chan struct{}), release: release}
	peer := &approvedIntegrationProvider{identity: producer.identity, text: business.answer}
	optional := &approvedIntegrationOptionalCalls{}
	producerRouter := approvedIntegrationRouter(clients[0], producer, optional, nil)
	peerRouter := approvedIntegrationRouter(clients[1], peer, optional, nil)
	raw, policy := approvedIntegrationRequest(business), approvedIntegrationSet(business)
	canonical := approvedIntegrationApply(t, raw, policy, business.canonical, true)
	done := controlledWaitRun(ctx, producerRouter, canonical)
	controlledWaitSignal(t, ctx, producer.started)
	for _, form := range business.forms {
		prepared := approvedIntegrationApply(t, raw, policy, form, true)
		result, err := peerRouter.Run(ctx, prepared)
		if !errors.Is(err, ErrStopped) || !errors.Is(err, inference.ErrInFlight) || result.Response != nil || peer.generations.Load() != 0 || peer.counts.Load() != 0 {
			t.Fatalf("approved form bypassed a pending canonical claim: %+v %v", result, err)
		}
	}
	unblock()
	first := controlledWaitResult(t, ctx, done)
	if first.err != nil || first.result.Response == nil || producer.generations.Load() != 1 {
		t.Fatalf("original claimed work could not finish: %+v %v", first.result, first.err)
	}
	prepared := approvedIntegrationApply(t, raw, policy, business.forms[1], true)
	replay := approvedIntegrationCompleted(t, ctx, peerRouter, prepared, business.answer, true)
	if replay.Attempts[0].Result.Fingerprint != first.result.Attempts[0].Result.Fingerprint || peer.generations.Load() != 0 || peer.counts.Load() != 0 {
		t.Fatal("completed producer was not replayed through the same exact claim")
	}
	optional.assertZero(t)
}

func TestApprovedQuestionsKeepUnknownCanonicalOutcomeFencedAcrossForms(t *testing.T) {
	business := approvedIntegrationCases()[5]
	clients := approvedIntegrationClients(t)
	provider := &approvedIntegrationProvider{identity: "approved-unknown-fixture", err: errors.New("scripted provider outcome unknown")}
	optional := &approvedIntegrationOptionalCalls{}
	router := approvedIntegrationRouter(clients[0], provider, optional, nil)
	// Errors do not promise a result fingerprint. Observe the actual claim key
	// forwarded to the HTTP client without computing a broker identity here.
	recorded := &approvedIntegrationClaimRecorder{Client: clients[0]}
	router.Factory = func(Tier) (Runner, error) {
		return &inference.Runner{Broker: recorded, Provider: provider}, nil
	}
	peerRouter := approvedIntegrationRouter(clients[1], provider, optional, nil)
	raw, policy := approvedIntegrationRequest(business), approvedIntegrationSet(business)
	canonical := approvedIntegrationApply(t, raw, policy, business.canonical, true)
	first, err := router.Run(t.Context(), canonical)
	if !errors.Is(err, ErrStopped) || first.Response != nil || len(first.Attempts) != 1 || provider.generations.Load() != 1 {
		t.Fatalf("unknown canonical outcome did not stop: %+v %v", first, err)
	}
	key := recorded.claimKey
	if key == "" {
		t.Fatal("unknown provider work did not leave an observed broker claim")
	}
	claim, err := clients[0].Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	for _, form := range []string{business.forms[0], business.forms[1], business.canonical} {
		prepared := approvedIntegrationApply(t, raw, policy, form, true)
		result, err := peerRouter.Run(t.Context(), prepared)
		if !errors.Is(err, ErrStopped) || !errors.Is(err, inference.ErrInFlight) || result.Response != nil || provider.generations.Load() != 1 || provider.counts.Load() != 1 {
			t.Fatalf("approved form retried an unknown canonical outcome: %+v %v", result, err)
		}
	}
	after, err := clients[1].Get(t.Context(), key)
	if err != nil || after.Version != claim.Version || after.Content != claim.Content {
		t.Fatal("approved forms modified the unresolved canonical claim", err)
	}
	optional.assertZero(t)
}

type approvedIntegrationClaimRecorder struct {
	*client.Client
	claimKey string
}

func (b *approvedIntegrationClaimRecorder) Publish(ctx context.Context, request protocol.PublishRequest) (protocol.PublishResult, error) {
	if b.claimKey == "" && strings.HasPrefix(request.Key, "inference/v1/jobs/") {
		b.claimKey = request.Key
	}
	return b.Client.Publish(ctx, request)
}

func TestApprovedQuestionsLeaveUnlistedExactBaselineUnchanged(t *testing.T) {
	business := approvedIntegrationCases()[0]
	clients := approvedIntegrationClients(t)
	provider := &approvedIntegrationProvider{identity: "approved-baseline-fixture", text: `{"direct_exact_baseline":true}`}
	optional := &approvedIntegrationOptionalCalls{}
	router := approvedIntegrationRouter(clients[0], provider, optional, nil)
	raw := approvedIntegrationRequest(business)
	raw.Spec.Input = "Summarize Astrel's separate phone-support schedule."
	seed := approvedIntegrationCompleted(t, t.Context(), router, raw, provider.text, false)
	prepared := approvedIntegrationApply(t, raw, approvedIntegrationSet(business), raw.Spec.Input, false)
	replay := approvedIntegrationCompleted(t, t.Context(), router, prepared, provider.text, true)
	if replay.Attempts[0].Result.Fingerprint != seed.Attempts[0].Result.Fingerprint || provider.generations.Load() != 1 || provider.counts.Load() != 1 {
		t.Fatal("unlisted exact request lost its preexisting replay behavior")
	}
	optional.assertZero(t)
}
