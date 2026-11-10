# Before we trust a saved answer

This snapshot ends at the audited 618-attempt development boundary. Later
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

Ten runs failed their quality checks; the four-minute-budget pilot passed all
20 requests, then its larger regression failed. A later assessment-enabled pilot
stopped under critical memory pressure before making any semantic decision. Fresh independent qualification and matched economics
have not run. Keeping every run's misses and costs helps explain what still needs work.

| Run | Valid requests served | Invalid requests served | Unscored requests | Native claim/attempt records | Cached source checks |
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
| [Same four-minute budget: stopped larger regression](explicit-deadline-r3.json) | 20 / 71 | 2 / 73 | 108 | 39 | 30 |
| [Nonthinking target-contract pilot](target-contract-pilot24.json) | 6 / 12 | 1 / 12 | 0 | 31 | 11 |
| [Assessment-enabled pilot: critical-memory stop](assessment-memory-stop-pilot24.json) | 0 / 12 | 0 / 12 | 24 | 1 unconfirmed | 0 |

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

The same profile then failed the larger 144-request regression. It served two
invalid requests: a saved owner-permission answer after the requested actor
changed to a project maintainer, and a prose onboarding checklist when CSV rows
were requested. A third, complete pair response said `accept` while its
`unchanged_facts` field was false; the parser withheld it as protocol `unknown`.
Its confirmed usage remains charged, and withholding does not make that response
protocol-valid. The run then stopped on a different pair response that exhausted
its 2,048-token output cap. That incomplete result has known usage and no retained
final text. It created one additional unfinished fence, not a new usage-unknown
outcome. The remaining 107 requests were not attempted. Together with the failed
execution, 108 requests are unscored; none count as correct rejections. The report's
scored-subset metrics do not describe the full 144-request population.

This stopped regression used five new source checks (3,359 input and 70 output
tokens) and 34 pair checks (22,526 input and 15,322 output tokens, including
reasoning and the capped call). It reused 30 existing source receipts and made
42 embedding calls with 1,068 reported input tokens. There were no unreported
native jobs or outstanding reservations in this run's audited scope. The evidence
audit passed; semantic qualification failed. Fresh qualification remains sealed.

The next nonthinking pilot completed all 24 requests, but approved only six of
12 valid reuses and incorrectly approved one of 12 invalid ones. The stale answer
said to hold a release because a rehearsal had not run; the new request said that
rehearsal had passed under an updated snapshot. Six narrower questions were
rejected even though the saved answers already covered them. This pilot failed;
its larger regression did not start.

All 31 native calls completed with known usage: source checks used 7,996 input
and 154 output tokens; question checks used 14,738 input and 710 output tokens.
Eleven source lookups needed no new generation. The 36 embedding calls reported
1,040 input tokens. These 24 requests include the prior 20 plus four known
problem cases, so comparing the two pilots' aggregate totals would not establish
savings. The [derivation](pilot24-derivation.json) records that consumed union.

The original scorer stopped because it expected an explicit false
`verifier_pair_think` field, while Go omitted that false field. The raw report
stays unchanged. An independent replay normalized only that absent declared-false
field in a separate copy; the quality gate still failed with the same six misses
and one unsafe acceptance. This encoding mismatch did not cause the model's
wrong decisions. The [index](index.json) preserves the original audit and
encoding-review hashes. This run added no incomplete or unknown job and no new
unfinished fence.

The assessment-enabled pilot stopped after 4.61 seconds when the watchdog
observed critical memory pressure. Before the child started, the sample showed
high pressure and about 177 MiB available. The child group was stopped and waited;
no final after-child sample or `child_finished` event was recorded. The retained
process-return and watchdog wait evidence permits forensic inventory, not a
passed execution or resource qualification.

No question received an applicability decision: the first source check returned
an execution error and the other 23 requests were not attempted. All 24 requests
are unscored. One durable source claim became uncertain without response or
usage; a client-reported generation attempt does not establish delivery to the
model server or completion. An empty model-residency response cannot settle it
either. The two embedding calls reported 72 input tokens. The uncertain attempt
remains fenced and is not counted as zero-cost inference, a model-quality miss,
or a successful rejection. The 198 full input frames had passed bounded count
preflight, which did not establish semantic quality or eventual capacity.

Across these runs, 222 source-certificate lookups reused completed receipts with
zero current generation tokens. There were also 417 native claim/attempt records:
392 complete with known usage, 23 incomplete with known usage, and two with unknown
usage. Known-token subtotals exclude those unknown calls; they do not value them
at zero. All measured costs and both unknown job identities remain in the reports.
The separately audited earlier boundary retains another 201 attempts, including one
incomplete result and five unknown outcomes. That is 618 attempts in total and
31 unfinished execution fences; no claim was cleared to get another try.

Paid fallback was disabled and provider API spend was $0. Local hardware and time
still have costs. These runs checked answer validity; they did not generate
fresh comparison answers or measure dollar savings or reduced Codex allowance.
Token usage stays separated by model. A sum across tokenizers is useful for
reservation accounting, not a normalized savings figure.

Run `python3 docs/benchmarks/factorized-reuse/verify.py` from the repository root
to check the pinned fixtures, per-row counts, per-model usage and cached work
offline. [The index](index.json) records the distinct prompts, schemas, source
files, binaries and runtimes. The earlier 201-attempt boundary comes from its
retained independent audit; these twelve report files do not recompute those
older receipts. Replaying the arithmetic does not certify model reasoning or turn
already-consumed data into a fresh holdout.

See [the experimental two-stage policy](../../factorized-reuse.md) for the API
and admission controls. Exact task reuse keeps its existing path.
