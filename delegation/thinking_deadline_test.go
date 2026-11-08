package delegation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/semantic"
)

// Captured from the immutable relation-v2 source before deadline changes.
const thinkingDeadlineLegacyFixtures = `[{"config_sha256":"84124af31454036e51f2ebd87b5fe3386386d662f034242729cf101a824616c0","name":"standalone-default","native_plan_fingerprint":"068f6687043194fa1fed1ee3e54ef2db9b9ddeb53af9cff4ff721e5e6e31b50c","pair":false,"policy":"","policy_identity":"8f14b9e53bcace43089f76ff82e5e3e7bdcced4a3111cfcd7f1cb18932579baf","source":false,"verifier":0,"whole":0},{"config_sha256":"48e0b9a1e43e7c5828319ae37d9cbec3263936596d92ec835f8bd6558d61691c","name":"factorized-default","native_plan_fingerprint":"eb67f1f10de4c56d5e6a59ed50bae47cd599ebdff4754bf94015e288f190369f","pair":false,"policy":"factorized-v1","policy_identity":"9bdaa4fddae13513fe9cee2cbbd895ec75723cbb1f9354ad0fe3ea3d8c4c9bcf","source":false,"verifier":0,"whole":0},{"config_sha256":"15804af8d5ec8027ea92474858cca24f691710ed9cf328dc74c6e37be0b4f1f3","name":"source-default","native_plan_fingerprint":"c6e612da9cef019e9e44ca344ea8742417d5f00f790db658f8c0aa84387d63cc","pair":false,"policy":"factorized-v1","policy_identity":"2502a4349a6019008f1788173c3f23b8e4825156ba6c522911785fb03bec95ba","source":true,"verifier":0,"whole":0},{"config_sha256":"974d52e696f91926f69e4bb7aae52ae7f411103e06527c90be52297a4ef9706b","name":"pair-default","native_plan_fingerprint":"2caa0b59e9324ab30eef5fbbf6e625fa8e64e478b7003b3aa979f245ffd15839","pair":true,"policy":"factorized-v1","policy_identity":"8b9be2f69d884d18f08d2f56e961a345b201fdd1a738bffb69a29ff33275337f","source":false,"verifier":0,"whole":0},{"config_sha256":"8169fb1dfe986302f1033de7dcfca3ac6bd8c3cd0cb0e63f19cc73c3eee2fa4c","name":"both-small-verifier","native_plan_fingerprint":"65cec34e6516a7fbd9d387036947017b44ae925a316d175dfae753c52fb3e515","pair":true,"policy":"factorized-v1","policy_identity":"de4cefbd8ad79f1202f085c5b3bf8f2d0d7bd5fb53be0ee2209f8930b55c1e5f","source":true,"verifier":30000,"whole":0},{"config_sha256":"974d52e696f91926f69e4bb7aae52ae7f411103e06527c90be52297a4ef9706b","name":"pair-explicit120","native_plan_fingerprint":"2caa0b59e9324ab30eef5fbbf6e625fa8e64e478b7003b3aa979f245ffd15839","pair":true,"policy":"factorized-v1","policy_identity":"8b9be2f69d884d18f08d2f56e961a345b201fdd1a738bffb69a29ff33275337f","source":false,"verifier":120000,"whole":0},{"config_sha256":"ace8b81336454334f7c5d3b853255ee4385f8766bafd7405d700a9b7c4165863","name":"pair-short-whole","native_plan_fingerprint":"69b5d4ced088ae01b4e900b5f9b2dca82f2847dd06e7bb79fe0762b4b6246782","pair":true,"policy":"factorized-v1","policy_identity":"d911ffd5cb8f49907bd108b571d15b8a141be8f01b3dbc27eff8a7c7c40239a7","source":false,"verifier":60000,"whole":45000},{"config_sha256":"32c14214eaee54f15414182c522a7c6af18f7e6064c03cc4b763748d6398ace0","name":"ordinary-max","native_plan_fingerprint":"7a96c547052e9af4430df953f030d0bf0f1aadfebc7d599556d208c96cd46c7e","pair":false,"policy":"factorized-v1","policy_identity":"cad432bb959fd8e4585f4a8c3d4abedaa3a8f1af1b56b7455b6ee7f981cbb7cf","source":false,"verifier":120000,"whole":60000}]`

