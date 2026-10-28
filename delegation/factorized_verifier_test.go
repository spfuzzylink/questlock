package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/semantic"
)

const factorizedTestSupport = `{"verdict":"accept","supported":true,"complete":true}`
const factorizedTestPair = `{"verdict":"accept","original_covers_current":true,"current_covers_original":true,"same_output_contract":true}`

type factorizedTestProvider struct {
	mu            sync.Mutex
	support, pair string
	failStage     string
	unknownUsage  bool
	requests      []inference.Request
	counts        int
	sourceCalls   int
	pairCalls     int
}

func (*factorizedTestProvider) Identity() string { return "factorized-offline-fixture-v1" }

func (*factorizedTestProvider) stage(request inference.Request) string {
	if strings.Contains(request.Input, `"cached_answer"`) {
		return "source"
	}
	return "equivalence"
}

func (p *factorizedTestProvider) CountInputTokens(_ context.Context, request inference.Request) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.counts++
	if p.stage(request) == "source" {
		return 101, nil
	}
	return 73, nil
}

func (p *factorizedTestProvider) Generate(_ context.Context, request inference.Request) (inference.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, request)
	stage := p.stage(request)
	text, input := p.pair, int64(73)
	if stage == "source" {
		p.sourceCalls++
		text, input = p.support, 101
	} else {
		p.pairCalls++
	}
	if p.failStage == stage {
		return inference.Response{}, errors.New("synthetic unknown generation outcome")
	}
	response := knownResult(text).Response
	response.Usage.InputTokens, response.Usage.UncachedInputTokens = input, input
	if p.unknownUsage {
		response.Usage.UncachedInputTokens = 0
	}
	return response, nil
}

func factorizedTestRequest() semantic.ValidityRequest {
	answer := "For the approved preview, Mira owns the remaining access review. Public release approval is absent."
	return semantic.ValidityRequest{
		Source:           &semantic.ValiditySource{Key: "inference/v1/jobs/" + strings.Repeat("a", 64), Version: 1, SHA256: strings.Repeat("b", 64), OutputSHA256: reuseHash(answer), RequestFingerprint: strings.Repeat("c", 64)},
		OriginalQuestion: "Who owns the remaining access review for the preview?",
		Question:         "For the preview, who is responsible for the remaining access review?",
		Instructions:     "Explain only the evidence; do not perform an action.",
		Context:          "Preview is approved. Mira owns the remaining access review. Public release approval is absent.",
		CachedAnswer:     answer,
		Bindings:         map[string]string{"channel": "preview"},
		Intent:           "explain-preview-review-owner",
	}
}

func factorizedTestVerifier(t *testing.T, broker inference.Broker) (*FactorizedVerifier, *factorizedTestProvider) {
	t.Helper()
	if broker == nil {
		broker = testBroker(t)
	}
	config := controlledTestConfig()
	config.VerifierPolicy = "factorized-v1"
	provider := &factorizedTestProvider{support: factorizedTestSupport, pair: factorizedTestPair}
	return &FactorizedVerifier{runner: &inference.Runner{Broker: broker, Provider: provider}, config: config, identity: reuseHash("factorized-offline-policy-v1")}, provider
}

func TestFactorizedDecisionStrictPrimitiveContract(t *testing.T) {
	for _, schema := range []struct{ left, right string }{{"supported", "complete"}, {"original_covers_current", "current_covers_original"}} {
		valid := `{"verdict":"accept","` + schema.left + `":true,"` + schema.right + `":true}`
		parsed, err := parseFactorizedDecision([]byte(valid), schema.left, schema.right)
		if err != nil || !parsed.accepted() {
			t.Fatalf("valid contract: %+v %v", parsed, err)
		}
		for _, malformed := range []string{
			``, `null`, `[]`, `{"verdict":"accept"}`, valid + `{}`,
			strings.Replace(valid, `"accept"`, `null`, 1),
			strings.Replace(valid, `"accept"`, `"approve"`, 1),
			strings.Replace(valid, `true`, `null`, 1),
			strings.Replace(valid, `true`, `"true"`, 1),
			strings.Replace(valid, `true`, `{"value":true}`, 1),
			strings.Replace(valid, `true`, `[[[[true]]]]`, 1),
			strings.Replace(valid, `}`, `,"verdict":"accept"}`, 1),
			strings.Replace(valid, `}`, `,"`+schema.left+`":false}`, 1),
			strings.Replace(valid, `}`, `,"generation_called":false}`, 1),
			strings.Replace(valid, schema.right, "unexpected", 1),
			string([]byte{0xff}), strings.Repeat(" ", semantic.MaxVerdictBytes+1) + valid,
		} {
			if result, err := parseFactorizedDecision([]byte(malformed), schema.left, schema.right); err == nil || result.accepted() {
				t.Errorf("malformed decision accepted: %q %+v %v", malformed, result, err)
			}
		}
	}
}

