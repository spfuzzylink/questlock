package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
)

// modelQuestIO exercises real broker transactions and persistence without
// contacting a model service. Usage and prices are explicit fixture values.
func modelQuestIO(output io.Writer) error {
	if _, err := fmt.Fprintln(output, "MODEL QUEST — six offline cost-control scenarios\nReal HTTP broker + temporary SQLite; deterministic model fixture; no API key or paid network."); err != nil {
		return err
	}
	scenarios := []struct {
		name string
		run  func(context.Context) (string, error)
	}{
		{"simple 1: repeated approved task", modelQuestReuse},
		{"simple 2: bounded context", modelQuestContext},
		{"simple 3: preflight cost cap", modelQuestCap},
		{"complex 1: concurrent workers", modelQuestConcurrent},
		{"complex 2: restart and revision", modelQuestRestart},
		{"complex 3: shared budget and unknown outcome", modelQuestBudget},
	}
	for _, scenario := range scenarios {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		detail, err := scenario.run(ctx)
		cancel()
		if err != nil {
			return fmt.Errorf("%s: %w", scenario.name, err)
		}
		if _, err := fmt.Fprintf(output, "PASS  %s — %s\n", scenario.name, detail); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(output, "QUEST CLEARED — six scenarios passed. These are correctness checks, not measured live-provider savings.")
	return err
}

type modelQuestFixture struct {
	dir     string
	dbPath  string
	store   *store.Store
	server  *httptest.Server
	tokens  []string
	clients []*client.Client
}

func newModelQuestFixture(ctx context.Context) (*modelQuestFixture, error) {
	dir, err := os.MkdirTemp("", "questlock-model-quest-")
	if err != nil {
		return nil, err
	}
	f := &modelQuestFixture{dir: dir, dbPath: filepath.Join(dir, "state.db")}
	f.store, err = store.Open(f.dbPath)
	if err != nil {
		f.close()
		return nil, err
	}
	for i := 0; i < 20; i++ {
		token, err := f.store.CreateToken(ctx, fmt.Sprintf("fixture-worker-%d", i), "model-quest")
		if err != nil {
			f.close()
			return nil, err
		}
		f.tokens = append(f.tokens, token)
	}
	if err = f.serve(); err != nil {
		f.close()
		return nil, err
	}
	return f, nil
}

func (f *modelQuestFixture) serve() error {
	f.server = httptest.NewServer(httpapi.New(f.store))
	f.clients = nil
	for _, token := range f.tokens {
		c, err := client.New(f.server.URL, token)
		if err != nil {
			return err
		}
		f.clients = append(f.clients, c)
	}
	return nil
}

func (f *modelQuestFixture) restart() error {
	f.server.Close()
	f.server = nil
	if err := f.store.Close(); err != nil {
		return err
	}
	f.store = nil
	s, err := store.Open(f.dbPath)
	if err != nil {
		return err
	}
	f.store = s
	return f.serve()
}

func (f *modelQuestFixture) close() {
	if f.server != nil {
		f.server.Close()
	}
	if f.store != nil {
		_ = f.store.Close()
	}
	_ = os.RemoveAll(f.dir)
}

func (f *modelQuestFixture) runner(worker int, provider inference.Provider) inference.Runner {
	return inference.Runner{Broker: f.clients[worker], Provider: provider, Now: func() time.Time {
		// Keep the daily-budget race reproducible even at a UTC date boundary.
		return time.Date(2030, time.January, 2, 12, 0, 0, 0, time.UTC)
	}}
}

type modelQuestProvider struct {
	calls   atomic.Int64
	entered chan struct{}
	gate    <-chan struct{}
	failure error
}

func (*modelQuestProvider) Identity() string { return "offline://questlock/model-quest-v1" }

func (*modelQuestProvider) CountInputTokens(context.Context, inference.Request) (int64, error) {
	return 100, nil
}

func (p *modelQuestProvider) Generate(ctx context.Context, request inference.Request) (inference.Response, error) {
	n := p.calls.Add(1)
	if p.entered != nil {
		select {
		case p.entered <- struct{}{}:
		case <-ctx.Done():
			return inference.Response{}, ctx.Err()
		}
	}
	if p.gate != nil {
		select {
		case <-p.gate:
		case <-ctx.Done():
			return inference.Response{}, ctx.Err()
		}
	}
	if p.failure != nil {
		return inference.Response{}, p.failure
	}
	return inference.Response{
		ID: fmt.Sprintf("fixture-%d", n), Model: request.Model,
		Text: "A complete offline fixture answer.", Complete: true, StopReason: "stop",
		Usage: inference.Usage{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 25},
	}, nil
}

func modelQuestSpec(task string) inference.Spec {
	return inference.Spec{
		Task: task, Revision: "approved-snapshot-v1", Provider: "openai", Model: "offline-fixture",
		Instructions: "Use only the approved task snapshot and answer concisely.", Input: "Summarize the approved change.",
		MaxContextBytes: 1024, MaxInputTokens: 100, MaxOutputTokens: 100,
		ReuseForSeconds: 3600,
		Prices:          &inference.Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1},
	}
}

