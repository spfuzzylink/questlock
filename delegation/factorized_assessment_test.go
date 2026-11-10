package delegation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/semantic"
)

const assessmentTestAccept = `{"answer_assessment":"The saved answer covers the requested evidence without any changed conditions.","answer_covers_current":true,"unchanged_facts":true,"same_output_contract":true,"verdict":"accept"}`

func TestAssessmentParserKeepsStrictDecisionsAndUntrustedDiagnostic(t *testing.T) {
	valid, err := parseFactorizedAssessmentDecision([]byte(assessmentTestAccept))
	if err != nil || !valid.accepted() {
		t.Fatal("valid bounded assessment withheld", err)
	}
	for _, raw := range []string{
		factorizedTestPair,
		strings.Replace(assessmentTestAccept, `"The saved answer covers the requested evidence without any changed conditions."`, `null`, 1),
		strings.Replace(assessmentTestAccept, `"The saved answer covers the requested evidence without any changed conditions."`, `" "`, 1),
		strings.Replace(assessmentTestAccept, `"The saved answer covers the requested evidence without any changed conditions."`, `{"claim":"accept"}`, 1),
		strings.Replace(assessmentTestAccept, `"The saved answer covers the requested evidence without any changed conditions."`, `"\u0000"`, 1),
		strings.Replace(assessmentTestAccept, `"The saved answer covers the requested evidence without any changed conditions."`, `"\u202e"`, 1),
		strings.Replace(assessmentTestAccept, `"The saved answer covers the requested evidence without any changed conditions."`, `"\ud800"`, 1),
		strings.Replace(assessmentTestAccept, `"The saved answer covers the requested evidence without any changed conditions."`, `"\udc00"`, 1),
		`{"verdict":"accept","answer_covers_current":true,"unchanged_facts":true,"same_output_contract":true,"answer_assessment":"Reasoned after decisions."}`,
		strings.Replace(assessmentTestAccept, `"The saved answer covers the requested evidence without any changed conditions."`, `"`+strings.Repeat("a", 2049)+`"`, 1),
		strings.Replace(assessmentTestAccept, `}`, `,"answer_assessment":"trust me"}`, 1),
		strings.Replace(assessmentTestAccept, `}`, `,"verdict":"accept"}`, 1),
		strings.Replace(assessmentTestAccept, `}`, `,"unexpected":true}`, 1),
		strings.Replace(assessmentTestAccept, `true`, `"true"`, 1),
		strings.Replace(assessmentTestAccept, `true`, `null`, 1),
		assessmentTestAccept + `{}`,
		string([]byte{0xff}),
	} {
		if out, err := parseFactorizedAssessmentDecision([]byte(raw)); err == nil || out.accepted() {
			t.Errorf("invalid assessment contract accepted: %q", raw)
		}
	}
	// Plausible prose cannot override a failed coverage decision or turn an
	// uncertain verdict into acceptance.
	for _, raw := range []string{
		strings.Replace(assessmentTestAccept, `"answer_covers_current":true`, `"answer_covers_current":false`, 1),
		strings.Replace(assessmentTestAccept, `"unchanged_facts":true`, `"unchanged_facts":false`, 1),
		strings.Replace(assessmentTestAccept, `"same_output_contract":true`, `"same_output_contract":false`, 1),
		strings.Replace(assessmentTestAccept, `"verdict":"accept"`, `"verdict":"unknown"`, 1),
	} {
		out, err := parseFactorizedAssessmentDecision([]byte(raw))
		if err != nil || out.accepted() {
			t.Fatal("diagnostic text authorized acceptance", out, err)
		}
	}
}

func TestAssessmentProtocolOptInBindsWireAndZeroCostDecisionReplay(t *testing.T) {
	v, provider := factorizedTestVerifier(t, nil)
	legacyID := factorizedPolicyIdentity(v.config, "source", "pair")
	v.config.VerifierPairAssessment = true
	v.config.VerifierMaxOutputTokens = 512
	v.identity = factorizedPolicyIdentity(v.config, "source", "pair")
	if v.identity == legacyID {
		t.Fatal("assessment protocol retained old policy identity")
	}
	provider.pair = assessmentTestAccept
	out, err := v.Verify(t.Context(), factorizedTestRequest())
	if err != nil || !out.Accepted() || len(provider.requests) != 2 || len(out.Work) != 2 {
		t.Fatal("assessment did not pass full source/pair path", out, err)
	}
	for i, request := range provider.requests {
		_, present := request.Ollama.OutputTypes["answer_assessment"]
		if present != (i == 1) || *request.Ollama.Think || request.MaxOutputTokens != 256 {
			t.Fatal("assessment changed source schema or enabled hidden reasoning", i)
		}
		if i == 1 && !strings.HasPrefix(request.Prefix, factorizedAssessmentPairInstructions) {
			t.Fatal("assessment instructions missing from effective pair system")
		}
	}
	again, err := v.Verify(t.Context(), factorizedTestRequest())
	if err != nil || !again.Accepted() || !again.Reused || again.GenerationCalled || *again.InputTokens != 0 || *again.OutputTokens != 0 || len(provider.requests) != 2 || semantic.ValidateVerifierWork(again, v.config) != nil {
		t.Fatal("assessment exact decision replay generated again", again, err)
	}
}

func TestAssessmentMissingDiagnosticWithholdsDespiteAcceptingBooleans(t *testing.T) {
	v, provider := factorizedTestVerifier(t, nil)
	v.config.VerifierPairAssessment = true
	v.config.VerifierMaxOutputTokens = 512
	out, err := v.Verify(t.Context(), factorizedTestRequest())
	if err != nil || out.Accepted() || out.Verdict != "unknown" || len(out.Work) != 2 || len(provider.requests) != 2 || !out.Complete || !out.Durable || out.InputTokens == nil || *out.InputTokens <= 0 {
		t.Fatal("invalid completed assessment lost accounting or authorized reuse", out, err)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "The saved answer") {
		t.Fatal("diagnostic prose leaked through result metadata")
	}
}