// V20 intentionally replaces the v19 prompt/output-guard contract. Keep the
// historical pins above and pin the reviewed successor identities explicitly.
const targetContractV20Identities = `[{"name":"standalone-default","native_plan_fingerprint":"95b05fba34b6cedd44e70788eb58b77986ca69432d89192f6fb2358f829f687d","policy_identity":"a219c6db8dd2d69a0e99f752e2b449469a71fa39a7f3160ab093eeddbae39320"},{"name":"factorized-default","native_plan_fingerprint":"e683c3909f89ed5d8de3b67e97077935064a2b623cd492dad6ffa43a400f8b0b","policy_identity":"d22cb65fa70fc5a5c4c8a1390d22c5098292222bf338da98c75695761c73e1a8"},{"name":"source-default","native_plan_fingerprint":"5640e3cd93a02818c2eaa22a0654cca1c2a818f3f529f282fd153d0710df23a3","policy_identity":"c02537841ba1e3a44e55101a640bd28ed4e3def84fe4f72de9f08abf05aad63a"},{"name":"pair-default","native_plan_fingerprint":"a887788eae399f1111d0c9863b95b9c64516bf732f0b5dfda5ca2659752c1da2","policy_identity":"c79e3c982c09b8182df976012f0a985fe9caf673a0a348603f0e27cb411fffa1"},{"name":"both-small-verifier","native_plan_fingerprint":"90d5342084ed4406398610c5d02bd38846625bcc7e3c4e2c18e7798c05d7bc74","policy_identity":"aec13e286520a6e5a98ac2846a9ff584d2bab7d93b1f7607a6a6e687b381b552"},{"name":"pair-explicit120","native_plan_fingerprint":"a887788eae399f1111d0c9863b95b9c64516bf732f0b5dfda5ca2659752c1da2","policy_identity":"c79e3c982c09b8182df976012f0a985fe9caf673a0a348603f0e27cb411fffa1"},{"name":"pair-short-whole","native_plan_fingerprint":"15dc53256fc00eba4c81f0d59c26d5fa1336256012666ea7d136471b6cc0d822","policy_identity":"0b8fac7d9058235ef139ec0b2796b32e5e565f105d31e9f0eabb6009c2d26eb9"},{"name":"ordinary-max","native_plan_fingerprint":"b339f089ff68a3b2bea7757e97c679da3827cb674c1e16f07e1c9ed4d89d55a7","policy_identity":"cf3bc930ffe0ff7db6528d3a4b0cbfa94922d24b604065a945162315c024d915"}]`

func TestThinkingDeadlinePreservesLegacyConfigAndPinsV20Identities(t *testing.T) {
	var fixtures []struct {
		Name, Policy          string
		Source, Pair          bool
		Whole, Verifier       int
		ConfigSHA256          string `json:"config_sha256"`
		PolicyIdentity        string `json:"policy_identity"`
		NativePlanFingerprint string `json:"native_plan_fingerprint"`
	}
	if json.Unmarshal([]byte(thinkingDeadlineLegacyFixtures), &fixtures) != nil {
		t.Fatal("bad legacy fixture")
	}
	var current []struct {
		Name                  string
		PolicyIdentity        string `json:"policy_identity"`
		NativePlanFingerprint string `json:"native_plan_fingerprint"`
	}
	if json.Unmarshal([]byte(targetContractV20Identities), &current) != nil || len(current) != len(fixtures) {
		t.Fatal("invalid v20 identity pins")
	}
	for index, x := range fixtures {
		pin := current[index]
		if pin.Name != x.Name {
			t.Fatal("v20 fixture name mismatch")
		}

		c := controlledTestConfig()
		c.VerifierPolicy = x.Policy
		c.VerifierSourceThink = x.Source
		c.VerifierPairThink = x.Pair
		c.TimeoutMS = x.Whole
		c.VerifierTimeoutMS = x.Verifier
		c.VerifierMaxInputTokens = 0
		c.VerifierMaxOutputTokens = 0
		c, err := semantic.NormalizeReuseConfig(c)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(c)
		digest := sha256.Sum256(raw)
		identity := factorizedPolicyIdentity(c, strings.Repeat("a", 64), strings.Repeat("b", 64))
		v := FactorizedVerifier{config: c, identity: identity}
		p, err := inference.BuildPlan(v.stageSpec("equivalence", "stable", factorizedPairInstructions, `{"context":"unchanged"}`, "answer_covers_current", "unchanged_facts"))
		if err != nil || hex.EncodeToString(digest[:]) != x.ConfigSHA256 || identity != pin.PolicyIdentity || p.Fingerprint != pin.NativePlanFingerprint || identity == x.PolicyIdentity || p.Fingerprint == x.NativePlanFingerprint {
			t.Fatal("legacy config bytes or reviewed v20 identity changed", x.Name, err)
		}
	}
}

type thinkingDeadlineProbe struct {
	*factorizedTestProvider
	deadlines []time.Time
	counted   []inference.Request
}

