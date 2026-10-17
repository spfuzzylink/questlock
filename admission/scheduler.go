package admission

import (
	"context"
	"errors"
	"sync"
	"time"
	"unicode/utf8"
)

type entry struct {
	job   Job
	lane  Lane
	ctx   context.Context
	done  chan Outcome
	bytes int64
	stop  func() bool
}

// Scheduler owns one process-local queue. Construct one shared instance for
// broker, replication and inference lanes. Separate instances/processes do not
// coordinate capacity. No completed-job history or automatic retry is retained.
type Scheduler struct {
	mu                          sync.Mutex
	config                      Config
	lanes                       map[string]Lane
	execute                     Executor
	now                         func() time.Time
	queue                       []*entry
	ids                         map[string]*entry
	active                      map[string]int
	activeCPU, activeMemory     int64
	queuedBytes, protectedBytes int64
	protectedRows               int
	protectedWorkers            int
	limit, healthy              int
	lastRamp                    time.Time
	snapshot                    Snapshot
	running                     bool
	notify                      chan struct{}
}

func New(c Config, execute Executor) (*Scheduler, error) {
	if c.Validate() != nil || execute == nil {
		return nil, ErrConfiguration
	}
	c.Lanes = append([]Lane(nil), c.Lanes...)
	s := &Scheduler{config: c, execute: execute, lanes: map[string]Lane{}, ids: map[string]*entry{}, active: map[string]int{}, now: time.Now, notify: make(chan struct{}, 1)}
	for _, l := range c.Lanes {
		s.lanes[l.Name] = l
		if l.Protected {
			s.protectedWorkers += l.MaxWorkers
		}
	}
	return s, nil
}

func (s *Scheduler) signal() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// Update applies fresh measurements. Pressure shrinks immediately; recovery
// needs consecutive low-pressure samples and increases at most one worker per
// ramp interval. MinWorkers is only a goal: safety can pause all expensive work.
func (s *Scheduler) Update(snapshot Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	previous := s.snapshot.At
	if previous.IsZero() || now.Sub(previous) > s.config.MaxSampleAge {
		s.limit, s.healthy = 0, 0
	}
	if !previous.IsZero() && snapshot.At.Before(previous) {
		snapshot.Known = false
	}
	s.snapshot = snapshot
	capacity, reason, healthy, _, _ := s.budgetLocked(now)
	if reason != "ready" {
		s.limit = 0
		s.healthy = 0
		s.signal()
		return
	}
	if capacity < s.limit {
		s.limit = capacity
		s.healthy = 0
	}
	if !healthy {
		s.healthy = 0
		s.signal()
		return
	}
	// Replaying the same observation cannot manufacture recovery samples.
	if !previous.IsZero() && !snapshot.At.After(previous) {
		s.signal()
		return
	}
	if s.healthy < s.config.RecoverySamples {
		s.healthy++
	}
	if s.healthy >= s.config.RecoverySamples && s.limit < capacity && (s.lastRamp.IsZero() || now.Sub(s.lastRamp) >= s.config.RampInterval) {
		s.limit++ // even the minimum goal cannot bypass the gradual safety ramp
		s.lastRamp = now
	}
	s.signal()
}

func (s *Scheduler) budgetLocked(now time.Time) (capacity int, reason string, healthy bool, cpu, mem int64) {
	x, c := s.snapshot, s.config
	if !x.Known || x.At.IsZero() || x.At.After(now) || now.Sub(x.At) > c.MaxSampleAge || x.CPUCapacityMilli < 1 || x.CPUCapacityMilli > 1e9 || x.LoadCapacityMilli < 1 || x.LoadCapacityMilli > 1e9 || x.LoadMilli < 0 || x.LoadMilli > 1e12 || x.AvailableMemoryBytes < 0 || x.CPUStallMilliPercent < 0 || x.CPUStallMilliPercent > 100000 || x.MemoryStallMilliPercent < 0 || x.MemoryStallMilliPercent > 100000 || (x.MemoryPressure != Normal && x.MemoryPressure != High && x.MemoryPressure != Critical) {
		return 0, "signals_unavailable_or_stale", false, 0, 0
	}
	load := x.LoadMilli * 1000 / x.LoadCapacityMilli
	if x.MemoryPressure != Normal || load >= c.CPULoadHighMilli || x.CPUStallMilliPercent >= c.CPUStallHighMilliPercent || x.MemoryStallMilliPercent >= c.MemoryStallHighMilliPercent {
		return 0, "resource_pressure", false, 0, 0
	}
	cpu = max(int64(0), x.CPUCapacityMilli-c.ProtectedCPUMilli)
	mem = max(int64(0), x.AvailableMemoryBytes-c.MemoryHeadroomBytes-c.ProtectedMemoryBytes)
	capacity = c.MaxWorkers - s.protectedWorkers
	minCPU, minMemory := int64(1e9), int64(1<<62)
	for _, l := range c.Lanes {
		if !l.Protected {
			minCPU = min(minCPU, l.CPUMilli)
			minMemory = min(minMemory, l.MemoryBytes)
		}
	}
	capacity = min(capacity, int(min(cpu/minCPU, int64(capacity))), int(min(s.executionMemoryBudget(mem)/minMemory, int64(capacity))))
	if capacity == 0 {
		return 0, "insufficient_headroom", false, cpu, mem
	}
	healthy = load <= c.CPULoadLowMilli && x.CPUStallMilliPercent <= c.CPUStallLowMilliPercent && x.MemoryStallMilliPercent <= c.MemoryStallLowMilliPercent
	return capacity, "ready", healthy, cpu, mem
}

