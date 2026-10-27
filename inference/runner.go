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
	BudgetDiagnostic *BudgetDiagnostic `json:"budget_diagnostic,omitempty"`
	// RequestFingerprint binds the approved plan without persisting a serving
	// runtime identity. Legacy records omit it and remain usable for inference.
	RequestFingerprint string           `json:"request_fingerprint,omitempty"`
	Schema             int              `json:"schema"`
	Fingerprint        string           `json:"fingerprint"`
	State              string           `json:"state"`
	CreatedAt          time.Time        `json:"created_at"`
	ExpiresAt          time.Time        `json:"expires_at"`
	Response           *Response        `json:"response,omitempty"`
	BudgetKey          string           `json:"budget_key,omitempty"`
	Reservation        string           `json:"reservation,omitempty"`
	ReservedMicros     int64            `json:"reserved_micros,omitempty"`
	TokenBudgetKey     string           `json:"token_budget_key,omitempty"`
	Resolution         *ReuseResolution `json:"resolution,omitempty"`
}

func (r Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r Runner) Run(ctx context.Context, s Spec) (result Result, runErr error) {
	return r.run(ctx, s, nil)
}

// RunWithResolver preserves exact reuse and claim ownership, then consults the
// explicit resolver before any target token count, reservation or generation.
func (r Runner) RunWithResolver(ctx context.Context, s Spec, resolver ReuseResolver) (Result, error) {
	return r.run(ctx, s, resolver)
}

