# Local master, independent reviews

Keep the reasoning on your own machine. Give a master one approved text task,
check its answer, and ask independent local reviewers to inspect the same
candidate. Questlock releases the answer only after every gate passes. Repeating
that exact approved snapshot reuses the checked result without a model call.

![Local text roles share durable claims through one broker](diagrams/local-agent-teams.svg)

[Editable draw.io source](diagrams/local-agent-teams.drawio).

This is a bounded text workflow. Roles cannot run commands, browse project files,
edit the repository, or publish external actions. The existing tool-capable
[`local-agent` launcher](local-models.md) remains separate. Starting a team does
not switch the backend of an existing hosted Codex or Claude conversation.

The initial live suite did not qualify the tested small model. Read the
[actual failures and resource refusals](local-team-validation-v0.9.0.md) before
choosing a model. `questlock team quest` exercises three HTTPS worker identities
and six scripted cases without a model, account or local weights.

The JSON/release-only follow-up, `local-json-release-2`, completed two of six
cases without resource blocks. Correct formatting did not fix the remaining
reasoning failures. The explicit reasoning profile below keeps the same checks;
its settings alone do not establish a successful benchmark.

## Try one task

Install Questlock and Ollama, then deliberately install a model that fits your
hardware. These commands assume `qwen3.5:4b` is already installed and Ollama is
listening on `127.0.0.1:11434`; Questlock never downloads weights automatically.
Run from a private working directory:

```sh
umask 077
mkdir -p tasks receipts
questlock team init --model qwen3.5:4b --driver ollama \
  --json-output --release-model --think --output-tokens 4096 \
  --policy-revision local-json-reasoning-3 \
  > tasks/first.json
questlock team plan --workspace tasks --request first.json
```

Review `tasks/first.json`. It contains the task, an explicit revision, deterministic
checks, a master and two reviewers, model digests, token limits, and a reuse
window. `init` inspects installed model metadata without generation; `plan` is
offline.

The example opts into JSON output at temperature zero, bounded local reasoning,
and model release after each response. `--json-output` sets generic JSON mode;
it does not supply the expected answer or weaken the answer checks. Reviewers
receive the candidate as its parsed JSON value—an object, array or scalar—and
judge that value against the source task. Existing text-mode requests keep their
original candidate representation.

`--think` requires installed model metadata advertising support for `think:true`.
It uses nonstreaming generation; streamed reasoning is rejected. Only the final
answer is published and included in receipts; raw thinking is discarded.
`--output-tokens` accepts `16` through `4096`, defaults to `512`, and bounds each
role's total output allowance. Ollama's `eval_count` includes reasoning and final
answer tokens; a separate reasoning count is not reported. A larger allowance
can improve completion, but JSON mode and reasoning do not guarantee correctness.
Every original answer check and reviewer gate still applies.

`--release-model` sets each role's `ollama.keep_alive_seconds` to `0`.
This can recover resident model memory, but affects retention in the shared
Ollama daemon and can add cold-start latency to later requests. It is neither a
generation retry nor a global memory reservation.

JSON, reasoning and release switches require direct `ollama` roles and apply
only when preparing `init` or `suite` requests. Leaving these controls out
preserves existing request and runtime defaults. A role can instead specify an
optional
`ollama.keep_alive_seconds` value from `0` through `300`; an omitted value leaves
Ollama's default retention unchanged. Inspect these controls before approving
the snapshot, then run it explicitly:

```sh
questlock team run --workspace tasks --request first.json > receipts/first.json
questlock team run --workspace tasks --request first.json > receipts/repeat.json
```

Without `--broker`, Questlock starts or reuses its private managed loopback
broker. A successful first run calls the master, `review-correctness`, then
`review-risk`. Reviewers receive the approved task and candidate, without each
other's votes. The master must pass all declared checks; each reviewer must
return exactly `{"approved":true,"issues":[]}` to approve it. A rejection or
malformed answer stops the workflow. Check the exit status and JSON report;
only `status: "completed"` exposes final text.

Before a fresh role claims work, resource admission waits for enough healthy
capacity within the invocation deadline. `--timeout` defaults to five minutes
and accepts at most thirty minutes. If capacity never returns, the report is
`blocked` without a claim or model call. Only admission is polled; an admitted
model call is never retried. The approved request stays on disk for a deliberate
later submission. An uncertain claim needs investigation instead.

