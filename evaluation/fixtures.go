package evaluation

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"sync"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/protocol"
)

//go:embed fixtures/quickstart.json
var quickstart []byte

// DefaultSuite returns an independently decoded public six-case fixture suite.
// Embedded data is validated by tests and never used as generated evidence.
func DefaultSuite() Suite {
	suite, err := Load(bytes.NewReader(quickstart))
	if err != nil {
		panic("invalid embedded Questlock evaluation suite")
	}
	return suite
}

// RunFixtures uses supplied answers through the real inference Runner and a
// fresh in-memory CAS broker. No provider/network call or real-time delay is
// performed. Every duration/usage value is explicitly marked simulated.
func RunFixtures(ctx context.Context, suite Suite) (Report, error) {
	if err := Validate(suite); err != nil {
		return Report{}, err
	}
	for _, c := range suite.Cases {
		for _, config := range suite.Configurations {
			if _, ok := c.Fixtures[config.ID]; !ok {
				return Report{}, errors.New("fixture mode requires a supplied fixture for every case/configuration")
			}
		}
	}
	broker := &fixtureBroker{artifacts: make(map[string]protocol.Artifact), receipts: make(map[string]fixtureReceipt)}
	factory := func(c Case, config Configuration) (Runner, error) {
		fixture := c.Fixtures[config.ID]
		provider := &fixtureProvider{fixture: fixture, identity: "questlock-offline-fixture/" + c.ID + "/" + config.ID}
		return inference.Runner{Broker: broker, Provider: provider, Now: func() time.Time { return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) }}, nil
	}
	timing := func(c Case, config Configuration, reused bool) float64 {
		f := c.Fixtures[config.ID]
		if reused {
			return float64(f.ReuseMS)
		}
		return float64(f.FirstRunMS)
	}
	return evaluate(ctx, suite, factory, "fixture", timing)
}

type fixtureProvider struct {
	fixture  Fixture
	identity string
}

func (p *fixtureProvider) Identity() string { return p.identity }
func (p *fixtureProvider) CountInputTokens(context.Context, inference.Request) (int64, error) {
	return p.fixture.Response.Usage.InputTokens, nil
}
func (p *fixtureProvider) Generate(context.Context, inference.Request) (inference.Response, error) {
	return p.fixture.Response, nil
}

type fixtureReceipt struct {
	request protocol.PublishRequest
	result  protocol.PublishResult
}

type fixtureBroker struct {
	mu        sync.Mutex
	artifacts map[string]protocol.Artifact
	receipts  map[string]fixtureReceipt
}

func (b *fixtureBroker) Get(ctx context.Context, key string) (protocol.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Artifact{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	artifact, ok := b.artifacts[key]
	if !ok {
		return protocol.Artifact{}, &client.Error{Code: "not_found"}
	}
	return artifact, nil
}

func (b *fixtureBroker) Publish(ctx context.Context, request protocol.PublishRequest) (protocol.PublishResult, error) {
	if err := ctx.Err(); err != nil {
		return protocol.PublishResult{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if old, ok := b.receipts[request.OperationID]; ok {
		if old.request != request {
			return protocol.PublishResult{}, &client.Error{Code: "operation_conflict"}
		}
		result := old.result
		result.Replayed = true
		return result, nil
	}
	old := b.artifacts[request.Key]
	if old.Version != request.ExpectedVersion {
		return protocol.PublishResult{}, &client.Error{Code: "version_conflict"}
	}
	artifact := protocol.Artifact{Scope: "fixture", Key: request.Key, Version: old.Version + 1, Content: request.Content}
	result := protocol.PublishResult{Artifact: artifact}
	b.artifacts[request.Key] = artifact
	b.receipts[request.OperationID] = fixtureReceipt{request: request, result: result}
	return result, nil
}
