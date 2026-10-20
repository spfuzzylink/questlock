package team

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
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/protocol"
)

func fixture() Request {
	role := Role{ID: "master", Driver: "ollama", Model: "fixture:local", ModelDigest: strings.Repeat("a", 64), Instructions: "Return the requested answer.", MaxInputTokens: 4096, MaxOutputTokens: 256}
	a, b := role, role
	a.ID = "review-correctness"
	b.ID = "review-risk"
	return Request{Schema: 1, Task: "test/shared", Revision: "one", Instructions: "Return JSON with answer 17.", Input: "Answer 17.", Master: role, Reviewers: []Role{a, b}, ReuseForSeconds: 3600, Checks: []evaluation.Check{{ID: "answer", Kind: "json_equals", Expected: json.RawMessage(`{"answer":17}`)}}}
}

type fakeProvider struct {
	calls                *atomic.Int64
	role                 string
	badMaster, badReview bool
	entered              chan struct{}
	release              chan struct{}
}

func (p *fakeProvider) Identity() string { return "local-fixture/" + p.role }
func (p *fakeProvider) CountInputTokens(context.Context, inference.Request) (int64, error) {
	return 100, nil
}
func (p *fakeProvider) Generate(ctx context.Context, _ inference.Request) (inference.Response, error) {
	p.calls.Add(1)
	if p.entered != nil {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
			return inference.Response{}, ctx.Err()
		}
	}
	answer := `{"approved":true,"issues":[]}`
	if p.role == "master" {
		answer = `{"answer":17}`
		if p.badMaster {
			answer = `{"answer":18}`
		}
	}
	if p.badReview && p.role == "review-risk" {
		answer = `{"approved":false,"issues":["incorrect"]}`
	}
	return inference.Response{ID: "fixture", Model: "fixture:local", Text: answer, Complete: true, StopReason: "stop", Usage: inference.Usage{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 20}}, nil
}

func setup(t *testing.T) (*client.Client, *store.Store) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	token, err := s.CreateToken(context.Background(), "host-a", "shared")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.New(s))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	return c, s
}
func engine(c *client.Client, calls *atomic.Int64, badMaster, badReview bool) Engine {
	return Engine{Broker: c, WorkerID: "host-a", Factory: func(_ context.Context, r Role) (delegation.Runner, error) {
		return inference.Runner{Broker: c, Provider: &fakeProvider{calls: calls, role: r.ID, badMaster: badMaster, badReview: badReview}}, nil
	}}
}

func TestLocalTeamReusesWholePipelineWithoutFactoryOrAdmission(t *testing.T) {
	c, _ := setup(t)
	calls := new(atomic.Int64)
	e := engine(c, calls, false, false)
	r := fixture()
	a, err := e.Run(context.Background(), r, "all")
	if err != nil || a.Status != "completed" || a.Text != `{"answer":17}` || calls.Load() != 3 || a.LocalUsage.GenerationCalls != 3 || a.LocalUsage.InputTokens != 300 {
		t.Fatalf("fresh %+v %v calls%d", a, err, calls.Load())
	}
	e.Factory = func(context.Context, Role) (delegation.Runner, error) {
		t.Fatal("cache constructed a model")
		return nil, nil
	}
	e.Admit = func(context.Context, func(context.Context) error) error {
		t.Fatal("cache requested inference capacity")
		return nil
	}
	b, err := e.Run(context.Background(), r, "all")
	if err != nil || b.Text != a.Text || b.LocalUsage.GenerationCalls != 0 || b.LocalUsage.InputTokens != 0 || b.HostedTokens != 0 || b.PaidFallbackEnabled {
		t.Fatalf("repeat %+v %v", b, err)
	}
}

func TestBlockedAdmissionCreatesNoClaimAndMayBeExplicitlyResubmitted(t *testing.T) {
	c, _ := setup(t)
	calls := new(atomic.Int64)
	e := engine(c, calls, false, false)
	e.Admit = func(context.Context, func(context.Context) error) error { return errors.New("no headroom") }
	r, err := e.Run(context.Background(), fixture(), "master")
	if err == nil || r.Status != "blocked" || calls.Load() != 0 {
		t.Fatal(r, err)
	}
	e.Admit = nil
	r, err = e.Run(context.Background(), fixture(), "all")
	if err != nil || r.Status != "completed" || calls.Load() != 3 || !r.AccountingComplete {
		t.Fatal(r, err)
	}
}

