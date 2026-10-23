# Owner maintenance checklist

## Local storage retention (v0.12.0)

Use the [operator guide](storage-retention.md) to review a complete policy,
inspect `storage info`, preview `storage archive`, and apply bounded batches.
Keep exact reads, retry receipts, scope isolation, journal history, pending and
uncertain state, and token/dollar reservations under regression coverage.
Missing, corrupt or unsafe cold files must stop reads without new generation.
Exercise interrupted archive/restore, restart, concurrent writers, capacity
failures, strict policy decoding and resource-pressure deferral with synthetic
state. Keep old releases intact and test downgrade preparation by restoring all
archived payloads first.

Check main-database bytes, WAL bytes, free pages, cold bytes and interrupted
temporary bytes separately. Archival creates reusable SQLite pages; an explicit
`storage compact` reclaims physical database space and needs additional free
disk. Neither scheduled maintenance nor restore deletes final cold files.
Retained metadata can fill the configured database page limit and stop writes.
Back up a consistent SQLite state and the entire private cold directory together.

Optional Langfuse storage spans contain bounded operation/outcome counts and
timing only. Validate real readback before claiming storage telemetry works
against a live service; existing inference telemetry evidence does not establish
the new storage contract. Report measured payload/cold/file sizes separately,
without a general compression or total-disk-savings claim.

## Go execution release gates (v0.10.0)

Keep admission warm-up, bounded snapshots, cancellation, close and telemetry
failure under regression coverage. Test shared input/output/total token quotas
with concurrent HTTPS clients, policy omission/conflicts, UTC rollover, unknown
usage, rollback and settlement failures. Preserve nil-policy task identities.
Report quota overhead, contention and the 1,000-entry/day and 32-CAS bounds;
scripted providers and isolated nodes do not establish GPU or physical-cluster
throughput. Qualify custom runtime interfaces explicitly. Test the published Go
module in a separate app without a local replacement before recommending it.

## Revision release gates (v0.6.0)

Keep exact source/hash drift, counted sequential edits, source/output check
failure withholding, publication uncertainty, restart, scope isolation,
concurrent exact reuse, immutable parent provenance and MCP path/busy/cancel
behavior under regression coverage. Require no model calls in every revision
scenario and retain failed-run accounting; no retry may silently generate text.

Check the installed shell helper's offline token boundary and live argument
routing. Validate complete embedded revision examples. Native archives and both
source/published containers must run `revise quest` as the fifth offline quest.
Use fresh `deterministic_edit` and exact-repeat `reuse_zero` records, with no
inherited human acceptance or guessed avoided tokens. All readers/writers of
revision outcome cohorts must be v0.6.0+; keep older generation cohorts separate
while rolling out. Preserve old binaries/tags. Skill installation does not
restart MCP or authorize an unguarded client configuration change.

Use the existing Go, Python, and GitHub Actions workflows. Record checks actually
run and their source commit; a successful build is not evidence of publication.

## Changes and dependencies

- Review the diff, tests, fixtures, and public prose; keep credentials, local state,
  private outputs, and personal paths out of source and release archives.
- For behavior changes, add focused regressions for admission, recovery, token
  bounds, billing categories, or quality requirements. Run `make check`,
  `make quest`, `./bin/questlock model quest`, `./bin/questlock outcome quest`,
  `./bin/questlock model runtime-quest`, and `./bin/questlock revise quest`.
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
  Verify each archive's checksum, version, help, recovery/model/outcome/runtime/revision quests,
  and embedded fixture evaluation; retain the tested archives rather than
  rebuilding them in the publication job.
- Require the source-container CI job too. It builds through `.dockerignore` and
  runs all eight quests and fixture evaluation without a network and with a
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
  writable data ownership, all eight quests, fixture evaluation, and OCI
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
- Follow [positioning](positioning.md): the OpenRouter adapter pins an explicit
  model/provider route and enforces declared reservation/native billing semantics;
  it does not proxy all generation or intercept hosted client sessions.
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

## Runtime integration evidence

For SGLang changes, check the pinned native protocol, exact token-ID handoff,
immutable deployment identity, cache namespaces, context bounds and unknown
metadata. Keep runtime KV hits separate from provider billing. HiSparse and
other PD endpoints remain inspection only until an ingress is implemented and
qualified. Protocol fixtures, a CPU predictor, native Apple inference and real
GPU measurements must retain distinct labels. Require matching quality, model,
configuration and cold/warm workload evidence before publishing an acceleration
claim. Keep GPU instructions reproducible even when local hardware cannot run
them; do not substitute simulated timing for measured latency.

For OpenRouter changes, preserve explicit route pins, capability policy, realistic
operator reservation bounds, unknown-usage stops and caller price provenance.
A paid live qualification is separate from fixture compatibility. Update the
[runtime guide](runtime-cache.md), paired diagram, source examples, public archive
allowlist and real Langfuse metadata readback whenever their contract changes.

## Keep the landing pages current

Describe capabilities on the repository and profile READMEs, without release
numbers or “new in” sections. Keep the existing narrative and permanent brand
art. Put what changed on the GitHub release page; generated notes are a starting
point for the release controller to edit with verified behavior and limits.
Versioned validation reports and compatibility requirements retain their exact
versions so the evidence stays auditable.
