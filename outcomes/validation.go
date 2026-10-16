package outcomes

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/protocol"
)

type ledger struct {
	Schema    int                `json:"schema"`
	Task      Task               `json:"task"`
	CreatedAt time.Time          `json:"created_at"`
	Runs      []RecordedRun      `json:"runs"`
	Events    []RecordedFeedback `json:"events"`
}

// Key uses an unambiguous JSON tuple, so no delimiter in an ID can collide with
// a different task or revision. Broker authentication supplies the scope.
func Key(task Task) (string, error) {
	if err := validateTask(task); err != nil {
		return "", err
	}
	b, _ := json.Marshal([2]string{task.ID, task.Revision})
	d := sha256.Sum256(b)
	return "outcomes/v1/tasks/" + hex.EncodeToString(d[:]), nil
}

func invalid(message string) error { return fmt.Errorf("%w: %s", ErrInvalid, message) }

func validateTask(t Task) error {
	for _, s := range []string{t.ID, t.Revision} {
		if len(s) < 1 || len(s) > 256 || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
			return invalid("task ID and revision must be bounded nonempty UTF-8")
		}
		for _, r := range s {
			if unicode.IsControl(r) {
				return invalid("task identifiers cannot contain controls")
			}
		}
	}
	return nil
}

func identifier(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("._:-", c)) {
			return false
		}
	}
	return true
}

func hash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func finiteNonnegative(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }

func validateUsage(u inference.Usage) error {
	for _, n := range []int64{u.InputTokens, u.UncachedInputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.OutputTokens, u.ReasoningTokens} {
		if n < 0 {
			return invalid("negative token usage")
		}
	}
	if u.UncachedInputTokens > math.MaxInt64-u.CacheReadTokens || u.UncachedInputTokens+u.CacheReadTokens > math.MaxInt64-u.CacheWriteTokens {
		return invalid("token usage overflow")
	}
	if u.InputTokens != u.UncachedInputTokens+u.CacheReadTokens+u.CacheWriteTokens || u.ReasoningTokens > u.OutputTokens {
		return invalid("token usage must use exclusive normalized buckets")
	}
	return nil
}

func validateAttempt(a Attempt) error {
	if a.TierID != "" && !identifier(a.TierID) {
		return invalid("invalid tier ID")
	}
	if a.Fingerprint != "" && !hash(a.Fingerprint) || a.OutputSHA256 != "" && !hash(a.OutputSHA256) {
		return invalid("attempt hashes must be lowercase SHA-256")
	}
	if a.Complete && (a.Fingerprint == "" || a.OutputSHA256 == "") {
		return invalid("complete attempts require fingerprint and output hashes")
	}
	if !a.UsageKnown && a.Usage != (inference.Usage{}) {
		return invalid("unknown usage cannot contain guessed counters")
	}
	if err := validateUsage(a.Usage); err != nil {
		return err
	}
	if a.EstimatedCostUSD != nil && !finiteNonnegative(*a.EstimatedCostUSD) {
		return invalid("cost must be finite and nonnegative")
	}
	// Deterministic transformations complete without model generation. Keep
	// this additive basis distinct from legacy no_generation (incomplete)
	// records, and never permit a failed transformation to claim durability.
	if a.CostBasis == "deterministic_edit" {
		if a.TierID != "checked_revision" || a.Reused || a.GenerationCalled || !a.Complete || !a.UsageKnown || a.Usage != (inference.Usage{}) || a.EstimatedCostUSD == nil || *a.EstimatedCostUSD != 0 || (a.Durable && !a.CheckPassed) {
			return invalid("deterministic edits require completed checked revision work with zero generation usage and charge")
		}
		return nil
	}
	if a.Reused {
		if a.GenerationCalled || !a.Complete || !a.Durable || !a.UsageKnown || a.Usage != (inference.Usage{}) || a.CostBasis != "reuse_zero" || a.EstimatedCostUSD == nil || *a.EstimatedCostUSD != 0 {
			return invalid("reuse must be complete durable with zero new generation usage and charge")
		}
		return nil
	}
	if !a.GenerationCalled {
		if a.Complete || !a.UsageKnown || a.Usage != (inference.Usage{}) || a.CostBasis != "no_generation" || a.EstimatedCostUSD == nil || *a.EstimatedCostUSD != 0 {
			return invalid("uncalled generation requires explicit zero usage and charge")
		}
		return nil
	}
	switch a.CostBasis {
	case "unknown":
		if a.EstimatedCostUSD != nil {
			return invalid("unknown cost must be null")
		}
	case "caller_estimate":
		if a.EstimatedCostUSD == nil || !a.UsageKnown {
			return invalid("caller estimate requires known usage and an explicit cost")
		}
	case "local_token_charge":
		if a.EstimatedCostUSD == nil || *a.EstimatedCostUSD != 0 {
			return invalid("local token charge must be explicitly zero; hardware cost is excluded")
		}
	default:
		return invalid("invalid generation cost basis")
	}
	return nil
}

