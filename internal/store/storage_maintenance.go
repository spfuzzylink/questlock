package store

import (
	"context"
	"errors"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/retention"
)

const DefaultStorageInterval = 5 * time.Minute

// StartStorageMaintenance schedules one bounded batch per interval. An absent
// policy does nothing; resource pressure defers work without changing claim or
// payload eligibility. Stop must finish before closing the store.
func (s *Store) StartStorageMaintenance(ctx context.Context, interval time.Duration, source admission.Source) (stop func(), err error) {
	if interval < time.Second || interval > 24*time.Hour {
		return nil, errors.New("storage interval must be between 1s and 24h")
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				batchCtx, finish := context.WithTimeout(ctx, min(interval, 30*time.Second))
				_ = s.storageMaintainOnce(batchCtx, source)
				finish()
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}

func (s *Store) storageMaintainOnce(ctx context.Context, source admission.Source) error {
	started := time.Now()
	c, err := storagePolicy(ctx, s.db)
	if err != nil {
		s.emitStorage(ctx, "archive", started, StorageReport{}, err)
		return err
	}
	if c == nil {
		return nil
	}
	snapshot := admission.Snapshot{}
	if c.Adaptive && source != nil {
		probeCtx, finish := context.WithTimeout(ctx, 2*time.Second)
		snapshot, err = source.Sample(probeCtx)
		finish()
		if err != nil {
			snapshot.Known = false
		}
	}
	now := time.Now().UTC()
	_, err = s.StorageArchive(ctx, false, now, retention.Plan(*c, snapshot, now))
	return err
}
