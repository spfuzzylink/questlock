package delegation

import (
	"context"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/semantic"
)

func TestIndependentThinkingDeadlineDoesNotChangeRetrievalPartition(t *testing.T) {
	f := independentFactorizedSetup(t)
	f.request.SemanticReuse.VerifierSourceThink = true
	f.request.SemanticReuse.VerifierMaxOutputTokens = 4096
	f.request.SemanticReuse.TimeoutMS = 120000
	long, err := ReusePartition(f.request, f.request.Policy.Local, f.verifier.Identity())
	if err != nil || len(long) != 64 {
		t.Fatalf("controlled thinking policy was rejected by shadow deadline normalization: %v", err)
	}
	f.request.SemanticReuse.TimeoutMS = 15000
	short, err := ReusePartition(f.request, f.request.Policy.Local, f.verifier.Identity())
	if err != nil || short != long {
		t.Fatalf("operational deadline split the retrieval facts partition: %v", err)
	}
	f.request.SemanticReuse.TimeoutMS = 600001
	if _, err := ReusePartition(f.request, f.request.Policy.Local, f.verifier.Identity()); err == nil {
		t.Fatal("partition bypassed controlled policy normalization")
	}
}

func TestIndependentThinkingResolverKeepsExplicitAndParentDeadlines(t *testing.T) {
	for _, test := range []struct {
		name          string
		resolverMS    int
		verifierMS    int
		parent        time.Duration
		wantRemaining time.Duration
	}{
		{"source thinking default", 0, 120000, 0, 120 * time.Second},
		{"explicit resolver limit", 15000, 120000, 0, 15 * time.Second},
		{"earlier parent", 0, 120000, 20 * time.Second, 20 * time.Second},
		{"larger explicit verifier", 0, 240000, 0, 240 * time.Second},
		{"larger verifier shorter resolver", 15000, 240000, 0, 15 * time.Second},
		{"larger verifier earlier parent", 0, 240000, 20 * time.Second, 20 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := independentFactorizedSetup(t)
			f.request.SemanticReuse.VerifierSourceThink = true
			f.request.SemanticReuse.VerifierMaxOutputTokens = 4096
			f.request.SemanticReuse.VerifierTimeoutMS = test.verifierMS
			f.request.SemanticReuse.TimeoutMS = test.resolverMS
			f.verifier.config.VerifierSourceThink = true
			f.verifier.config.VerifierMaxOutputTokens = 4096
			f.verifier.config.VerifierTimeoutMS = test.verifierMS
			f.seed(t)
			var captured []time.Duration
			f.router.Reuse.EmbedderFactory = func(ctx context.Context, _ semantic.Config) (semantic.Embedder, error) {
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Error("resolver lost its bounded context")
				}
				captured = append(captured, time.Until(deadline))
				return &shadowFailureEmbedder{}, nil
			}
			ctx := t.Context()
			if test.parent != 0 {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, test.parent)
				defer stop()
			}
			f.request.Spec.Input = "What must still happen before the preview proceeds?"
			out, err := f.router.Run(ctx, f.request)
			if err != nil || out.SemanticReuse == nil || !out.SemanticReuse.GenerationAvoided || len(captured) == 0 {
				t.Fatalf("bounded thinking resolver failed before applicability: %+v %v", out.SemanticReuse, err)
			}
			for _, remaining := range captured {
				if remaining > test.wantRemaining || remaining < test.wantRemaining-2*time.Second {
					t.Fatalf("deadline was widened or truncated: got=%v want=%v", remaining, test.wantRemaining)
				}
			}
		})
	}
}
