# Exact edits with no model call

These v0.6.0 source-candidate requests are public fixtures, not real customer
data. Each includes the exact source SHA-256, literal replacements with counted
matches, source/output checks, and opt-in outcome accounting. No provider key or
model is needed. Published v0.5.0 does not implement `revise`.

Build this checkout and preview each request offline:

```sh
go build -trimpath -o bin/questlock ./cmd/questlock
./bin/questlock revise plan --request examples/revisions/bold-product.json
./bin/questlock revise plan --request examples/revisions/bold-mission.json
./bin/questlock revise plan --request examples/revisions/json-status.json
./bin/questlock revise quest
```

| Request | Exact change | Check boundary |
| --- | --- | --- |
| [bold-product.json](bold-product.json) | Two selected prose uses become Unicode bold; `#Questlock` stays plain. | Source companion phrase, bold name, retained hashtag, no original selected prose. |
| [bold-mission.json](bold-mission.json) | One entire mission sentence becomes Unicode bold. | Exact source sentence and exact emphasized output. |
| [json-status.json](json-status.json) | The literal JSON `stage` value changes from `draft` to `review`. | Strict JSON and expected field values; no new GPU benchmark claim. |

The six-case quest also exercises parent provenance/restart, source/count/check
failures, concurrent exact reuse, and scope isolation. It creates its own real
HTTP broker and SQLite state and removes them when done.

For durable publication, start your [existing scoped broker](../../docs/model-workflows.md),
then run with its private credential:

```sh
export QUESTLOCK_TOKEN_FILE="/absolute/path/to/private/worker.token"
./bin/questlock revise run --request examples/revisions/bold-product.json \
  --broker http://127.0.0.1:8080
```

## Chain an exact shared parent

The successful `bold-product.json` result has this immutable key and output
digest. The following complete child request only changes `checked work.` to
`checked results.`. Save it as a private JSON request and run `revise plan` and
`revise run` against the same broker scope as the parent:

```json
{
  "schema": 1,
  "task": "launch-emphasis-child",
  "revision": "approved-1",
  "source": {
    "key": "revision/v1/results/62152327bd51fa51765a7e8399471300cfe425106fb753a103c92934d88a8624",
    "version": 1,
    "sha256": "fa70a69a07a8c1b972db05db4e5e0292789c50acf248943bceb6bdb9efaa1a74"
  },
  "edits": [
    {"old": "checked work.", "new": "checked results.", "expected_matches": 1}
  ],
  "source_checks": [
    {"id": "old-work", "kind": "contains", "text": "checked work."},
    {"id": "tag-source", "kind": "contains", "text": "#Questlock"}
  ],
  "checks": [
    {"id": "new-results", "kind": "contains", "text": "checked results."},
    {"id": "tag-output", "kind": "contains", "text": "#Questlock"}
  ],
  "track_outcome": true
}
```

These constants identify the exact published example above, not arbitrary source
text. Changing its source or edit/check plan changes its digest. For other work,
copy `artifact.key`, `artifact.version`, and `artifact.sha256` from the successful
parent result. Inline text and a parent reference are alternatives. Never use a
mutable head as a substitute for the reviewed version. See the
[request/provenance contract](../../docs/checked-revisions.md) before chaining.

All callers sharing these revision outcome cohorts need v0.6.0; older clients
cannot read the additive `deterministic_edit` cost basis. A recorded run is
unreviewed until a person explicitly accepts it. Checks, Unicode formatting, and
zero model calls establish no general quality or invoice-savings result.
