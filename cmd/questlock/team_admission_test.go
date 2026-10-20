package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/admission"
)

func teamAdmissionTestConfig() admission.Config {
	c := admission.DefaultConfig()
	c.SampleInterval = 10 * time.Millisecond
	c.RampInterval = 10 * time.Millisecond
	c.MaxSampleAge = time.Second
	c.ProbeTimeout = time.Second
	c.RecoverySamples = 2
	c.TaskTimeout = time.Second
	return c
}

func teamAdmissionHealthySnapshot() admission.Snapshot {
	return admission.Snapshot{At: time.Now(), Known: true, Source: "fixture",
		CPUCapacityMilli: 8000, LoadCapacityMilli: 8000, LoadMilli: 1000,
		AvailableMemoryBytes: 2 << 30, MemoryPressure: admission.Normal}
}

func teamAdmissionWait(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("bounded wait expired:", name)
	}
}

func TestTeamAdmissionWaitsForCapacityThenExecutesOnce(t *testing.T) {
	c := teamAdmissionTestConfig()
	var probes, executions atomic.Int64
	source := admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		s := teamAdmissionHealthySnapshot()
		if probes.Add(1) <= 3 {
			s.AvailableMemoryBytes = 0
		}
		return s, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := admitTeamRole(ctx, c, source, func(context.Context) error {
		executions.Add(1)
		if probes.Load() < int64(3+c.RecoverySamples) {
			return errors.New("execution preceded healthy recovery samples")
		}
		return nil
	})
	if err != nil || executions.Load() != 1 {
		t.Fatal("capacity recovery did not admit exactly one role", err, executions.Load(), probes.Load())
	}
}

func TestTeamAdmissionDeadlineWhileBlockedExecutesNothing(t *testing.T) {
	var probes, executions atomic.Int64
	source := admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		probes.Add(1)
		s := teamAdmissionHealthySnapshot()
		s.AvailableMemoryBytes = 0
		return s, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	err := admitTeamRole(ctx, teamAdmissionTestConfig(), source, func(context.Context) error {
		executions.Add(1)
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || executions.Load() != 0 || probes.Load() == 0 {
		t.Fatal("blocked role was executed or deadline lost", err, executions.Load(), probes.Load())
	}
}

func TestTeamAdmissionRejectsMissingProtectedAndUndersizedInferenceLanes(t *testing.T) {
	for _, mode := range []string{"missing", "protected", "undersized"} {
		t.Run(mode, func(t *testing.T) {
			c := teamAdmissionTestConfig()
			for i, lane := range c.Lanes {
				if lane.Name != admission.LaneInference {
					continue
				}
				switch mode {
				case "missing":
					c.Lanes = append(c.Lanes[:i], c.Lanes[i+1:]...)
				case "protected":
					c.Lanes[i].Protected = true
				case "undersized":
					c.Lanes[i].MemoryBytes = admission.QueueEntryBytes
				}
				break
			}
			var probes, executions atomic.Int64
			source := admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
				probes.Add(1)
				return teamAdmissionHealthySnapshot(), nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err := admitTeamRole(ctx, c, source, func(context.Context) error {
				executions.Add(1)
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), "unprotected inference lane") || probes.Load() != 0 || executions.Load() != 0 {
				t.Fatal("invalid inference lane reached admission or execution", err, probes.Load(), executions.Load())
			}
		})
	}
}

func TestTeamAdmissionDoesNotRetryAnExecutedFailure(t *testing.T) {
	want := errors.New("local model quality failed")
	var executions atomic.Int64
	source := admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		return teamAdmissionHealthySnapshot(), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := admitTeamRole(ctx, teamAdmissionTestConfig(), source, func(context.Context) error {
		executions.Add(1)
		return want
	})
	if !errors.Is(err, want) || executions.Load() != 1 {
		t.Fatal("executed failure was retried or replaced", err, executions.Load())
	}
}

func TestTeamAdmissionCancellationJoinsBlockedProbeBeforeReturning(t *testing.T) {
	entered, stopped := make(chan struct{}), make(chan struct{})
	var executions atomic.Int64
	source := admission.SourceFunc(func(ctx context.Context) (admission.Snapshot, error) {
		close(entered)
		defer close(stopped)
		<-ctx.Done()
		return admission.Snapshot{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- admitTeamRole(ctx, teamAdmissionTestConfig(), source, func(context.Context) error {
			executions.Add(1)
			return nil
		})
	}()
	teamAdmissionWait(t, entered, "first blocked probe")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || executions.Load() != 0 {
			t.Fatal("cancellation admitted a role or lost its cause", err, executions.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("admission did not stop after cancellation")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("admission returned before its probe stopped")
	}
}

func TestTeamAdmissionCancellationStopsAdmittedJobWithoutRetry(t *testing.T) {
	c := teamAdmissionTestConfig()
	c.RecoverySamples = 1
	c.ProbeTimeout = 5 * time.Second
	c.TaskTimeout = 5 * time.Second
	started, finished := make(chan struct{}), make(chan struct{})
	probeBlocked, probeStopped := make(chan struct{}), make(chan struct{})
	var executions atomic.Int64
	var probeOnce, probeStoppedOnce sync.Once
	source := admission.SourceFunc(func(ctx context.Context) (admission.Snapshot, error) {
		if executions.Load() == 0 {
			return teamAdmissionHealthySnapshot(), nil
		}
		probeOnce.Do(func() { close(probeBlocked) })
		defer probeStoppedOnce.Do(func() { close(probeStopped) })
		<-ctx.Done()
		return admission.Snapshot{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- admitTeamRole(ctx, c, source, func(jobCtx context.Context) error {
			executions.Add(1)
			close(started)
			defer close(finished)
			<-jobCtx.Done()
			return jobCtx.Err()
		})
	}()
	teamAdmissionWait(t, started, "admitted job")
	teamAdmissionWait(t, probeBlocked, "next probe")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || executions.Load() != 1 {
			t.Fatal("admitted cancellation was retried or lost", err, executions.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("admitted job did not stop after cancellation")
	}
	for name, signal := range map[string]<-chan struct{}{"executor": finished, "probe": probeStopped} {
		select {
		case <-signal:
		default:
			t.Fatal("admission returned before cleanup:", name)
		}
	}
}
