# Use cases

## Several workers need the same answer

Your support agents need a handoff for the same ticket revision. Give them the
same approved task snapshot, provider identity and broker scope. One worker can
claim generation; a concurrent duplicate defers, and a later exact request can
reuse the completed result while its reuse window is valid.

The [Go coordinator](go-integration.md) puts that flow inside your application.
Set per-task input/output limits, a shared scope/day token quota, and optional
token-price dollar caps. Each process throttles its own queue using CPU/memory
signals. Your runtime can use CPU-only hardware or an explicitly configured GPU
server. Your code still checks whether the answer is useful.

This is useful for repeated support handoffs, document summaries and agent
handoffs with fixed source revisions. Changed facts need a new revision. Agent
IDs belong in credentials/logs, outside the prompt; adding them to the request
makes it a different task. [Config and failure cases](../examples/execution/README.md).

## First-time setup without a provider account

Run `questlock setup`, choose your coding client, and finish a checked JSON edit
without a model call. Repeat it to see exact durable reuse. Doctor checks local
configuration without starting a model or changing settings. Codex and Claude
Code use complete native candidate validation and independent rollback;
Claude Desktop uses the native extension installer. [Walkthrough](onboarding.md).

## A local agent team, using the machines you already have

A support queue needs a short handoff: what happened, what is due, and what to
do next. Give a local master the ticket snapshot, check the required fields,
and have two local reviewers examine its proposed answer. Your application
supplies the source and declared quality checks, then decides what reaches a person
or ticket system.

Run every role on one machine, or put reviewers on spare machines at home.
Each inference node uses its own installed local weights. Remote workers connect
over authenticated HTTPS to one authoritative broker; only that broker owns
SQLite. Fresh roles wait for CPU and memory headroom before claiming work.
Failed checks or reviews withhold the answer, and paid fallback stays disabled.
An exact completed repeat reuses the checked result without a new model call.

Measurements used three worker identities on **one Mac**. Six sample tasks,
tuned on the same fixtures, passed their original checks and both reviews.
The same saved Codex baseline used **65,800 hosted tokens**; the selected local
workflows used **0 hosted tokens**. The final run reused five completed results
and ran three fresh roles for the remaining task, with manual answer contracts.
Whole-chat credit savings remain unmeasured. Physical second-machine validation
remains open.

[Run a team locally or across nodes](local-agent-teams.md) ·
[Read the results, failures and limits](local-team-validation-v0.9.1.md)

# Where Questlock fits

## Exact edits of reviewed work (v0.6.0)

A launch post needs its product name and one mission sentence emphasized. A
reviewed JSON document needs one approved field changed. The source is already
available and the intended replacement is exact. Use `revise plan` to preview
pinned source bytes and counted edits, then `revise run` to publish checked text
and provenance. The [examples](../examples/revisions/README.md) include both
formatting and strict JSON cases; `revise quest` covers three simple and three
complex scenarios without any model account.

The concrete win is a deterministic operation with no new model call, rather
than an assumed cheaper rewrite. Source drift, wrong counts and failed checks
withhold output. Human review, the source-generation cost, hardware and hosted
chat allowance remain separate. Meaning changes and open-ended rewrites still
need a new approved task. [Contract and limits](checked-revisions.md).

Questlock is useful when cooperating workers publish small pieces of shared text
and need to detect stale updates or recover the result of an uncertain publish.
One authoritative broker runs on a trusted host and stores UTF-8 artifacts of
up to 1 MiB each; workers can connect remotely over authenticated HTTPS.

The workflows below are plausible integration patterns, not bundled agent
framework integrations or claims of production adoption. The published foundation
is the HTTP API, Go client, scoped credentials, version checks, successful-operation
receipts, publication journal, and process-failure quest, plus bounded inference.
Version **v0.3.0** adds an actual Go/MCP delegation worker,
evaluation, current-run measurement, and optional content-free Langfuse export.
**v0.4.0 experimental** adds explicit human review events
and recorded-cohort cost reporting. It does not automatically observe rewrites
or infer human acceptance from declared checks.
The **v0.5.0 experimental release** added a dedicated OpenRouter text
adapter and explicit SGLang runtime/cache contracts. Standalone dense/HiCache
engines can generate; HiSparse/PD deployments support read-only inspection.
Tagged publication is verified; real GPU qualification remains pending.

