package admission

import (
	"context"
	"time"

	"golang.org/x/sys/unix"
)

func sampleSystem(ctx context.Context) (Snapshot, error) {
	read := func(path string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return readSmall(path)
	}
	var affinity unix.CPUSet
	if unix.SchedGetaffinity(0, &affinity) != nil {
		return Snapshot{At: time.Now(), Source: "linux_live_affinity"}, ErrSignals
	}
	s, err := sampleLinux(read, affinity.Count(), time.Now())
	if err != nil {
		s.Known = false
	}
	return s, err
}
