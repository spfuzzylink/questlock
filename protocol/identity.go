package protocol

import (
	"encoding/hex"
	"errors"
	"regexp"
)

// Identity is authenticated server evidence, not authority supplied by a writer.
// AgentID matters because successful operation IDs are namespaced by agent.
type Identity struct {
	Authority string `json:"authority"`
	Scope     string `json:"scope"`
	AgentID   string `json:"agent_id"`
}

var identityName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func (i Identity) Validate() error {
	b, err := hex.DecodeString(i.Authority)
	if err != nil || len(b) != 32 || !identityName.MatchString(i.Scope) || !identityName.MatchString(i.AgentID) {
		return errors.New("invalid authority identity")
	}
	return nil
}

const (
	ExpectedAuthorityHeader = "X-Questlock-Expected-Authority"
	ExpectedScopeHeader     = "X-Questlock-Expected-Scope"
	ExpectedAgentHeader     = "X-Questlock-Expected-Agent"
)