func (s *Scheduler) queueMemoryBudget(mem int64) int64 {
	return min(s.config.MaxQueueBytes-s.config.ProtectedQueueBytes, (mem/100)*s.config.QueueMemoryPercent)
}

func (s *Scheduler) executionMemoryBudget(mem int64) int64 {
	// Keep the bounded queue's allowance separate from running reservations.
	// A shrink may leave accepted payloads above the new queue allowance; those
	// bytes remain charged while waiting for recovery, never silently discarded.
	return max(int64(0), mem-max(s.queueMemoryBudget(mem), s.queuedBytes-s.protectedBytes))
}

func (s *Scheduler) statusLocked(now time.Time) Status {
	capacity, reason, _, _, mem := s.budgetLocked(now)
	limit := min(s.limit, capacity)
	if reason == "ready" && limit == 0 {
		reason = "recovering"
	}
	rows := min(s.config.MaxQueueRows-s.config.ProtectedQueueRows, limit*s.config.QueueRowsPerWorker)
	// Queued payload and active execution estimates share the same headroom.
	// OS readings may already include some active allocation; subtracting the
	// estimate again is deliberately conservative, never an allocation promise.
	bytes := min(s.queueMemoryBudget(mem), max(int64(0), mem-s.activeMemory))
	if limit == 0 {
		rows = 0
		bytes = 0
	}
	a := map[string]int{}
	total := 0
	for k, v := range s.active {
		a[k] = v
		total += v
	}
	return Status{Snapshot: s.snapshot, Reason: reason, ExpensiveWorkerLimit: limit, PotentialWorkers: capacity, Active: total, ActiveByLane: a, QueuedRows: len(s.queue), QueuedBytes: s.queuedBytes, DynamicQueueRows: rows, DynamicQueueBytes: bytes, CPUReservedMilli: s.activeCPU, MemoryReservedBytes: s.activeMemory}
}
func (s *Scheduler) Status() Status { s.mu.Lock(); defer s.mu.Unlock(); return s.statusLocked(s.now()) }

