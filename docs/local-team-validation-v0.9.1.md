# Local team validation: v0.9.1

Observed 2026-10-19 on one 24 GiB, 10-logical-CPU Mac, using Ollama 0.40.3 and
three scoped HTTPS worker identities. These are candidate-run observations;
[publication checks](validation.md) separately identify shipped assets.
[Machine-readable evidence](local-team-validation-v0.9.1.json).

**The six original task contracts completed with a local master and both
reviewers.** The unchanged saved Codex baseline reports **65,800 hosted tokens**;
the selected local workflows report **0 hosted tokens and $0 provider API spend**.
Paid fallback stayed disabled. No new hosted baseline was run.

That meets the 99% hosted-token target **for this bounded workflow comparison**.
It does not measure this development chat's allowance, subscription credits,
whole-build usage or hypothetical USD invoices. Human acceptance/rework was not
measured, so cost per human-accepted task remains unknown.

## What actually ran

| Case | Originating model for all three roles | Local input / output tokens | Result |
| --- | --- | --- | --- |
| `ticket` | qwen3.5:4b | 433 / 6,848 | Master + both reviews passed |
| `route` | qwen3.5:4b | 391 / 891 | Master + both reviews passed |
| `queue` | qwen3.5:4b | 376 / 2,549 | Master + both reviews passed |
| `uncertain` | qwen3.5:9b | 697 / 8,227 | Master + both reviews passed |
| `resources` | qwen3.5:9b | 1,005 / 6,039 | Master + both reviews passed |
| `rework` | qwen3.5:9b | 771 / 4,642 | Master + both reviews passed |

The final invocation reused five exact completed cases and generated only the
three `resources` roles: **1,005 input + 6,039 output tokens**.
The originating successes total **3,673 input + 29,196 output
tokens across 18 adapter generation invocations**. Every exact completed
repeat reported zero new generation. A final status read also has zero fresh
usage; it is not the cost of producing the answer.

The three simple origins used the 4B reasoning policy and legacy split framing.
The other origins used 9B policies with canonical user framing. The final
resource master uses the same original task, user prompt and exact answer check,
plus a manual decoder/semantic contract: integer `max_workers` as `quantity`,
string `excess_work` as `policy_action`. This adds generic system guidance and
operator interpretation. It supplies neither answer value; expected answers
are never read into model prompts or reviewer reference data. Reviewers see
only the task, actual candidate and declared types/roles, without each other's
votes. Their fixed approval check remains unchanged.

This is **contract-assisted workflow evidence, not identical-information model
parity**. Policies were iteratively tuned on these same six fixtures. The suite
is not held-out, representative coding evidence or a guarantee of repeatability.
The single-answer baseline also does less review work than the local team.

## Changes that mattered

- macOS admission includes bounded reusable memory, using
  `free + speculative + max(purgeable, reusable)`. An unavailable reusable counter
  contributes zero; malformed successful readings fail closed. Memory headroom,
  pressure thresholds and protected reservations were retained.
- Fresh roles can wait for healthy admission before claiming. Reasoning uses an
  explicitly approved deadline, total output cap and optional model release.
  Generation is never retried after a quality, timeout or uncertain result.
- Direct Ollama can use canonical framing, JSON output, declared primitive types
  and semantic field roles. Sampling defaults remain installed defaults unless
  explicitly overridden. Original semantic checks still reject wrong values.
- JSON-requesting masters give reviewers parsed candidates. Runtime contracts are
  fingerprinted, visible in plans/comparisons and separate from the task binding.
  Nil options preserve old wire bytes, identities and defaults.

A 14B master repeatedly confused policy actions with quantities. The annotated
fast policy then returned the right action with the wrong capacity; its thinking
policy computed capacity correctly but returned a whole sentence. Both failed
the original exact check. The selected 9B policy passed all three gates. This
records observed differences; it does not establish a causal model-size,
sampling, decoder or latency effect.