## Who this could help

The useful outcome is a shared artifact whose newer version is not silently
replaced by an update based on an older version, with a recoverable result when
a publish response is lost. These are candidate team workflows to evaluate:

| Team | Concrete pain | What Questlock could help with | Integration still needed |
| --- | --- | --- | --- |
| Research and knowledge-tool teams | An agent finishes using notes that another worker has already updated. | Keep the newer shared handoff notes current and surface the stale update for reconciliation. | Connect note reads and publishes to the API; merge, discard, or regenerate conflicting work. |
| Support automation teams | Two assistants prepare competing versions of the same case summary. | Reject a stale replacement of the shared summary draft before the application chooses what to use. | Connect the support system, validate drafts, and implement review and reconciliation. Ticket updates and customer messages remain outside Questlock. |
| AI evaluation teams | A summary publish times out after it may have committed. | Recover the original accepted summary by retrying the saved request, without publishing another version for that operation. | Persist the intended request and operation ID, validate report content, and decide when to retry. |
| Platform teams and homelab builders | Worker failures and restart behavior are difficult to reproduce and inspect. | Exercise the process-failure quest and inspect accepted, conflicting, and replayed publishes in a scoped journal. | Connect representative workers, instrument the workload, and operate the host; Questlock supplies no fleet scheduler. |

These outcomes depend on workers using the publishing API and handling its
responses. They are not adoption claims or evidence of reduced operating cost,
better model output, or production readiness.

### What to measure in a small pilot

- **Observed stale-write rejections:** count `version_conflict` events and inspect
  which competing updates caused them. Decide whether rejected work should be
  discarded, recomputed, or reconciled; a high conflict count alone is not proof
  of useful work saved.
- **Retry correctness:** deliberately lose a publish response, retry the saved
  request, and verify the original artifact is returned with `replayed: true`
  without that retry creating a new version or restoring an older head.
- **Conflict-resolution effort:** instrument the application to count reruns,
  reconciliation attempts, and manual reviews, along with time spent resolving
  them. Version 0.4.0 can store explicitly reported incremental reviewer
  minutes through outcome feedback; it does not observe or automate that work.
- **Storage growth:** record database size and growth in artifact versions,
  successful-operation receipts, and journal events over the pilot. Include
  rejected attempts and replays; they also create journal entries. Optional
  [local cold retention](storage-retention.md) moves eligible payloads and sets
  storage limits without deleting receipts or journal history. Include cold
  files, WAL and compaction headroom in host disk measurements.

## A shared local broker has months of completed work

Keep the broker and approved snapshots your workers already use. Configure a
90-day hot-history policy, preview the eligible batch, then archive it into a
private local directory. A later exact request can read the verified original
content without another model call; reuse validity checks still apply.

Current claims, uncertain outcomes and budget ledgers stay hot. Versions, retry
receipts and audit history remain retained. This can reduce hot payload storage,
but total savings depend on the content; metadata and cold files still need
capacity and a consistent backup. [Operator walkthrough](storage-retention.md).

Start with a bounded workload and use these observations to decide whether the
publishing boundary is worth the integration and operating effort for that team.

## 1. Shared agent notes and handoffs

Two research workers update `notes/handoff.md`. Each reads the current version
before doing its work. If one finishes after another has updated the notes, its
publish receives a version conflict instead of silently replacing newer context.

**Implemented:** reading content and version, compare-and-swap publication, and
an audit record for the conflicting attempt.

**Your integration:** connecting the agent to the API, deciding which notes to
publish, and reconciling changes after a conflict. Questlock does not merge text,
verify a model's conclusions, or protect readers from prompt injection in notes.