A completed exact repeat reports zero new generation. Reuse includes the entire
request, including policy, role instructions, model digests and checks. Keep
`revision` for task/data changes; use `policy_revision` for a reviewed execution
policy change, such as `local-json-reasoning-3`. Both change execution identity,
so a new policy cannot silently reuse or reset the original failed workflow.
Retain that failure. Machine checks and model votes do not establish human
acceptance.

## Choose the local driver

| Driver | Execution | Requirements |
| --- | --- | --- |
| `ollama` | Direct local Ollama text generation | Installed model and fixed loopback Ollama |
| `codex-oss` | Isolated native Codex harness using local Ollama | The same installed model and a compatible native Codex CLI |

For the native route, generate a separate request:

```sh
questlock team init --model qwen3.5:4b --driver codex-oss > tasks/native.json
questlock team run --workspace tasks --request native.json \
  --client-bin /absolute/path/to/codex > receipts/native.json
```

The native adapter currently accepts Codex CLI `0.163.0`, including its prerelease
builds. It pins the executable and installed model identity, uses a fresh private
home and empty workspace, disables tools, and imports no account credentials,
user configuration, hooks or project instructions. It never changes active
client settings. A model or executable change stops the existing provider.

Native input admission includes a conservative harness reserve. Actual native
token counts are checked after completion; the output limit is a validation
limit, not a proven hard generation ceiling. Questlock does not retry generation;
the native client's internal finite transport retries are not observable as
separate model calls. Read-only execution relies on the trusted native client
and host OS; it is not a confidentiality sandbox.

Both drivers are local-only. The team schema has no hosted role, provider key,
paid fallback, or automatic escalation. Local hardware and electricity still
have operating costs. Resource admission reserves local CPU/memory headroom
before fresh work. `--config` selects an explicit complete
[admission configuration](adaptive-admission.md). The default admitted task
deadline is 45 seconds; a longer whole-run timeout alone does not extend that
policy. For a slower reasoning workload, review a complete admission config
with a bounded task deadline, such as two minutes, while preserving memory
headroom and pressure thresholds; pass it with `--config`. No timeout or quality
failure triggers a generation retry or provider fallback. These estimates are
per process, rather than OS allocation or a distributed scheduler. Retrieval of already checked results
needs no model admission.

## Share work across nodes

Run one authoritative broker. Every worker keeps its own private files and calls
the broker over authenticated HTTPS. Only the broker owns SQLite; never mount
its database into workers or share that file over NFS.

On the trusted broker host, provision distinct credentials in one shared scope
**before** starting the broker. These commands refuse to overwrite token files:

```sh
umask 077
mkdir -p .questlock/tokens
(set -C; questlock token create --db .questlock/state.db \
  --agent node-a --scope local-team > .questlock/tokens/node-a.token)
(set -C; questlock token create --db .questlock/state.db \
  --agent node-b --scope local-team > .questlock/tokens/node-b.token)
(set -C; questlock token create --db .questlock/state.db \
  --agent node-c --scope local-team > .questlock/tokens/node-c.token)
questlock serve --db .questlock/state.db --listen 127.0.0.1:8080
```

Keep that terminal running. For separate machines, put an operator-managed TLS
reverse proxy on a private interface in front of the loopback listener. The
broker itself does not terminate TLS. Use a certificate whose name matches the
worker endpoint, restrict network access, and forward the Authorization header
unchanged. The [deployment guide](deployment.md) covers broker isolation and
persistence. With a private CA, distribute only its public PEM certificate to
workers; keep server private keys on the broker host. No system trust-store
change or certificate-verification bypass is needed.

Securely give each worker its own token file, the identical approved request,
and the CA PEM. Each inference node needs its own installed local model matching
the pinned digest. Synchronize their clocks; records more than 30 seconds in the
future or with an expiry inconsistent with the approved reuse window are
rejected. Replace the example endpoint and local file paths below:

```sh
# Node A: master
QUESTLOCK_TOKEN= QUESTLOCK_TOKEN_FILE=/private/node-a.token \
questlock team run --workspace tasks --request first.json \
  --broker https://questlock-broker.internal --ca-file /private/ca.pem \
  --role master --worker-id node-a > receipts/master.json

# Node B: correctness review
QUESTLOCK_TOKEN= QUESTLOCK_TOKEN_FILE=/private/node-b.token \
questlock team run --workspace tasks --request first.json \
  --broker https://questlock-broker.internal --ca-file /private/ca.pem \
  --role review-correctness --worker-id node-b > receipts/correctness.json

# Node C: risk review
QUESTLOCK_TOKEN= QUESTLOCK_TOKEN_FILE=/private/node-c.token \
questlock team run --workspace tasks --request first.json \
  --broker https://questlock-broker.internal --ca-file /private/ca.pem \
  --role review-risk --worker-id node-c > receipts/risk.json
```

