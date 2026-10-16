# Measurable bounded inference

Version **v0.3.0** adds current-run measurements, optional Langfuse export, and a bounded evaluation command. The [measurement record](measurements-v0.3.0.md) shows one passing MCP extraction, exact reuse, and quality failures in the broader local suite. The self-hosted Langfuse CI contract passed actual metadata readback. These observations are task-specific, not a statistically reliable benchmark.

Repeated handoffs and fixed-snapshot summaries should be faster and more affordable when their completed answer can be reused. Measurements make that behavior inspectable: distinguish a fresh generation from a broker lookup, record the context admitted, and compare explicit output caps against declared task checks. [Bounded delegation](delegation.md) uses the same runtime.

![Questlock layered inference runtime](diagrams/runtime-layers.svg)

The current diagram is [editable in diagrams.net](diagrams/runtime-layers.drawio).
The [historical v0.3 runtime view](diagrams/inference-runtime.svg) and its
[editable source](diagrams/inference-runtime.drawio) remain available. The Go Runner owns model calls; the authenticated broker owns durable coordination and results. Langfuse receives an optional metadata copy after execution. The [existing model architecture](model-architecture.md) explains claims, budget reservation, and uncertain outcomes.

## Read the measurements correctly

`model run` returns a `measurement` for this invocation. `total_ms` covers the Runner call; `phase_ms` separates planning, lookup, admission, preflight, reservation, generation, publication, settlement, and recovery where those phases occurred. CLI provider setup is reported separately as `provider_setup_ms`. Completion and durability describe protocol state, not answer quality. Usage
covers the delegated provider or local generation and excludes the surrounding
hosted assistant's prompt, tool, and context tokens.

| Field or outcome | Meaning |
| --- | --- |
| `generation_called` | The provider's `Generate` method was entered. It does not prove upstream generation or billing. |
| `known_generation_usage` | Returned token usage passed validation. Unknown usage stays unknown. |
| `this_run_usage` | New usage attributable to this run; zero on completed-result reuse. |
| `generation_usage` / `avoided_usage` | Original generation usage, and the generation avoided by reuse. |
| `generation_timing` | Historical original streaming timing, if available; it is separate from current-run `measurement`. |
| `pending`, `uncertain`, `incomplete`, `limit_exceeded` | Inspect the retained broker record before deliberately starting another attempt. |

`measurement` describes only this invocation and is neither persisted in the job record nor included in its fingerprint. Original streaming timing and runtime KV observations are persisted separately. A reused result retains original timing in `generation_timing`, clears `response.timing`, and has no fresh generation timing in `measurement`; its original KV observations remain in `generation_runtime_cache` rather than current `response.runtime_cache`.

For local Ollama only, set `"stream": true` in the approved spec to collect content-arrival observations. `first_content_ms` (TTFT) starts at entry to `Generate` and ends at the first nonempty content chunk observed by the client, including metadata and network overhead. `output_content_ms` is the interval from the first to the last nonempty content chunk; one content chunk can yield zero. Empty frames do not extend it. These are not pure compute TTFT, per-token throughput, or reasoning-token timing. Streaming consumes and validates the bounded response before exposing approved output.

## Runtime KV evidence in v0.5.0

The SGLang adapter reports optional `response.runtime_cache` counters for a new
generation. `generation_runtime_cache` retains original observations on an exact
result hit; current `response.runtime_cache` is cleared on reuse. Missing
aggregate/tier counters stay unknown. KV hits describe compute reuse and never
become the API `cache_read_tokens` billing bucket or a dollar discount.

Optional generation metadata exports controlled `questlock_runtime_cache_*`
counts, known flags, profile and `runtime_reported_compute_reuse` basis. No
runtime URL, path, cache namespace, storage credential, prompt or answer is
exported. `phase_ms.runtime_identity` records the pre-lookup identity check.
Self-hosted SGLang uses explicit zero provider token charge; hardware and rental
costs remain outside this token-price ledger.

