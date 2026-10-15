# Owner maintenance checklist

Use the existing Go, Python, and GitHub Actions workflows. Record checks actually
run and their source commit; a successful build is not evidence of publication.

## Changes and dependencies

- Review the diff, tests, fixtures, and public prose; keep credentials, local state,
  private outputs, and personal paths out of source and release archives.
- For behavior changes, add focused regressions for admission, recovery, token
  bounds, billing categories, or quality requirements. Run `make check`,
  `make quest`, `./bin/questlock model quest`, and `./bin/questlock outcome quest`.
  Feedback changes also need checks for explicit acceptance, every rewrite
  clearing it, immutable missing effort, scoped retries/conflicts, failed work in
  cohort costs, and unknown accounting staying unknown.
- Review `go.mod`, `go.sum`, toolchain changes, and dependency security advisories.
  Regenerate notices with `python3 scripts/generate-notices.py`, then run its
  `--check` mode. Preserve [third-party attribution](../THIRD_PARTY_NOTICES.md).
- Use the private vulnerability route in [SECURITY.md](../SECURITY.md). If absent,
  request a private contact without publishing exploit details or sensitive data.

## Releases and containers

- Release a reviewed, clean commit with a new tag matching `v` plus `VERSION`.
  Preserve old tags, release assets, and working published versions; never force
  rewrite a published tag or replace old binaries to conceal a correction.
- Require all four native CI targets: Darwin and Linux, each on AMD64 and ARM64.
  Verify each archive's checksum, version, help, recovery/model/outcome quests,
  and embedded fixture evaluation; retain the tested archives rather than
  rebuilding them in the publication job.
- Require the source-container CI job too. It builds through `.dockerignore` and
  runs all three quests and fixture evaluation without a network and with a
  read-only root filesystem. Keep Go source and embedded skill assets in the
  build context as the project evolves.
- Require the reusable self-hosted Langfuse CI job for the release source. Its
  disposable v4.59.0 stack needs Docker/Compose and enough memory/CPU for six
  services. Use `scripts/langfuse/run.py` for config, startup, smoke reads, service
  state, and cleanup so ambient credentials, proxies, contexts, or service URLs
  cannot redirect the check. An OTLP acknowledgement alone does not prove the
  observed spans, exclusive usage buckets, or scores were stored correctly.
- Check `BUILDINFO.json` for the exact commit, version, target, and clean source,
  and compare archive license/notices against that released source. Use the
  explicit `scripts/package.py` allowlist; inspect newly included documents.
- Resolve container inputs to that published tag and exact source commit through
  `scripts/container_release.py`; reject checksum or provenance mismatches.
  Require native Linux AMD64 and ARM64 image checks: version, UID/GID 65532,
  writable data ownership, all three quests, fixture evaluation, and OCI
  source/revision/license labels. Require a manifest containing exactly those
  two tested architectures.
- Record immutable image digests. Verify anonymous pulling after publication
  before advertising access; an upload or authenticated pull is insufficient:

  ```sh
  anonymous_config="$(mktemp -d)"
  docker --config "$anonymous_config" pull "ghcr.io/spfuzzylink/questlock:v$(cat VERSION)"
  rm -rf "$anonymous_config"
  ```

## Documentation and launch copy

- Keep [architecture](model-architecture.md) prose, editable `.drawio` sources,
  exported diagrams, and packaged document links consistent with the code.
  Reopen the source and inspect each export after changes; ship matching files.
- Keep creator credit and [attribution guidance](../ATTRIBUTION.md) in future
  archives. Follow [brand guidance](../BRANDING.md); requested visible credit is
  optional, and neither document changes the MIT license.
- Report observed calls, token categories, latency, output quality, and actual
  charges with the provider/model and snapshot. Label fixture results as fixtures.
  Keep failed cases visible: the [v0.3.0 observations](measurements-v0.3.0.md)
  qualified neither configuration. Reuse is evidence of avoided generation,
  not evidence that the reused answer passed its task checks. Keep candidate
  wording until tags, archives, CI, and publication are confirmed.
- Follow [positioning](positioning.md): OpenRouter needs a future capability-aware
  adapter; compatible generation alone does not supply counting or usage semantics.
  Questlock cannot bypass credits, rate limits, or hosted assistant allowances.
  Local inference has hardware costs. Promise neither infinite usage nor novelty.
- Keep [runtime observability](inference-observability.md) and [delegation](delegation.md)
  descriptions aligned with the implementation: streaming time is client-observed,
  current metrics are not durable identity, MCP telemetry is silent best effort,
  and only a known complete durable quality failure permits the explicit next tier.
  Validated local jobs skip API USD accounting without consuming or clearing it.
- Keep [human outcome feedback](outcomes.md) distinct from declared-check scores.
  A reviewer must explicitly accept or reaccept the final artifact after a rewrite;
  a scope token does not establish that person's identity. Keep the explicit
  cohort, its ID/revision counting boundary, missing-accounting coverage, and
  exclusions visible in public cost claims. Immutable unknown effort cannot be
  retrospectively filled in through this release's API. Full ledger snapshots
  accumulate in history despite per-ledger size/event limits.
- Update current download/container references, matching project/profile banners,
  and editable diagrams only after actual publication and anonymous registry
  verification. Preserve historical release evidence and distinguish immutable
  archive documentation from later web-documentation corrections.

## Client configuration rollout

- Prefer explicit session flags and the [local model workflow](local-models.md).
  Before changing active Codex/Claude settings, save a private complete known-good
  backup and validate the complete staged candidate in an isolated home; preserve
  existing command arrays such as `notify`, then check syntax and native CLI reads.
- Install a detached rollback watchdog independent of the active client before
  swapping files. Record the candidate hash; rollback only if the live file still
  matches it, preserving concurrent human repairs. Test each changed setting and
  native local connection/status before committing the rollout; on failure or
  timeout restore the matching known-good backup through that watchdog.
