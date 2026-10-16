<div align="center">

![Questlock — coordinate work, inspect runtime reuse, measure acceptance](assets/questlock-v0.6.0.svg)

**Coordinate shared agent work before paying for it again.**

[![CI](https://github.com/spfuzzylink/questlock/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/spfuzzylink/questlock/actions/workflows/ci.yml)

[Run the demo](#start-the-quest) · [Download](#download-a-binary) · [Container](#try-the-container) · [Fork Questlock](https://github.com/spfuzzylink/questlock/fork) · [Contribute](CONTRIBUTING.md)

[Ask a question](https://github.com/spfuzzylink/questlock/discussions/categories/q-a) · [Security](SECURITY.md) · [Architecture](docs/architecture.md)

</div>

An agent stalls. Another takes over and finishes. The original wakes up with a
confident, outdated write. **Questlock checks its expected version before accepting
the update. An exact retry of a successful publish recovers its original receipt.**

An experimental Go + SQLite broker for cooperating agents. It controls publication
of shared text artifacts; it does not run or sandbox the agents.

The quest theme is a little weekend motivation. Underneath it: a compiled Go
broker, a Go client library, SQLite transactions, and concrete failure tests.
One host. No Kubernetes. Offline demos need no model account.

## Make exact edits without another generation

**v0.6.0 experimental release.** An answer is already checked;
the next request only changes its presentation. `questlock revise` pins those
source bytes, applies counted literal edits, checks the output, and publishes an
immutable result through the shared broker. It calls no model. Exact source
hashes, declared checks, and parent provenance keep the change reviewable.

```sh
# Install v0.6.0 below, or build this checkout.
make build
./bin/questlock revise quest                  # three simple + three complex cases
./bin/questlock revise plan --request examples/revisions/bold-mission.json
```

Use this path for known formatting or literal changes; new reasoning still needs
review and an approved model task. A failed hash, count, check, or uncertain
publish withholds text without a generation fallback. Measured zero new model
usage applies to the revision, excluding this hosted chat, hardware and human
work. Checks do not create human acceptance.

[What, why, and how checked revisions work](docs/checked-revisions.md) ·
[Runnable examples](examples/revisions/README.md) ·
[Editable architecture](docs/diagrams/checked-revisions.drawio) ·
[Release validation](docs/validation.md#v060-observations--2026-10-16)

## Reduce repeated model work

Two workers ask for the same approved handoff. One model call should be enough.
Questlock now offers a Go `inference` library and a bounded model-task CLI for
**OpenAI Responses, Anthropic Claude Messages, OpenRouter, local Ollama, and
self-hosted SGLang**. The worker plans whole context
chunks, checks token and configured cost allowances, claims the task in shared
storage, and saves the completed answer for explicitly opted-in reuse.

```sh
./bin/questlock model quest
./bin/questlock model plan --spec examples/model-workflows/openai-handoff.json
./bin/questlock model install --agents codex,claude
```

The model quest runs **three simple and three complex scenarios offline**, with
real authenticated HTTP/SQLite coordination and deterministic model fixtures.
It demonstrates 20 simultaneous workers starting one generation, reuse after a
broker restart, context reduction, preflight rejection, and an unknown outcome
retaining its budget reservation. No API key or paid call is needed for this demo.

[Local models without provider token bills](docs/local-models.md) ·
[What, why, and how](docs/model-architecture.md) ·
[Setup, Codex/Claude skills, and model specs](docs/model-workflows.md) ·
[Runtime caches and provider contracts](docs/runtime-cache.md)

Local Ollama generation needs installed local weights and no provider API key.
Cloud generation needs the selected provider's API key. Reuse can avoid a complete
repeated generation; provider prompt caching can reduce eligible input cost.
These controls apply to tasks routed through Questlock. They do not train the
model, replace an entire Codex/Claude session, or remove provider credit limits.
The architecture guide describes OpenRouter composition and states the storage,
count-estimate, price, and billing limits. Live local measurements now show exact reuse and quality failures; see [the actual evidence](docs/measurements-v0.3.0.md). These are not invoice savings or a model ranking.

### Delegate, measure, and check the result

**v0.3.0** adds a bounded local worker for Codex and Claude,
optional metadata observability for Langfuse, and a six-case evaluation command.
You declare the task snapshot, resource limits, and deterministic acceptance
checks. Only a confirmed passing tier exposes an approved answer. One explicitly
configured next tier may run after a known quality failure; uncertain calls stop.

```sh
./bin/questlock model evaluate                 # offline fixtures, including failures
./bin/questlock worker run --request approved-task.json --broker http://127.0.0.1:8080
./bin/questlock worker serve --workspace "$HOME/questlock-workspace"
```

The worker needs a scoped broker credential and installed local weights. Its MCP
interface reads only the named approved request file and admits one active task.
It makes bounded delegation available to an agent; it does not reroute a whole
hosted chat. These usage counters cover the delegated generation and exclude
the surrounding hosted assistant's prompt, tool, and context tokens. Langfuse
export is opt-in and carries model metadata, usage, timing, outcome, and declared
check scores without prompts or answer text.

Streaming Ollama jobs measure client-observed first content and output intervals.
Each invocation has separate phase timings; an exact reuse reports zero new
usage and no new generation timing. These distinguish faster retrieval from
faster inference, and explicit price estimates from provider invoices.

**Our first real local run rejected both configurations.** The six-case suite
passed 4/6 checks with one cap and 3/6 with another. All 12 exact repeats avoided
new generation, but reuse preserved the wrong answers too. The evaluator selected
neither configuration, and a failing worker withheld its answer. This is why
quality checks accompany cost measurements.

A separate bounded MCP ticket extraction passed strict JSON and exact-value checks
with 58 input / 21 output tokens; its exact repeat made no new generation. The
self-hosted Langfuse CI contract also passed actual metadata readback. See the
[measurement record](docs/measurements-v0.3.0.md) for timings, scope, and limits.

[Delegation and MCP setup](docs/delegation.md) ·
[Langfuse, timings, and evaluation](docs/inference-observability.md) ·
[Actual local measurements and limits](docs/measurements-v0.3.0.md)

### Count the work a person actually accepts

A check-passing answer can still need a rewrite. **v0.4.0 experimental** adds
explicit human outcome feedback, separate from worker checks.
Opted-in invocations record content-free attempt metadata in the existing scoped
broker. A reviewer can accept, rewrite, reject, or reopen the work; every rewrite
clears acceptance until the reviewer explicitly accepts the final artifact again.

```sh
./bin/questlock outcome quest                  # six synthetic offline scenarios
```

The cohort report counts all recorded attempts, including failed and rejected
work, alongside explicit reviewer time and an hourly rate. Cost per currently
accepted outcome stays unknown when required accounting is missing, and is
undefined with no accepted outcomes. These are configured estimates for recorded
work, not invoices or whole-chat costs. No editor watching, automatic acceptance,
model training, or human-feedback export is implied.

[What, why, and how human feedback works](docs/outcomes.md) ·
[Synthetic examples](examples/outcomes/README.md) ·
[Release validation](docs/validation.md#v040-observations--2026-10-14) ·
[Editable feedback diagram](docs/diagrams/human-outcomes.drawio)

### A companion layer for the OpenRouter ecosystem

OpenRouter is a strong common interface for models, routing, and provider caching.
Its response cache already makes identical completed requests reusable.
**OpenRouter routes the model. Questlock coordinates the work.**
**OpenRouter is the switchboard. Questlock helps keep a swarm from running up duplicate tabs.**

The **v0.5.0 experimental release** adds a dedicated OpenRouter text adapter:
one explicit model/provider route, requested fallbacks disabled, an operator
input reservation bound, and strict known-usage accounting. It complements
OpenRouter with bounded context, shared task admission, durable results,
review feedback, and budget coordination. It is a standalone companion adapter,
not an OpenRouter-hosted plugin. Live paid routing and invoices remain unvalidated.
[Add-on architecture and sources](docs/model-architecture.md#openrouter-inspiration-and-possible-add-on) ·
[Positioning and reproducible evidence](docs/positioning.md)

### Reuse answers and inspect runtime caches separately

Version **v0.5.0 experimental** also adds a pinned SGLang v0.5.20
adapter. Standalone dense and HiCache engines can run bounded native tasks;
read-only inspection reports runtime identity and the declared profile.
HiSparse and other prefill/decode instances are inspection-only while a
validated PD ingress remains future work.

```sh
# Install v0.6.0 with the steps below, or build this checkout.
make build
./bin/questlock model runtime-quest           # six offline HTTP-fixture scenarios
./bin/questlock model runtime-inspect --spec deploy/sglang/task.json \
  --api-base-url http://127.0.0.1:30000         # needs your configured runtime
```

Questlock stores whole results; SGLang manages device/host/storage KV. A KV hit
still belongs to a new generation and is not zero-token answer reuse or a billed
API cache discount. Self-hosted inference leaves the provider USD ledger alone;
hardware and operating costs remain. Runtime inspection reports configuration,
with `gpu_validated: false`; fixtures establish protocol behavior, not GPU speedup.

We also used the local worker during this release's development: one recorded
attempt used 75 input / 17 output tokens. Its outcome remains unreviewed; this
is not a human-acceptance or surrounding hosted-chat savings claim. Published
archives and both native container images are verified; live GPU qualification
remains pending. See [the release evidence](docs/validation.md#v050-observations--2026-10-16).

[Runtime guide and evidence levels](docs/runtime-cache.md) ·
[GPU profiles and reproducible qualification](deploy/sglang/README.md) ·
[Editable layered architecture](docs/diagrams/runtime-layers.drawio)

## Start the quest

**macOS or Linux: install and run in four commands. No Go or model account needed.**

```sh
git clone https://github.com/spfuzzylink/questlock.git
cd questlock
./scripts/install.sh
./bin/questlock quest
```

The installer selects your OS/CPU, verifies the release checksum, and puts one
executable in `bin/`. It needs `curl`, `tar`, and `sha256sum` or `shasum`; it uses no
sudo and changes no shell settings. The demo cleans up its temporary state.

[Download without Git](https://github.com/spfuzzylink/questlock/releases) ·
[Installation, requirements, and updates](docs/installation.md)

### Download a binary

The current published experimental binary is **v0.6.0**.
Choose your operating system and
CPU; x86-64 covers 64-bit Intel and AMD processors. No Go installation is needed.

| System | ARM64 | x86-64 (Intel / AMD) |
| --- | --- | --- |
| macOS 13+ | [Download](https://github.com/spfuzzylink/questlock/releases/download/v0.6.0/questlock_0.6.0_darwin_arm64.tar.gz) | [Download](https://github.com/spfuzzylink/questlock/releases/download/v0.6.0/questlock_0.6.0_darwin_amd64.tar.gz) |
| Linux | [Download](https://github.com/spfuzzylink/questlock/releases/download/v0.6.0/questlock_0.6.0_linux_arm64.tar.gz) | [Download](https://github.com/spfuzzylink/questlock/releases/download/v0.6.0/questlock_0.6.0_linux_amd64.tar.gz) |

Compare the archive's SHA-256 with [checksums.txt](https://github.com/spfuzzylink/questlock/releases/download/v0.6.0/checksums.txt)
before extracting, then run `./questlock quest`. The installer above performs the
checksum check for you. [Release notes](https://github.com/spfuzzylink/questlock/releases/tag/v0.6.0).
[Older releases](https://github.com/spfuzzylink/questlock/releases), including
v0.2.0, remain available.

### Try the container

The public [GitHub Packages image](https://github.com/spfuzzylink/questlock/pkgs/container/questlock)
supports Linux ARM64 and x86-64. With Podman running:

```sh
podman run --rm --network none --read-only \
  --cap-drop all --security-opt no-new-privileges \
  --memory 256m --cpus 1 --pids-limit 128 \
  --tmpfs /data:rw,nosuid,nodev,noexec,size=64m,mode=1777 \
  ghcr.io/spfuzzylink/questlock:v0.6.0 quest
```

Use `docker` in place of `podman` if preferred. The image pulls without a registry
login; the demo then runs with networking disabled and no host folders or ports
shared. [Container usage, persistent deployment, and updates](docs/containers.md).

### Explore the failure case

Developing the source? Install **Go 1.26+** and run `make quest` instead. The
installer downloads the version in `VERSION`; a source build runs your checkout.
`make check` also needs Python 3 and a C toolchain for Go's race detector.

**Quest 001: keep a stale worker from rewriting the present.**

The executable launches real broker and worker subprocesses in temporary state.
It kills a worker after it saves its intended write, lets another publish, resumes
the stale request, then hard-kills and restarts the broker.

```text
PASS  Created shared artifact at version 1
PASS  Killed worker A after it saved a write based on version 1
PASS  Worker B published version 2
PASS  Restarted worker A; its stale publish was rejected with HTTP 409
PASS  Hard-killed and restarted broker; version 2 survived
PASS  Retried the acknowledged operation; original result returned without version 3
PASS  Durable journal contains 2 accepted, 1 rejected, and 1 replayed publish
```

These are deterministic test workers, not a live LLM swarm. The quest runs through
the same HTTP API and storage engine used by real clients. Temporary state is
removed when it finishes, including its generated fixture credentials.

## What is implemented

| Quest | Status | Concrete behavior |
| --- | --- | --- |
| Guard the gate | Implemented | A credential grants access to one shared scope. |
| Reject the stale challenger | Implemented | Compare-and-swap rejects outdated writes with HTTP 409. |
| Recover the lost reply | Implemented | An exact retry recovers the original successful result. |
| Survive the restart | Tested | Acknowledged data and receipts survive broker process kills. |
| Leave a trace | Implemented | Accepted, conflicting, and replayed publishes enter a durable journal. |
| Open a second gate | Future experiment | Multi-host coordination, placement, and failure recovery. |

The guarantee is deliberately narrow: a publish is accepted only when its expected
version matches. Artifact content, its current pointer, the successful retry
receipt, and the journal entry commit in the same SQLite transaction.

## Where it fits

Use it for shared agent notes, competing text-artifact updates, recovering a
publish after a lost response, or separating small experiments into scopes.
It also provides runnable ways to study stale workers, broker crashes, and
shared model-task coordination on one machine. The model workflow supplies a
CLI/Go integration for bounded text tasks; other agent integrations and conflict
resolution remain application responsibilities.

See [who this could help and why](docs/use-cases.md#who-this-could-help) for
research, support automation, evaluation, and platform workflows. The notes cover
the potential benefit, integration work, pilot checks, and technical limits.

## Run your own local broker

After installing (or running `make build`), start a persistent broker:

```sh
umask 077
mkdir -p .questlock
(set -C; ./bin/questlock token create --agent writer-a --scope weekend > .questlock/writer-a.token)
(set -C; ./bin/questlock token create --agent writer-b --scope weekend > .questlock/writer-b.token)
./bin/questlock serve
```

The default address is `127.0.0.1:8080`; state lives in `.questlock/state.db`.
Create each agent once. Restarting with the same database preserves credentials,
artifact versions, successful operation receipts, and the journal.

To try the Go client example, install Go and open another terminal in this repository:

```sh
export QUESTLOCK_TOKEN="$(cat .questlock/writer-a.token)"
go run ./examples/publish
unset QUESTLOCK_TOKEN
```

The [compilable client example](examples/publish/main.go) reads the current
version, publishes, and retries the exact request. For crash recovery, a worker
must save its intended request in its own private scratch space before publishing.
On a timeout, reuse that request and operation ID. On a conflict, re-read and
reconcile instead of blindly advancing the expected version.

Revoke an agent through the trusted local operator CLI:

```sh
./bin/questlock token revoke --agent writer-a
```

Revoked agent names cannot be reused. Give a replacement credential a new agent
name so old audit records and retry receipts keep their original meaning.

## Security and your machine

Questlock is designed to keep this experiment's access small and explicit:

- **Local by default.** The server binds to literal loopback. Any other bind
  requires `--allow-remote-http`. The Podman recipe uses that flag inside the
  container and publishes only `127.0.0.1` on the host.
- **Credentials stay on the intended connection.** The Go client accepts plaintext
  HTTP only for literal loopback IPs; other endpoints require HTTPS. It rejects
  redirects and ignores environment proxy settings for its authenticated traffic.
- **One scope per credential.** The server chooses the scope from a random token,
  stores only its SHA-256 digest, and rechecks revocation inside the write transaction.
- **No host-file access API.** Artifact keys are logical names stored in SQLite.
  There is no shell execution, filesystem browsing, model call, or telemetry in
  the broker. Explicit model CLI workers call providers with their own API keys. The demo's child processes receive a small explicit environment,
  excluding the parent shell's cloud/API credentials.
- **Private state and bounded input.** New state directories use `0700`; database
  files use `0600`. Artifact text is capped at 1 MiB, requests and responses are
  bounded, and the service applies HTTP timeouts.
- **Separate the worker from the vault.** Only the broker and trusted operator
  provisioning containers receive its persistent volume. Never give an agent the
  database, host secrets, container-engine socket, or the broker's OS credentials.
- **Keep private files out of releases.** Git ignores state, tokens, environment
  files, logs, keys, and binaries. Container builds copy an explicit source allowlist.
  A tracked-source/history guard runs in CI. Publication also receives a dedicated
  secret scan; scanners reduce risk but cannot prove the absence of every secret.

**This is not an agent execution sandbox or a complete safety system.** A permitted
worker can deliberately fetch the current version and publish bad content. CAS
does not validate reasoning, establish task ownership, or fence external tool
effects. Configure worker OS/network isolation separately. The trusted host
operator can read and change the database.

Read the [security model and reporting guidance](SECURITY.md) before exposing it
beyond a local experiment. Never post credentials, private databases, or sensitive
artifact content in issues or logs.

## Project map

```text
questlock/
  cmd/questlock/       CLI and real process-failure quest
  client/             Public Go client library
  inference/          Context planning, provider adapters, shared claims/budgets
  delegation/         Explicit local-first policy and deterministic quality gates
  revision/           Checked literal edits, immutable provenance, no model calls
  evaluation/         Six-case comparisons with fresh/reuse measurements
  observability/      Optional content-free Langfuse OTLP metadata and scores
  worker/             Bounded MCP stdio tool for approved request files
  protocol/           Shared JSON types
  internal/httpapi/   Authenticated HTTP boundary
  internal/store/     SQLite transactions, scopes, versions, receipts
  examples/publish/   Minimal compilable integration
  deploy/quadlet/     Rootless Linux service definition
  deploy/sglang/      Pinned runtime argv profiles and qualification instructions
  docs/               Architecture, API, deployment, validation
  scripts/            Installer, release packaging, public-source guard
  assets/             Repository artwork
```

[Architecture](docs/architecture.md) · [HTTP API](docs/api.md) ·
[Deployment](docs/deployment.md) · [Verification record](docs/validation.md)

```sh
make check
make quest
./bin/questlock model quest
```

The tests cover competing writer processes, scope isolation, revocation, atomic
rollback, missing/malformed content, lost responses, replay after newer versions,
and worker/broker crashes. Linux deployment has also been exercised with rootless
Podman and systemd/Quadlet; see the verification record for versions and limits.

SQLite serializes writes. This prototype has no fleet scheduler, replication,
quotas, automatic retention, or demonstrated multi-host scale. Versions and audit
history grow over time. Process-crash tests do not establish power-loss durability.

## Bring your own failure case

Run the quest, then [fork Questlock](https://github.com/spfuzzylink/questlock/fork)
to explore a failure case or add a small integration example. Reproducible bugs,
missing concurrency or recovery tests, and clearer installation notes are useful
contributions. [Start here](CONTRIBUTING.md), or
[open an issue](https://github.com/spfuzzylink/questlock/issues/new) with what you
tried and what happened. Discuss larger changes before starting them.

Community discussion: [r/coolgithubprojects](https://www.reddit.com/r/coolgithubprojects/comments/1wsyenb/experimental_gosqlite_broker_for_shared_agent/).

## Credit and license

Questlock was created and is maintained by
[@spfuzzylink](https://github.com/spfuzzylink), with community contributions.
Questlock's own code is [MIT licensed](LICENSE). Bundled dependencies retain their
original terms; [third-party notices](THIRD_PARTY_NOTICES.md) ship with the archives
and container image.

[Attribution](ATTRIBUTION.md) separates required license notices from optional
visible credit. [Brand guidance](BRANDING.md) explains how to identify upstream
Questlock and independent forks or services. Built out of curiosity, one quest
at a time.
