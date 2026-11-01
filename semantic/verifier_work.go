package semantic

import (
	"errors"
	"strings"
)

// ValidateVerifierWork checks attribution for an explicitly factorized policy.
// It does not infer model correctness from counts or a receipt's syntax.
func ValidateVerifierWork(v ValidityResult, c ReuseConfig) error {
	invalid := errors.New("unconfirmed factorized verification work")
	if c.VerifierPolicy == "" {
		if len(v.Work) != 0 || v.MixedModelUsage {
			return invalid
		}
		return nil
	}
	if c.VerifierPolicy != "factorized-v1" || len(v.Work) < 1 || len(v.Work) > 2 || v.SharedWork || v.Accepted() && len(v.Work) != 2 {
		return invalid
	}
	const prefix = "semantic/v1/decisions/"
	if !strings.HasPrefix(v.ReceiptKey, prefix) || !validHash(strings.TrimPrefix(v.ReceiptKey, prefix)) || v.OutputSHA256 != strings.TrimPrefix(v.ReceiptKey, prefix) {
		return invalid
	}
	attributed := c.VerifierSourceMaxInputTokens != 0 || c.VerifierPairModel != "" || c.VerifierPairMaxInputTokens != 0
	pairModel := c.VerifierPairModel
	if pairModel == "" {
		pairModel = c.VerifierModel
	}
	if c.VerifierSourceMaxInputTokens < 0 || c.VerifierSourceMaxInputTokens > c.VerifierMaxInputTokens/2 || c.VerifierPairMaxInputTokens < 0 || c.VerifierPairMaxInputTokens > c.VerifierMaxInputTokens/2 {
		return invalid
	}
	if v.MixedModelUsage != (len(v.Work) == 2 && pairModel != c.VerifierModel) {
		return invalid
	}
	input, output := int64(0), int64(0)
	generated, reused := false, true
	for i, w := range v.Work {
		expected := "source"
		if i == 1 {
			expected = "equivalence"
		}
		const job = "inference/v1/jobs/"
		if w.AdmissionDeferred || w.Stage != expected || !w.Complete || !w.Durable || !strings.HasPrefix(w.ReceiptKey, job) || !validHash(strings.TrimPrefix(w.ReceiptKey, job)) || !validHash(w.OutputSHA256) || w.InputTokens == nil || w.OutputTokens == nil {
			return invalid
		}
		inputCap, model := c.VerifierMaxInputTokens/2, c.VerifierModel
		if c.VerifierSourceMaxInputTokens != 0 {
			inputCap = c.VerifierSourceMaxInputTokens
		}
		if i == 1 {
			inputCap, model = c.VerifierMaxInputTokens/2, pairModel
			if c.VerifierPairMaxInputTokens != 0 {
				inputCap = c.VerifierPairMaxInputTokens
			}
		}
		if attributed {
			if w.Model != model || !validHash(w.RuntimeIdentity) {
				return invalid
			}
		} else if w.Model != "" || w.RuntimeIdentity != "" {
			return invalid
		}
		if *w.InputTokens < 0 || *w.OutputTokens < 0 || *w.InputTokens > inputCap || *w.OutputTokens > c.VerifierMaxOutputTokens/2 {
			return invalid
		}
		if w.Reused {
			if w.GenerationCalled || *w.InputTokens != 0 || *w.OutputTokens != 0 {
				return invalid
			}
		} else if !w.GenerationCalled || *w.InputTokens < 1 || *w.OutputTokens < 1 {
			return invalid
		}
		if i == 1 && w.ReceiptKey == v.Work[0].ReceiptKey {
			return invalid
		}
		input += *w.InputTokens
		output += *w.OutputTokens
		generated = generated || w.GenerationCalled
		reused = reused && w.Reused
	}
	if !v.Complete || !v.Durable || v.InputTokens == nil || v.OutputTokens == nil || *v.InputTokens != input || *v.OutputTokens != output || v.GenerationCalled != generated || v.Reused != reused {
		return invalid
	}
	return nil
}
