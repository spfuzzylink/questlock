package delegation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/protocol"
)

var ErrResourceAdmissionConfig = errors.New("controlled_reuse_resource_policy_invalid")

// ReuseAdmissionPolicy is a host/operator policy, never answer-validity evidence
// or durable task identity. Its queue and concurrency bound one reuser process;
// multiple hosts/reusers do not share resource reservations. Nil on the reuser
// retains the legacy optional-reuse skip. An enabled policy defers the whole
// fresh task before its claim instead of falling through to fresh generation.
//
// Zero values select documented defaults except MaxQueued and MaxWaitMS, where
// zero means no waiting queue and one immediate resource sample. Nonzero limits
// are configurable for the deployment. Admitted work keeps its caller deadline;
// this policy never cancels it because a later sample reports pressure.
type ReuseAdmissionPolicy struct {
	MinAvailableMemoryBytes    int64 `json:"min_available_memory_bytes"`
	MaxConcurrent              int   `json:"max_concurrent"`
	MaxQueued                  int   `json:"max_queued"`
	MaxWaitMS                  int   `json:"max_wait_ms"`
	PollIntervalMS             int   `json:"poll_interval_ms"`
	ProbeTimeoutMS             int   `json:"probe_timeout_ms"`
	MaxSampleAgeMS             int   `json:"max_sample_age_ms"`
	MaxLoadMilliPerCapacity    int64 `json:"max_load_milli_per_capacity"`
	MaxCPUStallMilliPercent    int64 `json:"max_cpu_stall_milli_percent"`
	MaxMemoryStallMilliPercent int64 `json:"max_memory_stall_milli_percent"`
	// High pressure is opt-in; critical, unknown and invalid signals always
	// defer new admission. This is not an OOM-safety guarantee.
	AllowHighMemoryPressure bool `json:"allow_high_memory_pressure"`
}

// LoadReuseAdmissionPolicy strictly reads one bounded operator configuration.
// It rejects unknown/duplicate fields and trailing data, then applies defaults.
func LoadReuseAdmissionPolicy(r io.Reader) (ReuseAdmissionPolicy, error) {
	data, err := io.ReadAll(io.LimitReader(r, (16<<10)+1))
	if err != nil || len(data) > 16<<10 {
		return ReuseAdmissionPolicy{}, ErrResourceAdmissionConfig
	}
	var p ReuseAdmissionPolicy
	if protocol.DecodeSyncJSON(data, &p) != nil {
		return ReuseAdmissionPolicy{}, ErrResourceAdmissionConfig
	}
	var fields, allowed map[string]json.RawMessage
	shape, _ := json.Marshal(ReuseAdmissionPolicy{})
	_ = json.Unmarshal(shape, &allowed)
	_ = json.Unmarshal(data, &fields)
	for key, value := range fields {
		if _, ok := allowed[key]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ReuseAdmissionPolicy{}, ErrResourceAdmissionConfig
		}
	}
	return NormalizeReuseAdmissionPolicy(p)
}

// NewResourceAdmission builds a reusable, process-local admission callback for
// Runner.RunWithAdmission or FactorizedVerifier.Admission. It uses the same
// bounded queue/wait policy as ControlledReuser, including exact-hit bypass in
// Runner. A nil source uses the native system probe. Apply one admission layer
// to a call path; acquiring the same non-reentrant lease again inside it can
// exhaust its configured concurrency. No policy data enters task identity.
func NewResourceAdmission(p ReuseAdmissionPolicy, source admission.Source) (inference.AdmissionFunc, error) {
	p, err := NormalizeReuseAdmissionPolicy(p)
	if err != nil {
		return nil, err
	}
	owner := &ControlledReuser{Resources: source}
	return func(ctx context.Context, spec inference.Spec) (func(), error) {
		resolver := &controlledResolver{owner: owner, admissionPolicy: &p, observation: &ReuseObservation{}}
		return resolver.admit(ctx, spec)
	}, nil
}

