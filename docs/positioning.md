# Questlock: a companion layer for model routing

Developers pay for repeated context and duplicate generations while several
agents work on the same task. Questlock coordinates a bounded task in shared
storage before starting the paid call.

**OpenRouter routes the model. Questlock coordinates the work.**

OpenRouter deserves credit for the common provider interface, model access,
routing, and prompt-cache guidance that inform this architecture. Its exact
response cache is part of that foundation. Questlock's companion role brings
approved context, task admission, durable reuse, and budget coordination around
the selected provider path. Participating workers claim a task in shared storage
before generation, while related workers can recover the same valid result.
[OpenRouter response caching](https://openrouter.ai/docs/guides/features/response-caching).

**OpenRouter is the switchboard. Questlock helps keep a swarm from running up duplicate tabs.**

Use “OpenRouter companion adapter” in v0.5 launch copy. This experimental release
selects OpenRouter through its own strict text protocol, one explicit
model/provider route, an operator-declared input reservation bound, and known
token/cache accounting. It is a standalone adapter, not an OpenRouter-hosted
plugin or endorsed partnership. Tagged publication is verified; live paid routing/billing
validation remains pending. Earlier v0.2–v0.4 releases retain their direct
OpenAI, Anthropic and Ollama integration history.

**v0.4.0 experimental** adds explicit downstream human
feedback. It makes check success and human acceptance separate measurements,
with versioned acceptance/rewriting/rejection/reopening events and a declared
cohort report. Every rewrite removes an outcome from the accepted denominator
until explicit reacceptance. The numerator retains all recorded attempts and
reported human effort, including failed and rejected work. Missing accounting
stays unknown. See [what this measures and excludes](outcomes.md).

The v0.5 release also separates **task reuse from runtime KV reuse**.
Standalone SGLang dense/HiCache engines can generate bounded tasks; HiSparse and
PD profiles can be inspected but need a future validated ingress for generation.
SGLang's KV tiers are its implementation, credited to its upstream team.
Questlock adds explicit deployment identity, shared task coordination and
content-free runtime observations around that serving boundary. A cache hit is
not evidence of zero fresh tokens, improved quality, or a lower provider bill.
See [contracts and evidence levels](runtime-cache.md).

Use “cost per currently human-accepted outcome in the recorded cohort” only when
those human signals and required cost/effort inputs are present. Use “cost per
check-passing attempt” for declared-check observations alone. Neither phrase
establishes invoice savings or total business cost; the report does not watch
editor rewrites automatically or infer a person's acceptance.

## Evidence developers can reproduce

| Task | Questlock's useful behavior | Evidence |
| --- | --- | --- |
| Same approved snapshot requested again | Reuse a complete, fresh stored answer with no new provider generation | Offline fixture: two requests, one generation. Live reuse returns original usage separately and zero new generation usage. |
| Twenty workers request that snapshot together | Claim it before generation so one worker calls the provider | Channel-controlled HTTP/SQLite scenario: one generation, nineteen workers defer. |
| Irrelevant historical material fills the prompt | Select whole optional context chunks under a byte allowance, preserving required context | Demo keeps 212 of 794 context bytes. These bytes are not a measured token-saving percentage. |
| A response is lost or a broker restarts | Retain receipts, completed results, and uncertain claims | Restart/reuse scenario and unknown-outcome reservation test. |
| A task has bounded requirements | Declare a model, output/reasoning limits, and task checks | The local live suite qualified neither 64/256-token configuration; the worker withheld a failed extraction. Passing reuse counters did not make answers correct. |
| Developers need to inspect a repeated task | Separate Runner phases, current usage, historical timing, and optional metadata export | [v0.3.0 measured observations](measurements-v0.3.0.md); [content-free Langfuse contract](inference-observability.md), verified by actual-server CI readback. |
| A new generation reuses runtime prefix KV | Keep runtime tier counters distinct from API billing and whole-answer reuse | Six v0.5 HTTP-fixture scenarios; live GPU performance remains unqualified. |
| A runtime restarts or changes configuration | Stop an existing provider before reusing its old identity | Deterministic runtime-mutation fixture; no weight attestation is implied. |

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

The combined design places Questlock's task planning, claims, receipts,
and budget state around a capability-aware OpenRouter adapter. OpenRouter supplies
the model-access and provider-routing layer. See
[the add-on architecture](model-architecture.md#openrouter-inspiration-and-possible-add-on).

The current boundary is one broker host and bounded single-turn text tasks.
Local content streaming and a bounded MCP delegation tool remain available.
Broader native agent-tool orchestration, full-session interception, SGLang PD
ingress, and invoice reconciliation remain separate work. The
developer-facing aim is faster, affordable, measurable repeat tasks; the next
evidence should qualify representative task quality under controlled settings
and measure invoice effects for paid work. Keep public copy tied
to executed checks: no infinite fresh usage, guaranteed near-zero total bills,
unmeasured customer savings, or patent-novelty claims.

The surrounding hosted assistant chat keeps its model, allowance, and billing.
Questlock usage counters cover only delegated provider/local generation, excluding
the hosted assistant's prompt, tool, and context tokens. Creating a request and
reviewing the result can still consume those hosted tokens.
During v0.5 development, one recorded local attempt used 75 input / 17 output
tokens. Its human outcome remains unreviewed. That is measured delegated usage,
not evidence of accepted work or reduced surrounding hosted-chat charges.
Questlock neither trains weights nor selects models automatically. An explicit
quality-gated escalation is a declared policy, with at most one next tier after
a confirmed complete durable answer fails its checks; uncertain outcomes stop.
