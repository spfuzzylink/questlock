package execution

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/observability"
)

type call struct {
	cancel context.CancelFunc
	result Result // written by executor, read after scheduler outcome delivery
}

// Coordinator owns one bounded queue and no completed-result cache. Durable
// claims/results belong to Runner.Broker. Separate coordinators and hosts do not
// share resource reservations, even when their broker scope is the same.
type Coordinator struct {
	mu        sync.Mutex
	config    Config
	scheduler *admission.Scheduler
	lifecycle context.Context
	cancel    context.CancelFunc
	loopDone  chan struct{}
	drained   chan struct{}
	drainDone bool
	closed    bool
	nextID    uint64
	calls     map[string]*call
	slots     chan struct{}
}

// New starts the sampler under lifecycle. It performs no model call or broker
// access. Admission initially pauses until fresh recovery samples establish
// headroom; Run returns admission.ErrPaused during warm-up or pressure. It does
// not hold unbounded requests outside the queue while waiting for recovery.
func New(lifecycle context.Context, cfg Config) (*Coordinator, error) {
	if lifecycle == nil || nilInterface(cfg.Runner.Broker) || nilInterface(cfg.Runner.Provider) || cfg.Runner.Provider.Identity() == "" {
		return nil, ErrConfiguration
	}
	if err := lifecycle.Err(); err != nil {
		return nil, err
	}
	if reflect.ValueOf(cfg.Admission).IsZero() {
		cfg.Admission = admission.DefaultConfig()
	}
	if cfg.Admission.Validate() != nil {
		return nil, ErrConfiguration
	}
	found := false
	for _, lane := range cfg.Admission.Lanes {
		if lane.Name == admission.LaneInference && !lane.Protected {
			found = true
		}
	}
	if !found {
		return nil, ErrConfiguration
	}
	if cfg.Source == nil {
		cfg.Source = admission.SystemSource()
	} else if nilInterface(cfg.Source) {
		return nil, ErrConfiguration
	}
	if cfg.Exporter != nil && nilInterface(cfg.Exporter) {
		return nil, ErrConfiguration
	}
	if cfg.ExportTimeout == 0 {
		cfg.ExportTimeout = 2 * time.Second
	}
	if cfg.ExportTimeout < time.Millisecond || cfg.ExportTimeout > 5*time.Second {
		return nil, ErrConfiguration
	}
	ctx, cancel := context.WithCancel(lifecycle)
	c := &Coordinator{config: cfg, lifecycle: ctx, cancel: cancel, loopDone: make(chan struct{}),
		drained: make(chan struct{}), calls: make(map[string]*call),
		slots: make(chan struct{}, cfg.Admission.MaxQueueRows+cfg.Admission.MaxWorkers)}
	var err error
	c.scheduler, err = admission.New(cfg.Admission, c.execute)
	if err != nil {
		cancel()
		return nil, err
	}
	// Shutdown also runs if a custom source ignores cancellation and holds the
	// scheduler loop. Close still has its caller-supplied wait deadline.
	context.AfterFunc(ctx, c.shutdown)
	go func() {
		defer close(c.loopDone)
		_ = c.scheduler.Run(ctx, cfg.Source)
		c.shutdown()
	}()
	return c, nil
}

func nilInterface(v any) bool {
	if v == nil {
		return true
	}
	x := reflect.ValueOf(v)
	switch x.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return x.IsNil()
	}
	return false
}

