# Contributing to Questlock

Questlock is an experimental broker for shared text-artifact publication on one
host. Start by running the [recovery quest](README.md#start-the-quest), then inspect
the [architecture](docs/architecture.md) and [security boundaries](SECURITY.md).

## Useful first contributions

- A reproducible bug with the version, OS/CPU, commands, expected result, and
  actual result. Use synthetic content and remove credentials from output.
- A small test for an uncovered concurrency or process-recovery failure.
- A runnable client example that handles conflicts and preserves the intended
  request for retry. The [Go example](examples/publish/main.go) is a starting point.
- Clearer installation instructions based on trying a supported platform.

Open an issue before substantial features or dependencies so we can agree on the
scope. Describe observed behavior separately from a proposed fix. Do not promise
sandboxing, task ownership, or multi-host guarantees that the broker does not offer.

## Work on a change

Fork the repository, clone your fork with its full history, and create a branch.
Use Go 1.26+, Python 3, and a C compiler for the race detector. Then run:

```sh
make check
make quest
```

Add a focused regression test for a behavior change. For documentation changes,
check the commands and links you changed. A pull request should explain the
problem, the resulting behavior, and the validation performed.

Only commit intended source changes. Keep local databases, tokens, environment
files, keys, and private examples out of Git. Report security issues according to
[SECURITY.md](SECURITY.md); do not publish an exploit with credentials or private
data in a public issue.

Contributions to Questlock's own code use its [MIT license](LICENSE). Preserve
third-party attribution. Dependency or toolchain changes also require reviewing
and regenerating [third-party notices](THIRD_PARTY_NOTICES.md).
