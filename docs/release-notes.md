# Questlock 0.10.0 — plug it into your Go app

**Experimental release candidate.** Existing broker and inference APIs remain;
the new `execution` package wires the pieces together for application developers.

- One process-local coordinator combines bounded admission, shared task/result
  reuse and optional content-free Langfuse export. Telemetry failure leaves the
  task result alone.
- Optional shared scope/UTC-day input, output and total token quotas reserve
  declared task caps before generation, settle known usage and retain unknown
  reservations. Existing per-call/day dollar controls stay separate.
- Strict task JSON loading and declarative examples cover local Ollama, pinned
  standalone SGLang, resource settings and explicit cost/token budgets.
- CPU-only runtimes use the same API. Device execution, GPU batching and KV
  storage stay with the inference engine. Router-backed serving still needs a
  qualified provider contract.

Read the [Go guide](go-integration.md), [complete example](../examples/execution/README.md)
and [measured overhead and reuse](execution-validation-v0.10.0.md).
Resource admission is per process; quota accounting coordinates trusted workers
using one broker scope and matching policy. It does not meter outside calls or
turn the broker into a GPU scheduler. Provider-token cost and hardware cost are
separate; synthetic control-plane tests are not GPU or model-quality benchmarks.

# Questlock 0.9.1 — local quality gates

**Experimental release, published and verified.** Four native archives, two macOS
MCPB bundles, checksums and both Linux container architectures passed their
release gates. [Validation](validation.md) records the exact source, pipelines,
anonymous asset checks and guarded local upgrade. Immutable archives retain
their source-era candidate wording. Published v0.9.0 and older assets remain
unchanged.

Local team quality controls now include opt-in canonical framing, JSON output,
manual primitive types and semantic field roles, bounded nonstreaming reasoning,
explicit sampling and HTTP deadlines, and optional model release. Every original
semantic check and reviewer approval remains required. Nil options preserve
existing identities, wire bytes and defaults; paid fallback stays unavailable.

macOS admission now includes bounded reusable memory, retaining headroom and
pressure limits. Fresh roles can wait for admission before claiming; generation
is not retried. Command arrays, active client settings and the permanent brand
image remain unchanged.

The six original bounded task contracts completed with local masters and both
reviews against the same saved 65,800-token Codex baseline: zero hosted tokens
and $0 provider API spend in the selected local workflows. The final invocation
reused five completed cases and generated three resource roles. That case uses
extra operator-declared types/semantics. All deliberate failed policies remain
in the report; this is tuned workflow evidence, not model parity or general
coding quality. Separate frozen validation completed 1 of 2 cases; one risk reviewer
exhausted its output cap. Whole-build credit and invoice savings remain unknown.

The archives now include a completed single-task request, complete reasoning
admission settings, the exact matched suite and synthetic historical receipts.
`team benchmark` can inspect the saved comparison offline without models or
account access. New experiments keep private task data in ignored directories;
the publication guard also rejects forced/deleted private demo data in history.

[Measurements and failures](local-team-validation-v0.9.1.md) ·
[Team guide](local-agent-teams.md) · [Offline comparison](../examples/teams/README.md) ·
[Architecture](diagrams/local-agent-teams.svg) · [Editable draw.io](diagrams/local-agent-teams.drawio)

---

# Questlock 0.9.0 — local roles, shared gates

