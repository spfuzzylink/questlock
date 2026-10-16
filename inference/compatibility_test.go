package inference

import "testing"

func TestLegacyV04PlanIdentitySurvivesNewAdapters(t *testing.T) {
	// Captured from the published v0.4.0 CLI. Optional new provider settings
	// must not invalidate already stored results for unchanged legacy requests.
	p, err := BuildPlan(testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if p.Fingerprint != "63ccb99cd334aaede8a9ef09739895a12e4275ba144ba2dc81bb132ef2f91518" {
		t.Fatal("legacy request identity changed", p.Fingerprint)
	}
}

func TestNewProviderConfigurationCannotLeakAcrossAdapters(t *testing.T) {
	s := testSpec()
	s.OpenRouter = &OpenRouterConfig{UpstreamProvider: "openai", InputTokenUpperBound: 100, CacheWriteUsage: "not_applicable"}
	if _, err := BuildPlan(s); err == nil {
		t.Fatal("OpenRouter policy admitted on direct OpenAI")
	}
	s.OpenRouter = nil
	s.SGLang = &SGLangConfig{DeploymentID: "pinned", CacheNamespace: "scope", Profile: "dense", SelfHosted: true, MaxInputTokens: s.MaxInputTokens}
	if _, err := BuildPlan(s); err == nil {
		t.Fatal("self-hosted policy admitted on hosted provider")
	}
	s.Provider = "sglang"
	s.SGLang.MaxInputTokens--
	if _, err := BuildPlan(s); err == nil {
		t.Fatal("mismatched input caps admitted")
	}
}
