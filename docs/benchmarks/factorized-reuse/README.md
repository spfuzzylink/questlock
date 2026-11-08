# Before we trust a saved answer

This snapshot ends at the audited 472-attempt development boundary. Later
qualification needs its own retained evidence.

Two people can ask different questions and need the same answer. They can also
mention the same topic while needing different answers. Changed permissions,
deadlines, amounts and output formats matter.

These development runs cover six kinds of work: support permissions, employee
onboarding, incident handoffs, release decisions, refund calculations and broker
recovery. The fixtures include narrower questions and changed constraints. They
are test populations, not question lists required by the product.

None of the six configurations below qualified. Keeping their misses and costs
helps explain what still needs work.

| Run | Valid requests served | Invalid requests served | Unscored requests | New generations | Cached source checks |
| --- | ---: | ---: | ---: | ---: | ---: |
| [Initial two-stage regression](guard4-r3.json) | 65 / 71 | 2 / 73 | 0 | 157 | 122 |
| [Compact applicability check](compact-pilot.json) | 8 / 12 | 0 / 8 | 0 | 28 | 9 |
| [Discarded evidence-quote prototype](evidence-pilot-discarded.json) | 1 / 12 | 0 / 8 | 3 | 28 | 9 |
| [Same smaller model for both stages](same-model-pilot.json) | 5 / 12 | 0 / 8 | 0 | 28 | 9 |
| [Smaller model with pair reasoning](thinking-pilot.json) | 0 / 12 | 0 / 8 | 18 | 28 | 9 |
| [Fixed sampled pair reasoning, stop on execution error](sampled-thinking-pilot.json) | 0 / 12 | 0 / 8 | 20 | 2 | 0 |

The table uses the whole declared population. Unscored work is still in those
denominators; it cannot be counted as a correct rejection. The reports also give
metrics for scored decisions and their confidence intervals. One compact-pilot
response was contradictory and normalized to `unknown`; withholding it did not
make that pilot pass. The first pair-reasoning run exhausted its output cap on all
18 question checks. The sampled successor stopped after its first capped question
check and left 19 requests unattempted. Their receipts retain usage, but contain
no final answer or reasoning trace that could establish why they failed.

Across these runs, 158 source-certificate lookups reused completed receipts with
zero current generation tokens. There were also 271 new generations, including
22 incomplete ones. All their measured costs remain in the reports. The
separately audited earlier boundary retains another 201 attempts, including one
incomplete result and five unknown outcomes. That is 472 attempts in total and
28 unfinished execution fences; no claim was cleared to get another try.

Paid fallback was disabled and provider API spend was $0. Local hardware and time
still have costs. These runs checked answer validity; they did not generate
fresh comparison answers or measure dollar savings or reduced Codex allowance.
Token usage stays separated by model. A sum across tokenizers is useful for
reservation accounting, not a normalized savings figure.

Run `python3 docs/benchmarks/factorized-reuse/verify.py` from the repository root
to check the pinned fixtures, per-row counts, per-model usage and cached work
offline. [The index](index.json) records the distinct prompts, schemas, source
files, binaries and runtimes. The earlier 201-attempt boundary comes from its
retained independent audit; these six report files do not recompute those older
receipts. Replaying the arithmetic does not certify model reasoning or turn
already-consumed data into a fresh holdout.

See [the experimental two-stage policy](../../factorized-reuse.md) for the API
and admission controls. Exact task reuse keeps its existing path.
