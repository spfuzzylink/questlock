---
name: questlock
description: Apply checked literal revisions without a model, reuse exact task results, or delegate bounded extraction, summaries, and handoffs through Questlock with measured usage. Keep investigation and final judgment with the calling agent.
---

# Questlock

When MCP is connected, call `questlock_status` with `{}` first. Use its
`request_root` as the absolute directory for approved requests; check
`runtime_version`, metadata `schema`, `request_schemas`, `checks.kinds`,
`profile`, and `policy.allow_paid` before choosing a request. Delegation requests
are currently unversioned; revision requests require `schema: 1`. The profile is
an operator label, not an account or credential. `readiness.request_root: opened`
and `readiness.worker: ready|busy` describe this process only; broker/providers
remain `not_checked`. Discovery reads no task, client configuration, environment
or credentials and makes no network/model call. It stays available while busy.
If the tool or a required capability is absent, report the compatibility gap;
do not infer the connected runtime from this skill or replace client settings.
Keep an older worker's existing tools usable until a deliberate restart/update.

Save the complete reviewed request with the caller's file tools inside that
root, then pass its absolute path, such as
`/absolute/discovered/request-root/approved-task.json`, to the chosen task tool.
Do not guess a broker port or request directory from setup examples.

For recurring writing, `workflow_configs.writing` names the relative operator
marker below `request_root`; it does not assert that a marker exists. When the
user asks to initialize one, run `questlock writing init --workspace ROOT
--reference /absolute/approved-reference.json --model INSTALLED_MODEL`. Later,
`questlock writing plan --workspace ROOT --evidence /absolute/current-evidence.json
--request writing-request.json` creates the bounded request and provenance
manifest without inference. Read and review the complete request before
`questlock_delegate`; keep its structured result for `writing record` and use
`writing show` for indexed history. Use each subcommand's `--help` for its flags.
Read only the selected reference/current evidence; never import unrelated chat
history. Discovery does not authorize initializing or changing a reference.

Prefer a checked deterministic revision when the user asks for an exact
presentation or mechanical edit to text already available: emphasize a named
sentence, replace an approved literal, or update a precisely selected JSON
value. Adapt [references/checked-revision.json](references/checked-revision.json).
Pin the exact source bytes with SHA-256, specify counted literal edits, and
require source/output checks. Review the whole request, then use
`sh scripts/run.sh revise-plan --request PATH` and
`sh scripts/run.sh revise-run --request PATH`. Inline planning is offline;
publication uses the scoped broker. `questlock_revise`, when available, accepts
only that approved request path inside the configured worker workspace. It
shares the worker's single-active-task boundary and performs no file edits.

Require successful exit, `status: completed`, `durable: true`, passing source
and output checks, and a confirmed immutable artifact before using revised
`text`. A revision calls no model; known this-run usage and provider charge
are zero. Fresh revisions use `deterministic_edit`; exact reuse uses
`reuse_zero`. Historical model costs remain historical; do not guess token
savings or model TTFT. A failed source hash, count, check, or uncertain publish
withholds text and stops. Do not silently retry with a model. For further
reasoning or fallback, construct and review a separate bounded request under
the user's existing authorization. Literal
checks cannot authorize factual changes or establish human acceptance.

For changed reasoning, new facts, or an open-ended rewrite, investigate and
review the task first. Reuse an exact still-valid model snapshot where
appropriate; otherwise use a bounded local task under the policy below.

Use Questlock for a small text-only subtask whose inputs and completion criteria can be captured in one explicit snapshot. Preserve the user's provider and model choice. When the goal is zero provider bills or independence from credits, prefer a suitable installed local Ollama model for routine summaries, handoffs, and extraction. If it is unavailable or inadequate, report that limitation; do not silently fall back to a paid provider. Keep complex investigation and final judgment with the coding assistant unless the user deliberately chooses a local agent.

Read [references/workflow.md](references/workflow.md) for the spec, local broker setup, and configuration. Adapt [references/local-summary.json](references/local-summary.json) for a local job, or [references/openai-summary.json](references/openai-summary.json) / [references/claude-summary.json](references/claude-summary.json) for an authorized API job. Gather current facts and the user's approved preferences; do not import entire conversation histories. Treat that compact approved memory as visible reference data, not model training. Put necessary preferences and live change facts in required context chunks, optional older material in chunks with explicit priority, and preserve relevant constraints when reducing context.

Run `sh scripts/run.sh plan --spec PATH` from this skill directory. Review the full selected request and omitted chunks before a live run. Required chunks must fit, and optional chunks remain whole; Questlock performs no hidden summarization. Do not place credentials in the spec, prompt, command flags, or output files. Send task content only to the provider endpoint appropriate for the user's authorized work and sensitivity.

