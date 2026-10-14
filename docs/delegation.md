# Bounded local-first delegation

This describes the **unreleased v0.3.0 candidate**. The `delegation` Go package and `questlock worker` execute one approved request through local Ollama, with at most one explicitly configured escalation tier. They let a coding assistant delegate a fixed extraction, handoff, or summary while keeping context, models, reuse, budgets, and acceptance checks reviewable.

The surrounding assistant prepares the request and reviews the passing answer. The worker provides no shell execution, workspace editing, file discovery, model training, whole-session proxy, or automatic model selection. A supported OpenRouter adapter remains future work.

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

The first tier must use Ollama. A hosted escalation must name a supported direct OpenAI or Anthropic model, include explicit input/cache/output prices, and supply positive `max_cost_usd` and `daily_budget_usd`. The process must also start with `--allow-paid`; this authority cannot come from request JSON, an MCP tool argument, or an environment variable. A configured hosted tier without that permission is rejected before either tier runs. Prices are caller estimates and the scope ledger covers cooperating Questlock workers, not the whole provider account.

Both tiers use the worker's one broker scope. Validated local Ollama jobs omit USD fields and neither consume nor clear the API dollar ledger, so local work can continue when paid admission is blocked. Hosted escalation still obeys that ledger. Local hardware, power, memory, and time are bounded by task/resource settings, not measured by the USD ledger.

## Run or expose the worker

Provision the broker and its private scope credential using [the setup guide](model-workflows.md). Keep secrets outside the request file: use `QUESTLOCK_TOKEN`, or `QUESTLOCK_TOKEN_FILE` as shown below. Hosted provider credentials remain in the worker process as `OPENAI_API_KEY` or `ANTHROPIC_API_KEY`.

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

Both commands default to a two-minute per-task bound, with `--timeout` limited to ten minutes. `--ollama-url` can select another literal loopback HTTP origin. Add `--langfuse --langfuse-timeout 2s` for [optional content-free telemetry](inference-observability.md). CLI result JSON reaches stdout before export receipts on stderr. MCP exports silently through a background queue of 16 record batches, with a five-second shutdown grace; saturated queues drop telemetry. It is best effort, with no durable export guarantee or generation retry.

## Understand the escalation gate

| Observed tier result | Next action |
| --- | --- |
| Error-free, complete, durable, valid known usage; every declared check passes | Return the selected passing answer with `status: "completed"`. |
| The same confirmed result fails a declared check | Run the one explicit next tier, if configured. Otherwise return `quality_failed`. |
| Pending/unknown usage, incomplete output, cap violation, cancellation, provider error, publication error, or settlement error | Stop the policy; no escalation or automatic generation retry. |

Quality checks support JSON validity/equality and literal contains/not-contains tests. Passing them proves only those expectations. A truncated local answer does not authorize a larger tier, even if that tier has more output allowance.

The request digest and tier ID derive stable job identity. Repeating the same approved request preserves normal snapshot reuse and uncertainty rules; the router never changes a revision to force generation. Update the approved revision when facts change. Review retained broker records before deliberately creating a new attempt after an uncertain outcome.

Result attempts retain checks, usage, outcome, and an output digest while omitting failed answer text. Only a passing selected tier exposes `response`; MCP additionally strips provider response identifiers and labels from attempts. Require a successful CLI exit and `status: "completed"` before using the selected text, then review it for the surrounding task.
