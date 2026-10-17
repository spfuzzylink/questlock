package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/revision"
)

// admissionQuest executes checked revisions against the real temporary broker.
// Resource readings are explicit fixtures; it makes no inference request.
func admissionQuest(out io.Writer) error {
	fmt.Fprintln(out, "ADMISSION QUEST — fixture pressure, real bounded queue and temporary SQLite broker; no model calls")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fixture, e := newModelQuestFixture(ctx)
	if e != nil {
		return e
	}
	defer fixture.close()
	c := admission.DefaultConfig()
	c.MaxWorkers = 2
	c.MinWorkers = 1
	c.MaxQueueRows = 8
	c.ProtectedQueueRows = 1
	c.QueueRowsPerWorker = 1
	c.RecoverySamples = 2
	c.SampleInterval = 10 * time.Millisecond
	c.RampInterval = 20 * time.Millisecond
	for i := range c.Lanes {
		c.Lanes[i].MaxWorkers = 1
	}
	var pressure atomic.Bool
	sample := func() admission.Snapshot {
		x := admission.Snapshot{At: time.Now(), Known: true, Source: "explicit_fixture", CPUCapacityMilli: 8000, LoadCapacityMilli: 8000, AvailableMemoryBytes: 2 << 30, MemoryPressure: admission.Normal}
		if pressure.Load() {
			x.MemoryPressure = admission.High
		}
		return x
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var first, second revision.Result
	s, e := admission.New(c, func(ctx context.Context, j admission.Job) error {
		if j.Lane == admission.LaneBroker {
			return nil
		}
		if j.ID == "first" {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		request, e := revision.Load(bytes.NewReader(j.Payload))
		if e != nil {
			return e
		}
		result, e := (revision.Runner{Broker: fixture.clients[0]}).Run(ctx, request)
		if e = reviseQuestCompleted(result, e); e != nil {
			return e
		}
		if j.ID == "first" {
			first = result
		} else {
			second = result
		}
		return nil
	})
	if e != nil {
		return e
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	var futures []<-chan admission.Outcome
	go func() {
		done <- s.Run(runCtx, admission.SourceFunc(func(context.Context) (admission.Snapshot, error) { return sample(), nil }))
	}()
	defer func() {
		cancel()
		stop()
		<-done
		for _, future := range futures {
			<-future // callbacks honor cancellation; close the broker after they finish
		}
	}()
	if e = waitAdmissionReady(ctx, s); e != nil {
		return e
	}
	payload, _ := json.Marshal(firstRequest())
	a, e := s.Submit(ctx, admission.Job{ID: "first", Lane: admission.LaneRevision, Payload: payload})
	if e != nil {
		return e
	}
	futures = append(futures, a)
	select {
	case <-entered:
	case <-ctx.Done():
		return ctx.Err()
	}
	b, e := s.Submit(ctx, admission.Job{ID: "second", Lane: admission.LaneRevision, Payload: payload})
	if e != nil {
		return e
	}
	futures = append(futures, b)
	if _, e = s.Submit(ctx, admission.Job{ID: "overflow", Lane: admission.LaneRevision, Payload: payload}); !errors.Is(e, admission.ErrQueueFull) {
		return errors.New("dynamic queue cap failed")
	}
	fmt.Fprintln(out, "PASS bounded admission rejects overflow without executing it")
	pressure.Store(true)
	s.Update(sample())
	if s.Status().ExpensiveWorkerLimit != 0 || s.Status().Active != 1 || len(s.Pending()) != 1 {
		return errors.New("pressure killed or discarded work")
	}
	if _, e = s.Submit(ctx, admission.Job{ID: "paused", Lane: admission.LaneRevision, Payload: payload}); !errors.Is(e, admission.ErrPaused) {
		return errors.New("pressure admitted expensive work")
	}
	health, e := s.Submit(ctx, admission.Job{ID: "health", Lane: admission.LaneBroker})
	if e != nil {
		return e
	}
	futures = append(futures, health)
	if e = awaitAdmission(ctx, health); e != nil {
		return e
	}
	fmt.Fprintln(out, "PASS pressure retains work while reserved broker health remains usable")
	close(release)
	if e = awaitAdmission(ctx, a); e != nil {
		return e
	}
	if first.Reused || first.Text != `{"status":"ready","model_calls":0}` {
		return errors.New("first publication result mismatch")
	}
	fmt.Fprintln(out, "PASS queued checked revision publishes durably without generation")
	select {
	case <-b:
		return errors.New("queued work ran during pressure")
	default:
	}
	pressure.Store(false)
	if e = awaitAdmission(ctx, b); e != nil {
		return e
	}
	if !second.Reused || second.Artifact == nil || first.Artifact == nil || *second.Artifact != *first.Artifact {
		return errors.New("recovery lost exact result reuse")
	}
	fmt.Fprintln(out, "PASS recovery resumes the same queued task and exact immutable reuse")
	fmt.Fprintln(out, "QUEST CLEARED — reservations govern admission only; fixture capacity is not a machine benchmark")
	return nil
}
func awaitAdmission(ctx context.Context, ch <-chan admission.Outcome) error {
	select {
	case o := <-ch:
		return o.Err
	case <-ctx.Done():
		return ctx.Err()
	}
}