func TestStatusNeverExecutesAndReservedRoleNamesAreRejected(t *testing.T) {
	c, _ := setup(t)
	calls := new(atomic.Int64)
	e := engine(c, calls, false, false)
	r := fixture()
	if _, err := e.Run(context.Background(), r, "master"); err != nil {
		t.Fatal(err)
	}
	e.Factory = func(context.Context, Role) (delegation.Runner, error) {
		t.Fatal("status constructed provider")
		return nil, nil
	}
	e.Admit = func(context.Context, func(context.Context) error) error {
		t.Fatal("status requested capacity")
		return nil
	}
	if out, err := e.Run(context.Background(), r, "status"); err != nil || out.Status != "waiting" || calls.Load() != 1 {
		t.Fatal(out, err)
	}
	for _, id := range []string{"status", "all", "master"} {
		bad := fixture()
		bad.Reviewers[0].ID = id
		if _, err := e.Run(context.Background(), bad, "status"); err == nil {
			t.Fatalf("reserved reviewer %s accepted", id)
		}
	}
}

func TestReviewersRunInEitherOrderAfterMaster(t *testing.T) {
	c, _ := setup(t)
	calls := new(atomic.Int64)
	e := engine(c, calls, false, false)
	r := fixture()
	if _, err := e.Run(context.Background(), r, "master"); err != nil {
		t.Fatal(err)
	}
	out, err := e.Run(context.Background(), r, "review-risk")
	if err != nil || out.Status != "waiting" || out.Text != "" || calls.Load() != 2 || len(out.Steps) != 3 || out.Steps[1].State != "waiting" || out.Steps[2].State != "completed" {
		t.Fatal(out, err)
	}
	out, err = e.Run(context.Background(), r, "review-correctness")
	if err != nil || out.Status != "completed" || calls.Load() != 3 {
		t.Fatal(out, err)
	}
}

type missingAccounting struct{ inference.Broker }

func (b missingAccounting) Publish(ctx context.Context, r protocol.PublishRequest) (protocol.PublishResult, error) {
	if !strings.HasPrefix(r.Key, "team/") && !strings.HasPrefix(r.Key, "model/") {
		return protocol.PublishResult{}, errors.New("accounting unavailable")
	}
	return b.Broker.Publish(ctx, r)
}

func TestAccountingFailureIsVisibleWithoutRepeatingGeneration(t *testing.T) {
	c, _ := setup(t)
	calls := new(atomic.Int64)
	e := engine(c, calls, false, false)
	// Only metadata registration is interrupted; inference and role publication
	// still use the original broker so there is a completed checked candidate.
	b := missingAccounting{c}
	e.Broker = b
	r, err := e.Run(context.Background(), fixture(), "all")
	if err != nil || r.Status != "completed" || r.AccountingComplete || calls.Load() != 3 {
		t.Fatal(r, err)
	}
	for _, s := range r.Steps {
		if s.OutcomeTrackingStatus != "failed" {
			t.Fatal(s)
		}
	}
	e.Factory = nil
	r, err = e.Run(context.Background(), fixture(), "all")
	if err != nil || r.AccountingComplete || calls.Load() != 3 {
		t.Fatal("accounting repair regenerated", r, err)
	}
}

func TestTwoIndependentWorkerClientsCoordinateRolesAndWaiting(t *testing.T) {
	c, s := setup(t)
	token, err := s.CreateToken(context.Background(), "host-b", "shared")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.New(s))
	defer srv.Close()
	peer, err := client.New(srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	calls := new(atomic.Int64)
	a := engine(c, calls, false, false)
	b := engine(peer, calls, false, false)
	b.WorkerID = "host-b"
	r := fixture()
	out, err := b.Run(context.Background(), r, "review-risk")
	if err != nil || out.Status != "waiting" || calls.Load() != 0 {
		t.Fatalf("earlyreview %+v %v", out, err)
	}
	for _, step := range []struct {
		e    Engine
		role string
	}{{a, "master"}, {b, "review-correctness"}, {b, "review-risk"}} {
		out, err = step.e.Run(context.Background(), r, step.role)
		if err != nil {
			t.Fatal(err)
		}
	}
	if out.Status != "completed" || out.Steps[0].WorkerID != "host-a" || out.Steps[1].WorkerID != "host-b" || calls.Load() != 3 {
		t.Fatalf("shared %+v", out)
	}
	a.Factory = nil
	out, err = a.Run(context.Background(), r, "status")
	if err != nil || out.Status != "completed" || out.LocalUsage.GenerationCalls != 0 {
		t.Fatalf("final %+v %v", out, err)
	}
}

