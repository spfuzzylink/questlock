# v0.3.0 candidate: local measurements

Observed on 2026-10-14 against the **unpublished v0.3.0 candidate**. These are bounded local observations, not a statistically reliable benchmark or a general model recommendation. No generated answer text, private paths, credentials, or model digests are included.

The runtime completed and durably stored all twelve fresh evaluation results, then reused all twelve exact snapshots with zero new generation usage and unchanged output hashes. **Neither configuration passed every declared quality gate.** The evaluation returned no eligible selection and exited with status 1. Reliable storage and reuse did not make these answers correct.

## Environment and method

- Apple M5, 10 CPUs, 24GiB memory; macOS 26.6 ARM64.
- Go 1.27.1, Ollama 0.40.3, installed local `qwen3.5:4b` weights using MLX/nvfp4.
- The default public suite: three simple and three complex cases; explicit 64-token and 256-token output caps, local streaming enabled.
- One fresh generation and one exact-snapshot reuse per case/configuration in a fresh evaluation scope. All fresh results had complete, durable, known, bounded usage.
- Model warmth, runtime prompt-cache state, and machine load were uncontrolled. The 256-token configuration reported more prompt-cache reads. Execution order and cache state confound latency comparisons.

Runner elapsed time includes planning, coordination, and provider work, with provider setup reported separately. Generation time covers the whole provider `Generate` call. First-content time starts at that call's entry and includes metadata/network overhead. Output-content time spans the first to last nonempty content chunk. Neither streaming metric is pure server compute time. See the [measurement contract](inference-observability.md#read-the-measurements-correctly).

## A passing bounded MCP task

A separate single-tier ticket extraction copied the public values `OPS-17` and
`2030-04-03`. The actual MCP tool call passed strict JSON and exact-value checks,
with one complete, durable generation using **58 input / 21 output tokens**.
The SDK call took **5,524.343 ms**, including transport and provider setup. Its
client-observed first-content and output-content intervals were **4,772.504 ms**
and **589.916 ms**.

One exact repeat in the same private test scope took **27.584 ms** through MCP,
returned the passing saved result, and reported **zero current input/output
tokens** with `generation_called: false`. There was no paid tier, escalation,
or generation retry. This is one accepted extraction and one reuse observation,
not a general speedup, semantic reasoning test, or invoice saving.

The official Go MCP SDK negotiated protocol `2026-07-28` and discovered the sole
`questlock_delegate` tool. Complete candidate Codex settings were parsed by the
native CLI in isolation; Claude Code connected to the candidate worker in an
isolated profile. Those checks did not modify active client configuration or
reroute an existing hosted chat.

## Default suite observations

| Configuration | Cases passing checks | Fresh input/output tokens, summed | Mean Runner ms | Mean reuse ms |
| --- | --- | --- | --- | --- |
| `compact-64` | 4/6 | 551 / 191 | 2,181.499 | 0.589 |
| `balanced-256` | 3/6 | 551 / 155 | 1,040.625 | 0.523 |

| Configuration | Mean generation ms | Mean first-content ms | Mean output-content ms |
| --- | --- | --- | --- |
| `compact-64` | 2,149.769 | 1,189.812 | 892.142 |
| `balanced-256` | 1,001.941 | 185.446 | 754.723 |

Input totals include uncached and cached input, rather than treating a cache read as a second input charge. Each row has six fresh and six reuse observations. Every reuse skipped `Generate`, reported zero new input/output usage, and retained the original usage separately as avoided local computation. No hosted token charge or invoice saving was measured. Local hardware, electricity, memory, and time remain costs.

The lower observed latency for `balanced-256` does not establish a faster or better configuration: it had different cache conditions and fewer passing task checks. These samples provide neither percentiles nor confidence intervals.

## Failed declared checks

| Configuration | Case | Failed check and affected expectation |
| --- | --- | --- |
| `compact-64` | `incident-handoff` | `expected-task-values`: `/unknown` mismatch. |
| `compact-64` | `uncertain-call-recovery` | `expected-task-values`: `/reservation` and `/state` mismatches. |
| `balanced-256` | `reservation-check` | `expected-task-values`: `/admit` mismatch. |
| `balanced-256` | `incident-handoff` | `strict-json` and `expected-task-values`: invalid JSON. |
| `balanced-256` | `uncertain-call-recovery` | `expected-task-values`: `/state` mismatch. |

These deterministic JSON checks are deliberately limited. They identify missed public expectations; they do not measure arbitrary code correctness, semantic accuracy, or general reasoning quality. Increasing the output cap alone did not qualify the model for this suite.

## Worker extraction observation

A local-only public-feature extraction request also failed its declared quality gate. The worker withheld the failed answer and returned `quality_failed`. Repeating the exact approved request reused its durable result, reran the checks, and again returned `quality_failed` without another generation.

| Attempt | Input/output tokens for this run | Runner ms | First-content ms | Output-content ms | Result |
| --- | --- | --- | --- | --- | --- |
| Fresh | 310 / 55 | 2,229.445 | 516.666 | 1,588.533 | `quality_failed`; answer withheld |
| Exact reuse | 0 / 0 | 1.165 | No fresh generation | No fresh generation | `quality_failed`; answer withheld |

`strict-json` passed. `exact-public-feature-facts` failed at `/quality_checks_scope` and `/reuse_generation_spans`. No failed text was exposed as an approved answer and no generation was retried. This validates the guard's refusal for that request; it does not qualify the local model or establish the quality of future tasks.

## Remaining evidence

The self-hosted Langfuse actual-server/CI contract is still pending. Its deterministic metadata fixtures validate observability separately from model quality. Live OpenAI/Anthropic quality, paid-account costs, representative engineering tasks, repeated controlled measurements, and end-to-end hosted assistant behavior remain unevaluated for this candidate. Questlock delegation does not change the surrounding hosted chat's model or allowance.
