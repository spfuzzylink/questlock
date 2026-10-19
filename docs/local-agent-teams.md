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

## Try one task

Install Questlock and Ollama, then deliberately install a model that fits your
hardware. These commands assume `qwen3.5:4b` is already installed and Ollama is
listening on `127.0.0.1:11434`; Questlock never downloads weights automatically.
Run from a private working directory:

```sh
umask 077
mkdir -p tasks receipts
questlock team init --model qwen3.5:4b > tasks/first.json
questlock team plan --workspace tasks --request first.json
```

Review `tasks/first.json`. It contains the task, an explicit revision, deterministic
checks, a master and two reviewers, model digests, token limits, and a reuse
window. `init` inspects installed model metadata without generation; `plan` is
offline. To approve the snapshot, run it explicitly:

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

A completed exact repeat reports zero new generation. Reuse includes the entire
request, including policy, role instructions, model digests and checks. Changed
facts or policy need a deliberately reviewed revision. Machine checks and model
votes do not establish human acceptance.

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
[admission configuration](adaptive-admission.md); `--timeout` bounds the whole
invocation. The default admitted task deadline is 45 seconds; a longer whole-run
timeout alone does not extend that policy. Set measured per-role limits before
approving a slower workload. These estimates are per process, rather than OS
allocation or a distributed scheduler. Retrieval of already checked results
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