func modelQuestReuse(ctx context.Context) (string, error) {
	f, err := newModelQuestFixture(ctx)
	if err != nil {
		return "", err
	}
	defer f.close()
	p := &modelQuestProvider{}
	spec := modelQuestSpec("repeat")
	first, err := f.runner(0, p).Run(ctx, spec)
	if err != nil {
		return "", err
	}
	second, err := f.runner(1, p).Run(ctx, spec)
	if err != nil {
		return "", err
	}
	if first.Reused || !second.Reused || p.calls.Load() != 1 || second.ThisRunUsage != (inference.Usage{}) || second.AvoidedUsage.InputTokens != 100 || second.AvoidedUsage.OutputTokens != 25 {
		return "", errors.New("repeat did not recover the result with zero generation usage")
	}
	return "2 workers, 1 generation; repeat avoids fixture 100 input + 25 output tokens", nil
}

func modelQuestContext(context.Context) (string, error) {
	spec := modelQuestSpec("context")
	spec.MaxContextBytes = 256
	spec.Context = []inference.ContextItem{
		{ID: "policy", Text: strings.Repeat("p", 32), Required: true, Stable: true},
		{ID: "current-diff", Text: strings.Repeat("d", 32), Priority: 10},
		{ID: "old-log", Text: strings.Repeat("l", 512), Priority: 1},
	}
	plan, err := inference.BuildPlan(spec)
	if err != nil {
		return "", err
	}
	if len(plan.Selected) != 2 || len(plan.Omitted) != 1 || plan.Omitted[0] != "old-log" || plan.ContextBytes >= plan.TotalContextBytes || strings.Contains(plan.Request.Input, strings.Repeat("l", 512)) {
		return "", errors.New("optional context was not omitted as a whole item")
	}
	return fmt.Sprintf("%d of %d context bytes selected; required policy preserved; old log omitted whole", plan.ContextBytes, plan.TotalContextBytes), nil
}

func modelQuestCap(ctx context.Context) (string, error) {
	f, err := newModelQuestFixture(ctx)
	if err != nil {
		return "", err
	}
	defer f.close()
	p := &modelQuestProvider{}
	spec := modelQuestSpec("cost-cap")
	spec.MaxCostUSD = 0.0001
	_, err = f.runner(0, p).Run(ctx, spec)
	if !errors.Is(err, inference.ErrBudgetExceeded) || p.calls.Load() != 0 {
		return "", fmt.Errorf("expected cost rejection before generation, got %v (%d calls)", err, p.calls.Load())
	}
	return "fixture $0.0002 maximum exceeds $0.0001 cap; 0 generations", nil
}

type modelQuestOutcome struct {
	result inference.Result
	err    error
}

func modelQuestConcurrent(ctx context.Context) (string, error) {
	f, err := newModelQuestFixture(ctx)
	if err != nil {
		return "", err
	}
	defer f.close()
	gate := make(chan struct{})
	p := &modelQuestProvider{entered: make(chan struct{}, 1), gate: gate}
	spec := modelQuestSpec("concurrent")
	winner := make(chan modelQuestOutcome, 1)
	go func() {
		result, err := f.runner(0, p).Run(ctx, spec)
		winner <- modelQuestOutcome{result, err}
	}()
	if err := modelQuestAwaitGeneration(ctx, p); err != nil {
		close(gate)
		<-winner
		return "", err
	}
	blocked := make(chan error, 19)
	for i := 1; i < 20; i++ {
		go func(worker int) {
			_, err := f.runner(worker, p).Run(ctx, spec)
			blocked <- err
		}(i)
	}
	var blockedErr error
	for i := 0; i < 19; i++ {
		if err := <-blocked; !errors.Is(err, inference.ErrInFlight) && blockedErr == nil {
			blockedErr = fmt.Errorf("pending duplicate expected ErrInFlight, got %v", err)
		}
	}
	close(gate)
	first := <-winner
	if blockedErr != nil {
		return "", blockedErr
	}
	if first.err != nil {
		return "", first.err
	}
	second, err := f.runner(1, p).Run(ctx, spec)
	if err != nil {
		return "", err
	}
	if p.calls.Load() != 1 || !second.Reused || first.result.Reused {
		return "", errors.New("concurrent workers generated more than once or failed subsequent reuse")
	}
	return "20 simultaneous workers, 1 generation; 19 defer; completed result then reused", nil
}

