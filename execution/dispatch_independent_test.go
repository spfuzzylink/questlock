package execution_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/execution"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/protocol"
)

// Publish a scripted peer's completion between the initial missing lookup and
// this worker's claim CAS. The real broker then rejects the stale claim.
type completedPeerRaceBroker struct {
	inference.Broker
	claims atomic.Int64
}

func (b *completedPeerRaceBroker) Publish(ctx context.Context, req protocol.PublishRequest) (protocol.PublishResult, error) {
	if b.claims.Add(1) == 1 {
		var record map[string]any
		if err := json.Unmarshal([]byte(req.Content), &record); err != nil {
			return protocol.PublishResult{}, err
		}
		record["state"] = "complete"
		record["response"] = inference.Response{Model: "cpu-fixture", Text: "scripted peer completion", Complete: true,
			Usage: inference.Usage{InputTokens: 24, UncachedInputTokens: 24, OutputTokens: 3}}
		record["expires_at"] = time.Now().Add(time.Hour)
		content, err := json.Marshal(record)
		if err != nil {
			return protocol.PublishResult{}, err
		}
		if _, err := b.Broker.Publish(ctx, protocol.PublishRequest{Key: req.Key, OperationID: "independent-peer-won", Content: string(content)}); err != nil {
			return protocol.PublishResult{}, err
		}
	}
	return b.Broker.Publish(ctx, req)
}

func TestIndependentDispatchLostClaimReadsCompletedPeerWithoutGeneration(t *testing.T) {
	b := newBrokerFixture(t)
	broker := &completedPeerRaceBroker{Broker: b.client(t, b.tokenA)}
	p := &fixtureProvider{}
	d, err := execution.NewDispatcher(broker, []execution.Node{{ID: "race-loser", Provider: p,
		Admission: func(context.Context, inference.Spec) (func(), error) { return nil, nil }}},
		execution.DispatchOptions{MaxWait: time.Second, PollInterval: 10 * time.Millisecond, WaitForPeer: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	out, err := d.Run(ctx, fixtureSpec())
	if err != nil || !out.Durable || !out.Reused || out.Response.Text != "scripted peer completion" ||
		p.counts.Load() != 0 || p.generations.Load() != 0 || broker.claims.Load() != 1 ||
		out.ThisRunUsage != (inference.Usage{}) || out.Nodes[0].PeerPolls != 1 {
		t.Fatalf("lost claim did not reuse the completed peer: %+v %v", out, err)
	}
}
