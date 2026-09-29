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

This is not an agent sandbox, fleet scheduler, lease/fencing service, vector
database, or production HA system. It cannot make external tool side effects
exactly-once or establish multi-host scalability. Read `SECURITY.md` for boundaries.

Checksums are not independent signatures. macOS packages are not Developer ID
signed or notarized. No credentials or private state are shipped.
