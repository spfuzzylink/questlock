# Check the answer once. Check each new question separately.

**Experimental policy: not qualified for production reuse.** The assessment-enabled pilot stopped under critical
memory pressure before making a semantic decision. All 24 requests remain
unscored; one source attempt is uncertain and the other 23 requests were not run.
The earlier nonthinking pilot reused only six of 12 valid answers and served one
stale answer after the facts changed. An older thinking pilot passed 20 requests,
then failed its larger regression. Fresh independent qualification and matched
cost measurements have not run. These fixed-source checks do not measure fresh
answer generation or alias publication.

This optional verifier ships for evaluation alongside the independently tested
[capacity dispatcher](capacity-routing.md). Its remaining accuracy and whole-task
savings work is tracked in [#44](https://github.com/spfuzzylink/questlock/issues/44).
Shipping coordination controls does not establish that this policy is safe to
serve arbitrary paraphrases. The standalone verifier and exact-reuse defaults
are unchanged.

The successor checks whether the unchanged answer covers the new request.
Source bindings describe the saved task; they cannot substitute its old target
when the request changes the object. A necessary prerequisite can also answer a
before/without question negatively. A narrower question need not repeat the
original question. Exact reuse and the default standalone verifier keep their
existing behavior.

The passing pilot used `qwen3.5:9b` for 10 source checks (7,363 input and 140 output
tokens) and `qwen3:14b` for 17 question checks (12,396 input and 7,826 output
tokens, including reasoning). Its 31 embedding calls reported 909 input tokens.
The failed larger regression added five source checks (3,359 input and 70 output)
and 34 question checks (22,526 input and 15,322 output), including all known
failed work. Its 42 embedding calls reported 1,068 input tokens. These were
explicit shared four-minute verifier budgets with fixed context and output caps;
the two-minute thinking default stays unchanged.

The latest nonthinking run used 11 source checks (7,996 input and 154 output
tokens), 20 question checks (14,738 input and 710 output), and 36 embedding calls
with 1,040 input tokens. Eleven source lookups reused completed receipts with no
new generation. All 31 native calls completed with known usage. A scorer encoding
mismatch was reviewed separately; correcting it in an offline copy did not change
the failed quality result.

The assessment-enabled attempt added two embedding calls (72 input tokens) and
one unconfirmed source attempt. The watchdog stopped and waited for its child
group after critical memory pressure; it returned no native response or token
usage. That unknown work remains fenced. Stopping safely does not prove model
completion or qualification, and its unknown usage is not valued at zero.

[Retained development runs](benchmarks/factorized-reuse/README.md) preserve ten
failed quality runs, one passing pilot and the unscored operational stop. They
include all 144 rows of the stopped larger regression. The audited boundary is
618 native claim/attempt records: 587 complete with known usage, 24 incomplete
with known usage, and seven with unknown usage. All 31
unfinished fences remain. A new result cannot recover an older unconfirmed call.
The evidence's offline check runs in `make check`.

Provider API spend was $0 with paid fallback disabled. Model token counts stay
separate; they are not a normalized savings figure. These validity diagnostics
did not measure fresh-answer savings, alias publication or reduced hosted Codex
usage. Older labels and policy versions stay with their original reports.

Someone asks the same thing in different words. We want the saved answer back,
without quietly borrowing an answer to a different question.

The experimental `factorized-v1` policy splits that decision into two local
checks. First, a source check reads the original question, evidence and saved
answer. It must confirm that the answer is supported **and complete**. Then an
applicability check reads that exact answer, the new request and the same evidence.
It asks whether the unchanged answer actually satisfies the request. Different
wording, or a narrower question, can be fine; changed facts or missing detail
are not. A prose answer does not become JSON just because the facts match.

No question list is required. Both checks keep the full approved background;
a context hash or a generated summary cannot replace it. The second check sees
the original question and full saved answer, including its keys, values and layout. That costs input
tokens, but a coarse label such as “JSON object” cannot tell us whether the keys
match. The source check can be cached; net savings still need measurement.
The receipt stage named `equivalence` records this applicability check. Intent
labels still partition work and bind its identity; they are omitted from the
model input so a source label cannot define what the new question means.
The verifier uses native system/user separation rather than wrapping its data
record in another serialized request. All evidence and decoder-schema bytes
still count against preflight and runtime limits.

This is still an experimental verifier. Qualify it on your own work before
serving its results. A valid JSON verdict or high embedding similarity does not
prove that a model understood the question.

```mermaid
flowchart TD
    A[Approved read-only task] --> B{Exact lookup and existing claim}
    B -->|valid hit| C[Return checked result: no new model work]
    B -->|pending or uncertain| J[Stop; keep claim and reservations]
    B -->|no result| K{Task-level admission policy configured?}
    K -->|no| D[Claim task and retrieve candidate]
    K -->|yes| R{Capacity available within the wait bound?}
    R -->|yes| D
    R -->|no| L[Return resource_deferred before a new claim]
    D --> E[Validate original source, evidence, checks and expiry]
    E --> F[Source support and completeness certificate]
    F --> G[Check the unchanged answer against the current question]
    G -->|accept| H[Publish alias with original source expiry]
    H --> C
    D -->|known miss or legacy pressure skip| I[Bounded fresh local generation]
    E -->|known rejection| I
    F -->|known rejection or abstention| I
    G -->|known rejection or abstention| I
    D -->|claim race or uncertainty| J
    F -->|unconfirmed work| J
    G -->|unconfirmed work| J
```

The source certificate may already exist. That saves its model call, but it
doesn't remove current source validation or the new question's applicability
check. Unconfirmed source reads or publication also stop; the fresh path cannot
be used to escape an uncertain result.

## Opt in explicitly

Add these settings to an otherwise complete [controlled reuse request](controlled-reuse.md):

```json
{
  "verifier_policy": "factorized-v1",
  "verifier_model": "qwen3.5:9b",
  "verifier_max_input_tokens": 16384,
  "verifier_max_output_tokens": 128,
  "verifier_timeout_ms": 60000
}
```

The model is an example, not a quality recommendation. Install weights separately.
The worker selects the requested policy. Go callers supply
`delegation.NewFactorizedVerifier` from their `ControlledReuser.VerifierFactory`
and set its `DailyTokenBudget` to the approved task's shared budget, just as for
`NewLocalVerifier`. Omitting `verifier_policy` keeps the standalone verifier.
Older workers must reject unsupported settings rather than silently ignoring them.

The two stages default to half the declared input and output allowance, under one
verifier deadline. Optional smaller stage input caps are described below. Their actual usage and durable receipts appear separately in
`verification.work`. Outcome accounting flattens those stages into verifier work;
the combined decision receipt is not another inference call. An unconfirmed
inference outcome stops the target without automatic retry or paid fallback. A complete
rejection or `unknown` verdict with confirmed usage is a known abstention; the
approved workflow may continue to fresh bounded local generation.

Some installed models need a reasoning budget to check a saved answer properly.
Set `verifier_source_think: true` for source-check reasoning, or
`verifier_pair_think: true` for applicability-check reasoning. These opt-ins are
independent and require `factorized-v1`. Either opt-in defaults to a shared
4,096-token output allowance and a 120-second deadline; each stage still gets
half the token allowance. Smaller explicit caps still apply. Count reasoning
output in the selected stage too: hiding its text does not make that work free.
With both settings omitted or false, the existing smaller limits and identities
stay unchanged. A larger budget is an option to test, not evidence of better
answers or lower cost.

There is also an opt-in short assessment: `verifier_pair_assessment: true`
(CLI `--verifier-pair-assessment`). The applicability model writes a bounded
comparison before its decisions, with native pair thinking disabled. This is an
experiment to give the model room to check a narrower question without spending
thousands of hidden reasoning tokens. It is not proof that the answer is safe.
The note stays in the private inference artifact; public result metadata contains
only the decision and usage. Invalid or contradictory output is withheld.

Assessment requires `factorized-v1` and cannot be combined with
`verifier_pair_think`. If output allowance is omitted, it selects 512 tokens
total, split equally between stages; an explicit smaller allowance still wins.
Source thinking, if selected separately, keeps its existing limits. Assessment
does not extend deadlines. With assessment omitted or false, the existing pair
prompt and output schema stay unchanged. Check MCP discovery's
`semantic_reuse_optional_reasoning_fields` for `verifier_pair_assessment` before
sending it to a worker. The installed worker may need a deliberate upgrade.

An explicitly adopted new snapshot or policy can also be rejected before the
verifier runs. Asking about a change or quoting one does not adopt it. This guard
only catches recognizable cases; passing it still requires the full validity
check. Neither option has passed fresh workload qualification yet.

Before enabling pair reasoning through MCP, check that
`questlock_status.request_schemas.semantic_reuse_optional_reasoning_fields`
includes `verifier_pair_think`. Older workers must reject this unsupported field
rather than silently ignoring it. The diagnostic and measurement example expose
`--verifier-pair-think`; no client configuration change is required. The
diagnostic keeps its 20-minute default and one-hour wall limit, with a two-hour
wall limit only when pair reasoning is explicitly enabled. Per-verification
deadlines and token/resource caps remain separately bounded.

Factorized thinking keeps its 120,000ms verifier default. An explicit
`verifier_timeout_ms` (CLI `--verifier-timeout-ms`) may select up to 600,000ms
when either stage's thinking flag is enabled. Source and pair share that deadline;
it is not a separate allowance for each stage. With `timeout_ms` omitted, the
resolver uses the larger of 120,000ms and the selected verifier deadline. An
explicit shorter resolver deadline or earlier caller cancellation still wins.
Nonthinking and standalone limits remain unchanged. Check the read-only numeric
capability `questlock_status.request_schemas.semantic_reuse_factorized_thinking_max_timeout_ms`
before requesting a longer deadline through MCP. A longer deadline permits more
latency; token/output caps and existing unfinished claims remain unchanged.

Optional `verifier_pair_temperature` (finite 0..2) and `verifier_pair_seed`
(integer 0..2147483647) apply only to factorized applicability requests. Omitted
values preserve temperature zero and no explicit seed; explicit zero is retained.
They bind the policy and native request identities without changing source-stage
sampling, prompts, output limits or deadlines. A seed is a requested runtime
control, not a cross-runtime determinism guarantee. The diagnostic and measurement
example expose `--verifier-pair-temperature` and `--verifier-pair-seed`. For MCP,
check `questlock_status.request_schemas.semantic_reuse_optional_sampling_fields`
for both names before sending them to a worker. No client configuration change
is required.

## Give busy hardware time to catch up

Memory headroom, concurrency and waiting time are deployment choices. A laptop
can admit one job at a time; a larger machine can allow more. The answer still
has to pass the same checks.

Admission policy is operator configuration, not a delegation request JSON field.
Go callers can load it with `delegation.LoadReuseAdmissionPolicy(reader)` and set
`ControlledReuser.AdmissionPolicy` before the first request. For a verifier used on its own, build a shared
callback with `delegation.NewResourceAdmission` and assign it to
`FactorizedVerifier.Admission`. Use one admission layer per call path: acquiring
the same lease again inside an admitted job can fill its own concurrency limit.

A small runtime policy looks like this:

```json
{
  "min_available_memory_bytes": 268435456,
  "max_concurrent": 1,
  "max_queued": 8,
  "max_wait_ms": 120000
}
```

The evaluation CLI accepts this file with `--resource-policy PATH`. Omitted
fields use the documented defaults in `ReuseAdmissionPolicy`; zero queue slots
means no waiting queue, and a zero wait means one immediate resource sample.
Set the policy before the reuser's first request. Create a new reuser instance
to change it; current capacity samples still change on every admission probe.
More memory headroom leaves less room for model work. More concurrency can
improve throughput when the hardware can carry it.
High memory pressure requires an explicit opt-in. Critical pressure, unknown
samples and invalid samples still defer new admission.

With this policy enabled, exact hits and existing claim fences are checked
first. New work waits for capacity before claiming the task. If its bounded wait
expires, it returns `resource_deferred`; it does not start fresh generation or
count as a failed answer-quality check. A proven pre-claim deferral can be
rescheduled with the same task identity after capacity returns; no revision bump
is needed. Unknown execution remains fenced. Bounded waiting promises neither
admission nor eventual completion. Once admitted, work keeps its caller
deadline. A later resource sample does not cancel it through this admission
policy. Resource observations and wait time are reported separately from model
verdicts, including any source-check work already completed.

These queue and concurrency limits belong to one reuser process. They are not
cluster memory reservations or a guarantee against an out-of-memory failure.
Omitting the policy preserves the existing behavior, which may skip optional
semantic reuse under pressure and continue to bounded fresh generation.

## Choose the two verifier stages separately

By default, both stages use `verifier_model`. An optional
`verifier_pair_model` selects a different installed local model for the new
question's applicability check. Optional `verifier_source_max_input_tokens`
and `verifier_pair_max_input_tokens` reduce each stage's input cap. Each cap
must fit within half the total verifier input allowance; omitted caps keep that
half. A smaller cap must still fit the complete rendered input, including the
schema and model instructions. An over-budget request stops before generation;
the verifier does not trim evidence to make it fit. These settings enable no
fallback route.

Explicit stage model or input-cap settings add declared model and runtime
identity attribution to the stage usage. Default omitted fields stay omitted.
Changing the pair
model changes the decision identity. Keep the two stages separate when reporting
token savings: a token from one model's tokenizer is not a common unit of work
across models. Aggregate counts are reservation-accounting sums, not normalized
savings. Measure the complete attempt against a matched fresh baseline.

When new stage model/runtime or pre-claim deferral fields are recorded, every
worker and strict outcome reader in that cohort must use v0.17 or newer. Older
readers can reject those fields; use a compatible cohort rather than silently
dropping accounting. Legacy controlled work with these fields omitted retains
its existing compatibility requirements.

## What gets reused

The source certificate binds the original job version, content and output hashes,
original request, approved evidence, verifier policy and installed runtime.
Its task result can be reused for up to one hour. Reading it does not extend that
window. Each question decision also binds its source certificate, the exact answer and
current request.
The final answer alias still inherits the original answer's expiry, even when the
certificate remains available longer.

Exact task lookup stays first. An exact hit skips embeddings and both verifier
stages. Pending and uncertain claims retain their existing fences. This option
adds no action replay, source promotion, weight training or automatic cancellation
of another agent.

The default verifier retains its conservative literal guards. This policy adds
rejection-only checks for recognized numeric-only, JSON, CSV/TSV, Markdown
table, prose and no-JSON requests. It checks the actual saved representation, not a format label,
and never converts an answer. Unrecognized wording still needs the local
verifier; these checks cannot establish facts, complete coverage or meaning.
They can conservatively withhold an otherwise usable answer. Changed numbers,
identifiers, roles and conditions remain the contextual verifier’s job. Keep
independent hard negatives in the workload.

Explicit language changes get a separate check. An English response cannot be
reused unchanged when the new request asks for Spanish. The screen recognizes
English, Spanish and French directives and shared instruction defaults; other
explicit languages or conflicting directives withhold reuse. It does not detect
the actual answer's language. Quoted examples are data, and passing this screen
still leaves the full decision to the local verifier.

There is also a rejection-only screen for statistical measures in saved answers.
An answer saying “average latency below 250 ms” cannot borrow evidence that only
establishes “p95 latency below 250 ms.” Recognized measures near numbers must
appear in the approved context or bindings; mean/average and median/p50 count as
aliases. Questions and instructions supply no evidence for this screen. It does
not check which object a measurement belongs to, prove its value, or validate
every unit, denominator or time window. It can withhold valid derived statistics.
Both model checks remain required after the screen passes.

Some answers depend on how the question is asked. “Yes, that blocks the release”
doesn't answer “Is the release ready?” unchanged. A saved explanation of read
access also doesn't cover a new request for read **and** write access.

The two-stage policy now has a rejection-only screen for suspected changes in
yes/no direction, capability scope and approval roles. It runs before the
question-checking model can authorize reuse. It can withhold valid paraphrases;
passing it proves neither equivalence nor correctness. The full evidence and
unchanged answer still go to the contextual verifier. Its version is part of the
policy identity, so an older experimental decision cannot skip the new check.

## Measure the whole attempt

The first new wording pays for source certification and question comparison.
Later new wordings may reuse the certificate. Exact repeats of a resolved task
need neither. Count all three paths separately, including embeddings, misses,
failed answers and uncertain work. For short answers, checking can still cost more
than answering fresh. There is no automatic token-saving claim for this option.

The [six-case measurement](../examples/paraphrase-reuse/README.md) runs fresh
sources, two new wordings per source, exact repeats and optional fresh baselines.
Those examples exercise the integration; they do not establish general language
accuracy. Freeze a separate population before evaluation, test changed facts and
constraints, and report precision, recall and coverage by use case.

## Where FreeToken fits

[FreeToken](https://github.com/FlashML-org/FreeToken) serves open-weight models
on local hardware. Its [current installation requirements](https://github.com/FlashML-org/FreeToken/blob/main/docs/install.md)
are Linux x86-64 with an NVIDIA GPU; this is not an Apple Silicon backend.
Its [OpenAI and Anthropic APIs](https://github.com/FlashML-org/FreeToken/blob/main/docs/quickstart.md)
make it worth assessing as a separate provider. This verifier currently uses
Ollama. API compatibility alone does not validate token preflight, bounded output,
usage accounting, runtime identity or uncertain-call recovery. We have not added
a FreeToken adapter or measured its speed or reuse quality. A faster runtime
still needs the same answer-validity tests.
