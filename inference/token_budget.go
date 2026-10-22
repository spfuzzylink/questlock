package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/spfuzzylink/questlock/protocol"
)

// TokenBudget is a shared scope/UTC-day allowance across participating models
// and workers. All three caps are positive, at most 1e12, and must agree across
// participants. Input includes cache reads/writes; output includes reasoning.
// These are each model's reported token units, not equivalent units of work.
// A nil policy cannot bypass an existing day's ledger. Register the policy in a
// dedicated scope before concurrent traffic; old/arbitrary clients can bypass it.
type TokenBudget struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

type tokenEntry struct {
	Reserved TokenBudget  `json:"reserved"`
	Actual   *TokenBudget `json:"actual,omitempty"`
}

type tokenLedger struct {
	Schema  int                   `json:"schema"`
	Caps    TokenBudget           `json:"caps"`
	Frozen  bool                  `json:"frozen,omitempty"`
	Entries map[string]tokenEntry `json:"entries"`
}

func validTokenBudget(b TokenBudget) bool {
	return b.InputTokens > 0 && b.InputTokens <= 1e12 && b.OutputTokens > 0 && b.OutputTokens <= 1e12 && b.TotalTokens > 0 && b.TotalTokens <= 1e12
}

func tokenAmount(input, output int64) TokenBudget {
	return TokenBudget{InputTokens: input, OutputTokens: output, TotalTokens: input + output}
}

func tokenOver(a, b TokenBudget) bool {
	return a.InputTokens > b.InputTokens || a.OutputTokens > b.OutputTokens || a.TotalTokens > b.TotalTokens
}

func addTokens(a, b TokenBudget) (TokenBudget, error) {
	if b.InputTokens < 0 || b.OutputTokens < 0 || b.TotalTokens < 0 || a.InputTokens > math.MaxInt64-b.InputTokens || a.OutputTokens > math.MaxInt64-b.OutputTokens || a.TotalTokens > math.MaxInt64-b.TotalTokens {
		return TokenBudget{}, errors.New("daily token ledger arithmetic overflow")
	}
	return TokenBudget{a.InputTokens + b.InputTokens, a.OutputTokens + b.OutputTokens, a.TotalTokens + b.TotalTokens}, nil
}

func (r Runner) reserveTokens(ctx context.Context, s Spec, id string) (string, error) {
	key := "inference/v1/token-budget/" + r.now().Format("2006-01-02")
	if s.DailyTokenBudget == nil {
		a, err := r.Broker.Get(ctx, key)
		if brokerCode(err, "not_found") {
			return "", nil
		}
		if err != nil {
			return key, err
		}
		var ledger tokenLedger
		if err := decodeTokenLedger(a.Content, &ledger); err != nil {
			return key, err
		}
		return key, fmt.Errorf("%w: scope already has a daily token budget; all workers must declare its caps", ErrBudgetExceeded)
	}
	amount := tokenAmount(s.MaxInputTokens, s.MaxOutputTokens)
	for attempt := 0; attempt < 32; attempt++ {
		a, err := r.Broker.Get(ctx, key)
		version := a.Version
		ledger := tokenLedger{Schema: 1, Caps: *s.DailyTokenBudget, Entries: map[string]tokenEntry{}}
		if err == nil {
			if err := decodeTokenLedger(a.Content, &ledger); err != nil {
				return key, err
			}
			if ledger.Caps != *s.DailyTokenBudget {
				return key, errors.New("scope already has a different daily token budget")
			}
			if ledger.Frozen {
				return key, fmt.Errorf("%w: daily token ledger frozen after a reported overrun", ErrBudgetExceeded)
			}
		} else if brokerCode(err, "not_found") {
			version = 0
		} else {
			return key, err
		}
		if _, exists := ledger.Entries[id]; exists {
			return key, errors.New("daily token reservation ID already exists")
		}
		if len(ledger.Entries) >= 1000 {
			return key, errors.New("daily token ledger is full")
		}
		total, err := totalTokenEntries(ledger.Entries)
		if err != nil {
			return key, err
		}
		requested, err := addTokens(total, amount)
		if err != nil {
			return key, err
		}
		if tokenOver(requested, ledger.Caps) {
			// Establish a new policy even when its first task cannot fit. An
			// omitted policy on the next task must not bypass that registration.
			if version == 0 {
				_, err = r.publish(ctx, key, 0, fmt.Sprintf("%s-token-policy-%d", id, attempt), ledger)
				if brokerCode(err, "version_conflict") {
					continue
				}
				if err != nil {
					return key, err
				}
			}
			return key, fmt.Errorf("%w: daily token allowance exhausted", ErrBudgetExceeded)
		}
		ledger.Entries[id] = tokenEntry{Reserved: amount}
		_, err = r.publish(ctx, key, version, fmt.Sprintf("%s-tokens-%d", id, attempt), ledger)
		if err == nil {
			return key, nil
		}
		if !brokerCode(err, "version_conflict") {
			return key, err
		}
		if err = ctx.Err(); err != nil {
			return key, err
		}
	}
	return key, errors.New("daily token contention exceeded 32 attempts; no generation started")
}

func totalTokenEntries(entries map[string]tokenEntry) (TokenBudget, error) {
	var total TokenBudget
	for _, entry := range entries {
		amount := entry.Reserved
		if entry.Actual != nil {
			amount = *entry.Actual
		}
		var err error
		total, err = addTokens(total, amount)
		if err != nil {
			return TokenBudget{}, err
		}
	}
	return total, nil
}

