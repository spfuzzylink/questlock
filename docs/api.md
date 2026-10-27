# HTTP and Go APIs

## Call from a Go application

Use the public `execution` package to put an existing `inference.Runner` behind
one process-local admission controller. It keeps shared claims, results and
opt-in scope/day quotas in the broker. Providers and telemetry stay explicit.
`execution.LoadSpec` reads a bounded task configuration; `admission.LoadConfig`
reads resource settings. [Complete example](go-integration.md).

For optional semantic candidate observations, the
[shadow guide](semantic-shadow.md#from-go) shows the checked workflow adapter.
For serving a previous answer to a differently worded read-only request, use the
[controlled reuse adapter](controlled-reuse.md). It adds source provenance,
current checks and a local validity verifier before publication. The HTTP index
still returns references only; it cannot authorize an answer by itself.

## Broker HTTP API

The native broker defaults to `http://127.0.0.1:8080`. Every `/v1/` endpoint requires
`Authorization: Bearer <token>`. JSON responses have `Cache-Control: no-store` and
`X-Content-Type-Options: nosniff`.

| Method | Route | Behavior |
| --- | --- | --- |
| GET | `/healthz` | Process liveness, without authentication; not a DB readiness probe |
| POST | `/v1/artifact` | Existing unpinned publish with an expected version and operation ID |
| POST | `/v1/artifact/pinned` | Publish only with complete matching authority/scope/agent pins |
| GET | `/v1/artifact?key=memory%2Fsummary.txt` | Read the current content and version |
| GET | `/v1/artifacts` | First 1,000 keys sorted by key; content is empty in this metadata view |
| GET | `/v1/audit?limit=100` | Latest scoped publish events, newest first; maximum 1,000 |
| GET | `/v1/identity` | Authenticated persistent authority, scope and receipt agent; no query fields |
| POST | `/v1/sync` | Bounded snapshot and accepted changes in the token's scope; read-only |
| POST | `/v1/semantic/query` | Bounded candidate search in the authenticated scope; never returns an answer |
| POST | `/v1/semantic/entry` | Index a checked caller's complete, fresh result reference; preserves artifacts and claims |

The semantic routes accept strict JSON (including duplicate-field rejection),
at most 128 KiB. They reject query strings. `client.QuerySemantic` and
`client.PutSemantic` expose the typed Go contract. The index is created lazily:
capacity failure in this optional feature does not prevent ordinary exact reads.
See [limits, source validation and scope rules](semantic-shadow.md).

## Publish

```json
{
  "key": "memory/summary.txt",
  "expected_version": 0,
  "operation_id": "task-42-publish-1",
  "content": "A result ready to share."
}
```

All fields are required. Empty content is allowed; omitted or null content is
rejected. Unknown fields and trailing JSON are rejected. Content is UTF-8 text,
up to 1 MiB; JSON is not a byte-preserving binary transport. Standard Go JSON
normalization applies, including last-value handling of duplicate object fields.

Keys are logical relative names, up to 512 bytes, without empty, dot, parent, or
backslash components. They are never interpreted as host paths. Operation IDs
are nonblank UTF-8 strings of 1–128 bytes without control characters. Version 0
means the artifact must not exist.

Success returns HTTP 200:

```json
{
  "artifact": {
    "scope": "weekend",
    "key": "memory/summary.txt",
    "version": 1,
    "content": "A result ready to share.",
    "updated_at": "2026-01-01T00:00:00Z"
  },
  "replayed": false
}
```

The timestamp above is illustrative. An exact successful retry returns the
original artifact with `replayed: true`, even if the current head has advanced.

## Optional publication identity pins

`GET /v1/identity` returns `{authority, scope, agent_id}` derived from the bearer
token and private authority metadata. It accepts no claimed identity/query.
`client.PublishPinned` uses the dedicated `POST /v1/artifact/pinned` route and
sends all three preconditions:

```text
X-Questlock-Expected-Authority: <persistent authority identity>
X-Questlock-Expected-Scope: <authenticated scope>
X-Questlock-Expected-Agent: <receipt agent>
```

Missing, partial, repeated, empty or malformed pins are HTTP 400 on this route.
An older broker returns 404 for the dedicated route before any publication;
the client never falls back to the original publish route. Pin headers alone
on the old route cannot establish compatibility with older brokers. A different
active authority/scope/agent is HTTP 409 `identity_conflict`, checked inside the same
SQLite publication transaction before receipts or versions change. Credential
revocation remains HTTP 401. The original `POST /v1/artifact` route and
`Client.Publish` preserve their existing unpinned behavior. The durable [edge SDK](second-gate.md) uses these
pins, immutable persisted requests, explicit conflict resolution and an atomic
pull cache. It never treats an uncertain negative retry as proof of no write.

## Read-only synchronization

`POST /v1/sync` accepts only `cursor`, `limit`, and `max_bytes`. Empty cursor
starts a snapshot; its returned cursor continues frozen pages, then accepted
changes. Requests reject duplicate, unknown, null and trailing fields. Every
response envelope, entry and artifact field must be present and non-null;
intentionally empty artifact content remains valid. Pages contain raw artifact
content; remote endpoints require HTTPS. There is no
incoming journal, supplied scope/agent authority, or automatic upstream write.

See [SecondGate](second-gate.md) for page limits, cursor persistence, safe backup
recovery, the [editable flow](diagrams/second-gate.drawio), and six offline cases.

## Errors and auditing

| Status | Code | Meaning |
| --- | --- | --- |
| 400 | `invalid` | Invalid fields, content, key, limit, or version |
| 401 | `unauthorized` | Missing, invalid, or revoked credential |
| 404 | `not_found` | Missing artifact in the authenticated scope, or unknown route |
| 405 | `method_not_allowed` | Incorrect HTTP method |
| 409 | `version_conflict` | Expected version no longer matches the head |
| 409 | `identity_conflict` | Optional publication pin differs from the authenticated authority/scope/agent |
| 409 | `operation_conflict` | A successful operation ID was reused with different data |
| 413 | `too_large` | Encoded request body exceeds its limit |
| 413 | `sync_item_too_large` | Next complete sync item exceeds `max_bytes`; raise the limit and keep the cursor |
| 500 | `internal` | Internal failure; filesystem/DB details are withheld |
| 500 | `archive_corrupt` | Referenced cold content failed verification; preserve the claim and recover verified storage |
| 503 | `archive_unavailable` | Referenced cold storage is unavailable; do not treat the task as missing |
| 507 | `storage_limit` | Configured storage capacity reached; no publication was acknowledged |

Errors use `{code, message, current_version?}`. A zero current version may be
omitted. Re-read and reconcile conflicts; never blindly increment the expected
version to force an old result through.

Journal outcomes are `accepted`, `version_conflict`, `operation_conflict`, and
`replayed`. A replay event's `current_version` describes the actual head at replay
time, while its returned artifact can be older. Invalid input and authentication
failures do not enter this publication journal.

There are no network token-administration endpoints. A trusted local operator
uses the CLI to create and revoke credentials.