func TestFactorizedCertificateAndPairReuseHaveSeparateUsage(t *testing.T) {
	v, provider := factorizedTestVerifier(t, nil)
	request := factorizedTestRequest()
	first, err := v.Verify(t.Context(), request)
	if err != nil || !first.Accepted() || !first.Complete || !first.Durable || !first.GenerationCalled || first.Reused || first.InputTokens == nil || *first.InputTokens != 174 || first.OutputTokens == nil || *first.OutputTokens != 40 || len(first.Work) != 2 || first.Work[0].Stage != "source" || first.Work[1].Stage != "equivalence" || !strings.HasPrefix(first.ReceiptKey, "semantic/v1/decisions/") {
		t.Fatalf("first decision: %+v %v", first, err)
	}
	repeated, err := v.Verify(t.Context(), request)
	if err != nil || !repeated.Accepted() || !repeated.Reused || repeated.GenerationCalled || repeated.InputTokens == nil || *repeated.InputTokens != 0 || repeated.OutputTokens == nil || *repeated.OutputTokens != 0 || repeated.ReceiptKey != first.ReceiptKey {
		t.Fatalf("exact decision repeat: %+v %v", repeated, err)
	}
	request.Question = "Which person owns the remaining preview access review?"
	novel, err := v.Verify(t.Context(), request)
	if err != nil || !novel.Accepted() || novel.Reused || novel.InputTokens == nil || *novel.InputTokens != 73 || novel.OutputTokens == nil || *novel.OutputTokens != 20 || !novel.Work[0].Reused || novel.Work[1].Reused || novel.Work[0].ReceiptKey != first.Work[0].ReceiptKey || novel.Work[1].ReceiptKey == first.Work[1].ReceiptKey || novel.ReceiptKey == first.ReceiptKey || provider.sourceCalls != 1 || provider.pairCalls != 2 {
		t.Fatalf("certificate not reused independently: %+v %v calls=%d/%d", novel, err, provider.sourceCalls, provider.pairCalls)
	}
	for _, request := range provider.requests {
		if !strings.Contains(request.Input, `"context"`) || !strings.Contains(request.Input, `"instructions"`) || !strings.Contains(request.Input, `"bindings"`) || strings.Contains(request.Input, `"source"`) || strings.Contains(request.Input, strings.Repeat("b", 64)) {
			t.Fatalf("stage omitted background or exposed source metadata: %s", request.Input)
		}
		if provider.stage(request) == "source" && strings.Contains(request.Input, `"question"`) {
			t.Fatal("new question leaked into reusable source certificate")
		}
		if provider.stage(request) == "equivalence" && strings.Contains(request.Input, `"cached_answer"`) {
			t.Fatal("pair reread cached answer")
		}
		if request.MaxOutputTokens != v.config.VerifierMaxOutputTokens/2 {
			t.Fatal("stage output cap was not a fixed half")
		}
	}
	artifact, err := v.runner.Broker.Get(t.Context(), first.ReceiptKey)
	var receipt factorizedReceipt
	if err != nil || json.Unmarshal([]byte(artifact.Content), &receipt) != nil || artifact.Version != 1 || receipt.Support.Key != first.Work[0].ReceiptKey || receipt.Pair == nil || receipt.Pair.Key != first.Work[1].ReceiptKey || receipt.Support.OutputSHA256 != first.Work[0].OutputSHA256 || receipt.Pair.OutputSHA256 != first.Work[1].OutputSHA256 || reuseHash(artifact.Content) != first.OutputSHA256 {
		t.Fatalf("composite proof does not pin both jobs: %+v %v", receipt, err)
	}
}