The disposable real Langfuse v4.59.0 test now reads back four roots and three
generation children. Its additional synthetic cache observation reports 80
cached, 50 device and 30 host tokens with unknown storage, while billing cache
usage stays zero. This verifies telemetry mapping, not GPU performance.
See [current evidence](validation.md#v050-observations--2026-10-16) and
[the runtime contract](runtime-cache.md).

## Opt in to Langfuse

Make `LANGFUSE_PUBLIC_KEY` and `LANGFUSE_SECRET_KEY` available through your secret manager. Set `LANGFUSE_BASE_URL`, or set `QUESTLOCK_LANGFUSE_ENDPOINT` to the complete URL ending in `/api/public/otel/v1/traces` when a path prefix is needed. Export requires HTTPS except for literal loopback HTTP. Credentials, broker tokens, and endpoint configuration stay outside task specs.

```sh
export LANGFUSE_BASE_URL="http://127.0.0.1:3000"
export QUESTLOCK_TOKEN_FILE="$HOME/.local/share/questlock/agent-workflows/worker.token"
questlock model run --spec "$HOME/questlock-workspace/task.json" \
  --langfuse --langfuse-timeout 2s
```

The timeout defaults to two seconds per HTTP request and must be 1ms–5s. Output JSON reaches stdout before export; acknowledgement status and trace/span IDs go to stderr. Export failure cannot alter the result or retry generation. An acknowledgement proves endpoint acceptance, not dashboard visibility. Live evaluation and delegation can also attach a numeric `questlock.quality` score derived only from their declared checks.

Each fresh attempted generation exports one `questlock.run` root and one `questlock.generate` child. Reuse exports one root, zero new usage, and no generation child. The narrow schema includes allowlisted outcomes, model/provider identifiers, fingerprint, context counts, timing, and usage. It has no prompt, answer, task/revision labels, user/session identity, raw errors, credentials, or endpoint URL. Telemetry is off unless explicitly enabled. For your own dashboard, provision a
Langfuse project and supply its endpoint and keys; Questlock does not create a
project or connect to a user dashboard automatically.

Input/cache and output/reasoning buckets are exclusive in Langfuse: uncached input, cache reads, and cache writes sum to input; visible output plus reasoning sum to output. Reasoning is never charged twice. Only the generation child uses Langfuse generation usage/cost fields; root usage/cost numbers remain metadata. Local Ollama export explicitly records zero provider token charges, while hardware, electricity, memory, and runtime still cost resources. Hosted USD and avoided USD use caller-supplied prices and reported usage; they are estimates, not invoices or proven savings. Missing cost or usage is unknown.

## Evaluate explicit configurations

```sh
questlock model evaluate --report "$HOME/questlock-workspace/NEW-fixture-report.json"
questlock model evaluate --suite "$HOME/questlock-workspace/suite.json" \
  --live --broker http://127.0.0.1:8080 \
  --report "$HOME/questlock-workspace/NEW-live-report.json" --langfuse
```

Report paths must be new; files are created privately and existing paths are refused. Fixture mode needs no provider or broker and rejects `--langfuse`. Live mode needs explicitly configured models and an intentionally fresh broker scope, so the first sample is a generation rather than a previous result hit. See [setup](model-workflows.md) and [local models](local-models.md) for broker credentials and installed Ollama weights.

The default suite has three simple and three complex public cases, with two explicit `qwen3.5:4b` configurations capped at 64 and 256 output tokens. Both enable local streaming measurements in live mode. It attempts one generation per case/configuration and one exact-snapshot reuse only after an error-free complete, durable result. A candidate must pass every declared gate. The reported selection applies only to that suite and environment and changes no subsequent model choice.

Checks support strict JSON, JSON equality at a pointer, and literal contains/not-contains tests. They do not establish general semantic quality or arbitrary code correctness. Fixture answers, token usage, and durations are supplied simulations, including simulated durability in memory. Live samples remain single observations, not a statistically significant benchmark, percentile estimate, or universal model ranking. The report records OS, architecture, CPU count, Go version, local weight digests, and uncontrolled load state.

## Validate a self-hosted endpoint

The disposable stack in `scripts/langfuse/compose.yml` needs no hosted account. Its six service images are pinned by content digest; Langfuse web and worker use v4.59.0. PostgreSQL, ClickHouse, Redis, and MinIO make this a resource-intensive local integration stack. It is not a lightweight component of the Questlock broker or a production deployment recipe.

From a checkout with Go meeting the module minimum and Docker on `PATH`, use a new private environment path. Compose must be available outside a user Docker configuration because the wrapper uses an empty private configuration. Install cleanup before startup:

```sh
(
  set -eu
  umask 077
  questlock_lf_env="$HOME/.local/share/questlock/langfuse/NEW-validation.env"
  mkdir -p "$HOME/.local/share/questlock/langfuse"
  python3 scripts/langfuse/prepare.py "$questlock_lf_env"
  questlock_lf_cleanup() {
    if python3 scripts/langfuse/run.py --env-file "$questlock_lf_env" down; then
      rm "$questlock_lf_env"
    else
      printf '%s\n' 'Cleanup failed; retain the private environment for a deliberate retry.' >&2
    fi
  }
  trap questlock_lf_cleanup EXIT
  python3 scripts/langfuse/run.py --env-file "$questlock_lf_env" config
  python3 scripts/langfuse/run.py --env-file "$questlock_lf_env" up
  python3 scripts/langfuse/run.py --env-file "$questlock_lf_env" smoke
)
```

Preparation refuses existing paths and does not print credentials. The wrapper requires the generated, owned, mode-0600 regular file and isolates each operation from ambient database, Langfuse, provider, proxy, Docker-context, and Go settings. Docker operations default to `unix:///var/run/docker.sock`; for another local daemon, add `--docker-socket unix:///absolute/path/to/local/docker.sock` to each Docker operation. Remote Docker contexts are unsupported. The fixed `config` operation validates quietly, `up` waits for health, and `down` removes the disposable volumes.

The smoke command sends deterministic metadata fixtures, then reads observations
and scores back to check span relationships, exclusive token buckets, content-free
fields, estimate/local-zero costs, zero-new-usage reuse, client-observed streaming
timing, and root quality scores. It calls no model. The
[actual self-hosted CI run](https://github.com/spfuzzylink/questlock/actions/runs/38632010394)
passed this readback contract against Langfuse v4.59.0; see the
[validation record](validation.md#v030-observations--2026-10-14). The stack binds
its web process to `0.0.0.0` inside the container and publishes only
`127.0.0.1:3000` on the host. CI removes its disposable services and volumes; this
does not populate or clean up your own Langfuse project.

Questlock's developer-facing direction is faster, affordable, measurable repeat tasks through a small Go runtime. Langfuse observability and an actual bounded worker are the new pieces here. A capability-aware OpenRouter companion adapter remains future work. Questlock does not intercept a full assistant session, train a model, bypass paid limits, or choose models automatically.

## Checks and human acceptance are separate measurements

**v0.4.0 experimental** adds a separate
[human outcome feedback loop](outcomes.md). An opted-in worker records its
content-free attempts; a reviewer explicitly supplies acceptance, rewrite,
rejection, or reopening events. Passing `questlock.quality` checks does not
populate this human-acceptance state. Every rewrite clears acceptance until a
later explicit acceptance identifies the final artifact digest.

A cohort report includes all its recorded attempts, including failures and
rejected work, with known generation-cost estimates and explicitly reported
incremental review effort. Unknown cost or event effort, a missing hourly rate,
or no currently accepted outcomes prevents a complete cost-per-accepted-task
number. Reports include each task's observed version and do not atomically freeze
the entire cohort. Model token counters and configured prices remain estimates
for delegated work, separate from surrounding hosted-assistant tokens and invoices.

Human feedback stays in Questlock's scoped broker artifacts. The existing
content-free Langfuse export is unchanged; no new review event, reviewer
identity, artifact content, or human acceptance score is exported. There is no
passive editor watcher or automated human-acceptance inference.
