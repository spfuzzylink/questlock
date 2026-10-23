# Storage retention checks — v0.12.0

These are offline correctness checks, run on 2026-10-23. They use synthetic
content and private temporary broker state. No model or paid provider is needed.
They establish storage and reuse behavior, not model quality, GPU performance
or an estimate of someone else's invoice.

| Case | What was checked |
| --- | --- |
| Dry run and archive | A default dry run leaves inline bytes alone. Applying a bounded batch preserves identities, exact historical content and retry receipts. |
| Restart and reuse | Real inference and revision runners read archived completed results through the HTTP broker after restart. A provider that panics on any new count/generation call is never called. |
| Pending and uncertain work | Lost claim acknowledgements and uncertain generation retain claims and unknown quota reservations across archival, restart and a one-year clock advance. |
| Interrupted writes | A child exits after the cold blob is durable but before SQLite commits the move. Hot bytes and receipts survive; a later batch adopts the retained orphan. Injected transaction/migration failures also roll back. |
| Capacity and concurrency | Main database capacity rejects an atomic publication without losing confirmed replay. Cold capacity leaves hot bytes intact. Concurrent writers, deduplication, stale connections, replacement connections and candidate starvation have regressions. |
| Recovery and observation | Missing/corrupt blobs stop reads and reuse. Recovery restores exact bytes. Verify checks foreign keys and archive integrity; restore and explicit compaction preserve behavior. Storage telemetry is allowlisted and content-free. |

`scripts/verify_storage.py --binary /absolute/path/to/questlock` repeats the
packaged MCP path in a clean temporary profile: first checked task, warm reuse,
archive, restart/cold reuse, missing-file HTTP 503, failed task without fallback,
recovery, bounded restore and compaction. The local run archived and restored
two fixture versions, retained the final cold files, and reported **0 model
calls and $0 provider cost**. Those numbers describe this deterministic test;
the hosted assistant building the release has separate account usage.

Run `make check` for the Python publication checks, Go vet and race tests. The
native CI matrix also runs the packaged storage smoke on macOS/Linux,
AMD64/ARM64, alongside existing plugin, onboarding, sync and model-coordination
quests. The disposable Langfuse job sends and reads back an independent
synthetic `archive_corrupt` storage span without generation, token or cost data.
Check the release's actual CI result before treating those platform/live-service
checks as completed.

This does not simulate physical power loss, validate a remote filesystem, test
production backup infrastructure or benchmark terabytes of history. Initial
migration and compaction need disk headroom; the SQLite page cap excludes WAL
and transient workspace. [Policy, recovery and limits](storage-retention.md).