func TestFailedMasterOrReviewWithholdsAndNeverRegenerates(t *testing.T) {
	for _, masterBad := range []bool{true, false} {
		t.Run(map[bool]string{true: "master", false: "reviewer"}[masterBad], func(t *testing.T) {
			c, _ := setup(t)
			calls := new(atomic.Int64)
			e := engine(c, calls, masterBad, !masterBad)
			r := fixture()
			a, err := e.Run(context.Background(), r, "all")
			if !errors.Is(err, ErrQuality) || a.Text != "" || a.Status != "quality_failed" {
				t.Fatalf("failure %+v %v", a, err)
			}
			count := calls.Load()
			_, err = e.Run(context.Background(), r, "all")
			if !errors.Is(err, ErrQuality) || calls.Load() != count {
				t.Fatal("failed role reran")
			}
		})
	}
}

func TestConcurrentClaimAndUncertainClaimNeverDuplicateGeneration(t *testing.T) {
	c, _ := setup(t)
	calls := new(atomic.Int64)
	entered, release := make(chan struct{}), make(chan struct{})
	e := engine(c, calls, false, false)
	e.Factory = func(_ context.Context, r Role) (delegation.Runner, error) {
		return inference.Runner{Broker: c, Provider: &fakeProvider{calls: calls, role: r.ID, entered: entered, release: release}}, nil
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = e.Run(context.Background(), fixture(), "master") }()
	<-entered
	peer := e
	peer.WorkerID = "host-b"
	out, err := peer.Run(context.Background(), fixture(), "master")
	if !errors.Is(err, ErrPending) || out.Text != "" || calls.Load() != 1 {
		t.Fatalf("duplicate %+v %v", out, err)
	}
	close(release)
	wg.Wait()
}

type lostClaim struct{ inference.Broker }

func (b lostClaim) Publish(ctx context.Context, r protocol.PublishRequest) (protocol.PublishResult, error) {
	v, e := b.Broker.Publish(ctx, r)
	if e == nil && r.ExpectedVersion == 0 && strings.HasPrefix(r.Key, "team/") {
		return protocol.PublishResult{}, errors.New("lost acknowledgement")
	}
	return v, e
}
func TestLostClaimAcknowledgementStopsBeforeModel(t *testing.T) {
	c, _ := setup(t)
	calls := new(atomic.Int64)
	e := engine(c, calls, false, false)
	e.Broker = lostClaim{c}
	_, err := e.Run(context.Background(), fixture(), "all")
	if !errors.Is(err, ErrPending) || calls.Load() != 0 {
		t.Fatal(err, calls.Load())
	}
	e.Broker = c
	_, err = e.Run(context.Background(), fixture(), "all")
	if !errors.Is(err, ErrPending) || calls.Load() != 0 {
		t.Fatal("uncertain claim restarted model")
	}
}

func TestChangedSnapshotAndExpiryAreExplicit(t *testing.T) {
	c, _ := setup(t)
	calls := new(atomic.Int64)
	e := engine(c, calls, false, false)
	now := time.Now().UTC()
	e.Now = func() time.Time { return now }
	r := fixture()
	if _, err := e.Run(context.Background(), r, "all"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	_, err := e.Run(context.Background(), r, "all")
	if !errors.Is(err, ErrExpired) || calls.Load() != 3 {
		t.Fatal("expired regenerated", err)
	}
	r.Revision = "two"
	now = time.Now().UTC()
	if _, err = e.Run(context.Background(), r, "all"); err != nil || calls.Load() != 6 {
		t.Fatal("explicitrevision", err, calls.Load())
	}
}

func TestRequestRejectsPaidUnknownNullDuplicateAndInvalidReview(t *testing.T) {
	b, _ := json.Marshal(fixture())
	for _, s := range []string{strings.Replace(string(b), `"ollama"`, `"openai"`, 1), strings.Replace(string(b), `"schema":1`, `"schema":null`, 1), strings.Replace(string(b), `"schema":1`, `"schema":1,"schema":1`, 1), strings.TrimSuffix(string(b), "}") + `,"allow_paid":true}`} {
		if _, err := Load(strings.NewReader(s)); err == nil {
			t.Fatal("unsafe accepted", s)
		}
	}
	for _, s := range []string{`{}`, `{"approved":null,"issues":[]}`, `{"approved":true,"issues":null}`, `{"approved":true,"approved":true,"issues":[]}`, `{"approved":true,"issues":["bad"]}`, `{"approved":true,"issues":[],"extra":1}`} {
		if reviewApproved(s) {
			t.Fatal("bad review", s)
		}
	}
}
