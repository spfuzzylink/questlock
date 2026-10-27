package inference

import (
	"crypto/sha256"
	"encoding/hex"
)

// RuntimeIdentity returns a content-free binding to the provider's captured
// runtime identity. It performs no discovery and changes no plan or job keys.
func (r Runner) RuntimeIdentity() string {
	if r.Provider == nil || r.Provider.Identity() == "" {
		return ""
	}
	d := sha256.Sum256([]byte(r.Provider.Identity()))
	return hex.EncodeToString(d[:])
}
