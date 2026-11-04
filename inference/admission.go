package inference

import (
	"context"
	"errors"
)

// ErrAdmissionDeferred means runtime capacity was not admitted before any new
// claim, token reservation or model call. It is not an answer-quality verdict
// and does not authorize taking over a pending or uncertain job.
var ErrAdmissionDeferred = errors.New("inference_resource_admission_deferred")

// AdmissionFunc applies a caller-owned runtime policy after exact lookup and
// existing ownership fences, immediately before a new claim. It may wait under
// ctx and return ErrAdmissionDeferred. A returned release function runs exactly
// once when this invocation exits, including on admission failure. Implementors
// must bound their queue and honor cancellation. Admission is an estimate, not
// an OS resource reservation, and never becomes part of task identity.
type AdmissionFunc func(context.Context, Spec) (release func(), err error)

// RunWithAdmission preserves exact reuse while admitting fresh work before it
// claims shared state. The callback never cancels another worker or clears a
// pending/uncertain result. A nil callback retains Run's behavior.
func (r Runner) RunWithAdmission(ctx context.Context, s Spec, admit AdmissionFunc) (Result, error) {
	return r.run(ctx, s, nil, admit)
}

// RunWithResolverAdmission adds runtime admission to RunWithResolver. Exact
// original and policy-authorized alias results and ownership fences precede the
// callback; the callback's lease covers resolution and any target generation.
func (r Runner) RunWithResolverAdmission(ctx context.Context, s Spec, resolver ReuseResolver, admit AdmissionFunc) (Result, error) {
	return r.run(ctx, s, resolver, admit)
}

// PreClaimDeferred checks the Runner's explicit no-claim/no-generation evidence.
// A provider returning the same sentinel after Generate is not a deferral. It
// remains an execution error/unknown outcome with its ordinary durable fence.
func PreClaimDeferred(result Result, err error) bool {
	m := result.Measurement
	if !errors.Is(err, ErrAdmissionDeferred) || m == nil || m.Outcome != "deferred" || m.GenerationCalled || m.KnownGenerationUsage || m.GenerationUsage != nil || result.Fingerprint != "" || result.Durable || result.Reused || result.Response.Complete || result.Response.Text != "" || result.ThisRunUsage != (Usage{}) || result.GenerationUsage != (Usage{}) || result.AvoidedUsage != (Usage{}) {
		return false
	}
	for _, phase := range []string{"admission", "reuse_resolution", "preflight", "reservation", "generation", "publication", "settlement", "recovery"} {
		if _, found := m.PhaseMS[phase]; found {
			return false
		}
	}
	_, waited := m.PhaseMS["resource_admission"]
	return waited
}
