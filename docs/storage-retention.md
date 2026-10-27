# Local storage retention

**Questlock** can keep older work available while moving eligible payload bytes
out of the hot SQLite database. The broker retains version identities, current
pointers, successful-operation receipts, audit history, claims and budget state.
Cold content lives in immutable local gzip files named by the SHA-256 of the
original bytes. Reads and exact reuse verify those bytes without a model call.

This is opt-in local retention in v0.12.0. It is useful when a shared broker has
months of completed work, but the hot database need not carry every old payload.
It does not delete history or make a reused result valid beyond its original
reuse policy. Compression and physical disk savings depend on the content.

[Checks and limits for this release](storage-validation-v0.12.0.md).

![Hot coordination state, immutable cold payloads and verified reads](diagrams/storage-retention.svg)

[Editable draw.io source](diagrams/storage-retention.drawio).

## What can move

An archive batch selects nonempty content older than `hot_history_days`:

- Noncurrent versions beyond `keep_recent_versions`. With the default of 2,
  the current version and its immediately preceding version stay hot.
- When `archive_completed_current` is true, recognized complete
  `inference/v1/jobs/` results and immutable version-1 `revision/v1/results/`
  results can move even while current. Their embedded identity must match the key.
- That same opt-in also covers well-formed closed controlled-reuse aliases and
  their immutable `semantic/v1/provenance/` question records. Their source
  references, hashes and response evidence stay intact. Archival does not approve
  an answer: normal reuse still checks the original source and expiry on read.

Opaque current values, pending or uncertain claims, budget ledgers, team state
and outcome records stay hot. Archival never deletes their identities or older
versions. It changes payload placement, without expiring a claim, releasing a
reservation, resetting a task or granting permission to generate again.

The broker writes and synchronizes a cold file before committing its reference
and the removal of its inline payload in one SQLite transaction. Identical
content can share one file. Interrupted work may leave unreferenced final files;
they remain retained and count toward cold capacity. Reads check the digest,
uncompressed length and gzip representation. Missing or corrupt files stop the
read, exact retry or reuse; preserve the claim and recover verified storage.

Each call scans at most `max(64, batch_items × 16)` metadata rows and inspects
at most `max(64 KiB, batch_bytes)` payload bytes, using the effective invocation
budget when adaptive admission reduces it. Output keeps the configured item and
byte caps. A private SQLite checkpoint advances past skipped records atomically
with archival and wraps at the end. Dry runs do not advance it. The checkpoint
table is allocated only after retention is enabled; it changes no artifact or
claim format. Existing restore-before-downgrade guidance still applies.

`scanned_items` and `scanned_bytes` describe that work. A result with zero archived
items and `reason: "scan_limit"` means the traversal is unfinished, so continue
with later non-dry batches. It does not establish that no eligible payloads
remain. Restarting the broker preserves progress; opaque or malformed current
records cannot permanently hide later eligible records behind the scan cap.

## Choose a complete policy

Generate a policy for your own absolute local path:

```sh
umask 077
questlock storage config --cold-dir /absolute/private/questlock-cold > retention.json
```

Review it before configuring the database. The
[complete JSON example](../examples/storage/retention.json) has the default values
and a placeholder path. All fields are required; unknown, duplicate, missing and
null fields are rejected. Policies are regular JSON files of at most 64 KiB.

First configuration migrates the payload table inside one transaction. Back up
first and allow extra temporary database/WAL space for that copy. A failed
migration rolls back the schema, references and policy together. Custom indexes
or triggers on the rebuilt tables require operator review before migration.

| Field | Default | Meaning |
| --- | --- | --- |
| `schema` | `1` | Policy format. |
| `cold_dir` | Your absolute path | Private local cold directory, outside worker access. |
| `hot_history_days` | `90` | Minimum age before archival; choose, for example, 30, 90 or 180 days. |
| `keep_recent_versions` | `2` | Recent versions kept hot, including the current version. |
| `archive_completed_current` | `true` | Allow the recognized completed results described above. |
| `max_database_bytes` | `268435456` (256 MiB) | Main SQLite page limit; `0` explicitly permits unlimited pages. |
| `max_cold_bytes` | `2147483648` (2 GiB) | Positive logical byte limit for files in the cold directory. |
| `batch_items` | `32` | Maximum payloads selected per archive batch. |
| `batch_bytes` | `8388608` (8 MiB) | Maximum original payload bytes selected per batch. |
| `adaptive` | `true` | Scale or defer batches using local resource signals. |
| `min_free_memory_bytes` | `268435456` (256 MiB) | Memory headroom retained by the adaptive estimate. |
| `memory_percent` | `2` | Share of memory beyond that headroom considered for batch planning. |
| `max_cpu_load_milli` | `1200` | Normalized load threshold for scaling; 1000 represents one unit of capacity. |

Adaptive planning can reduce a batch or defer it when CPU/memory pressure is
high, signals are unavailable, or signals are stale. The estimate allows for
compression overhead but is not an OS resource reservation. With `adaptive:
false`, the fixed item and byte bounds still apply.

## Operate an existing broker

Use the database belonging to the broker your workers already share. Storage
commands require an existing regular database; they do not create a new broker.
For a managed local profile, the usual paths are:

| Profile | Database |
| --- | --- |
| Guided `setup` / `local` | `$HOME/.local/share/questlock/local/state.db` |
| Default Codex / Claude Code plugins | `$HOME/.local/share/questlock/plugins/local/state.db` |
| Manual `serve` from a checkout | `.questlock/state.db`, unless `--db` was supplied |

