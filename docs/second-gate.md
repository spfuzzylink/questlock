# SecondGate: durable edge push and pull

SecondGate lets an edge queue a reviewed publish intent durably and keep a
local read view of one authoritative Questlock broker. Every write still uses
the upstream broker's existing version CAS and operation-ID receipt. Pull uses
a consistent snapshot followed by accepted changes. Edges do not accept peer
journals, choose upstream versions, automatically rebase conflicts, or merge
independent authorities.

The implementation is qualified by loopback HTTP, separate private SQLite
stores, concurrent writers and restart fixtures. Actual two-host/TLS deployment
remains unvalidated. No provider/model call or cluster dependency is required.

![Pinned durable outbox publishes through existing CAS; atomic snapshot and changes update the edge cache](diagrams/second-gate.svg)

[Editable draw.io source](diagrams/second-gate.drawio).

## Try the packaged binary

```sh
questlock edge quest
```

This command needs no Go installation, model, credentials, active client, or
external service. It creates temporary private databases and a real loopback HTTP
broker, checks the six scenarios below, prints JSON evidence, and removes its
state. It does not connect two actual hosts or benchmark performance. From a
source checkout the same fixture is `go run ./examples/edge`.

| Case | Class | Passing evidence |
| --- | --- | --- |
| Durable before network | Simple | Reopened outbox retains the exact queued request before any POST |
| Receipt agent pin | Simple | Another authenticated agent cannot use the pinned receipt namespace |
| Current pull state | Simple | An old replay receipt cannot replace a newer authoritative cache head |
| Lost response and restart | Complex | Authority commits, response disappears, uncertain intent survives reopen, exact retry obtains the original receipt |
| Concurrent stale edges | Complex | Exactly one CAS acceptance; the other intent blocks and requires explicit abandonment |
| Paginated restart and catch-up | Complex | Reopen between snapshot pages; concurrent updates stay outside the frozen view and arrive in ordered changes afterward |

## Durable Go edge API

Use a distinct absolute canonical directory for each edge, separate from the
broker database. Supported private filesystem ownership checks are on Darwin
and Linux; other platforms refuse `edge.Open`.

```go
c, err := client.New(endpoint, token) // existing TLS/loopback transport policy
if err != nil { return err }
e, err := edge.Open(edgeDirectory)
if err != nil { return err }
defer e.Close()
if _, err = e.Bind(ctx, c); err != nil { return err }
request := protocol.PublishRequest{
    Key: "report", ExpectedVersion: 0,
    OperationID: "reviewed-task-publish-1", Content: "reviewed result",
}
if _, err = e.Enqueue(ctx, request); err != nil { return err }
intent, err := e.Push(ctx, c, request.OperationID) // exactly one attempt
// Inspect err and intent; retry or resolve only by an explicit caller decision.
_ = intent
return err
```

`Bind` explicitly trusts the first authenticated `{authority, scope, agent_id}`
returned by `GET /v1/identity`, then persists that tuple. The agent matters
because successful operation IDs belong to its receipt namespace. Bind cannot
silently change this tuple. Use a separate edge directory for another identity.
The dedicated `POST /v1/artifact/pinned` route requires all three identity
preconditions and checks them in the same transaction as publication, including
credential activity. Older brokers return 404 before publication; there is no
fallback to the old route, whose unknown headers might otherwise be ignored.
A separate identity preflight cannot close the identity-change race.
This assumes the configured authenticated endpoint is trusted. Pins prevent
accidental authority/credential mismatch, not a malicious server impersonating
the original identity. Never operate copied broker databases as independent
authorities: a copy retains its identity and signing key.

`Enqueue` persists the complete immutable operation ID, expected version, key
and content before any network attempt. Enqueuing the same ID/body returns the
same intent; changing its body is refused even after abandonment. Restarting
never generates a new ID, edits a request or sends it automatically.

| State | Meaning | Explicit next decision |
| --- | --- | --- |
| `queued` | Durable intent; no attempt is recorded yet | `Push`, or `Resolve(id, "abandon")` |
| `uncertain` | Sending began; the authority may already have accepted | `Push` retries the exact request; abandonment is refused |
| `blocked` | First attempt returned a known version/operation conflict | `Resolve(id, "retry")` keeps the same bytes, or `Resolve(id, "abandon")` |
| `accepted` | Valid matching receipt is durably saved | `Push` returns the saved receipt without network |
| `abandoned` | Caller discarded unsent or known-rejected intent | Retained as immutable history; another ID/body needs a separate caller decision |

