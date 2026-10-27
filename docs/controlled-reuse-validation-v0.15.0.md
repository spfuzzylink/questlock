# Controlled reuse: what passed, what cost more

The useful question is whether a saved answer still does the job. Different
wording is fine. Different facts, scope or instructions are a reason to stop
reuse, even when the questions look related.

These observations used one Mac, local Ollama, `all-minilm` and `qwen3.5:4b` on
2026-10-27. Paid fallback stayed off. Provider API spend was $0; that excludes
hardware, power and the surrounding hosted coding assistant.
[Inputs, individual observations, usage and limits](controlled-reuse-validation-v0.15.0.json).

## Answer validity

The frozen synthetic challenge contains 72 pairs across six families: 36 should
reuse and 36 should not. Source answers are supplied fixtures; embeddings and
verification are real local inference. The retrieval cutoff was 0.75, which is
not a quality confidence.

| Observation | First guard | Corrected guard |
| --- | ---: | ---: |
| Eligible paraphrases served | 7/36 | 23/36 |
| Invalid variants served | 0/36 | 0/36 |
| Precision among served answers | 7/7 | 23/23 |
| Recall of eligible answers | 19.4% | 63.9% |
| Overall serve/withhold accuracy | 59.7% | 81.9% |
| Coverage of all questions | 9.7% | 31.9% |
| Wilson 95% precision interval | 64.6–100% | 85.7–100% |

The first run exposed lexical bugs: ordinary words could be mistaken for units,
and possessive identifiers could mismatch. The corrected guard was evaluated on
the **same development corpus after seeing those results**. Both runs are kept.
This is not an untouched held-out test or evidence of universal 90–98% accuracy.
Both runs completed all pairs with no execution errors or unknown verdicts.

| Family | Eligible paraphrases served by corrected guard | Invalid variants served |
| --- | ---: | ---: |
| Support FAQ | 5/6 | 0/6 |
| Employee onboarding | 6/6 | 0/6 |
| Incident handoff | 3/6 | 0/6 |
| Release readiness | 0/6 | 0/6 |
| Refund eligibility | 4/6 | 0/6 |
| Shared broker lifecycle | 5/6 | 0/6 |

Release readiness remains unqualified for useful coverage with this local
policy. A conservative rejection avoids a false answer but saves no generation.
The corrected run used 78 embedding calls (1,836 known input tokens) and 32
verifier generations (18,801 input + 1,089 output tokens). This diagnostic had
no fresh-answer baseline and establishes no token saving.

## Six actual short-answer workflows

Each family ran a fresh source, its exact repeat, a paraphrase, that question's
exact repeat, and a matched fresh paraphrase in isolated broker state. All use
the same local generator/tokenizer. The fresh comparison omits semantic policy.

| Family | Paraphrase path | Fresh comparison tokens | Verifier tokens | Net generation tokens saved |
| --- | --- | ---: | ---: | ---: |
| Support FAQ | Checked reuse | 301 | 671 | **−370** |
| Employee onboarding | Fresh under resource pressure | 299 | 0 | 0 |
| Incident handoff | Fresh under resource pressure | 342 | 0 | 0 |
| Release readiness | Fresh under resource pressure | 307 | 0 | 0 |
| Refund eligibility | Fresh under resource pressure | 325 | 0 | 0 |
| Shared broker lifecycle | Fresh under resource pressure | 356 | 0 | 0 |

For support, target generation was avoided, but verification used 370 more
generation tokens than the fresh comparison. Another 30 embedding tokens are
separate. The other five paths skipped optional reuse under observed memory
pressure and generated normally; the library's main generation path was not
under a resource-admission controller in this run. **This short-answer sample
does not demonstrate net token savings for a first paraphrase.** Local routing
kept provider charges at zero in both the comparison and controlled paths.

All 12 exact repeats completed with zero new target-generation tokens and no
embedding or verifier calls. One was an alias of the semantically reused answer;
the others reused original generations. Source usage remains historical rather
than an observed fresh alternative for those exact repeats.

Format checks passed all calls. Assistant review found a factual error in the
release-readiness paraphrase and its fresh comparison: both incorrectly said
a two-hour scan exceeded a 24-hour threshold. The source answer was correct.
Those failures remain in the record. Word counts, durable publication and AI
review are not human acceptance, and exact reuse cannot repair a wrong answer.

Caller timings include setup and model loading. Serial observations on one host
do not establish TTFT, throughput, GPU or physical multi-machine improvements.

## Coordination and regression checks

`questlock semantic reuse-quest` checks 54 fixture requests across the six
families, including exact reuse, paraphrase reuse, changed facts/format,
evidence/context changes, alias replay and a second scoped client after SQLite
reopen. Scripted providers are protocol tests, not model-quality evidence.

HTTP/SQLite tests also hold a producer in generation while another client waits
for its newly indexed answer. The waiter reuses without another target
generation; canceling it preserves the producer. Wait expiry permits bounded
fresh work. Slow repeated queries receive the wait deadline. Race coverage
includes ownership, unknown outcomes, source mutation/expiry, publication
acknowledgements, cold reads and bounded archive checkpoint progress.

During development, Questlock's checked revision path applied 20 counted version
replacements across 12 files with zero model calls. No hosted-chat token or
dollar saving was measured. These measurements support an explicit experimental
opt-in, not a default switch for every question.

## Reproduce the quality diagnostic

Install the example weights explicitly and prepare the local runtime first:

```sh
questlock semantic evaluate --live \
  --suite examples/semantic/controlled-evaluation.json \
  --embedding-model all-minilm --verifier-model qwen3.5:4b \
  --min-similarity 0.75 --timeout 20m
questlock semantic reuse-quest
```

For serving behavior, use the complete [source](../examples/semantic/support-faq.controlled.request.json)
and [paraphrase](../examples/semantic/support-faq.controlled-paraphrase.request.json)
requests against the same private broker. Their fictional JSON task differs
from the short paragraphs measured above. Keep failures and misses, account for
verification and embeddings, and qualify the actual workload before deployment.
