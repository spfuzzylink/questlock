# Bounded model workflows

This guide covers the **v0.5.0 experimental release**, including runtime
cache inspection, the OpenRouter adapter, evaluation, observability, the bounded
worker, and [explicit human outcome feedback](outcomes.md). These commands are
available in the verified [v0.5.0 binary or a source build](installation.md).

Repeated handoffs, change summaries, and draft release notes often resend yesterday's discussion and regenerate an answer someone already requested. Questlock now makes those small, explicit jobs reproducible: keep necessary context, bound the response, and reuse a completed answer for the same task snapshot.

The adapters support direct OpenAI Responses, Anthropic Claude Messages,
OpenRouter text completions, installed local Ollama weights, and standalone
self-hosted SGLang dense/HiCache engines. Specs use `provider: "openai"`,
`"anthropic"` (`"claude"` is an alias), `"openrouter"`, `"ollama"`, or `"sglang"`.
Each generation is one text-only turn. HiSparse/PD profiles are inspection-only.
Choose an explicit model qualified for the task and review its result.

Questlock uses the instructions and context you supply. It does not train a model on your work or automatically read past conversations. It cannot bypass provider credits, billing, rate limits, or a Codex/Claude subscription limit. Direct API requests use provider API billing; the surrounding coding assistant can still consume its own plan allowance. Questlock usage counters measure only the delegated provider/local generation and exclude that assistant's prompt, tool, and context tokens.

## Start with an offline plan

Copy one of the examples and replace its revision, current changed-file summary, and verification facts:

```sh
cp examples/model-workflows/openai-handoff.json handoff.json
questlock model plan --spec handoff.json
questlock model quest
```

`plan` performs no network request and needs no API key, token, or broker. It prints the entire effective request, selected/omitted context IDs, context byte counts, and the offline request fingerprint as JSON. Treat that output as task data when redirecting it to a file. `model quest` exercises six scenarios offline; see [architecture and scenarios](model-architecture.md).

The examples are fixtures, not current model recommendations. Their model IDs, `gpt-4.1-mini` and `claude-sonnet-4-5`, are configurable. Check the provider's current availability and choose an accessible model before a live run. The Claude example enables prefix caching, but its short prefix may fall below the provider's minimum cache length. Do not add irrelevant text to reach a caching threshold.

All specs must be UTF-8 JSON objects of at most 1MiB. Unknown fields, invalid field types, additional JSON values, and trailing garbage are rejected. Commands accept flags only; `--help` and `-h` work without credentials.

## Run through a shared broker

Create a worker credential for a shared scope, then start the broker against the same database. Token creation prints the credential once. Keep it in a secret store outside your repository:

```sh
questlock token create --agent model-worker --scope engineering --db .questlock/state.db
questlock serve --db .questlock/state.db --listen 127.0.0.1:8080
```

In another terminal, load `QUESTLOCK_TOKEN` and the appropriate provider key into the process environment using your secret manager. The command deliberately has no API-key flag. With those environment variables available:

```sh
questlock model run --spec handoff.json --broker http://127.0.0.1:8080 --timeout 2m
questlock model run --spec examples/model-workflows/claude-handoff.json --timeout 2m
```

OpenAI uses `OPENAI_API_KEY`; Anthropic uses `ANTHROPIC_API_KEY`; OpenRouter uses
`OPENROUTER_API_KEY`. SGLang optionally reads `SGLANG_API_KEY` (required for a
remote HTTPS endpoint); literal-loopback local SGLang and Ollama need no provider
key. Provider keys remain in the worker and are not published to the broker.
The CLI also accepts a protected `QUESTLOCK_TOKEN_FILE` when `QUESTLOCK_TOKEN`
is absent. Keep all credentials outside specs. Workers sharing results/budgets
need credentials in the same broker scope; see [deployment](deployment.md).

`--timeout` must be positive and bounds the whole run. `--api-base-url` selects
an endpoint for the chosen adapter's own protocol. Select
`provider: "openrouter"` for its dedicated Chat Completions contract, not
`"openai"` with a
gateway URL. SGLang uses native runtime readback, tokenization and generation;
it is not an interchangeable OpenAI-compatible completion endpoint.

## Inspect an explicit runtime contract

The [OpenRouter spec](../examples/inference/openrouter-summary.json) includes
`openrouter.upstream_provider`, `input_token_upper_bound`, and the explicit
`cache_write_usage` policy. The bound reserves approved input capacity; it is
not a measured tokenizer count. One model/provider is requested with upstream
fallbacks disabled. Unknown or contradictory usage stops the job. Add current
prices and per-call/day caps before a paid run; worker escalation also needs
process `--allow-paid`.

