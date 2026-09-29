# Questlock 0.1.0 — Quest 001

A small Go + SQLite publishing gate for cooperating agents that share text state.
This is an experimental first release for one host.

## Try it

From a checkout of this tag:

```sh
./scripts/install.sh
./bin/questlock quest
```

No Go installation or model account is required for the prebuilt demo. It checks
stale-worker rejection, broker crash recovery, and durable retry receipts using
real subprocesses and temporary state. Success ends with `QUEST CLEARED`.

Or download the archive for your platform, compare its SHA-256 digest with the
matching line in `checksums.txt`, extract it, and run `./questlock quest`.
The archive includes installation instructions, the API guide, and build metadata.

## Included

- macOS (13+) and Linux executables for ARM64 and x86-64.
- Scope-bound bearer tokens, version checks, atomic artifact publication, durable
  successful-operation receipts, and a scoped publication journal.
- An HTTP API, Go client library, deterministic recovery quest, and a rootless
  Podman/systemd deployment guide.
- Native validation of all four packaged targets before publication.

Shared notes, handoffs, and generated reports are integration candidates, not
bundled agent-framework integrations. Clients must handle conflicts and preserve
their pending request for recovery.

## Who this could help

- **Research teams:** detect a delayed agent trying to replace newer shared
  findings or handoff notes, so the caller can reconcile the conflict.
- **Support automation teams:** catch conflicting edits when several agents
  prepare the same case summary. This controls publication, not the correctness
  of the summary or actions taken in a support system.
- **AI evaluation teams:** coordinate updates to a shared text report and recover
  the original successful publish result after its response is lost.
- **Platform builders:** try a small local service for version checks and retry
  receipts before deciding whether their workload needs more infrastructure.

The potential benefit is fewer silent overwrites and a definite result for an
exact retry of a successful publish. These are proposed applications of the
tested storage behavior; there are no production customer or ROI claims.
Agent integration, conflict resolution, output validation, and operational
controls remain the caller's responsibility. See the
[use-case notes](https://github.com/spfuzzylink/questlock/blob/main/docs/use-cases.md)
for workflow details, pilot checks, and limits.

This is not an agent sandbox, fleet scheduler, lease/fencing service, vector
database, or production HA system. It cannot make external tool side effects
exactly-once or establish multi-host scalability. Read `SECURITY.md` for boundaries.

Checksums are not independent signatures. macOS packages are not Developer ID
signed or notarized. No credentials or private state are shipped.
