package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/semantic"
)

// These providers exercise protocol decisions, not a model's semantic quality.
type independentFactorizedProvider struct {
	identity, support, pair string
	requests                []inference.Request
	failSource              bool
}

func (p *independentFactorizedProvider) Identity() string { return p.identity }
func (*independentFactorizedProvider) CountInputTokens(context.Context, inference.Request) (int64, error) {
	return 91, nil
}
func (p *independentFactorizedProvider) Generate(_ context.Context, request inference.Request) (inference.Response, error) {
	p.requests = append(p.requests, request)
	text := p.pair
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimPrefix(request.Input, "\n\nTask:\n")), &fields); err != nil {
		return inference.Response{}, err
	}
	if fields["cached_answer"] != nil {
		if p.failSource {
			return inference.Response{}, errors.New("offline unknown source outcome")
		}
		text = p.support
	}
	return inference.Response{Text: text, Complete: true, Model: "offline", Usage: inference.Usage{InputTokens: 91, UncachedInputTokens: 91, OutputTokens: 17}}, nil
}

func TestIndependentFactorizedBadSourceCertificateNeverReachesPair(t *testing.T) {
	f := independentFactorizedSetup(t)
	f.seed(t)
	f.stages.support = `{"verdict":"reject","supported":false,"complete":false}`
	f.request.Spec.Input = "Before the preview can proceed, what is still required?"
	out, err := f.router.Run(t.Context(), f.request)
	if err != nil || out.SemanticReuse == nil || out.SemanticReuse.GenerationAvoided || len(out.SemanticReuse.Verifications) != 1 || out.SemanticReuse.Verifications[0].AnswerSupported || out.SemanticReuse.Verifications[0].Verdict != "reject" || len(f.stages.requests) != 1 || f.target.generations != 2 {
		t.Fatalf("bad source certificate reached reuse or pair inference: observation=%+v error=%v", out.SemanticReuse, err)
	}
	record := OutcomeRecord(f.request, out, "inspect-rejection")
	if len(record.VerificationWork) != 1 || !record.VerificationWork[0].GenerationCalled || !record.VerificationWork[0].UsageKnown || record.VerificationWork[0].Usage.InputTokens != 91 || record.VerificationWork[0].CheckPassed {
		t.Fatalf("rejected certificate work disappeared: %+v", record.VerificationWork)
	}
}

func TestIndependentFactorizedUnknownSourceFencesOtherQuestionsAndRetainsCost(t *testing.T) {
	f := independentFactorizedSetup(t)
	f.seed(t)
	f.stages.failSource = true
	f.request.Spec.Input = "Before the preview can proceed, what is still required?"
	out, err := f.router.Run(t.Context(), f.request)
	if !errors.Is(err, ErrStopped) || out.SemanticReuse == nil || out.SemanticReuse.GenerationAvoided || len(f.stages.requests) != 1 || f.target.generations != 1 {
		t.Fatalf("unknown source verification authorized target work: observation=%+v error=%v", out.SemanticReuse, err)
	}
	RecordOutcome(t.Context(), f.broker, f.request, &out, "independent-unknown-source")
	record := OutcomeRecord(f.request, out, "inspect-unknown")
	if out.OutcomeTracking == nil || out.OutcomeTracking.Status != "recorded" || len(record.VerificationWork) != 1 || !record.VerificationWork[0].GenerationCalled || record.VerificationWork[0].UsageKnown || record.VerificationWork[0].Fingerprint == "" {
		t.Fatalf("unknown started work vanished or became zero: tracking=%+v work=%+v", out.OutcomeTracking, record.VerificationWork)
	}
	f.stages.failSource = false
	f.request.Spec.Input = "What must still happen before the preview proceeds?"
	again, err := f.router.Run(t.Context(), f.request)
	if !errors.Is(err, ErrStopped) || again.Response != nil || len(f.stages.requests) != 1 || f.target.generations != 1 {
		t.Fatalf("new question bypassed the uncertain shared source job: observation=%+v error=%v", again.SemanticReuse, err)
	}
}

type independentFactorizedFixture struct {
	broker   storedBroker
	target   *fixtureProvider
	stages   *independentFactorizedProvider
	verifier *FactorizedVerifier
	router   Router
	request  Request
	now      time.Time
}