Push durably records uncertainty before sending. A missing/malformed response,
timeout, cancellation, authentication failure or identity mismatch retains that
state. A negative response on an already-uncertain retry cannot disprove earlier
acceptance and cannot make abandonment safe. Restore the pinned authority and
credential, then explicitly retry the original intent. There is no automatic
retry, new operation ID, version increment, dependent-intent rewrite, or force
option. A genuinely new request requires an explicit caller decision after
reading current authoritative state. Publish receipts may describe historical
accepted versions; only Pull advances the read cache.

`Pull(ctx, c, edge.PullOptions{Limit: 32})` performs one bounded page request
from its own durable checkpoint. Persisted staging survives reopen, remains
invisible until the complete snapshot is promoted, and retains any prior
complete view during `ResetPull`. Every page's artifacts and cursor commit
atomically. Ordered changes must advance each artifact exactly one version;
validation, quota or SQLite failure leaves the previous page/checkpoint intact.
Every response envelope, entry and artifact field is required and non-null;
`content:""` is valid, while omitted/null content is never an empty-value update.
Omitted/null `has_more` cannot promote an incomplete snapshot.
Continue while `HasMore`, then explicitly poll later. `Get` reads only the
complete scope-qualified cache. `ResetPull` explicitly begins a fresh staging
snapshot without discarding intents or the previous complete cache.

The edge directory must be owned mode 0700; lock/database/WAL/SHM/journal files
must be owned regular mode 0600 with one hard link. Symlink paths, noncanonical
paths and permissive files are refused. One lifetime advisory lock prevents
multiple live owners; concurrent method calls return `ErrBusy`. No token is
saved in the edge database. **Raw intent and artifact content is saved there**;
keep the directory private and use SQLite-safe backups while closed or through
SQLite's backup facilities. These checks assume a trusted local account and do
not sandbox another process with the same account's permissions.

Hard quotas are 128 retained intents (including accepted/abandoned history),
64 MiB outbox reservations including eventual receipts, 4,096 items and 32 MiB
encoded artifact JSON independently for the visible and staged cache, and
256 MiB per database file. SQLite also has a maximum page count. There is no
automatic history pruning. `ErrLimit` preserves existing durable records; use a
new explicitly managed edge directory when retained history reaches its cap.

`Status` reports state counts, encoded `PendingBytes` for queued/uncertain/
blocked requests, and total `OutboxReservedBytes`. These are storage/accounting
figures, not measured resident memory or allocated CPU. To use a shared per-host
admission controller, admit a small replication job containing an edge handle,
action and operation ID, then call Push or Pull once inside the executor. Keep
credentials and request bodies in the caller/outbox. Admission pause/rejection
leaves the durable queue unchanged; cancellation or uncertainty stays an
explicit edge decision. Estimate page memory with the configured byte ceiling
and decoding overhead. A shared controller can budget broker, replication and
inference lanes, but cannot relax edge hard quotas or discard accepted intents.
The SDK itself does not start a scheduler or allocate OS resources.

## Transport and authorization

`POST /v1/sync` uses the existing bearer token. Its authenticated scope is the
only scope selected; JSON fields claiming a scope, agent, version, or incoming
mutation are rejected. Revoked tokens cannot resume a cursor. Another active
token in the same scope may resume it. Cursors never grant authentication.

**Pages contain complete raw artifact content**, which may include sensitive
task input and output. That content crosses the configured endpoint. The Go
client permits plaintext HTTP only to literal loopback IPs and requires HTTPS
for remote endpoints. It ignores environment proxies, rejects redirects, uses
the existing timeout and performs no automatic retries. Operators must supply
and secure the remote HTTPS deployment; this increment supplies no TLS proxy,
credential exchange, discovery, or remote deployment setup. Keep tokens out of
command arguments, logs, source control and exported checkpoints.

## Request and response

```json
{"cursor":"","limit":32,"max_bytes":8388608}
```

All fields are optional. The request body is limited to 4096 bytes. `limit` is
1–128, default 32. `max_bytes` is 4096–8388608, default 8388608. Responses include
their final newline in this byte ceiling. JSON escaping is counted; strings
containing control characters can expand sixfold. If the next complete item
does not fit, the server returns HTTP 413 `sync_item_too_large`; increase
`max_bytes` and reuse the unchanged cursor. Items are never truncated or skipped.
The implementation reserves up to 2048 bytes for the envelope and cursor, so a
page can stop below the requested ceiling. One maximum-size valid artifact fits
the maximum page size.

```json
{
  "authority":"<persistent source identity>",
  "scope":"<authenticated scope>",
  "kind":"snapshot",
  "through":42,
  "entries":[{"sequence":39,"artifact":{"scope":"team","key":"report","version":2,"content":"...","updated_at":"..."}}],
  "cursor":"<opaque signed continuation>",
  "has_more":false
}
```