func modelQuestRestart(ctx context.Context) (string, error) {
	f, err := newModelQuestFixture(ctx)
	if err != nil {
		return "", err
	}
	defer f.close()
	p := &modelQuestProvider{}
	spec := modelQuestSpec("restart")
	first, err := f.runner(0, p).Run(ctx, spec)
	if err != nil {
		return "", err
	}
	if err = f.restart(); err != nil {
		return "", err
	}
	second, err := f.runner(1, p).Run(ctx, spec)
	if err != nil {
		return "", err
	}
	if !second.Reused || p.calls.Load() != 1 || second.Fingerprint != first.Fingerprint {
		return "", errors.New("completed result did not survive storage reopen")
	}
	spec.Revision = "approved-snapshot-v2"
	third, err := f.runner(1, p).Run(ctx, spec)
	if err != nil {
		return "", err
	}
	if third.Reused || third.Fingerprint == first.Fingerprint || p.calls.Load() != 2 {
		return "", errors.New("changed task revision did not invalidate reuse")
	}
	return "broker/database reopened: 0 extra generations; new revision: 1 fresh generation", nil
}

func modelQuestBudget(ctx context.Context) (string, error) {
	f, err := newModelQuestFixture(ctx)
	if err != nil {
		return "", err
	}
	defer f.close()
	gate := make(chan struct{})
	p := &modelQuestProvider{entered: make(chan struct{}, 1), gate: gate}
	outcomes := make(chan modelQuestOutcome, 2)
	for i := 0; i < 2; i++ {
		go func(worker int) {
			spec := modelQuestSpec(fmt.Sprintf("shared-budget-%d", worker))
			spec.DailyBudgetUSD = 0.0003
			result, err := f.runner(worker, p).Run(ctx, spec)
			outcomes <- modelQuestOutcome{result, err}
		}(i)
	}
	if err = modelQuestAwaitGeneration(ctx, p); err != nil {
		close(gate)
		<-outcomes
		<-outcomes
		return "", err
	}
	// Keep the paid call pending until the other request has tested its claim
	// against the outstanding reservation. No sleep or scheduling assumption.
	loser := <-outcomes
	close(gate)
	winner := <-outcomes
	if !errors.Is(loser.err, inference.ErrBudgetExceeded) || winner.err != nil || p.calls.Load() != 1 {
		return "", fmt.Errorf("shared budget did not admit exactly one request: loser=%v winner=%v calls=%d", loser.err, winner.err, p.calls.Load())
	}

	unknown, err := newModelQuestFixture(ctx)
	if err != nil {
		return "", err
	}
	defer unknown.close()
	pending := &modelQuestProvider{failure: errors.New("simulated lost provider response")}
	spec := modelQuestSpec("unknown-outcome")
	spec.DailyBudgetUSD = 0.0003
	_, err = unknown.runner(0, pending).Run(ctx, spec)
	if err == nil {
		return "", errors.New("simulated unknown provider outcome unexpectedly completed")
	}
	_, retryErr := unknown.runner(1, pending).Run(ctx, spec)
	spec.Task = "another-request-after-unknown"
	_, budgetErr := unknown.runner(1, pending).Run(ctx, spec)
	if !errors.Is(retryErr, inference.ErrInFlight) || !errors.Is(budgetErr, inference.ErrBudgetExceeded) || pending.calls.Load() != 1 {
		return "", fmt.Errorf("unknown outcome did not retain reservation and prevent duplicate generation: retry=%v budget=%v calls=%d", retryErr, budgetErr, pending.calls.Load())
	}
	return "2 distinct requests race: 1 admitted; unknown outcome keeps reservation and blocks duplicate retry", nil
}

func modelQuestAwaitGeneration(ctx context.Context, p *modelQuestProvider) error {
	select {
	case <-p.entered:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("generation did not start: %w", ctx.Err())
	}
}
