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

Download and extract the matching [v0.9.0 archive](https://github.com/spfuzzylink/questlock/releases/tag/v0.9.0), then:

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
Codex/Claude chat free or remove subscription limits. Measure savings against a
matched task baseline, with failed attempts and human rework included.

## Try before connecting anything

```sh
./questlock quest                 # crashes, stale writes and safe retries
./questlock revise quest          # three simple + three complex checked edits
./questlock model quest           # shared work and exact result reuse
./questlock outcome quest         # acceptance, rejection and human rework
./questlock model runtime-quest   # runtime contracts, using HTTP fixtures
./questlock edge quest           # durable edge intent and accepted-state sync
./questlock resources quest      # bounded queues and pressure-aware admission
./questlock team quest           # three scoped HTTPS workers; scripted providers
```

These demos use temporary state and no model account. Local inference needs
installed Ollama weights. Paid generation requires explicit credentials and
policy; guided setup enables no paid escalation.

## New in 0.9.0

A local master, two independent reviewers, and one shared checked result.
Run bounded text tasks through installed Ollama or the native Codex OSS harness;
hand roles between machines over an authenticated HTTPS broker. Paid fallback
stays disabled. Try the model-free demo with `questlock team quest`.

[Run a local team](docs/local-agent-teams.md) ·
[Six matched tasks and honest results](docs/local-team-validation-v0.9.0.md)

The tested small model did not complete the live team suite. The 99% hosted-usage
target remains unproven; failures and memory refusals are included in the report.

## Go deeper

- [What changed in 0.9.0](docs/release-notes.md) · [Measured build report](docs/success-report-v0.7.0.md)
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
