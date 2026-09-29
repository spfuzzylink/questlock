# Security model

Questlock is a single-host publication gate for cooperating agent workers. It
protects the versioned artifacts stored through its API. It does not execute or
sandbox arbitrary agent code, inspect prompts, or authorize external tool calls.

## Trust boundaries

The broker process, local operator, host OS, filesystem, and SQLite library are
trusted. Workers get a bearer credential for one scope and a reachable API address.
Agents sharing a scope can read and write every key in that scope and read its
journal. There are no per-key roles or read-only tokens in this version.

An authenticated worker is allowed to submit content. Version checks stop stale
publication, not malicious or incorrect content from a worker that knows the
current version. Task generation fencing and leases are not implemented.

The operator must prevent direct worker access to the database/volume, broker
credentials, container engine, host secrets, and alternative mutation paths.
Giving a worker the broker's host identity defeats this separation.

## Controls and limits

- The server defaults to literal loopback; non-loopback HTTP binds require an
  explicit flag. The service has no built-in TLS termination. Use a trusted local
  reverse proxy with TLS and authentication appropriate to your deployment before
  crossing an untrusted network. Never publish the container port on all interfaces.
- The client requires HTTPS for non-loopback destinations, rejects redirects,
  uses certificate verification, and disables environment proxies. Localhost
  hostnames are not accepted for plaintext HTTP; use `127.0.0.1` or `[::1]`.
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
- Request sizes, response sizes, and HTTP timeouts are bounded. Aggregate storage,
  request concurrency, and rate limiting are not application-level quotas. The
  Podman recipe applies resource limits, but an authorized client can still exhaust
  the allocated resources or grow stored history.
- Content and audit data are not encrypted by the application. Use appropriate
  host disk protection and backups. Tokens may be visible to their owning OS user
  while a client runs. Do not share that identity with untrusted workloads.

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
