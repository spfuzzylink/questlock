# Validation record

## v0.3.0 candidate observations — 2026-10-14

Version 0.3.0 is not yet published. The local live evaluation used installed
`qwen3.5:4b` through Ollama on an Apple M5 host. All twelve fresh generations had
complete, durable, known, bounded usage; twelve exact reuses skipped generation,
reported zero new usage, and retained the same output hashes. Neither tested
configuration met every quality gate: the 64-token cap passed 4/6 cases and the
256-token cap passed 3/6. The CLI correctly returned no eligible selection and
exited 1. A worker extraction also returned `quality_failed` and withheld its
failed answer on both the fresh call and exact reuse.

The [measurement record](measurements-v0.3.0.md) contains content-free counters,
timings, environment, and failed check IDs. Model warmth, cache state, and load
were uncontrolled. The results establish these observed protocol and guard
outcomes, not a reliable latency benchmark, a general model ranking, or invoices.

The new source includes regressions for current-run versus historical timing,
bounded local streaming, exclusive observability buckets, exporter failures,
strict evaluation gates, cancellation, concurrent `worker_busy`, workspace
paths, explicit escalation, and local work preserving a frozen API ledger.
The disposable self-hosted Langfuse contract is a required reusable CI gate;
actual-server/CI proof remains pending. Record final native, packaging, and CI
results against the concrete release commit before publication. Passing a fake
HTTP exporter test alone does not prove dashboard/API ingestion.

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
