# Less repeat work, more room for useful models

Developers pay for repeated context and duplicate generations while several
agents work on the same task. Questlock coordinates a bounded task in shared
storage before starting the paid call.

**OpenRouter is the switchboard. Questlock helps keep a swarm from running up duplicate tabs.**

OpenRouter deserves credit for the common provider interface, model access,
routing, and prompt-cache guidance that inform this architecture. It already has
an exact response cache, so caching a completed answer is not a Questlock
invention. Its documentation describes concurrent identical cache misses as
separate billed calls. Questlock's participating workers first claim a task in
shared storage, so one launches generation while the others defer.
[OpenRouter response caching](https://openrouter.ai/docs/guides/features/response-caching).

## What we can claim

| Task | Questlock's useful behavior | Evidence |
| --- | --- | --- |
| Same approved snapshot requested again | Reuse a complete, fresh stored answer with no new provider generation | Offline fixture: two requests, one generation. Live reuse returns original usage separately and zero new generation usage. |
| Twenty workers request that snapshot together | Claim it before generation so one worker calls the provider | Channel-controlled HTTP/SQLite scenario: one generation, nineteen workers defer. |
| Irrelevant historical material fills the prompt | Select whole optional context chunks under a byte allowance, preserving required context | Demo keeps 212 of 794 context bytes. These bytes are not a measured token-saving percentage. |
| A response is lost or a broker restarts | Retain receipts, completed results, and uncertain claims | Restart/reuse scenario and unknown-outcome reservation test. |
| A task has bounded requirements | Choose a suitable smaller model and explicit output/reasoning limits | Direct adapters accept an explicit model; quality and real bill effects require a representative live evaluation. |

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

## Where OpenRouter is stronger

OpenRouter offers a broad model catalog and managed provider-routing experience.
Questlock currently has direct OpenAI, Claude, and local Ollama adapters, one broker host, and
single-turn text tasks. It does not supply a full chat/session proxy, streaming,
tool execution, provider billing, or an implemented OpenRouter adapter.
The natural combined design is Questlock's task planning/claims/budget state
around a capability-aware router adapter, after counting and usage semantics
are defined and tested. See [the architecture decision](model-architecture.md).

“Better” is therefore specific: Questlock provides durable pre-call coordination
for cooperating workers sharing a task. It is not a universal replacement for
OpenRouter. We make no claim of infinite fresh usage, guaranteed near-zero total
bills, measured customer savings, or patent novelty. The code and six scenarios
are the starting evidence; the next evidence should be live quality and invoice
comparisons for a repeated engineering workflow.
