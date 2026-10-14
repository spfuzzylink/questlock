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
