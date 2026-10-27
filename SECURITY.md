# Security model

Questlock is a single-host publication gate for cooperating agent workers. It
protects the versioned artifacts stored through its API. It does not execute or
sandbox arbitrary agent code, inspect prompts, or authorize external tool calls.

The optional [local team runner](docs/local-agent-teams.md) executes bounded text
roles through installed Ollama weights or a pinned native Codex OSS client.
Native roles use an isolated home/workspace without imported credentials,
configuration, project instructions or enabled tools. This relies on the trusted
native executable and host OS; read-only execution is not a confidentiality
sandbox. Active Codex/Claude configurations are not changed by a team run.

## Trust boundaries

The broker process, local operator, host OS, filesystem, and SQLite library are
trusted. Workers get a bearer credential for one scope and a reachable API address.
Agents sharing a scope can read and write every key in that scope and read its
journal. There are no per-key roles or read-only tokens in this version.

An authenticated worker is allowed to submit content. Version checks stop stale
publication, not malicious or incorrect content from a worker that knows the
current version. Task generation fencing and leases are not implemented.

Team role claims bind the approved snapshot and candidate but do not add
per-role ACLs or authenticated machine attestations. `--worker-id` is an operator
label; the broker authenticates bearer agents. Scope members remain cooperating,
trusted writers. Pending or uncertain roles have no automatic takeover. A model
review is advisory quality evidence, never a person's acceptance or permission
for an external action.

The operator must prevent direct worker access to the database/volume, broker
credentials, container engine, host secrets, and alternative mutation paths.
Giving a worker the broker's host identity defeats this separation.

## Go execution and shared quotas

The `execution` coordinator calls trusted provider, resource-source and exporter
implementations inside your process. They must honor deadlines and the configured
concurrency. Its queue and reservations are bounded estimates, not OS allocation
or a sandbox. A runtime on another host needs its own capacity controls.

The default coordinator requires both a self-hosted task and the provider's
explicit self-hosted assertion. That declaration is trusted configuration, not
a billing audit. Hosted use requires explicit opt-in; it adds no automatic fallback.

Token and dollar ledgers coordinate cooperating workers in one broker scope.
Configure matching caps on v0.10.0+ participants before traffic, in a dedicated
scope. A new quota cannot cover earlier unbudgeted calls; an old or arbitrary
broker writer can bypass the client policy. These ledgers do not replace API-key
spend controls or meter calls made directly to a provider. Unknown outcomes keep
reservations, and reported overruns freeze further budgeted work for review.
Do not change revisions just to escape pending state.

## Controls and limits

The optional client plugins run a reviewed local launcher through the native
plugin manager. Git installs download a pinned runtime from this project's
GitHub release and verify its checksum before caching it privately. Offline
ZIPs include the runtime. Checksums do not supply an independent signature.
The launcher refuses symlinks and writable-by-others cache paths, preserves an
unexpected runtime and creates no client hooks. Installation and enablement
belong to the native host; the plugin does not rewrite client settings. The
MCP worker still accepts only reviewed requests inside its configured directory,
with paid fallback disabled. This is not an OS sandbox or a control over calls
made by the surrounding assistant. See [plugin lifecycle](docs/plugins.md).

- The server defaults to literal loopback; non-loopback HTTP binds require an
  explicit flag. The service has no built-in TLS termination. Use a trusted local
  reverse proxy with TLS and authentication appropriate to your deployment before
  crossing an untrusted network. Never publish the container port on all interfaces.
- The client requires HTTPS for non-loopback destinations, rejects redirects,
  uses certificate verification, and disables environment proxies. Localhost
  hostnames are not accepted for plaintext HTTP; use `127.0.0.1` or `[::1]`.
  Team clients can explicitly add a private CA PEM with `--ca-file` for an HTTPS
  broker. This adds trust to that client only; it neither changes system trust
  nor disables certificate or hostname verification. Protect the CA distribution
  path and retain server private keys on the trusted proxy host.
- Random tokens are issued once, stored as SHA-256 digests, and revocable. They do
  not expire automatically. Store tokens outside source control and rotate using
  a new agent identity. Existing prototype `af_` token strings remain valid; the
  prefix is not an authorization mechanism.
- New state directories are private; existing parent directory permissions are
  not changed. Keep state on a trusted local filesystem in an operator-only
  directory. Do not use a worker-writable parent or a network filesystem.
