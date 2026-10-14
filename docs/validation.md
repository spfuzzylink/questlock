# Validation record

## v0.4.0 candidate observations — 2026-10-14

The source candidate adds operator-reported outcome feedback after declared
checks. Local `go vet ./...` and `go test -race ./...` passed, as did all 70 Python
publication/packaging/security regressions and Actionlint. The six new outcome
scenarios passed with real authenticated HTTP and temporary SQLite. They use
synthetic cost and effort, make no model calls, and validate acceptance/rewrite
transitions, receipt replay, inclusive cohort accounting, unknown values, stale
feedback, and recovery after a broker restart.

Root integration regressions verified that enabling tracking preserves exact
inference reuse; a stopped token-cap violation can retain its observed passing
checks and known usage without exposing its answer; and failed outcome
registration returns its original recovery record without another generation.
Library tests cover concurrent scoped records, competing feedback, exact retries
after a lost publication acknowledgement, strict ledger validation, bounds, and
accounting overflow.

### Using Questlock during development

One bounded extraction of public outcome-contract facts used installed local
`qwen3.5:4b` through Ollama with explicit JSON/value checks and no paid tier.
It passed with **81 input / 26 output tokens** and recorded its invocation in the
existing broker scope. One exact repeat reused the passing result with **zero
new generation tokens** and recorded a separate invocation. The report showed
two recorded runs, zero operator-reported acceptances, one missing human-effort
observation, and `cost_per_accepted_task_usd: null`. Assistant review was not
recorded as human acceptance. This is a bounded development observation, not an
invoice saving, a whole-chat token reduction, or a general model-quality result.

Native release archives, tagged CI, and container publication are not yet
confirmed for this candidate. The published v0.3.0 record below remains intact.

## v0.3.0 observations — 2026-10-14

Release source: `3727709908d878becfa29281d3fe3fc0d67f556f` (the peeled `v0.3.0`
tag), with Git tree `42f8169e1d08f65e7dec95e9fe4e3d54baaddbb3`. Release
archives preserve that tagged source and its bundled documentation. These web
docs record later verification and setup clarifications without replacing the
immutable release artifacts.

### Local inference and MCP

The live evaluation used installed `qwen3.5:4b` through Ollama on an Apple M5 host.
All twelve fresh generations had complete, durable, known, bounded usage; twelve
exact reuses skipped generation, reported zero new usage, and retained the same
output hashes. Neither tested configuration met every quality gate: the 64-token
cap passed 4/6 cases and the 256-token cap passed 3/6. The CLI correctly returned
no eligible selection and exited 1. A feature extraction also returned
`quality_failed` and withheld its failed answer on both the fresh call and exact
reuse.

A separate actual MCP ticket extraction passed strict JSON and exact-value checks
with 58 input / 21 output tokens. One exact reuse returned its passing saved
answer with zero current tokens and `generation_called: false`. The official Go
MCP SDK negotiated protocol `2026-07-28` and discovered only `questlock_delegate`.
Complete Codex settings were read by `codex mcp get` and `codex mcp list` in an
isolated configuration home; the existing `notify` array remained intact. Claude
Code's native `mcp get` connected from an isolated complete user profile. Active
client settings were not modified.

The [measurement record](measurements-v0.3.0.md) contains content-free counters,
timings, environment, and failed check IDs. Model warmth, cache state, and load
were uncontrolled. Usage covers delegated provider/local generation and excludes
the surrounding hosted assistant's prompt, tool, and context tokens. The results
establish observed protocol and guard outcomes, not a reliable latency benchmark,
a general model ranking, or invoices.

### Actual Langfuse metadata contract

