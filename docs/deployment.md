# One Linux host with rootless Podman

This deployment runs one Questlock broker under a user systemd manager. It is a
single-host experiment, with no replication, failover, placement scheduler, or
fleet-scale result implied. The Go binary can also run directly on macOS; these
Quadlet instructions require Linux and do not run in the macOS service manager.

The broker listens on `0.0.0.0:8080` **inside** its container. Only
`127.0.0.1:8080` is published on the host. The container runs as UID/GID `65532`,
with a read-only image, a 16 MiB temporary directory, no Linux capabilities, and
limits of one CPU, 256 MiB memory, and 128 processes.

The container explicitly opts into `--allow-remote-http` because its listener
must be reachable from the host port mapping. The host binding remains the
literal loopback address above. Native runs default to a loopback listener and
`.questlock/state.db`; this deployment keeps its database at `/data/state.db`.

## Prerequisites

- A Linux host with Podman 5 or newer, Quadlet, systemd, and cgroup v2.
- A working rootless Podman setup, including subordinate UID/GID ranges and
  delegated CPU, memory, and process controllers for the user service.
- `curl` for the checks below. Run the commands from the repository root, as the
  same unprivileged Linux user throughout.

Check the host before building:

```sh
podman version
podman info --format '{{.Host.CgroupsVersion}}'
systemctl --user status
```