// settleTokens also handles a confirmed-not-generated rollback. Absent entries
// may be ignored only for that rollback, never after attempted generation.
func (r Runner) settleTokens(ctx context.Context, key, id string, actual TokenBudget, rollback bool) error {
	for attempt := 0; attempt < 32; attempt++ {
		a, err := r.Broker.Get(ctx, key)
		if rollback && brokerCode(err, "not_found") {
			return nil
		}
		if err != nil {
			return err
		}
		var ledger tokenLedger
		if err := decodeTokenLedger(a.Content, &ledger); err != nil {
			return err
		}
		entry, ok := ledger.Entries[id]
		if !ok {
			if rollback {
				return nil
			}
			return errors.New("daily token reservation missing")
		}
		if entry.Actual != nil {
			if *entry.Actual == actual {
				return nil
			}
			return errors.New("daily token reservation already settled with different usage")
		}
		if actual.InputTokens < 0 || actual.OutputTokens < 0 || actual.InputTokens > 1e9 || actual.OutputTokens > 1e9 || actual.TotalTokens != actual.InputTokens+actual.OutputTokens {
			return errors.New("invalid daily token settlement")
		}
		if tokenOver(actual, entry.Reserved) {
			ledger.Frozen = true
		}
		entry.Actual = &actual
		ledger.Entries[id] = entry
		_, err = r.publish(ctx, key, a.Version, fmt.Sprintf("%s-token-settle-%d", id, attempt), ledger)
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
	return errors.New("daily token settlement contention; original reservation retained")
}

func decodeTokenLedger(content string, ledger *tokenLedger) error {
	invalid := errors.New("invalid daily token ledger")
	if len(content) > protocol.MaxContentBytes || !utf8.ValidString(content) {
		return invalid
	}
	d := json.NewDecoder(strings.NewReader(content))
	d.UseNumber()
	value, err := tokenJSONValue(d, 0)
	if err != nil {
		return invalid
	}
	if _, err := d.Token(); err != io.EOF {
		return invalid
	}
	if !tokenObjectKeys(value, "schema", "caps", "frozen", "entries") {
		return invalid
	}
	root := value.(map[string]any)
	if !tokenAmountObject(root["caps"]) {
		return invalid
	}
	entries, ok := root["entries"].(map[string]any)
	if !ok || len(entries) > 1000 {
		return invalid
	}
	for id, value := range entries {
		if !validID(id, 128) || !tokenObjectKeys(value, "reserved", "actual") {
			return invalid
		}
		entry := value.(map[string]any)
		if !tokenAmountObject(entry["reserved"]) {
			return invalid
		}
		if actual, exists := entry["actual"]; exists && !tokenAmountObject(actual) {
			return invalid
		}
	}
	if json.Unmarshal([]byte(content), ledger) != nil || ledger.Schema != 1 || !validTokenBudget(ledger.Caps) || ledger.Entries == nil {
		return invalid
	}
	for _, entry := range ledger.Entries {
		b := entry.Reserved
		if b.InputTokens < 1 || b.InputTokens > 1e6 || b.OutputTokens < 16 || b.OutputTokens > 1e6 || b.TotalTokens != b.InputTokens+b.OutputTokens {
			return invalid
		}
		if a := entry.Actual; a != nil {
			if a.InputTokens < 0 || a.InputTokens > 1e9 || a.OutputTokens < 0 || a.OutputTokens > 1e9 || a.TotalTokens != a.InputTokens+a.OutputTokens || (tokenOver(*a, b) && !ledger.Frozen) {
				return invalid
			}
		}
	}
	total, err := totalTokenEntries(ledger.Entries)
	if err != nil || (tokenOver(total, ledger.Caps) && !ledger.Frozen) {
		return invalid
	}
	return nil
}

func tokenObjectKeys(value any, allowed ...string) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for key := range object {
		found := false
		for _, want := range allowed {
			if key == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func tokenAmountObject(value any) bool {
	object, ok := value.(map[string]any)
	return ok && len(object) == 3 && tokenObjectKeys(value, "input_tokens", "output_tokens", "total_tokens")
}

// Reject duplicates, nulls and deep objects before typed JSON decoding, whose
// permissive case/duplicate matching must not alter a shared quota policy.
func tokenJSONValue(d *json.Decoder, depth int) (any, error) {
	if depth > 8 {
		return nil, errors.New("invalid token JSON")
	}
	t, err := d.Token()
	if err != nil || t == nil {
		return nil, errors.New("invalid token JSON")
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return t, nil
	}
	if delim != '{' {
		return nil, errors.New("invalid token JSON")
	}
	object := map[string]any{}
	for d.More() {
		t, err := d.Token()
		key, ok := t.(string)
		if err != nil || !ok {
			return nil, errors.New("invalid token JSON")
		}
		if _, exists := object[key]; exists {
			return nil, errors.New("duplicate token JSON")
		}
		value, err := tokenJSONValue(d, depth+1)
		if err != nil {
			return nil, err
		}
		object[key] = value
	}
	if t, err := d.Token(); err != nil || t != json.Delim('}') {
		return nil, errors.New("invalid token JSON")
	}
	return object, nil
}
