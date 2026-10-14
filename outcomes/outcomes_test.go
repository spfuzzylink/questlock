package outcomes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/protocol"
)

var ctx = context.Background()

func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func task(id string) Task    { return Task{ID: id, Revision: "revision-1"} }

func completed(id string, cost float64) Run {
	return Run{ID: id, RequestDigest: digest("request"), Status: "completed", Attempts: []Attempt{{TierID: "local", Fingerprint: digest("fingerprint"), OutputSHA256: digest("output"), CheckPassed: true, Complete: true, Durable: true, GenerationCalled: true, UsageKnown: true, Usage: inference.Usage{InputTokens: 20, UncachedInputTokens: 15, CacheReadTokens: 3, CacheWriteTokens: 2, OutputTokens: 10, ReasoningTokens: 2}, EstimatedCostUSD: number(cost), CostBasis: "caller_estimate"}}}
}

func accept(id, runID string, minutes *float64) Feedback {
	return Feedback{ID: id, RunID: runID, Kind: "accepted", FinalArtifactSHA256: digest("final artifact"), HumanMinutes: minutes}
}

func testStore(t *testing.T) (Store, *store.Store, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	token, err := db.CreateToken(ctx, "writer", "outcomes-tests")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(db))
	t.Cleanup(server.Close)
	c, err := client.New(server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	return Store{Broker: c}, db, server.URL
}