func validateRun(r Run) error {
	if !identifier(r.ID) || !hash(r.RequestDigest) {
		return invalid("run requires a bounded ID and request digest")
	}
	if len(r.Attempts) > 2 {
		return invalid("run cannot contain more than two attempts")
	}
	for _, a := range r.Attempts {
		if err := validateAttempt(a); err != nil {
			return err
		}
	}
	if len(r.Attempts) == 2 {
		first := r.Attempts[0]
		if first.CheckPassed || !first.Complete || !first.Durable || !first.UsageKnown {
			return invalid("a second attempt requires a known complete durable failed first check")
		}
	}
	switch r.Status {
	case "completed":
		if len(r.Attempts) == 0 || !r.Attempts[len(r.Attempts)-1].CheckPassed {
			return invalid("completed run must finish with passing checks")
		}
		last := r.Attempts[len(r.Attempts)-1]
		if !last.Complete || !last.Durable || !last.UsageKnown {
			return invalid("completed run requires a known complete durable final attempt")
		}
	case "quality_failed":
		if len(r.Attempts) == 0 {
			return invalid("quality failure requires an attempt")
		}
		for _, a := range r.Attempts {
			if a.CheckPassed || !a.Complete || (!a.Durable && a.CostBasis != "deterministic_edit") || !a.UsageKnown {
				return invalid("quality failure requires known complete durable failed checks")
			}
		}
	case "stopped":
	default:
		return invalid("invalid run status")
	}
	return nil
}

func validateFeedback(f Feedback) error {
	if !identifier(f.ID) || !identifier(f.RunID) {
		return invalid("feedback requires bounded event and run IDs")
	}
	if f.HumanMinutes != nil && !finiteNonnegative(*f.HumanMinutes) {
		return invalid("human minutes must be finite and nonnegative")
	}
	if f.Kind != "accepted" && f.FinalArtifactSHA256 != "" {
		return invalid("only acceptance records a final artifact hash")
	}
	if f.Kind != "rewritten" && f.Reason != "" {
		return invalid("only rewrite records a reason")
	}
	switch f.Kind {
	case "accepted":
		if !hash(f.FinalArtifactSHA256) {
			return invalid("human acceptance requires a final artifact SHA-256")
		}
	case "rewritten":
		switch f.Reason {
		case "correctness", "style", "requirements", "other":
		default:
			return invalid("rewrite requires a declared reason")
		}
	case "rejected", "reopened":
	default:
		return invalid("invalid human feedback kind")
	}
	return nil
}

func validTime(t time.Time) bool { _, offset := t.Zone(); return !t.IsZero() && offset == 0 }