The [SGLang spec](../deploy/sglang/task.json) includes `sglang.deployment_id`,
`cache_namespace`, `profile`, `self_hosted: true`, and `max_input_tokens`, which
must equal the task's top-level input cap. Replace the deployment ID with your
pinned runtime/model identity, use the server's served model alias, and match
the profile to its configuration.

```sh
questlock model runtime-quest
questlock model plan --spec deploy/sglang/task.json
questlock model runtime-inspect --spec deploy/sglang/task.json \
  --api-base-url http://127.0.0.1:30000
```

The quest is six offline protocol-fixture scenarios. Inspection requires an
already running configured SGLang v0.5.20 server but no broker or model call.
Its JSON contains `capabilities`, `evidence: "runtime_reported_configuration"`,
and `gpu_validated: false`. Only standalone dense/HiCache engines can generate;
HiSparse and PD prefill/decode instances remain inspection-only. Follow
[the runtime guide](runtime-cache.md) and [pinned profiles](../deploy/sglang/README.md)
for exact capabilities, setup, shared-cache boundaries, and GPU qualification.

## Make context selection explicit

`max_context_bytes` bounds context chunks. Required chunks are selected first; if they cannot fit, the request fails before generation. Optional chunks are considered by descending `priority`, with ties broken by ID. Selection keeps entire chunks and skips those that do not fit. It never silently summarizes or truncates a chunk.

