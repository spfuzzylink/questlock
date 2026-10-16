# Bounded local-first delegation

## Prefer exact revisions before generating (v0.6.0 candidate)

For a precisely specified presentation or literal edit of reviewed text, use
`questlock revise plan|run --request PATH` or MCP `questlock_revise`. The request
pins the source hash or exact shared parent and declares counted replacements
and source/output checks. This deterministic path calls no model. The CLI/MCP
withholds failed or uncertain results; a failure does not authorize generation
fallback. For further reasoning, the caller constructs and reviews a separate
bounded request under the user's existing authorization.

The two MCP tools share path confinement, one-active-task admission and
cancellation. `questlock_revise` reads only the explicit request inside the
worker workspace, returns checked text/provenance, and edits no files. Existing
v0.5.0 clients remain usable; upgrade/restart a worker deliberately to discover
the new tool. Installing a skill does not restart the process or rewrite client
configuration. [Full contract, examples and accounting](checked-revisions.md).

The **v0.5.0 experimental source candidate** retains bounded delegation and
opt-in outcome tracking, while adding standalone self-hosted SGLang dense/HiCache
tiers and a dedicated OpenRouter paid tier. The `delegation` Go package and
`questlock worker` execute one approved task with at most one explicitly
configured escalation. Context, models, reuse, budgets and checks stay reviewable.

The surrounding assistant prepares the request and reviews the passing answer.
The worker provides no shell execution, workspace editing, file discovery,
model training, whole-session proxy, or automatic model selection. The
[runtime guide](runtime-cache.md) defines each adapter's distinct contract;
HiSparse/PD engines are inspection-only, not generation tiers. Questlock measures the
delegated generation; it excludes the surrounding hosted assistant's prompt,
tool, and context tokens. Preparing the request and reviewing its result can
still consume that assistant's normal allowance.

## Declare the complete request

Save an approved UTF-8 JSON file of at most 1MiB inside your worker workspace. `spec` holds only task identity, revision, instructions, input, and supplied context. Each tier's `spec` declares its provider/model and resource settings. Every tier needs a positive reuse TTL. Requests reject unknown fields and duplicate keys; policies require 1–64 deterministic checks.

For example, save this as `$HOME/questlock-workspace/approved-task.json`. The model must already be installed; these settings are an explicit example, not a model recommendation.

```json
{
  "spec": {
    "task": "ticket-extraction",
    "revision": "approved-snapshot-1",
    "instructions": "Return only a JSON object with issue and deadline strings. Copy the supplied values exactly.",
    "input": "Ticket OPS-17 must be completed on 2030-04-03."
  },
  "policy": {
    "local": {
      "id": "local-compact",
      "spec": {
        "provider": "ollama",
        "model": "qwen3.5:4b",
        "max_context_bytes": 8192,
        "max_input_tokens": 8192,
        "max_output_tokens": 64,
        "reuse_for_seconds": 3600
      }
    },
    "escalation": {
      "id": "local-roomy",
      "spec": {
        "provider": "ollama",
        "model": "qwen3.5:4b",
        "max_context_bytes": 8192,
        "max_input_tokens": 8192,
        "max_output_tokens": 256,
        "reuse_for_seconds": 3600
      }
    }
  },
  "checks": [
    {"id": "strict-json", "kind": "json_valid"},
    {
      "id": "expected-values",
      "kind": "json_equals",
      "expected": {"issue": "OPS-17", "deadline": "2030-04-03"}
    }
  ]
}
```

Omit `escalation` for a single local tier. Optional `"stream": true` belongs in a local tier spec and enables the [client-observed timing](inference-observability.md). The policy permits at most 32,768 input tokens, 4,096 output tokens, and 131,072 context bytes per tier; the inference planner also validates minimums, TTL, and effective request bounds.

The first tier must use Ollama or an explicitly self-hosted SGLang deployment.
A SGLang tier needs the complete nested `sglang` contract and a standalone
dense/HiCache engine; its observed runtime identity is rechecked before reuse.
A hosted escalation may name direct OpenAI, Anthropic or OpenRouter, with
explicit input/cache/output prices and positive `max_cost_usd` and
`daily_budget_usd`. OpenRouter also needs the explicit route/input-bound/cache
policy in its nested `openrouter` object. See [both schema examples](runtime-cache.md).

