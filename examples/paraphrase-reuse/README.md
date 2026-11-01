# Measure a new wording and its exact repeat

Six fictional records: support, onboarding, incident handoff, release readiness,
refund arithmetic and an uncertain broker job. No provider API keys or paid fallback.
You need installed `qwen3.5:9b` and `all-minilm` Ollama weights and a scoped broker.
The example never downloads weights.

`--model` and `--verifier-model` select other explicitly installed local weights.
The fresh baseline always uses the same answer model as its source. Keep every
runtime trial, including failures; selecting a model is development work, not a
fresh independent qualification.

Review `cases.json` and the Go source before starting. The inputs are benchmark
examples, not an application question allowlist. Every new wording goes through
retrieval and the experimental [two-stage verifier](../../docs/factorized-reuse.md).

```sh
# Offline help; no broker, model or file access.
go run ./examples/paraphrase-reuse

# Summarize one saved attempt offline; reads only this explicitly named file.
go run ./examples/paraphrase-reuse --report /absolute/private/run/results.jsonl

# After creating a private directory and a fresh scoped broker token:
go run ./examples/paraphrase-reuse --live --fresh-baseline \
  --broker http://127.0.0.1:8080 --ollama http://127.0.0.1:11434 \
  --token-file /absolute/private/run/worker.token \
  --state /absolute/private/run/progress.jsonl \
  --output /absolute/private/run/results.jsonl
```

Use your actual loopback broker URL. Token, progress and output paths must be
absolute; their immediate parent directory must already be private. Output files
must be new. Keep these files outside your repository. Only the broker owns SQLite.

Each case runs one fresh source, two paraphrases and their two exact repeats.
`--fresh-baseline` adds two fresh requests with the same questions, model, facts
and output checks. Whole-object JSON checks catch wrong values, extra fields and
confusion between a proposed action and an action already completed.

Add `--fresh-repeat-baseline` together with `--fresh-baseline` to measure another
two fresh requests for those same questions. Their task identities use indices
2/3, distinct from baseline indices 0/1; all four disable semantic reuse. This
opt-in path records 54 rows: 6 sources, 12 paraphrases, 12 exact repeats, 12 first
baselines and 12 `baseline_repeat` rows. The existing baseline path stays at 42
rows. The header adds `fresh_repeat_baseline: true` only for the opt-in path.

A failed source withholds its reuse rows. Known failures stay in the output; an
unknown outcome stops without retry. Outcome recording must succeed for every
attempt. The run has a 30-minute deadline and a shared scope/day cap of 524,288
input and 16,384 output tokens. These are ceilings, not predicted usage. Verifier
work shares that budget; embeddings are reported separately.

Keep source generation, verifier stages, embeddings, misses and fresh baselines
in the comparison. Exact replay must report zero new generation and no new
embedding or verification. Passing these small fixtures does not establish a
90–98% accuracy claim or prove savings on another workload.

`--report` makes no broker or model calls and prints a JSON summary to stdout.
The existing input file and its immediate parent must be private. It accepts at
most 8 MiB, 1 MiB per line and 56 lines, checks the frozen six-case identity,
and rejects malformed, duplicate or missing required fields, duplicate rows and
conflicting receipt attribution. It emits no questions, answers, context, raw
errors, model names or credentials. The default help path still reads no files.

Every phase retains its observed, failed, withheld and missing row counts, output
check results, recorded/unconfirmed outcome coverage and known usage subtotals.
Answer generation, verifier generation and embedding input tokens remain separate;
unknown usage is counted explicitly. Factorized `verification.work` stages count
once; the decision aggregate contributes no additional call. Cached source
certificates contribute zero current usage. `embedding_work_records` counts
observations with embedding evidence or an inferred retrieval attempt, not a
provider call counter; repeated searches may use the same vector. Any model, embedding or verifier work
on an exact repeat fails that phase, including zero-token verifier invocations.

Partial runs remain reportable, with missing rows and the absent completion marker
visible. Their observed subtotals never establish complete accounting. Eligibility
fields gate a comparison of exactly twelve matched paraphrases and twelve fresh
baselines on quality, zero-work exact repeats, known usage, complete rows and
recorded outcomes. `compared_requests_per_arm` is zero until every condition passes.
`whole_cohort_comparison_eligibility` additionally requires twelve successful fresh
repeat baselines, enabling an observed 24-versus-24 comparison: paraphrases plus
exact repeats versus both fresh baseline phases. Older logs remain readable but
cannot qualify for that whole-cohort comparison. Source cold work remains a
separate common cost. The report never extrapolates twelve fresh calls to
twenty-four, combines different tokenizer units into a savings figure, infers an
invoice or estimates net savings.

New rows include `elapsed_ms`, measured immediately before `Router.Run` through
the end of `RecordOutcome`. This includes router finishing/indexing and outcome
registration, and stops before the forensic model-output fetch and JSONL writes.
Per-phase `known_end_to_end_ms` sums those clocks; missing clocks have explicit
`unknown_end_to_end_records` counts. Older logs remain reportable and may qualify
for token comparison, but cannot qualify for end-to-end latency comparison.

`known_attempt_elapsed_ms` separately sums saved answer `Measurement.TotalMS`,
which is a partial runner clock. `reported_verification_ms` and
`reported_embedding_ms` are separate diagnostics: work may overlap runner time,
and finish/indexing embeddings can occur after the runner returns. These clocks
are never added to each other or used to invent missing end-to-end time.