func independentFactorizedSetup(t *testing.T) *independentFactorizedFixture {
	t.Helper()
	f := &independentFactorizedFixture{broker: testBroker(t), now: time.Now().UTC()}
	f.target = &fixtureProvider{identity: "independent-target", response: knownResult(`{"ship":false}`).Response}
	f.stages = &independentFactorizedProvider{identity: "independent-verifier-runtime", support: `{"verdict":"accept","supported":true,"complete":true}`, pair: `{"verdict":"accept","original_covers_current":true,"current_covers_original":true,"same_output_contract":true}`}
	c := controlledTestConfig()
	c.VerifierPolicy = "factorized-v1"
	f.verifier = &FactorizedVerifier{runner: &inference.Runner{Broker: f.broker, Provider: f.stages, Now: func() time.Time { return f.now }}, config: c, identity: reuseHash("independent-factorized-policy")}
	f.router = Router{Factory: func(Tier) (Runner, error) {
		return &inference.Runner{Broker: f.broker, Provider: f.target, Now: func() time.Time { return f.now }}, nil
	}, Reuse: &ControlledReuser{Broker: f.broker, Index: controlledStoredIndex{f.broker}, EmbedderFactory: func(context.Context, semantic.Config) (semantic.Embedder, error) {
		return &shadowFailureEmbedder{}, nil
	}, VerifierFactory: func(context.Context, semantic.ReuseConfig) (semantic.Verifier, error) { return f.verifier, nil }}}
	f.request = testRequest()
	f.request.Policy.Escalation = nil
	f.request.SemanticReuse = &c
	f.request.Spec.Input = "What remains before the preview can proceed?"
	f.request.Spec.Context = []inference.ContextItem{{ID: "evidence", Text: "The preview is approved. The public release is not approved. The operator must notify the reviewer. Container checks remain; unit checks completed.", Required: true}}
	return f
}

func (f *independentFactorizedFixture) seed(t *testing.T) Result {
	t.Helper()
	r, err := f.router.Run(t.Context(), f.request)
	if err != nil || r.SemanticReuse == nil || !r.SemanticReuse.Indexed || f.target.generations != 1 || len(f.stages.requests) != 0 {
		t.Fatalf("source setup: %+v %v", r, err)
	}
	return r
}

func TestIndependentFactorizedServingCarriesCompleteEvidenceAndExpiresWithSource(t *testing.T) {
	f := independentFactorizedSetup(t)
	f.seed(t)
	f.request.Spec.Input = "Before the preview can proceed, what is still required?"
	first, err := f.router.Run(t.Context(), f.request)
	if err != nil || first.SemanticReuse == nil || !first.SemanticReuse.GenerationAvoided || f.target.generations != 1 || len(f.stages.requests) != 2 {
		t.Fatalf("factorized serving did not avoid target work: observation=%+v stages=%d target=%d error=%v", first.SemanticReuse, len(f.stages.requests), f.target.generations, err)
	}
	for i, request := range f.stages.requests {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(strings.TrimPrefix(request.Input, "\n\nTask:\n")), &fields); err != nil {
			t.Fatal(err)
		}
		var evidence, instructions string
		if json.Unmarshal(fields["context"], &evidence) != nil || json.Unmarshal(fields["instructions"], &instructions) != nil || !strings.Contains(evidence, f.request.Spec.Context[0].Text) || instructions != f.request.Spec.Instructions || fields["original_question"] == nil || fields["bindings"] == nil {
			t.Fatalf("stage %d lost complete approved context: %s", i, request.Input)
		}
		if i == 0 && (fields["cached_answer"] == nil || fields["question"] != nil) || i == 1 && (fields["cached_answer"] != nil || fields["question"] == nil) {
			t.Fatalf("stage %d did not preserve certificate/pair separation: %s", i, request.Input)
		}
	}
	RecordOutcome(t.Context(), f.broker, f.request, &first, "independent-factorized-first")
	if first.OutcomeTracking == nil || first.OutcomeTracking.Status != "recorded" {
		t.Fatalf("two real jobs could not be recorded: %+v", first.OutcomeTracking)
	}
	record := OutcomeRecord(f.request, first, "inspect")
	if len(record.VerificationWork) != 2 || record.VerificationWork[0].Fingerprint == record.VerificationWork[1].Fingerprint {
		t.Fatalf("composite proof replaced or duplicated stage work: %+v", record.VerificationWork)
	}
	f.now = f.now.Add(2 * time.Hour)
	f.request.Spec.Input = "What must still happen before the preview proceeds?"
	expired, err := f.router.Run(t.Context(), f.request)
	if err != nil || expired.Response == nil || expired.SemanticReuse == nil || expired.SemanticReuse.GenerationAvoided || f.target.generations != 2 || len(f.stages.requests) != 2 {
		t.Fatalf("cached certificate extended expired source: %+v %v", expired, err)
	}
}

