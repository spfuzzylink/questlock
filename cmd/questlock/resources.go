package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/internal/localruntime"
	"github.com/spfuzzylink/questlock/revision"
	"github.com/spfuzzylink/questlock/worker"
	"golang.org/x/sys/unix"
)

func resourcesCommand(args []string) error {
	return resourcesCommandIO(args, os.Stdout, os.Stderr, admission.SystemSource())
}
func resourcesCommandIO(args []string, out, diagnostics io.Writer, source admission.Source) error {
	if len(args) == 1 && args[0] == "config" {
		return writeModelJSON(out, admission.DefaultConfig())
	}
	if len(args) == 1 && args[0] == "quest" {
		return admissionQuest(out)
	}
	if len(args) == 0 || args[0] != "inspect" {
		return errors.New("use resources config, inspect [--config PATH], or quest")
	}
	f := flag.NewFlagSet("resources inspect", flag.ContinueOnError)
	f.SetOutput(diagnostics)
	path := f.String("config", "", "strict complete runtime policy JSON, resources config prints defaults")
	if e := f.Parse(args[1:]); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if f.NArg() != 0 {
		return errors.New("resources inspect accepts flags only")
	}
	c, e := loadAdmissionConfig(*path)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.ProbeTimeout)
	defer cancel()
	snapshot, probeErr := source.Sample(ctx)
	if probeErr != nil {
		snapshot.Known = false
	}
	s, e := admission.New(c, func(context.Context, admission.Job) error { return nil })
	if e != nil {
		return e
	}
	s.Update(snapshot)
	if e = writeModelJSON(out, map[string]any{"configuration": c, "admission": s.Status(), "read_only": true, "process_local_capacity_only": true}); e != nil {
		return e
	}
	return probeErr
}

func loadAdmissionConfig(path string) (admission.Config, error) {
	if path == "" {
		return admission.DefaultConfig(), nil
	}
	// Open atomically without following a final symlink or waiting for a FIFO
	// writer. This runs before CLI task/probe contexts exist.
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return admission.Config{}, errors.New("cannot read runtime admission policy")
	}
	f := os.NewFile(uintptr(fd), "admission_policy")
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > 64<<10 {
		return admission.Config{}, admission.ErrConfiguration
	}
	return admission.LoadConfig(f)
}

type approvedQueuePaths []string

func (p *approvedQueuePaths) String() string { return "explicit approved request paths" }
func (p *approvedQueuePaths) Set(v string) error {
	if len(*p) >= 4096 {
		return admission.ErrQueueFull
	}
	*p = append(*p, v)
	return nil
}

func queueCommand(args []string) error {
	return queueCommandIO(args, os.Stdout, os.Stderr, admission.SystemSource(), localruntime.Ensure)
}

