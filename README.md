<div align="center">

![Questlock — lower cost per accepted task, including human rework](assets/questlock-brand.svg)

[![CI](https://github.com/spfuzzylink/questlock/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/spfuzzylink/questlock/actions/workflows/ci.yml)

[Get started](docs/onboarding.md) · [Downloads](https://github.com/spfuzzylink/questlock/releases) · [Architecture](docs/architecture.md) · [Questions](https://github.com/spfuzzylink/questlock/discussions/categories/q-a)

</div>

You already paid for the answer. Why generate it again to change a sentence?

**Questlock** helps agents reuse completed work, make checked literal edits without
a model, and delegate small tasks to a local model. A Go library and CLI, with a
shared SQLite broker underneath.

**The mission: lower cost per accepted task, including the work a person has to redo.**

## Get going

Download and extract the matching [v0.7.0 archive](https://github.com/spfuzzylink/questlock/releases/tag/v0.7.0), then:

```sh
./questlock setup
```

Pick Codex, Claude Code, or Claude Desktop. Setup runs a first task with **zero
model calls**. Codex and Claude Code get guarded configuration and a workflow
skill. Desktop gets a native extension bundle to install in its settings.
No provider key or model download is needed for that first task.

From a fork or checkout:

```sh
./scripts/install.sh
./bin/questlock setup
```

Already have the binary? `questlock try` runs just the first task;
`questlock doctor --client codex` checks local configuration. The broker stays
running for your next request; `questlock local stop` stops it and keeps your data.

[Setup, recovery and all three clients](docs/onboarding.md) ·
[Source builds and requirements](docs/installation.md)

To run the **main coding loop locally**, after installing Codex CLI, Ollama and
local weights:

```sh
./questlock local-agent --model qwen3.5:4b --workspace "$PWD" \
  --prompt 'Read the README and list the setup commands.'
```

Read-only by default, with no imported provider credentials or paid fallback.
[Local-agent details and quality limits](docs/onboarding.md#run-the-main-codex-agent-locally).

## What it actually does

| When you need… | Questlock does… |
| --- | --- |
| The same completed answer | Reuses an exact, explicitly approved task snapshot. |
| A known text or JSON change | Pins the source hash, applies counted edits, checks and publishes the result. No model call. |
| A small new task | Runs a bounded request through local Ollama or a configured provider, with declared checks. |
| Multiple agents on one task | Coordinates claims and rejects stale writes; exact publish retries recover their original receipt. |
| Evidence of a win | Records usage and timing, plus explicit acceptance or later human rework. |

**OpenRouter routes the model. Questlock coordinates the work.** We build alongside
OpenRouter: shared task admission, checked revisions, reusable results and human
feedback around your chosen route. An independent companion, with credit to the
[OpenRouter ecosystem](https://openrouter.ai/). [How they fit](docs/positioning.md).

Local generation has no provider token bill; your machine still does the work.
Hosted OpenAI, Claude and OpenRouter calls use their own billing. Only tasks
routed through Questlock get these controls. This does **not** make the surrounding
Codex/Claude chat free or remove subscription limits. The target is 99% less
hosted usage on suitable workflows; that is a measurement goal, not a shipped
whole-chat savings claim.

## Try before connecting anything

```sh
./questlock quest                 # crashes, stale writes and safe retries
./questlock revise quest          # three simple + three complex checked edits
./questlock model quest           # shared work and exact result reuse
./questlock outcome quest         # acceptance, rejection and human rework
./questlock model runtime-quest   # runtime contracts, using HTTP fixtures
```

These demos use temporary state and no model account. Local inference needs
installed Ollama weights. Paid generation requires explicit credentials and
policy; guided setup enables no paid escalation.

## New in the 0.8 source candidate

Queue edge writes safely, share accepted results, and admit local work according
to CPU and memory headroom. Optional typed decisions use the official TypeSafe
adapter with local Ollama. [Your policy is explicit configuration](examples/preferences/README.md),
including a local-only preset; forks choose their own preferences.
[Design and limits](docs/local-coordination.md).

## Go deeper

- [What changed in 0.7.0](docs/release-notes.md) · [Measured build report](docs/success-report-v0.7.0.md)
- [Checked revisions](docs/checked-revisions.md) · [Bounded delegation](docs/delegation.md)
- [Model workflows](docs/model-workflows.md) · [Acceptance and human rework](docs/outcomes.md)
- [Langfuse and timings](docs/inference-observability.md) · [SGLang/KV cache integration](docs/runtime-cache.md)
- [Containers](docs/containers.md) · [API](docs/api.md) · [Validation](docs/validation.md)
- [SecondGate: durable edge push and pull](docs/second-gate.md)
- [Contribute](CONTRIBUTING.md) · [Security](SECURITY.md) · [MIT license](LICENSE) · [Credits](ATTRIBUTION.md)

Experimental. One authoritative broker on one host; no automatic multi-writer
replication or demonstrated multi-host scale. The broker controls artifact
publication; it does not execute or sandbox agents. See the
[engineering boundaries](docs/architecture.md).
