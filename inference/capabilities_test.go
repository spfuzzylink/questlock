package inference

import "testing"

func TestSelfHostedCapabilityIsOptionalExplicitAssertion(t *testing.T) {
	var ollama *OllamaProvider
	var sglang *SGLangProvider
	if ollama.SelfHosted() || sglang.SelfHosted() {
		t.Fatal("nil runtime declared self-hosted")
	}
	if !(&OllamaProvider{}).SelfHosted() {
		t.Fatal("local Ollama capability missing")
	}
	if (&SGLangProvider{}).SelfHosted() || !(&SGLangProvider{cfg: SGLangConfig{SelfHosted: true}}).SelfHosted() {
		t.Fatal("SGLang assertion ignored deployment contract")
	}
	if _, ok := any(&HTTPProvider{}).(SelfHostedProvider); ok {
		t.Fatal("hosted HTTP adapter asserted self-hosting")
	}
	if _, ok := any(&OpenRouterProvider{}).(SelfHostedProvider); ok {
		t.Fatal("gateway adapter asserted self-hosting")
	}
}
