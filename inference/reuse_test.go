package inference

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/protocol"
)

type fixtureReuseResolver struct {
	id            string
	identityCalls atomic.Int32
	calls         atomic.Int32
	resolve       func(context.Context, Spec) (*ReuseResolution, error)
}

func (r *fixtureReuseResolver) Identity() string {
	r.identityCalls.Add(1)
	return r.id
}

func (r *fixtureReuseResolver) Resolve(ctx context.Context, spec Spec) (*ReuseResolution, error) {
	r.calls.Add(1)
	return r.resolve(ctx, spec)
}

func reuseFixture(t *testing.T) (Runner, *testProvider, Spec, *ReuseResolution) {
	t.Helper()
	r, provider := testRunner(t)
	spec := testSpec()
	first, err := r.Run(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	key := "inference/v1/jobs/" + first.Fingerprint
	a, err := r.Broker.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	var source jobRecord
	if err := json.Unmarshal([]byte(a.Content), &source); err != nil {
		t.Fatal(err)
	}
	resolution := &ReuseResolution{PolicyID: "controlled-policy-v1", VerificationKey: "inference/v1/jobs/" + reuseDigest("verification"), Source: ReuseSource{Key: key, Version: a.Version, SHA256: reuseDigest(a.Content), OutputSHA256: reuseDigest(first.Response.Text), RequestFingerprint: source.RequestFingerprint}, Response: *source.Response}
	spec.Input = "Give a short account of the current changes."
	return r, provider, spec, resolution
}

func fixtureResolver(resolution *ReuseResolution) *fixtureReuseResolver {
	return &fixtureReuseResolver{id: resolution.PolicyID, resolve: func(context.Context, Spec) (*ReuseResolution, error) { return resolution, nil }}
}

func requireSemanticReuse(t *testing.T, result Result, resolution *ReuseResolution) {
	t.Helper()
	if !result.Reused || !result.Durable || result.ReuseKind != "semantic" || result.Resolution == nil || result.Resolution.Source != resolution.Source || result.Response.Text != resolution.Response.Text || result.GenerationUsage != resolution.Response.Usage || result.AvoidedUsage != resolution.Response.Usage || result.ThisRunUsage != (Usage{}) || result.Response.Usage != (Usage{}) || result.Measurement == nil || result.Measurement.GenerationCalled || result.Measurement.Outcome != "reused" {
		t.Fatalf("invalid semantic reuse accounting: %+v", result)
	}
}

func TestResolverOwnsClaimBeforeResolutionAndReplaysWithoutResolving(t *testing.T) {
	r, provider, spec, resolution := reuseFixture(t)
	spec.DailyTokenBudget = &TokenBudget{InputTokens: 1000, OutputTokens: 1000, TotalTokens: 2000}
	spec.Prices = &Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
	spec.MaxCostUSD, spec.DailyBudgetUSD = 1, 1
	resolver := fixtureResolver(resolution)
	resolver.resolve = func(ctx context.Context, current Spec) (*ReuseResolution, error) {
		plan, _ := BuildPlan(current)
		a, err := r.Broker.Get(ctx, "inference/v1/jobs/"+reuseDigest(plan.Fingerprint+"\x00"+provider.Identity()))
		var record jobRecord
		if err != nil || json.Unmarshal([]byte(a.Content), &record) != nil || record.State != "pending" || record.BudgetKey != "" || record.TokenBudgetKey != "" {
			t.Fatalf("resolver ran without clean owned claim: %+v %v", record, err)
		}
		if provider.calls != 1 || provider.counts != 1 {
			t.Fatal("target provider ran before resolution")
		}
		return resolution, nil
	}
	first, err := r.RunWithResolver(t.Context(), spec, resolver)
	if err != nil {
		t.Fatal(err)
	}
	requireSemanticReuse(t, first, resolution)
	if first.CostUSD == nil || *first.CostUSD != 0 {
		t.Fatal("semantic reuse charged target generation")
	}
	second, err := r.RunWithResolver(t.Context(), spec, resolver)
	if err != nil {
		t.Fatal(err)
	}
	requireSemanticReuse(t, second, resolution)
	if resolver.calls.Load() != 1 || provider.calls != 1 || provider.counts != 1 || first.Fingerprint != second.Fingerprint {
		t.Fatal("resolved replay repeated work")
	}
	a, err := r.Broker.Get(t.Context(), "inference/v1/jobs/"+first.Fingerprint)
	var record jobRecord
	if err != nil || json.Unmarshal([]byte(a.Content), &record) != nil || record.State != "resolved" || record.Reservation != "" || record.BudgetKey != "" || record.TokenBudgetKey != "" || record.ReservedMicros != 0 {
		t.Fatalf("resolved target acquired generation reservations: %+v %v", record, err)
	}
}

func TestResolverConcurrentExactRequestsResolveOnlyOnce(t *testing.T) {
	r, provider, spec, resolution := reuseFixture(t)
	// Use separate authenticated clients in one shared broker scope. The
	// source is copied as synthetic fixture state, not regenerated.
	shared, err := store.Open(filepath.Join(t.TempDir(), "shared.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	server := httptest.NewServer(httpapi.New(shared))
	t.Cleanup(server.Close)
	var peers []Runner
	for _, agent := range []string{"owner", "waiter"} {
		token, err := shared.CreateToken(t.Context(), agent, "shared")
		if err != nil {
			t.Fatal(err)
		}
		broker, err := client.New(server.URL, token)
		if err != nil {
			t.Fatal(err)
		}
		peer := r
		peer.Broker = broker
		peers = append(peers, peer)
	}
	sourceArtifact, err := r.Broker.Get(t.Context(), resolution.Source.Key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peers[0].Broker.Publish(t.Context(), protocol.PublishRequest{Key: sourceArtifact.Key, OperationID: "copied-fixture-source", Content: sourceArtifact.Content}); err != nil {
		t.Fatal(err)
	}
	resolution.Source.Version = 1
	r = peers[0]
	entered, release := make(chan struct{}), make(chan struct{})
	resolver := fixtureResolver(resolution)
	resolver.resolve = func(context.Context, Spec) (*ReuseResolution, error) {
		close(entered)
		<-release
		return resolution, nil
	}
	var owner Result
	var ownerErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); owner, ownerErr = r.RunWithResolver(t.Context(), spec, resolver) }()
	<-entered
	for range 8 {
		if _, err := peers[1].RunWithResolver(t.Context(), spec, resolver); !errors.Is(err, ErrInFlight) {
			t.Fatalf("competing worker did not retain claim: %v", err)
		}
	}
	close(release)
	wg.Wait()
	if ownerErr != nil {
		t.Fatal(ownerErr)
	}
	requireSemanticReuse(t, owner, resolution)
	if resolver.calls.Load() != 1 || provider.calls != 1 || provider.counts != 1 {
		t.Fatal("concurrent exact request duplicated work")
	}
}

func TestResolverNilMissErrorAndExactHitBoundaries(t *testing.T) {
	t.Run("nil resolver preserves normal generation", func(t *testing.T) {
		r, provider := testRunner(t)
		result, err := r.RunWithResolver(t.Context(), testSpec(), nil)
		if err != nil || result.Reused || result.ReuseKind != "" || result.Resolution != nil || provider.calls != 1 || provider.counts != 1 {
			t.Fatalf("nil resolver changed Run: %+v %v", result, err)
		}
	})
	t.Run("nil resolution permits one fresh generation", func(t *testing.T) {
		r, provider := testRunner(t)
		resolver := &fixtureReuseResolver{id: "miss-policy", resolve: func(context.Context, Spec) (*ReuseResolution, error) { return nil, nil }}
		result, err := r.RunWithResolver(t.Context(), testSpec(), resolver)
		if err != nil || result.Reused || provider.calls != 1 || provider.counts != 1 || resolver.calls.Load() != 1 {
			t.Fatalf("clean miss did not generate normally: %+v %v", result, err)
		}
	})
	t.Run("resolver uncertainty stops target generation", func(t *testing.T) {
		r, provider := testRunner(t)
		resolver := &fixtureReuseResolver{id: "error-policy", resolve: func(context.Context, Spec) (*ReuseResolution, error) { return nil, ErrInFlight }}
		result, err := r.RunWithResolver(t.Context(), testSpec(), resolver)
		if !errors.Is(err, ErrInFlight) || result.Response.Text != "" || provider.calls != 0 || provider.counts != 0 {
			t.Fatalf("uncertain resolver generated target: %+v %v", result, err)
		}
	})
	t.Run("ordinary exact hit never initializes resolver", func(t *testing.T) {
		r, provider := testRunner(t)
		if _, err := r.Run(t.Context(), testSpec()); err != nil {
			t.Fatal(err)
		}
		resolver := &fixtureReuseResolver{resolve: func(context.Context, Spec) (*ReuseResolution, error) {
			t.Fatal("exact hit called Resolve")
			return nil, nil
		}}
		result, err := r.RunWithResolver(t.Context(), testSpec(), resolver)
		if err != nil || !result.Reused || resolver.identityCalls.Load() != 0 || provider.calls != 1 {
			t.Fatalf("exact hit touched resolver: %+v %v", result, err)
		}
	})
	t.Run("ordinary mode can replace a resolved target", func(t *testing.T) {
		r, provider, spec, resolution := reuseFixture(t)
		if _, err := r.RunWithResolver(t.Context(), spec, fixtureResolver(resolution)); err != nil {
			t.Fatal(err)
		}
		result, err := r.Run(t.Context(), spec)
		if err != nil || result.Reused || result.ReuseKind != "" || provider.calls != 2 || provider.counts != 2 {
			t.Fatalf("ordinary mode could not generate a resolved-only target: %+v %v", result, err)
		}
	})
}

func TestResolvedReplaySourceChangesAndExpiryFailClosed(t *testing.T) {
	for _, change := range []string{"mutation", "expiry", "shorter caller ttl"} {
		t.Run(change, func(t *testing.T) {
			r, provider, spec, resolution := reuseFixture(t)
			resolver := fixtureResolver(resolution)
			first, err := r.RunWithResolver(t.Context(), spec, resolver)
			if err != nil {
				t.Fatal(err)
			}
			if change == "mutation" {
				a, err := r.Broker.Get(t.Context(), resolution.Source.Key)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := r.Broker.Publish(t.Context(), protocol.PublishRequest{Key: a.Key, ExpectedVersion: a.Version, OperationID: "source-change", Content: a.Content}); err != nil {
					t.Fatal(err)
				}
			} else if change == "expiry" {
				r.Now = func() time.Time { return first.ExpiresAt }
			} else {
				r.Now = func() time.Time { return first.CreatedAt.Add(2 * time.Second) }
				spec.ReuseForSeconds = 1
			}
			result, err := r.RunWithResolver(t.Context(), spec, resolver)
			if err == nil || result.Response.Text != "" || resolver.calls.Load() != 1 || provider.calls != 1 || provider.counts != 1 {
				t.Fatalf("invalidated resolution repeated work: %+v %v", result, err)
			}
		})
	}
}

func TestResolverRejectsUnprovedSourcesAndResponseMismatch(t *testing.T) {
	for _, change := range []string{"response", "request fingerprint", "hash", "policy", "alias source", "expired", "usage"} {
		t.Run(change, func(t *testing.T) {
			r, provider, spec, original := reuseFixture(t)
			resolution := cloneResolution(original)
			switch change {
			case "response":
				resolution.Response.Text += " modified"
			case "request fingerprint":
				resolution.Source.RequestFingerprint = reuseDigest("other plan")
			case "hash":
				resolution.Source.OutputSHA256 = reuseDigest("other output")
			case "policy":
				resolution.PolicyID = "other-policy"
			case "expired":
				r.Now = func() time.Time { return time.Date(2030, 1, 2, 5, 4, 5, 0, time.UTC) }
			case "alias source", "usage":
				a, _ := r.Broker.Get(t.Context(), resolution.Source.Key)
				var record jobRecord
				_ = json.Unmarshal([]byte(a.Content), &record)
				if change == "alias source" {
					record.State, record.Resolution = "resolved", original
				} else {
					record.Response.Usage.UncachedInputTokens++
				}
				updated, err := r.publish(t.Context(), a.Key, a.Version, "invalid-source", record)
				if err != nil {
					t.Fatal(err)
				}
				resolution.Source.Version, resolution.Source.SHA256 = updated.Version, reuseDigest(updated.Content)
				resolution.Response = *record.Response
			}
			resolver := fixtureResolver(original)
			resolver.resolve = func(context.Context, Spec) (*ReuseResolution, error) { return resolution, nil }
			result, err := r.RunWithResolver(t.Context(), spec, resolver)
			if err == nil || result.Response.Text != "" || provider.calls != 1 || provider.counts != 1 {
				t.Fatalf("unproved source generated or escaped: %+v %v", result, err)
			}
		})
	}
}

type resolutionAckBroker struct {
	Broker
	commit bool
	claim  bool
	failed atomic.Int32
}

func (b *resolutionAckBroker) Publish(ctx context.Context, request protocol.PublishRequest) (protocol.PublishResult, error) {
	selected := strings.HasSuffix(request.OperationID, "-resolved") || b.claim && !strings.Contains(request.OperationID, "-resolved")
	if !selected {
		return b.Broker.Publish(ctx, request)
	}
	if b.commit {
		if _, err := b.Broker.Publish(ctx, request); err != nil {
			return protocol.PublishResult{}, err
		}
	}
	b.failed.Add(1)
	return protocol.PublishResult{}, errors.New("synthetic lost acknowledgement")
}

func TestResolverLostAcknowledgementsWithholdAnswersAndPreserveClaims(t *testing.T) {
	for _, mode := range []string{"uncommitted resolution", "committed resolution", "claim"} {
		t.Run(mode, func(t *testing.T) {
			r, provider, spec, resolution := reuseFixture(t)
			broker := &resolutionAckBroker{Broker: r.Broker, commit: mode != "uncommitted resolution", claim: mode == "claim"}
			r.Broker = broker
			resolver := fixtureResolver(resolution)
			result, err := r.RunWithResolver(t.Context(), spec, resolver)
			if err == nil || result.Response.Text != "" || result.Reused || provider.calls != 1 || provider.counts != 1 || broker.failed.Load() != 2 {
				t.Fatalf("lost acknowledgement exposed result or regenerated: %+v %v", result, err)
			}
			r.Broker = broker.Broker
			result, err = r.RunWithResolver(t.Context(), spec, resolver)
			if mode == "committed resolution" {
				if err != nil {
					t.Fatal(err)
				}
				requireSemanticReuse(t, result, resolution)
			} else if !errors.Is(err, ErrInFlight) || result.Response.Text != "" {
				t.Fatalf("unconfirmed claim was cleared: %+v %v", result, err)
			}
			want := int32(1)
			if mode == "claim" {
				want = 0
			}
			if resolver.calls.Load() != want || provider.calls != 1 {
				t.Fatal("lost acknowledgement repeated resolution or generation")
			}
		})
	}
}

func TestLookupIsReadOnlyAndNeverServesResolvedAliases(t *testing.T) {
	r, provider, target, resolution := reuseFixture(t)
	source := testSpec()
	before, err := r.Broker.Get(t.Context(), resolution.Source.Key)
	if err != nil {
		t.Fatal(err)
	}
	result, found, err := r.Lookup(t.Context(), source)
	if err != nil || !found || !result.Reused || result.Response.Text != resolution.Response.Text || provider.calls != 1 || provider.counts != 1 {
		t.Fatalf("lookup did not return exact source: %+v %v %v", result, found, err)
	}
	after, err := r.Broker.Get(t.Context(), resolution.Source.Key)
	if err != nil || before != after {
		t.Fatal("lookup rewrote source")
	}
	if _, found, err := r.Lookup(t.Context(), target); err != nil || found {
		t.Fatalf("lookup created missing target: %v %v", found, err)
	}
	if _, err := r.RunWithResolver(t.Context(), target, fixtureResolver(resolution)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := r.Lookup(t.Context(), target); err != nil || found {
		t.Fatalf("lookup served an unauthorized alias: %v %v", found, err)
	}
	provider.err = errors.New("unknown generation")
	unknown := source
	unknown.Revision = "unknown-call"
	if _, err := r.Run(t.Context(), unknown); err == nil {
		t.Fatal("unknown call accepted")
	}
	if _, found, err := r.Lookup(t.Context(), unknown); !errors.Is(err, ErrInFlight) || found {
		t.Fatalf("lookup bypassed uncertainty: %v %v", found, err)
	}
}
