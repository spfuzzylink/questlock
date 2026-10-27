package inference

// Close releases idle sockets owned by this provider; it does not unload weights
// or cancel another inference request. The provider must no longer be in use.
func (p *OllamaProvider) Close() {
	if p == nil || p.client == nil {
		return
	}
	if transport, ok := p.client.Transport.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}
}