Mark lasting preferences and stable reference text `stable: true`; they form the request prefix after the instructions. Current diffs, test results, and the task go in the changing input. The examples keep concise preferences and current changed-file facts required, while archival planning is optional. `max_input_tokens` covers the complete provider request, including instructions, prefix, changing input, and provider formatting. `max_output_tokens` bounds generation; for OpenAI this includes reasoning and other non-visible output tokens. [OpenAI output-token counting](https://developers.openai.com/api/docs/guides/token-counting) documents that behavior.

For OpenAI models that support it, optional `reasoning_effort: "low"` requests lower reasoning effort for a suitable small task. The adapter forwards the chosen effort to both count and generation; supported efforts vary by model, so verify the selected model's documentation. Omit the field to use the provider default. Claude specs must omit `reasoning_effort`; this adapter rejects the OpenAI setting rather than translating it into Claude thinking configuration.

For direct OpenAI/Claude, the runner submits the exact prepared request to the
provider's count endpoint: [OpenAI input-token counting](https://developers.openai.com/api/docs/guides/token-counting)
and [Claude token counting](https://platform.claude.com/docs/en/build-with-claude/token-counting).
Claude documents an estimate that can differ from actual usage. An unavailable
direct-provider count endpoint stops the run. Ollama uses its documented local
estimate; SGLang reuses native token IDs from the exact prepared prompt;
OpenRouter requires the declared operator bound. None silently truncates input
or assumes another provider's tokenizer is equivalent.

## Reuse results and account for costs

`reuse_for_seconds` is opt-in; absent or zero disables completed-result reuse. The examples allow reuse for 3,600 seconds. Reuse requires the same effective request, task/revision, provider, model, and provider endpoint, and an unexpired completed answer. Update `revision` whenever external facts change, even if the prompt wording stays the same. Never reuse a previous snapshot for a live status question.

Concurrent identical jobs claim a durable broker record before generation. A pending job is not automatically retried. A network timeout or process crash can leave generation outcome unknown; inspect the broker's `inference/v1/jobs/` artifacts via authenticated `GET /v1/artifacts` and investigate the upstream request before intentionally changing `revision` to attempt again, because a new attempt may duplicate billing. The offline plan fingerprint describes the request; the stored/result fingerprint also includes the configured provider endpoint. An incomplete or truncated response is stored as nonreusable and returns an error. The output cap remains the original cap, including any reasoning tokens consumed.

Provider prompt caching is separate from Questlock result reuse. OpenAI's default cache policy is provider-managed; the Anthropic `prefix` mode marks the stable prefix for ephemeral caching. Cache eligibility, hits, lifetime, and pricing remain provider decisions. See [OpenAI prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching) and [Claude prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching).

Results separate `generation_usage`, `this_run_usage`, and `avoided_usage`. A Questlock cache hit has zero generation usage for this run; avoided usage describes the original generation that was not repeated. Anthropic's total input includes uncached input, cache reads, and cache writes. Reasoning is part of output and is not charged twice. A known paid answer can still have inspection JSON when publication, incomplete-output, or budget-settlement errors cause a nonzero exit. `durable: false` means an in-memory answer was not durably stored. Require a successful command exit, `response.complete: true`, and `durable: true` before using generated text.

There is no built-in price list. If you need dollar estimates or budget enforcement, supply `prices` with USD per million tokens for `input_per_million`, `cache_read_per_million`, `cache_write_per_million`, and `output_per_million`. Use current account/provider prices and the correct cache write policy. `max_cost_usd` optionally caps one request; `daily_budget_usd` optionally uses a shared UTC-day ledger in the broker scope. Both need explicit prices. Preflight reserves the configured `max_input_tokens` at the largest configured input/cache rate plus `max_output_tokens` at the output rate, rather than relying on a count estimate as a bill ceiling. Set realistic token caps: unused reservation is settled after known generation usage. Reported cap violations are stored as `limit_exceeded` and cannot be reused. Incorrect prices or provider usage above those configured maxima can still exceed the intended dollar cap.

Once a scope has a daily ledger, cooperating hosted API workers must use its
same daily cap and supply accurate prices. Validated Ollama and explicitly
self-hosted SGLang jobs omit USD fields and leave that ledger untouched, even
when paid admission is frozen. Self-hosting still costs hardware and operations.
SGLang KV hit counters are runtime metrics, not billable input-cache discounts.
Separate scopes can isolate permission/privacy; they are not required to
separate local/paid accounting. These controls coordinate trusted workers, not
the whole provider account. Provider invoices remain final billing evidence.

## Measure and gate a bounded task

`model run` returns current-run phase and usage measurements. Set `"stream": true` only in a local Ollama spec for client-observed first-content and output-content intervals. A reused answer has no fresh generation or TTFT. Optional `--langfuse --langfuse-timeout 2s` exports content-free metadata after stdout output, with receipts on stderr; supply Langfuse keys and endpoint in the environment. Export failure never retries generation. See [the timing and export contract](inference-observability.md).

```sh
questlock model evaluate --report "$HOME/questlock-workspace/NEW-fixture-report.json"
questlock model evaluate --live --broker http://127.0.0.1:8080 \
  --report "$HOME/questlock-workspace/NEW-live-report.json"
questlock worker run --request "$HOME/questlock-workspace/approved-task.json" \
  --broker http://127.0.0.1:8080
```

Evaluation defaults to six simulated public cases. Live mode needs an intentionally fresh broker scope and explicit installed/configured models. Report files are private and existing paths are refused. JSON/literal checks establish only the supplied expectations; no eligible selection is a nonzero result. The [recorded local run](measurements-v0.3.0.md) qualified neither configuration.

The [delegation request](delegation.md) declares local and optional escalation tiers, budgets, context, TTL, and checks. Only an error-free complete, durable, valid-known-usage answer failing those checks can reach the one explicit next tier. Hosted escalation also requires process `--allow-paid` and explicit prices/caps. `worker serve --workspace ROOT` exposes only `questlock_delegate` with `request_path`; it does not discover or edit files, execute commands, or proxy an assistant session.

## Use from Codex or Claude Code

Install the embedded workflow skill with `questlock model install --agents codex,claude` (both are the default). It installs the running binary in `~/.local/bin/questlock` and discoverable skills for the selected clients; see [installation](installation.md). The skill's `sh scripts/run.sh` helper works independently of the checkout. Existing custom skills need an explicit `--replace`. This installation does not install either coding client or change their model defaults.

For a fresh local workflow broker, provision a private token once and start the service in a foreground terminal:

```sh
umask 077
mkdir -p "$HOME/.local/share/questlock/agent-workflows"
(set -C; "$HOME/.local/bin/questlock" token create --agent agent-workflows --scope agent-workflows --db "$HOME/.local/share/questlock/agent-workflows/state.db" > "$HOME/.local/share/questlock/agent-workflows/worker.token")
"$HOME/.local/bin/questlock" serve --db "$HOME/.local/share/questlock/agent-workflows/state.db" --listen 127.0.0.1:8080
```

Stop if provisioning fails; the token-file command refuses overwrite. For an already provisioned workflow, use just the same `serve` command. The installed helper reads this token file for `run`, `worker-run`, or `worker-serve`, or preserves an explicitly supplied `QUESTLOCK_TOKEN`. Set a hosted provider key separately through your secret manager. The token and database are not installed by `model install`.

Ask the coding assistant to gather current facts into a spec, inspect `questlock model plan`, run the bounded job, and read `response.text` from its JSON result. Suitable jobs include summarizing a fixed diff, producing a short handoff, or drafting release text from verified facts. Keep complex diagnosis and final review with the assistant when the cheaper model does not meet the quality bar.

Example instruction to either assistant:

> Prepare an engineering handoff spec with current changed files and confirmed checks. Keep my concise preferences required and old discussion optional. Run Questlock's offline plan, then use the configured provider to generate at most six bullets. Reuse only this exact revision's completed result and report usage from the JSON response.

This integration is an explicit CLI or bounded MCP tool call within the assistant's workflow. It does not proxy Codex's entire agent protocol, switch the assistant's global model, or suppress tokens needed to prepare and review the task. The surrounding hosted chat retains its own allowance and billing. CLI/fake-server checks demonstrate behavior; real provider savings and billing require a live run with configured credentials.
