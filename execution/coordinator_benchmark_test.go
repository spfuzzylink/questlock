package execution_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/execution"
	"github.com/spfuzzylink/questlock/inference"
)

// BenchmarkCoordinatorCPUControlPlane measures actual local HTTPS/SQLite work
// with scripted providers. Nodes are isolated coordinator instances on this
// machine, not physical hosts. No GPU, model inference or paid API is involved.
// quota_on exercises the real shared daily ledger, including parsing and CAS.
// Use a fixed operation count to keep storage and elapsed time bounded:
// go test ./execution -run '^$' -bench BenchmarkCoordinatorCPUControlPlane -benchtime=100x -benchmem
func BenchmarkCoordinatorCPUControlPlane(b *testing.B) {
	for _, nodes := range []int{1, 2, 4} {
		for _, workload := range []string{"duplicate_heavy", "unique"} {
			for _, quota := range []string{"quota_off", "quota_on"} {
				b.Run(fmt.Sprintf("nodes_%d/%s/%s", nodes, workload, quota), func(b *testing.B) {
					if b.N > 1000 {
						b.Fatal("use -benchtime=100x (at most 1000 operations per sub-benchmark)")
					}
					broker := newBrokerFixture(b)
					provider := &fixtureProvider{}
					coordinators := make([]*execution.Coordinator, nodes)
					for i := range coordinators {
						// Distinct clients share the authenticated fixture scope. Each
						// coordinator owns its own queue and synthetic CPU/memory source.
						token := broker.tokenA
						if i%2 == 1 {
							token = broker.tokenB
						}
						coordinators[i] = newNode(b, broker.client(b, token), provider, &fixtureResources{}, nil)
						waitStatus(b, coordinators[i], "ready")
					}
					var dailyBudget *inference.TokenBudget
					if quota == "quota_on" {
						spec := fixtureSpec()
						input, output := spec.MaxInputTokens*int64(b.N), spec.MaxOutputTokens*int64(b.N)
						if input > 1e12 || output > 1e12 || input+output > 1e12 {
							b.Fatal("fixture daily token caps exceed the supported bounds")
						}
						dailyBudget = &inference.TokenBudget{InputTokens: input, OutputTokens: output, TotalTokens: input + output}
					}
					ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
					defer cancel()
					var next, completed, reused, rejected, pending, unexpected, passed, knownTokens atomic.Int64
					var workers sync.WaitGroup
					b.ReportAllocs()
					b.ResetTimer()
					for _, coordinator := range coordinators {
						workers.Go(func() {
							for {
								i := next.Add(1) - 1
								if i >= int64(b.N) {
									return
								}
								spec := fixtureSpec()
								spec.DailyTokenBudget = dailyBudget
								if workload == "unique" || i%10 == 0 {
									spec.Revision = fmt.Sprintf("snapshot-%d", i)
								}
								result, err := coordinator.Run(ctx, spec)
								if err != nil {
									rejected.Add(1) // Pending contention is reported, never retried.
									if errors.Is(err, inference.ErrInFlight) {
										pending.Add(1)
									} else {
										unexpected.Add(1)
									}
									continue
								}
								completed.Add(1)
								knownTokens.Add(result.ThisRunUsage.InputTokens + result.ThisRunUsage.OutputTokens)
								if result.Reused {
									reused.Add(1)
								}
								freshUsage := inference.Usage{InputTokens: 24, UncachedInputTokens: 24, OutputTokens: 3}
								if result.Durable && result.Response.Complete && result.Response.Text == "private-response-fixture" &&
									((result.Reused && result.ThisRunUsage == (inference.Usage{})) || (!result.Reused && result.ThisRunUsage == freshUsage)) {
									passed.Add(1)
								}
							}
						})
					}
					workers.Wait()
					b.StopTimer()
					denominator := float64(b.N)
					b.ReportMetric(float64(provider.generations.Load()), "generation_calls")
					b.ReportMetric(float64(provider.counts.Load()), "count_calls")
					b.ReportMetric(float64(knownTokens.Load()), "known_tokens")
					b.ReportMetric(float64(pending.Load()), "pending_calls")
					b.ReportMetric(float64(unexpected.Load()), "unexpected_errors")
					b.ReportMetric(float64(completed.Load())/denominator, "completed/op")
					b.ReportMetric(float64(reused.Load())/denominator, "reused/op")
					b.ReportMetric(float64(rejected.Load())/denominator, "errors/op")
					b.ReportMetric(float64(passed.Load())/denominator, "checks_passed/op")
					if completed.Load()+rejected.Load() != int64(b.N) || completed.Load() != passed.Load() || unexpected.Load() != 0 {
						b.Fatal("fixture result accounting or declared checks failed")
					}
				})
			}
		}
	}
}
