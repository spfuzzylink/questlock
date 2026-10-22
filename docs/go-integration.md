# Bring your app. Keep your runtime.

You have a Go app, a model server and agents doing the same work again. Put
Questlock between the task and that model call. Let the broker share completed
work. Let each process slow down when its own machine is under pressure.

The `execution` package wires together the existing `inference.Runner`, bounded
`admission` queue and optional Langfuse exporter. Your app supplies the broker,
provider and approved task. There is no new gateway to deploy.

## Wire it once in your service

From your application module, with Go 1.26 or newer:

```sh
go get github.com/spfuzzylink/questlock@v0.10.0
```


```go
broker, err := client.New(brokerURL, scopedToken)
if err != nil {
    return err
}
provider, err := inference.NewOllamaProviderContext(
    serviceCtx, "http://127.0.0.1:11434", installedModel,
)
if err != nil {
    return err
}
coordinator, err := execution.New(serviceCtx, execution.Config{
    Runner: inference.Runner{Broker: broker, Provider: provider},
    Admission: admission.DefaultConfig(),
})
if err != nil {
    return err
}
// Keep this instance for the service's lifetime. Close with a bounded context
// during shutdown; do not construct a new queue for each incoming request.
```

Import the `client`, `inference`, `execution` and `admission` packages from
`github.com/spfuzzylink/questlock`. The [complete runnable example](../examples/execution/main.go)
includes imports, deadlines, startup sampling, shutdown and error handling.

`New` starts resource sampling. `Run` returns `admission.ErrPaused` while initial
samples are warming up or current CPU/memory headroom is unsafe. Inspect
`coordinator.Status()` and let your application decide when to submit again.
No model call is made for a rejected submission. Avoid putting an unbounded
list of goroutines in front of the bounded queue.

## Give repeated work a proper snapshot

```go
spec := inference.Spec{
    Task: "support-handoff", Revision: "ticket-42-r3",
    Provider: "ollama", Model: installedModel,
    Instructions: "Write one sentence using only the supplied facts.",
    Input: "The invoice export fix is deployed. Ask the customer to try again.",
    MaxInputTokens: 2048, MaxOutputTokens: 128,
    ReuseForSeconds: 3600,
}
result, err := coordinator.Run(taskCtx, spec)
if err != nil {
    return err // inspect pending/uncertain state; do not retry generation blindly
}
fmt.Println(result.Response.Text, result.Reused, result.ThisRunUsage)
```

Same approved snapshot and provider identity, within the reuse window: return
the completed result with zero new generation tokens. Two workers racing that
snapshot: one can claim it; the other gets `inference.ErrInFlight`. New facts,
model settings or provider identity: a different fingerprint, so fresh work can
run. Change `Revision` whenever external facts change, even if the prompt stayed
the same. Questlock does not spot arbitrary reasoning loops or decide that two
different prompts mean the same thing.

Reuse is for results. Do not use it to replay a payment, customer email or
another external action. A complete, durable answer still needs your task's
quality checks; it is not proof that a person accepted it.

## Set the limits in JSON

Keep your approved task in a file instead of wiring every setting into Go:

```go
file, err := os.Open("task.json")
if err != nil {
    return err
}
defer file.Close()
spec, err := execution.LoadSpec(file)
if err != nil {
    return err
}
result, err := coordinator.Run(taskCtx, spec)
```

`LoadSpec` reads at most 1 MiB. It rejects unknown or duplicate keys, nulls,
invalid UTF-8, trailing data and invalid task plans. It makes no network call.
Use `admission.LoadConfig` for the separate, complete local resource policy;
[admission.json](../examples/execution/admission.json) is a starting point.
The [runnable example](../examples/execution/README.md) accepts `-spec` and
`-admission`; endpoint and credentials stay outside the task snapshot.

Per-task `max_input_tokens` and `max_output_tokens` bound one fresh generation.
Add a shared quota when several processes should use the same daily allowance:

```json
"daily_token_budget": {
  "input_tokens": 32000,
  "output_tokens": 4000,
  "total_tokens": 36000
}
```

That is one broker-scope policy per UTC day, shared by participating workers and
models. All three caps are positive integers, at most 1e12 each. Local and hosted generations both
consume it. Before generation, reserve the full task input/output caps; after a
known result, settle to inclusive actual usage. Reuse adds no new generation
usage. Pending or unknown work keeps its reservation. A measured overrun freezes
the ledger for operator review. Reasoning tokens are already part of output;
do not count them twice. Tokenizers differ across models, so this is an aggregate
usage guard, not a unit of equal work or quality.

For hosted APIs, `prices`, `max_cost_usd` and `daily_budget_usd` add a separate
per-task/daily dollar policy. Supply current route prices, including cache-write
pricing. The [hosted example](../examples/execution/hosted-budget.example.json)
uses illustrative prices and is rejected by the local-only executable.

Use a dedicated scope and configure the same caps on every v0.10.0+ worker
before admitting any work. The quota does not count earlier unbudgeted runs.
The dollar ledger also requires a consistent daily cap and
explicit prices across models and credentials. These are cooperating-client
policies: an older client or raw broker writer is not forced to follow them.
Each daily ledger supports at most 1,000 reservation entries and 32 compare-and-
swap attempts per operation. A full ledger or exhausted contention bound stops
fresh work. This release does not claim unlimited cluster scale.

## CPU today, GPU when you have one

By default, the queue reads CPU capacity, memory headroom and pressure on its
own host. It works without a GPU. Ollama decides whether its installed weights run
on CPU, GPU or both; choose a model that fits. Nothing here silently switches a
failed GPU request to CPU or another provider.

