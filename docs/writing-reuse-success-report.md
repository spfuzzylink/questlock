# One writing task. One generation. The repeat used none.

You have the answer already. Asking for the same approved work again should
not need another model run.

On 23 October 2026, **Questlock** ran a real internal release update through
local Gemma 3 12B. The first answer passed its declared checks: **141 words,
two paragraphs**. The exact repeat returned byte-identical text with **zero
new model tokens and no second generation request**.

| MCP call | New input tokens | New output tokens | Generation requests | End-to-end time |
| --- | ---: | ---: | ---: | ---: |
| First | 730 | 195 | 1 | 30,920.61 ms |
| Exact repeat | 0 | 0 | 0 | 72.22 ms |

That is **99.77% lower observed call latency** for this repeat. The first call
included **15.764 seconds of model loading**. This is one first/repeat pair,
with no warm fresh-generation baseline, averages or percentiles.

## Where the win happened

**Questlock** avoided an inference call by coordinating reuse of a completed
result. That saves model work on a valid exact repeat; the model did not have
to produce the same answer faster.

| Layer | What this run establishes |
| --- | --- |
| Inference work | The repeat required no new prompt evaluation or output generation. The original answer used 730 input and 195 output tokens. |
| Coordination | The same approved request and runtime reached the same durable result within its reuse window. Checks ran again, and historical usage stayed separate from new usage. |
| Fresh inference performance | Not established. The inference engine, weights and kernels were unchanged. No warm baseline, TTFT or KV-cache speedup was measured. |
| Concurrent or multi-machine coordination | Not exercised. This run used one worker and one physical machine, with sequential calls. |

The repeat still did work. Provider setup took about **63.7 ms** and the
Runner took about **0.86 ms**, inside the 72.22 ms caller measurement. Those
are different timing scopes, not an isolated coordination-overhead benchmark.

## What we ran and checked

- **Questlock** runtime `0.13.0`, source commit
  [`3b8361a`](https://github.com/spfuzzylink/questlock/commit/3b8361ab95f579a716e6f8663c61dd42237f511d).
- Operator-recorded environment: Ollama `0.40.3`, installed `gemma3:12b`,
  on an Apple M5 Mac with 24 GiB RAM
  and macOS 26.6. No model was loaded before the run; accelerator details were
  not separately qualified. The [data](benchmarks/writing-reuse/observation.json)
  includes the pre-run model digest and metadata sources. The original inventory
  responses were not persisted; machine details were read during report preparation.
- One approved reference and current evidence, both required. Input cap:
  8,192 tokens; output cap: 512; exact-reuse window: 3,600 seconds.
- Non-streaming generation. A literal-loopback counting proxy forwarded to
  the real Ollama server. It counted one `/api/generate` request across the
  pair; provider responses were not simulated.
- A deliberate budget control stopped **3,834 estimated input tokens** against
  a **3,706-token cap**, without generation. Its checks were `not_evaluated`.
  A synthetic 193-word control passed its keyword check and failed the 165-word
  maximum, also without a model call.
- Changed evidence produced a different request identity. Reference byte drift
  stopped planning. Neither variation generated another answer.
- The answer was indexed as assistant reviewed. **Human acceptance and review
  effort remain unobserved.** Passing these checks establishes the declared
  length/phrase contract, not general writing quality or cost per accepted task.

Both calls had **$0 provider API spend**, because they used local weights.
No paid route was tested, so there is no measured dollar-saving comparison.
Hardware, electricity, storage and the surrounding hosted chat remain outside
that figure. Paid fallback stayed disabled throughout.

## Inspect it or try your own task

Read the [exact request](benchmarks/writing-reuse/request.json),
[unchanged answer](benchmarks/writing-reuse/answer.txt) and
[allowlisted receipts and counters](benchmarks/writing-reuse/observation.json).
Private paths, credentials, client settings and invocation identifiers were
omitted. This is operator-published evidence, not an independent attestation.

Verify the published file checksums, matching receipt identities, checks,
accounting and latency arithmetic offline:

```sh
python3 docs/benchmarks/writing-reuse/verify.py
```

That command makes no model call and does not rerun the timing experiment.
It does not independently reconstruct the runtime fingerprint: the transient
proxy endpoint is intentionally omitted.
For a fresh task, follow the [writing walkthrough](../examples/writing/README.md),
review its request, call `questlock_delegate`, then repeat that unchanged request
inside its reuse window. Keep the model, endpoint/runtime identity and policy
fixed; retain failed attempts, too. Results and timing can differ on your setup.

The caller timer covered the MCP call through structured-result receipt,
including provider metadata, broker work, outcome registration and any loading.
It excluded request preparation, caller review and writing-index operations.
Backend durations are separate nanosecond counters, as defined in
[Ollama's usage documentation](https://docs.ollama.com/api/usage).
No Langfuse or SGLang performance claim comes from this run.