func TestFactorizedCertificateBindsSourceAndCompleteSnapshot(t *testing.T) {
	for name, mutate := range map[string]func(*semantic.ValidityRequest){
		"source version": func(r *semantic.ValidityRequest) { r.Source.Version++ },
		"source content": func(r *semantic.ValidityRequest) { r.Source.SHA256 = strings.Repeat("d", 64) },
		"source key":     func(r *semantic.ValidityRequest) { r.Source.Key = "inference/v1/jobs/" + strings.Repeat("d", 64) },
		"source request": func(r *semantic.ValidityRequest) { r.Source.RequestFingerprint = strings.Repeat("d", 64) },
		"source answer": func(r *semantic.ValidityRequest) {
			r.CachedAnswer += " No execution occurred."
			r.Source.OutputSHA256 = reuseHash(r.CachedAnswer)
		},
		"original question": func(r *semantic.ValidityRequest) { r.OriginalQuestion += " Include the evidence." },
		"context":           func(r *semantic.ValidityRequest) { r.Context += " New evidence exists." },
		"instructions":      func(r *semantic.ValidityRequest) { r.Instructions += " Include caveats." },
		"bindings":          func(r *semantic.ValidityRequest) { r.Bindings["channel"] = "public" },
		"intent":            func(r *semantic.ValidityRequest) { r.Intent = "explain-public-release-owner" },
	} {
		t.Run(name, func(t *testing.T) {
			v, provider := factorizedTestVerifier(t, nil)
			request := factorizedTestRequest()
			first, err := v.Verify(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&request)
			after, err := v.Verify(t.Context(), request)
			if err != nil || !after.Accepted() || provider.sourceCalls != 2 || provider.pairCalls != 2 || after.Work[0].ReceiptKey == first.Work[0].ReceiptKey || after.Work[1].ReceiptKey == first.Work[1].ReceiptKey {
				t.Fatalf("drift reused previous proof: %+v %v calls=%d/%d", after, err, provider.sourceCalls, provider.pairCalls)
			}
		})
	}
}

func TestFactorizedRejectsMissingOrInvalidSourceBeforeInference(t *testing.T) {
	for name, mutate := range map[string]func(*semantic.ValidityRequest){
		"missing":         func(r *semantic.ValidityRequest) { r.Source = nil },
		"zero version":    func(r *semantic.ValidityRequest) { r.Source.Version = 0 },
		"bad key":         func(r *semantic.ValidityRequest) { r.Source.Key = "semantic/alias" },
		"bad hash":        func(r *semantic.ValidityRequest) { r.Source.SHA256 = "bad" },
		"uppercase hash":  func(r *semantic.ValidityRequest) { r.Source.SHA256 = strings.Repeat("A", 64) },
		"answer mismatch": func(r *semantic.ValidityRequest) { r.CachedAnswer += " Unsupported claim." },
		"empty question":  func(r *semantic.ValidityRequest) { r.Question = " " },
		"invalid UTF-8":   func(r *semantic.ValidityRequest) { r.Question = string([]byte{0xff}) },
		"invalid binding": func(r *semantic.ValidityRequest) { r.Bindings["bad"] = string([]byte{0xff}) },
	} {
		t.Run(name, func(t *testing.T) {
			v, provider := factorizedTestVerifier(t, nil)
			request := factorizedTestRequest()
			mutate(&request)
			out, err := v.Verify(t.Context(), request)
			if err == nil || out.Accepted() || len(out.Work) != 0 || provider.counts != 0 || provider.sourceCalls != 0 || provider.pairCalls != 0 {
				t.Fatalf("unbound source reached inference: %+v %v", out, err)
			}
		})
	}
}

func TestFactorizedKnownAbstentionsRetainUsageAndSkipUnneededPair(t *testing.T) {
	for _, test := range []struct {
		name, source, pair string
		stages             int
		verdict            string
	}{
		{"source reject", `{"verdict":"reject","supported":false,"complete":false}`, factorizedTestPair, 1, "reject"},
		{"supported but incomplete", `{"verdict":"accept","supported":true,"complete":false}`, factorizedTestPair, 1, "unknown"},
		{"complete but unsupported", `{"verdict":"accept","supported":false,"complete":true}`, factorizedTestPair, 1, "unknown"},
		{"source malformed", `{"verdict":"accept","supported":true,"complete":true,"complete":true}`, factorizedTestPair, 1, "unknown"},
		{"pair malformed", factorizedTestSupport, `{"verdict":"accept"}`, 2, "unknown"},
		{"one-way coverage", factorizedTestSupport, `{"verdict":"accept","original_covers_current":true,"current_covers_original":false,"same_output_contract":false}`, 2, "unknown"},
		{"pair reject", factorizedTestSupport, `{"verdict":"reject","original_covers_current":false,"current_covers_original":false,"same_output_contract":false}`, 2, "reject"},
	} {
		t.Run(test.name, func(t *testing.T) {
			v, provider := factorizedTestVerifier(t, nil)
			provider.support, provider.pair = test.source, test.pair
			out, err := v.Verify(t.Context(), factorizedTestRequest())
			if err != nil || out.Accepted() || out.Verdict != test.verdict || !out.Complete || !out.Durable || !out.GenerationCalled || out.InputTokens == nil || out.OutputTokens == nil || len(out.Work) != test.stages || out.ReceiptKey == "" || provider.sourceCalls != 1 || provider.pairCalls != test.stages-1 {
				t.Fatalf("known abstention lost evidence: %+v %v", out, err)
			}
		})
	}
}

