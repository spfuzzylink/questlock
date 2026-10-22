package observability

import (
	"github.com/spfuzzylink/questlock/inference"
)

// FromInference maps one invocation to content-free execution metadata and
// validates it before export. It never sends anything or infers human acceptance.
// Historical generation timing/cache observations do not become fresh spans on
// reuse. Missing usage remains unknown; configured dollar costs are estimates.
func FromInference(spec inference.Spec, result inference.Result) (Execution, error) {
	m := result.Measurement
	if m == nil {
		return Execution{}, ErrInvalidExecution
	}
	started := m.StartedAt
	phases := make(map[string]float64, len(m.PhaseMS)+1)
	for key, value := range m.PhaseMS {
		phases[key] = value
	}
	if !m.ProviderSetupStartedAt.IsZero() {
		started = m.ProviderSetupStartedAt
		phases["provider_setup"] = m.ProviderSetupMS
	}
	execution := Execution{StartedAt: started, EndedAt: m.EndedAt, Fingerprint: m.Fingerprint, Provider: spec.Provider, Model: spec.Model,
		Outcome: Outcome(m.Outcome), Reused: result.Reused, Durable: result.Durable,
		ContextBytes: m.ContextBytes, TotalContextBytes: m.TotalContextBytes, SelectedContext: m.SelectedContext, OmittedContext: m.OmittedContext,
		ThisRunUsage: observedUsage(result.ThisRunUsage), OriginalUsage: observedUsage(result.GenerationUsage), AvoidedUsage: observedUsage(result.AvoidedUsage),
		CostUSD: copied(result.CostUSD), AvoidedCostUSD: copied(result.AvoidedCostUSD), PhaseMS: phases}
	if spec.Provider == "anthropic" {
		execution.Provider = "claude"
	}
	zero := float64(0)
	if result.Reused {
		execution.CostUSD = &zero
	}
	if m.GenerationCalled {
		outcome := execution.Outcome
		if m.KnownGenerationUsage && (outcome == NotDurable || outcome == Failed) {
			if result.Response.Complete {
				outcome = Complete
			} else {
				outcome = Incomplete
			}
		}
		g := &Generation{StartedAt: m.GenerationStartedAt, EndedAt: m.GenerationEndedAt, Model: spec.Model, Outcome: outcome, KnownUsage: m.KnownGenerationUsage, CostUSD: copied(result.CostUSD)}
		if cache := result.Response.RuntimeCache; cache != nil {
			g.RuntimeCache = &RuntimeCache{Profile: cache.Profile, CachedTokens: copied(cache.CachedTokens), DeviceTokens: copied(cache.DeviceTokens), HostTokens: copied(cache.HostTokens), StorageTokens: copied(cache.StorageTokens)}
		}
		if m.GenerationUsage != nil {
			g.Usage = observedUsage(*m.GenerationUsage)
			execution.ThisRunUsage = g.Usage
		}
		if result.Response.Model != "" {
			g.Model = result.Response.Model
		}
		if inference.SelfHosted(spec) && m.KnownGenerationUsage {
			g.CostUSD = &zero
			execution.CostUSD = &zero
		}
		if m.GenerationTiming != nil {
			g.FirstTokenMS = copied(m.GenerationTiming.FirstTokenMS)
			g.OutputMS = copied(m.GenerationTiming.OutputMS)
			g.TimingBasis = m.GenerationTiming.Basis
		}
		execution.Generation = g
	}
	if err := validateExecution(execution); err != nil {
		return execution, err
	}
	return execution, nil
}

func observedUsage(u inference.Usage) Usage {
	return Usage{InputTokens: u.InputTokens, UncachedInputTokens: u.UncachedInputTokens, CacheReadTokens: u.CacheReadTokens, CacheWriteTokens: u.CacheWriteTokens, OutputTokens: u.OutputTokens, ReasoningTokens: u.ReasoningTokens}
}

func copied[T any](p *T) *T {
	if p == nil {
		return nil
	}
	value := *p
	return &value
}
