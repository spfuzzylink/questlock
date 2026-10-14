# Local models: routine work without provider credits

This guide includes the **unpublished v0.3.0 candidate**. New worker, evaluation,
and streaming-measurement commands require a source build until release assets
are confirmed.

Use Questlock's `ollama` provider to run a bounded handoff, repository summary,
or extraction on installed local model weights. Accepted local requests need no
OpenAI, Anthropic, or Ollama cloud API key and incur no provider token metering.
Hardware, electricity, memory, and elapsed time remain costs. Local model quality
must still meet the task's requirements.

The examples use `qwen3.5:4b`, a model listed in
[Ollama's library](https://ollama.com/library/qwen3.5:4b). The
[v0.3.0 live suite](measurements-v0.3.0.md) qualified neither tested output-cap
configuration, even though generation, durability, and exact reuse worked.
Treat this as an explicit example; evaluate a local model against your task's
requirements before approving its answers.

## What changes

Questlock has three layers that work together:

- A compact approved-memory chunk supplies relevant preferences and decisions.
  Current evidence lives in separate required chunks. You can inspect exactly
  what the model receives.
- A local Ollama adapter generates the bounded answer. It uses installed weights
  through a literal loopback HTTP endpoint and refuses cloud-model metadata.
- The shared broker coordinates identical tasks and retains exact results, so a
  later worker can avoid repeating even the local computation.

Approved memory is visible prompt context. It does not train weights, import
entire histories, or cause a model to remember facts that were omitted. The
included example remembers relevant approved preferences: Go for core logic,
`spfuzzylink/` branches, preserving published versions and working features, and
concise writing. Replace example facts with current evidence before real use.

`questlock model install` copies the binary and the same workflow skill into
Codex and Claude Code skill directories. The installed skill can prepare an
explicit local spec for an approved routine task when the user's goal is to avoid
provider credits.
An explicit provider/model choice takes precedence. The installer changes no
global assistant model setting and installs neither Ollama nor a coding client.

## Set up a local-only runtime

Install Ollama using its [official installation instructions](https://ollama.com/download).
Start a foreground local server with cloud features disabled:

```sh
OLLAMA_NO_CLOUD=1 OLLAMA_HOST=127.0.0.1:11434 OLLAMA_CONTEXT_LENGTH=65536 ollama serve
```

If Ollama is already running, configure and restart that instance instead of
starting a competing server. The context default above supports trying native
coding clients; larger windows use more memory. Questlock's bounded generation
sets its own smaller context window from the spec.
Ollama documents [disabling cloud features and configuring context](https://docs.ollama.com/faq).

In another terminal, download the local weights once:

```sh
ollama pull qwen3.5:4b
ollama list
```

Downloading weights needs a network connection and disk space. Questlock does
not pull models, sign in, or start Ollama. Generation remains local after those
weights are installed.

## Run a bounded Questlock task

Use a scope appropriate to the task's permissions and privacy. A fresh standalone
setup can use port 8081 so it does not compete with an existing broker on 8080:

```sh
questlock model install --agents codex,claude
umask 077
mkdir -p "$HOME/.local/share/questlock/local-workflows"
(set -C; questlock token create --agent local-workflows --scope local-workflows --db "$HOME/.local/share/questlock/local-workflows/state.db" > "$HOME/.local/share/questlock/local-workflows/worker.token")
questlock serve --db "$HOME/.local/share/questlock/local-workflows/state.db" --listen 127.0.0.1:8081
```

Provision the token once; the command refuses to overwrite its file. For later
starts use only the same `serve` command. Keep it running in that terminal.
Alternatively, use an existing broker and an appropriate scope. Validated local
Ollama jobs omit USD fields and skip the API dollar ledger entirely, neither
consuming nor clearing it. They can continue in the same scope when paid
admission is frozen. Separate scopes remain useful for permission/privacy
isolation; local hardware and runtime cost are outside that ledger.

From the source checkout, review the example first:

```sh
questlock model plan --spec examples/model-workflows/local-handoff.json
```

After adapting its facts and revision, run it from another terminal using the
installed helper to read the protected token file:

```sh
QUESTLOCK_TOKEN= QUESTLOCK_TOKEN_FILE="$HOME/.local/share/questlock/local-workflows/worker.token" \
  sh "$HOME/.codex/skills/questlock/scripts/run.sh" run \
  --spec examples/model-workflows/local-handoff.json \
  --broker http://127.0.0.1:8081 --timeout 5m
```

Use the corresponding installed skill path when `CODEX_HOME` differs or when
running from Claude Code. The installed skill also contains a self-contained
`references/local-summary.json` template, so it works without this checkout.
The CLI still requires the broker token; it never reads a provider API key for
`provider: "ollama"`.

Run the same approved snapshot twice to inspect completed-result reuse. Require
successful exit, `response.complete: true`, and `durable: true`; then check that
the text matches the evidence. The repeat should report `reused: true`, zero new
generation usage, and the original generation's usage in `avoided_usage`. Those
are avoided local tokens/computation; dollar estimates are absent.

Keep `prices`, `max_cost_usd`, `daily_budget_usd`, `reasoning_effort`, and explicit
`prefix` cache mode out of local specs. USD fields are rejected because no
provider token price applies. The example uses a 12,000 input-token policy cap
and 512 output tokens; this is an allowance, not a statement of actual token
usage. Update task revision whenever current facts or approved memory change.

## How local bounds work

The adapter verifies installed-model metadata and captures its digest in the
provider identity. A changed digest separates result reuse. Non-loopback URLs,
DNS hostnames such as `localhost`, redirects, names containing `cloud`, and
nonempty remote-model metadata are rejected. The default accepted endpoint is
`http://127.0.0.1:11434`; IPv6 literal loopback is also supported. No paid fallback
exists. These checks assume a trusted local Ollama installation; they do not
authenticate an arbitrary program that impersonates it on a loopback port.

Local preflight estimates one token per UTF-8 byte of prepared text and model
template/system, plus 512 tokens of overhead. Metadata reads run no generation.
This is a conservative policy estimate, not an exact tokenizer or a proof for
every template. The effective context window is the input cap plus output cap
plus 512; it must fit both the model's reported capacity and Questlock's 65,536
context-window ceiling. Avoid raising caps merely to fit irrelevant history.

Native `/api/generate` receives `think: false`, bounded `num_predict`, and the
chosen `num_ctx`. Streaming defaults off; an explicit local `"stream": true`
collects client-observed content-arrival timing while retaining the same output
and usage validation. The returned prompt/evaluation counters
are actual runtime usage. Incomplete, unexpected-thinking, tool, or limit-violating
output cannot become an approved reusable answer. Ollama documents these
[generation fields and usage counters](https://docs.ollama.com/api/generate).

Pending and unknown tasks retain Questlock's conservative recovery behavior.
Inspect the job before deliberately changing revision; another attempt could
repeat computation. Results and approved memory are retained scope data; reuse
TTL controls validity, not deletion. Workers share broker HTTP access, while the
database remains local to its broker host.

## Measure and delegate local work

`questlock model evaluate` runs simulated public fixtures by default; `--live`
uses the installed explicit model settings in a fresh evaluation scope. The
default suite has three simple and three complex cases at 64/256 output-token
caps, with local content streaming enabled. Single samples and uncontrolled
cache/warmth/load are observations, not a reliable latency ranking. JSON/literal
checks establish only their declared expectations.

`questlock worker run --request PATH` executes an approved local-first request
with checks and at most one explicit escalation. The stdio MCP mode,
`worker serve --workspace ROOT`, exposes only a named request file and returns
only a passing selected answer. No shell or workspace edit tool is provided.
Unknown, incomplete, or budget/error outcomes stop; they do not authorize a
larger model. Hosted escalation additionally needs process permission, explicit
prices, and per-call/daily caps. See [delegation](delegation.md).

The [recorded worker extraction](measurements-v0.3.0.md#worker-extraction-observation)
failed its public-field checks and withheld the answer, then reused that failed
snapshot without generating again. Reuse is useful accounting behavior, not a
quality guarantee. [Current-run measurements and optional Langfuse export](inference-observability.md)
keep that distinction visible without exporting prompt or answer content.

## Use the coding clients themselves with local inference

A Questlock skill makes its bounded subtask local. A surrounding hosted Codex or
Claude session still consumes that session's normal allowance. To move the
coding client's own model turns to local weights, use its native Ollama
integration as a separate session choice.

The installed Codex CLI exposes `--oss` and `--local-provider`. For a local
session:

```sh
codex --oss --local-provider ollama -m qwen3.5:4b
```

Ollama's [Codex integration guide](https://docs.ollama.com/integrations/codex)
describes local-provider operation and recommends at least 64k context for native
coding work. Tool use, client version, and repository size can require more
resources than a bounded handoff.

For an installed Claude Code client, its manual Ollama integration can select
local Qwen inference for that session:

```sh
ANTHROPIC_AUTH_TOKEN=ollama ANTHROPIC_API_KEY='' \
  ANTHROPIC_BASE_URL=http://127.0.0.1:11434 \
  claude --model qwen3.5:4b
```

Check `/status` because existing Claude Code settings may override environment
variables. Ollama's [Claude Code integration guide](https://docs.ollama.com/integrations/claude-code)
documents this connection and the context requirements for repository work.
Keep Ollama cloud disabled and select installed local weights for the zero
provider-metering path.

These native clients use Ollama's full compatibility protocols directly.
Questlock remains an explicit bounded task runner and skill: it does not proxy
or coalesce every tool call in the native client's session. The setup commands
are documented integration paths, not evidence that a particular model has
passed an end-to-end repository-agent quality evaluation. See the release's
validation record for tests actually completed on a given host.
