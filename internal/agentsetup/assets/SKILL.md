---
name: questlock
description: Run bounded engineering summaries, handoffs, and extraction with Questlock using approved compact memory, local Ollama inference or direct APIs, explicit context limits, and snapshot result reuse.
---

# Questlock

Use Questlock for a small text-only subtask whose inputs and completion criteria can be captured in one explicit snapshot. Preserve the user's provider and model choice. When the goal is zero provider bills or independence from credits, prefer a suitable installed local Ollama model for routine summaries, handoffs, and extraction. If it is unavailable or inadequate, report that limitation; do not silently fall back to a paid provider. Keep complex investigation and final judgment with the coding assistant unless the user deliberately chooses a local agent.

Read [references/workflow.md](references/workflow.md) for the spec, local broker setup, and configuration. Adapt [references/local-summary.json](references/local-summary.json) for a local job, or [references/openai-summary.json](references/openai-summary.json) / [references/claude-summary.json](references/claude-summary.json) for an authorized API job. Gather current facts and the user's approved preferences; do not import entire conversation histories. Treat that compact approved memory as visible reference data, not model training. Put necessary preferences and live change facts in required context chunks, optional older material in chunks with explicit priority, and preserve relevant constraints when reducing context.

Run `sh scripts/run.sh plan --spec PATH` from this skill directory. Review the full selected request and omitted chunks before a live run. Required chunks must fit, and optional chunks remain whole; Questlock performs no hidden summarization. Do not place credentials in the spec, prompt, command flags, or output files. Send task content only to the provider endpoint appropriate for the user's authorized work and sensitivity.

Run `sh scripts/run.sh run --spec PATH` for the bounded job. The helper reads the broker token only for `run`; Ollama needs no provider key, and API keys stay in the environment. Use a local-only broker scope for local jobs so API dollar ledgers do not mix with unmetered inference. Require successful exit, `response.complete: true`, and `durable: true` before using `response.text`. Check that the answer meets the task's facts and completion criteria. Report measured this-run usage and Questlock result reuse when useful; do not invent savings percentages.

Result reuse is opt-in for an exact task snapshot whose answer remains valid. Update `revision` when external facts or approved memory change. Use generation for text, not to replay external actions. A pending, incomplete, or unknown result is a stopping condition: inspect broker/upstream state before deliberately changing revision, since another attempt could duplicate computation or billing. Never automatically retry paid generation or fall back to a different provider/model after an uncertain outcome.

`sh scripts/run.sh quest` validates six scenarios offline. Local Ollama generation runs on the user's hardware without provider token metering; hardware, electricity, and time still cost resources. Questlock rejects cloud models and non-loopback Ollama endpoints and does not fall back. It does not train weights, proxy the coding assistant's whole agent protocol, or change its global model. Direct API jobs have separate billing; the surrounding hosted assistant still uses its normal allowance.