## 2. Multiple workers publishing a text artifact

Several workers can produce competing versions of `reports/current.json`, such
as an evaluation summary or a generated plan. A publish names the version the
worker started from. Only a matching update becomes current; the others must
re-read and reconsider their work. Version `0` is a create-if-absent condition.

**Implemented:** atomic publication of one artifact, its new version, successful
retry receipt, and journal entry. Content can be Markdown, JSON, or other UTF-8
text; JSON inside the content string is not schema-validated by the broker.

**Your integration:** worker execution, input selection, output validation, and
conflict resolution. Publishing several keys is not a transaction across those
keys. Publishing a plan does not execute it or make external actions atomic.

## 3. Recovering after a timeout or worker restart

A worker sends a publish and loses the connection before receiving the response.
It cannot infer whether the write committed. If it saved the intended request
before sending, it can retry that exact request with the same agent credential
and operation ID.

**Implemented:** if that request succeeded, the broker returns its original
artifact with `replayed: true`, even if another worker has since published a newer
version. Replaying does not move the current version backward. If no successful
receipt exists, the normal version check still applies.

**Your integration:** durable storage of the worker's pending request, deciding
when to retry, and handling a conflict if another write won. The Go client does
not retry automatically. These semantics cover publication in Questlock, not an
LLM call, email, payment, tool invocation, or other external effect.

## 4. Separate workspaces for separate experiments

A homelab can use a `research` scope for one experiment and a `summaries` scope
for another. Both may use the key `notes/current.md`; their artifacts and journals
remain separate. Workers collaborating within one experiment receive different
agent credentials assigned to the same scope.

**Implemented:** the broker derives the scope from each credential. Reads,
publishes, metadata lists, and journal queries are restricted to that scope.
The trusted local operator creates and revokes credentials through the CLI.

**Your integration:** deciding scope membership and delivering credentials to
workers. Every valid credential can read and publish every key in its scope;
there are no per-key roles or read-only tokens. Scopes are not process sandboxes
or resource quotas. Keep the SQLite volume and operator credentials away from
workers, and configure their OS and network isolation separately.

## 5. A storage and recovery experiment on a small machine

Run `questlock quest` to observe a real worker process being killed, another
worker publishing, and the old request being rejected after restart. The same
quest hard-kills the broker, reads the acknowledged artifact after recovery,
and retries the acknowledged publish to recover its original result.

**Implemented and tested:** those deterministic process-failure cases, plus
competing writer processes and transaction rollback when journaling fails.
Rootless Podman and systemd/Quadlet deployment has also been exercised; the
[validation record](validation.md) describes what was actually run.

**Your experiment:** connecting real agents, measuring your workload, extending
fault injection, or comparing deployment approaches. There are no published
throughput claims here. One host and a serialized SQLite writer do not establish
fleet scale, multi-host failover, or durability under a physical power cut.

## A small integration sketch

Use an existing scoped credential and keep the broker on loopback for a local
experiment. An HTTP worker can follow this sequence:

1. Read `GET /v1/artifact?key=notes%2Fhandoff.md` with its bearer credential.
   Retain the returned version with the content the worker used. A `not_found`
   response means the expected version for creation is `0`.
2. Construct an operation ID unique to this agent's logical publish. Save the
   complete intended request in the worker's private scratch storage before
   sending it if recovery after a worker crash matters.
3. Publish through the same API. For example, if the worker read version `7`:

```http
POST /v1/artifact HTTP/1.1
Host: 127.0.0.1:8080
Authorization: Bearer <scoped-token>
Content-Type: application/json

{
  "key": "notes/handoff.md",
  "expected_version": 7,
  "operation_id": "research-task-42-publish-1",
  "content": "Updated findings ready for the next worker."
}
```