func validateLedger(l ledger, task Task, version int64) error {
	if l.Schema != 1 || l.Task != task || !validTime(l.CreatedAt) || len(l.Runs) < 1 || len(l.Runs) > MaxRuns || len(l.Events) > MaxEvents || version != int64(len(l.Runs)+len(l.Events)) {
		return invalid("stored ledger schema, task, limits, or version is invalid")
	}
	versions := make(map[int64]bool)
	runs := make(map[string]int64)
	events := make(map[string]bool)
	var knownCost, humanMinutes float64
	for _, r := range l.Runs {
		if err := validateRun(r.Run); err != nil {
			return err
		}
		if !validTime(r.CreatedAt) || r.Version < 1 || r.Version > version || versions[r.Version] || runs[r.ID] != 0 {
			return invalid("stored run ID, timestamp, or version is invalid")
		}
		versions[r.Version], runs[r.ID] = true, r.Version
		for _, a := range r.Attempts {
			if a.EstimatedCostUSD != nil {
				if err := addFinite(&knownCost, *a.EstimatedCostUSD); err != nil {
					return err
				}
			}
		}
	}
	for _, e := range l.Events {
		if err := validateFeedback(e.Feedback); err != nil {
			return err
		}
		if !validTime(e.CreatedAt) || e.Version < 2 || e.Version > version || e.ExpectedVersion != e.Version-1 || versions[e.Version] || events[e.ID] || runs[e.RunID] == 0 || runs[e.RunID] >= e.Version {
			return invalid("stored feedback link, ID, timestamp, or version is invalid")
		}
		versions[e.Version], events[e.ID] = true, true
		if e.HumanMinutes != nil {
			if err := addFinite(&humanMinutes, *e.HumanMinutes); err != nil {
				return err
			}
		}
	}
	for v := int64(1); v <= version; v++ {
		if !versions[v] {
			return invalid("stored ledger has a version gap")
		}
	}
	for i := 1; i < len(l.Runs); i++ {
		if l.Runs[i].Version <= l.Runs[i-1].Version {
			return invalid("stored runs are not in commit order")
		}
	}
	for i := 1; i < len(l.Events); i++ {
		if l.Events[i].Version <= l.Events[i-1].Version {
			return invalid("stored feedback is not in commit order")
		}
	}
	return nil
}

// Strict decoding rejects duplicate keys as well as unknown fields, invalid
// UTF-8, trailing JSON, and overlarge/deep data. Mutation never repairs a corrupt
// ledger by silently dropping fields or replacing it.
func decodeLedger(content string, task Task, version int64) (ledger, error) {
	var l ledger
	if len(content) > protocol.MaxContentBytes || !utf8.ValidString(content) {
		return l, invalid("stored ledger exceeds byte limit or contains invalid UTF-8")
	}
	d := json.NewDecoder(strings.NewReader(content))
	if err := uniqueJSON(d, 0); err != nil {
		return l, invalid("stored ledger is not strict JSON")
	}
	if _, err := d.Token(); err != io.EOF {
		return l, invalid("stored ledger has trailing data")
	}
	d = json.NewDecoder(strings.NewReader(content))
	d.DisallowUnknownFields()
	if err := d.Decode(&l); err != nil {
		return l, invalid("stored ledger fields are invalid")
	}
	if err := validateLedger(l, task, version); err != nil {
		return ledger{}, err
	}
	return l, nil
}

func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 32 {
		return ErrInvalid
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			t, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := t.(string)
			if !ok || seen[key] {
				return ErrInvalid
			}
			seen[key] = true
			if err := uniqueJSON(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueJSON(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return ErrInvalid
	}
	close, err := d.Token()
	if err != nil {
		return err
	}
	if delim == '{' && close != json.Delim('}') || delim == '[' && close != json.Delim(']') {
		return ErrInvalid
	}
	return nil
}

func same(a, b any) bool {
	aa, ea := json.Marshal(a)
	bb, eb := json.Marshal(b)
	return ea == nil && eb == nil && bytes.Equal(aa, bb)
}
