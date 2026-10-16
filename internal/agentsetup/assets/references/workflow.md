# Task snapshot and local setup

The skill runs independently of the source checkout. Its helper uses `QUESTLOCK_BIN`, defaulting to `$HOME/.local/bin/questlock`. Call it with `sh scripts/run.sh plan|run|quest|revise-plan|revise-run|revise-quest|worker-run|worker-serve` from the skill directory. Set `QUESTLOCK_TOKEN` or `QUESTLOCK_TOKEN_FILE`; live commands read the default token file `$HOME/.local/share/questlock/agent-workflows/worker.token`. Ollama uses no provider key. Use `OPENAI_API_KEY` for OpenAI, `ANTHROPIC_API_KEY` for Claude, `OPENROUTER_API_KEY` for OpenRouter and `SGLANG_API_KEY` for authenticated SGLang. Load keys using the user's existing secret mechanism; never print them.

For a fresh local broker, provision once in a foreground shell:

```sh
umask 077
mkdir -p "$HOME/.local/share/questlock/agent-workflows"
(set -C; "$HOME/.local/bin/questlock" token create --agent agent-workflows --scope agent-workflows --db "$HOME/.local/share/questlock/agent-workflows/state.db" > "$HOME/.local/share/questlock/agent-workflows/worker.token")
"$HOME/.local/bin/questlock" serve --db "$HOME/.local/share/questlock/agent-workflows/state.db" --listen 127.0.0.1:8080
```

The token command refuses an existing output file. Stop if provisioning fails; investigate existing state instead of overwriting a credential. Once provisioned, restart using just the same `serve` command. Keep that process running while workers use it. This is not a boot service. For a shared remote broker, use its HTTPS URL via `--broker` and credentials in the intended scope; workers never share the SQLite file over a network.

## Exact presentation edits with no model

On v0.6.0+, adapt [checked-revision.json](checked-revision.json) when exact
literal changes satisfy the request. Do not ask another model to regenerate
already-reviewed text just to apply known formatting. An inline source plus its
SHA-256 supports offline preview; a shared source uses the immutable
`revision/v1/results/<request_digest>` key, exact version `1`, and output digest
from a prior successful revision in the same scope. These are alternatives.

```sh
sh scripts/run.sh revise-plan --request references/checked-revision.json
sh scripts/run.sh revise-run --request references/checked-revision.json --broker http://127.0.0.1:8080
sh scripts/run.sh revise-quest
```

The strict request has `schema: 1`, `task`, `revision`, `source`, 1–32 ordered
`edits`, and required `source_checks`/`checks` (1–64 each). Each edit specifies
nonempty `old`, `new` (empty is allowed), and positive `expected_matches`.
`source.sha256` always pins the unmodified UTF-8 bytes. Source/output are capped
at 256 KiB and each literal at 16 KiB. Check kinds are `json_valid`,
`json_equals`, `contains`, and `not_contains`. There is no fuzzy or semantic
matching. Source/check/count failure or uncertain publication withholds text
and stops; no model fallback runs.

`plan` returns `status: planned` without durability. Only a successful `run`
with `status: completed`, `durable: true`, passing checks, and `artifact`
confirms publication. The application reviews the result and applies `text`
to its destination; Questlock exposes no filesystem edit tool. A fresh edit
reports cost basis `deterministic_edit`, an exact repeat `reuse_zero`; both
report `generation_called: false`, known zero `this_run_usage` and provider
cost. Source-generation cost is not recharged, and avoided tokens are not
estimated. These are local text operations, not faster model TTFT.

The optional `track_outcome: true` creates content-free accounting in a separate
task/revision cohort. Require a recorded receipt and v0.6.0+ readers/writers for
that cohort; older outcome clients reject its additive `deterministic_edit`
basis. Human acceptance remains unset until explicit operator feedback. The
same policy applies through `questlock_revise` using the approved request path
inside the worker workspace, sharing admission and cancellation with
`questlock_delegate`. This helper does not change native client settings or
restart a running MCP process.

## Model spec fields

Specs are UTF-8 JSON objects, maximum 1MiB, with no unknown fields or trailing content. The included examples are fixtures. Replace their facts with the caller's approved current snapshot. For API jobs choose a currently supported, account-accessible model; for local jobs choose an installed local model.

| Field | Meaning |
| --- | --- |
| `task`, `revision`, `model` | Nonempty identifiers, maximum 128 bytes. Update revision for changed facts. |
| `provider` | `ollama`, `sglang`, `openrouter`, `openai`, `claude`, or `anthropic` (Claude alias). |
| `instructions`, `input` | Nonempty text; instructions express the task's behavior and input is the changing request. |
| `context` | Up to 1,000 chunks: unique `id`, nonempty `text`, optional `required`, `priority`, `stable`. |
| `max_context_bytes` | 0–524,288 bytes for rendered context. Required chunks must fit; optional chunks use descending priority, then ID, and stay whole. |
| `max_input_tokens` | 1–1,000,000 tokens covering the entire prepared request. OpenAI/Claude use provider preflight; Claude's count is an estimate. Ollama uses a byte/template/overhead estimate and checks actual usage afterward. |
| `max_output_tokens` | 16–1,000,000, still subject to model limits. Includes reported reasoning tokens. |
| `cache_mode` | `default`, or `prefix` for supported API explicit caching. Claude marks a stable prefix for 5-minute ephemeral caching; minimum cache lengths apply. Local Ollama requires `default`. |
| `reuse_for_seconds` | 0–86,400; default 0 disables completed-result reuse. Use a TTL only for a valid fixed snapshot. |
| `reasoning_effort` | Optional OpenAI setting, model-specific support; Claude rejects it. Omit unless deliberately selected. |
| `stream` | Optional `true` for local Ollama client-observed first-content/output timing; APIs reject it. Omit or use `false` for legacy non-streaming identity. |
| `prices` | API-only optional explicit USD per million tokens: `input_per_million`, `cache_read_per_million`, `cache_write_per_million`, `output_per_million`. Never assume fixture/current prices. Omit for Ollama. |
| `max_cost_usd`, `daily_budget_usd` | API-only optional positive caps, require accurate explicit prices. Daily ledger covers cooperating workers in one broker scope per UTC day. Omit for Ollama. |

