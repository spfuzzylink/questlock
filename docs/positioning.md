# Questlock: a companion layer for model routing

Developers pay for repeated context and duplicate generations while several
agents work on the same task. Questlock coordinates a bounded task in shared
storage before starting the paid call.

**OpenRouter routes the model. Questlock coordinates the work.**

OpenRouter deserves credit for the common provider interface, model access,
routing, and prompt-cache guidance that inform this architecture. Its exact
response cache is part of that foundation. Questlock's intended add-on role brings
approved context, task admission, durable reuse, and budget coordination around
the selected provider path. Participating workers claim a task in shared storage
before generation, while related workers can recover the same valid result.
[OpenRouter response caching](https://openrouter.ai/docs/guides/features/response-caching).

**OpenRouter is the switchboard. Questlock helps keep a swarm from running up duplicate tabs.**

Use “companion layer” or “planned OpenRouter add-on” in launch copy. Version 0.2.0
introduced direct OpenAI, Claude, and local Ollama adapters. Version **v0.3.0** adds measurements, optional Langfuse observability, evaluation,
and explicit local-first Go/MCP delegation; an OpenRouter plugin or adapter is
not selectable yet. The integration needs a tested contract for
counting, routing policy, output limits, and billed usage before it can ship.

## Evidence developers can reproduce

| Task | Questlock's useful behavior | Evidence |
| --- | --- | --- |
| Same approved snapshot requested again | Reuse a complete, fresh stored answer with no new provider generation | Offline fixture: two requests, one generation. Live reuse returns original usage separately and zero new generation usage. |
| Twenty workers request that snapshot together | Claim it before generation so one worker calls the provider | Channel-controlled HTTP/SQLite scenario: one generation, nineteen workers defer. |
| Irrelevant historical material fills the prompt | Select whole optional context chunks under a byte allowance, preserving required context | Demo keeps 212 of 794 context bytes. These bytes are not a measured token-saving percentage. |
| A response is lost or a broker restarts | Retain receipts, completed results, and uncertain claims | Restart/reuse scenario and unknown-outcome reservation test. |
| A task has bounded requirements | Declare a model, output/reasoning limits, and task checks | The local live suite qualified neither 64/256-token configuration; the worker withheld a failed extraction. Passing reuse counters did not make answers correct. |
| Developers need to inspect a repeated task | Separate Runner phases, current usage, historical timing, and optional metadata export | [v0.3.0 measured observations](measurements-v0.3.0.md); [content-free Langfuse contract](inference-observability.md), verified by actual-server CI readback. |

The cheapest repeated generation is the one you can safely skip. A high volume
of unchanged snapshot reads can have **zero additional model-generation cost**.
The original generation, storage, broker operation, counting calls where
applicable, and fresh tasks still have their own costs. Reuse does not create
new reasoning, fix stale source material, or eliminate provider usage limits.

Local Ollama weights add a practical route for fresh routine tasks with no hosted
provider token meter. Approved preferences travel as compact required context;
that is personalization through data, not automatic weight training or a copy
of a person's brain. Hardware, electricity, runtime, and quality limits remain.
[Local setup and evidence](local-models.md).

## Complementary by design

The natural combined design places Questlock's task planning, claims, receipts,
and budget state around a capability-aware OpenRouter adapter. OpenRouter supplies
the model-access and provider-routing layer. See
[the add-on architecture](model-architecture.md#openrouter-inspiration-and-possible-add-on).

The current boundary is one broker host and bounded single-turn text tasks.
Local content streaming and a bounded MCP delegation tool are implemented in
v0.3.0. Broader native agent-tool orchestration, full-session interception,
router integration, and invoice reconciliation remain separate work. The
developer-facing aim is faster, affordable, measurable repeat tasks; the next
evidence should qualify representative task quality under controlled settings
and measure invoice effects for paid work. Keep public copy tied
to executed checks: no infinite fresh usage, guaranteed near-zero total bills,
unmeasured customer savings, or patent-novelty claims.

The surrounding hosted assistant chat keeps its model, allowance, and billing.
Questlock usage counters cover only delegated provider/local generation, excluding
the hosted assistant's prompt, tool, and context tokens. Creating a request and
reviewing the result can still consume those hosted tokens.
Questlock neither trains weights nor selects models automatically. An explicit
quality-gated escalation is a declared policy, with at most one next tier after
a confirmed complete durable answer fails its checks; uncertain outcomes stop.
