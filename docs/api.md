# HTTP API

The native broker defaults to `http://127.0.0.1:8080`. Every `/v1/` endpoint requires
`Authorization: Bearer <token>`. JSON responses have `Cache-Control: no-store` and
`X-Content-Type-Options: nosniff`.

| Method | Route | Behavior |
| --- | --- | --- |
| GET | `/healthz` | Process liveness, without authentication; not a DB readiness probe |
| POST | `/v1/artifact` | Publish an artifact with an expected version and operation ID |
| GET | `/v1/artifact?key=memory%2Fsummary.txt` | Read the current content and version |
| GET | `/v1/artifacts` | First 1,000 keys sorted by key; content is empty in this metadata view |
| GET | `/v1/audit?limit=100` | Latest scoped publish events, newest first; maximum 1,000 |

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

## Errors and auditing

| Status | Code | Meaning |
| --- | --- | --- |
| 400 | `invalid` | Invalid fields, content, key, limit, or version |
| 401 | `unauthorized` | Missing, invalid, or revoked credential |
| 404 | `not_found` | Missing artifact in the authenticated scope, or unknown route |
| 405 | `method_not_allowed` | Incorrect HTTP method |
| 409 | `version_conflict` | Expected version no longer matches the head |
| 409 | `operation_conflict` | A successful operation ID was reused with different data |
| 413 | `too_large` | Encoded request body exceeds its limit |
| 500 | `internal` | Internal failure; filesystem/DB details are withheld |

Errors use `{code, message, current_version?}`. A zero current version may be
omitted. Re-read and reconcile conflicts; never blindly increment the expected
version to force an old result through.

Journal outcomes are `accepted`, `version_conflict`, `operation_conflict`, and
`replayed`. A replay event's `current_version` describes the actual head at replay
time, while its returned artifact can be older. Invalid input and authentication
failures do not enter this publication journal.

There are no network token-administration endpoints. A trusted local operator
uses the CLI to create and revoke credentials.
