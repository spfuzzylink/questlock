package delegation

import (
	"encoding/hex"
	"strings"

	"github.com/spfuzzylink/questlock/semantic"
)

// Account real stage jobs, never the composite decision as another model call.
func outcomeVerifications(values []semantic.ValidityResult) []semantic.ValidityResult {
	var out []semantic.ValidityResult
	for _, v := range values {
		if len(v.Work) == 0 {
			out = append(out, v)
			continue
		}
		if len(v.Work) > 2 || v.SharedWork {
			out = append(out, semantic.ValidityResult{SharedWork: true})
			continue
		}
		for _, w := range v.Work {
			out = append(out, semantic.ValidityResult{Verdict: v.Verdict, SameIntent: v.SameIntent, SameFacts: v.SameFacts, SameConstraints: v.SameConstraints, AnswerSupported: v.AnswerSupported, GenerationCalled: w.GenerationCalled, Complete: w.Complete, Durable: w.Durable, InputTokens: w.InputTokens, OutputTokens: w.OutputTokens, Reused: w.Reused, ReceiptKey: w.ReceiptKey, OutputSHA256: w.OutputSHA256})
		}
	}
	return out
}

// Refuse malformed attribution instead of silently dropping or double-charging work.
func validOutcomeVerificationWork(values []semantic.ValidityResult) bool {
	digest := func(s string) bool {
		b, e := hex.DecodeString(s)
		return e == nil && len(b) == 32 && strings.ToLower(s) == s
	}
	generated := map[string]bool{}
	for _, v := range values {
		if len(v.Work) == 0 {
			continue
		}
		if len(v.Work) > 2 || v.SharedWork {
			return false
		}
		seen := map[string]bool{}
		for i, w := range v.Work {
			stage := "source"
			if i == 1 {
				stage = "equivalence"
			}
			if w.Stage != stage {
				return false
			}
			if w.GenerationCalled && w.Reused {
				return false
			}
			if w.ReceiptKey == "" {
				if w.GenerationCalled || w.Reused || w.Complete {
					return false
				}
				continue
			}
			const prefix = "inference/v1/jobs/"
			if !strings.HasPrefix(w.ReceiptKey, prefix) || !digest(strings.TrimPrefix(w.ReceiptKey, prefix)) || seen[w.ReceiptKey] {
				return false
			}
			seen[w.ReceiptKey] = true
			if w.GenerationCalled {
				if generated[w.ReceiptKey] {
					return false
				}
				generated[w.ReceiptKey] = true
			}
			if w.Reused && (w.InputTokens == nil || w.OutputTokens == nil || *w.InputTokens != 0 || *w.OutputTokens != 0) {
				return false
			}
			if w.Complete && w.Durable && !digest(w.OutputSHA256) {
				return false
			}
		}
	}
	return true
}