Honor any custom `--state-dir`, `--db` or plugin path overrides already in use.
Use `questlock local status --state-dir /absolute/private/profile` or the
[plugin doctor](plugins.md#diagnose-update-or-remove) for the selected profile.
These operations need no provider credentials and change no client configuration.

Pause writers and stop the selected broker for the first review. Keep it stopped
if you want to inspect a dry run before any scheduled batch applies the policy.
Then, with the intended existing database and the reviewed policy:

```sh
questlock storage info --db /absolute/private/profile/state.db
questlock storage configure --db /absolute/private/profile/state.db --config retention.json
questlock storage archive --db /absolute/private/profile/state.db
questlock storage archive --db /absolute/private/profile/state.db --apply
questlock storage verify --db /absolute/private/profile/state.db
questlock storage info --db /absolute/private/profile/state.db
```

`archive` defaults to a dry run; `--dry-run` makes that explicit. Only `--apply`
moves content, and it cannot be combined with `--dry-run`. A report's `items` and
`bytes` describe that bounded batch; archive bytes are original payload bytes,
not a measured physical disk reduction. Repeated batches may be needed.

The configured policy persists in SQLite. Restart the selected broker through
its existing launch path. Both `serve` and the managed local broker run one
bounded apply batch every five minutes when a policy is present, starting after
the first interval. A manual service can validate and persist a policy at startup:

```sh
questlock serve --db /absolute/private/profile/state.db \
  --storage-config retention.json --storage-interval 5m
```

Do not start this alongside the broker already using that service address.
`--storage-config` is optional once the policy is persisted.
`--storage-interval` accepts 1 second through 24 hours for `serve`; it changes the
batch schedule, not age eligibility. Scheduled maintenance neither compacts the
database nor deletes archive files.

All database storage commands accept `--timeout`, defaulting to `30s` and capped
at `5m`. `verify` checks SQLite and all referenced cold payloads without printing
content. To inspect one exact historical version deliberately:

```sh
questlock storage history --db /absolute/private/profile/state.db \
  --scope example --key notes/handoff.md --version 1
```

That command prints historical content. Keep its output private. A timeout or
unknown failure requires inspecting retained state before retrying; it is not
permission to clear claims or invent a new operation ID.

## Capacity and backups

The database limit caps main SQLite pages, rounded down to whole pages. Receipts,
identities, audit entries and other metadata still accumulate; when they exhaust
the cap, writes fail rather than deleting coordination history. A policy cannot
set a page cap below the database's existing allocated pages. Inspect, archive
and compact with sufficient capacity, or deliberately raise the limit.

Cold capacity counts logical compressed file bytes, including unreferenced
files, unknown regular files and interrupted temporary files. It is not
filesystem block accounting. There is also a 100,000-data-file inventory bound.
The application has no archive deletion command; restore retains final cold
files too. Do not remove apparent orphans after an uncertain operation.

Archival frees pages for SQLite reuse; it need not shrink the database file.
To reclaim physical main-database space, quiesce writers, close other readers
and explicitly run:

```sh
questlock storage compact --db /absolute/private/profile/state.db --timeout 5m
```

Compaction checkpoints WAL and runs `VACUUM`. WAL, compaction workspace,
filesystem overhead and other host data require extra disk headroom beyond
`max_database_bytes` and `max_cold_bytes`. These settings are not a total host
disk cap, and compaction does not shrink cold files.

Use a trusted local filesystem, never NFS. The cold directory must be owned by
the broker's OS user with mode `0700`; files must be owned regular files with
mode `0600`. Every ancestor must be a real directory without symlinks and remain
under trusted operator control. **Questlock** does not encrypt either store.
Keep the database and cold directory out of worker mounts and public backups.

Back up a consistent SQLite state and every file in its configured cold directory
together. A straightforward procedure is to pause workers, stop the broker,
copy the complete broker state and cold directory while neither is changing,
then verify a restored copy with `storage verify`. Do not copy only a live main
database while ignoring its WAL. Cold files alone are not an independent backup:
the database supplies their versions, references, receipts and coordination state.

## Restore, move or downgrade

Pause workers and stop the broker to prevent scheduled rearchival. Ensure the
main database has room for all original payloads; raise its reviewed policy cap
first if needed. Restore in bounded batches:

```sh
questlock storage restore --db /absolute/private/profile/state.db --max-items 32
questlock storage info --db /absolute/private/profile/state.db
questlock storage verify --db /absolute/private/profile/state.db
```

Repeat `restore` until `archived_versions` is `0`; the CLI accepts `--max-items`
from 1 through 128. Final cold files remain on disk. Only after all payloads are
restored may `storage configure` change `cold_dir`. Prepare a reviewed complete
policy for the new location instead of renaming the active directory behind
the broker.

Restore all archived payloads before attempting a downgrade. Old binaries cannot
resolve cold references and fail reading them. Restoration addresses that
payload boundary; the experimental project does not promise general downgrade
compatibility. Keep a consistent backup and the previous binary before changing
versions. If files are unavailable or corrupt, recover verified copies at the
configured path before proceeding.

## Optional storage telemetry

Add `--langfuse` to a database storage command or `--storage-langfuse` to `serve`
to opt into the existing [Langfuse environment settings](inference-observability.md#opt-in-to-langfuse).
Storage spans contain operation, outcome, bounded error code, item/byte counts
and duration. They exclude content, paths, keys, digests, tokens, provider data,
generation usage and cost. Export failure changes neither retained state nor
the returned content and never retries generation or maintenance.

Storage telemetry is a separate contract from inference traces. A live Langfuse
readback must validate the storage fields before a release claims that evidence;
an export acknowledgement alone is insufficient.
