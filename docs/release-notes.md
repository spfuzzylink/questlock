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
