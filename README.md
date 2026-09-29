# Agent Fence

**A small publishing gate for agents that share state.**

An agent stalls. Another finishes the work. The first wakes up with an outdated
result. Agent Fence rejects that stale publish and keeps a durable record of why.

This is a weekend storage experiment: a compiled Go service, a Go client library,
and SQLite on one host. No model provider or Kubernetes installation is required.

## Try the failure case

Requires Go 1.26 or later. Developed and tested with Go 1.27.1.

```sh
make build
./bin/agent-fence demo
```

The demo launches real server and worker subprocesses. It kills a worker after it
saves its intended write, publishes a newer result, restarts the stale worker,
then hard-kills and restarts the broker. It verifies the conflict, the surviving
artifact, the original retry receipt, and the journal. All demo state is temporary.
The workers are deterministic fixtures; this tests storage behavior without an LLM.

```text
PASS  Created shared artifact at version 1
PASS  Killed worker A after it saved a write based on version 1
PASS  Worker B published version 2
PASS  Restarted worker A; its stale publish was rejected with HTTP 409
PASS  Hard-killed and restarted broker; version 2 survived
PASS  Retried the acknowledged operation; original result returned without version 3
PASS  Durable journal contains 2 accepted, 1 rejected, and 1 replayed publish
```

## Run on localhost

```sh
make build
umask 077
mkdir -p .agent-fence
./bin/agent-fence token create --agent writer-a --scope weekend > .agent-fence/writer-a.token
./bin/agent-fence token create --agent writer-b --scope weekend > .agent-fence/writer-b.token
./bin/agent-fence serve
```

The server listens on `127.0.0.1:8080` and uses `.agent-fence/fence.db`. Restarting
it with the same database preserves tokens, versions, receipts, and journal entries.
Create each agent only once. Revoked agent names cannot be reused; issue a new name
when rotating a token so historical receipts retain their identity.

In another terminal, from this repository:

```sh
export AGENT_FENCE_TOKEN="$(cat .agent-fence/writer-a.token)"
go run ./examples/publish
```

That example reads the current version, publishes an artifact, and repeats the
same request to recover its original result. Use the library in an existing agent
loop; keep its token and any pending request in that worker's private scratch space.

```go
c, err := client.New("http://127.0.0.1:8080", token)
// Check err, then persist this request before first publication if crash recovery
// matters. An operation ID belongs to one agent and one exact successful request.
result, err := c.Publish(ctx, protocol.PublishRequest{
    Key:             "memory/summary.txt",
    ExpectedVersion: 0, // 0 creates a missing artifact; otherwise use the last read version.
    OperationID:     "task-42-publish-1",
    Content:         "A result ready to share.",
})
```

Full compilable example: [`examples/publish`](examples/publish/main.go).
Revoke a worker using the local operator CLI:

```sh
./bin/agent-fence token revoke --agent writer-a
```

## What the boundary actually does

Each token is tied to one agent and one shared workspace (called a **scope**).
The server derives the scope from the credential; clients cannot choose another
scope in a publish request. Agents in the same scope can read and publish its
artifacts. Provision separate scopes for separate trust boundaries.

```text
worker A ─┐                   ┌─ immutable artifact versions
          ├─ authenticated ── broker ─ SQLite transaction
worker B ─┘      HTTP          └─ current pointer + retry receipt + journal
```

- **Compare-and-swap:** a publish succeeds only if `expected_version` matches the
  current version. Conflicts return HTTP 409 without changing the artifact.
- **Durable retry receipts:** repeating a successful request with the same agent
  and operation ID returns its original result, even if later versions exist.
  Reusing that ID with different content, key, or expected version returns 409.
- **Atomic storage:** artifact content, current pointer, successful receipt, and
  audit entry commit in one SQLite transaction. Content is stored inside SQLite;
  there is no separate filesystem write that could get ahead of the database.
- **Scoped access:** random bearer tokens are stored as SHA-256 digests. The broker
  checks revocation again inside the publication transaction.
- **Inspectable history:** valid authenticated publish attempts record acceptance,
  version/operation conflicts, and successful retries. Invalid input and failed
  authentication are rejected before this journal; it is not a complete access log.

A rejected request has no successful receipt. Corrected work should use a fresh
operation ID. On a timeout or lost response, resend the exact original request;
do not invent a new ID or automatically update the expected version.

## HTTP API

All `/v1/` routes require `Authorization: Bearer <token>`. Responses are JSON.

| Method | Route | Behavior |
| --- | --- | --- |
| GET | `/healthz` | Process liveness; unauthenticated, not a database readiness probe |
| POST | `/v1/artifact` | Publish `{key, expected_version, operation_id, content}` |
| GET | `/v1/artifact?key=memory%2Fsummary.txt` | Read current artifact and its version |
| GET | `/v1/artifacts` | Metadata for the first 1,000 current keys in sorted order; content is empty |
| GET | `/v1/audit?limit=100` | Latest scoped events, newest first; maximum 1,000 |

Artifact content is UTF-8 text, at most 1 MiB per version. Keys are logical paths,
up to 512 bytes, with no parent/dot/empty components. They are never resolved as
host filesystem paths. There is no delete, retention, historical-content API, or
list pagination in this version. Immutable versions remain in the database.

## Deployment and limits

See [Podman + systemd/Quadlet deployment](docs/DEPLOYMENT.md) for a rootless Linux
service with a persistent broker-only volume, localhost port binding, restart
policy, and a publish/restart/read smoke test. The `Containerfile` also builds
with Docker. Plain Go works on macOS and Linux.

The control applies only to publishes through the broker. Workers must not have
the database volume, the broker's host credentials, or an alternative path to
mutate shared state. Scope enforcement is a service permission boundary, not an
OS sandbox for arbitrary agent code. The container recipe isolates the broker;
worker filesystem/network isolation must be configured separately.

A worker with a valid token can deliberately read the latest version and submit
bad content. Version checks prevent stale updates; they do not validate reasoning
or revoke task ownership on reassignment. Task leases/generation fencing are
future experiments. Neither this service nor a client library makes arbitrary
tool calls exactly-once.

This is one host, one broker, and a local SQLite database with WAL and FULL
synchronization. Acknowledged transactions survive the process-crash tests; host
power loss still depends on filesystem/device durability. SQLite serializes
writes. There is no fleet scheduler, replication, high availability, quota, or
claim of demonstrated multi-host scale. Version and audit storage grow over time.
Use a trusted loopback/network path; add TLS before crossing an untrusted network.

## Verify and develop

```sh
make check
make demo
```

Tests cover process-level competing writers, exact retry behavior, atomic rollback,
revocation and scope isolation, payload validation, HTTP errors, and worker/broker
crash recovery. CI builds on Linux and macOS and runs the Go race detector.

The next experiment is adding another machine and measuring where coordination,
placement, and failure recovery force the architecture to change.

MIT licensed. Built out of curiosity.
