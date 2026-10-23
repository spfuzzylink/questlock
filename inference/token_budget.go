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

var errInvalidTokenLedger = errors.New("invalid daily token ledger")

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
		total, err := totalTokenEntries(ledger.Entries)
		if err != nil {
			return key, err
		}
		diagnostic := tokenBudgetDiagnostic(ledger.Caps, total, tokenAmount(s.MaxInputTokens, s.MaxOutputTokens))
		diagnostic.Reason = "policy_required"
		return key, &BudgetError{Diagnostic: diagnostic}
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
				total, err := totalTokenEntries(ledger.Entries)
				if err != nil {
					return key, err
				}
				diagnostic := tokenBudgetDiagnostic(ledger.Caps, total, amount)
				diagnostic.Reason = "frozen_after_overrun"
				return key, &BudgetError{Diagnostic: diagnostic}
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
			return key, &BudgetError{Diagnostic: tokenBudgetDiagnostic(ledger.Caps, total, amount)}
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

func tokenBudgetDiagnostic(caps, current, amount TokenBudget) BudgetDiagnostic {
	kind, limit, used, reserved := "daily_total_tokens", caps.TotalTokens, current.TotalTokens, amount.TotalTokens
	if current.InputTokens+amount.InputTokens > caps.InputTokens {
		kind, limit, used, reserved = "daily_input_tokens", caps.InputTokens, current.InputTokens, amount.InputTokens
	} else if current.OutputTokens+amount.OutputTokens > caps.OutputTokens {
		kind, limit, used, reserved = "daily_output_tokens", caps.OutputTokens, current.OutputTokens, amount.OutputTokens
	}
	return BudgetDiagnostic{LimitKind: kind, ConfiguredLimit: limit, Unit: "tokens", Reason: "exceeds_limit", CountBasis: "configured_reservation", Required: used + reserved, CurrentUsage: used, RequestedReservation: reserved}
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
	invalid := errInvalidTokenLedger
	if len(content) > protocol.MaxContentBytes || !utf8.ValidString(content) {
		return invalid
	}
	d := json.NewDecoder(strings.NewReader(content))
	d.UseNumber()
	// Decode directly to the existing typed record. The schema fixes the
	// nesting depth; no intermediate per-entry generic maps or second parse.
	if !tokenDelimiter(d, '{') {
		return invalid
	}
	decoded := tokenLedger{}
	seen := uint8(0)
	for d.More() {
		name, ok := tokenName(d)
		if !ok {
			return invalid
		}
		var bit uint8
		var err error
		switch name {
		case "schema":
			bit = 1
			if seen&bit != 0 {
				return invalid
			}
			version, ok := tokenInteger(d)
			if !ok || version != 1 {
				return invalid
			}
			decoded.Schema = 1
		case "caps":
			bit = 2
			if seen&bit != 0 {
				return invalid
			}
			decoded.Caps, err = decodeTokenAmount(d)
		case "entries":
			bit = 4
			if seen&bit != 0 {
				return invalid
			}
			decoded.Entries, err = decodeTokenEntries(d)
		case "frozen":
			bit = 8
			if seen&bit != 0 {
				return invalid
			}
			value, readErr := d.Token()
			frozen, ok := value.(bool)
			if readErr != nil || !ok {
				return invalid
			}
			decoded.Frozen = frozen
		default:
			return invalid
		}
		if err != nil {
			return invalid
		}
		seen |= bit
	}
	if seen&7 != 7 || !tokenDelimiter(d, '}') {
		return invalid
	}
	if _, err := d.Token(); err != io.EOF {
		return invalid
	}
	if !validTokenBudget(decoded.Caps) {
		return invalid
	}
	*ledger = decoded
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

func tokenDelimiter(d *json.Decoder, want json.Delim) bool {
	value, err := d.Token()
	return err == nil && value == want
}

func tokenName(d *json.Decoder) (string, bool) {
	value, err := d.Token()
	name, ok := value.(string)
	return name, err == nil && ok
}

func tokenInteger(d *json.Decoder) (int64, bool) {
	value, err := d.Token()
	number, ok := value.(json.Number)
	if err != nil || !ok {
		return 0, false
	}
	n, err := number.Int64()
	return n, err == nil
}

// The required-field masks reject missing/duplicate keys even when their
// zero value would otherwise describe a valid released reservation. Token
// typing rejects nulls, arrays, aliases and fractional or overflowing numbers.
func decodeTokenAmount(d *json.Decoder) (TokenBudget, error) {
	invalid := errInvalidTokenLedger
	if !tokenDelimiter(d, '{') {
		return TokenBudget{}, invalid
	}
	var result TokenBudget
	seen := uint8(0)
	for d.More() {
		name, ok := tokenName(d)
		if !ok {
			return TokenBudget{}, invalid
		}
		var bit uint8
		var dest *int64
		switch name {
		case "input_tokens":
			bit, dest = 1, &result.InputTokens
		case "output_tokens":
			bit, dest = 2, &result.OutputTokens
		case "total_tokens":
			bit, dest = 4, &result.TotalTokens
		default:
			return TokenBudget{}, invalid
		}
		if seen&bit != 0 {
			return TokenBudget{}, invalid
		}
		value, ok := tokenInteger(d)
		if !ok {
			return TokenBudget{}, invalid
		}
		*dest = value
		seen |= bit
	}
	if seen != 7 || !tokenDelimiter(d, '}') {
		return TokenBudget{}, invalid
	}
	return result, nil
}

func decodeTokenEntry(d *json.Decoder) (tokenEntry, error) {
	invalid := errInvalidTokenLedger
	if !tokenDelimiter(d, '{') {
		return tokenEntry{}, invalid
	}
	var result tokenEntry
	seen := uint8(0)
	for d.More() {
		name, ok := tokenName(d)
		if !ok {
			return tokenEntry{}, invalid
		}
		var bit uint8
		switch name {
		case "reserved":
			bit = 1
		case "actual":
			bit = 2
		default:
			return tokenEntry{}, invalid
		}
		if seen&bit != 0 {
			return tokenEntry{}, invalid
		}
		value, err := decodeTokenAmount(d)
		if err != nil {
			return tokenEntry{}, invalid
		}
		if bit == 1 {
			result.Reserved = value
		} else {
			actual := value
			result.Actual = &actual
		}
		seen |= bit
	}
	if seen&1 == 0 || !tokenDelimiter(d, '}') {
		return tokenEntry{}, invalid
	}
	return result, nil
}

func decodeTokenEntries(d *json.Decoder) (map[string]tokenEntry, error) {
	invalid := errInvalidTokenLedger
	if !tokenDelimiter(d, '{') {
		return nil, invalid
	}
	entries := make(map[string]tokenEntry)
	for d.More() {
		if len(entries) >= 1000 {
			return nil, invalid
		}
		id, ok := tokenName(d)
		if !ok || !validID(id, 128) {
			return nil, invalid
		}
		if _, duplicate := entries[id]; duplicate {
			return nil, invalid
		}
		entry, err := decodeTokenEntry(d)
		if err != nil {
			return nil, invalid
		}
		entries[id] = entry
	}
	if !tokenDelimiter(d, '}') {
		return nil, invalid
	}
	return entries, nil
}
