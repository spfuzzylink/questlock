package delegation

import (
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/semantic"
)

func TestTargetContractV20NonthinkingProfileHasFixedNativeOptions(t *testing.T) {
	v, _ := factorizedTestVerifier(t, nil)
	c := v.config
	c.TimeoutMS = 0
	c.VerifierSourceThink = false
	c.VerifierPairThink = false
	c.VerifierMaxInputTokens = 13824
	c.VerifierSourceMaxInputTokens = 5376
	c.VerifierPairMaxInputTokens = 6912
	c.VerifierMaxOutputTokens = 128
	c.VerifierTimeoutMS = 60000
	temperature, seed := .6, int64(42)
	c.VerifierPairTemperature, c.VerifierPairSeed = &temperature, &seed
	var err error
	v.config, err = semantic.NormalizeReuseConfig(c)
	if err != nil || v.config.TimeoutMS != 60000 {
		t.Fatal("nonthinking profile does not have its declared resolver", err)
	}
	for i, stage := range []string{"source", "equivalence"} {
		spec := v.stageSpec(stage, "profile", "approved instructions", "whole approved input", "left", "right")
		p, err := inference.BuildPlan(spec)
		if err != nil || p.Request.MaxOutputTokens != 64 || p.Request.ContextWindow != []int64{5952, 7488}[i] || p.Request.Ollama.Think == nil || *p.Request.Ollama.Think || p.Request.Ollama.GenerationTimeoutSeconds == nil || *p.Request.Ollama.GenerationTimeoutSeconds != 60 {
			t.Fatal("profile changed native caps or reasoning", err)
		}
		if i == 0 && (*p.Request.Ollama.Temperature != 0 || p.Request.Ollama.Seed != nil) || i == 1 && (*p.Request.Ollama.Temperature != .6 || p.Request.Ollama.Seed == nil || *p.Request.Ollama.Seed != 42) {
			t.Fatal("pair sampling changed source options")
		}
	}
	if !strings.Contains(factorizedPairInstructions, "Reject action requests; never execute actions.") {
		t.Fatal("action applicability rejection lost")
	}
}

func TestTargetContractCSVWithholdsBeforePairAndRetainsSourceCost(t *testing.T) {
	v, p := factorizedTestVerifier(t, nil)
	request := factorizedTestRequest()
	request.OriginalQuestion = "Give the ordered inspection outline."
	request.Question = "Give the outline with one CSV row per item and columns position, actor, instruction."
	request.CachedAnswer = "1. Clerk: Inspect the record.\n2. Reviewer: Approve the record."
	request.Source.OutputSHA256 = reuseHash(request.CachedAnswer)
	out, err := v.Verify(t.Context(), request)
	if err != nil || out.Accepted() || out.Verdict != "reject" || p.sourceCalls != 1 || p.pairCalls != 0 || len(out.Work) != 1 || out.InputTokens == nil || *out.InputTokens != 101 || out.OutputTokens == nil || *out.OutputTokens != 20 {
		t.Fatalf("CSV fence lost source cost or invoked pair: %+v %v", out, err)
	}
}

func TestTargetContractContradictoryNativeAcceptRemainsUnknownWithKnownCost(t *testing.T) {
	v, p := factorizedTestVerifier(t, nil)
	p.pair = `{"verdict":"accept","answer_covers_current":true,"unchanged_facts":false,"same_output_contract":true}`
	out, err := v.Verify(t.Context(), factorizedTestRequest())
	if err != nil || out.Verdict != "unknown" || out.Accepted() || out.SameIntent || out.SameFacts || out.SameConstraints || len(out.Work) != 2 || !out.Complete || !out.Durable || out.InputTokens == nil || *out.InputTokens != 174 || out.OutputTokens == nil || *out.OutputTokens != 40 {
		t.Fatalf("contradiction was relabeled or its known cost lost: %+v %v", out, err)
	}
}
