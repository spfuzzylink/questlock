# Runtime cache integration

Questlock v0.5.0 experimental connects bounded task execution to an explicitly
identified SGLang deployment and adds a dedicated OpenRouter adapter. These are
separate serving contracts, not interchangeable base-URL substitutions.

The useful boundary is **which work can be reused, under which identity, with
which evidence**. Questlock coordinates whole results, quality checks, budgets,
and [human acceptance/rework](outcomes.md). SGLang owns runtime KV tensors and
their placement. OpenRouter owns its provider routing and account billing.

## Three kinds of reuse

| Mechanism | What is reused | Evidence |
|---|---|---|
| Questlock completed-result reuse | A completed answer for the same approved task snapshot | Durable record, matching fingerprint, TTL; zero new generation usage |
| SGLang HiCache | Prefix KV across device, host and optional storage tiers | Runtime-reported cache counters; a new generation still occurs |
| SGLang HiSparse | A decode request's KV in host memory, with a device working set | Supported model/kernel and a working prefill/decode deployment; not an answer cache |

HiCache can help repeated-prefix prefill workloads. HiSparse targets decode
memory pressure for supported DeepSeek Sparse Attention / DeepSeek V4 models.
The pinned upstream requires HiSparse on a PD decode instance with radix caching
disabled. HiCache and HiSparse cannot be enabled in the same engine under that
configuration. Read the pinned [HiCache configuration](https://github.com/sgl-project/sglang/blob/94602c9c2b7cbdb8efd5c52802dac6a1c180089e/python/sglang/srt/arg_groups/hicache_hook.py)
and [HiSparse constraints](https://github.com/sgl-project/sglang/blob/94602c9c2b7cbdb8efd5c52802dac6a1c180089e/python/sglang/srt/arg_groups/hisparse_hook.py).

## Implemented SGLang boundary

Dense and HiCache profiles on a standalone, non-PD runtime support bounded,
nonstreaming native generation.
The adapter inspects the runtime, tokenizes the exact prepared prompt, and sends
the resulting token IDs to the native generation endpoint. It checks the
declared profile and served model; the observed runtime/model/tokenizer identity
also separates durable results. Change `deployment_id` when weights, tokenizer,
runtime build, or relevant serving configuration change. That ID is an operator
assertion, not a remote attestation or a substitute for pinning model files.

Use `model runtime-inspect` to read capabilities without generation. A declared
HiSparse profile is **inspection only** in this release. A direct PD decode
request needs bootstrap/transfer metadata; Questlock does not yet orchestrate
that ingress. It rejects direct HiSparse generation rather than sending an
ordinary token-ID request to the decode node. [Deployment examples](../deploy/sglang/README.md)
include the upstream PD prerequisites for a future live qualification.
Dense/HiCache instances in prefill or decode disaggregation mode are also
inspection-only until a validated PD ingress is supported.

`cache_namespace` is hashed with `deployment_id` to form SGLang `cache_salt` and
participates in Questlock's request identity. It separates cache keys; it grants no access control. Keep
broker scopes, runtime authentication, backend credentials, and storage ACLs as
separate boundaries. HiCache L1/device and L2/host pools belong to each runtime
instance; only a configured L3 backend can share across instances. A matching
salt alone does not make incompatible model KV safe to share. See upstream's
[tier-sharing guidance](https://github.com/sgl-project/sglang/blob/94602c9c2b7cbdb8efd5c52802dac6a1c180089e/docs/docs/advanced_features/hicache_best_practices.mdx).

Reported KV hits remain runtime metrics, not billable API cache tokens. They do
not reduce total prompt token accounting or establish dollar savings.
`self_hosted: true` declares an operator-owned runtime with no provider token
invoice handled by Questlock; GPU rental, hardware, electricity, storage, and
operations still cost resources. A metered third-party SGLang service needs a
separate billing contract and should not use this declaration.

## OpenRouter boundary

Use `provider: "openrouter"` with an explicit `openrouter` object:

```json
{
  "upstream_provider": "openai",
  "input_token_upper_bound": 2048,
  "cache_write_usage": "not_applicable"
}
```

The adapter submits one text-only Chat Completions request with one model,
`provider.only` and `provider.order` set to the declared provider,
`allow_fallbacks: false`, `require_parameters: true`, and a zero per-request fee
ceiling. Questlock sends no retry, model list, tools, or routing variants. These
are requested upstream controls, not a guarantee about OpenRouter's internals.
The current response check accepts base provider slugs whose display name
normalizes to that slug; endpoint/region suffix pins are unsupported.
[OpenRouter documents the routing controls](https://openrouter.ai/docs/guides/routing/provider-selection).

OpenRouter does not expose a documented general preflight count endpoint for
this adapter. `input_token_upper_bound` is an explicit operator reservation
bound, not a tokenizer measurement. It must fit the task input cap; a reported
overrun is detected after generation and cannot undo its charge. Choose the
bound against the actual model/tokenizer and preserve realistic caps.

Known native prompt/output totals and cache-read counters are required.
`cache_write_usage: "required"` also requires an explicit write counter;
`"not_applicable"` permits an omitted zero write bucket only for a route with
no separate cache-write charge. Contradictory or disjoint counters fail closed.
Reasoning is already included in completion tokens. Unknown usage stops the
job and is never converted to zero or automatically retried. See
[OpenRouter usage accounting](https://openrouter.ai/docs/cookbook/administration/usage-accounting).

The [example spec](../examples/inference/openrouter-summary.json) is for offline
planning. Before a paid run, add current explicit prices and per-call/day caps;
load `OPENROUTER_API_KEY` through your secret manager. Worker paid tiers also
need process `--allow-paid`. Questlock's dollar ledger uses caller-supplied
token prices; it is not a verified OpenRouter invoice. Streaming, explicit
prefix-cache controls, and reasoning-effort overrides are not supported by
this adapter. Worker endpoints are process-level `--openrouter-url` and
`--sglang-url` settings, never fields supplied by an MCP request.

## Evidence levels and the next qualification

| Environment | What it can establish | What remains unproved |
|---|---|---|
| Questlock `model runtime-quest` | Six deterministic HTTP-fixture scenarios, including cache tiers and fail-closed identity/usage handling | Model quality, GPU kernels, throughput and bill savings |
| Linux ARM64 VM | Linux userspace and ARM64 runtime/package compatibility | Apple GPU access or CUDA/ROCm behavior |
| AMD64 QEMU emulation | AMD64 userspace/ABI paths on another CPU architecture | GPU emulation, realistic accelerator timing or memory traffic |
| Upstream SGLang CPU simulator | Scheduler/cache behavior with predicted forward latency | Actual generated answers, kernel correctness or measured GPU speedup |
| Native Apple MLX | Real local inference using supported Apple hardware and models | HiSparse CUDA/ROCm paths or equivalence to a Linux GPU deployment |
| Matched real CUDA/ROCm deployment | Actual outputs, cache behavior, memory and timing on that hardware | Transferability to another model, hardware or workload |

SGLang's [Apple Metal guide](https://github.com/sgl-project/sglang/blob/94602c9c2b7cbdb8efd5c52802dac6a1c180089e/docs/docs/hardware-platforms/apple_metal.mdx)
describes a separate MLX backend. An emulator or Linux VM cannot provide the
PD bootstrap, supported sparse kernels, or real host/device transfer evidence.

For a live comparison, hold model and tokenizer digests, runtime build, prompt
tokens, sampling, output cap, concurrency and hardware fixed. Start each cold
sample with an isolated empty cache; repeat the identical prefix for the warm
sample. Disable Questlock result reuse for this runtime comparison. Run both
dense and HiCache profiles in alternating order over multiple samples, record
failures and accepted/reworked outcomes, then report distributions and hardware
cost alongside tokens. Do not combine a simulated timing result with a live
quality result as if they came from one measured run. No GPU performance or
quality win is claimed by the protocol fixtures in this release.