Empty cursor starts a fresh snapshot. `through` freezes the accepted-audit
watermark at that request's read transaction. Snapshot entries contain the
latest accepted version of each key at that watermark, ordered by key. New keys
and updates published during pagination cannot enter the snapshot. All
historical versions remain immutable; no transaction stays open between pages.

When a snapshot finishes, its response cursor switches to changes after that
watermark. Subsequent responses have `kind: "changes"` and contain every newly
accepted immutable version, ordered by `sequence`. A change batch also freezes
its watermark until all its pages are consumed. Replays and rejected write
attempts produce no change entry. Sequence numbers may have gaps and are not
artifact versions, counts, wall-clock times, or authority supplied by a reader.

`has_more` means another page remains inside the current frozen batch. When it
is false, retain the returned cursor and explicitly poll later for a new batch.
An empty change page is normal. An empty cursor always restarts the snapshot;
it does not resume the previous consumer's progress.

## Raw synchronization reader workflow and recovery

Use `client.New(endpoint, token)` and `Client.Sync(ctx, protocol.SyncRequest{})`.
The same method continues with `SyncRequest{Cursor: savedCursor}`. This lower-level
read API returns entries without applying them locally. The `edge` package
implements durable staging, atomic checkpoints and ordered cache application.

1. Start an empty staging view for a new snapshot. Keep any prior complete view
   separate until all snapshot pages have been consumed.
2. Durably consume each complete page, then durably save its returned cursor.
   Commit entries and checkpoint atomically in the reader's own storage when
   practical. Reapplying entries must be idempotent. A continuation inside a
   frozen batch replays the same page with unchanged limits. Empty cursor and a
   completed batch's cursor open a new horizon; repeating those starting
   requests can include newer publications. They are not immutable page IDs.
3. Mark the staged snapshot complete at `has_more: false`. Continue from its
   returned cursor, applying changes only in increasing sequence order.
4. On HTTP 400 `invalid` cursor, stop and verify endpoint, scope and source
   identity. Restart a fresh staging snapshot only after that decision. Never
   translate failure into local acceptance or an automatic upstream write.

Cursors are HMAC-signed and bound to a persistent database identity, scope,
phase and watermark. They survive process restarts because the signing key is
stored privately in SQLite. A different database rejects them. A restored older
backup rejects a cursor whose watermark is ahead of that store. A live database
copy also copies its identity: operating both copies as independent authorities
is unsupported. Do not delete/prune accepted audit or version history; this
protocol has no history-compaction or deletion/tombstone scheme.

Cursor payloads are authenticated, not encrypted; treat them as private scoped
metadata. Back up the complete SQLite store using SQLite-safe procedures,
including its authority metadata. The schema addition is a metadata table and
accepted-audit indexes; existing artifacts, versions and receipts are retained.

## Additional raw synchronization regressions

From the repository root, use Go 1.27.1 with dependencies already downloaded.
Set `GOTOOLCHAIN=local GOPROXY=off` in the command environment for offline runs.
These deterministic fixtures require no model, active client, or remote service;
they are contract tests, not performance benchmarks or actual two-host evidence.

| Case | Passing evidence | Command |
| --- | --- | --- |
| Simple 1: empty scope | Empty snapshot transitions to an empty change checkpoint; corrupt authority state is refused | `go test -count=1 ./internal/store -run '^TestSyncEmptyScopeAndCorruptAuthorityFailClosed$'` |
| Simple 2: scoped HTTP reads | Authentication and strict fields hold; incoming writes cannot change the artifact | `go test -count=1 ./internal/httpapi -run '^TestSyncAuthenticationStrictInputAndNoIngestion$'` |
| Simple 3: SDK page sequence | Snapshot continues into changes; another fixture authority rejects its cursor | `go test -count=1 ./client -run '^TestSyncClientSnapshotChangesAndDifferentAuthority$'` |
| Complex 1: concurrent publications | Frozen snapshot stays at version 1; all concurrent version-2 changes arrive afterward | `go test -count=1 ./internal/store -run '^TestSyncConcurrentWritersAndSnapshotPagination$'` |
| Complex 2: restart and authorization | Reopened store resumes the same frozen page; wrong scope, tampering and revocation fail | `go test -count=1 ./internal/store -run '^TestSyncCursorSurvivesRestartAndRejectsScopeAuthorityTampering$'` |
| Complex 3: older safe backup | `VACUUM INTO` backup rejects an ahead cursor while preserving versions and retry receipts | `go test -count=1 ./internal/store -run '^TestSyncRestoredSQLiteSafeBackupRejectsAheadCursor$'` |

These fixtures do not establish a reduction in hosted assistant usage. A
reduction claim still needs a controlled quality baseline and measured subsequent
runs. The main hosted assistant has not been
routed locally, and no paid provider call was made for these tests.
