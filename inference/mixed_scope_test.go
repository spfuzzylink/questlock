package inference

import (
	"context"
	"errors"
	"testing"
)

func TestLocalWorkDoesNotConsumeOrClearFrozenPaidLedger(t *testing.T) {
	r, p := testRunner(t)
	paid := testSpec()
	paid.Revision = "paid-overrun"
	paid.Prices = &Prices{Input: 1, CacheRead: 1, CacheWrite: 1, Output: 1}
	paid.DailyBudgetUSD = 0.1
	p.response.Usage = Usage{InputTokens: 2000, UncachedInputTokens: 2000, OutputTokens: 25}
	if _, err := r.Run(context.Background(), paid); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatal("fixture did not freeze paid ledger")
	}
	key := "inference/v1/budget/" + r.now().Format("2006-01-02")
	before, err := r.Broker.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	p.response.Usage = Usage{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 25}
	local := testSpec()
	local.Provider = "ollama"
	local.Revision = "local-after-paid-freeze"
	result, err := r.Run(context.Background(), local)
	if err != nil || !result.Durable || !result.Response.Complete {
		t.Fatal("frozen API ledger blocked unmetered local work")
	}
	after, err := r.Broker.Get(context.Background(), key)
	if err != nil || after.Version != before.Version || after.Content != before.Content {
		t.Fatal("local work consumed or cleared paid reservation/freeze")
	}
	paid.Revision = "another-paid-task"
	if _, err := r.Run(context.Background(), paid); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatal("local work weakened API budget admission")
	}
	if p.calls != 2 {
		t.Fatal("blocked paid request generated")
	}
}