func queueCommandIO(args []string, out, diagnostics io.Writer, source admission.Source, ensure func(context.Context, localruntime.Options) (localruntime.Handle, error)) error {
	if len(args) == 0 || args[0] != "revise" {
		return errors.New("use queue revise --workspace PATH --request FILE [--request FILE] [--config PATH]")
	}
	_, defaultState, _, self, e := onboardingDefaults()
	if e != nil {
		return e
	}
	f := flag.NewFlagSet("queue revise", flag.ContinueOnError)
	f.SetOutput(diagnostics)
	var paths approvedQueuePaths
	f.Var(&paths, "request", "explicit approved revision file; repeat within queue limits")
	workspace := f.String("workspace", "", "approved request boundary")
	state := f.String("state-dir", defaultState, "managed local broker state")
	configPath := f.String("config", "", "strict runtime admission policy JSON")
	timeout := f.Duration("timeout", 2*time.Minute, "whole queue command deadline, at most 30m")
	if e := f.Parse(args[1:]); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if f.NArg() != 0 || len(paths) == 0 || *workspace == "" || *timeout <= 0 || *timeout > 30*time.Minute {
		return errors.New("queue revise requires workspace and request flags, with a positive deadline of at most 30m")
	}
	c, e := loadAdmissionConfig(*configPath)
	if e != nil {
		return e
	}
	if len(paths) > c.MaxQueueRows-c.ProtectedQueueRows {
		return admission.ErrQueueFull
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	var once sync.Once
	var handle localruntime.Handle
	var startErr error
	var outputMu sync.Mutex
	s, e := admission.New(c, func(taskCtx context.Context, job admission.Job) error {
		request, e := revision.Load(bytes.NewReader(job.Payload))
		if e != nil {
			return e
		}
		once.Do(func() {
			handle, startErr = ensure(taskCtx, localruntime.Options{StateDir: *state, Executable: self, Version: version})
		})
		if startErr != nil {
			return startErr
		}
		result, runErr := (revision.Runner{Broker: handle.Client}).Run(taskCtx, request)
		outputMu.Lock()
		writeErr := writeModelJSON(out, map[string]any{"job_id": job.ID, "result": result})
		outputMu.Unlock()
		if writeErr != nil {
			return writeErr
		}
		if runErr != nil {
			return runErr
		}
		if result.Status != "completed" || !result.Durable || result.OutcomeTracking != nil && result.OutcomeTracking.Status != "recorded" {
			return errors.New("queued revision outcome requires inspection; no retry was attempted")
		}
		return nil
	})
	if e != nil {
		return e
	}
	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	runDone := make(chan error, 1)
	go func() { runDone <- s.Run(runCtx, source) }()
	defer func() { stopRun(); <-runDone }()
	if e = waitAdmissionReady(ctx, s); e != nil {
		_ = writeModelJSON(out, map[string]any{"status": "not_admitted", "admission": s.Status(), "model_calls": 0})
		return e
	}
	// Validate the entire explicit batch before submitting any task. The copied
	// batch itself is bounded by the current dynamic and absolute queue budget.
	status := s.Status()
	if len(paths) > status.DynamicQueueRows {
		return admission.ErrQueueFull
	}
	jobs := make([]admission.Job, 0, len(paths))
	var batchBytes int64
	for i, path := range paths {
		payload, e := worker.ReadApprovedRequest(*workspace, path)
		if e != nil {
			return e
		}
		if _, e = revision.Load(bytes.NewReader(payload)); e != nil {
			return e
		}
		id := fmt.Sprintf("request-%d", i+1)
		batchBytes += int64(len(payload)+len(id)+len(admission.LaneRevision)) + admission.QueueEntryBytes
		if batchBytes > status.DynamicQueueBytes {
			return admission.ErrQueueFull
		}
		jobs = append(jobs, admission.Job{ID: id, Lane: admission.LaneRevision, Payload: payload})
	}
	futures := make([]<-chan admission.Outcome, 0, len(jobs))
	var admissionErr error
	for i, job := range jobs {
		future, e := s.Submit(ctx, job)
		if e != nil {
			admissionErr = e
			break
		}
		futures = append(futures, future)
		jobs[i].Payload = nil // the controller owns its bounded copy now
	}
	failed := 0
	outcomes := make([]admission.Outcome, 0, len(futures))
	for _, future := range futures {
		outcome := <-future
		outcomes = append(outcomes, outcome)
		if outcome.Err != nil {
			failed++
		}
	}
	if e = writeModelJSON(out, map[string]any{"status": "finished", "admitted": len(futures), "not_admitted": len(jobs) - len(futures), "failed": failed, "model_calls": 0, "outcomes": outcomes, "admission": s.Status()}); e != nil {
		return e
	}
	if admissionErr != nil {
		return admissionErr
	}
	if failed > 0 {
		return errors.New("queued revision failed; inspect recorded result before any explicit retry")
	}
	return nil
}

func waitAdmissionReady(ctx context.Context, s *admission.Scheduler) error {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		status := s.Status()
		if status.ExpensiveWorkerLimit > 0 {
			return nil
		}
		if !status.Snapshot.At.IsZero() && status.Reason != "recovering" {
			return admission.ErrPaused
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