// Run freezes the approved spec, admits the whole runner call once and awaits
// its outcome. Even result lookups use this process's inference lane. There is
// no automatic wait/retry after admission rejection or ErrInFlight. The caller
// must not mutate a spec concurrently while Run copies it.
//
// Cancellation removes queued work or cancels an active provider. Active
// capacity remains held until the executor returns; custom dependencies must
// honor contexts. The returned error and partial result preserve Runner semantics.
func (c *Coordinator) Run(ctx context.Context, spec inference.Spec) (Result, error) {
	empty := Result{Telemetry: TelemetryResult{Status: "disabled"}}
	if c.config.Exporter != nil {
		empty.Telemetry.Status = "not_attempted"
	}
	if ctx == nil {
		return empty, ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	c.mu.Lock()
	closed := c.closed || c.lifecycle.Err() != nil
	c.mu.Unlock()
	if closed {
		return empty, ErrClosed
	}
	// Bound both queued registry entries and concurrent snapshot preparation.
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	default:
		return empty, admission.ErrQueueFull
	}
	if _, err := inference.BuildPlan(spec); err != nil {
		return empty, err
	}
	encoded, err := json.Marshal(spec)
	if err != nil || len(encoded) > MaxSpecBytes {
		return empty, ErrConfiguration
	}
	var frozen inference.Spec
	if json.Unmarshal(encoded, &frozen) != nil {
		return empty, ErrConfiguration
	}
	if !c.config.AllowHosted {
		provider, ok := c.config.Runner.Provider.(inference.SelfHostedProvider)
		if !inference.SelfHosted(frozen) || !ok || !provider.SelfHosted() {
			return empty, ErrHostedDisabled
		}
	}
	taskCtx, cancel := context.WithCancel(ctx)
	stopLife := context.AfterFunc(c.lifecycle, cancel)
	defer stopLife()
	defer cancel()
	item := &call{cancel: cancel, result: empty}
	c.mu.Lock()
	if c.closed || c.lifecycle.Err() != nil {
		c.mu.Unlock()
		return empty, ErrClosed
	}
	c.nextID++
	id := "execution-" + strconv.FormatUint(c.nextID, 10)
	c.calls[id] = item
	// Insert before Submit so a fast executor always finds its reply slot.
	future, err := c.scheduler.Submit(taskCtx, admission.Job{ID: id, Lane: admission.LaneInference, Payload: encoded})
	if err != nil {
		delete(c.calls, id)
		c.maybeDrainedLocked()
		c.mu.Unlock()
		return empty, err
	}
	c.mu.Unlock()
	// Submit owns a copy. Do not retain additional prompt bodies in the waiting
	// caller's frame; the bounded registry contains only cancellation and a reply.
	encoded, frozen, spec = nil, inference.Spec{}, inference.Spec{}
	outcome := <-future
	c.mu.Lock()
	delete(c.calls, id)
	c.maybeDrainedLocked()
	c.mu.Unlock()
	return item.result, outcome.Err
}

func (c *Coordinator) execute(ctx context.Context, job admission.Job) error {
	c.mu.Lock()
	item := c.calls[job.ID]
	c.mu.Unlock()
	if item == nil {
		return ErrConfiguration
	}
	var spec inference.Spec
	if len(job.Payload) > MaxSpecBytes || json.Unmarshal(job.Payload, &spec) != nil {
		return ErrConfiguration
	}
	result, err := c.config.Runner.Run(ctx, spec)
	item.result.Result = result
	item.result.Telemetry = c.export(ctx, spec, result)
	return err
}

func (c *Coordinator) export(ctx context.Context, spec inference.Spec, result inference.Result) (out TelemetryResult) {
	if c.config.Exporter == nil {
		return TelemetryResult{Status: "disabled"}
	}
	execution, err := observability.FromInference(spec, result)
	if err != nil {
		return TelemetryResult{Status: "invalid_metadata", Error: err}
	}
	out.Status = "unconfirmed"
	// An exporter is a caller-owned dependency. Preserve the generation outcome
	// even if that optional dependency panics, and never start another model.
	defer func() {
		if recover() != nil {
			out = TelemetryResult{Status: "unconfirmed", Error: observability.ErrUnavailable}
		}
	}()
	exportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.config.ExportTimeout)
	defer cancel()
	receipt, err := c.config.Exporter.Export(exportCtx, execution)
	out.Receipt, out.Error = &receipt, err
	if err == nil {
		out.Status = "acknowledged"
	}
	return out
}

// Status reports the existing process-local admission state without task data.
func (c *Coordinator) Status() admission.Status { return c.scheduler.Status() }

func (c *Coordinator) maybeDrainedLocked() {
	if c.closed && len(c.calls) == 0 && !c.drainDone {
		close(c.drained)
		c.drainDone = true
	}
}

func (c *Coordinator) shutdown() {
	c.mu.Lock()
	c.closed = true
	cancels := make([]context.CancelFunc, 0, len(c.calls))
	for _, item := range c.calls {
		cancels = append(cancels, item.cancel)
	}
	c.maybeDrainedLocked()
	c.mu.Unlock()
	c.cancel()
	for _, cancel := range cancels {
		cancel()
	}
}

// Close stops admission, cancels queued and active calls, and waits for the
// sampler and admitted calls. A deadline returns while a noncooperating callback
// remains counted; it never opens a replacement slot. Repeated Close is safe.
func (c *Coordinator) Close(ctx context.Context) error {
	if ctx == nil {
		return ErrConfiguration
	}
	c.shutdown()
	select {
	case <-c.drained:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-c.loopDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