**Published experimental release.**
[Release and downloads](https://github.com/spfuzzylink/questlock/releases/tag/v0.9.0) ·
[Verified publication](validation.md#v090-observations--2026-10-19)

Four native archives, two macOS MCPB extensions and both Linux container
architectures are published and verified. Immutable archives retain their
source-era candidate wording; the web guides record publication. Older releases
and the permanent brand image stay unchanged.

A bounded local master and one to three independent local reviewers now share
claims and checked results through the existing broker. `team init` creates an
inspectable task; `team run` executes selected roles; `team status` reads without
generation. Installed Ollama and isolated native Codex OSS are supported. The
team schema permits no hosted role or paid fallback. Native compatibility is
currently Codex CLI 0.163.0 and its prereleases.

Workers can hand roles across HTTPS with separate scoped credentials and an
explicit private CA. Pending/uncertain claims never expire into automatic
regeneration. Rejected answers withhold final text. Exact checked repeats need
no model, native executable, or inference admission. These text roles cannot
edit files or replace an existing chat backend.

`team quest` exercises three identities and six cases using a real private
HTTPS broker and scripted providers, with zero model calls. `team suite`,
`team prompt`, `team baseline-record` and `team benchmark` make a matched
comparison explicit and keep failed/missing cases in the denominator.

**The 99% target is not established.** Six deliberate Codex subscription baseline
answers passed; the tested local model and memory headroom did not complete the
team suite. $0 paid API charges are not evidence of equivalent accepted work.
[Measurements, failures and limits](local-team-validation-v0.9.0.md) include the
65,800 hosted baseline tokens, local usage, pressure refusals and unknowns.
No active client config or permanent brand image changed.

[Team guide](local-agent-teams.md) · [Six-case walkthrough](../examples/teams/README.md) ·
[Architecture](diagrams/local-agent-teams.svg) · [Editable draw.io](diagrams/local-agent-teams.drawio)

---

# Questlock 0.8.0 — local work, shared results, your policy

**Published experimental release.**
[Release and downloads](https://github.com/spfuzzylink/questlock/releases/tag/v0.8.0) ·
[Verified publication](validation.md#v080-observations--2026-10-17)

Four native archives, two macOS MCPB extensions and both Linux container
architectures are published and verified against the tagged source. Older
releases stay unchanged. Immutable archives retain their source-era candidate
wording; these web guides record publication.

- **SecondGate:** a private durable edge outbox, pinned writes to one authoritative
  broker, and atomic paginated snapshot/change consumption. Offline intent is
  not acceptance. Exact uncertain retries retain their operation ID; conflicts
  require an explicit caller decision. A dedicated pinned route makes older
  brokers reject the operation before mutation.
- **Local decisions:** optional TypeSafe System One Adapter integration with an
  installed Ollama model. Go returns an advisory choice or abstains. This is a
  local alternative, not Jev weights; scores are uncalibrated and authorize no
  action. Larger local reasoning remains an explicit separate request.
- **Adaptive admission:** opt-in CPU/memory-aware queues across named local lanes,
  with protected broker headroom, hard bounds, immediate pressure pause and
  gradual recovery. One controller coordinates one process. Reservations are
  estimates; OS/container resource enforcement stays separate.
- **Your policy:** copyable local-only worker and personal-instruction examples.
  Provider, model, limits, checks and reuse remain explicit configuration. Personal
  maintainer preferences stay outside public repository instructions.

`questlock edge quest` and `questlock resources quest` exercise real temporary
HTTP/SQLite workflows without a model. Existing setup, manual publishing, worker,
revision, outcome and runtime commands remain supported; existing MCP single-active
admission is unchanged. Ordinary Go commands require no Python. The optional
System One bridge has its own pinned environment and setup inputs.

Local smoke tests retain a malformed four-choice abstention alongside passing
two-choice calls. CPU/memory pressure refusals are retained too. These establish
protocol and safety behavior, not quality parity, invoice savings, faster inference
or a whole-chat hosted-usage percentage. Native Claude Desktop connection and
real cross-host/TLS deployment qualification remain unverified.

[One local control loop](local-coordination.md) · [Edge coordination](second-gate.md) ·
[Local decisions](system-one.md) · [Resource policy](adaptive-admission.md) ·
[Personal policy examples](../examples/preferences/README.md)

---

# Questlock 0.7.0 — a first task before a first token

**Published experimental release.**
[Release and downloads](https://github.com/spfuzzylink/questlock/releases/tag/v0.7.0) ·
[Verified publication](validation.md#v070-observations--2026-10-17)

The four native archives and two macOS MCPB extensions are published. Native
Claude Desktop import/connection remains pending; package and protocol checks
alone do not qualify it. Immutable archives retain their source-era candidate
wording; the web documents record publication without replacing old assets.

Run `questlock setup`, choose Codex, Claude Code or Claude Desktop, and finish a
checked task without a model call or provider account. Codex/Claude Code setup
validates complete isolated candidates, preserves unrelated settings and uses
an independent rollback watchdog before committing a live native read.

New commands: `setup`, `doctor`, `try`, `local start|status|stop`, and
`worker local-serve`. `local-agent` optionally runs the main native Codex CLI
loop against an installed local Ollama model in an isolated home. The managed broker provisions private credentials and
shares one local SQLite owner across clients. Paid escalation stays disabled.
Claude Desktop uses a platform-specific binary MCPB extension and its native
installer; native Desktop import/connection qualification remains pending.

The repository now uses one permanent versionless brand image and a shorter
README. Historical releases and artwork remain available. Existing manual
broker, model, revision, outcome and runtime commands remain supported.

[Start here](onboarding.md) · [What we measured, including failures](success-report-v0.7.0.md) ·
[Architecture](diagrams/onboarding.drawio)

---

# Questlock 0.6.0 — checked edits without another generation

**Published experimental release.**
[Release and downloads](https://github.com/spfuzzylink/questlock/releases/tag/v0.6.0) ·
[Verified artifacts and installed-worker evidence](validation.md#v060-observations--2026-10-16)

Published v0.5.0 and older releases/assets remain available unchanged. Four native
archives, both container architectures and the installed v0.6.0 worker are verified.
The immutable archives retain their source-era candidate wording; these web docs
record subsequent publication without replacing those assets.

## New in 0.6.0

- A Go `revision` library and `revise plan`, `run`, and `quest` commands apply
  exact counted literal replacements to a SHA-256-pinned source. Required
  source/output checks bound what can be returned; no model is called.
- Immutable content-addressed results compose with the existing scoped broker
  and exact parent key/version/digest provenance. Failed source/count/checks and
  uncertain publication withhold text without a paid or local model fallback.
- `questlock_revise` shares the bounded MCP workspace, admission and cancellation
  boundary with `questlock_delegate`; neither gains a filesystem-edit tool.
- Opt-in revision accounting records known zero new usage/provider charge with
  `deterministic_edit` or exact-repeat `reuse_zero`. Human acceptance remains
  explicit. Revision outcome cohorts require v0.6.0+ readers/writers; keep them
  separate from older generation cohorts during upgrades.
- Three simple and three complex offline scenarios, complete JSON examples,
  editable architecture, and Codex/Claude skill guidance for preferring exact
  edits before new generation.

Read [what, why and how](checked-revisions.md), [runnable examples](../examples/revisions/README.md),
and [release evidence](validation.md#v060-observations--2026-10-16).
A formatting development run and exact repeat made zero new model calls; their
single observed latencies establish no benchmark or whole-session savings.
New facts, open-ended rewrites and reasoning changes still need reviewed tasks.


Go vet/race, packaging/public-source checks, all five offline quests, native
Darwin/Linux ARM64/AMD64 archive CI, source-container execution and disposable
Langfuse readback passed before publication. Anonymous registry verification
checked both image configs and all layers. Published binary/skills activation
used an independent rollback watchdog and a private complete known-good backup;
a fresh native MCP process discovered both tools and returned a checked exact
revision reuse with zero new generation. Installation changed no client settings.
The development workstation separately completed its guarded global local-Codex
instruction rollout; [supported opt-in setup](installation.md#optional-default-for-local-codex-chats)
is documented. This preference is not enabled by the library installer. Broader
local comparison evidence now records all six deterministic edits and repeats
passing with zero generation, alongside one passing local-model baseline and
five withheld failures. See the validation record for all measured usage, the
single passing timing pair and the comparison's limits.

---

# Questlock 0.5.0 — explicit provider routes and runtime cache evidence

**Published experimental release.**
[Release and downloads](https://github.com/spfuzzylink/questlock/releases/tag/v0.5.0) · [Verified artifacts](validation.md#v050-observations--2026-10-16)
Repeated tasks and repeated prefix computation are different costs. Version
**0.5.0** adds explicit OpenRouter and SGLang contracts while keeping runtime KV
evidence, API token accounting, whole-result reuse and human acceptance separate.
Published v0.4.0 and older tags/assets remain unchanged.

## New in 0.5.0

- A dedicated nonstreaming OpenRouter Chat Completions adapter: one explicit
  model/provider, requested upstream fallbacks disabled, operator input
  reservation bound, and strict native token/cache usage validation. It makes
  no generation retries and does not substitute a direct OpenAI counting route.
  Worker paid escalation still requires process permission, prices and caps.
- An explicitly self-hosted SGLang v0.5.20 native adapter pinned to the reviewed
  upstream protocol. Standalone dense/HiCache engines support bounded
  tokenization/generation. Observed runtime identity is rechecked before result
  lookup and around generation; an existing provider stops on changes.
- `model runtime-inspect --spec PATH --api-base-url URL` for read-only
  configuration evidence, with `capabilities`,
  `evidence: "runtime_reported_configuration"`, and `gpu_validated: false`.
  HiSparse and PD prefill/decode engines are inspection-only: direct decode
  generation lacks the validated ingress/bootstrap orchestration this release
  deliberately does not implement.
- Runtime-reported KV hit/tier counters stay separate from billable cache token
  buckets. Missing optional runtime counters stay unknown. Exact result reuse
  retains historical cache evidence without presenting it as a new generation.
  Allowlisted optional Langfuse metadata includes no prompts, answers, runtime
  URLs, model paths or credentials.
- `model runtime-quest`: three simple and three complex deterministic native
  HTTP scenarios with the real broker. They exercise dense generation, exact
  reuse, namespace separation, HiCache tier accounting, runtime mutation, and
  unknown metrics/HiSparse boundaries. They are neither GPU execution nor a
  latency predictor.
- Pinned dense/HiCache/HiSparse argv profiles, example specs, matched cold/warm
  qualification instructions, and an editable layered architecture diagram.

Read [the runtime contract](runtime-cache.md), [setup and qualification](../deploy/sglang/README.md),
[workflow schemas](model-workflows.md), and the
[editable architecture](diagrams/runtime-layers.drawio).

## Evidence and limits

Go vet/race, 70 Python regressions, all four native platforms, source-container
execution, and disposable real Langfuse readback passed. Published archives
match the tagged source and checksums; both native GHCR images and all layers
were verified anonymously. Older tags remain unchanged. See the
[verification record](validation.md#v050-observations--2026-10-16).
Live OpenRouter paid-account and SGLang GPU/PD performance have not been
validated. The upstream CPU simulator instructions are exploratory and were
not executed as evidence for this release.

One local development attempt recorded known usage of 75 input / 17 output
tokens; its registered outcome remains unreviewed. It does not establish human acceptance, whole-chat
savings, model quality, or a GPU optimization win. Surrounding hosted assistant
allowances remain separate. Self-hosted inference has no provider token charge
in Questlock's ledger, but hardware, power, storage and operation still cost
resources. Caller-supplied prices remain estimates, not reconciled invoices.

---

# Questlock 0.4.0 — explicit human outcomes after checks

**Experimental release.** A worker can pass its declared checks while a person
still has to rewrite the work. Version **0.4.0** separates check success from
explicit human acceptance, so cost per accepted
task can be reported for a declared cohort when its accounting inputs are known.

[Release and downloads](https://github.com/spfuzzylink/questlock/releases/tag/v0.4.0) ·
[Installation](installation.md) ·
[Validation record](validation.md#v040-observations--2026-10-14)

## New in 0.4.0

- A Go `outcomes` package using the existing scoped versioned artifact API. No
  SQLite schema migration or additional storage service is required.
- Opt-in approved worker request field `track_outcome: true`, recording
  content-free invocation/attempt metadata, including failed checks and exact
  zero-new-generation reuse. Tracking failure never retries generation.
- Explicit `accepted`, `rewritten`, `rejected`, and `reopened` feedback linked to
  run IDs. Every rewrite clears acceptance; explicit reacceptance identifies the
  final artifact by SHA-256. Event IDs and expected versions preserve retry and
  conflict behavior.
- `outcome record`, `feedback`, `show`, `report`, and `quest` commands. Reports
  include all recorded cohort attempts and explicit incremental reviewer time,
  including failed/rejected work, with an optional explicit hourly rate. Missing
  accounting remains unknown; zero accepted outcomes has no unit-cost result.
- Three simple and three complex synthetic offline scenarios plus runnable
  public examples and an editable feedback diagram. They require no model call.

Read the [human outcome guide](outcomes.md), [synthetic examples](../examples/outcomes/README.md),
and [feedback architecture](diagrams/human-outcomes.drawio). This release
does not monitor edits automatically, authenticate a reviewer's human identity,
retrain or select models, export human feedback to Langfuse, reconcile invoices,
or include surrounding hosted-assistant/hardware/unrecorded-work costs.

The [validation record](validation.md#v040-observations--2026-10-14) records
the tagged publication gates, archive provenance, and release limitations.
Published v0.3.0 and older archives, tags, and historical observations remain
available unchanged.

---

# Questlock 0.3.0 — measurable bounded tasks and local delegation

Repeated tasks need observable resource use and an acceptance gate before a
worker returns generated text. Version **0.3.0** adds current-run measurements,
optional content-free Langfuse export, deterministic evaluation, and a Go/MCP
worker with an explicit local-first policy. Published 0.2.0 and older releases
remain available.

[Release and downloads](https://github.com/spfuzzylink/questlock/releases/tag/v0.3.0) ·
[Installation](installation.md) · [Container](containers.md) ·
[Validation record](validation.md#v030-observations--2026-10-14)

## New in 0.3.0

- Per-invocation Runner phase metrics, separate from durable result identity and
  historical generation timing. Optional local Ollama streaming observes first
  and last content arrival; reuse has no fresh generation or TTFT.
- Opt-in Langfuse model metadata export: one root plus one generation child for a
  fresh attempt, one root with zero new usage for reuse, exclusive cache/reasoning
  token buckets, and optional declared-check scores. No prompt or answer text is
  exported. Export failures never retry a model call. MCP uses a silent bounded
  best-effort queue that can drop telemetry.
- `model evaluate` with three simple and three complex public cases, explicit
  configurations, private reports, and separate fixture/live modes. A report's
  selection changes no model or future routing policy.
- `worker run` and stdio `worker serve`; the sole MCP tool accepts an approved
  request path inside a declared workspace. No shell, file editing, file discovery,
  or full-session interception is exposed. Concurrent calls return `worker_busy`.
- At most one explicit escalation after an error-free complete, durable,
  valid-known-usage answer fails declared checks. Unknown, incomplete, budget,
  provider, publication, and settlement outcomes stop. Hosted escalation requires
  process `--allow-paid`, explicit prices, and per-call/daily budgets.
- Local Ollama work neither consumes nor clears the API dollar ledger; it can
  continue when paid admission is frozen. Hardware and runtime costs remain.
- An isolated disposable Langfuse v4.59.0 stack with six content-pinned service
  images and a reusable required CI contract. Actual API readback verified three
  roots, two generation children, exclusive usage/cost fields, streaming timing,
  content-free metadata, and root quality scores `1/1/0`.

## Try it

After extracting your platform's archive:

```sh
./questlock model quest
./questlock model evaluate
./questlock model install --agents codex,claude
```

Both demo commands use simulated public fixtures and need no model account.
The worker needs an existing authenticated broker, an approved request, and
installed local weights. See [measurements and Langfuse setup](inference-observability.md),
[bounded delegation and native MCP registration](delegation.md), and the
[installation guide](installation.md).

To build the code you checked out instead, run:

```sh
go build -trimpath -o bin/questlock ./cmd/questlock
./bin/questlock worker run --request "$HOME/questlock-workspace/approved-task.json"
```

## Observed result and release limits

A bounded MCP ticket extraction passed strict JSON and exact-value checks with
58 input / 21 output tokens. One exact MCP repeat returned the saved passing
result with zero new usage and no generation. The SDK calls took 5,524.343 ms and
27.584 ms respectively, including transport/provider setup; these are two
observations, not a general speedup or invoice saving.

The broader live local suite completed twelve fresh durable generations and
twelve exact reuses with zero new generation usage. `compact-64` passed 4/6 task
checks; `balanced-256` passed 3/6. No configuration qualified, so the live
evaluation exited 1. A separate feature extraction withheld its failed answer,
including on exact reuse. These observations demonstrate measured reuse and the
quality guard, not general local-model quality or a reliable latency ranking.
See the [content-free measurement record](measurements-v0.3.0.md).

The usage counters cover delegated provider/local generation and exclude the
surrounding hosted assistant's prompt, tool, and context tokens. Request
preparation and result review can still consume its normal allowance. Langfuse
export needs an explicit project, endpoint, and keys; the CI stack uses a separate
disposable project and cleans it up.

Direct OpenAI, Anthropic, and local Ollama remain supported. A capability-aware
OpenRouter companion adapter is future work. Questlock does not train models,
choose them automatically, replace an entire hosted chat, or bypass paid limits.
Native archive and container publication evidence is recorded in the
[validation record](validation.md#v030-observations--2026-10-14).

---

# Questlock 0.2.0 — coordinate paid model work in shared storage

Several agents ask for the same repository handoff or summary. Without shared
coordination, each can pay to generate it again. This experimental release adds
a Go inference library and CLI that plans bounded context, claims a task before
generation, and reuses an explicitly approved, complete, fresh result.

## Included

- A loopback-only local Ollama adapter for installed local model weights, with
  cloud-model rejection, digest-aware reuse, resource/output bounds, and no paid fallback.
- Direct OpenAI Responses and Anthropic Claude Messages adapters with provider
  token preflight, output limits, opt-in prefix caching, and normalized usage.
- Required context preservation and whole optional chunks selected by priority.
- Scoped CAS claims for concurrent workers; unknown outcomes block automatic
  regeneration. Only exact broker publications may be retried.
- Optional per-request cost allowances and a shared UTC-day ledger. Reservations
  use configured input/output allowances and explicit prices; cache writes and
  reasoning are accounted without double charging.
- Known cap violations remain nonreusable; a measured reservation overrun freezes
  that day's ledger when its settlement can be persisted.
- Installable Codex and Claude Code skills from the same portable executable.
- Three simple and three complex offline scenarios, plus the original stale
  worker/restart/retry quest. All use temporary state.
- System and task-lifecycle diagrams with editable draw.io source, showing
  shared-storage ownership, result reuse, and budget/recovery boundaries.
- macOS 13+ and Linux ARM64/x86-64 archives, checksum manifest, docs, example
  specs, and a TLS trust bundle in the Linux container image.

## Try it

```sh
./questlock model quest
./questlock model plan --spec examples/model-workflows/openai-handoff.json
./questlock model install --agents codex,claude
```

The demos and planning need no model account. Live runs require an existing
broker and scoped Questlock credential. Local Ollama runs need installed weights;
cloud runs also need the selected provider's API key.
Read `docs/model-workflows.md` for setup and `docs/model-architecture.md` for
what, why, how, OpenRouter composition, and shared-storage boundaries.

The original broker API and artifact/retry semantics remain available. Published
0.1.0 and 0.1.1 releases are preserved. Legal notices remain included.

## Evidence and limits

Local race tests, fixture HTTP provider tests, publication/packaging regressions,
and both quests validate coordination and recovery behavior. Native CI verifies
all four archive targets before publication; the separate container pipeline
verifies both Linux architectures before publishing its manifest.

The model scenarios demonstrate avoided fixture generations, not measured live
provider savings. Input-count estimates, wrong prices, a provider exceeding the
configured allowance, or inability to persist a budget reconciliation can cause
actual billing to differ. Provider account spending controls remain independent.

This is one trusted broker with local SQLite and cooperative scoped workers.
It is not a full session proxy, semantic cache, distributed database, billing
service, or agent sandbox. OpenRouter is an architectural composition candidate;
no OpenRouter adapter is selectable in this release. Result TTL does not erase
stored history. API billing and subscription usage are separate. The workflow
cannot eliminate credits or train models from daily activity.

macOS packages are not Developer ID signed or notarized. Checksums are not
independent signatures. No credentials or private state are shipped.
