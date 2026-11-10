package semantic

import (
	"slices"
	"strings"
	"testing"
)

func TestAdoptedUpdateRiskRejectsNewEvidenceEpoch(t *testing.T) {
	for _, question := range []string{
		"The pending condition is now satisfied. Assess the result under that updated snapshot.",
		"Use the revised policy to assess the request.",
		"Using the updated facts, determine the status.",
		"Under the new rules, who may complete the request?",
		"What is the status under the replacement snapshot now available?",
		"Updated facts: the pending condition has passed. Assess the result.",
		"Policy update: the second signature is optional. Who must sign?",
		"Revised policy: completion is allowed only if the receipt exists. Assess the request.",
		"Assume the remaining checks have now passed. Assess the result.",
		"Assume the policy now requires the opposite decision. Give the decision.",
		"Use the updated policy, not the old policy.",
		"İstanbul: evaluate the request under the updated snapshot.",
	} {
		t.Run(question, func(t *testing.T) {
			codes := CheckReuseTargetRisks("Assess the request from the saved snapshot.", "", question, "The request is not ready.")
			if !slices.Contains(codes, "target_risk_adopted_update") {
				t.Fatal("new adopted update was not withheld", codes)
			}
		})
	}
}

func TestAdoptedUpdateRiskLeavesDiscussionAndConditionalsToVerifier(t *testing.T) {
	for _, question := range []string{
		`The note reads "use the updated policy". Assess the saved request.`,
		"The note reads ‘use the updated policy’. Assess the saved request.",
		"```text\nUse the updated policy.\n```\nAssess the saved request.",
		"> Use the updated policy.\nAssess the saved request.",
		"Can we use the updated policy?",
		"Should I apply the new rules?",
		"Under the updated snapshot?",
		"Explain whether to use the updated policy.",
		"What does using the updated policy mean?",
		"Explain examples of requests that use the updated policy.",
		"An example v1.2 says use the updated policy. Assess the saved request.",
		"The operator reported using the updated snapshot. Assess the saved request.",
		"Use the updated policy in the example. Assess the saved request.",
		"Do not use the updated policy.",
		"Don't use the updated policy.",
		"Don’t use the updated policy.",
		"Never assess under the updated snapshot.",
		"If it becomes available, use the updated policy.",
		"Unless the record is unchanged, use the updated snapshot.",
		"Use the updated policy only if it becomes available.",
		"Use the updated policy when it is published.",
		"Suppose we use the updated snapshot. Would that change the result?",
		"What would change if the new policy were adopted?",
		"The label is Updated facts: approved. Assess the saved request.",
		"If changed, policy update: the pending condition passes.",
		"Does updated policy: no signature mean anything?",
		"Assume the checks now pass? Assess the saved request.",
		"Assume the checks now pass if the operator confirms them.",
		"Who must supply the outstanding evidence? Keep the standard fields.",
		"Can pickup happen before approval? Keep the full object.",
		"Are existing connections broken too? Keep the full answer.",
	} {
		t.Run(question, func(t *testing.T) {
			if targetAdoptedUpdate("Assess the saved request.", "", question) {
				t.Fatal("discussion, conditional or ordinary question treated as adopted update")
			}
		})
	}
}

func TestAdoptedUpdateRiskKeepsSavedEvidenceEpoch(t *testing.T) {
	for _, tc := range []struct{ original, instructions, current string }{
		{"Use the updated policy. Assess status.", "", "Assess readiness under the revised policy."},
		{"Under the updated policy, what is the status?", "", "Assess status using the revised policy."},
		{"What is the status under the revised snapshot now available?", "", "Assess the result under the updated snapshot."},
		{"Assess status.", "Use the updated policy.", "Assess status under the updated policy."},
		{"Updated facts: all checks passed. Assess status.", "", "Updated facts: all checks passed. Explain the result."},
		{"Assume the checks now pass. Assess status.", "", "Assume the checks now pass. Explain the result."},
	} {
		if targetAdoptedUpdate(tc.original, tc.instructions, tc.current) {
			t.Fatal("saved update was treated as a new evidence epoch", tc)
		}
	}
	if !targetAdoptedUpdate("Updated facts: checks pending. Assess status.", "", "Updated facts: all checks passed. Assess status.") {
		t.Fatal("changed update declaration was treated as the same facts")
	}
}

func TestAdoptedUpdateRiskPreservesExactQuestionAndInputGuards(t *testing.T) {
	question := "Use the updated policy. Assess the request."
	if codes := CheckReuseTargetRisks(question, "", question, "The request is not ready."); len(codes) != 0 {
		t.Fatal("exact-question behavior changed", codes)
	}
	for _, question := range []string{"", "\xff", strings.Repeat("x", MaxInputBytes+1), "Use the updated\u200bpolicy."} {
		codes := CheckReuseTargetRisks("Assess status.", "", question, "Not ready.")
		if !slices.Contains(codes, "target_risk_invalid_text") {
			t.Fatal("invalid guard input accepted", codes)
		}
	}
}
