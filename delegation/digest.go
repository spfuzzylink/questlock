package delegation

import (
	"errors"

	"github.com/spfuzzylink/questlock/inference"
)

// RequestDigest returns the canonical identity used by Router results. Callers
// must Validate the request before using the digest as an approval binding.
// Outcome tracking is excluded, matching the existing generation identity.
func RequestDigest(request Request) string { return requestDigest(request) }

// SpecForTier validates the entire approved request and returns the exact
// derived spec for a declared tier. It uses the Router's unchanged task and
// revision identities; it performs no inference or provider discovery.
func SpecForTier(request Request, tierID string, allowPaid bool) (inference.Spec, error) {
	if err := Validate(request, allowPaid); err != nil {
		return inference.Spec{}, err
	}
	for _, tier := range tiers(request.Policy) {
		if tier.ID == tierID {
			return tierSpec(request, tier, requestDigest(request)), nil
		}
	}
	return inference.Spec{}, errors.New("requested tier is not declared in the approved policy")
}