The process must start with `--allow-paid`; this authority cannot come from
request JSON, an MCP tool argument, or an environment variable. A configured
hosted tier without permission is rejected before either tier runs. Prices are
caller estimates and the ledger covers cooperating workers, not the provider
account. OpenRouter's operator input bound is not a measured token count.

Both tiers use the worker's one broker scope. Validated Ollama and explicitly
self-hosted SGLang jobs omit USD fields and neither consume nor clear the API
dollar ledger. They can continue when paid admission is blocked. Hardware,
power, memory and time remain costs outside that ledger. SGLang KV hit metrics
do not become billable API cache discounts or zero-token generation.

## Run or expose the worker

Provision the broker and its private scope credential using [the setup guide](model-workflows.md).
Keep secrets outside the request file: use `QUESTLOCK_TOKEN`, or
`QUESTLOCK_TOKEN_FILE` below. Provider credentials stay in the process as
`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `OPENROUTER_API_KEY`, or optional
`SGLANG_API_KEY` (required for remote HTTPS SGLang).

```sh
export QUESTLOCK_TOKEN_FILE="$HOME/.local/share/questlock/agent-workflows/worker.token"
questlock worker run --request "$HOME/questlock-workspace/approved-task.json" \
  --broker http://127.0.0.1:8080
```

For an MCP client, register a stdio server running this command with the same environment:

```sh
questlock worker serve --workspace "$HOME/questlock-workspace" \
  --broker http://127.0.0.1:8080
```

The only tool is `questlock_delegate`, and its only argument is:

```json
{"request_path":"approved-task.json"}
```

Paths may be relative to the declared workspace or absolute within it. Traversal, symlink path components, nonregular files, and oversized files are rejected. The worker reads only the named request file. MCP admits one active task per process; concurrent calls fail immediately with `worker_busy`, without executing or retrying. Calls honor cancellation and return controlled error categories. A normal CLI `run` reads the exact path explicitly supplied by the operator; the workspace boundary applies to `serve`.

Both commands default to a two-minute per-task bound, with `--timeout` limited
to ten minutes. Process flags `--ollama-url`, `--sglang-url`, and
`--openrouter-url` select the respective adapters' endpoints; request JSON and
MCP arguments cannot replace them. SGLang plaintext HTTP requires literal
loopback; remote HTTPS needs an explicit key. Add
`--langfuse --langfuse-timeout 2s` for [content-free telemetry](inference-observability.md).
CLI JSON reaches stdout before export receipts on stderr. MCP exports silently
through a background queue of 16 batches with a five-second shutdown grace;
full queues drop telemetry. Export is best effort and never retries generation.

## Register with native Codex or Claude Code

Install each coding client separately. `model install` installs Questlock's skills
and binary; it does not register an MCP server. Native stdio registration is
supported by [Codex's official MCP guide](https://learn.chatgpt.com/docs/extend/mcp?surface=cli)
and [Claude Code's official MCP guide](https://code.claude.com/docs/en/mcp).

Before changing active client settings, make a private complete candidate copy of
the existing configuration in an isolated home. Preserve unrelated settings and
command arrays such as `notify`. Use new private candidate directories outside
the repository, then point these variables at those complete copies. The Codex
path is the directory containing the copied `config.toml`; the Claude path is an
isolated home containing the copied `.claude.json` and settings directory.

```sh
questlock_codex_candidate="/absolute/path/to/private/complete-codex-candidate"
questlock_claude_candidate="/absolute/path/to/private/complete-home-candidate"
questlock_native_bin="$HOME/.local/bin/questlock"
questlock_native_workspace="$HOME/questlock-workspace"
questlock_native_token_file="$HOME/.local/share/questlock/agent-workflows/worker.token"
```

The broker must already be running and the protected token file must grant the
intended scope. Add the server only to the staged candidate. If that candidate
already has a `questlock` entry, review and update that entry instead of adding a
second one. These commands expand absolute paths before writing configuration;
configuration files do not expand a literal `$HOME` inside a path.

For Codex:

```sh
CODEX_HOME="$questlock_codex_candidate" codex mcp add questlock \
  --env "QUESTLOCK_TOKEN=" --env "QUESTLOCK_TOKEN_FILE=$questlock_native_token_file" \
  -- "$questlock_native_bin" worker serve \
  --workspace "$questlock_native_workspace" --broker http://127.0.0.1:8080 \
  --timeout 2m
