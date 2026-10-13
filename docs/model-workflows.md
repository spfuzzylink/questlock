# Bounded model workflows

Repeated handoffs, change summaries, and draft release notes often resend yesterday's discussion and regenerate an answer someone already requested. Questlock now makes those small, explicit jobs reproducible: keep necessary context, bound the response, and reuse a completed answer for the same task snapshot.

The adapters support direct OpenAI Responses requests and Anthropic Claude Messages requests. Specs use `provider: "openai"` or `provider: "anthropic"`; `"claude"` is an alias for Anthropic. Each request is a single text-generation turn. Pick a provider/model that meets the quality bar for the task, and review its result before acting on it.

Questlock uses the instructions and context you supply. It does not train a model on your work or automatically read past conversations. It cannot bypass provider credits, billing, rate limits, or a Codex/Claude subscription limit. Direct API requests use provider API billing; the surrounding coding assistant can still consume its own plan allowance.

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

OpenAI uses `OPENAI_API_KEY`; Anthropic uses `ANTHROPIC_API_KEY`. Provider keys remain in the worker and are not published to the broker. The broker stores task coordination, output, and usage. Workers that should share results and budgets need credentials in the same broker scope; see [deployment](deployment.md) for the shared service/storage boundary.

`--timeout` must be positive and bounds the whole run. `--api-base-url` is an explicit endpoint override; it requires the selected adapter's generation and token-counting routes. An OpenRouter route is a future adapter candidate, not a supported substitution: this implementation depends on provider-specific count preflight and usage semantics. Do not treat a compatible completion endpoint as sufficient compatibility.

## Make context selection explicit

`max_context_bytes` bounds context chunks. Required chunks are selected first; if they cannot fit, the request fails before generation. Optional chunks are considered by descending `priority`, with ties broken by ID. Selection keeps entire chunks and skips those that do not fit. It never silently summarizes or truncates a chunk.

