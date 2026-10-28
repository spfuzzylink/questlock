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

A failed source withholds its reuse rows. Known failures stay in the output; an
unknown outcome stops without retry. Outcome recording must succeed for every
attempt. The run has a 30-minute deadline and a shared scope/day cap of 524,288
input and 16,384 output tokens. These are ceilings, not predicted usage. Verifier
work shares that budget; embeddings are reported separately.

Keep source generation, verifier stages, embeddings, misses and fresh baselines
in the comparison. Exact replay must report zero new generation and no new
embedding or verification. Passing these small fixtures does not establish a
90–98% accuracy claim or prove savings on another workload.