[CI run 38632010394](https://github.com/spfuzzylink/questlock/actions/runs/38632010394)
passed all seven jobs at commit `510590ed31d3ca7c4890f91a8d71ebe58843e4f7`, whose
Git tree matches the release source above. Its isolated self-hosted Langfuse
v4.59.0 smoke check exported deterministic fixtures and read actual observations
and scores back through the service API. It verified three roots, two generation
children, exclusive normalized cache/reasoning buckets, content-free metadata,
caller-estimated/local-zero costs, zero-new-usage reuse, streamed first-content
and output-content timing, and root quality scores `1/1/0`. The fixtures call no
model and establish no paid charges or model-quality result.

The web process binds `0.0.0.0` inside its container; host publication stays on
`127.0.0.1:3000`. CI cleans up the disposable services and volumes. Your own
Langfuse project is separate and requires manual provisioning plus explicit
export opt-in; the test does not populate a user dashboard.

### Release archives and container

The [v0.3.0 release run](https://github.com/spfuzzylink/questlock/actions/runs/38633672804)
passed all seven validation gates and published the four native archives. Every
published archive was downloaded and verified against `checksums.txt`; its
`BUILDINFO.json` names clean source commit
`3727709908d878becfa29281d3fe3fc0d67f556f`, Go 1.27.1, and its intended target.
The license and notices agree byte-for-byte with that tagged source. Published
archives preserve the source snapshot's candidate-era documentation; these web
docs record the subsequent publication evidence.

| Archive | SHA-256 |
| --- | --- |
| Darwin AMD64 | `7db6d5d45cc1bae24b6fb4d009bee685f0376d8fd2caaceade24040ec15e73c7` |
| Darwin ARM64 | `c65943d89a31e7ab87413f84ef9635d5e20fa6314d670a54cdb209b7a69e0c49` |
| Linux AMD64 | `6644a33dbac9721d114995ee67400e70c03839c65705953e746593824c513bd3` |
| Linux ARM64 | `d744f2156792ae75d0af66c2f7625ca745f464cb5194a7426e44e1ec2949e6a3` |

The [container pipeline](https://github.com/spfuzzylink/questlock/actions/runs/38634508620)
passed both native architectures, version/user/source/license checks, both
recovery quests, and the offline evaluator's negative quality controls. The
published `v0.3.0` and moving `experimental` tags resolve to:

- Index: `sha256:867d9c24f88c21cf6e80dc02b573cf1ea48f9c246bbeec2d4fac5a9bc6ebaae2`
- Linux AMD64: `sha256:53c707aa280b77e156bdf0d49a9d130d4a1af48629dfebb4f860bba69b82aad3`
- Linux ARM64: `sha256:604d22c16400519ed20c8c32bb2d0d0fd0e710b45f199413b9339388efbcb2f1`

Anonymous registry requests verified both manifests, their configs, and all 16
layer references by SHA-256, with exactly two Linux platforms. OCI labels name
v0.3.0, the exact source revision, MIT, and `@spfuzzylink`; both images run as
`65532:65532`. The v0.2.0 digest remains unchanged. No old tags or assets were
replaced.

The verified Darwin ARM64 release executable and embedded skills were installed
on the test workstation. Global local Codex and Claude Code MCP registrations
passed complete-candidate isolated validation, private known-good backups,
independent automatic rollback protection, and native live config reads. Codex's
existing command arrays were preserved; Claude Code reported a connected worker.
This enables explicit bounded delegation in local chats; it does not reroute a
hosted session or eliminate its normal usage.

The release gates cover all four native Darwin/Linux AMD64/ARM64 archives,
source-container execution, source/history publication checks, and the reusable
self-hosted Langfuse contract. The container pipeline uses the confirmed release
archives, runs both quests on native Linux AMD64 and ARM64, and verifies anonymous
pulling after publication. Exact release evidence belongs in this record.

The source also includes regressions for current-run versus historical timing,
bounded local streaming, exclusive observability buckets, exporter failures,
strict evaluation gates, cancellation, concurrent `worker_busy`, workspace
paths, explicit escalation, and local work preserving a frozen API ledger.

## Historical prototype record — 2026-09-28

Verified locally on 2026-09-28. This records executed checks rather than planned
capabilities. It is a prototype validation, not a production certification.

## Native build and tests

Environment: macOS arm64, Go 1.27.1, modernc.org/sqlite 1.60.0.

Executed successfully:

```sh
go vet ./...
go test -race -count=1 ./...
go build -trimpath -buildvcs=false -o bin/questlock ./cmd/questlock
./bin/questlock quest
```

The test suite passed for `client`, `cmd/questlock`, `internal/httpapi`, and
`internal/store`. The protocol and example packages compiled with the module.

Observed behavior:

- Four synchronized writer subprocesses competed on one expected version; only
  one publication won. Independent store connections and concurrent retries were
  also exercised.
- An injected journal failure rolled back the artifact, head pointer, and receipt.
- A killed worker resumed its saved stale request and received HTTP 409 after
  another worker published a newer version.
- Acknowledged artifact content, scoped credentials, and receipts survived a hard
  broker process kill and restart.
- A server committed a request and dropped its connection before returning a
  response. After a newer update, retrying that original request recovered its
  original result without changing the current head.
- A replay of version 1 with head version 3 returned version 1 and recorded
  `current_version: 3` in the replay journal event.
- Scope isolation, forged principals, token revocation, path validation, size
  limits, required fields, malformed UTF-8, and client redirect handling passed.
- Plaintext remote URLs were rejected. HTTPS certificate validation remained on,
  an environment-proxy trap received no credential traffic, and unsafe listener
  addresses were rejected before database creation or network binding.
- Quest subprocesses did not inherit cloud or GitHub credential environment
  variables from the parent process.

A separate native localhost service also accepted and returned a real artifact.
The compilable Go example published through it and recovered the same version
on retry.

## Linux deployment

The Containerfile was built under rootless Podman 6.1.2 in a dedicated Linux arm64
virtual machine with cgroup v2 and SELinux enabled. The VM had no host-folder
shares. The documented Quadlet service was generated and started through the
unprivileged user's systemd manager.

Executed successfully: non-root token provisioning, health check, publication,
stale-write rejection, service restart, persistent read, identical retry replay,
and live token revocation (HTTP 401 afterward).

Container inspection confirmed UID/GID 65532, a read-only root filesystem,
dropped capabilities, no-new-privileges, a broker-only persistent volume, loopback
port publication, and limits of 256 MiB memory, one CPU, and 128 processes.

The final source was rebuilt, the service restarted, and the persisted artifact
read again. All seven compiled `quest` checks also passed inside the read-only
Linux container with a 64 MiB `/tmp`. The disposable service and VM were stopped
after verification.

Verified local container image ID:
`5d29406e1e6737cdfc087df35f49a6a742df166ac20e72d615aecd4715eb7b23`.

The image's builder source tree contained only the production Go allowlist and
compiled binary. No host state, credentials, Git internals, tests, or documentation
entered that tree. Credential-bearing curl calls used stdin rather than putting
the bearer token in the command argument list.

## Publication checks

The public-source guard and its 21 regression tests passed. It inspected tracked
working files, staged blobs, and reachable history/metadata without reading the
host's unrelated directories. Ignored `.local/` helpers stay outside the scan;
force-added helpers are rejected by path, including after removal from the
working tree if they remain in history. A separate Gitleaks 8.30.1 scan reported no leaks
in the existing Git history and the staged-source snapshot. These are pattern
checks, not a proof of complete secrecy or anonymity.

## Packaging checks

The release installer and archive builder were tested locally before publication.
All 37 Python tests passed: 21 publication-guard regressions, 14 installer tests,
and two archive-boundary tests. Installer cases cover failed downloads, checksum
mismatches, malformed/duplicate manifest entries, unsupported targets, unsafe
archive members, symlink destinations, and preserving an existing executable.

A macOS arm64 archive was built, its metadata and executable permissions checked,
and its extracted binary passed the full recovery quest without the Go toolchain
in its runtime path. Archive ownership is normalized so workstation user names
and filesystem paths are not carried in tar metadata. Release builds use baseline
CPU targets and disable cgo.

The expanded CI workflow runs the packaged executable on native Linux and macOS
runners for both amd64 and arm64. The release job depends on all four successful
platform jobs and the source/history guard. Downloaded-release validation is
recorded with the release once the assets are published.

## Practical limits

No host power cut, disk loss/corruption, network partition, malicious kernel
escape, or multi-host deployment was tested. The process-crash checks do not
prove storage-device power-loss durability. The journal-failure test exercises
transaction rollback; it does not inject a process kill during SQLite's commit.

These workers are deterministic fixtures. No LLM provider or agent framework was
needed to exercise the storage boundary. CAS does not establish task ownership
or prevent an authorized worker from deliberately publishing incorrect content.

## Published CI

The [first published CI run](https://github.com/spfuzzylink/questlock/actions/runs/36504998094)
passed for commit `6586b79` on 2026-09-28. Both `ubuntu-latest` and
`macos-latest` passed vet, race tests, the compiled build, and the recovery quest.
The separate public-source and full-history guard job passed as well.

See [GitHub Actions](https://github.com/spfuzzylink/questlock/actions) for later
commits and runner changes.

## 0.2.0 model workflow validation — 2026-10-12

Native environment: macOS ARM64, Go 1.27.1. All Go packages passed vet and race
checks, and all 54 Python publication/packaging regressions passed. Both the
original process-failure quest and the six-case model quest are included in
native release CI; the model fixture uses real authenticated HTTP and SQLite.
OpenAI/Claude provider tests use bounded fake HTTP endpoints, not paid inference.

Independent release review found and verified fixes for count underestimation,
nonreusable usage/cost violations, reservation settlement and freeze after a
failed result publication, and keeping a known answer available when its durable
publication fails. Installer tests cover independent temp homes, CODEX_HOME,
idempotence, customized-skill preservation, explicit replacement, symlinks,
embedded examples, and protected-token helper behavior.

The local path was exercised with real Ollama 0.40.3 and installed Qwen3.5 4B
weights, with `OLLAMA_NO_CLOUD=1` and a literal loopback endpoint. The installed
Codex skill generated a release-candidate handoff: runtime-reported 366 input
tokens and 135 output tokens. The installed Claude skill submitted that exact
snapshot again and recovered the identical durable answer: zero new generation
tokens, original 366/135 usage reported separately as avoided local computation.
These are executed local counters, not cloud invoice savings or a quality score.
The draft still required factual review, like any generated engineering summary.

Codex CLI 0.160.1 and Claude Code 2.1.287 each returned their expected connection
check text through the local Ollama service. This verifies basic local model
turns, not every native tool, repository task, UI path, or foreground app session.
The stock Codex `--oss` probe emitted model-catalog/telemetry warnings before
succeeding; a dedicated local catalog/profile was then configured. Claude Code
reported its custom-model catalog warning before its successful result.

No live OpenAI or Anthropic API generation was executed because their separate
API credentials were unavailable. Their compatibility is fixture-tested; live
model availability, pricing, production billing, and broader output quality
remain to be evaluated. Local inference needs no hosted generation credits,
but consumes hardware, memory, power, and time.