Mark lasting preferences and stable reference text `stable: true`; they form the request prefix after the instructions. Current diffs, test results, and the task go in the changing input. The examples keep concise preferences and current changed-file facts required, while archival planning is optional. `max_input_tokens` covers the complete provider request, including instructions, prefix, changing input, and provider formatting. `max_output_tokens` bounds generation; for OpenAI this includes reasoning and other non-visible output tokens. [OpenAI output-token counting](https://developers.openai.com/api/docs/guides/token-counting) documents that behavior.

For OpenAI models that support it, optional `reasoning_effort: "low"` requests lower reasoning effort for a suitable small task. The adapter forwards the chosen effort to both count and generation; supported efforts vary by model, so verify the selected model's documentation. Omit the field to use the provider default. Claude specs must omit `reasoning_effort`; this adapter rejects the OpenAI setting rather than translating it into Claude thinking configuration.

The runner submits the exact prepared request to the provider's count endpoint before generation: [OpenAI input-token counting](https://developers.openai.com/api/docs/guides/token-counting) and [Claude token counting](https://platform.claude.com/docs/en/build-with-claude/token-counting). Anthropic documents its count as an estimate that can differ slightly from actual usage, so a Claude preflight is not an absolute billing or token guarantee. An unavailable count endpoint fails the run; there is no character-count fallback or silent truncation.

## Reuse results and account for costs

`reuse_for_seconds` is opt-in; absent or zero disables completed-result reuse. The examples allow reuse for 3,600 seconds. Reuse requires the same effective request, task/revision, provider, model, and provider endpoint, and an unexpired completed answer. Update `revision` whenever external facts change, even if the prompt wording stays the same. Never reuse a previous snapshot for a live status question.

Concurrent identical jobs claim a durable broker record before generation. A pending job is not automatically retried. A network timeout or process crash can leave generation outcome unknown; inspect the broker's `inference/v1/jobs/` artifacts via authenticated `GET /v1/artifacts` and investigate the upstream request before intentionally changing `revision` to attempt again, because a new attempt may duplicate billing. The offline plan fingerprint describes the request; the stored/result fingerprint also includes the configured provider endpoint. An incomplete or truncated response is stored as nonreusable and returns an error. The output cap remains the original cap, including any reasoning tokens consumed.

Provider prompt caching is separate from Questlock result reuse. OpenAI's default cache policy is provider-managed; the Anthropic `prefix` mode marks the stable prefix for ephemeral caching. Cache eligibility, hits, lifetime, and pricing remain provider decisions. See [OpenAI prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching) and [Claude prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching).

Results separate `generation_usage`, `this_run_usage`, and `avoided_usage`. A Questlock cache hit has zero generation usage for this run; avoided usage describes the original generation that was not repeated. Anthropic's total input includes uncached input, cache reads, and cache writes. Reasoning is part of output and is not charged twice. A known paid answer can still have inspection JSON when publication, incomplete-output, or budget-settlement errors cause a nonzero exit. `durable: false` means an in-memory answer was not durably stored. Require a successful command exit, `response.complete: true`, and `durable: true` before using generated text.

There is no built-in price list. If you need dollar estimates or budget enforcement, supply `prices` with USD per million tokens for `input_per_million`, `cache_read_per_million`, `cache_write_per_million`, and `output_per_million`. Use current account/provider prices and the correct cache write policy. `max_cost_usd` optionally caps one request; `daily_budget_usd` optionally uses a shared UTC-day ledger in the broker scope. Both need explicit prices. Preflight reserves the configured `max_input_tokens` at the largest configured input/cache rate plus `max_output_tokens` at the output rate, rather than relying on a count estimate as a bill ceiling. Set realistic token caps: unused reservation is settled after known generation usage. Reported cap violations are stored as `limit_exceeded` and cannot be reused. Incorrect prices or provider usage above those configured maxima can still exceed the intended dollar cap.

Once a scope has a daily ledger, all cooperating workers must use its same daily cap and supply accurate prices for their model. This coordinates trusted workers; arbitrary clients with write access can modify broker artifacts, so it is not an account-wide billing restriction. It covers this Questlock workflow, not unrelated API use or the entire provider account. Provider usage and invoices remain the final billing evidence.

## Use from Codex or Claude Code

Install the embedded workflow skill with `questlock model install --agents codex,claude` (both are the default). It installs the running binary in `~/.local/bin/questlock` and discoverable skills for the selected clients; see [installation](installation.md). The skill's `sh scripts/run.sh` helper works independently of the checkout. Existing custom skills need an explicit `--replace`. This installation does not install either coding client or change their model defaults.

For a fresh local workflow broker, provision a private token once and start the service in a foreground terminal:

```sh
umask 077
mkdir -p "$HOME/.local/share/questlock/agent-workflows"
(set -C; "$HOME/.local/bin/questlock" token create --agent agent-workflows --scope agent-workflows --db "$HOME/.local/share/questlock/agent-workflows/state.db" > "$HOME/.local/share/questlock/agent-workflows/worker.token")
"$HOME/.local/bin/questlock" serve --db "$HOME/.local/share/questlock/agent-workflows/state.db" --listen 127.0.0.1:8080
```

Stop if provisioning fails; the token-file command refuses overwrite. For an already provisioned workflow, use just the same `serve` command. The installed helper reads this token file only for `run`, or preserves an explicitly supplied `QUESTLOCK_TOKEN`. Set the provider key separately through your secret manager. The token and database are not installed by `model install`.

Ask the coding assistant to gather current facts into a spec, inspect `questlock model plan`, run the bounded job, and read `response.text` from its JSON result. Suitable jobs include summarizing a fixed diff, producing a short handoff, or drafting release text from verified facts. Keep complex diagnosis and final review with the assistant when the cheaper model does not meet the quality bar.

Example instruction to either assistant:

> Prepare an engineering handoff spec with current changed files and confirmed checks. Keep my concise preferences required and old discussion optional. Run Questlock's offline plan, then use the configured provider to generate at most six bullets. Reuse only this exact revision's completed result and report usage from the JSON response.

This integration is a CLI tool call within the assistant's workflow. It does not proxy Codex's entire agent protocol, switch the assistant's global model, or suppress the tokens needed by the assistant to prepare and review the task. CLI/fake-server checks demonstrate behavior; real provider savings and billing require a live run with configured credentials.