The credential and version above are illustrative. On an uncertain response,
retry the saved request unchanged. On HTTP `409` with `version_conflict`, read
the current artifact and reconcile before constructing a new request and ID.
Do not simply copy the reported version onto an old result to force it through.
An `operation_conflict` means an already-successful ID was reused with different
request data; fix the caller's request/ID handling.

For Go, the corresponding methods are `client.New(baseURL, token)`,
`Client.Get(ctx, key)`, and `Client.Publish(ctx, protocol.PublishRequest{...})`.
The [compilable example](../examples/publish/main.go) demonstrates a read,
publication, and exact retry. Its request stays in memory; add private durable
intent storage to recover across a real worker restart. See the
[HTTP API](api.md) for the full request and error contract.

## 6. Measurable bounded tasks for a coding assistant

An assistant can prepare an approved extraction, handoff, or summary request from
current supplied facts, name its local model and resource limits, and declare
deterministic task checks. `worker run` or the MCP `questlock_delegate` tool
executes that request; identical valid snapshots retain normal durable reuse.

**Implemented in v0.3.0:** bounded local-first generation, at most one
explicit escalation after a confirmed complete durable quality failure, withheld
failed answers, and current-run measurements. Unknown, incomplete, budget, or
provider/accounting-error outcomes stop. Hosted escalation additionally requires
process permission, explicit prices, and per-call/daily caps. The MCP worker
offers no shell, workspace edit, or file-discovery tools.

**Your evaluation:** qualify a model on the actual task, supply approved context,
update revision when facts change, and review passing output. The
[local v0.3.0 observations](measurements-v0.3.0.md) qualified neither suite
configuration, and the worker withheld a failed feature extraction. Durable
reuse did not turn those outputs into approved answers. Read [delegation](delegation.md)
and [measurement limits](inference-observability.md) before using the worker.

The surrounding hosted chat keeps its existing model and billing. Questlock
usage counters exclude its prompt, tool, and context tokens; they measure only
the delegated provider/local generation. Version v0.5.0 added an explicit
OpenRouter companion adapter and self-hosted SGLang dense/HiCache tiers;
automatic model choice and full-session interception remain future work.

## 7. A passing answer is rewritten by a person

A support-case extraction passes strict JSON and value checks, then a reviewer
finds an omitted requirement and corrects the final artifact. Counting only the
passing attempt would hide that correction and its effort.

**Implemented in v0.4.0:** opt-in `track_outcome` worker
records, explicit accepted/rewritten/rejected/reopened events, final artifact
digests, and a declared-cohort report. Every rewrite clears prior acceptance,
including style changes. The reviewer must explicitly reaccept the final
artifact. Failed and rejected tasks remain in the cohort's recorded cost
numerator; reused invocations add zero new generation cost. Unknown attempt
cost, event effort, or hourly rate keeps the combined metric unknown. A task
with no feedback also has unknown human effort.

**Your integration:** obtain the actual review decision, report incremental
effort and its reason, choose the cohort before comparing it, and reconcile
version conflicts. A scope token authorizes artifact access but does not prove a
human authored an event. The application must supply that identity and review
boundary. There is no passive editor watcher or model training from these events.

Define what one business deliverable means in the application. The report counts
ID/revision pairs, so selecting two accepted revisions counts two outcomes.
Missing effort in an immutable feedback event cannot be retroactively corrected
in this release; appending an event does not erase the missing observation.

Run `questlock outcome quest` for three simple and three complex synthetic
scenarios without a model call. The [runnable examples](../examples/outcomes/README.md)
show a check-passing answer becoming accepted, rewritten, then reaccepted; a
cohort adds rejected work instead of discarding its cost. Read
[the outcome accounting guide](outcomes.md) for report fields and limitations.

For a useful pilot, compare the same declared task set across configurations.
Record failed attempts, corrections, acceptance, and review effort consistently.
Avoid selecting only successful tasks after seeing the results. Report known
partial subtotals and missing coverage alongside the current acceptance count.
Configured model prices are estimates rather than invoice data; surrounding
hosted-assistant tokens, hardware, and unrecorded work remain outside this report.