func record(t *testing.T, s Store, task Task, run Run) Receipt {
	t.Helper()
	r, err := s.Record(ctx, task, run)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func feedback(t *testing.T, s Store, task Task, version int64, f Feedback) Receipt {
	t.Helper()
	r, err := s.Feedback(ctx, task, version, f)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func snapshot(t *testing.T, s Store, task Task) Snapshot {
	t.Helper()
	r, err := s.Get(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestHumanRewriteReopensAcceptanceAndPreservesReceipts(t *testing.T) {
	s, _, _ := testStore(t)
	task := task("acceptance-loop")
	run := completed("run-1", .01)
	r := record(t, s, task, run)
	if snapshot(t, s, task).State.Status != "unreviewed" {
		t.Fatal("check pass inferred human acceptance")
	}
	a := accept("accepted-1", run.ID, number(2))
	ar := feedback(t, s, task, r.Version, a)
	if snapshot(t, s, task).State.FinalArtifactSHA256 == run.Attempts[0].OutputSHA256 {
		t.Fatal("final corrected artifact hash was replaced by generated hash")
	}
	wr := feedback(t, s, task, ar.Version, Feedback{ID: "rewrite-1", RunID: run.ID, Kind: "rewritten", Reason: "correctness", HumanMinutes: number(10)})
	state := snapshot(t, s, task).State
	if state.Status != "rewritten" || state.AcceptedRunID != "" || state.FinalArtifactSHA256 != "" {
		t.Fatal("rewrite did not clear human acceptance")
	}
	if _, err := s.Feedback(ctx, task, ar.Version, accept("stale", run.ID, number(0))); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale acceptance rebased: %v", err)
	}
	if again := feedback(t, s, task, r.Version, a); again != ar {
		t.Fatal("late exact retry did not return original receipt")
	}
	if snapshot(t, s, task).Version != wr.Version || snapshot(t, s, task).State.Status != "rewritten" {
		t.Fatal("idempotent retry replayed an old transition")
	}
	a.HumanMinutes = number(3)
	if _, err := s.Feedback(ctx, task, r.Version, a); !errors.Is(err, ErrIDConflict) {
		t.Fatal("conflicting event ID accepted")
	}
	final := feedback(t, s, task, wr.Version, accept("accepted-2", run.ID, number(1)))
	reopen := feedback(t, s, task, final.Version, Feedback{ID: "reopened-1", RunID: run.ID, Kind: "reopened", HumanMinutes: number(0)})
	if snapshot(t, s, task).State.Status != "reopened" {
		t.Fatal("reopen lost")
	}
	feedback(t, s, task, reopen.Version, accept("accepted-3", run.ID, number(0)))
	if again := record(t, s, task, run); again != r {
		t.Fatal("run retry did not preserve original receipt")
	}
	changed := completed(run.ID, .02)
	if _, err := s.Record(ctx, task, changed); !errors.Is(err, ErrIDConflict) {
		t.Fatal("same run ID with altered cost accepted")
	}
	got := snapshot(t, s, task)
	if got.Version != 6 || len(got.Runs) != 1 || len(got.Events) != 5 || got.State.Status != "accepted" {
		t.Fatalf("incorrect durable history: %+v", got.State)
	}
}

func TestFeedbackDifferentRewriteReasonsStillRequireExplicitReacceptance(t *testing.T) {
	for _, reason := range []string{"correctness", "style", "requirements", "other"} {
		t.Run(reason, func(t *testing.T) {
			s, _, _ := testStore(t)
			task := task(reason)
			r := record(t, s, task, completed("run", 0))
			a := feedback(t, s, task, r.Version, accept("accept", "run", number(0)))
			feedback(t, s, task, a.Version, Feedback{ID: "rewrite", RunID: "run", Kind: "rewritten", Reason: reason, HumanMinutes: number(1)})
			report, err := s.Report(ctx, []Task{task}, number(60))
			if err != nil {
				t.Fatal(err)
			}
			if report.AcceptedTasks != 0 || report.CostPerAcceptedTaskUSD != nil || report.TotalCostUSD == nil || *report.TotalCostUSD != 1 {
				t.Fatal("rewrite retained denominator or lost human cost")
			}
		})
	}
}

func TestReportChargesRejectedAndFailedWorkOnceAndCountsCurrentAcceptance(t *testing.T) {
	s, _, _ := testStore(t)
	accepted, rejected := task("accepted"), task("rejected")
	r := record(t, s, accepted, completed("fresh", .25))
	a := feedback(t, s, accepted, r.Version, accept("accept", "fresh", number(2)))
	reuse := completed("reuse", 0)
	reuse.Attempts[0].GenerationCalled = false
	reuse.Attempts[0].Reused = true
	reuse.Attempts[0].Usage = inference.Usage{}
	reuse.Attempts[0].CostBasis = "reuse_zero"
	record(t, s, accepted, reuse)
	if snapshot(t, s, accepted).State.AcceptedRunID != "fresh" {
		t.Fatal("new run silently changed human acceptance")
	}
	failed := completed("bad", .75)
	failed.Status = "quality_failed"
	failed.Attempts[0].CheckPassed = false
	rr := record(t, s, rejected, failed)
	feedback(t, s, rejected, rr.Version, Feedback{ID: "reject", RunID: "bad", Kind: "rejected", HumanMinutes: number(3)})
	report, err := s.Report(ctx, []Task{accepted, rejected}, number(60))
	if err != nil {
		t.Fatal(err)
	}
	if report.AcceptedTasks != 1 || report.RunCount != 3 || report.AttemptCount != 3 || report.KnownInferenceSubtotalUSD != 1 || report.RecordedHumanMinutes != 5 || report.TotalCostUSD == nil || *report.TotalCostUSD != 6 || report.CostPerAcceptedTaskUSD == nil || *report.CostPerAcceptedTaskUSD != 6 || *report.InferenceCostPerAcceptedTaskUSD != 1 {
		t.Fatalf("wrong cohort accounting: %+v", report)
	}
	if report.Tasks[0].Version != a.Version+1 || report.Tasks[1].State.Status != "rejected" {
		t.Fatal("report did not expose individually versioned current task states")
	}
	if _, err := s.Report(ctx, []Task{accepted, accepted}, number(60)); !errors.Is(err, ErrInvalid) {
		t.Fatal("duplicate task cohort counted twice")
	}
	if _, err := s.Report(ctx, []Task{task("missing")}, number(60)); err == nil {
		t.Fatal("unregistered task was silently omitted")
	}
}

func TestReportUnknownCostsEffortRateAndUnreviewedRemainUnknown(t *testing.T) {
	s, _, _ := testStore(t)
	task := task("unknown")
	run := completed("uncertain", 0)
	run.Status = "stopped"
	run.Attempts[0].CheckPassed = false
	run.Attempts[0].Complete = false
	run.Attempts[0].Durable = false
	run.Attempts[0].UsageKnown = false
	run.Attempts[0].Usage = inference.Usage{}
	run.Attempts[0].EstimatedCostUSD = nil
	run.Attempts[0].CostBasis = "unknown"
	r := record(t, s, task, run)
	report, err := s.Report(ctx, []Task{task}, number(60))
	if err != nil {
		t.Fatal(err)
	}
	if report.MissingInferenceCosts != 1 || report.MissingUsage != 1 || report.MissingHumanEffort != 1 || report.InferenceCostUSD != nil || report.HumanCostUSD != nil || report.TotalCostUSD != nil || report.CostPerAcceptedTaskUSD != nil {
		t.Fatal("unknown or unreviewed work became free")
	}
	feedback(t, s, task, r.Version, accept("human-completed", run.ID, nil))
	report, err = s.Report(ctx, []Task{task}, number(60))
	if err != nil {
		t.Fatal(err)
	}
	if report.AcceptedTasks != 1 || report.MissingHumanEffort != 1 || report.CostPerAcceptedTaskUSD != nil {
		t.Fatal("acceptance converted unknown effort/cost into zero")
	}

	known := taskForKnown(t, s)
	report, err = s.Report(ctx, []Task{known}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.InferenceCostUSD == nil || report.HumanCostUSD != nil || report.TotalCostUSD != nil || report.InferenceCostPerAcceptedTaskUSD == nil {
		t.Fatal("missing rate fabricated comprehensive cost or erased known inference")
	}
	report, err = s.Report(ctx, []Task{known}, number(0))
	if err != nil || report.TotalCostUSD == nil || *report.TotalCostUSD != .1 {
		t.Fatal("explicit zero rate was treated as missing")
	}
}

func taskForKnown(t *testing.T, s Store) Task {
	task := task("known")
	r := record(t, s, task, completed("known-run", .1))
	feedback(t, s, task, r.Version, accept("known-accept", "known-run", number(2)))
	return task
}

func TestLocalKnownZeroChargeCanKeepUsageUnknownWithoutClaimingHardwareCosts(t *testing.T) {
	s, _, _ := testStore(t)
	task := task("local")
	run := completed("run", 0)
	run.Status = "stopped"
	run.Attempts[0].CheckPassed = false
	run.Attempts[0].Complete = false
	run.Attempts[0].Durable = false
	run.Attempts[0].UsageKnown = false
	run.Attempts[0].Usage = inference.Usage{}
	run.Attempts[0].CostBasis = "local_token_charge"
	r := record(t, s, task, run)
	feedback(t, s, task, r.Version, accept("human-done", "run", number(5)))
	report, err := s.Report(ctx, []Task{task}, number(60))
	if err != nil {
		t.Fatal(err)
	}
	if report.MissingUsage != 1 || report.MissingInferenceCosts != 0 || report.TotalCostUSD == nil || *report.TotalCostUSD != 5 || report.Coverage != "explicit_cohort_recorded_attempts_and_feedback_only" {
		t.Fatal("local billing and usage knowledge were conflated")
	}
}

func TestConcurrentRecordsAndFeedbackUseScopedCAS(t *testing.T) {
	s, db, url := testStore(t)
	task := task("concurrent")
	token, err := db.CreateToken(ctx, "reviewer", "outcomes-tests")
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.New(url, token)
	if err != nil {
		t.Fatal(err)
	}
	other := Store{Broker: c}
	const n = 12
	var wg sync.WaitGroup
	errorsCh := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Record(ctx, task, completed(fmt.Sprintf("run-%d", i), .01))
			errorsCh <- err
		}(i)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	before := snapshot(t, s, task)
	if before.Version != n || len(before.Runs) != n {
		t.Fatal("concurrent runs lost records")
	}
	original := before.Runs[0]
	replay := record(t, other, task, original.Run)
	if replay.Version != original.Version || replay.CreatedAt != original.CreatedAt {
		t.Fatal("same-scope cross-token replay did not preserve receipt")
	}
	var successes atomic.Int32
	errorsCh = make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := other.Feedback(ctx, task, before.Version, accept(fmt.Sprintf("event-%d", i), original.ID, number(0)))
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				errorsCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	if successes.Load() != 1 || snapshot(t, s, task).Version != n+1 {
		t.Fatal("stale human feedback implicitly rebased")
	}

	privateToken, err := db.CreateToken(ctx, "other-scope", "private")
	if err != nil {
		t.Fatal(err)
	}
	privateClient, err := client.New(url, privateToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Store{Broker: privateClient}).Get(ctx, task); !brokerCode(err, "not_found") {
		t.Fatal("task leaked across token scopes")
	}
}

type lostPublish struct {
	inference.Broker
	lose  atomic.Bool
	calls atomic.Int32
}

func (b *lostPublish) Publish(ctx context.Context, r protocol.PublishRequest) (protocol.PublishResult, error) {
	b.calls.Add(1)
	p, err := b.Broker.Publish(ctx, r)
	if err == nil && b.lose.Swap(false) {
		return protocol.PublishResult{}, errors.New("response lost after commit")
	}
	return p, err
}

func TestUncertainPublicationIsNotRetriedAndExactRetryRecoversOnce(t *testing.T) {
	s, _, _ := testStore(t)
	b := &lostPublish{Broker: s.Broker}
	b.lose.Store(true)
	s.Broker = b
	task := task("lost")
	run := completed("run", .5)
	if _, err := s.Record(ctx, task, run); err == nil || b.calls.Load() != 1 {
		t.Fatal("uncertain publication retried")
	}
	r := record(t, s, task, run)
	if r.Version != 1 || b.calls.Load() != 1 {
		t.Fatal("retry republished recorded run")
	}
	b.lose.Store(true)
	f := accept("accept", "run", number(1))
	if _, err := s.Feedback(ctx, task, r.Version, f); err == nil || b.calls.Load() != 2 {
		t.Fatal("uncertain feedback publication retried")
	}
	recovered := feedback(t, s, task, r.Version, f)
	if recovered.Version != 2 || b.calls.Load() != 2 || snapshot(t, s, task).Version != 2 {
		t.Fatal("feedback retry duplicated event")
	}
}

func TestConcurrentIdenticalFeedbackReturnsOneOriginalReceipt(t *testing.T) {
	s, _, _ := testStore(t)
	task := task("same-feedback")
	r := record(t, s, task, completed("run", 0))
	f := accept("event", "run", number(0))
	var wg sync.WaitGroup
	results := make(chan Receipt, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := s.Feedback(ctx, task, r.Version, f)
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first Receipt
	for r := range results {
		if first.Version == 0 {
			first = r
		} else if r != first {
			t.Fatal("identical feedback returned different receipts")
		}
	}
	if snapshot(t, s, task).Version != 2 {
		t.Fatal("identical feedback appended more than once")
	}
}

func TestStoppedPassingChecksAndZeroAttemptAdmissionFailuresAreRecordable(t *testing.T) {
	s, _, _ := testStore(t)
	task := task("stopped")
	run := completed("not-durable", .2)
	run.Status = "stopped"
	run.Attempts[0].Durable = false
	record(t, s, task, run)
	if !snapshot(t, s, task).Runs[0].Attempts[0].CheckPassed {
		t.Fatal("raw checks erased after publication or budget stop")
	}
	record(t, s, task, Run{ID: "blocked-before-attempt", RequestDigest: digest("request"), Status: "stopped"})
}

func TestInvalidInputsCannotMutate(t *testing.T) {
	s, _, _ := testStore(t)
	task := task("invalid")
	good := completed("run", 0)
	tests := map[string]func(*Run){
		"unknown billed zero":        func(r *Run) { r.Attempts[0].CostBasis = "unknown" },
		"negative cost":              func(r *Run) { r.Attempts[0].EstimatedCostUSD = number(-1) },
		"nan cost":                   func(r *Run) { r.Attempts[0].EstimatedCostUSD = number(math.NaN()) },
		"infinite cost":              func(r *Run) { r.Attempts[0].EstimatedCostUSD = number(math.Inf(1)) },
		"bad input partitions":       func(r *Run) { r.Attempts[0].Usage.CacheReadTokens++ },
		"reasoning billed twice":     func(r *Run) { r.Attempts[0].Usage.ReasoningTokens = 99 },
		"usage overflow":             func(r *Run) { r.Attempts[0].Usage.UncachedInputTokens = math.MaxInt64 },
		"unknown usage counters":     func(r *Run) { r.Attempts[0].UsageKnown = false },
		"reused incurred new charge": func(r *Run) { r.Attempts[0].Reused = true; r.Attempts[0].CostBasis = "reuse_zero" },
		"fake completion":            func(r *Run) { r.Attempts[0].Durable = false },
		"too many attempts":          func(r *Run) { r.Attempts = append(r.Attempts, r.Attempts[0], r.Attempts[0]) },
		"uppercase hash":             func(r *Run) { r.RequestDigest = strings.ToUpper(r.RequestDigest) },
		"free-form ID":               func(r *Run) { r.ID = "reviewer said this was wrong" },
	}
	for name, alter := range tests {
		t.Run(name, func(t *testing.T) {
			run := completed("run", 0)
			alter(&run)
			if _, err := s.Record(ctx, task, run); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid input accepted: %v", err)
			}
		})
	}
	if _, err := s.Get(ctx, task); !brokerCode(err, "not_found") {
		t.Fatal("invalid calls created an artifact")
	}
	r := record(t, s, task, good)
	badEvents := []Feedback{accept("accept", "run", number(-1)), {ID: "rewrite", RunID: "run", Kind: "rewritten"}, {ID: "bad-link", RunID: "missing", Kind: "rejected"}, {ID: "unknown-kind", RunID: "run", Kind: "deployed"}, {ID: "no-artifact", RunID: "run", Kind: "accepted"}, {ID: "wrong-reason", RunID: "run", Kind: "rejected", Reason: "style"}}
	for _, f := range badEvents {
		if _, err := s.Feedback(ctx, task, r.Version, f); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid event accepted: %v", err)
		}
	}
	if snapshot(t, s, task).Version != r.Version {
		t.Fatal("invalid feedback changed ledger")
	}
	for _, rate := range []float64{-1, math.NaN(), math.Inf(1)} {
		if _, err := s.Report(ctx, []Task{task}, &rate); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid hourly rate accepted")
		}
	}
}

type staticBroker struct {
	artifact     protocol.Artifact
	publishCalls int
}

func (b *staticBroker) Get(context.Context, string) (protocol.Artifact, error) {
	return b.artifact, nil
}
func (b *staticBroker) Publish(context.Context, protocol.PublishRequest) (protocol.PublishResult, error) {
	b.publishCalls++
	return protocol.PublishResult{}, errors.New("unexpected publish")
}

func encodedLedger(t *testing.T, task Task, runs, events int) protocol.Artifact {
	t.Helper()
	created := time.Now().UTC()
	key, _ := Key(task)
	l := ledger{Schema: 1, Task: task, CreatedAt: created, Runs: []RecordedRun{}, Events: []RecordedFeedback{}}
	for i := range runs {
		l.Runs = append(l.Runs, RecordedRun{Run: completed(fmt.Sprintf("run-%d", i), 0), Version: int64(i + 1), CreatedAt: created})
	}
	for i := range events {
		l.Events = append(l.Events, RecordedFeedback{Feedback: Feedback{ID: fmt.Sprintf("event-%d", i), RunID: "run-0", Kind: "reopened", HumanMinutes: number(0)}, ExpectedVersion: int64(runs + i), Version: int64(runs + i + 1), CreatedAt: created})
	}
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Artifact{Key: key, Version: int64(runs + events), Content: string(b)}
}

func TestCapsAndStrictCorruptLedgersFailBeforePublication(t *testing.T) {
	task := task("limits")
	for _, test := range []struct{ runs, events int }{{MaxRuns, 0}, {1, MaxEvents}} {
		b := &staticBroker{artifact: encodedLedger(t, task, test.runs, test.events)}
		s := Store{Broker: b}
		var err error
		if test.runs == MaxRuns {
			_, err = s.Record(ctx, task, completed("over-limit", 0))
		} else {
			_, err = s.Feedback(ctx, task, b.artifact.Version, Feedback{ID: "over-limit", RunID: "run-0", Kind: "reopened", HumanMinutes: number(0)})
		}
		if !errors.Is(err, ErrLimit) || b.publishCalls != 0 {
			t.Fatal("ledger cap did not stop before mutation")
		}
	}
	base := encodedLedger(t, task, 1, 0)
	corruptions := map[string]func(protocol.Artifact) protocol.Artifact{
		"unknown field": func(a protocol.Artifact) protocol.Artifact {
			a.Content = strings.TrimSuffix(a.Content, "}") + `,"raw_prompt":"secret"}`
			return a
		},
		"duplicate field": func(a protocol.Artifact) protocol.Artifact {
			a.Content = strings.Replace(a.Content, `"schema":1`, `"schema":1,"schema":1`, 1)
			return a
		},
		"wrong task": func(a protocol.Artifact) protocol.Artifact {
			a.Content = strings.Replace(a.Content, `"id":"limits"`, `"id":"other"`, 1)
			return a
		},
		"version gap":        func(a protocol.Artifact) protocol.Artifact { a.Version++; return a },
		"wrong artifact key": func(a protocol.Artifact) protocol.Artifact { a.Key = "other"; return a },
		"invalid utf8":       func(a protocol.Artifact) protocol.Artifact { a.Content += string([]byte{0xff}); return a },
		"over one mib": func(a protocol.Artifact) protocol.Artifact {
			a.Content += strings.Repeat(" ", protocol.MaxContentBytes)
			return a
		},
		"trailing JSON": func(a protocol.Artifact) protocol.Artifact { a.Content += "{}"; return a },
		"missing run digest": func(a protocol.Artifact) protocol.Artifact {
			a.Content = strings.Replace(a.Content, digest("request"), "", 1)
			return a
		},
		"invalid schema": func(a protocol.Artifact) protocol.Artifact {
			a.Content = strings.Replace(a.Content, `"schema":1`, `"schema":2`, 1)
			return a
		},
		"nested duplicate": func(a protocol.Artifact) protocol.Artifact {
			a.Content = strings.Replace(a.Content, `"input_tokens":20`, `"input_tokens":20,"input_tokens":20`, 1)
			return a
		},
	}
	for name, corrupt := range corruptions {
		t.Run(name, func(t *testing.T) {
			b := &staticBroker{artifact: corrupt(base)}
			s := Store{Broker: b}
			if _, err := s.Get(ctx, task); !errors.Is(err, ErrInvalid) {
				t.Fatalf("corrupt ledger read: %v", err)
			}
			if _, err := s.Record(ctx, task, completed("new-run", 0)); !errors.Is(err, ErrInvalid) {
				t.Fatal("corrupt ledger overwritten")
			}
			if _, err := s.Feedback(ctx, task, 1, accept("event", "run-0", number(0))); !errors.Is(err, ErrInvalid) {
				t.Fatal("corrupt ledger received feedback")
			}
			if _, err := s.Report(ctx, []Task{task}, number(0)); !errors.Is(err, ErrInvalid) {
				t.Fatal("corrupt ledger reported as truthful")
			}
			if b.publishCalls != 0 {
				t.Fatal("corrupt ledger published")
			}
		})
	}
}

func TestReportRejectsOverflowWithoutConvertingItToUnknownOrZero(t *testing.T) {
	s, _, _ := testStore(t)
	a, b := task("overflow-a"), task("overflow-b")
	ra := record(t, s, a, completed("run-a", math.MaxFloat64))
	feedback(t, s, a, ra.Version, accept("accept-a", "run-a", number(0)))
	rb := record(t, s, b, completed("run-b", math.MaxFloat64))
	feedback(t, s, b, rb.Version, accept("accept-b", "run-b", number(0)))
	if _, err := s.Report(ctx, []Task{a, b}, number(1)); !errors.Is(err, ErrInvalid) {
		t.Fatal("inference subtotal overflow accepted")
	}
	c := task("human-overflow")
	rc := record(t, s, c, completed("run-c", 0))
	feedback(t, s, c, rc.Version, accept("accept-c", "run-c", number(math.MaxFloat64)))
	if _, err := s.Report(ctx, []Task{c}, number(math.MaxFloat64)); !errors.Is(err, ErrInvalid) {
		t.Fatal("human effort-rate overflow accepted")
	}
}

func TestLedgerAggregateOverflowAndCorruptFeedbackLinksFailClosed(t *testing.T) {
	s, _, _ := testStore(t)
	task := task("aggregate-overflow")
	r := record(t, s, task, completed("run", math.MaxFloat64))
	if _, err := s.Record(ctx, task, completed("overflow", math.MaxFloat64)); !errors.Is(err, ErrInvalid) {
		t.Fatal("unreportable per-task subtotal was published")
	}
	a := feedback(t, s, task, r.Version, accept("accept", "run", number(math.MaxFloat64)))
	if _, err := s.Feedback(ctx, task, a.Version, Feedback{ID: "reopen", RunID: "run", Kind: "reopened", HumanMinutes: number(math.MaxFloat64)}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unreportable per-task effort was published")
	}
	if snapshot(t, s, task).Version != a.Version {
		t.Fatal("overflow changed a valid ledger")
	}
	base := encodedLedger(t, task, 1, 1)
	corruptions := map[string]func(string) string{
		"missing feedback run":     func(s string) string { return strings.Replace(s, `"run_id":"run-0"`, `"run_id":"absent"`, 1) },
		"stale committed feedback": func(s string) string { return strings.Replace(s, `"expected_version":1`, `"expected_version":0`, 1) },
		"wrong receipt version":    func(s string) string { return strings.Replace(s, `"version":2`, `"version":1`, 1) },
		"zero creation timestamp": func(s string) string {
			var l ledger
			_ = json.Unmarshal([]byte(s), &l)
			l.Events[0].CreatedAt = time.Time{}
			b, _ := json.Marshal(l)
			return string(b)
		},
	}
	for name, corrupt := range corruptions {
		t.Run(name, func(t *testing.T) {
			a := base
			a.Content = corrupt(a.Content)
			b := &staticBroker{artifact: a}
			source := Store{Broker: b}
			if _, err := source.Get(ctx, task); !errors.Is(err, ErrInvalid) {
				t.Fatal("corrupt feedback history accepted")
			}
			if _, err := source.Record(ctx, task, completed("new", 0)); !errors.Is(err, ErrInvalid) || b.publishCalls != 0 {
				t.Fatal("corrupt feedback history mutated")
			}
		})
	}
}

func TestTaskKeysUnambiguousAndRevisionDoesNotInheritAcceptance(t *testing.T) {
	a, _ := Key(Task{ID: "a/b", Revision: "c"})
	b, _ := Key(Task{ID: "a", Revision: "b/c"})
	if a == b {
		t.Fatal("ambiguous task tuple")
	}
	s, _, _ := testStore(t)
	task := task("revision")
	r := record(t, s, task, completed("run", 0))
	feedback(t, s, task, r.Version, accept("accept", "run", number(0)))
	task.Revision = "revision-2"
	record(t, s, task, completed("run", 0))
	if snapshot(t, s, task).State.Status != "unreviewed" {
		t.Fatal("new revision inherited acceptance")
	}
}