func TestFactorizedUnknownStageIsNeverRegeneratedAutomatically(t *testing.T) {
	for _, stage := range []string{"source", "equivalence"} {
		t.Run(stage, func(t *testing.T) {
			v, provider := factorizedTestVerifier(t, nil)
			provider.failStage = stage
			out, err := v.Verify(t.Context(), factorizedTestRequest())
			last := out.Work[len(out.Work)-1]
			if err == nil || out.Accepted() || !out.GenerationCalled || out.InputTokens != nil || out.OutputTokens != nil || last.Stage != stage || !last.GenerationCalled || last.Complete || last.Durable || last.InputTokens != nil || last.ReceiptKey == "" || out.ReceiptKey != "" {
				t.Fatalf("unknown stage lost: %+v %v", out, err)
			}
			calls := provider.sourceCalls + provider.pairCalls
			provider.failStage = ""
			again, err := v.Verify(t.Context(), factorizedTestRequest())
			if err == nil || again.Accepted() || provider.sourceCalls+provider.pairCalls != calls {
				t.Fatalf("uncertain stage regenerated: %+v %v", again, err)
			}
		})
	}
}

func TestFactorizedUnknownUsageNeverBecomesZero(t *testing.T) {
	v, provider := factorizedTestVerifier(t, nil)
	provider.unknownUsage = true
	out, err := v.Verify(t.Context(), factorizedTestRequest())
	if err == nil || out.Accepted() || !out.GenerationCalled || out.InputTokens != nil || out.OutputTokens != nil || len(out.Work) != 1 || out.Work[0].InputTokens != nil || !out.Work[0].GenerationCalled || provider.sourceCalls != 1 || provider.pairCalls != 0 {
		t.Fatalf("unknown certificate usage was hidden or treated as zero: %+v %v", out, err)
	}
}

type factorizedBlockingProvider struct {
	*factorizedTestProvider
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *factorizedBlockingProvider) Generate(ctx context.Context, request inference.Request) (inference.Response, error) {
	if p.stage(request) == "source" {
		p.once.Do(func() { close(p.started) })
		select {
		case <-p.release:
		case <-ctx.Done():
			return inference.Response{}, ctx.Err()
		}
	}
	return p.factorizedTestProvider.Generate(ctx, request)
}

func TestFactorizedConcurrentQuestionCannotDuplicatePendingCertificate(t *testing.T) {
	v, base := factorizedTestVerifier(t, nil)
	provider := &factorizedBlockingProvider{factorizedTestProvider: base, started: make(chan struct{}), release: make(chan struct{})}
	v.runner.Provider = provider
	var release sync.Once
	defer release.Do(func() { close(provider.release) })
	finished := make(chan error, 1)
	go func() {
		result, err := v.Verify(t.Context(), factorizedTestRequest())
		if err == nil && !result.Accepted() {
			err = errors.New("original verification did not accept fixture")
		}
		finished <- err
	}()
	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("source certificate generation did not start")
	}
	other := factorizedTestRequest()
	other.Question = "Which person owns the remaining preview access review?"
	result, err := v.Verify(t.Context(), other)
	if err == nil || result.Accepted() || len(result.Work) != 1 || result.Work[0].Stage != "source" || result.Work[0].GenerationCalled || result.Work[0].ReceiptKey == "" {
		t.Fatalf("pending certificate was bypassed or regenerated: %+v %v", result, err)
	}
	release.Do(func() { close(provider.release) })
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original source verification did not finish")
	}
	if base.sourceCalls != 1 || base.pairCalls != 1 || base.counts != 2 {
		t.Fatalf("concurrent question duplicated source work: source=%d pair=%d counts=%d", base.sourceCalls, base.pairCalls, base.counts)
	}
}

