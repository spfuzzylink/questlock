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

Read the [latest measurements](local-team-validation-v0.9.1.md) for the exact
models, execution policies, failures and quality limits. The original
[v0.9.0 result](local-team-validation-v0.9.0.md) stays available unchanged.
`questlock team quest` runs six scripted cases through three HTTPS identities
without a model or account.

## Try one task

Start with the supplied request that completed all three local roles. It uses
installed `qwen3.5:9b` weights and Ollama on `127.0.0.1:11434`. Questlock never
downloads weights automatically. Review the model license, hardware needs and
pinned digest before installing a model; a changed digest stops this snapshot.

From the repository or extracted archive root, with `questlock` on your PATH:

```sh
umask 077
set -C
mkdir -p .questlock-demo/tasks .questlock-demo/receipts
cat examples/teams/local-uncertain.request.json > .questlock-demo/tasks/first.json
questlock team plan --workspace .questlock-demo/tasks --request first.json
questlock team run --workspace .questlock-demo/tasks --request first.json \
  --config examples/teams/reasoning-resources.json --timeout 8m \
  > .questlock-demo/receipts/first.json
questlock team run --workspace .questlock-demo/tasks --request first.json \
  --config examples/teams/reasoning-resources.json --timeout 8m \
  > .questlock-demo/receipts/repeat.json
```

Inspect the request and offline plan before the first run. A fresh run calls the
master, correctness reviewer and risk reviewer. The completed exact repeat
reports zero new generation. Neither run accesses provider account credentials
or changes Codex/Claude configuration. A recorded success is an observation,
not a guarantee that another model or machine will pass.

The supplied admission configuration gives each admitted role five minutes;
`--timeout 8m` bounds the invocation. The ordinary admitted-task default is 45
seconds, so increasing only the invocation timeout is insufficient for long
reasoning. The supplied file changes only that task deadline; memory headroom,
queue bounds and pressure thresholds retain their defaults.

For a new task, `team init` creates an editable request after reading installed
model metadata, without inference:

```sh
questlock team init --model qwen3.5:9b --driver ollama \
  --json-output --canonical-prompt --release-model --think \
  --output-tokens 4096 --policy-revision my-local-policy-1 \
  > .questlock-demo/tasks/new.json
```

Approve its task, checks, role instructions, model digests, limits and revision
before running it. `--json-output` requests generic JSON; it preserves installed
sampling defaults. A request may explicitly set `ollama.temperature` from `0`
through `2`. Model-specific sampling matters; JSON mode guarantees no semantic
correctness. When the master requests JSON, reviewers receive its parsed JSON value.
Other requests keep their original string representation.

`--canonical-prompt` uses the same provider-independent user prompt emitted by
`team prompt`. Optional decoder contracts add separately disclosed system
context. These preparation flags work only with direct Ollama `init`/`suite`;
leaving them out preserves existing defaults.

### Make a field's meaning explicit

A direct Ollama master can opt into manually declared JSON types and field
roles. For example, inside that master's `ollama` object:

```json
{
  "format": "json",
  "output_types": {"count": "integer", "handling": "string"},
  "field_roles": {"count": "quantity", "handling": "policy_action"}
}
```

Types permit `integer`, `number`, `string` and `boolean`; all declared fields
are required and extra fields are rejected. There may be 1–32 fields, with
lowercase ASCII snake_case names of at most 64 bytes. The decoder schema is
bounded to 4096 bytes and contains no answer values, enums, defaults or bounds.

Field roles are optional manual interpretation: `quantity` requires an integer
or number, while `policy_action` and `identifier` require strings. Role keys
must already exist in `output_types`; at most 32 roles and 4096 serialized bytes
are allowed. Numeric-looking identifiers and policy codes remain valid strings.
Questlock supplies fixed generic definitions and source-grounding instructions,
never derives annotations from expected checks, and never rewrites an answer.

`team plan` and `team prompt` show these declarations separately. They change
the execution fingerprint; the canonical task and user prompt stay unchanged.
Reviewers see the same declarations as reference data and retain the fixed
approval response contract. Reviewer decoder/field-role overrides are rejected.
The benchmark discloses this extra operator assistance; it is not
identical-information model parity. The original answer checks still decide
whether a well-typed answer is correct.

### Bound reasoning and memory

`--think` requires metadata advertising boolean thinking support. It uses
nonstreaming generation. Only final text is published; raw thinking is discarded.
The total output allowance includes both reasoning and answer tokens. It
defaults to `512`, normally permits `16..4096`, and permits up to `8192` only for
explicit direct Ollama thinking. Native and nonthinking roles retain the `4096`
ceiling. Longer deliberation can still produce the wrong answer.

