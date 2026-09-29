# Install and try Questlock

## From a checkout, without Go

On macOS or Linux:

```sh
git clone https://github.com/spfuzzylink/questlock.git
cd questlock
./scripts/install.sh
./bin/questlock quest
```

The installer detects your OS and CPU, downloads the release named in `VERSION`,
checks its SHA-256 digest, and installs one executable in this checkout's ignored
`bin/` directory. It needs `curl`, `tar`, and either `sha256sum` or `shasum`.
It does not require Go, Python, a database server, a model account, a container
engine, administrator access, or a change to your shell configuration.

`quest` runs seven checks involving real worker and broker crashes. A successful
run ends with `QUEST CLEARED`. The demo uses a temporary database and generated
fixture credentials, removes them on completion, and leaves no service running.
It makes only local HTTP requests. The installer itself downloads from GitHub.

This installs the **tagged release**, not edits in your checkout. Use the source
build below when developing or trying unreleased changes.

## Without Git

Download the matching archive from [Releases](https://github.com/spfuzzylink/questlock/releases),
along with `checksums.txt`. Available targets are:

| Platform | Archive suffix |
| --- | --- |
| macOS, Apple Silicon | `darwin_arm64.tar.gz` |
| macOS, Intel | `darwin_amd64.tar.gz` |
| Linux, ARM64 | `linux_arm64.tar.gz` |
| Linux, x86-64 | `linux_amd64.tar.gz` |

The prebuilt macOS binaries require macOS 13 or newer. A native Windows package
is not validated or provided. WSL2 follows the Linux path but has not received
separate runtime validation.

For example, on Linux x86-64, from the download directory:

```sh
sha256sum questlock_0.1.0_linux_amd64.tar.gz
```

On macOS use `shasum -a 256` instead. Compare the result with the **matching
filename's entry** in `checksums.txt` before extracting. Then:

```sh
tar -xzf questlock_0.1.0_linux_amd64.tar.gz
./questlock version
./questlock quest
```

Use your platform's filename in those commands. Each archive contains the binary,
license, documentation, and `BUILDINFO.json` with the source commit and build
target. The packages do not contain a database or any preissued credentials.

The checksum detects corruption or a mismatched download. It is hosted alongside
the archive and is **not an independent signature**. macOS packages are not
Developer ID signed or notarized. If your device policy requires signed software,
use an approved source-build route; do not disable OS security checks.

## Build the code you checked out

Install [Go 1.26 or newer](https://go.dev/doc/install), then:

```sh
go build -trimpath -o bin/questlock ./cmd/questlock
./bin/questlock quest
```

Or use `make quest`. Dependencies download through the Go module proxy on the
first build. The installed binary needs no Go runtime, system SQLite library, or
external service. To run all development checks, install Python 3 and run
`make check`; the Go race detector also needs a working C toolchain.

## Start a persistent local broker

From the checkout, after installing or building:

```sh
umask 077
mkdir -p .questlock
(set -C; ./bin/questlock token create --agent writer-a --scope weekend > .questlock/writer-a.token)
./bin/questlock serve
```

Provision that agent once. `set -C` refuses to overwrite an existing credential.
The service binds to `127.0.0.1:8080` and keeps state in `.questlock/state.db`.
Stop it with Ctrl-C and run the same `serve` command from the same directory to
reuse the data. This is a foreground process; it is not an installed boot service.

Workers receive the API address and a token for their scope; they do not receive
the database. Use the [HTTP API](api.md) from any language or the
[Go client example](../examples/publish/main.go). The Go example requires Go even
when you installed a prebuilt broker. See [use cases](use-cases.md) for integration
ideas, and [rootless Podman + Quadlet](deployment.md) for a persistent Linux user
service with resource limits and automatic restart.

## Updates and removal

Stop a broker before replacing its binary. From an updated checkout, rerun
`./scripts/install.sh` to install the version in `VERSION`; `--version v0.1.0`
selects a specific published release. `--dir PATH` selects a different installation
directory. Nothing is added to `PATH` automatically.

The installer preserves your database and tokens. Back up private state before
upgrading. This experimental release promises neither downgrade compatibility nor
long-term database migration support.

To remove the executable, delete `bin/questlock` after stopping it. State under
`.questlock/` is separate and remains available until you deliberately remove it.

## For maintainers

`python3 scripts/package.py` builds all four targets into ignored `dist/`; repeated
`--target` options can select fewer. The archive builder copies an explicit file
allowlist, strips local filesystem paths from Go builds, and normalizes archive
ownership. It never packages `.local/`, tokens, databases, or the checkout itself.

CI runs each platform's packaged binary on a matching native runner. A `vX.Y.Z`
tag matching `VERSION` publishes an experimental GitHub release only after all
four platform jobs and the publication guard pass. Publishing uses a separate
job with `contents: write`; build/test jobs keep read-only permissions.