func TestIndependentFactorizedGuardOmissionsStillRequireBidirectionalDecision(t *testing.T) {
	for name, questions := range map[string][2]string{
		"role reversal":    {"Which reviewer must the operator notify?", "Which operator must the reviewer notify?"},
		"time scope":       {"Which checks remain for the preview?", "Which checks completed for the preview?"},
		"output scope":     {"Describe all checks for the preview.", "Describe remaining checks for the preview."},
		"action authority": {"Explain the preview approval.", "Perform the preview approval."},
	} {
		t.Run(name, func(t *testing.T) {
			if reasons := semantic.CheckReuseProtectedTerms(questions[0], questions[1], "factorized-v1"); len(reasons) != 0 {
				t.Fatalf("fixture should reach the mandatory verifier: %v", reasons)
			}
			f := independentFactorizedSetup(t)
			f.stages.pair = `{"verdict":"reject","original_covers_current":false,"current_covers_original":false,"same_output_contract":false}`
			f.request.Spec.Input = questions[0]
			f.seed(t)
			f.request.Spec.Input = questions[1]
			out, err := f.router.Run(t.Context(), f.request)
			if err != nil || out.SemanticReuse == nil || out.SemanticReuse.GenerationAvoided || len(out.SemanticReuse.Verifications) != 1 || out.SemanticReuse.Verifications[0].Verdict != "reject" || len(f.stages.requests) != 2 || f.target.generations != 2 {
				t.Fatalf("guard passage bypassed the rejecting pair decision: %+v %v", out, err)
			}
		})
	}
}

type independentMutatingVerifier struct {
	inner  semantic.Verifier
	mutate func(*semantic.ValidityResult)
}

func (v independentMutatingVerifier) Identity() string { return v.inner.Identity() }
func (v independentMutatingVerifier) Verify(ctx context.Context, request semantic.ValidityRequest) (semantic.ValidityResult, error) {
	out, err := v.inner.Verify(ctx, request)
	if err == nil {
		v.mutate(&out)
	}
	return out, err
}

func TestIndependentFactorizedMalformedStageProofStopsAndFencesTarget(t *testing.T) {
	for name, mutate := range map[string]func(*semantic.ValidityResult){
		"missing stage":        func(v *semantic.ValidityResult) { v.Work = v.Work[:1] },
		"empty job":            func(v *semantic.ValidityResult) { v.Work[0].ReceiptKey = "" },
		"double job":           func(v *semantic.ValidityResult) { v.Work[1].ReceiptKey = v.Work[0].ReceiptKey },
		"wrong order":          func(v *semantic.ValidityResult) { v.Work[0], v.Work[1] = v.Work[1], v.Work[0] },
		"unknown job usage":    func(v *semantic.ValidityResult) { v.Work[0].InputTokens = nil },
		"missing output hash":  func(v *semantic.ValidityResult) { v.Work[0].OutputSHA256 = "" },
		"aggregate usage":      func(v *semantic.ValidityResult) { n := *v.InputTokens + 1; v.InputTokens = &n },
		"aggregate generation": func(v *semantic.ValidityResult) { v.GenerationCalled = false },
		"charged reuse":        func(v *semantic.ValidityResult) { v.Work[0].Reused = true; v.Work[0].GenerationCalled = false },
	} {
		t.Run(name, func(t *testing.T) {
			f := independentFactorizedSetup(t)
			f.router.Reuse.VerifierFactory = func(context.Context, semantic.ReuseConfig) (semantic.Verifier, error) {
				return independentMutatingVerifier{inner: f.verifier, mutate: mutate}, nil
			}
			f.seed(t)
			f.request.Spec.Input = "Before the preview can proceed, what is still required?"
			out, err := f.router.Run(t.Context(), f.request)
			if !errors.Is(err, ErrStopped) || out.Response != nil || out.SemanticReuse == nil || out.SemanticReuse.Status != "verification_unconfirmed" || out.SemanticReuse.GenerationAvoided || f.target.generations != 1 {
				t.Fatalf("malformed proof authorized reuse or fresh target work: observation=%+v target=%d error=%v", out.SemanticReuse, f.target.generations, err)
			}
			if _, err := f.router.Run(t.Context(), f.request); !errors.Is(err, ErrStopped) || len(f.stages.requests) != 2 || f.target.generations != 1 {
				t.Fatalf("malformed proof uncertainty was bypassed: %v", err)
			}
		})
	}
}