func (p *thinkingDeadlineProbe) record(ctx context.Context) {
	d, ok := ctx.Deadline()
	if !ok {
		p.deadlines = append(p.deadlines, time.Time{})
	} else {
		p.deadlines = append(p.deadlines, d)
	}
}
func (p *thinkingDeadlineProbe) CountInputTokens(ctx context.Context, r inference.Request) (int64, error) {
	p.record(ctx)
	p.counted = append(p.counted, r)
	return p.factorizedTestProvider.CountInputTokens(ctx, r)
}
func (p *thinkingDeadlineProbe) Generate(ctx context.Context, r inference.Request) (inference.Response, error) {
	p.record(ctx)
	return p.factorizedTestProvider.Generate(ctx, r)
}

func TestThinkingDeadlineIsOneSharedContextAndCeilNativeCountGenerateOptions(t *testing.T) {
	for _, milliseconds := range []int{240000, 240001, 600000} {
		v, base := factorizedTestVerifier(t, nil)
		c := v.config
		c.TimeoutMS = 0
		c.VerifierPairThink = true
		c.VerifierTimeoutMS = milliseconds
		c.VerifierMaxOutputTokens = 4096
		var err error
		v.config, err = semantic.NormalizeReuseConfig(c)
		if err != nil {
			t.Fatal(err)
		}
		p := &thinkingDeadlineProbe{factorizedTestProvider: base}
		v.runner.Provider = p
		started := time.Now()
		out, err := v.Verify(t.Context(), factorizedTestRequest())
		if err != nil || !out.Accepted() || len(p.deadlines) != 4 || len(p.counted) != 2 {
			t.Fatal("bounded fixture failed", err)
		}
		for _, deadline := range p.deadlines {
			if !deadline.Equal(p.deadlines[0]) || deadline.Before(started.Add(time.Duration(milliseconds)*time.Millisecond-time.Second)) || deadline.After(time.Now().Add(time.Duration(milliseconds)*time.Millisecond)) {
				t.Fatal("source and pair received additive/widened/truncated deadlines")
			}
		}
		for i, r := range p.counted {
			generated := base.requests[i]
			if r.Ollama.GenerationTimeoutSeconds == nil || *r.Ollama.GenerationTimeoutSeconds != int64((milliseconds+999)/1000) || generated.Ollama.GenerationTimeoutSeconds == nil || *generated.Ollama.GenerationTimeoutSeconds != *r.Ollama.GenerationTimeoutSeconds || r.MaxOutputTokens != 2048 || r.ContextWindow != generated.ContextWindow || r.Input != generated.Input || r.Prefix != generated.Prefix {
				t.Fatal("count/generate deadline, complete input or cap differs")
			}
		}
	}
}

type thinkingDeadlineCallerProbe struct {
	*canceledPairThinkingProvider
	t        *testing.T
	deadline time.Time
}

func (p *thinkingDeadlineCallerProbe) Generate(ctx context.Context, r inference.Request) (inference.Response, error) {
	if p.stage(r) == "equivalence" {
		if deadline, ok := ctx.Deadline(); !ok || !deadline.Equal(p.deadline) {
			p.t.Error("pair did not inherit the earlier caller deadline")
		}
	}
	return p.canceledPairThinkingProvider.Generate(ctx, r)
}

func cancelAfterThinkingPairStarts(started <-chan struct{}, cancel context.CancelFunc) <-chan bool {
	observed := make(chan bool, 1)
	go func() {
		pairStarted := false
		select {
		case <-started:
			pairStarted = true
		case <-time.After(3 * time.Second):
		}
		cancel()
		observed <- pairStarted
	}()
	return observed
}

func TestThinkingLongDeadlineCallerCancellationRetainsUsageAndFence(t *testing.T) {
	v, base := factorizedTestVerifier(t, nil)
	c := v.config
	c.TimeoutMS = 0
	c.VerifierPairThink = true
	c.VerifierTimeoutMS = 240000
	var err error
	v.config, err = semantic.NormalizeReuseConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	p := &thinkingDeadlineCallerProbe{canceledPairThinkingProvider: &canceledPairThinkingProvider{factorizedTestProvider: base, started: make(chan struct{})}, t: t, deadline: deadline}
	v.runner.Provider = p
	// Cancellation must interrupt a started pair, rather than race durable setup.
	started := cancelAfterThinkingPairStarts(p.started, cancel)
	out, err := v.Verify(ctx, factorizedTestRequest())
	if !<-started {
		t.Fatal("pair did not start before the bounded setup wait")
	}
	if err == nil || out.Accepted() || len(out.Work) != 2 || out.Work[0].InputTokens == nil || out.Work[1].InputTokens != nil || !out.Work[1].GenerationCalled {
		t.Fatal("earlier caller lost source usage or unknown pair")
	}
	_, err = v.Verify(t.Context(), factorizedTestRequest())
	if err == nil || p.pairStarts.Load() != 1 || base.sourceCalls != 1 {
		t.Fatal("long profile bypassed an uncertain claim")
	}
}