For your own **SGLang v0.5.20** standalone server, change the explicit provider:

```go
runtime := inference.SGLangConfig{
    DeploymentID: "support-model-r1", CacheNamespace: "support",
    Profile: "hicache", SelfHosted: true, MaxInputTokens: 2048,
}
provider, err := inference.NewSGLangProviderContext(
    serviceCtx, apiKey, runtimeURL, servedModel, runtime,
)
// Handle err, then supply provider to the same execution.Config.
// Match the task: Provider="sglang", Model=servedModel, SGLang=&runtime.
```

Use `dense` for a dense server and `hicache` only when that deployment has
HiCache configured. The adapter checks the pinned native runtime, tokenizes
through it and records reported cache counters. SGLang owns KV tensors,
batching and eviction. Questlock does not mirror its radix tree, pin VRAM blocks
or read GPU memory through the CPU probe. Cache namespaces separate reuse
domains; they do not reserve a physical cache slot.

For a remote GPU server, this process's OS readings do not describe that remote
GPU. Keep serving capacity limits in the runtime; set measured local admission
estimates and provide a trusted `admission.Source` when your app has better
CPU/memory signals. The current source contract has no VRAM counter. See the
[SGLang deployment and GPU qualification instructions](../deploy/sglang/README.md).

## More than one machine

Give each trusted worker a broker token in the same scope and connect over
HTTPS. For a private CA:

```go
broker, err := client.NewWithTLSRoots(brokerURL, scopedToken, brokerCAPEM)
```

Each process gets its own coordinator. All of them can share the broker's task
claims and completed results. They must agree on the task snapshot and provider
identity for a reuse hit. Share one immutable runtime endpoint when that is your
deployment; otherwise configure and validate each runtime deliberately. A
matching label is not proof that two machines have identical weights or memory.

Keep one authoritative broker database on its own trusted host. Tokens in the
same scope grant access to that scope's artifacts; worker labels are not machine
attestations. Local queues do not add up to a distributed GPU budget. The broker
also does not route model requests: your worker calls its configured provider.

SGLang Router can handle serving-side routing in a larger deployment, but a
router URL is not automatically compatible with this native adapter's runtime
readback and tokenization contract. Validate a gateway's identity, counting,
usage and no-retry behavior before implementing an `inference.Provider` for it.
Do not swap it into the standalone example and assume cache locality is covered.

## Add Langfuse without changing the task

```go
exporter, err := observability.NewLangfuse(observability.Config{
    Endpoint: tracesURL, PublicKey: publicKey, SecretKey: secretKey,
})
// Handle err. Set execution.Config.Exporter=exporter before calling New.
```

Questlock exports bounded timing, usage, outcome and available runtime-cache
metadata. Its event schema leaves prompts, answers and raw task labels out.
Check `result.Telemetry.Status` and `result.Telemetry.Error` separately from
the task result. Export is attempted once. An unconfirmed export keeps the
durable result and never triggers another generation. Endpoint acknowledgement
does not establish dashboard visibility or answer quality.

Export runs inside the admitted operation and holds its worker slot until it
returns. `ExportTimeout` defaults to two seconds and accepts 1 ms through five
seconds. Lower it or leave export disabled when that wait does not fit your service.

## Keep the provider choice yours

The interfaces stay small: `inference.Provider` counts and generates;
`inference.Broker` reads and publishes; `admission.Source` samples resources;
`observability.Exporter` sends metadata. Custom implementations must honor
contexts and be safe for the configured concurrency. A custom self-hosted
provider also implements `inference.SelfHostedProvider`; that is your trusted
declaration, not an independent billing audit.

Ollama and explicitly self-hosted SGLang work with the coordinator's default
hosted policy. Existing OpenAI, Claude and OpenRouter adapters require
`AllowHosted: true` plus your explicit route, token and price budgets. That
opt-in selects a provider; it does not enable fallback. OpenRouter remains the
provider-access layer. Questlock is an independent companion that helps your
application avoid repeating eligible work before reaching it.

Admission also wraps completed-result lookups, so a hit can pause under local
pressure. Reservations estimate per-task CPU/memory; they are not OS limits,
GPU allocations or a promise to prevent every OOM. Throttling trades immediate
concurrency for controlled pressure. Measure useful completions and latency on
your hardware before calling it a throughput improvement.

## Try the failure cases too

| Scenario | What to check |
| --- | --- |
| Repeated support handoff | Same snapshot returns `Reused=true` with zero new token usage. |
| New ticket revision | Changed facts produce fresh work instead of stale reuse. |
| CPU-only app | The same admission flow runs without GPU telemetry or paid fallback. |
| Two workers race | Shared HTTPS broker allows one generation; the other sees pending work. |
| Memory pressure or stale readings | Admission rejects before a task claim or provider call. |
| Langfuse is down or generation is uncertain | Keep the result or pending claim; neither condition authorizes another model attempt. |

Run the [example](../examples/execution/README.md) against your own local
runtime. For repeatable checks without a model, GPU, account or provider bill:

```sh
go test ./execution -run 'TestHTTPS|TestAdmissionAndValidation|TestLangfuseFailure|TestUnknownGeneration'
```

Those tests use real HTTPS and private SQLite with isolated worker clients,
scripted generation and synthetic resource readings on one machine. They check
coordination and failure handling, not multi-host scale or model throughput.
The [measured overhead and reuse report](execution-validation-v0.10.0.md) includes
all repeated and unique workload samples, with the shared quota on and off.
