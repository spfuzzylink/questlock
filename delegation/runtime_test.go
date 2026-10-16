package delegation

import (
	"context"
	"errors"
	"testing"

	"github.com/spfuzzylink/questlock/inference"
)

func TestSelfHostedSGLangStopsUnknownWithoutPaidEscalation(t *testing.T) {
	request := paidRequest()
	request.Policy.Local.Spec.Provider = "sglang"
	request.Policy.Local.Spec.SGLang = &inference.SGLangConfig{DeploymentID: "fixture", CacheNamespace: "scope-a", Profile: "hicache", SelfHosted: true, MaxInputTokens: request.Policy.Local.Spec.MaxInputTokens}
	calls := 0
	result, err := (Router{AllowPaid: true, Factory: func(Tier) (Runner, error) {
		calls++
		return &scriptedRunner{err: errors.New("unknown runtime completion")}, nil
	}}).Run(context.Background(), request)
	if !errors.Is(err, ErrStopped) || calls != 1 || len(result.Attempts) != 1 || result.Response != nil {
		t.Fatal("unknown runtime result escalated")
	}
	request.Policy.Escalation = nil
	if err = Validate(request, false); err != nil {
		t.Fatal("explicit self-hosted tier required paid permission", err)
	}
	request.Policy.Local.Spec.SGLang.SelfHosted = false
	if err = Validate(request, true); err == nil {
		t.Fatal("metered runtime claimed unmetered admission")
	}
}
