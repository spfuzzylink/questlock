package inference

// SelfHostedProvider is an optional assertion by a trusted provider
// implementation that it uses an operator-owned runtime without provider token
// metering. It is not remote attestation or evidence that hardware is free.
// Existing Provider implementations need not implement this capability.
type SelfHostedProvider interface {
	SelfHosted() bool
}

func (p *OllamaProvider) SelfHosted() bool { return p != nil }

func (p *SGLangProvider) SelfHosted() bool { return p != nil && p.cfg.SelfHosted }
