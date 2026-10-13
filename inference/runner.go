package inference

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/protocol"
)

var (
	ErrInFlight       = errors.New("request is pending or has an uncertain outcome; inspect its artifact before deliberately changing revision to retry")
	ErrIncomplete     = errors.New("provider returned incomplete output; result is saved but cannot be reused; inspect before changing revision")
	ErrBudgetExceeded = errors.New("request exceeds the configured token or cost budget")
)

// Runner coordinates workers via the existing broker API. Now may be set by
// tests; production uses the UTC wall clock. A pending claim never expires into
// another paid call: a crashed worker may already have incurred a charge.
type Runner struct {
	Broker   Broker
	Provider Provider
	Now      func() time.Time
}

type jobRecord struct {
	Schema         int       `json:"schema"`
	Fingerprint    string    `json:"fingerprint"`
	State          string    `json:"state"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	Response       *Response `json:"response,omitempty"`
	BudgetKey      string    `json:"budget_key,omitempty"`
	Reservation    string    `json:"reservation,omitempty"`
	ReservedMicros int64     `json:"reserved_micros,omitempty"`
}

func (r Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r Runner) Run(ctx context.Context, s Spec) (Result, error) {
	plan, err := BuildPlan(s)
	if err != nil {
		return Result{}, err
	}
	if r.Broker == nil || r.Provider == nil || r.Provider.Identity() == "" {
		return Result{}, errors.New("broker and identified provider are required")
	}
	digest := sha256.Sum256([]byte(plan.Fingerprint + "\x00" + r.Provider.Identity()))
	fingerprint := hex.EncodeToString(digest[:])
	key := "inference/v1/jobs/" + fingerprint
	a, err := r.Broker.Get(ctx, key)
	version := int64(0)
	if err == nil {
		version = a.Version
		var saved jobRecord
		if json.Unmarshal([]byte(a.Content), &saved) != nil || saved.Schema != 1 || saved.Fingerprint != fingerprint {
			return Result{}, errors.New("invalid stored inference record")
		}
		switch saved.State {
		case "pending", "uncertain":
			return Result{}, ErrInFlight
		case "incomplete":
			return Result{Fingerprint: fingerprint}, ErrIncomplete
		case "limit_exceeded":
			return Result{Fingerprint: fingerprint}, ErrBudgetExceeded
		case "complete":
			if saved.Response == nil || !saved.Response.Complete {
				return Result{}, errors.New("invalid stored completed response")
			}
			if s.ReuseForSeconds > 0 && r.now().Before(saved.ExpiresAt) && r.now().Before(saved.CreatedAt.Add(time.Duration(s.ReuseForSeconds)*time.Second)) {
				return savedResult(saved, s, true), nil
			}
		case "blocked": // A preflight rejection never called Generate; safe to retry.
		default:
			return Result{}, errors.New("unknown stored inference state")
		}
	} else if !brokerCode(err, "not_found") {
		return Result{}, err
	}

	claimID, err := operationID()
	if err != nil {
		return Result{}, err
	}
	record := jobRecord{Schema: 1, Fingerprint: fingerprint, State: "pending", CreatedAt: r.now()}
	a, err = r.publish(ctx, key, version, claimID, record)
	if err != nil {
		if brokerCode(err, "version_conflict") {
			return Result{}, ErrInFlight
		}
		return Result{}, err
	}
	version = a.Version
	// Count after winning ownership, so simultaneous workers do not even
	// duplicate the provider token-count request. It performs no generation.
	inputTokens, err := r.Provider.CountInputTokens(ctx, plan.Request)
	if err != nil {
		r.saveState(key, version, record, "blocked")
		return Result{}, fmt.Errorf("token preflight failed: %w", err)
	}
	if inputTokens < 1 || inputTokens > s.MaxInputTokens {
		r.saveState(key, version, record, "blocked")
		return Result{}, ErrBudgetExceeded
	}
	upper, err := upperCost(inputTokens, s)
	if err != nil {
		r.saveState(key, version, record, "blocked")
		return Result{}, err
	}
	if s.MaxCostUSD > 0 && upper > capMicros(s.MaxCostUSD) {
		r.saveState(key, version, record, "blocked")
		return Result{}, ErrBudgetExceeded
	}
	budgetKey, err := r.reserve(ctx, s, claimID, upper)
	if err != nil {
		r.saveState(key, version, record, "blocked")
		return Result{}, err
	}
	record.BudgetKey, record.Reservation, record.ReservedMicros = budgetKey, claimID, upper
	// Persist the reservation's identity before a paid call. If this write
	// fails, keep the reservation and pending claim, and do not generate.
	if budgetKey != "" {
		a, err = r.publish(ctx, key, version, claimID+"-reserved", record)
		if err != nil {
			return Result{}, err
		}
		version = a.Version
	}
	response, err := r.Provider.Generate(ctx, plan.Request)
	if err != nil {
		r.saveState(key, version, record, "uncertain")
		return Result{}, fmt.Errorf("generation outcome uncertain; reservation retained: %w", err)
	}
	if err = validateUsage(response.Usage); err != nil {
		r.saveState(key, version, record, "uncertain")
		return Result{}, err
	}
	// Provider counts may be estimates. Validate the actual billable usage
	// before marking any answer reusable, including when only a per-call cap
	// was configured. A cached answer must never erase a limit violation.
	var postflight error
	actual := int64(0)
	if s.Prices != nil {
		actual, err = costMicros(response.Usage, *s.Prices)
		if err != nil {
			r.saveState(key, version, record, "uncertain")
			return Result{}, err
		}
		if (s.MaxCostUSD > 0 && actual > capMicros(s.MaxCostUSD)) || (budgetKey != "" && actual > upper) {
			postflight = fmt.Errorf("%w: reported cost exceeded the preflight limit; inspect prices and provider counting", ErrBudgetExceeded)
		}
	}
	if response.Usage.InputTokens > s.MaxInputTokens || response.Usage.OutputTokens > s.MaxOutputTokens {
		postflight = fmt.Errorf("%w: provider reported usage above the configured token cap", ErrBudgetExceeded)
	}
	if postflight != nil {
		response.Complete = false
	}
	record.Response = &response
	if postflight != nil {
		record.State = "limit_exceeded"
	} else if response.Complete {
		record.State = "complete"
	} else {
		record.State = "incomplete"
	}
	record.CreatedAt = r.now()
	record.ExpiresAt = record.CreatedAt.Add(time.Duration(s.ReuseForSeconds) * time.Second)
	result := savedResult(record, s, false)
	var publicationError, settlementError error
	if _, err = r.publish(ctx, key, version, claimID+"-result", record); err != nil {
		result.Durable = false
		publicationError = fmt.Errorf("result publication failed; answer is available but not confirmed durable; do not regenerate automatically: %w", err)
	}
	if budgetKey != "" {
		// Known billing must be reconciled even if the answer could not fit in
		// the job artifact or that publication failed. In particular, persist a
		// measured overrun/freeze whenever the ledger remains writable.
		settleCtx := ctx
		if ctx.Err() != nil {
			var cancel context.CancelFunc
			settleCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
		}
		if err = r.settle(settleCtx, budgetKey, claimID, actual); err != nil {
			settlementError = fmt.Errorf("budget settlement not confirmed; stop this scope and inspect its retained reservation: %w", err)
		}
	}
	if err = errors.Join(postflight, publicationError, settlementError); err != nil {
		return result, err
	}
	if !response.Complete {
		return result, ErrIncomplete
	}
	return result, nil
}

func validateUsage(u Usage) error {
	for _, n := range []int64{u.InputTokens, u.UncachedInputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.OutputTokens, u.ReasoningTokens} {
		if n < 0 || n > 1000000000 {
			return errors.New("invalid provider usage")
		}
	}
	if u.InputTokens != u.UncachedInputTokens+u.CacheReadTokens+u.CacheWriteTokens || u.ReasoningTokens > u.OutputTokens {
		return errors.New("inconsistent provider usage")
	}
	return nil
}

func savedResult(rec jobRecord, s Spec, reused bool) Result {
	result := Result{Fingerprint: rec.Fingerprint, Reused: reused, Durable: true, CreatedAt: rec.CreatedAt, ExpiresAt: rec.ExpiresAt}
	if rec.Response == nil {
		return result
	}
	result.Response = *rec.Response
	result.GenerationUsage = rec.Response.Usage
	if reused {
		result.Response.Usage = Usage{}
		result.AvoidedUsage = rec.Response.Usage
	} else {
		result.ThisRunUsage = rec.Response.Usage
	}
	if s.Prices != nil {
		micros, err := costMicros(rec.Response.Usage, *s.Prices)
		if err == nil {
			cost := float64(micros) / 1000000
			zero := float64(0)
			if reused {
				result.CostUSD = &zero
				result.AvoidedCostUSD = &cost
			} else {
				result.CostUSD = &cost
			}
		}
	}
	return result
}

func operationID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "model-" + hex.EncodeToString(b[:]), nil
}

func brokerCode(err error, code string) bool {
	var e *client.Error
	return errors.As(err, &e) && e.Code == code
}

func (r Runner) publish(ctx context.Context, key string, version int64, id string, v any) (protocol.Artifact, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return protocol.Artifact{}, err
	}
	if len(b) > protocol.MaxContentBytes {
		return protocol.Artifact{}, errors.New("inference record exceeds broker artifact limit")
	}
	request := protocol.PublishRequest{Key: key, ExpectedVersion: version, OperationID: id, Content: string(b)}
	result, err := r.Broker.Publish(ctx, request)
	if err != nil {
		var app *client.Error
		if !errors.As(err, &app) {
			// Only publication is retried, with the EXACT operation and content.
			// A lost acknowledgement can never trigger another Generate call.
			result, err = r.Broker.Publish(ctx, request)
		}
	}
	return result.Artifact, err
}

func (r Runner) saveState(key string, version int64, record jobRecord, state string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	record.State = state
	id, err := operationID()
	if err == nil {
		_, _ = r.publish(ctx, key, version, id, record)
	}
	// If this fails, the durable pending claim still prohibits regeneration.
}
