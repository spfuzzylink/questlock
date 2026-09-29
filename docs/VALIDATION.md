# Validation record

Verified locally on 2026-09-28. This records executed checks rather than planned
capabilities. It is a prototype validation, not a production certification.

## Native build and tests

Environment: macOS arm64, Go 1.27.1, modernc.org/sqlite 1.60.0.

Executed successfully:

```sh
go vet ./...
go test -race -count=1 ./...
go build -trimpath -o bin/agent-fence ./cmd/agent-fence
./bin/agent-fence demo
```

The test suite passed for `client`, `cmd/agent-fence`, `internal/httpapi`, and
`internal/store`. The protocol and example packages compiled with the module.

Observed behavior:

- Four synchronized writer subprocesses competed on one expected version; only
  one publication won. Independent store connections and concurrent retries were
  also exercised.
- An injected journal failure rolled back the artifact, head pointer, and receipt.
- A killed worker resumed its saved stale request and received HTTP 409 after
  another worker published a newer version.
- Acknowledged artifact content, scoped credentials, and receipts survived a hard
  broker process kill and restart.
- A server committed a request and dropped its connection before returning a
  response. After a newer update, retrying that original request recovered its
  original result without changing the current head.
- A replay of version 1 with head version 3 returned version 1 and recorded
  `current_version: 3` in the replay journal event.
- Scope isolation, forged principals, token revocation, path validation, size
  limits, required fields, malformed UTF-8, and client redirect handling passed.

A separate native localhost service also accepted and returned a real artifact.
The compilable Go example published through it and recovered the same version
on retry.

## Linux deployment

The Containerfile was built under rootless Podman 6.1.2 in a dedicated Linux arm64
virtual machine with cgroup v2 and SELinux enabled. The documented Quadlet service was generated and
started through the unprivileged user's systemd manager.

Executed successfully: non-root token provisioning, health check, publication,
stale-write rejection, service restart, persistent read, identical retry replay,
and live token revocation (HTTP 401 afterward).

Container inspection confirmed UID/GID 65532, a read-only root filesystem,
dropped capabilities, no-new-privileges, a broker-only persistent volume, loopback
port publication, and limits of 256 MiB memory, one CPU, and 128 processes.

The final source was rebuilt, the service restarted, and the persisted artifact
read again. All seven compiled `demo` checks also passed inside the read-only
Linux container with a 64 MiB `/tmp`. The disposable service and VM were stopped
after verification.

Verified local container image ID:
`7eb477c636b3e452fbd5071291800c0b605879781681b278382ca798cf7670e2`.

## Practical limits

No host power cut, disk loss/corruption, network partition, malicious kernel
escape, or multi-host deployment was tested. The process-crash checks do not
prove storage-device power-loss durability. The journal-failure test exercises
transaction rollback; it does not inject a process kill during SQLite's commit.

These workers are deterministic fixtures. No LLM provider or agent framework was
needed to exercise the storage boundary. CAS does not establish task ownership
or prevent an authorized worker from deliberately publishing incorrect content.

The GitHub Actions workflow is included for future publication. Remote GitHub CI
has not run for this local repository.