- Artifact content, pointers, receipts, and publish audit entries are committed
  atomically. The journal is not tamper-proof against the host operator. It does
  not log failed authentication, invalid input, reads, or every administrative action.
- Request sizes, response sizes, and HTTP timeouts are bounded. Opt-in
  [local cold retention](docs/storage-retention.md) limits main-database pages
  and logical cold-file bytes. It retains identities, receipts and audit history;
  metadata can fill the page limit and stop writes. WAL, compaction workspace,
  filesystem overhead and other host data need separate disk headroom. Request
  concurrency and rate limiting are not application-level quotas. The Podman
  recipe applies resource limits, but an authorized client can still exhaust
  allocated resources.
- Content and audit data are not encrypted by the application. Use appropriate
  host disk protection and backups. Tokens may be visible to their owning OS user
  while a client runs. Do not share that identity with untrusted workloads.

Local cold storage shares the broker's trust boundary. Use an absolute path on
a trusted local filesystem, with an owned mode-0700 cold directory and owned
mode-0600 regular files. Path ancestors must be real directories, without
symlinks. Keep every ancestor under trusted operator control; permission checks
do not make an attacker-writable parent safe. Do not use NFS or mount the cold
directory into workers. SHA-256 and gzip validation detect damaged payloads;
they supply neither encryption nor protection from the trusted host operator.

An unavailable or corrupt archive fails the read; it does not clear a claim,
release an uncertain budget reservation or authorize fresh generation. Preserve
the database and restore verified cold files. Back up a consistent SQLite state
with all files in its cold directory. Cold files alone lack identities and
receipts and are not an independent backup. Restore all archived payloads into
SQLite before a downgrade or cold-directory change; see the
[retention recovery procedure](docs/storage-retention.md#restore-move-or-downgrade).

## Semantic shadow data

The optional [semantic observer](docs/semantic-shadow.md) sends approved input to
an explicitly configured literal-loopback Ollama embedding endpoint. It disables
environment proxies and redirects; model failures produce a diagnostic without
paid fallback or generation retry. Weights are installed separately from the
core binary. Exact reuse bypasses embeddings.

Vectors are sensitive task data, not anonymized summaries. The existing broker
stores them in SQLite behind authenticated, scope-bound semantic query and entry
routes. Scope comes from the bearer credential, not a request-supplied scope.
Entry, scan, input, timeout and TTL limits bound shadow work. The index validates
current source version, hashes, completeness and freshness, including sources in
cold storage; it cannot extend a source's reuse window.

Similarity never authorizes answer serving or changes claims, checks, task
identity or the checked result. A score is not proof of correctness. Same-scope
members remain trusted cooperating writers, and the host operator, SQLite files
and backups retain the existing confidentiality boundary.

## Controlled answer reuse

Controlled answer reuse is a separate, explicit opt-in for read-only text tasks.
It binds the question provenance, source version and hashes, evidence, runtime,
checks and verifier policy. Changed bindings or expired source data cannot be
made reusable by a high similarity score. Pending or uncertain claims are never
cleared to make a candidate fit. Optional peer waiting stops before generation;
it does not cancel another worker.

The local verifier receives both questions, the cached answer and the approved
context. Treat these as sensitive input and untrusted data, including any prompt
injection in historical text. A strict verdict and lexical guards reduce risk;
they do not prove correctness. Qualify the intended workload, retain false
acceptances and abstentions, and keep human review where the consequence needs
it. This feature does not replay external actions or add tool permissions.
See [controlled reuse](docs/controlled-reuse.md) for limits and accounting.

## Publishing and contributing safely

Only reviewed source and documentation belong in this repository. Do not add
databases, tokens, environment files, logs, private keys, dumps, or home-directory
paths. Git ignore rules are a convenience; they do not remove already-tracked
secrets or scrub history.

`python3 scripts/check-public-source.py` inspects this repository's tracked source,
index, and reachable history. It does not read unrelated host directories. CI runs
the guard on full history, plus compilation and behavior tests. A dedicated secret
scanner should also run before publication. Neither is a proof that content is
safe to share: review prose, fixtures, commit metadata, screenshots, and outputs.

If a credential is exposed, revoke it at its issuer and issue a replacement.
Deleting a line or rewriting Git history does not revoke the credential.

## Reporting a vulnerability

If GitHub displays **Security → Report a vulnerability** for this repository,
use that private channel. If private reporting is unavailable, open an issue
requesting a private contact route without including exploit details, credentials,
private data, or a working attack against a real deployment.

This is an experimental project with no response-time or production-support SLA.