## 8. Repeated prefixes on an explicitly operated runtime

A team repeatedly summarizes approved repository snapshots with the same stable
instructions. Whole answers may be reused only when the task revision is
unchanged. For a new revision, the runtime may still reuse eligible prefix KV.

**Implemented since v0.5.0:** SGLang v0.5.20 runtime readback, native
tokenization and bounded standalone dense/HiCache generation. Deployment and
cache namespace enter identity; a changed observed runtime stops an existing
provider before stale answer reuse. Runtime-reported device/host/storage cache
counters remain separate from API cache billing. Missing optional KV counters
stay unknown; they do not become zero. HiSparse and PD engines are inspection-only.

**Your qualification:** pin actual model/tokenizer/runtime versions, supply a
supported GPU environment, and compare matched cold/warm runs with Questlock
result reuse disabled. Record output checks, human outcomes, latency, memory,
failures and hardware costs. An emulator or predictor is not GPU evidence.

Run `questlock model runtime-quest` without installing SGLang or weights:

| Complexity | Offline HTTP-fixture scenario | Observable boundary |
| --- | --- | --- |
| Simple | Dense native generation | One bounded generation with known usage |
| Simple | Exact result reuse | One original generation, zero new usage on repeat |
| Simple | Separate cache namespaces | Different request identities and runtime salts |
| Complex | HiCache tier reporting | Runtime KV evidence does not discount billable input |
| Complex | Runtime configuration changes | Existing provider stops before stale reuse |
| Complex | Unknown KV metrics and HiSparse | Missing counters stay unknown; direct sparse decode sends no generation |

These scenarios test protocol behavior, not GPU execution or a latency predictor.
See [the runtime guide](runtime-cache.md) and [deployment profiles](../deploy/sglang/README.md).

## 9. A bounded task through OpenRouter

A developer wants OpenRouter model access while coordinating an approved task
across several workers. The v0.5 companion adapter requests one explicit model
and upstream provider with fallbacks disabled, keeps the route policy in
identity, and applies the same durable claims, review checks and result reuse.

**Your integration:** choose a route with the required token/cache usage fields,
supply an explicit input reservation bound and current prices, and configure
per-call/day caps. The bound is operator-declared, not a tokenizer measurement.
Unknown usage halts instead of becoming a free or automatically retried call.
Paid worker escalation needs process `--allow-paid` too. Direct provider
protocols remain available. The [example spec](../examples/inference/openrouter-summary.json)
is for offline planning; live paid routing and invoice savings remain unvalidated.

## When another component is needed

- **Task ownership or worker leases:** a version check does not grant a worker
  exclusive ownership or stop an old worker from running. A permitted worker
  can fetch the latest version and publish again.
- **Agent execution safety:** the broker does not sandbox arbitrary workers'
  shell commands, model access, host files, or network calls. The bounded MCP
  worker exposes a narrower request-file interface, but its declared checks
  do not judge general safety or correctness. The trusted host operator can
  inspect or alter the database.
- **Large or binary artifacts:** content is limited to 1 MiB of UTF-8 text. This
  is not an object store or a binary transfer service.
- **Fleet management or high availability:** remote workers can share one
  authoritative HTTPS broker. There is no fleet manager, multi-writer database
  replication, distributed consensus, or automatic broker failover.
- **A fixed total disk footprint:** optional cold retention moves eligible
  payloads and caps main-database pages and logical cold bytes. Retained metadata,
  WAL and compaction need capacity; there is no automatic deletion. The HTTP API
  does not expose a general historical-content browser, while local
  `storage history` reads one exact version. SecondGate provides scoped pages.

The publishing boundary makes stale writes and uncertain results explicit.
Bounded inference builds on it to avoid repeating an eligible task and to expose
resource use; the worker returns only answers passing declared checks.
Those checks and measured reuse remain task-specific, rather than a guarantee of
general model quality.