The full prepared prompt cannot exceed 512KiB. Stable chunks form the prefix; changing chunks and task input follow it. Installed local Ollama, direct OpenAI, and direct Anthropic APIs are supported. v0.5.0 adds a dedicated `openrouter` adapter with a pinned upstream, declared input upper bound, and explicit cache-write accounting policy; it is not a direct OpenAI base-URL override. SGLang requires a `sglang` object declaring self-hosted ownership, deployment ID, cache namespace, profile and the same input cap. Standalone dense/HiCache native generation is supported for pinned SGLang 0.5.20; all PD instances, including HiSparse decode, are inspection only. Runtime KV hits remain separate from API billing usage. Read the packaged `docs/runtime-cache.md` and `deploy/sglang/README.md` before selecting these providers.

## Local inference and approved memory

Use [local-summary.json](local-summary.json) when provider credits must not be involved. Install Ollama separately, start its local server with cloud disabled, and pull a local model such as `qwen3.5:4b`. Questlock does not download models or start Ollama. The default endpoint is `http://127.0.0.1:11434`; an override must be a literal loopback HTTP address. Names or metadata indicating a cloud model are rejected. Missing or incompatible local models fail without a paid fallback. The installed model digest is part of result identity, so updating weights invalidates old reuse.

Use a dedicated broker scope such as `local-workflows` and a separate token file to isolate task permissions. This can share the same broker/database with API workers. Validated local jobs neither consume nor clear API dollar ledgers, and can continue while paid admission is frozen:

```sh
umask 077
mkdir -p "$HOME/.local/share/questlock/local-workflows"
(set -C; "$HOME/.local/bin/questlock" token create --agent local-workflows --scope local-workflows --db "$HOME/.local/share/questlock/agent-workflows/state.db" > "$HOME/.local/share/questlock/local-workflows/worker.token")
QUESTLOCK_TOKEN= QUESTLOCK_TOKEN_FILE="$HOME/.local/share/questlock/local-workflows/worker.token" sh scripts/run.sh run --spec PATH --timeout 5m
```

Provision only once; do not overwrite an existing token. The broker must be running against that same database. This setup changes no coding assistant model settings.

For local tasks, keep an `approved-memory` required stable chunk containing only relevant user-approved preferences and decisions, and required current-facts chunks with the exact evidence needed. Review their text in `plan`; update the task revision when these facts change. This is visible prompt context, not fine-tuning, weight changes, automatic history import, or a guarantee that omitted history is remembered.

Ollama preflight estimates the prepared UTF-8 bytes, model template/system bytes, and 512 tokens of overhead; it is not an exact tokenizer. The request context window is input cap plus output cap plus 512 and must fit the model's reported capacity. Local output is bounded with `num_predict`, thinking disabled, and no tools. Returned prompt/evaluation counts are actual runtime usage. If actual input exceeds the cap or output is incomplete, inspect the nonzero result rather than treating it as approved text. Local jobs omit USD accounting because there is no provider token meter; hardware, electricity, and runtime remain costs.

## Review output and recovery

For the bounded worker on v0.4.0+, `track_outcome: true` records content-free
attempt accounting without changing generation identity. It does not watch an
editor or infer acceptance. Use `questlock outcome show`, explicit versioned
`outcome feedback`, and `outcome report --cohort PATH` to connect later operator
acceptance, rewrites, and reported effort. Read the packaged `docs/outcomes.md`
for fields and examples. A failed registration returns exact recovery metadata;
reconcile that record instead of regenerating. Missing cost, human minutes,
valuation rate, or an accepted denominator leaves the applicable metric null.
No human acceptance event may be invented from a check or assistant review.

`plan` is offline and includes full prompt text. Inspect what will be sent and whether necessary context was omitted. Treat plan files as sensitive task data. `run` accepts `--broker`, `--api-base-url`, and positive `--timeout` (default 2 minutes). Keys belong only in environment variables, never flags or JSON.

Result JSON reports original `generation_usage`, actual `this_run_usage`, and `avoided_usage` for completed-result reuse. Input usage includes uncached tokens, cache reads, and cache writes; reasoning is already part of output. A prompt-cache discount is a provider outcome, not a guaranteed Questlock result hit. A known paid answer may still have inspection JSON when publication or settlement fails; `durable: false` means it was not durably stored. First incomplete responses also retain inspection JSON but exit nonzero. Require successful exit, `response.complete: true`, and `durable: true` before using text as an approved answer.

Pending/unknown jobs freeze generation. Inspect authenticated broker `GET /v1/artifacts` records under `inference/v1/jobs/` and the upstream request before intentionally changing revision. Uncertain generation retains the budget reservation. Never delete a pending claim just to get a new call through. Daily caps coordinate trusted workers with accurate prices; provider invoices remain billing evidence and unrelated account usage is outside this ledger.

Cost reservation uses the configured `max_input_tokens` at the largest input/cache rate and `max_output_tokens` at the output rate, then settles known usage. It does not use Claude's estimated count as a billing guarantee. Keep token caps realistic. Incorrect prices or provider usage above configured maxima can exceed an intended dollar cap; observed cap violations become nonreusable `limit_exceeded` records.