Use `team status` with the same request, endpoint, CA and credential arguments to
retrieve the combined result without generation. A reviewer invoked before the
master is ready returns `waiting` without claiming work. Separate reviewer
processes can start once the master completes; each sees the same candidate.

`--worker-id` is an operator label, not a cryptographic machine identity.
Distinct bearer agents provide the broker's authenticated publication identity.
All agents in one scope can read and write every key there; role names do not
create per-role permissions or protect against a malicious scope member.

The reproducible qualification topology uses isolated local nodes and scoped
identities on one physical host. These instructions support deployment to
separate machines; they do not establish that a physical second host was tested.
There is one write authority, no broker failover or replicated model weights.
[SecondGate](second-gate.md) separately provides durable edge synchronization;
team claims require an online authoritative broker.

## When work stops

A role first publishes a claim, then publishes its result. A lost acknowledgement,
process crash or incomplete model answer can leave a pending or uncertain role.
Another worker does not take it over after a timer, regenerate an answer, or
invent a new operation ID. This trades availability for avoiding duplicate
inference. Inspect the durable claim, result and receipts before deciding what
to do next. `team plan` identifies the exact shared keys.

Quality failure, expired reuse, changed model identity and uncertain completion
also stop without fallback. The maximum reuse window is 24 hours. There is no
automatic lease recovery or reset command. After investigation, approve a new
explicit revision if new inference is appropriate; retain the original failure
in your evidence. Reissuing the same task is not a recovery procedure.

Outcome registration is separate from generation. Reports expose
`outcome_tracking_status` per step and `accounting_complete`; recover a missing
metadata record through its exact receipt rather than generating another answer.
Outcome tracking and model approval remain separate from a person's acceptance
or later rewrite.

## Measure the same work

The [six-case walkthrough](../examples/teams/README.md) covers three simple cases
and three bounded reasoning cases. Generate the suite with `team suite`, inspect
it, save every result, and keep failed cases. A fresh suite calls local models;
it is not an offline fixture or a claim of repository-scale coding quality.

`team prompt` emits the approved baseline user prompt without inference.
`team baseline-record` imports an explicitly obtained native Codex JSONL file;
`team benchmark` compares receipts without starting either model. A hosted Codex
baseline is a separate deliberate use of the operator's subscription or account,
never an automatic fallback. Inspect its authentication and model policy before
authorizing that run.

Reuse an existing baseline when its bindings still match. The current six-case
follow-up uses the saved v0.9.0 Codex baseline of **65,800 reported input plus
output tokens**, without another hosted call. A runtime-only `policy_revision`
does not change `TaskDigest` or the exact `MasterPrompt`; the comparator still
requires both saved hashes to match. Changed facts, task revision, checks or
emitted master prompt need a new comparison contract, not a relabeled receipt.

Comparison requires matching task/revision, facts, checks and emitted user prompt,
successful local master and reviews, a checked baseline answer, and known hosted
input/output counters for every case. Local and hosted native harness overhead
may differ. Receipts are operator-supplied evidence, not remote attestation. A
missing, failed, mismatched or unknown-usage case makes the 99% target
unestablished; the percentage stays null rather than dropping that case.

For `codex-oss` comparisons, use the exact emitted user prompt on each side;
system instructions and native harness overhead are not claimed identical.
For the direct `ollama` route, compare the same task/data/check contract and
report the different provider prompt framing.

`this_run_local_usage` counts only the current invocation. Sum all master/reviewer
phase receipts when workers are separate; a final `status` read has zero fresh
usage. Generation-call counts are adapter invocations, not measured native HTTP
retry counts. Retain unknown counters as unknown.

The comparison measures observed hosted input plus output tokens for that bounded
suite. It does not measure subscription-credit depletion, a counterfactual USD
invoice, whole-build savings, or cost per human-accepted task. Hosted bootstrap
coordination/review consumes its normal allowance and is excluded and unmeasured.
See the [validation record](validation.md) for published observations rather than
assuming a target has already been achieved.
