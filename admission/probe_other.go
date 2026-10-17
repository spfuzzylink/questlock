//go:build !linux && !darwin

package admission

import "context"

func sampleSystem(context.Context) (Snapshot, error) {
	return Snapshot{Source: "unsupported_platform"}, ErrSignals
}