type factorizedReceiptFaultBroker struct {
	inference.Broker
	write, confirm, conflict bool
}

func (b factorizedReceiptFaultBroker) Get(ctx context.Context, key string) (protocol.Artifact, error) {
	if strings.HasPrefix(key, "semantic/v1/decisions/") {
		if b.conflict {
			return protocol.Artifact{Key: key, Version: 1, Content: "conflicting proof"}, nil
		}
		if b.write && !b.confirm {
			if _, err := b.Broker.Get(ctx, key); err == nil {
				return protocol.Artifact{}, errors.New("synthetic receipt read unavailable")
			}
		}
	}
	return b.Broker.Get(ctx, key)
}

func (b factorizedReceiptFaultBroker) Publish(ctx context.Context, request protocol.PublishRequest) (protocol.PublishResult, error) {
	if strings.HasPrefix(request.Key, "semantic/v1/decisions/") {
		if b.write {
			if _, err := b.Broker.Publish(ctx, request); err != nil {
				return protocol.PublishResult{}, err
			}
		}
		return protocol.PublishResult{}, errors.New("synthetic publication acknowledgement lost")
	}
	return b.Broker.Publish(ctx, request)
}

func TestFactorizedCompositePublicationMustBeConfirmed(t *testing.T) {
	for _, test := range []struct {
		name                     string
		write, confirm, conflict bool
		wantError                bool
	}{
		{"not published", false, false, false, true},
		{"published but unreadable", true, false, false, true},
		{"identical read confirmation", true, true, false, false},
		{"conflicting receipt", false, false, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			broker := factorizedReceiptFaultBroker{Broker: testBroker(t), write: test.write, confirm: test.confirm, conflict: test.conflict}
			v, provider := factorizedTestVerifier(t, broker)
			out, err := v.Verify(t.Context(), factorizedTestRequest())
			if (err != nil) != test.wantError || out.Durable == test.wantError || out.Accepted() == test.wantError || out.InputTokens == nil || *out.InputTokens != 174 || len(out.Work) != 2 || provider.sourceCalls != 1 || provider.pairCalls != 1 || out.ReceiptKey == "" && !test.wantError {
				t.Fatalf("publication evidence mismatch: %+v %v", out, err)
			}
		})
	}
}

func TestFactorizedStageUsageSettlesActualSharedTokenLedger(t *testing.T) {
	broker := testBroker(t)
	v, provider := factorizedTestVerifier(t, broker)
	v.DailyTokenBudget = &inference.TokenBudget{InputTokens: 1000000, OutputTokens: 1000000, TotalTokens: 1000000}
	fixed := time.Date(2032, 4, 5, 12, 0, 0, 0, time.UTC)
	v.runner.Now = func() time.Time { return fixed }
	request := factorizedTestRequest()
	for _, question := range []string{request.Question, request.Question, "Which person owns the remaining preview access review?"} {
		request.Question = question
		if result, err := v.Verify(t.Context(), request); err != nil || !result.Accepted() {
			t.Fatalf("verification: %+v %v", result, err)
		}
	}
	artifact, err := broker.Get(t.Context(), "inference/v1/token-budget/2032-04-05")
	var ledger struct {
		Entries map[string]struct {
			Reserved inference.TokenBudget  `json:"reserved"`
			Actual   *inference.TokenBudget `json:"actual"`
		} `json:"entries"`
	}
	if err != nil || json.Unmarshal([]byte(artifact.Content), &ledger) != nil || len(ledger.Entries) != 3 {
		t.Fatalf("one source and two pair jobs were not separately charged: %+v %v", ledger, err)
	}
	var input, output int64
	for _, entry := range ledger.Entries {
		if entry.Actual == nil || entry.Reserved.InputTokens != v.config.VerifierMaxInputTokens/2 || entry.Reserved.OutputTokens != v.config.VerifierMaxOutputTokens/2 {
			t.Fatalf("missing actual usage or unstable split budget: %+v", entry)
		}
		input += entry.Actual.InputTokens
		output += entry.Actual.OutputTokens
	}
	if input != 247 || output != 60 || provider.sourceCalls != 1 || provider.pairCalls != 2 {
		t.Fatalf("actual ledger counted historical work twice: input=%d output=%d calls=%d/%d", input, output, provider.sourceCalls, provider.pairCalls)
	}
}
