# Contributing to Questlock

Questlock is an experimental Go library and CLI for bounded model workflows and
shared text-artifact publication on one host. Start with the
[recovery quest](README.md#start-the-quest), then inspect the
[broker architecture](docs/architecture.md),
[model architecture](docs/model-architecture.md), and
[security boundaries](SECURITY.md).

## Useful first contributions

- A reproducible bug with the version, OS/CPU, commands, expected result, and
  actual result. Use synthetic content and remove credentials from output.
- A small test for an uncovered concurrency or process-recovery failure.
- A model-adapter regression covering count preflight, output limits, cache usage,
  incomplete results, or an unknown outcome without an automatic paid retry.
- A budget regression covering concurrent reservations, UTC-day boundaries,
  underestimated counts, cap violations, or settlement after publication failure.
- A synthetic quality case with explicit facts and completion criteria. Check that
  selected context preserves required facts and that reuse stays within a valid
  snapshot; shorter output alone does not establish useful savings.
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
./bin/questlock model quest
```

The model quest runs six deterministic offline scenarios with the real broker;
it needs no provider key or model account. A new adapter must define its counting,
generation, usage, and endpoint capabilities rather than assuming that an
OpenAI-compatible completion route is sufficient. Keep live provider checks
explicit, bounded, and separate from these fixtures.

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

For releases, container publication, documentation upkeep, and client rollout,
follow the [owner maintenance checklist](docs/maintenance.md).