func TestIndependentFactorizedOutcomeRefusesUnconfirmedStartedJob(t *testing.T) {
	for _, fault := range []string{"empty job", "double job", "free complete job"} {
		t.Run(fault, func(t *testing.T) {
			f := independentFactorizedSetup(t)
			f.seed(t)
			f.request.Spec.Input = "Before the preview can proceed, what is still required?"
			out, err := f.router.Run(t.Context(), f.request)
			if err != nil {
				t.Fatal(err)
			}
			if fault == "empty job" {
				out.SemanticReuse.Verifications[0].Work[0].ReceiptKey = ""
			} else if fault == "double job" {
				out.SemanticReuse.Verifications[0].Work[1].ReceiptKey = out.SemanticReuse.Verifications[0].Work[0].ReceiptKey
			} else {
				out.SemanticReuse.Verifications[0].Work[0].GenerationCalled = false
			}
			RecordOutcome(t.Context(), f.broker, f.request, &out, "independent-invalid-work")
			if out.OutcomeTracking == nil || out.OutcomeTracking.Status != "failed" {
				t.Fatalf("invalid actual generation was silently dropped or double charged: %+v", out.OutcomeTracking)
			}
		})
	}
}

func TestIndependentFactorizedProofJobsDoNotCrossRuntimeOrBrokerScope(t *testing.T) {
	for _, boundary := range []string{"runtime", "scope"} {
		t.Run(boundary, func(t *testing.T) {
			f := independentFactorizedSetup(t)
			answer := `{"ship":false}`
			request := semantic.ValidityRequest{Source: &semantic.ValiditySource{Key: "inference/v1/jobs/" + reuseHash("independent-source-job"), Version: 1, SHA256: reuseHash("independent-source-artifact"), OutputSHA256: reuseHash(answer), RequestFingerprint: reuseHash("independent-source-request")}, OriginalQuestion: "What remains before the preview can proceed?", Question: "Before the preview can proceed, what is still required?", Context: f.request.Spec.Context[0].Text, Instructions: f.request.Spec.Instructions, CachedAnswer: answer, Intent: "explain-preview"}
			first, err := f.verifier.Verify(t.Context(), request)
			if err != nil || !first.Accepted() || len(f.stages.requests) != 2 {
				t.Fatalf("initial proof: %+v %v", first, err)
			}
			if boundary == "runtime" {
				// Even keeping the adapter policy constant cannot reuse a job
				// after the provider's pinned runtime identity changes.
				f.stages.identity += "-changed"
			} else {
				token, err := f.broker.store.CreateToken(t.Context(), "independent-other-scope", "reviewer")
				if err != nil {
					t.Fatal(err)
				}
				principal, err := f.broker.store.Authenticate(t.Context(), token)
				if err != nil {
					t.Fatal(err)
				}
				f.verifier.runner.Broker = storedBroker{store: f.broker.store, principal: principal}
			}
			after, err := f.verifier.Verify(t.Context(), request)
			if err != nil || !after.Accepted() || after.Reused || len(f.stages.requests) != 4 || !after.Work[0].GenerationCalled || !after.Work[1].GenerationCalled || after.InputTokens == nil || *after.InputTokens != 182 {
				t.Fatalf("proof jobs crossed %s: %+v %v", boundary, after, err)
			}
		})
	}
}
