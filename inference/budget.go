package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// A scope has one ledger per UTC day, shared across models and credentials.
// Reservations are retained on unknown outcomes. All workers using a budgeted
// scope must supply the same cap and current prices; this is coordination among
// trusted workers, not a broker-enforced restriction on arbitrary clients.
type budgetLedger struct {
	Schema    int              `json:"schema"`
	CapMicros int64            `json:"cap_micros"`
	Frozen    bool             `json:"frozen,omitempty"`
	Entries   map[string]int64 `json:"entries"`
}

func (r Runner) reserve(ctx context.Context, s Spec, id string, amount int64) (string, error) {
	// Validated local Ollama has no provider token charge and cannot fall back
	// to a cloud model. It neither consumes nor clears the API dollar ledger.
	// A paid scope can therefore keep doing local work after an API overrun.
	if SelfHosted(s) && s.Prices == nil && s.DailyBudgetUSD == 0 && s.MaxCostUSD == 0 {
		return "", nil
	}
	key := "inference/v1/budget/" + r.now().Format("2006-01-02")
	for attempt := 0; attempt < 32; attempt++ {
		a, err := r.Broker.Get(ctx, key)
		version := a.Version
		ledger := budgetLedger{Schema: 1, CapMicros: capMicros(s.DailyBudgetUSD), Entries: map[string]int64{}}
		if err == nil {
			if err = decodeLedger(a.Content, &ledger); err != nil {
				return "", err
			}
			if ledger.Frozen {
				return "", fmt.Errorf("%w: daily ledger is frozen after a measured overrun; operator review required", ErrBudgetExceeded)
			}
			if s.Prices == nil || ledger.CapMicros != capMicros(s.DailyBudgetUSD) {
				return "", errors.New("scope already has a daily budget; all workers must use its same cap and explicit prices")
			}
		} else if brokerCode(err, "not_found") {
			if s.DailyBudgetUSD == 0 {
				return "", nil
			}
			version = 0
		} else {
			return "", err
		}
		if amount < 0 || len(ledger.Entries) >= 1000 {
			return "", errors.New("daily budget ledger is full or reservation is invalid")
		}
		total := int64(0)
		for _, n := range ledger.Entries {
			total += n
		}
		if total > ledger.CapMicros || amount > ledger.CapMicros-total {
			return "", ErrBudgetExceeded
		}
		ledger.Entries[id] = amount
		_, err = r.publish(ctx, key, version, fmt.Sprintf("%s-budget-%d", id, attempt), ledger)
		if err == nil {
			return key, nil
		}
		if !brokerCode(err, "version_conflict") {
			return "", err
		}
		if err = ctx.Err(); err != nil {
			return "", err
		}
	}
	return "", errors.New("daily budget contention exceeded 32 attempts; no generation was started")
}

func decodeLedger(content string, ledger *budgetLedger) error {
	if json.Unmarshal([]byte(content), ledger) != nil || ledger.Schema != 1 || ledger.CapMicros <= 0 || ledger.CapMicros > 1000000000000 || ledger.Entries == nil || len(ledger.Entries) > 1000 {
		return errors.New("invalid daily budget ledger")
	}
	for _, amount := range ledger.Entries {
		if amount < 0 || amount > 1000000000000000 {
			return errors.New("invalid daily budget reservation")
		}
	}
	return nil
}

func (r Runner) settle(ctx context.Context, key, id string, actual int64) error {
	for attempt := 0; attempt < 32; attempt++ {
		a, err := r.Broker.Get(ctx, key)
		if err != nil {
			return err
		}
		var ledger budgetLedger
		if err = decodeLedger(a.Content, &ledger); err != nil {
			return err
		}
		if _, ok := ledger.Entries[id]; !ok {
			return errors.New("daily budget reservation missing")
		}
		if ledger.Entries[id] == actual {
			return nil
		}
		if actual > ledger.Entries[id] {
			ledger.Frozen = true
		}
		ledger.Entries[id] = actual
		_, err = r.publish(ctx, key, a.Version, fmt.Sprintf("%s-settle-%d", id, attempt), ledger)
		if err == nil {
			return nil
		}
		if !brokerCode(err, "version_conflict") {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	return errors.New("daily budget settlement contention; original reservation retained")
}