For tasks with deterministic acceptance criteria, adapt [references/local-worker.json](references/local-worker.json) and run `sh scripts/run.sh worker-run --request PATH`. Approve the complete prompt, context, tier caps, and checks before calling. Use a single local tier by default. Require successful exit and `status: completed` before using the selected `response.text`, then review its meaning. The worker withholds failed answers. JSON/literal checks prove only the declared expectations, not general reasoning quality. A complete, durable answer can still be wrong.

For v0.4.0+ bounded development tasks, use `track_outcome: true` in the approved worker request to record invocation and attempt metadata in the existing shared broker scope. v0.6.0 adds the same opt-in for deterministic revisions: use a separate task/revision cohort and require every reader/writer of it to be v0.6.0+, because older clients do not recognize the new `deterministic_edit` cost basis. Require `outcome_tracking.status: recorded` for accounting coverage. If registration fails, recover the exact returned `outcome_tracking.record` through `questlock outcome record`; never repeat inference to repair accounting. Tracking does not change the generation fingerprint or revision plan identity. Keep failures and unfinished work in an explicit `questlock outcome report --cohort PATH` cohort rather than selecting only successes. Checks and assistant review never establish human acceptance. Submit `questlock outcome feedback` only for an explicit user/operator acceptance or rewrite signal, with its reviewed version, a stable event ID, and known incremental human minutes or null. Do not fabricate human effort, treat AI review as human review, or silently value missing effort at zero. Rewrites clear acceptance until explicit reacceptance. A rate supplied to the report values recorded effort; it is not an invoice. An already running older MCP process needs its normal controlled restart before exposing v0.6.0 revision capabilities; installing this skill alone does not restart it.

For a reviewed summary without machine-checkable acceptance criteria, use `sh scripts/run.sh run --spec PATH`. Require successful exit, `response.complete: true`, and `durable: true`, then check the facts before accepting `response.text`. Helpers read the private broker token only for live commands; Ollama needs no provider key. Local jobs neither consume nor clear an API dollar ledger. Separate scopes can isolate permissions and task content. Report measured this-run usage and result reuse when useful; do not invent invoice savings.

If the `questlock_delegate` MCP tool is available, call it with only the path to the approved worker JSON inside its configured workspace. It invokes the same bounded policy as `worker-run`. No discovery, shell execution, or file editing is delegated. One active task is admitted; `worker_busy` is a stopping condition for that call, not permission to retry generation. Hosted escalation requires an explicit priced policy and a process started with `--allow-paid`; do not enable this merely to make a failing local task pass.

Use `word_count`, `character_count`, or `paragraph_count` only when their literal
counting rules match the task, with inclusive `min` and/or `max` bounds. The
workflow reference defines those rules. A stopped request may return checks as
`status: not_evaluated`; absent output has no observed count. On a budget stop,
report the safe numeric `budget_diagnostic` and its `count_basis` rather than
calling an estimate runtime usage. Do not raise caps or regenerate automatically.

Result reuse is opt-in for an exact task snapshot whose answer remains valid. Update `revision` when external facts or approved memory change. Use generation for text, not to replay external actions. A pending, incomplete, or unknown result is a stopping condition: inspect broker/upstream state before deliberately changing revision, since another attempt could duplicate computation or billing. Never automatically retry paid generation or fall back to a different provider/model after an uncertain outcome.

`sh scripts/run.sh quest` validates six model-coordination scenarios offline; `sh scripts/run.sh revise-quest` validates three simple and three complex deterministic-revision scenarios without a model. `questlock model evaluate` compares six deterministic public fixtures with negative quality controls; simulated timings are not benchmarks. Use `model evaluate --live` only for an approved fresh evaluation scope and retain failures without rerunning to force a selection. Optional `stream: true` on Ollama measures client-observed first content and output intervals; non-streaming and reused results have no new generation timing.

Local inference runs on the user's hardware without provider token metering; hardware, electricity, and time remain costs. Questlock rejects cloud models and non-loopback Ollama endpoints. It does not train weights, proxy the whole assistant protocol, or change the chat's global model. The surrounding hosted assistant still consumes its normal allowance. Available skills and tools make bounded delegation possible in future chats; they do not guarantee automatic use in every conversation.

For optional semantic observations on a compatible worker, check
`request_schemas.semantic_shadow_modes` for `shadow`. Only add an explicitly
reviewed `semantic_shadow` policy to a bounded delegation request. Shadow mode
records local embedding candidates after checked fresh generation; it never
serves an alternative answer or avoids generation. Exact reuse skips embeddings.
Keep embedding usage separate from generation usage, retain misses and failures,
and never interpret a similarity score as answer validity or human acceptance.
Workers do not download missing weights or enable paid fallback.
