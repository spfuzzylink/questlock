# Where Questlock fits

Questlock is useful when cooperating workers publish small pieces of shared text
and need to detect stale updates or recover the result of an uncertain publish.
It runs on one trusted host and stores UTF-8 artifacts of up to 1 MiB each.

The workflows below are plausible integration patterns, not bundled agent
framework integrations or claims of production adoption. The shipped pieces are
the HTTP API, Go client, scoped credentials, version checks, successful-operation
receipts, publication journal, and process-failure quest.

## Who this could help

The useful outcome is a shared artifact whose newer version is not silently
replaced by an update based on an older version, with a recoverable result when
a publish response is lost. These are candidate team workflows to evaluate:

| Team | Concrete pain | What Questlock could help with | Integration still needed |
| --- | --- | --- | --- |
| Research and knowledge-tool teams | An agent finishes using notes that another worker has already updated. | Keep the newer shared handoff notes current and surface the stale update for reconciliation. | Connect note reads and publishes to the API; merge, discard, or regenerate conflicting work. |
| Support automation teams | Two assistants prepare competing versions of the same case summary. | Reject a stale replacement of the shared summary draft before the application chooses what to use. | Connect the support system, validate drafts, and implement review and reconciliation. Ticket updates and customer messages remain outside Questlock. |
| AI evaluation teams | A summary publish times out after it may have committed. | Recover the original accepted summary by retrying the saved request, without publishing another version for that operation. | Persist the intended request and operation ID, validate report content, and decide when to retry. |
| Platform teams and homelab builders | Worker failures and restart behavior are difficult to reproduce and inspect. | Exercise the process-failure quest and inspect accepted, conflicting, and replayed publishes in a scoped journal. | Connect representative workers, instrument the workload, and operate the host; Questlock supplies no fleet scheduler. |

These outcomes depend on workers using the publishing API and handling its
responses. They are not adoption claims or evidence of reduced operating cost,
better model output, or production readiness.

### What to measure in a small pilot

- **Observed stale-write rejections:** count `version_conflict` events and inspect
  which competing updates caused them. Decide whether rejected work should be
  discarded, recomputed, or reconciled; a high conflict count alone is not proof
  of useful work saved.
- **Retry correctness:** deliberately lose a publish response, retry the saved
  request, and verify the original artifact is returned with `replayed: true`
  without that retry creating a new version or restoring an older head.
- **Conflict-resolution effort:** instrument the application to count reruns,
  reconciliation attempts, and manual reviews, along with time spent resolving
  them. Questlock does not measure or automate that work.
- **Storage growth:** record database size and growth in artifact versions,
  successful-operation receipts, and journal events over the pilot. Include
  rejected attempts and replays; they also create journal entries. There are no
  built-in quotas or automatic retention.

Start with a bounded workload and use these observations to decide whether the
publishing boundary is worth the integration and operating effort for that team.

## 1. Shared agent notes and handoffs

Two research workers update `notes/handoff.md`. Each reads the current version
before doing its work. If one finishes after another has updated the notes, its
publish receives a version conflict instead of silently replacing newer context.

**Implemented:** reading content and version, compare-and-swap publication, and
an audit record for the conflicting attempt.

**Your integration:** connecting the agent to the API, deciding which notes to
publish, and reconciling changes after a conflict. Questlock does not merge text,
verify a model's conclusions, or protect readers from prompt injection in notes.

## 2. Multiple workers publishing a text artifact

Several workers can produce competing versions of `reports/current.json`, such
as an evaluation summary or a generated plan. A publish names the version the
worker started from. Only a matching update becomes current; the others must
re-read and reconsider their work. Version `0` is a create-if-absent condition.

**Implemented:** atomic publication of one artifact, its new version, successful
retry receipt, and journal entry. Content can be Markdown, JSON, or other UTF-8
text; JSON inside the content string is not schema-validated by the broker.

**Your integration:** worker execution, input selection, output validation, and
conflict resolution. Publishing several keys is not a transaction across those
keys. Publishing a plan does not execute it or make external actions atomic.

## 3. Recovering after a timeout or worker restart

A worker sends a publish and loses the connection before receiving the response.
It cannot infer whether the write committed. If it saved the intended request
before sending, it can retry that exact request with the same agent credential
and operation ID.

**Implemented:** if that request succeeded, the broker returns its original
artifact with `replayed: true`, even if another worker has since published a newer
version. Replaying does not move the current version backward. If no successful
receipt exists, the normal version check still applies.