func (r Runner) run(ctx context.Context, s Spec, resolver ReuseResolver) (result Result, runErr error) {
	started := time.Now()
	measurement := &Measurement{StartedAt: started.UTC(), PhaseMS: make(map[string]float64)}
	defer func() {
		if diagnostic := BudgetDiagnosticFromError(runErr); diagnostic != nil {
			// A replayed limit breach retains the original generation evidence;
			// Measurement describes whether this invocation called Generate.
			if measurement.GenerationCalled && !diagnostic.GenerationCalled {
				diagnostic.GenerationCalled = true
				// Preserve other typed causes without mutating provider-owned
				// evidence or losing this invocation's Generate-call boundary.
				runErr = errors.Join(&BudgetError{Diagnostic: *diagnostic}, runErr)
			}
			result.BudgetDiagnostic = diagnostic
		}
		measurement.EndedAt = time.Now().UTC()
		measurement.TotalMS = milliseconds(time.Since(started))
		if measurement.Outcome == "" {
			switch {
			case runErr == nil && result.Reused:
				measurement.Outcome = "reused"
			case runErr == nil:
				measurement.Outcome = "complete"
			case errors.Is(runErr, ErrInFlight):
				measurement.Outcome = "pending"
			case errors.Is(runErr, ErrIncomplete):
				measurement.Outcome = "incomplete"
			case errors.Is(runErr, ErrBudgetExceeded) && measurement.GenerationCalled:
				measurement.Outcome = "limit_exceeded"
			case errors.Is(runErr, ErrBudgetExceeded):
				measurement.Outcome = "budget_blocked"
			case measurement.GenerationCalled && !measurement.KnownGenerationUsage:
				measurement.Outcome = "uncertain"
			case result.Fingerprint != "" && !result.Durable:
				measurement.Outcome = "not_durable"
			default:
				measurement.Outcome = "error"
			}
		}
		result.Measurement = measurement
	}()
	saveState := func(key string, version int64, record jobRecord, state string) {
		phase := time.Now()
		r.saveState(key, version, record, state)
		measurement.PhaseMS["recovery"] += milliseconds(time.Since(phase))
		if state == "uncertain" {
			measurement.Outcome = "uncertain"
		}
	}
	phase := time.Now()
	plan, err := BuildPlan(s)
	measurement.PhaseMS["plan"] = milliseconds(time.Since(phase))
	if err != nil {
		return Result{}, err
	}
	if r.Broker == nil || r.Provider == nil || r.Provider.Identity() == "" {
		return Result{}, errors.New("broker and identified provider are required")
	}
	// Mutable serving runtimes must validate their captured identity even on
	// a completed-result hit. The check cannot make administrator mutations
	// atomic; deployments still require immutable weights/configuration.
	if guarded, ok := r.Provider.(interface{ RefreshIdentity(context.Context) error }); ok {
		phase = time.Now()
		err = guarded.RefreshIdentity(ctx)
		measurement.PhaseMS["runtime_identity"] = milliseconds(time.Since(phase))
		if err != nil {
			return Result{}, err
		}
	}
	digest := sha256.Sum256([]byte(plan.Fingerprint + "\x00" + r.Provider.Identity()))
	fingerprint := hex.EncodeToString(digest[:])
	measurement.Fingerprint = fingerprint
	measurement.ContextBytes, measurement.TotalContextBytes = plan.ContextBytes, plan.TotalContextBytes
	measurement.SelectedContext, measurement.OmittedContext = len(plan.Selected), len(plan.Omitted)
	key := "inference/v1/jobs/" + fingerprint
	phase = time.Now()
	a, err := r.Broker.Get(ctx, key)
	measurement.PhaseMS["lookup"] = milliseconds(time.Since(phase))
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
			if saved.BudgetDiagnostic != nil && validBudgetDiagnostic(*saved.BudgetDiagnostic) {
				return Result{Fingerprint: fingerprint}, &BudgetError{Diagnostic: *saved.BudgetDiagnostic}
			}
			return Result{Fingerprint: fingerprint}, ErrBudgetExceeded
		case "complete":
			if saved.Response == nil || !saved.Response.Complete {
				return Result{}, errors.New("invalid stored completed response")
			}
			if s.ReuseForSeconds > 0 && r.now().Before(saved.ExpiresAt) && r.now().Before(saved.CreatedAt.Add(time.Duration(s.ReuseForSeconds)*time.Second)) {
				return savedResult(saved, s, true), nil
			}
		case "resolved":
			if saved.Resolution == nil || saved.RequestFingerprint != plan.Fingerprint {
				return Result{}, errors.New("invalid stored resolved response")
			}
			if resolver != nil && resolver.Identity() != "" && resolver.Identity() == saved.Resolution.PolicyID {
				source, err := r.validateResolution(ctx, s, saved.Resolution)
				if err != nil {
					return Result{}, err
				}
				if saved.Response == nil || !sameResponse(*saved.Response, saved.Resolution.Response) || !saved.CreatedAt.Equal(source.CreatedAt) || saved.ExpiresAt.After(source.ExpiresAt) || !validReuseWindow(saved.CreatedAt, saved.ExpiresAt, s.ReuseForSeconds, r.now()) {
					return Result{}, errors.New("invalid or expired stored resolved response")
				}
				return resolvedResult(saved, s), nil
			}
			// This target never generated. A caller that did not authorize the
			// recorded policy can replace it through ordinary CAS admission.
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
	record := jobRecord{Schema: 1, Fingerprint: fingerprint, RequestFingerprint: plan.Fingerprint, State: "pending", CreatedAt: r.now()}
	if s.DailyTokenBudget != nil {
		// Keep a direct recovery link even if the later reserved-record publish
		// fails after a ledger committed. Older no-quota records stay unchanged.
		record.Reservation = claimID
	}
	phase = time.Now()
	a, err = r.publish(ctx, key, version, claimID, record)
	measurement.PhaseMS["admission"] = milliseconds(time.Since(phase))
	if err != nil {
		if brokerCode(err, "version_conflict") {
			return Result{}, ErrInFlight
		}
		return Result{}, err
	}
	version = a.Version
	if resolver != nil {
		phase = time.Now()
		policyID := resolver.Identity()
		if !validReuseID(policyID) {
			saveState(key, version, record, "blocked")
			return Result{}, errors.New("reuse resolver identity is invalid")
		}
		resolution, resolveErr := resolver.Resolve(ctx, s)
		measurement.PhaseMS["reuse_resolution"] = milliseconds(time.Since(phase))
		if resolveErr != nil {
			saveState(key, version, record, "blocked")
			return Result{}, fmt.Errorf("reuse resolution failed: %w", resolveErr)
		}
		if resolution != nil {
			if resolution.PolicyID != policyID || resolver.Identity() != policyID {
				saveState(key, version, record, "blocked")
				return Result{}, errors.New("reuse resolution policy differs from resolver identity")
			}
			source, resolveErr := r.validateResolution(ctx, s, resolution)
			if resolveErr != nil {
				saveState(key, version, record, "blocked")
				return Result{}, resolveErr
			}
			record.State, record.Resolution = "resolved", cloneResolution(resolution)
			record.Response = &record.Resolution.Response
			record.Reservation = "" // The target never reserved generation quota.
			record.CreatedAt, record.ExpiresAt = source.CreatedAt, source.ExpiresAt
			if cap := source.CreatedAt.Add(time.Duration(s.ReuseForSeconds) * time.Second); cap.Before(record.ExpiresAt) {
				record.ExpiresAt = cap
			}
			phase = time.Now()
			_, err = r.publish(ctx, key, version, claimID+"-resolved", record)
			measurement.PhaseMS["publication"] = milliseconds(time.Since(phase))
			if err != nil {
				return Result{Fingerprint: fingerprint}, fmt.Errorf("resolved result publication unconfirmed; do not regenerate automatically: %w", err)
			}
			return resolvedResult(record, s), nil
		}
	}
	// Count after winning ownership, so simultaneous workers do not even
	// duplicate the provider token-count request. It performs no generation.
	phase = time.Now()
	inputTokens, err := r.Provider.CountInputTokens(ctx, plan.Request)
	measurement.PhaseMS["preflight"] = milliseconds(time.Since(phase))
	if err != nil {
		saveState(key, version, record, "blocked")
		return Result{}, fmt.Errorf("token preflight failed: %w", err)
	}
	if inputTokens < 1 || inputTokens > s.MaxInputTokens {
		saveState(key, version, record, "blocked")
		diagnostic := inputDiagnostic("input_tokens", s.MaxInputTokens, inputTokens, inputCountBasis(r.Provider), s.MaxOutputTokens)
		if inputTokens < 1 {
			diagnostic.Reason, diagnostic.Required, diagnostic.CountedInputTokens, diagnostic.EstimatedInputTokens = "invalid_count", 0, 0, 0
		}
		return Result{}, &BudgetError{Diagnostic: diagnostic}
	}
	upper, err := upperCost(inputTokens, s)
	if err != nil {
		saveState(key, version, record, "blocked")
		return Result{}, err
	}
	if s.MaxCostUSD > 0 && upper > capMicros(s.MaxCostUSD) {
		saveState(key, version, record, "blocked")
		diagnostic := inputDiagnostic("per_call_cost", capMicros(s.MaxCostUSD), inputTokens, inputCountBasis(r.Provider), s.MaxOutputTokens)
		diagnostic.Unit, diagnostic.Required, diagnostic.RequestedReservation, diagnostic.CountBasis = "usd_micros", upper, upper, "configured_reservation"
		return Result{}, &BudgetError{Diagnostic: diagnostic}
	}
	phase = time.Now()
	budgetKey, err := r.reserve(ctx, s, claimID, upper)
	measurement.PhaseMS["reservation"] = milliseconds(time.Since(phase))
	if err != nil {
		if s.DailyTokenBudget == nil || errors.Is(err, ErrBudgetExceeded) {
			saveState(key, version, record, "blocked")
		}
		return Result{}, err
	}
	record.BudgetKey, record.Reservation, record.ReservedMicros = budgetKey, claimID, upper
	tokenBudgetKey := ""
	{
		phase = time.Now()
		tokenBudgetKey, err = r.reserveTokens(ctx, s, claimID)
		record.TokenBudgetKey = tokenBudgetKey
		measurement.PhaseMS["reservation"] += milliseconds(time.Since(phase))
		if err != nil {
			// Generate was never called. A failed CAS acknowledgement can still
			// have committed: reconcile the exact IDs before releasing this job.
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			var rollbackError error
			if budgetKey != "" {
				rollbackError = r.settle(rollbackCtx, budgetKey, claimID, 0)
			}
			if tokenBudgetKey != "" {
				rollbackError = errors.Join(rollbackError, r.settleTokens(rollbackCtx, tokenBudgetKey, claimID, TokenBudget{}, true))
			}
			cancel()
			if rollbackError != nil {
				saveState(key, version, record, "pending")
				return Result{}, errors.Join(err, fmt.Errorf("reservation rollback unconfirmed; inspect this pending job before retrying: %w", rollbackError))
			}
			saveState(key, version, record, "blocked")
			return Result{}, err
		}
	}
	// Persist the reservation's identity before a paid call. If this write
	// fails, keep the reservation and pending claim, and do not generate.
	if budgetKey != "" || tokenBudgetKey != "" {
		phase = time.Now()
		a, err = r.publish(ctx, key, version, claimID+"-reserved", record)
		measurement.PhaseMS["reservation"] += milliseconds(time.Since(phase))
		if err != nil {
			return Result{}, err
		}
		version = a.Version
	}
	phase = time.Now()
	measurement.GenerationCalled = true
	measurement.GenerationStartedAt = phase.UTC()
	response, err := r.Provider.Generate(ctx, plan.Request)
	measurement.GenerationTiming = response.Timing
	measurement.PhaseMS["generation"] = milliseconds(time.Since(phase))
	measurement.GenerationEndedAt = time.Now().UTC()
	if err != nil {
		saveState(key, version, record, "uncertain")
		return Result{}, fmt.Errorf("generation outcome uncertain; reservation retained: %w", err)
	}
	if err = validateUsage(response.Usage); err != nil {
		saveState(key, version, record, "uncertain")
		return Result{}, err
	}
	measurement.KnownGenerationUsage = true
	measurement.GenerationUsage = &response.Usage
	// Provider counts may be estimates. Validate the actual billable usage
	// before marking any answer reusable, including when only a per-call cap
	// was configured. A cached answer must never erase a limit violation.
	var postflight error
	actual := int64(0)
	if s.Prices != nil {
		actual, err = costMicros(response.Usage, *s.Prices)
		if err != nil {
			saveState(key, version, record, "uncertain")
			return Result{}, err
		}
		if (s.MaxCostUSD > 0 && actual > capMicros(s.MaxCostUSD)) || (budgetKey != "" && actual > upper) {
			reason := "reservation_exceeded"
			if s.MaxCostUSD > 0 && actual > capMicros(s.MaxCostUSD) {
				reason = "exceeds_limit"
			}
			postflight = &BudgetError{Diagnostic: BudgetDiagnostic{LimitKind: "per_call_cost", ConfiguredLimit: capMicros(s.MaxCostUSD), Unit: "usd_micros", Reason: reason, CountBasis: "runtime_reported_usage", GenerationCalled: true, Required: actual, RequestedReservation: upper, ReportedInputTokens: response.Usage.InputTokens, ReportedOutputTokens: response.Usage.OutputTokens}}
		}
	}
	if response.Usage.InputTokens > s.MaxInputTokens {
		postflight = &BudgetError{Diagnostic: BudgetDiagnostic{LimitKind: "input_tokens", ConfiguredLimit: s.MaxInputTokens, Unit: "tokens", Reason: "exceeds_limit", CountBasis: "runtime_reported_usage", GenerationCalled: true, Required: response.Usage.InputTokens, ReportedInputTokens: response.Usage.InputTokens, ReportedOutputTokens: response.Usage.OutputTokens}}
	} else if response.Usage.OutputTokens > s.MaxOutputTokens {
		postflight = &BudgetError{Diagnostic: BudgetDiagnostic{LimitKind: "output_tokens", ConfiguredLimit: s.MaxOutputTokens, Unit: "tokens", Reason: "exceeds_limit", CountBasis: "runtime_reported_usage", GenerationCalled: true, Required: response.Usage.OutputTokens, ReportedInputTokens: response.Usage.InputTokens, ReportedOutputTokens: response.Usage.OutputTokens}}
	}
	if postflight != nil {
		response.Complete = false
	}
	record.Response = &response
	if postflight != nil {
		record.State = "limit_exceeded"
		record.BudgetDiagnostic = BudgetDiagnosticFromError(postflight)
	} else if response.Complete {
		record.State = "complete"
	} else {
		record.State = "incomplete"
	}
	record.CreatedAt = r.now()
	record.ExpiresAt = record.CreatedAt.Add(time.Duration(s.ReuseForSeconds) * time.Second)
	result = savedResult(record, s, false)
	var publicationError, settlementError, tokenSettlementError error
	phase = time.Now()
	_, err = r.publish(ctx, key, version, claimID+"-result", record)
	measurement.PhaseMS["publication"] = milliseconds(time.Since(phase))
	if err != nil {
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
		phase = time.Now()
		err = r.settle(settleCtx, budgetKey, claimID, actual)
		measurement.PhaseMS["settlement"] = milliseconds(time.Since(phase))
		if err != nil {
			settlementError = fmt.Errorf("budget settlement not confirmed; stop this scope and inspect its retained reservation: %w", err)
		}
	}
	if tokenBudgetKey != "" {
		// Charge all known attempts, including incomplete/rejected output. Cache
		// input is inclusive, and reasoning already belongs to output tokens.
		settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		phase = time.Now()
		err = r.settleTokens(settleCtx, tokenBudgetKey, claimID, tokenAmount(response.Usage.InputTokens, response.Usage.OutputTokens), false)
		measurement.PhaseMS["settlement"] += milliseconds(time.Since(phase))
		cancel()
		if err != nil {
			tokenSettlementError = fmt.Errorf("token settlement unconfirmed; stop this scope and inspect its retained reservation: %w", err)
		}
	}
	if err = errors.Join(postflight, publicationError, settlementError, tokenSettlementError); err != nil {
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
	result.GenerationTiming = rec.Response.Timing
	result.GenerationRuntimeCache = rec.Response.RuntimeCache
	result.GenerationUsage = rec.Response.Usage
	if reused {
		result.Response.Timing = nil       // Historical timing is not current inference.
		result.Response.RuntimeCache = nil // Historical KV hits are not a new runtime observation.
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