func NormalizeReuseAdmissionPolicy(p ReuseAdmissionPolicy) (ReuseAdmissionPolicy, error) {
	if p.MinAvailableMemoryBytes == 0 {
		p.MinAvailableMemoryBytes = 256 * admission.MiB
	}
	if p.MaxConcurrent == 0 {
		p.MaxConcurrent = 1
	}
	if p.PollIntervalMS == 0 {
		p.PollIntervalMS = 250
	}
	if p.ProbeTimeoutMS == 0 {
		p.ProbeTimeoutMS = 3000
	}
	if p.MaxSampleAgeMS == 0 {
		p.MaxSampleAgeMS = 5000
	}
	if p.MaxLoadMilliPerCapacity == 0 {
		p.MaxLoadMilliPerCapacity = 2000
	}
	if p.MaxCPUStallMilliPercent == 0 {
		p.MaxCPUStallMilliPercent = 20000
	}
	if p.MaxMemoryStallMilliPercent == 0 {
		p.MaxMemoryStallMilliPercent = 1000
	}
	if p.MinAvailableMemoryBytes < 1 || p.MaxConcurrent < 1 || p.MaxConcurrent > 1024 || p.MaxQueued < 0 || p.MaxQueued > 4096 || p.MaxWaitMS < 0 || p.MaxWaitMS > 24*60*60*1000 || p.PollIntervalMS < 10 || p.PollIntervalMS > 60000 || p.ProbeTimeoutMS < 1 || p.ProbeTimeoutMS > 30000 || p.MaxSampleAgeMS < 1 || p.MaxSampleAgeMS > 10*60*1000 || p.MaxLoadMilliPerCapacity < 1 || p.MaxLoadMilliPerCapacity > 1000000 || p.MaxCPUStallMilliPercent < 1 || p.MaxCPUStallMilliPercent > 100000 || p.MaxMemoryStallMilliPercent < 1 || p.MaxMemoryStallMilliPercent > 100000 {
		return ReuseAdmissionPolicy{}, ErrResourceAdmissionConfig
	}
	return p, nil
}

type admissionReuseRunner interface {
	RunWithResolverAdmission(context.Context, inference.Spec, inference.ReuseResolver, inference.AdmissionFunc) (inference.Result, error)
}

func (r *controlledResolver) run(ctx context.Context, s inference.Spec) (inference.Result, error) {
	if r.admissionPolicy != nil {
		return r.runner.(admissionReuseRunner).RunWithResolverAdmission(ctx, s, r, r.admit)
	}
	return r.runner.RunWithResolver(ctx, s, r)
}