`--release-model` sets `keep_alive_seconds: 0`, asking the shared daemon to
release that model after its response. This can free memory and add cold-start
latency. Explicit values `0..300` are accepted; omission keeps daemon defaults.
This affects shared model retention, rather than reserving machine-wide memory.

A successful master must pass every declared check. Each independent reviewer
must return exactly `{"approved":true,"issues":[]}`. A rejection, malformed
answer or uncertain completion stops without fallback. Require a successful
exit and `status: "completed"` before using final text. Machine checks and model
votes are separate from a person's acceptance or later rewrite.

Before a fresh claim, admission waits for healthy capacity within the invocation
deadline. If it never returns, the report is `blocked` without generation.
Polling applies to admission; a model call is never retried. Keep the approved
request and inspect an uncertain claim before deciding on any later work.

A repeat reuses the entire exact request: facts, checks, model digests, role
instructions and policy. Change `revision` for task/data changes and
`policy_revision` for a reviewed execution change. Retain earlier failures;
changing policy must not reset or relabel them.

## Choose the local driver

| Driver | Execution | Requirements |
| --- | --- | --- |
| `ollama` | Direct local Ollama text generation | Installed model and fixed loopback Ollama |
| `codex-oss` | Isolated native Codex harness using local Ollama | The same installed model and a compatible native Codex CLI |

For the native route, generate a separate request:

```sh
questlock team init --model qwen3.5:4b --driver codex-oss > .questlock-demo/tasks/native.json
questlock team run --workspace .questlock-demo/tasks --request native.json \
  --client-bin /absolute/path/to/codex > .questlock-demo/receipts/native.json
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
policy. The hybrid follow-up uses a complete config from `resources config` with
only `task_timeout` changed to `300000000000` nanoseconds (five minutes), passed
with `--config` and `--timeout 8m`. Memory headroom and pressure thresholds stay
unchanged. Separately, `ollama.generation_timeout_seconds` accepts `1..600`
seconds and overrides both generation HTTP deadlines using a private client.
Omission preserves 120 seconds; metadata and hosted defaults stay unchanged.
The caller deadline still wins. No timeout or quality failure triggers a
generation retry or provider fallback. These estimates are
per process, rather than OS allocation or a distributed scheduler. Retrieval of
already checked results needs no model admission.

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

Securely give each worker its own token file, the identical approved request, the reviewed complete
admission configuration, and the CA PEM. Each inference node needs its own installed local model matching
the pinned digest. Synchronize their clocks; records more than 30 seconds in the
future or with an expiry inconsistent with the approved reuse window are
rejected. Replace the example endpoint and local file paths below:

```sh
# Node A: master
QUESTLOCK_TOKEN= QUESTLOCK_TOKEN_FILE=/private/node-a.token \
questlock team run --workspace .questlock-demo/tasks --request first.json \
  --broker https://questlock-broker.internal --ca-file /private/ca.pem \
  --config /private/reasoning-resources.json --timeout 8m \
  --role master --worker-id node-a > .questlock-demo/receipts/master.json

# Node B: correctness review
QUESTLOCK_TOKEN= QUESTLOCK_TOKEN_FILE=/private/node-b.token \
questlock team run --workspace .questlock-demo/tasks --request first.json \
  --broker https://questlock-broker.internal --ca-file /private/ca.pem \
  --config /private/reasoning-resources.json --timeout 8m \
  --role review-correctness --worker-id node-b > .questlock-demo/receipts/correctness.json

# Node C: risk review
QUESTLOCK_TOKEN= QUESTLOCK_TOKEN_FILE=/private/node-c.token \
questlock team run --workspace .questlock-demo/tasks --request first.json \
  --broker https://questlock-broker.internal --ca-file /private/ca.pem \
  --config /private/reasoning-resources.json --timeout 8m \
  --role review-risk --worker-id node-c > .questlock-demo/receipts/risk.json
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

The supplied qualified-suite snapshot keeps each recorded role policy explicit.
Read the latest report for originating generations, exact reuses, extra runtime
contracts and all failed policies. The suite was iteratively tuned on these six
fixtures; it is not held-out evidence of arbitrary coding quality. Two separately
designed validation tasks have no Codex baseline and stay outside the six-case
percentage.

Comparison requires matching task/revision, facts, checks and canonical master prompt,
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
