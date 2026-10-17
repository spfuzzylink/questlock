# One process, one adaptive resource budget

The opt-in Go `admission` package coordinates a bounded queue across local
broker, replication, inference and checked-revision lanes. Existing broker and
MCP worker admission stays unchanged; an existing MCP worker still admits one
active tool call and returns `worker_busy` for concurrent calls.

```sh
questlock resources inspect
questlock resources config > admission.json
questlock resources quest
questlock queue revise --workspace /absolute/approved/requests \
  --request first-task.json --config admission.json
```

`inspect` reads current OS signals and prints the complete policy and proposed
capacity. `config` prints a strict runtime JSON policy to edit before creating
the controller; durations are integer nanoseconds. `--config` accepts a regular
file of at most 64 KiB and refuses symlinks and special files. The defaults use one
inference worker, 1 CPU and 256 MiB of estimated incremental memory per inference
task, plus broker and host headroom. These are starting estimates to measure for
your tasks. They do not reserve a second copy of shared model weights: resident
models already reduce OS-reported available memory. Under pressure, the minimum
worker goal can become zero. A successful inspection is not permission to
ignore a later admission rejection.

`resources quest` uses explicit resource fixtures, a real bounded queue and a
temporary HTTP/SQLite broker. It checks overflow rejection, pressure pauses,
protected broker work, durable checked publication and exact reuse after
recovery. It calls no model and is not a resource benchmark.

`queue revise` validates only the explicitly named approved files through the
same pinned workspace boundary as MCP. It runs checked revisions under the
controller and prints each result plus a bounded batch summary of job outcomes.
IDs `request-1`, `request-2`, and so on follow explicit request-flag order. It starts the
managed local broker on the first admitted task; `local stop` stops that broker
without deleting its state. No shell executor, file discovery, paid provider or
automatic task retry is added. A partial admission failure reports how many
tasks were admitted: inspect their results before an explicit retry.

## Runtime admission policy

Each sample re-evaluates effective CPU capacity and available memory. Linux
uses live process affinity, every visible cgroup-v2 ancestor's `cpu.max`,
`cpuset.cpus.effective`, and `memory.max`/`memory.high` minus `memory.current`.
Host load is normalized by the separately counted host CPUs, so a narrow
cpuset does not turn ordinary host load into false overload. Host and current
cgroup CPU/memory PSI `avg10` signals can pause expensive work. Hidden ancestor
constraints and other processes' reservations are not a shared capacity ledger.

macOS reads the native `sysctl` and `vm_stat` tools through fixed absolute paths,
with bounded output and a deadline. Its available-memory estimate includes only
free and purgeable pages, excluding inactive caches; kernel warning/critical
memory pressure pauses expensive work. Load average is runnable/waiting demand,
not a CPU utilization percentage. Missing, malformed, stale, unsupported or
unknown readings pause expensive work instead of inventing spare capacity.

Pressure lowers admission immediately. Recovery requires consecutive fresh
low-pressure samples, then ramps by at most one worker per configured interval.
In-flight work remains counted until its callback returns. The controller never
kills it because capacity shrank. Required prompt/context, model parameters,
fingerprints, durable intents and results remain unchanged.

The dynamic queue row/byte budget is bounded by current memory headroom and
worker capacity, and by absolute configured ceilings. Pressure preserves
already-queued entries even when they exceed the new dynamic budget; new
expensive submissions pause. Protected small broker operations have separate
bounded queue slots, CPU headroom and memory headroom, so expensive tasks cannot
consume that allowance. This keeps admission for cheap persistence/health
available; it cannot guarantee that an exhausted OS will complete the operation.

Queue bytes charge copied payload/ID/lane bytes plus fixed metadata. A lane's
memory reservation must cover these known bytes. Queue allowance is carved out
before running-task memory admission; protected headroom covers its own queue
and bounded workers. Caller contexts, executor
captures, external libraries, decoded payloads and runtime overhead still need
additional estimates. **Reservations are admission accounting, not OS CPU or
memory allocation/enforcement.** Use OS/container limits separately when needed.

## Share the controller across local services

```go
cfg := admission.DefaultConfig() // adjust measured per-lane estimates
controller, err := admission.New(cfg, func(ctx context.Context, job admission.Job) error {
    switch job.Lane {
    case admission.LaneReplication:
        // Decode a small approved edge action/operation ID, resolve the already
        // opened Store/Client, then call Store.Push or Store.Pull exactly once.
        return pushOrPullOnce(ctx, job.Payload)
    case admission.LaneInference:
        // Decode the unchanged approved request; call the local runner once.
        // Publish its result/abstention to a caller-owned bounded receipt sink.
        return chooseLocallyOnce(ctx, job.Payload)
    case admission.LaneBroker:
        return cheapPersistenceOnce(ctx, job.Payload)
    default:
        return errUnknownAction
    }
})
// Handle err. Own one Run loop for this shared instance.
go controller.Run(serviceCtx, admission.SystemSource())
outcome, err := controller.Submit(taskCtx, admission.Job{
    ID: "approved-operation-1", Lane: admission.LaneReplication,
    Payload: approvedSmallActionID,
})
// Handle ErrPaused, ErrQueueFull and ErrDoesNotFit without invoking the action.
// Await outcome and preserve the action's existing uncertainty/recovery rules.
```

An edge push/pull callback leaves its durable outbox alone when submission is
rejected. After a send, cancellation may mean an uncertain publish: retain the
intent and use its explicit exact retry, not an automatic scheduler retry.
An inference callback runs the existing approved local request unchanged;
failed quality or abstention authorizes neither an action nor paid fallback.

The queue is in memory, not a durable job store. Durable tasks belong to the
edge/broker store. `Pending()` returns copied queued descriptors for explicit
inspection/recovery. Cancelling a submitted task explicitly cancels it;
canceling the Run loop stops dispatch and retains pending descriptors, without
canceling active callbacks. Executors and sources must honor their contexts.
No finished-task history or unbounded event log is retained.

Only one controller instance in one process coordinates these reservations.
Separate CLI invocations, brokers, processes and hosts do not share its budget.
In particular, several processes using the same local model can still contend;
put those calls behind the same inference lane or use the model server's own
concurrency controls. Distributed admission and resource allocation remain out
of scope. See the shared [local-coordination architecture](local-coordination.md).

Your chosen provider-cost policy remains separate: resource admission establishes
neither dollar savings nor an account-wide hosted token cap. See the opt-in
[personal policy examples](../examples/preferences/README.md).

## Primary references

- [Linux cgroup-v2 hierarchy and resource interfaces](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html)
- [Linux pressure stall information](https://www.kernel.org/doc/html/latest/accounting/psi.html)
- [Go runtime CPU limits and GOMAXPROCS](https://go.dev/src/runtime/debug.go)
- [Apple virtual-memory inspection](https://developer.apple.com/library/archive/documentation/Performance/Conceptual/ManagingMemory/Articles/VMPages.html)
- [Apple vm_stat implementation](https://github.com/apple-oss-distributions/system_cmds/blob/main/vm_stat/vm_stat.c)
- [Apple memory-pressure implementation](https://github.com/apple-oss-distributions/system_cmds/blob/main/memory_pressure/memory_pressure.c)