func (r *controlledResolver) admit(ctx context.Context, _ inference.Spec) (release func(), runErr error) {
	p, err := NormalizeReuseAdmissionPolicy(*r.admissionPolicy)
	if err != nil {
		return nil, err
	}
	o := r.owner
	o.admissionOnce.Do(func() {
		o.admissionConfig = p
		o.admissionSlots = make(chan struct{}, p.MaxConcurrent)
		o.admissionQueue = make(chan struct{}, p.MaxQueued)
	})
	if o.admissionConfig != p {
		return nil, ErrResourceAdmissionConfig
	}
	started := time.Now()
	diagnostic := &ReuseResourceAdmission{Reason: "busy", Outcome: "deferred"}
	r.observation.ResourceAdmission = diagnostic
	defer func() {
		diagnostic.WaitMS = float64(time.Since(started)) / float64(time.Millisecond)
		if runErr != nil {
			r.observation.Status = "resource_deferred"
		}
	}()
	wait := ctx
	stop := func() {}
	if p.MaxWaitMS > 0 {
		wait, stop = context.WithTimeout(ctx, time.Duration(p.MaxWaitMS)*time.Millisecond)
	}
	defer stop()
	deferred := func(reason string) error {
		diagnostic.DeferredReason = reason
		if ctx.Err() != nil {
			return errors.Join(inference.ErrAdmissionDeferred, ctx.Err())
		}
		return inference.ErrAdmissionDeferred
	}
	select {
	case o.admissionSlots <- struct{}{}:
	default:
		if p.MaxWaitMS == 0 {
			return nil, deferred("busy")
		}
		select {
		case o.admissionQueue <- struct{}{}:
		default:
			return nil, deferred("queue_full")
		}
		queued := time.Now()
		select {
		case o.admissionSlots <- struct{}{}:
			<-o.admissionQueue
			diagnostic.QueueWaitMS = float64(time.Since(queued)) / float64(time.Millisecond)
		case <-wait.Done():
			<-o.admissionQueue
			diagnostic.QueueWaitMS = float64(time.Since(queued)) / float64(time.Millisecond)
			return nil, deferred("queue_wait_expired")
		}
	}
	release = func() { <-o.admissionSlots }
	source := o.Resources
	if source == nil {
		source = admission.SystemSource()
	}
	for {
		if wait.Err() != nil {
			return release, deferred("admission_wait_expired")
		}
		probe, cancel := context.WithTimeout(wait, time.Duration(p.ProbeTimeoutMS)*time.Millisecond)
		snapshot, sampleErr := source.Sample(probe)
		cancel()
		observed := resourcePolicyAdmission(snapshot, sampleErr, time.Now(), p)
		diagnostic.Reason, diagnostic.MemoryPressure, diagnostic.AvailableMemoryBytes = observed.Reason, observed.MemoryPressure, observed.AvailableMemoryBytes
		diagnostic.Samples++
		if wait.Err() != nil {
			return release, deferred("admission_wait_expired")
		}
		if diagnostic.Reason == "ready" {
			diagnostic.Outcome = "admitted"
			r.admitted = true
			return release, nil
		}
		if p.MaxWaitMS == 0 {
			return release, deferred("resource_not_ready")
		}
		timer := time.NewTimer(time.Duration(p.PollIntervalMS) * time.Millisecond)
		select {
		case <-timer.C:
		case <-wait.Done():
			timer.Stop()
			return release, deferred("admission_wait_expired")
		}
	}
}

func resourcePolicyAdmission(s admission.Snapshot, err error, now time.Time, p ReuseAdmissionPolicy) *ReuseResourceAdmission {
	d := &ReuseResourceAdmission{Reason: "sample_unavailable"}
	if err != nil || !s.Known {
		return d
	}
	d.Reason = "sample_invalid"
	if s.At.IsZero() || s.At.After(now) || s.AvailableMemoryBytes < 0 || s.CPUStallMilliPercent < 0 || s.CPUStallMilliPercent > 100000 || s.MemoryStallMilliPercent < 0 || s.MemoryStallMilliPercent > 100000 || s.LoadCapacityMilli <= 0 || s.LoadCapacityMilli > 1e9 || s.LoadMilli < 0 || s.LoadMilli > 1e12 || (s.MemoryPressure != admission.Normal && s.MemoryPressure != admission.High && s.MemoryPressure != admission.Critical) {
		return d
	}
	available := s.AvailableMemoryBytes
	d.AvailableMemoryBytes, d.MemoryPressure = &available, s.MemoryPressure
	switch {
	case now.Sub(s.At) > time.Duration(p.MaxSampleAgeMS)*time.Millisecond:
		d.Reason = "sample_expired"
	case s.MemoryPressure == admission.Critical || s.MemoryPressure == admission.High && !p.AllowHighMemoryPressure:
		d.Reason = "memory_pressure"
	case s.AvailableMemoryBytes < p.MinAvailableMemoryBytes:
		d.Reason = "insufficient_memory"
	case s.CPUStallMilliPercent >= p.MaxCPUStallMilliPercent:
		d.Reason = "cpu_stall"
	case s.MemoryStallMilliPercent >= p.MaxMemoryStallMilliPercent:
		d.Reason = "memory_stall"
	case s.LoadMilli*1000 > p.MaxLoadMilliPerCapacity*s.LoadCapacityMilli:
		d.Reason = "load"
	default:
		d.Reason = "ready"
	}
	return d
}