```

In that candidate's `[mcp_servers.questlock]` table, add `tool_timeout_sec = 180`
so the client can wait beyond the worker's two-minute bound. Validate the complete
candidate with the native CLI:

```sh
CODEX_HOME="$questlock_codex_candidate" codex mcp get questlock --json
CODEX_HOME="$questlock_codex_candidate" codex mcp list --json
```

These commands confirm Codex can parse the saved command, argument array,
environment, and timeout; they do not prove a model call or completed task.

For Claude Code, keep the user-scope entry inside the isolated home:

```sh
env -u CLAUDE_CONFIG_DIR HOME="$questlock_claude_candidate" claude mcp add \
  --env "QUESTLOCK_TOKEN=" --env "QUESTLOCK_TOKEN_FILE=$questlock_native_token_file" \
  --transport stdio --scope user questlock -- "$questlock_native_bin" worker serve \
  --workspace "$questlock_native_workspace" --broker http://127.0.0.1:8080 \
  --timeout 2m
env -u CLAUDE_CONFIG_DIR HOME="$questlock_claude_candidate" claude mcp get questlock
```

Require a connected status, then use an isolated client session to discover
`questlock_delegate` and submit the approved `request_path`. A connection alone
does not establish task quality. Require `status: "completed"` and the declared
checks before accepting an answer. The [observed MCP task](measurements-v0.3.0.md#a-passing-bounded-mcp-task)
records one passing extraction and one exact reuse; it does not qualify broader
repository work.

Before installing the validated complete candidate, keep a private complete
known-good backup and install an independent rollback watchdog. Verify the live
client can read the configuration and test every changed setting before
committing the swap. Preserve concurrent human repairs; see the
[configuration rollout requirements](maintenance.md#client-configuration-rollout).
Keep tokens in protected files, rather than embedding their values in MCP
configuration or passing them on command lines. Langfuse remains separately
opt-in; its endpoint and keys are not part of the approved task file.

## Understand the escalation gate

| Observed tier result | Next action |
| --- | --- |
| Error-free, complete, durable, valid known usage; every declared check passes | Return the selected passing answer with `status: "completed"`. |
| The same confirmed result fails a declared check | Run the one explicit next tier, if configured. Otherwise return `quality_failed`. |
| Pending/unknown usage, incomplete output, cap violation, cancellation, provider error, publication error, or settlement error | Stop the policy; no escalation or automatic generation retry. |

Quality checks support JSON validity/equality and literal contains/not-contains tests. Passing them proves only those expectations. A truncated local answer does not authorize a larger tier, even if that tier has more output allowance.

## Opt in to downstream outcome tracking

Version 0.4.0 accepts `"track_outcome": true` at the top level of the
approved request. It records content-free invocation and attempt metadata in
the worker's existing broker scope and adds `outcome_tracking` to the result.
Each invocation receives a run ID, including an exact reuse; reuse records zero
new generation usage and cost rather than charging the original result again.
Omitting the flag preserves the untracked workflow.

Registration does not turn a passed check into human acceptance. The reviewer
uses explicit `outcome feedback` events to accept, rewrite, reject, or reopen
the work. A rewrite clears acceptance even when the reason is style; a later
acceptance identifies the final artifact by SHA-256. An agent must not invent a
human decision to make its metrics look better.

If registration is unavailable, the result reports that failure independently
of the generated answer. No generation is retried to repair outcome metadata.
The [outcome guide](outcomes.md) covers receipt recovery, scoped access, cohort
cost coverage, and the distinction between caller-reported records and automatic
worker linkage. Langfuse's existing quality score still describes declared
checks; human feedback is not exported to Langfuse in this release.

The request digest and tier ID derive stable job identity. Repeating the same approved request preserves normal snapshot reuse and uncertainty rules; the router never changes a revision to force generation. Update the approved revision when facts change. Review retained broker records before deliberately creating a new attempt after an uncertain outcome.

Result attempts retain checks, usage, outcome, and an output digest while omitting failed answer text. Only a passing selected tier exposes `response`; MCP additionally strips provider response identifiers and labels from attempts. Require a successful CLI exit and `status: "completed"` before using the selected text, then review it for the surrounding task.