The cgroups value must be `v2`. If a container cannot apply its resource limits,
fix user-session controller delegation instead of removing the limits. The
[Podman rootless troubleshooting guide](https://github.com/containers/podman/blob/main/troubleshooting.md)
covers host configuration problems.

## Build and provision

The multi-stage `Containerfile` builds the Go binary and puts it in a scratch
image. Its copy instructions and `.dockerignore` allow only Go build inputs and
the license: local state, credentials, Git history, docs, and unrelated checkout
files do not enter the image build. It also works with
`docker build -f Containerfile`; the remaining service instructions use Podman.

```sh
podman build -f Containerfile -t localhost/questlock:dev .
podman volume create questlock-data
umask 077
install -d -m 0700 .questlock/tokens
```

Run each provisioning command once for a fresh installation. Each prints a bearer
token to its protected local file; keep those files out of version control. The
subshell's `set -C` refuses to overwrite an existing token file, and its `umask`
creates new token files with mode `0600`.

```sh
(umask 077
set -C
podman run --rm --network none --read-only \
  --cap-drop all --security-opt no-new-privileges \
  --volume questlock-data:/data:Z,U \
  localhost/questlock:dev \
  token create --db /data/state.db --agent writer-a --scope demo \
  > .questlock/tokens/writer-a.token)

(umask 077
set -C
podman run --rm --network none --read-only \
  --cap-drop all --security-opt no-new-privileges \
  --volume questlock-data:/data:Z,U \
  localhost/questlock:dev \
  token create --db /data/state.db --agent writer-b --scope demo \
  > .questlock/tokens/writer-b.token)
```

The `U` mount option assigns the named volume to the container's non-root user;
the same UID/GID is used for provisioning and service operation. `Z` supplies a
private SELinux label. These options apply to the dedicated named volume, not to
the source checkout. See Podman's documentation for
[volume ownership and labels](https://docs.podman.io/en/latest/markdown/podman-run.1.html#volume-v-source-volume-host-dir-container-dir-options).

Only the broker and these trusted provisioning containers receive the data
volume. Agents receive their individual token and the HTTP endpoint. Untrusted
agents must have no host mounts, container-engine socket, broker volume or
database access, or host storage credentials. Keep any per-agent scratch storage
separate. The container's non-root UID does not protect the database from the
host user who owns and operates Podman.

## Install the user service

```sh
mkdir -p "$HOME/.config/containers/systemd"
install -m 0644 deploy/quadlet/questlock.container \
  "$HOME/.config/containers/systemd/questlock.container"
systemctl --user daemon-reload
systemctl --user start questlock.service
systemctl --user status questlock.service
curl --disable --fail --silent --show-error --noproxy '*' --retry 10 --retry-connrefused \
  --retry-delay 1 http://127.0.0.1:8080/healthz
```

The health response should contain `"status":"ok"`. The Quadlet's `[Install]`
section attaches it to the user manager's default target; generated services are
not enabled with `systemctl enable`. To keep the user manager running after
logout and start it at boot, configure lingering for this account:

```sh
loginctl enable-linger "$USER"
```

Host policy may require an administrator for that command. Quadlet's
[user-unit documentation](https://docs.podman.io/en/latest/markdown/podman-systemd.unit.5.html)
describes generation and startup behavior.

These examples address the API from the host. An agent in its own network
namespace needs a deliberately configured route to the broker; its own
`127.0.0.1` is not the host. Keep host publication on loopback and add a private
container network when introducing containerized agents. Do not solve that
problem by exposing the HTTP service on every host interface.

## Verify state across a restart

This writes a fresh key in the `demo` scope. The first response should report
artifact version `1`. Reusing the same operation ID with the identical request
is also a useful retry check. The commands feed the authorization header to
curl through stdin, so the token is absent from command arguments and shell
variables. Curl's [`--header @-` option](https://curl.se/docs/manpage.html#-H)
reads those headers from stdin.

```sh
sed 's/^/Authorization: Bearer /' .questlock/tokens/writer-a.token | \
  curl --disable --fail-with-body --silent --show-error --noproxy '*' \
  --header @- \
  --header 'Content-Type: application/json' \
  --data '{"key":"restart-check","expected_version":0,"operation_id":"deployment-check-1","content":"survives restart"}' \
  http://127.0.0.1:8080/v1/artifact

systemctl --user restart questlock.service
curl --disable --fail --silent --show-error --noproxy '*' --retry 10 --retry-connrefused \
  --retry-delay 1 http://127.0.0.1:8080/healthz

sed 's/^/Authorization: Bearer /' .questlock/tokens/writer-a.token | \
  curl --disable --fail-with-body --silent --show-error --noproxy '*' \
  --header @- \
  'http://127.0.0.1:8080/v1/artifact?key=restart-check'
```

The final response should retain version `1` and content `survives restart`.
This is a restart/persistence check, not a power-loss or disk-failure test.

Inspect the generated configuration and running container when checking the
deployment:

```sh
systemctl --user cat questlock.service
podman port questlock
podman inspect questlock
journalctl --user -u questlock.service -n 50 --no-pager
```

`podman port` should report `127.0.0.1:8080`. Inspect the container to verify the
configured user, read-only root filesystem, data mount, and resource limits on
your host.

## Tokens, updates, and state

Revoke a token through the running broker container, using the trusted host
account. This avoids relabeling the live volume in a second container:

```sh
podman exec questlock /usr/local/bin/questlock \
  token revoke --db /data/state.db --agent writer-a
```

For a code update, rebuild the same local image tag and restart the service:

```sh
podman build -f Containerfile -t localhost/questlock:dev .
systemctl --user restart questlock.service
```

The named volume persists across container replacement. Stop the service before
taking a cold backup and capture the complete volume, including any SQLite
sidecar files. A named volume provides persistence, not a backup. Do not put this
SQLite database on NFS or share it between machines.

`systemctl --user stop questlock.service` stops the broker without removing
data. There is deliberately no volume-deletion step in these instructions.

## Verification scope

The CI workflow is configured to run `go vet`, `go test -race`, and a static
binary build on Linux and macOS. Those checks are separate from container and
service validation.

Local deployment verification on 2026-09-28 used rootless Podman 6.1.2 in a Linux
arm64 virtual machine under the macOS Apple hypervisor, with SELinux enabled.
The VM had no host-folder shares. The image built, both non-root provisioning
containers wrote the volume, and the supplied Quadlet started under the user
systemd manager. HTTP checks covered publish, stale-write
rejection, persistence and authentication across a service restart, idempotent
replay, and live token revocation. Container inspection confirmed the configured
UID, loopback publication, read-only image, dropped capabilities, blocked
privilege escalation, absence of host bind mounts, and CPU/memory/process limits.
The documented curl authorization through stdin passed. A non-loopback listener
without `--allow-remote-http` was rejected. Inspection of the builder's source
tree confirmed that only the allowed production Go inputs and compiled binary
were present.

The compiled `questlock quest` also passed in the read-only Linux container with a 64 MiB
`/tmp` tmpfs, including its worker kill, stale retry, broker kill/restart, and
durable-journal checks.

These checks do not establish hardware power-loss durability, high availability,
multi-host behavior, or fleet scalability.
