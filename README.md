<div align="center">

![Questlock — lower cost per accepted task, including human rework](assets/questlock-brand.svg)

[![CI](https://github.com/spfuzzylink/questlock/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/spfuzzylink/questlock/actions/workflows/ci.yml)

[Plugins](docs/plugins.md) · [Get started](docs/onboarding.md) · [Downloads](https://github.com/spfuzzylink/questlock/releases) · [Architecture](docs/architecture.md)

</div>

You already paid for the answer. Why generate it again to change a sentence?

**Questlock** helps agents reuse completed work, make checked literal edits without
a model, and run bounded tasks through local or hosted models. Add CPU/memory-aware
admission and cache-aware SGLang integration when you need them. A Go library and
CLI, with a shared SQLite broker underneath.

**The mission: lower cost per accepted task, including the work a person has to redo.**

## Get going

Already use Codex or Claude Code? Install **Questlock** as a plugin:

Codex / ChatGPT desktop local workflows:

```sh
codex plugin marketplace add spfuzzylink/questlock
codex plugin add questlock@questlock
```

Claude Code:

```sh
claude plugin install questlock --marketplace spfuzzylink/questlock
```

Pick your client, start a fresh chat, and ask it to run the first checked task
and repeat it. No provider key or model download is needed. Git installs fetch
a pinned, checksum-verified runtime on first use; the offline plugin ZIP already
contains it. [Setup, offline bundles and recovery](docs/plugins.md).

Prefer the guided setup, including Claude Desktop? Download the matching
[native archive](https://github.com/spfuzzylink/questlock/releases), then:

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
| Repeated writing with changing facts | Combines an approved reference with today's evidence, checks length and keeps a reviewed answer index. |

[Put a local agent team to work on your own machines](docs/use-cases.md#a-local-agent-team-using-the-machines-you-already-have).

**OpenRouter routes the model. Questlock coordinates the work.** We build alongside
OpenRouter: shared task admission, checked revisions, reusable results and human
feedback around your chosen route. An independent companion, with credit to the
[OpenRouter ecosystem](https://openrouter.ai/). [How they fit](docs/positioning.md).

Local generation has no provider token bill; your machine still does the work.
Hosted OpenAI, Claude and OpenRouter calls use their own billing. Only tasks
routed through Questlock get these controls. This does **not** make the surrounding
Codex/Claude chat free or remove subscription limits. Measure savings against a
matched task baseline, with failed attempts and human rework included.

## Use it with what you already run

Keep your model. Stop repeating work around it. These integrations are opt-in;
each one has a different job.

| You use… | What Questlock adds | Sample / setup |
| --- | --- | --- |
| Your Go app | One coordinator for reuse, local admission, shared token quotas and optional telemetry. | [Go integration](docs/go-integration.md) · [Configuration examples](examples/execution/README.md) |
| OpenRouter | Shared task/result reuse around one explicit model/provider route, with adapter fallbacks disabled. | [Spec](examples/inference/openrouter-summary.json) · [Route limits](docs/runtime-cache.md#openrouter-boundary) |
| OpenAI or Claude API | Bounded text tasks, token-count preflight and optional shared token-price budgets. | [OpenAI](examples/model-workflows/openai-handoff.json) · [Claude](examples/model-workflows/claude-handoff.json) |
| Ollama | Bounded local generation using installed weights, plus exact result reuse. | [Spec](examples/model-workflows/local-handoff.json) · [Local setup](docs/local-models.md) |
| SGLang | Deployment identity, cache namespaces and reported KV counters for standalone dense/HiCache tasks. | [Spec](deploy/sglang/task.json) · [GPU setup and limits](docs/runtime-cache.md#where-hardware-awareness-fits) |
| Langfuse | Opt-in usage/timing traces without prompts or answers. You can see new generation versus reuse. | [Export example](docs/inference-observability.md#opt-in-to-langfuse) |
| TypeSafe System One adapter + Ollama | A local label recommendation or abstention; your code keeps control. Uses ordinary Ollama weights, not Jev weights. | [Request](optional/system-one/reuse.json) · [Setup](docs/system-one.md#prepare-explicitly-then-choose) |
| Codex / ChatGPT desktop, Claude Code or Claude Desktop | Explicit checked-edit and bounded-task MCP tools inside your client. | [Plugins](docs/plugins.md) · [Guided setup / Desktop MCPB](docs/onboarding.md) |
| Workers on your own machines | Scoped HTTPS claims and results through one broker, with durable edge sync. | [Go example](examples/edge/main.go) · [Deployment](docs/local-agent-teams.md) |

<details>
<summary>Show integration code samples</summary>

From a checkout, with `questlock` on your `PATH`. Start with offline plans:

```sh
questlock model plan --spec examples/inference/openrouter-summary.json
questlock model plan --spec examples/model-workflows/openai-handoff.json
questlock model plan --spec examples/model-workflows/claude-handoff.json
questlock model plan --spec examples/model-workflows/local-handoff.json
questlock model plan --spec deploy/sglang/task.json

# Preview setup for an installed client; choose the client you use.
questlock setup --client codex --plan
questlock setup --client claude-code --plan
questlock setup --client claude-desktop --plan
```

After the linked setup steps, these commands contact your configured services.
SGLang inspection generates no answer; the other two commands may run local inference:

```sh
questlock model runtime-inspect --spec deploy/sglang/task.json \
  --api-base-url http://127.0.0.1:30000
questlock system-one choose --request optional/system-one/reuse.json \
  --python /absolute/private/system-one-venv/bin/python
questlock model run --spec examples/model-workflows/local-handoff.json \
  --broker http://127.0.0.1:8080 --langfuse --langfuse-timeout 2s
```

Keep keys in your environment or secret manager. The Langfuse example also needs
a broker token and installed Ollama weights. Review the sample facts before a real
task; paid runs need current explicit prices and caps. Sample model IDs are examples,
not recommendations.

</details>

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

## Keep older work without keeping every payload hot

**Questlock** can move eligible old content into private, compressed local files
while keeping versions, retry receipts, audit history and coordination state.
Reads and exact reuse resolve the original bytes without generating them again.
Choose a hot-history window such as 30, 90 or 180 days; the default is 90.

Retention is opt-in. Preview with `questlock storage archive`; `--apply` moves
content. A configured broker also runs bounded maintenance batches. It deletes
no archive files, and metadata still grows within the configured database page
limit. [Policy, limits and operator walkthrough](docs/storage-retention.md).

## Use it from your Go app

Already have a Go app calling a model? Put the work through one
`execution.Coordinator` for shared result reuse, bounded local admission and
optional Langfuse traces. Keep the provider and model explicit.

```go
coordinator, err := execution.New(ctx, execution.Config{
    Runner: inference.Runner{Broker: broker, Provider: provider},
    Exporter: langfuse, // optional
})
// Handle err and initial admission readiness; see the complete example below.
result, err := coordinator.Run(ctx, approvedSpec)
```

A shared scope/day token quota reserves input, output and total tokens before
fresh generation. Known usage settles the reservation; uncertain calls keep it.
Per-task token limits, call/day dollar caps and local resource settings come from
explicit configuration. CPU-only runtimes use the same coordination API.

[Complete Go example and configuration](docs/go-integration.md) ·
[Runnable example](examples/execution/README.md) ·
[Measured overhead and reuse](docs/execution-validation-v0.10.0.md)

Live local check: one generation used **57 input + 20 output tokens**; the exact
repeat returned the same answer with **0 new tokens and no second generation**.
The shared quota settled to actual usage; paid fallback stayed disabled.

## Local quality evidence

Local teams use explicit JSON types and field roles, bounded reasoning, model
release and better macOS memory admission. Wrong answers still stop at the
original checks; paid fallback stays disabled.

In the published six-task check, local masters and both reviewers completed
the original task contracts. The same saved Codex baseline reports **65,800 hosted tokens**;
the selected local workflows use **0 hosted tokens and $0 provider API spend**.
That observation includes five exact case reuses and one fresh three-role run,
with extra operator guidance on that case. Whole-chat credit savings and general
coding quality remain unmeasured.

[Results, failures and limits](docs/local-team-validation-v0.9.1.md) ·
[Run a local team](docs/local-agent-teams.md) ·
[Inspect the saved comparison without a model](examples/teams/README.md)

A real writing check produced **141 words in two paragraphs**. Its exact repeat
used **0 new model tokens** and returned the same answer in **72 ms**. The first
call took **30.9 seconds**, including model loading.
[Inputs, receipts and what this one observation proves](docs/writing-reuse-success-report.md).

## Go deeper

- [Release notes](https://github.com/spfuzzylink/questlock/releases) · [Measured build report](docs/success-report-v0.7.0.md)
- [Checked revisions](docs/checked-revisions.md) · [Bounded delegation](docs/delegation.md)
- [Model workflows](docs/model-workflows.md) · [Acceptance and human rework](docs/outcomes.md)
- [Carry a writing workflow into another chat](docs/writing-workflows.md) · [Synthetic example](examples/writing/README.md)
- [Langfuse and timings](docs/inference-observability.md) · [SGLang/KV cache integration](docs/runtime-cache.md)
- [Containers](docs/containers.md) · [API](docs/api.md) · [Validation](docs/validation.md)
- [SecondGate: durable edge push and pull](docs/second-gate.md)
- [Contribute](CONTRIBUTING.md) · [Security](SECURITY.md) · [MIT license](LICENSE) · [Credits](ATTRIBUTION.md)

Experimental. One authoritative broker on one host; no automatic multi-writer
replication or demonstrated multi-host scale. The broker controls artifact
publication; it does not execute or sandbox agents. See the
[engineering boundaries](docs/architecture.md).