type thinkingDeadlineControlledCancel struct {
	*independentFactorizedProvider
	t                *testing.T
	expectedDeadline func() time.Time
	started          chan struct{}
	pairStarts       int
	sourceDeadline   time.Time
	pairDeadline     time.Time
	pairError        error
}

func (p *thinkingDeadlineControlledCancel) Generate(ctx context.Context, r inference.Request) (inference.Response, error) {
	if strings.HasPrefix(r.Prefix, factorizedPairInstructions) {
		p.pairStarts++
		var ok bool
		p.pairDeadline, ok = ctx.Deadline()
		if !ok || !p.pairDeadline.Equal(p.expectedDeadline()) {
			p.t.Error("pair did not inherit the exact resolver deadline")
		}
		if p.pairStarts == 1 {
			close(p.started)
		}
		<-ctx.Done()
		p.pairError = ctx.Err()
		return inference.Response{}, p.pairError
	}
	p.sourceDeadline, _ = ctx.Deadline()
	return p.independentFactorizedProvider.Generate(ctx, r)
}

func TestThinkingLongDeadlineControlledResolverCancellationKeepsFence(t *testing.T) {
	for _, shortResolver := range []bool{true, false} {
		f := independentFactorizedSetup(t)
		c := *f.request.SemanticReuse
		c.VerifierPairThink = true
		c.VerifierTimeoutMS = 240000
		c.TimeoutMS = 240000
		if shortResolver {
			c.TimeoutMS = 2000
		}
		var err error
		c, err = semantic.NormalizeReuseConfig(c)
		if err != nil {
			t.Fatal(err)
		}
		f.request.SemanticReuse = &c
		f.verifier.config = c
		f.seed(t)
		var resolverDeadline, beforeRouterRun time.Time
		embedderFactory := f.router.Reuse.EmbedderFactory
		f.router.Reuse.EmbedderFactory = func(ctx context.Context, c semantic.Config) (semantic.Embedder, error) {
			observedAt := time.Now()
			resolverDeadline, _ = ctx.Deadline()
			// Bracket deadline construction, independent of setup/completion speed.
			if shortResolver && (resolverDeadline.Before(beforeRouterRun.Add(2*time.Second)) || resolverDeadline.After(observedAt.Add(2*time.Second))) {
				t.Error("resolver did not construct the configured 2s deadline")
				return nil, context.DeadlineExceeded
			}
			return embedderFactory(ctx, c)
		}
		p := &thinkingDeadlineControlledCancel{independentFactorizedProvider: f.stages, t: t, expectedDeadline: func() time.Time { return resolverDeadline }, started: make(chan struct{})}
		f.verifier.runner.Provider = p
		ctx := t.Context()
		var started <-chan bool
		var callerDeadline time.Time
		if !shortResolver {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			callerDeadline, _ = ctx.Deadline()
			started = cancelAfterThinkingPairStarts(p.started, cancel)
		}
		f.request.Spec.Input = "Which review must still happen before the preview proceeds?"
		beforeRouterRun = time.Now()
		out, err := f.router.Run(ctx, f.request)
		if started != nil && !<-started {
			t.Fatal("controlled pair did not start before the bounded setup wait")
		}
		if resolverDeadline.IsZero() || !p.sourceDeadline.Equal(resolverDeadline) || !p.pairDeadline.Equal(resolverDeadline) {
			t.Fatal("source and pair did not share the exact shorter resolver deadline")
		}
		if shortResolver && p.pairError != context.DeadlineExceeded || !shortResolver && (!resolverDeadline.Equal(callerDeadline) || p.pairError != context.Canceled) {
			t.Fatal("controlled work did not obey the shorter resolver or caller cancellation")
		}
		if err == nil || out.SemanticReuse == nil || out.SemanticReuse.GenerationAvoided || p.pairStarts != 1 || f.target.generations != 1 || len(out.SemanticReuse.Verifications) != 1 {
			t.Fatalf("short resolver/caller did not stop controlled work: %+v %v", out.SemanticReuse, err)
		}
		work := out.SemanticReuse.Verifications[0].Work
		if len(work) != 2 || work[0].InputTokens == nil || work[1].InputTokens != nil || !work[1].GenerationCalled {
			t.Fatal("controlled cancellation lost known source/unknown pair evidence")
		}
		_, err = f.router.Run(t.Context(), f.request)
		if err == nil || p.pairStarts != 1 || f.target.generations != 1 {
			t.Fatal("canceled controlled pair retried or generated fallback")
		}
	}
}
