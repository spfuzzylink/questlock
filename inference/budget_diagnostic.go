package inference

import "errors"

// BudgetDiagnostic contains only bounded numeric evidence and fixed vocabulary.
// It never includes provider messages, request text, model names, or endpoints.
// Cost limits use integer millionths of USD. CurrentUsage includes unsettled
// reservations in shared ledgers. Preflight counts retain their counting basis;
// they must not be presented as runtime-reported token usage. On a saved limit
// breach, GenerationCalled describes its original breach; Result.Measurement
// separately describes generation during the current invocation.
type BudgetDiagnostic struct {
	LimitKind            string `json:"limit_kind"`
	ConfiguredLimit      int64  `json:"configured_limit"`
	Unit                 string `json:"unit"`
	Reason               string `json:"reason"`
	CountBasis           string `json:"count_basis"`
	GenerationCalled     bool   `json:"generation_called"`
	EstimatedInputTokens int64  `json:"estimated_input_tokens,omitempty"`
	CountedInputTokens   int64  `json:"counted_input_tokens,omitempty"`
	ReportedInputTokens  int64  `json:"reported_input_tokens,omitempty"`
	ReportedOutputTokens int64  `json:"reported_output_tokens,omitempty"`
	MaxOutputTokens      int64  `json:"max_output_tokens,omitempty"`
	ContextWindow        int64  `json:"context_window,omitempty"`
	Required             int64  `json:"required,omitempty"`
	CurrentUsage         int64  `json:"current_usage,omitempty"`
	RequestedReservation int64  `json:"requested_reservation,omitempty"`
}

// BudgetError preserves errors.Is(err, ErrBudgetExceeded) and makes safe
// machine-readable evidence available through errors.As. Its message is fixed;
// callers should inspect Diagnostic rather than parse error text.
type BudgetError struct {
	Diagnostic BudgetDiagnostic
}

func (e *BudgetError) Error() string { return ErrBudgetExceeded.Error() }
func (e *BudgetError) Unwrap() error { return ErrBudgetExceeded }

// BudgetDiagnosticFromError returns a copy only when all fields belong to the
// safe diagnostic vocabulary. In particular, an arbitrary provider cannot
// smuggle its error text through a diagnostic string field.
func BudgetDiagnosticFromError(err error) *BudgetDiagnostic {
	var budget *BudgetError
	if !errors.As(err, &budget) || budget == nil || !validBudgetDiagnostic(budget.Diagnostic) {
		return nil
	}
	diagnostic := budget.Diagnostic
	return &diagnostic
}

func validBudgetDiagnostic(d BudgetDiagnostic) bool {
	switch d.LimitKind {
	case "input_tokens", "model_context_tokens", "output_tokens", "daily_input_tokens", "daily_output_tokens", "daily_total_tokens":
		if d.Unit != "tokens" {
			return false
		}
	case "per_call_cost", "daily_cost":
		if d.Unit != "usd_micros" {
			return false
		}
	default:
		return false
	}
	switch d.Reason {
	case "exceeds_limit", "frozen_after_overrun", "policy_required", "invalid_count", "reservation_exceeded":
	default:
		return false
	}
	if !validCountBasis(d.CountBasis) {
		return false
	}
	for _, n := range []int64{d.ConfiguredLimit, d.EstimatedInputTokens, d.CountedInputTokens, d.ReportedInputTokens, d.ReportedOutputTokens, d.MaxOutputTokens, d.ContextWindow, d.Required, d.CurrentUsage, d.RequestedReservation} {
		if n < 0 {
			return false
		}
	}
	return true
}

func validCountBasis(basis string) bool {
	switch basis {
	case "conservative_byte_estimate", "operator_upper_bound", "provider_token_count", "provider_preflight_count", "runtime_reported_usage", "configured_reservation":
		return true
	}
	return false
}

func inputCountBasis(provider Provider) string {
	if counter, ok := provider.(interface{ InputTokenCountBasis() string }); ok {
		switch basis := counter.InputTokenCountBasis(); basis {
		case "conservative_byte_estimate", "operator_upper_bound", "provider_token_count", "provider_preflight_count":
			return basis
		}
	}
	return "provider_preflight_count"
}

func inputDiagnostic(kind string, limit, input int64, basis string, output int64) BudgetDiagnostic {
	d := BudgetDiagnostic{LimitKind: kind, ConfiguredLimit: limit, Unit: "tokens", Reason: "exceeds_limit", CountBasis: basis, Required: input, MaxOutputTokens: output}
	if basis == "conservative_byte_estimate" || basis == "operator_upper_bound" {
		d.EstimatedInputTokens = input
	} else {
		d.CountedInputTokens = input
	}
	return d
}
