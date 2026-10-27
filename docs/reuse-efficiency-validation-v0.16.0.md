# Reuse efficiency: what passed, what didn't

Saving tokens doesn't help if we return the wrong answer. This work started
with a first-paraphrase test that used 370 more generation tokens than its
fresh comparison. We tested shorter and shared verifiers, kept their failures,
and left the published standalone verifier unchanged.

The new application path is **explicit approved question forms**. Your app
defines a finite, versioned set of equivalent questions. A pure Go helper
prepares one canonical request, then the existing exact path handles it.
It makes no inference about an unfamiliar sentence.

## Independent behavior checks

An independent test author exercised six business contracts through real
HTTP/SQLite clients: support, onboarding, incidents, release readiness, refunds
and broker lifecycle. Each has a canonical question, two approved forms and six
nearby unapproved questions. The tests cover actual publication/replay and
provider calls, plus changed facts, owners, conditions, quantities, actions and
output formats. Separate checks cover contract/runtime/scope/TTL drift, quotas,
concurrent Pending work and unknown outcomes.

All 12 listed repeats use the existing exact result with no additional count,
generation, embedding or verifier calls. The 36 unlisted questions stay outside
that canonical task. These are application-contract tests with scripted fresh
outputs. They establish neither model answer quality nor semantic precision.

## Live approved forms: zero work on ten checked repeats

We also ran the [six-case local example](../examples/approved-questions/README.md)
once, with a fresh dedicated broker scope, real Ollama generation, complete
business evidence and whole-object JSON checks. All checks were frozen before
inference. Expected JSON check values were not inserted into the prompts.

| Phase | Generation calls | Input tokens | Output tokens | Passed checks |
| --- | ---: | ---: | ---: | ---: |
| Canonical seeds | 6 | 1,905 | 185 | 5/6 |
| Approved-form repeats | 0 | 0 | 0 | 10 completed; 2 withheld |
| Separate fresh baselines | 12 | 3,833 | 372 | 9/12 |

The refund source and its two baselines returned 1,200 cents instead of
`3 × 2700 − 1500 = 6600`. The source check failed, and both repeats were withheld.
One release baseline changed the exact version identifier to “version 2.8.1”.
None of these failures was retried or counted as a successful answer.

Four cases—support, onboarding, incident and broker status—had a passing seed,
both passing repeats and both passing fresh baselines. In that **post-run
quality-qualified subset**, eight fresh answers used 2,756 generation tokens;
the eight approved repeats used zero. Including the four seed answers used
1,371 tokens, or **50.3% less** than those eight fresh comparisons. This is a
finite application-contract result, not a claim about arbitrary paraphrases.
The full six-case counts and failed-work costs above remain part of the report.

[Raw measured rows](benchmarks/reuse-efficiency/approved-forms-live.jsonl) ·
[Per-case totals and comparison rules](benchmarks/reuse-efficiency/approved-forms-summary.json).

## Why shared fuzzy verification isn't a serving feature

The [complete experiment index](benchmarks/reuse-efficiency/index.json) links
39 retained local diagnostic runs, including the initial pilot, malformed and
incomplete work, abstentions, misses and false acceptances. Run the arithmetic
check without a model:

```sh
python3 docs/benchmarks/reuse-efficiency/verify.py
```

Some results worth keeping visible:

| Population / policy | Eligible served | Invalid approved | Verifier generation tokens | Decision |
| --- | ---: | ---: | ---: | --- |
| Round 1, published standalone baseline | 45/60 | 0/60 | 46,022 | Baseline |
| Round 1, factorized two-question prototype | 43/60 | 1/60 | 23,639 | Withheld |
| Round 3, compact pair at exploratory 0.4 | 11/60 | 1/60 | 5,102 | Withheld |
| Round 4, published baseline at exploratory 0.4 | 2/60 | 0/60 | 9,471 | Low coverage |
| Round 4, factorized pair at exploratory 0.4 | 5/60 | 0/60 | 2,884 | Insufficient qualification |

The factorized pair saved 48.6% of Round 1 verifier tokens but approved a
completed-work-only question using a remaining-work answer. Separate unsupported
answer tests also produced false acceptances. A shorter compact policy approved
an internal-preview answer for a public-production qualification question.
Those are quality failures, not savings.

At the predeclared 0.65 cutoff, both Round 4 policies served **zero** eligible
questions. Filtering a difficult case out doesn't repair the verifier. The
exploratory 0.4 result is retained alongside the primary run, not substituted
for it. Per-case precision, recall, coverage and descriptive Wilson intervals
are in the index; precision is null when nothing is served.

Rounds 1–3 became development/regression evidence. Round 4 was authored from
the schema by a separate agent without reading the implementation, previous
corpora or results. Its question content was unseen before the factorized
policy freeze. An offline parser check found prose in the intent-label field;
the author repaired only that metadata before any Round 4 model call. Labels,
questions, evidence and answers stayed unchanged.

These synthetic cases are correlated within families and use one local model.
They do not establish a general 90–98% accuracy claim. The shared verifier now
lives only in the explicit internal diagnostic path behind
`semantic evaluate --batch-size 2`. It has no application answer-serving API.

## Real generation exposed another cost

A separate, selected native prototype generated six original answers and twelve
fresh comparisons. Independent automated factual review passed five originals
and only three fresh comparisons. The refund original and both comparisons
said USD71 but explained it as `78 - 9 - 7`, which equals USD62. That original
was withheld from reuse. Other failures changed owners, incident status or
authority that the evidence hadn't granted.

The withheld prototype delivered ten answers, including one explicit fresh
replacement, while the comparison generated twelve. Its measured generation
work was:

| Work | Calls | Input tokens | Output tokens |
| --- | ---: | ---: | ---: |
| Six seed answers | 6 | 2,034 | 854 |
| Twelve fresh comparisons | 12 | 4,008 | 1,983 |
| Shared verification plus replacement | 6 | 5,180 | 329 |
| Candidate including seed cost | 12 | 7,214 | 1,183 |
| Fourteen later exact replays | 0 | 0 | 0 |

Ten deliveries versus twelve, with different quality, is not a matched savings
claim. Adding setup cost also changes the comparison. The
[curated records and factual findings](benchmarks/reuse-efficiency/withheld-native-prototype.json)
retain the excluded family and every observed cost. Embedding work uses a
different tokenizer and is reported separately.

## Boundaries

All live inference used installed `qwen3.5:4b` and `all-minilm` through literal
loopback Ollama on one machine. Paid fallback stayed disabled; provider API
spend was **$0**. The model digests are in the experiment index. No physical
cluster, GPU throughput, inference-speed ranking or dollar invoice saving is
established here.

Original and repeated answers still require their declared checks. Automated
review is not human acceptance. Hardware, electricity, reviewer effort and the
surrounding hosted coding assistant's allowance are outside these token totals.
We used Questlock's checked-revision tools during development, but have no
measurement proving a 99% reduction in the surrounding Codex session.

Issue [#41](https://github.com/spfuzzylink/questlock/issues/41) remains open for
general paraphrase quality and first-use economics. The
[approved-form guide](approved-questions.md) describes the smaller, explicit
contract that applications can use without another verifier.
