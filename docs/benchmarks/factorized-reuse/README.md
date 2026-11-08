# Before we trust a saved answer

This snapshot ends at the audited 547-attempt development boundary. Later
qualification needs its own retained evidence.

Two people can ask different questions and need the same answer. They can also
mention the same topic while needing different answers. Changed permissions,
deadlines, amounts and output formats matter.

These development runs cover six kinds of work: support permissions, employee
onboarding, incident handoffs, release decisions, refund calculations and broker
recovery. The fixtures include narrower questions and changed constraints. They
are test populations, not question lists required by the product.

These are validity diagnostics using fixed source answers. They do not measure
fresh answer generation or alias publication; the matched workflow benchmark
needs separate evidence for those paths.

Eight runs failed; the latest pilot passed all 20 requests. The larger regression
and fresh independent qualification remain separate gates. Keeping every run's
misses and costs helps explain what still needs work.

| Run | Valid requests served | Invalid requests served | Unscored requests | Generation attempts | Cached source checks |
| --- | ---: | ---: | ---: | ---: | ---: |
| [Initial two-stage regression](guard4-r3.json) | 65 / 71 | 2 / 73 | 0 | 157 | 122 |
| [Compact applicability check](compact-pilot.json) | 8 / 12 | 0 / 8 | 0 | 28 | 9 |
| [Discarded evidence-quote prototype](evidence-pilot-discarded.json) | 1 / 12 | 0 / 8 | 3 | 28 | 9 |
| [Same smaller model for both stages](same-model-pilot.json) | 5 / 12 | 0 / 8 | 0 | 28 | 9 |
| [Smaller model with pair reasoning](thinking-pilot.json) | 0 / 12 | 0 / 8 | 18 | 28 | 9 |
| [Fixed sampled pair reasoning, stop on execution error](sampled-thinking-pilot.json) | 0 / 12 | 0 / 8 | 20 | 2 | 0 |
| [Larger model with sampled pair reasoning](pair14-thinking-pilot.json) | 11 / 12 | 1 / 8 | 0 | 28 | 9 |
| [Relation guard and source-binding clarification](relation-deadline-pilot.json) | 8 / 12 | 0 / 8 | 7 | 20 | 5 |
| [Explicit four-minute shared budget: passing pilot](explicit-deadline-pilot.json) | 12 / 12 | 0 / 8 | 0 | 27 | 9 |

The table uses the whole declared population. Unscored work is still in those
denominators; it cannot be counted as a correct rejection. The reports also give
metrics for scored decisions and their confidence intervals. One compact-pilot
response was contradictory and normalized to `unknown`; withholding it did not
make that pilot pass. The first pair-reasoning run exhausted its output cap on all
18 question checks. The sampled successor stopped after its first capped question
check and left 19 requests unattempted. Their receipts retain usage, but contain
no final answer or reasoning trace that could establish why they failed.

The larger model completed its calls, but reused a desk-tray refund calculation
when the request switched to returning the lamp. It also withheld a valid pickup
question. Completed JSON and reasoning output did not establish correctness.

The relation successor made 13 correct decisions before an unconfirmed pair
result at the configured 120-second deadline boundary stopped the run. Six
requests were never attempted. Those seven unscored requests prevent
qualification. The receipt cannot establish the transport cause or final upstream
outcome; the unknown job remains fenced.

With an explicitly selected four-minute budget, the next pilot completed all 20
requests correctly. One verification took 134.6 seconds; its pair generation used
1,515 output tokens including reasoning. Context and output caps stayed fixed.
This new invocation cannot settle or recover the older unknown job. The ordinary
two-minute thinking default stays unchanged; longer waits require caller choice.

Across these runs, 181 source-certificate lookups reused completed receipts with
zero current generation tokens. There were also 346 generation attempts: 323
complete with known usage, 22 incomplete with known usage, and one with unknown
usage. Known-token subtotals exclude that unknown call; they do not value it at
zero. All measured costs and the unknown job identity remain in the reports. The
separately audited earlier boundary retains another 201 attempts, including one
incomplete result and five unknown outcomes. That is 547 attempts in total and
29 unfinished execution fences; no claim was cleared to get another try.

Paid fallback was disabled and provider API spend was $0. Local hardware and time
still have costs. These runs checked answer validity; they did not generate
fresh comparison answers or measure dollar savings or reduced Codex allowance.
Token usage stays separated by model. A sum across tokenizers is useful for
reservation accounting, not a normalized savings figure.

Run `python3 docs/benchmarks/factorized-reuse/verify.py` from the repository root
to check the pinned fixtures, per-row counts, per-model usage and cached work
offline. [The index](index.json) records the distinct prompts, schemas, source
files, binaries and runtimes. The earlier 201-attempt boundary comes from its
retained independent audit; these nine report files do not recompute those older
receipts. Replaying the arithmetic does not certify model reasoning or turn
already-consumed data into a fresh holdout.

See [the experimental two-stage policy](../../factorized-reuse.md) for the API
and admission controls. Exact task reuse keeps its existing path.