**Your integration:** durable storage of the worker's pending request, deciding
when to retry, and handling a conflict if another write won. The Go client does
not retry automatically. These semantics cover publication in Questlock, not an
LLM call, email, payment, tool invocation, or other external effect.

## 4. Separate workspaces for separate experiments

A homelab can use a `research` scope for one experiment and a `summaries` scope
for another. Both may use the key `notes/current.md`; their artifacts and journals
remain separate. Workers collaborating within one experiment receive different
agent credentials assigned to the same scope.

**Implemented:** the broker derives the scope from each credential. Reads,
publishes, metadata lists, and journal queries are restricted to that scope.
The trusted local operator creates and revokes credentials through the CLI.

**Your integration:** deciding scope membership and delivering credentials to
workers. Every valid credential can read and publish every key in its scope;
there are no per-key roles or read-only tokens. Scopes are not process sandboxes
or resource quotas. Keep the SQLite volume and operator credentials away from
workers, and configure their OS and network isolation separately.

## 5. A storage and recovery experiment on a small machine

Run `questlock quest` to observe a real worker process being killed, another
worker publishing, and the old request being rejected after restart. The same
quest hard-kills the broker, reads the acknowledged artifact after recovery,
and retries the acknowledged publish to recover its original result.

**Implemented and tested:** those deterministic process-failure cases, plus
competing writer processes and transaction rollback when journaling fails.
Rootless Podman and systemd/Quadlet deployment has also been exercised; the
[validation record](validation.md) describes what was actually run.

**Your experiment:** connecting real agents, measuring your workload, extending
fault injection, or comparing deployment approaches. There are no published
throughput claims here. One host and a serialized SQLite writer do not establish
fleet scale, multi-host failover, or durability under a physical power cut.

## A small integration sketch

Use an existing scoped credential and keep the broker on loopback for a local
experiment. An HTTP worker can follow this sequence:

1. Read `GET /v1/artifact?key=notes%2Fhandoff.md` with its bearer credential.
   Retain the returned version with the content the worker used. A `not_found`
   response means the expected version for creation is `0`.
2. Construct an operation ID unique to this agent's logical publish. Save the
   complete intended request in the worker's private scratch storage before
   sending it if recovery after a worker crash matters.
3. Publish through the same API. For example, if the worker read version `7`:

```http
POST /v1/artifact HTTP/1.1
Host: 127.0.0.1:8080
Authorization: Bearer <scoped-token>
Content-Type: application/json

{
  "key": "notes/handoff.md",
  "expected_version": 7,
  "operation_id": "research-task-42-publish-1",
  "content": "Updated findings ready for the next worker."
}
```

The credential and version above are illustrative. On an uncertain response,
retry the saved request unchanged. On HTTP `409` with `version_conflict`, read
the current artifact and reconcile before constructing a new request and ID.
Do not simply copy the reported version onto an old result to force it through.
An `operation_conflict` means an already-successful ID was reused with different
request data; fix the caller's request/ID handling.

For Go, the corresponding methods are `client.New(baseURL, token)`,
`Client.Get(ctx, key)`, and `Client.Publish(ctx, protocol.PublishRequest{...})`.
The [compilable example](../examples/publish/main.go) demonstrates a read,
publication, and exact retry. Its request stays in memory; add private durable
intent storage to recover across a real worker restart. See the
[HTTP API](api.md) for the full request and error contract.

## When another component is needed

- **Task ownership or worker leases:** a version check does not grant a worker
  exclusive ownership or stop an old worker from running. A permitted worker
  can fetch the latest version and publish again.
- **Agent execution safety:** Questlock does not restrict shell commands, model
  access, host files, or network calls. It does not judge whether content is safe
  or correct, and the trusted host operator can inspect or alter the database.
- **Large or binary artifacts:** content is limited to 1 MiB of UTF-8 text. This
  is not an object store or a binary transfer service.
- **Fleet management or high availability:** there is no scheduler, replication,
  consensus, multi-host coordination, or automatic failover.
- **Bounded long-running storage:** versions, receipts, and journal entries grow
  without quotas or automatic retention. Lists are capped and have no pagination;
  the API does not expose a general historical-content browser.

The current experiment is a narrow publishing boundary. Its value is making
stale writes and uncertain publication results explicit, so the worker can handle
them deliberately.
