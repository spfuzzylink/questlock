package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
)

type baselineProvider struct{ calls, counts int }

func (*baselineProvider) Identity() string { return "measurement-fixture" }
func (p *baselineProvider) CountInputTokens(context.Context, inference.Request) (int64, error) {
	p.counts++
	return 10, nil
}
func (p *baselineProvider) Generate(context.Context, inference.Request) (inference.Response, error) {
	p.calls++
	return inference.Response{Model: "fixture", Text: "answer", Complete: true, Usage: inference.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 2}}, nil
}

func TestBaselineUsesPreclaimAdmissionAndExactBypassesIt(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	token, err := s.CreateToken(t.Context(), "fixture", "tests")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(s))
	defer server.Close()
	broker, err := client.New(server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	provider := &baselineProvider{}
	runner := &inference.Runner{Broker: broker, Provider: provider}
	checks := 0
	admitted := &admittedMeasurementRunner{Runner: runner, admit: func(context.Context, inference.Spec) (func(), error) {
		checks++
		return nil, inference.ErrAdmissionDeferred
	}}
	spec := inference.Spec{Task: "baseline", Revision: "same", Provider: "openai", Model: "fixture", Instructions: "answer", Input: "question", MaxInputTokens: 100, MaxOutputTokens: 20, ReuseForSeconds: 3600}
	deferred, err := admitted.Run(t.Context(), spec)
	if !inference.PreClaimDeferred(deferred, err) || provider.calls != 0 || provider.counts != 0 || checks != 1 {
		t.Fatalf("baseline bypassed admission: %+v %v", deferred, err)
	}
	// A deferred call owns no claim: a capable worker can complete the same task.
	done, err := runner.Run(t.Context(), spec)
	if err != nil || !done.Durable {
		t.Fatal(err)
	}
	exact, err := admitted.Run(t.Context(), spec)
	if err != nil || !exact.Reused || checks != 1 || provider.calls != 1 {
		t.Fatalf("exact hit waited: %+v %v", exact, err)
	}
	// The embedded resolver method remains available for ControlledReuser's gate.
	var _ interface {
		RunWithResolverAdmission(context.Context, inference.Spec, inference.ReuseResolver, inference.AdmissionFunc) (inference.Result, error)
	} = admitted
}

func TestBaselineDeferredReportHasZeroCurrentWork(t *testing.T) {
	for _, phase := range []string{"baseline", "baseline_repeat"} {
		h, r := deferredFixture(t, "baseline")
		h.FreshBaseline = true
		h.FreshRepeatBaseline = true
		r.Phase = phase
		r.Result.SemanticReuse = nil
		r.Result.OutcomeTracking = nil
		report, err := summarizeMeasurement(strings.NewReader(encodeDeferred(t, h, r)))
		if err != nil {
			t.Fatal(err)
		}
		p := report.Phases[phase]
		if p.ResourceDeferred != 1 || p.Failed != 0 || p.Generation.Calls != 0 || p.Generation.Unknown != 0 || p.EmbeddingWorkRecords != 0 || p.Verification.Calls != 0 {
			t.Fatalf("invented baseline work: %+v", p)
		}
	}
}

func TestSourceCapHeaderStrictAndDefaultCompatible(t *testing.T) {
	log := fixtureMeasurement(t, fixtureRows(t), true)
	for _, key := range []string{"verifier_max_input_tokens", "verifier_source_max_input_tokens"} {
		for _, value := range []any{nil, 0, -1} {
			changed := replaceMeasurementHeader(t, log, func(h map[string]any) { h[key] = value })
			if _, err := summarizeMeasurement(strings.NewReader(changed)); err == nil {
				t.Fatalf("accepted %s=%v", key, value)
			}
		}
	}
	var h measurementHeader
	if err := json.Unmarshal([]byte(strings.SplitN(log, "\n", 2)[0]), &h); err != nil || h.VerifierMaxInputTokens != 0 || h.VerifierSourceMaxInputTokens != 0 {
		t.Fatal("old header changed", err)
	}
	changed := replaceMeasurementHeader(t, log, func(h map[string]any) {
		h["verifier_max_input_tokens"] = 13312
		h["verifier_source_max_input_tokens"] = 6657
	})
	if _, err := summarizeMeasurement(strings.NewReader(changed)); err == nil {
		t.Fatal("accepted source greater than half")
	}
}