// Submit copies bounded job data. Rejection never calls the executor. Payloads
// should contain small operation IDs, not credentials or duplicate durable bodies.
// A result channel contains exactly one outcome. Context cancellation of queued
// work is explicit cancellation; pressure never removes queued work.
func (s *Scheduler) Submit(ctx context.Context, j Job) (<-chan Outcome, error) {
	if ctx == nil {
		return nil, ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if j.ID == "" || len(j.ID) > 128 || !utf8.ValidString(j.ID) || len(j.Payload) > 1<<20 {
		return nil, ErrConfiguration
	}
	for _, r := range j.ID {
		if r < 32 || r == 127 {
			return nil, ErrConfiguration
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.lanes[j.Lane]
	if !ok {
		return nil, ErrConfiguration
	}
	if _, ok := s.ids[j.ID]; ok {
		return nil, ErrDuplicate
	}
	n := QueueEntryBytes + int64(len(j.ID)+len(j.Lane)+len(j.Payload))
	if n > l.MemoryBytes {
		return nil, ErrDoesNotFit
	}
	status := s.statusLocked(s.now())
	if len(s.queue) >= s.config.MaxQueueRows || n > s.config.MaxQueueBytes-s.queuedBytes {
		return nil, ErrQueueFull
	}
	if l.Protected {
		if s.protectedRows >= s.config.ProtectedQueueRows || n > s.config.ProtectedQueueBytes-s.protectedBytes {
			return nil, ErrQueueFull
		}
	} else {
		if status.ExpensiveWorkerLimit == 0 {
			return nil, ErrPaused
		}
		_, _, _, cpu, mem := s.budgetLocked(s.now())
		if l.CPUMilli > cpu || l.MemoryBytes > s.executionMemoryBudget(mem) {
			return nil, ErrDoesNotFit
		}
		if len(s.queue)-s.protectedRows >= status.DynamicQueueRows || n > status.DynamicQueueBytes-(s.queuedBytes-s.protectedBytes) {
			return nil, ErrQueueFull
		}
	}
	j.Payload = append([]byte(nil), j.Payload...)
	e := &entry{job: j, lane: l, ctx: ctx, bytes: n, done: make(chan Outcome, 1)}
	s.queue = append(s.queue, e)
	s.ids[j.ID] = e
	s.queuedBytes += n
	if l.Protected {
		s.protectedRows++
		s.protectedBytes += n
	}
	e.stop = context.AfterFunc(ctx, func() { s.cancelQueued(e) })
	s.signal()
	return e.done, nil
}

func (s *Scheduler) removeLocked(index int) *entry {
	e := s.queue[index]
	copy(s.queue[index:], s.queue[index+1:])
	s.queue[len(s.queue)-1] = nil
	s.queue = s.queue[:len(s.queue)-1]
	s.queuedBytes -= e.bytes
	if e.lane.Protected {
		s.protectedRows--
		s.protectedBytes -= e.bytes
	}
	return e
}
func (s *Scheduler) cancelQueued(cancelled *entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.queue {
		if e == cancelled {
			s.removeLocked(i)
			delete(s.ids, e.job.ID)
			e.done <- Outcome{ID: e.job.ID, Lane: e.job.Lane, Status: "cancelled", Err: e.ctx.Err()}
			close(e.done)
			s.signal()
			return
		}
	}
}

// Pending returns copied payloads for explicit inspection/recovery. This queue
// is in memory; durable edge intents remain owned by their store. Run cancellation
// stops dispatch, retaining pending entries. It does not cancel running callbacks.
func (s *Scheduler) Pending() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Job, 0, len(s.queue))
	for _, e := range s.queue {
		j := e.job
		j.Payload = append([]byte(nil), j.Payload...)
		out = append(out, j)
	}
	return out
}

func (s *Scheduler) dispatch(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ctx.Err() == nil {
		status := s.statusLocked(s.now())
		_, _, _, cpu, mem := s.budgetLocked(s.now())
		index := -1
		if status.Active >= s.config.MaxWorkers {
			return
		}
		// Protected cheap operations have reserved headroom and queue slots.
		for pass := 0; pass < 2 && index < 0; pass++ {
			for i, e := range s.queue {
				if e.lane.Protected != (pass == 0) || s.active[e.job.Lane] >= e.lane.MaxWorkers {
					continue
				}
				if !e.lane.Protected {
					expensive := 0
					for name, count := range s.active {
						if !s.lanes[name].Protected {
							expensive += count
						}
					}
					if expensive >= status.ExpensiveWorkerLimit || e.lane.CPUMilli > cpu-s.activeCPU || e.lane.MemoryBytes > s.executionMemoryBudget(mem)-s.activeMemory {
						continue
					}
				}
				index = i
				break
			}
		}
		if index < 0 {
			return
		}
		e := s.removeLocked(index)
		e.stop()
		s.active[e.job.Lane]++
		if !e.lane.Protected {
			s.activeCPU += e.lane.CPUMilli
			s.activeMemory += e.lane.MemoryBytes
		}
		go s.executeOne(e)
	}
}

func (s *Scheduler) executeOne(e *entry) {
	ctx, cancel := context.WithTimeout(e.ctx, s.config.TaskTimeout)
	defer cancel()
	err := func() (err error) {
		defer func() {
			if recover() != nil {
				err = ErrExecutorPanic
			}
		}()
		if err = ctx.Err(); err != nil {
			return err
		}
		return s.execute(ctx, e.job)
	}()
	status := "completed"
	if err != nil {
		status = "failed"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = "cancelled"
		}
	}
	s.mu.Lock()
	s.active[e.job.Lane]--
	if !e.lane.Protected {
		s.activeCPU -= e.lane.CPUMilli
		s.activeMemory -= e.lane.MemoryBytes
	}
	delete(s.ids, e.job.ID)
	s.mu.Unlock()
	e.done <- Outcome{ID: e.job.ID, Lane: e.job.Lane, Status: status, Err: err}
	close(e.done)
	s.signal()
}

// Run samples resources and dispatches work. Source and Executor must honor
// their contexts. A callback ignoring cancellation remains counted until it
// returns; it never creates a replacement slot or an automatic retry.
func (s *Scheduler) Run(ctx context.Context, source Source) error {
	if ctx == nil || source == nil {
		return ErrConfiguration
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return ErrRunning
	}
	s.running = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.running = false; s.mu.Unlock() }()
	sample := func() {
		probe, cancel := context.WithTimeout(ctx, s.config.ProbeTimeout)
		defer cancel()
		snapshot, err := source.Sample(probe)
		if err != nil {
			snapshot.Known = false
		}
		s.Update(snapshot)
	}
	ticker := time.NewTicker(s.config.SampleInterval)
	defer ticker.Stop()
	sample()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.dispatch(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			sample()
		case <-s.notify:
		}
	}
}