The daemon already contains the upstream
[single-pass thinking/structured-output change](https://github.com/ollama/ollama/pull/18479).
Removing decoder formatting did not solve the observed case. No daemon upgrade,
active client configuration change, global memory purge or user-app termination
was used to force a result. Installed model weights were deliberately downloaded;
no weights are redistributed in Questlock packages.

## Every deliberate policy remains in the record

All six rows stay in each trial's denominator. Some rows reuse an earlier exact
success; not-attempted cases remain visible in the JSON. A changed policy is an
explicit new execution identity, never a reset of a failed or uncertain claim.

| Policy / admission observation | Completed | New calls | Known local input / output | Unknown-usage calls |
| --- | --- | --- | --- | --- |
| `local-json-memory-1` | 0/6 | 3 | 385 / 111 | 0 |
| `local-json-release-2` | 2/6 | 13 | 1,826 / 471 | 0 |
| `local-json-reasoning-3` | 3/6 | 13 | 1,313 / 12,795 | 3 |
| `local-hybrid-4` | 3/6 | 4 | 337 / 2,661 | 2 |
| `local-deadline-5` | 3/6 | 2 | 207 / 13,141 | 0 |
| `local-7b-6` | 1/6 | 8 | 842 / 133 | 0 |
| `local-canonical-7-before-probe` | 0/6 | 0 | 0 / 0 | 0 |
| `local-canonical-7-after-probe` | 1/6 | 8 | 1,452 / 121 | 0 |
| `local-canonical-9b-8` | 1/6 | 11 | 2,162 / 586 | 0 |
| `local-9b-reasoning-9` | 5/6 | 7 | 1,639 / 15,790 | 0 |
| `local-qwen3-14b-10` | 4/6 | 1 | 170 / 1,610 | 0 |
| `local-qwen3-sampled-12` | 5/6 | 1 | 170 / 350 | 0 |
| `local-gemma12b-13` | 5/6 | 1 | 173 / 16 | 0 |
| `local-native9b-14` | 5/6 | 1 | 1,605 / 414 | 0 |
| `local-typed-contract-15` | 5/6 | 1 | 194 / 1,169 | 0 |
| `local-grounded-contract-16` | 5/6 | 1 | 264 / 2,290 | 0 |
| `local-unconstrained-17` | 5/6 | 1 | 170 / 1,934 | 0 |
| `local-fast-master-18` | 5/6 | 1 | 176 / 20 | 0 |
| `local-semantic-contract-19` | 5/6 | 1 | 330 / 13 | 0 |
| `local-semantic-reasoning-20` | 5/6 | 1 | 324 / 419 | 0 |
| `local-semantic-9b-21` | 6/6 | 3 | 1,005 / 6,039 | 0 |

The compact policy11 proposal was rejected by metadata-only preflight: estimated
input 2,972 exceeded its approved 2,048 cap, with zero generation. The repeated
canonical7 request was still unclaimed when the memory probe was repaired;
its zero-call refusal and later exact request submission are both retained.

Across these current-turn trials **and the separate validation below**, local
usage is at least **16,845 input + 72,183 output tokens**,
with **88 adapter invocations and 5 calls whose local counters remain unknown**.
These totals are lower bounds, not complete local consumption. The earlier
[v0.9.0 failures](local-team-validation-v0.9.0.md), including their unknown counters,
remain separate and unchanged. Local hardware, electricity and elapsed time
are not priced. Output counts include reasoning; no extra reasoning count is
invented. Baseline cache reads are already within input and reasoning within
output, rather than added twice.

## Independent validation: 1 of 2 complete

An independent release reviewer designed two additional contracts. All requests,
model policies and the runtime binary were frozen before their first inference.
They ran once; neither has a Codex baseline or enters the six-case percentage.
They used the separately recorded fast 14B master policy19, not the final 9B
resource policy21.

- `dispatch-numeric-id`: master and both reviewers passed; copied `007`, computed
  12 slots and returned the stated quarantine policy. Exact reuse called no model.
- `invoice-numeric-policy`: master and correctness passed, preserving `00042` and
  numeric-looking policy code `009`, with amount 20. The risk reviewer stopped
  at its 4,096-output-token cap (368 input / 4,096 output). It was not rerun or
  relabeled successful.

Their six calls reported **2,101 input + 12,100 output tokens**,
all known. This retains a concrete reviewer limit alongside the successful
original six. Types and roles do not eliminate semantic or completion failures.

## Reproduce the saved comparison

```sh
questlock team benchmark \
  --suite examples/teams/qualified-suite-v0.9.1.json \
  --local-dir examples/teams/local-observations-v0.9.1 \
  --baseline-dir examples/teams/codex-baseline-v0.9.0
```

This is an offline read of operator-provided historical receipts, with no model
or account access. For new local inference, use the
[walkthrough](../examples/teams/README.md) and keep all failures. Native CLI
compatibility, pinned digests, complete admission configuration and different
machines need their own qualification. Three identities on this Mac do not
constitute physical multi-machine validation.

The JSON pins task/prompt bindings, role policies, receipt hashes and the actual
originating binaries. The historical 4B candidate was built with a modified
working tree; later binary sources were clean. These observations are not remote
attestation. Outer CLI timing for the three simple origins was not retained and
stays null. Nonstreaming elapsed observations establish no TTFT or speedup.

Questlock also applied 34 reviewed, source-hash-pinned literal replacements
across three documents, using six fresh revisions and six exact replays with
durable tracked receipts, zero model calls, zero new
tokens and $0 provider charge. Research, new prose, coding coordination and
independent release review still used the surrounding hosted assistant's normal
allowance; whole-build savings are excluded and unmeasured. Active client
settings and the permanent brand asset remained unchanged.
