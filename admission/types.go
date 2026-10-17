// Package admission provides opt-in, single-process resource admission. It
// queues bounded payloads and invokes a caller-owned Go executor exactly once.
// Reservations are estimates: this package neither allocates OS resources nor
// changes model requests, kills work under pressure, retries, or selects providers.
package admission

import (
	"context"
	"errors"
	"time"
)

const (
	LaneBroker            = "broker"
	LaneReplication       = "replication"
	LaneInference         = "inference"
	LaneRevision          = "revision"
	MiB             int64 = 1 << 20
	// QueueEntryBytes charges metadata in addition to copied ID/lane/payload.
	// It cannot account for memory retained by the caller's context or executor.
	QueueEntryBytes int64 = 512
)

var (
	ErrConfiguration = errors.New("admission_configuration_invalid")
	ErrPaused        = errors.New("admission_paused")
	ErrQueueFull     = errors.New("admission_queue_full")
	ErrDoesNotFit    = errors.New("admission_reservation_does_not_fit")
	ErrDuplicate     = errors.New("admission_job_id_already_present")
	ErrRunning       = errors.New("admission_controller_already_running")
	ErrExecutorPanic = errors.New("admission_executor_panicked_outcome_requires_inspection")
	ErrSignals       = errors.New("admission_resource_signals_unavailable")
)

type Lane struct {
	Name        string `json:"name"`
	CPUMilli    int64  `json:"cpu_milli"`
	MemoryBytes int64  `json:"memory_bytes"`
	MaxWorkers  int    `json:"max_workers"`
	// Protected is for cheap persistence/health, never for model generation.
	// Its bounded static allowance remains usable when expensive work pauses.
	Protected bool `json:"protected"`
}

// Durations encode as nanoseconds in JSON. LoadConfig supports strict complete
// configurations, while Go callers can modify DefaultConfig before New.
type Config struct {
	MinWorkers                  int           `json:"min_workers"`
	MaxWorkers                  int           `json:"max_workers"`
	MaxQueueRows                int           `json:"max_queue_rows"`
	MaxQueueBytes               int64         `json:"max_queue_bytes"`
	ProtectedQueueRows          int           `json:"protected_queue_rows"`
	ProtectedQueueBytes         int64         `json:"protected_queue_bytes"`
	ProtectedCPUMilli           int64         `json:"protected_cpu_milli"`
	ProtectedMemoryBytes        int64         `json:"protected_memory_bytes"`
	MemoryHeadroomBytes         int64         `json:"memory_headroom_bytes"`
	QueueMemoryPercent          int64         `json:"queue_memory_percent"`
	QueueRowsPerWorker          int           `json:"queue_rows_per_worker"`
	RecoverySamples             int           `json:"recovery_samples"`
	RampInterval                time.Duration `json:"ramp_interval"`
	SampleInterval              time.Duration `json:"sample_interval"`
	MaxSampleAge                time.Duration `json:"max_sample_age"`
	ProbeTimeout                time.Duration `json:"probe_timeout"`
	TaskTimeout                 time.Duration `json:"task_timeout"`
	CPULoadHighMilli            int64         `json:"cpu_load_high_milli"`
	CPULoadLowMilli             int64         `json:"cpu_load_low_milli"`
	CPUStallHighMilliPercent    int64         `json:"cpu_stall_high_milli_percent"`
	CPUStallLowMilliPercent     int64         `json:"cpu_stall_low_milli_percent"`
	MemoryStallHighMilliPercent int64         `json:"memory_stall_high_milli_percent"`
	MemoryStallLowMilliPercent  int64         `json:"memory_stall_low_milli_percent"`
	Lanes                       []Lane        `json:"lanes"`
}

func DefaultConfig() Config {
	return Config{MinWorkers: 1, MaxWorkers: 5, MaxQueueRows: 64, MaxQueueBytes: 8 * MiB,
		ProtectedQueueRows: 4, ProtectedQueueBytes: 64 << 10, ProtectedCPUMilli: 200,
		ProtectedMemoryBytes: 64 * MiB, MemoryHeadroomBytes: 256 * MiB,
		QueueMemoryPercent: 5, QueueRowsPerWorker: 4, RecoverySamples: 3,
		RampInterval: 2 * time.Second, SampleInterval: time.Second, MaxSampleAge: 5 * time.Second,
		ProbeTimeout: 2 * time.Second, TaskTimeout: 45 * time.Second,
		CPULoadHighMilli: 1200, CPULoadLowMilli: 800,
		CPUStallHighMilliPercent: 20000, CPUStallLowMilliPercent: 10000,
		MemoryStallHighMilliPercent: 1000, MemoryStallLowMilliPercent: 500,
		Lanes: []Lane{{LaneBroker, 100, 8 * MiB, 1, true}, {LaneReplication, 250, 32 * MiB, 1, false},
			{LaneInference, 1000, 256 * MiB, 1, false}, {LaneRevision, 250, 16 * MiB, 4, false}}}
}

type Pressure string

const (
	Normal   Pressure = "normal"
	High     Pressure = "high"
	Critical Pressure = "critical"
)

// LoadMilli is the one-minute runnable/waiting system load, not CPU percent.
// It is normalized by LoadCapacityMilli; cgroup PSI separately captures stalls.
// AvailableMemoryBytes is conservative headroom, not a guaranteed allocation.
type Snapshot struct {
	At                      time.Time `json:"at"`
	Known                   bool      `json:"known"`
	Source                  string    `json:"source"`
	CPUCapacityMilli        int64     `json:"cpu_capacity_milli"`
	LoadCapacityMilli       int64     `json:"load_capacity_milli"`
	LoadMilli               int64     `json:"load_milli"`
	CPUStallMilliPercent    int64     `json:"cpu_stall_milli_percent"`
	AvailableMemoryBytes    int64     `json:"available_memory_bytes"`
	MemoryPressure          Pressure  `json:"memory_pressure"`
	MemoryStallMilliPercent int64     `json:"memory_stall_milli_percent"`
}

type Source interface {
	Sample(context.Context) (Snapshot, error)
}
type SourceFunc func(context.Context) (Snapshot, error)

func (f SourceFunc) Sample(ctx context.Context) (Snapshot, error) { return f(ctx) }

type Job struct {
	ID      string
	Lane    string
	Payload []byte
}
type Executor func(context.Context, Job) error
type Outcome struct {
	ID     string `json:"id"`
	Lane   string `json:"lane"`
	Status string `json:"status"`
	Err    error  `json:"-"`
}

type Status struct {
	Snapshot                     Snapshot       `json:"snapshot"`
	Reason                       string         `json:"reason"`
	ExpensiveWorkerLimit         int            `json:"expensive_worker_limit"`
	PotentialWorkers             int            `json:"potential_workers"`
	Active                       int            `json:"active"`
	ActiveByLane                 map[string]int `json:"active_by_lane"`
	QueuedRows                   int            `json:"queued_rows"`
	QueuedBytes                  int64          `json:"queued_bytes"`
	DynamicQueueRows             int            `json:"dynamic_queue_rows"`
	DynamicQueueBytes            int64          `json:"dynamic_queue_bytes"`
	CPUReservedMilli             int64          `json:"cpu_reserved_milli"`
	MemoryReservedBytes          int64          `json:"memory_reserved_bytes"`
	ReservationsAreOSEnforcement bool           `json:"reservations_are_os_enforcement"`
}
