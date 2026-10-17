# Run the Questlock container

## Checked revisions without a model

The published v0.7.0 image includes `revise quest`: three simple and three complex
offline deterministic edit scenarios with temporary broker state. It needs no
model account. Use the network-disabled/read-only command below, replacing its
final `quest` arguments with `revise quest`. The [published artifact evidence](validation.md#v070-observations--2026-10-17)
includes both native architectures and anonymous registry verification.

A persistent revision worker talks to the broker through scoped HTTP. Give only
the broker its SQLite volume. No GPU, model, provider secret or KV storage is
needed for literal revisions. Upgrade all readers/writers before sharing new
revision outcome cohorts; older versions do not understand `deterministic_edit`.
See [checked revisions](checked-revisions.md).

This guide covers the experimental **v0.7.0** image. The preserved
[v0.6.0](https://github.com/spfuzzylink/questlock/releases/tag/v0.6.0),
[v0.5.0](https://github.com/spfuzzylink/questlock/releases/tag/v0.5.0),
[v0.4.0](https://github.com/spfuzzylink/questlock/releases/tag/v0.4.0),
[v0.3.0](https://github.com/spfuzzylink/questlock/releases/tag/v0.3.0) and
[v0.2.0](https://github.com/spfuzzylink/questlock/releases/tag/v0.2.0) releases and
their corresponding container tags remain available.

The current experimental image in [GitHub Packages](https://github.com/spfuzzylink/questlock/pkgs/container/questlock)
is `ghcr.io/spfuzzylink/questlock:v0.7.0`, with native Linux ARM64 and x86-64 images.
Podman and Docker select the matching CPU image. On macOS, the container engine
runs Linux in its own VM; native macOS binaries use the separate release archives.

## Run the recovery quest

With Podman installed and running:

```sh
podman run --rm --network none --read-only \
  --cap-drop all --security-opt no-new-privileges \
  --memory 256m --cpus 1 --pids-limit 128 \
  --tmpfs /data:rw,nosuid,nodev,noexec,size=64m,mode=1777 \
  ghcr.io/spfuzzylink/questlock:v0.7.0 quest
```

The same command works with `docker` in place of `podman`. Pulling the public
image needs an internet connection but no registry login. The demo then runs
without container networking, host mounts, published ports, or a model account.
Its writable temporary state disappears when the container exits. Look for seven
`PASS` lines and `QUEST CLEARED`.

## Run the offline model quest

Use the same isolated container settings to exercise bounded model workflows:

```sh
podman run --rm --network none --read-only \
  --cap-drop all --security-opt no-new-privileges \
  --memory 256m --cpus 1 --pids-limit 128 \
  --tmpfs /data:rw,nosuid,nodev,noexec,size=64m,mode=1777 \
  ghcr.io/spfuzzylink/questlock:v0.7.0 model quest
```

Look for six `PASS` lines and `QUEST CLEARED`. This quest starts a real local HTTP
broker with temporary SQLite state and uses a deterministic model fixture. It
needs no API key, model download, provider credits, or separate broker. The six
checks cover exact result reuse, bounded context, preflight cost rejection,
concurrent task admission, restart/revision behavior, and shared budgets with an
unknown provider outcome. Fixture token counts and dollar rates demonstrate
correctness; they are not measured provider savings. See the
[model architecture and scenario details](model-architecture.md).

## Run the offline outcome quest

Exercise explicit acceptance and rework accounting with the same isolated settings:

```sh
podman run --rm --network none --read-only \
  --cap-drop all --security-opt no-new-privileges \
  --memory 256m --cpus 1 --pids-limit 128 \
  --tmpfs /data:rw,nosuid,nodev,noexec,size=64m,mode=1777 \
  ghcr.io/spfuzzylink/questlock:v0.7.0 outcome quest
```

Look for six `PASS` lines and `QUEST CLEARED`. Three simple and three complex
scenarios use real scoped HTTP and temporary SQLite to check acceptance/rewrite
transitions, exact run/event receipts, inclusive cohort accounting, missing
cost/effort/rate, stale feedback, and broker restart. No model runs or real human
review decisions are involved; costs and reviewer minutes are synthetic. Read
[human feedback and accounting](outcomes.md) before interpreting the metric.

## Run the offline runtime quest

Use the same isolated container settings with `model runtime-quest`. Six native
HTTP-fixture scenarios test token-ID handoff, result reuse, namespaces, cache tier
evidence, runtime mutation, and unsupported sparse decode. They run on Linux
ARM64 or AMD64 without a GPU. These are protocol checks, not CUDA emulation or
GPU benchmarks. The Questlock image contains the broker/worker binary; SGLang
and its GPU runtime run separately. See [the runtime guide](runtime-cache.md).

## Run the offline revision quest

```sh
podman run --rm --network none --read-only \
  --cap-drop all --security-opt no-new-privileges \
  --memory 256m --cpus 1 --pids-limit 128 \
  --tmpfs /data:rw,nosuid,nodev,noexec,size=64m,mode=1777 \
  ghcr.io/spfuzzylink/questlock:v0.7.0 revise quest
```

Three simple cases cover exact product/sentence emphasis and a checked JSON
literal change. Three complex cases cover parent provenance and restart,
source/count/check failure withholding, and concurrent exact reuse with scope
isolation. No model runs; the application receives checked text and decides
where to apply it. The container grants no host-file editing capability.

## Run a persistent broker

The command above is a disposable demo. A persistent broker needs its own named
volume, explicitly provisioned agent credentials, and a loopback-only host port.
Follow the [rootless Podman deployment guide](deployment.md). Replace its initial
image-build command with:

```sh
podman pull ghcr.io/spfuzzylink/questlock:v0.7.0
podman tag ghcr.io/spfuzzylink/questlock:v0.7.0 localhost/questlock:dev
```

Then continue with the volume, credential provisioning, and Quadlet steps in that
guide. No other change to its security settings is required. The image's default
command starts a broker on port 8080 inside the container; it does not publish a
host port automatically. Keep any explicit host binding on `127.0.0.1`.

## Tags, updates, and provenance

- `v0.7.0` is the release tag; `experimental` is a moving tag
  for subsequent experimental publications. Neither implies a stable API.
- Registry tags can change. For a repeatable deployment, record the image digest
  shown by `podman images --digests` and use the corresponding digest reference.
- The image uses the published release binary, rather than recompiling it. Its
  OCI labels identify the release and source commit. `/BUILDINFO.json`, `/LICENSE`,
  and `/THIRD_PARTY_NOTICES.md` accompany the binary.
- Native Linux ARM64 and x86-64 runners check the binary version, non-root user,
  writable data directory, seven recovery checks, six offline model checks, six
  offline outcome checks, six runtime protocol checks, six revision checks, and
  the embedded fixture evaluation before publication.
  All five quests run from the image's packaged executable. See the
  [container workflow](https://github.com/spfuzzylink/questlock/actions/workflows/container.yml).

See the [v0.7.0 validation record](validation.md#v070-observations--2026-10-17)
for the tagged source, native publication checks, and immutable registry digests.

The broker runs as UID/GID 65532. Containers are an operating environment, not a
claim that Questlock sandboxes agents. Read the [security boundaries](../SECURITY.md).

## Publishing a later image

Repository maintainers can run **Actions → Experimental container → Run workflow**
with a confirmed published release tag. Successful CI for future version
tags also triggers the workflow. Only the final publication job has package-write
permission, using GitHub's built-in job token.

GitHub may make a newly created package private initially. Before advertising a
new package, set its visibility to public in GitHub Packages and verify pulling
with an empty registry authentication configuration. The workflow reports that
check; a successful upload alone does not establish anonymous access.

## Model runtime and observability

The broker container owns SQLite state. Run a local model worker on the same
host as its installed Ollama service and reach the broker through authenticated
HTTP; `127.0.0.1` inside a container refers to that container. The broker image
does not bundle model weights or silently switch a local request to a cloud API.
See [local models](local-models.md) and [delegation](delegation.md).

The optional disposable Langfuse stack is separate from this small broker image.
It has six content-pinned services, needs substantial local resources, and is
validated through an isolated wrapper. It needs no hosted Langfuse account.
The actual-server CI readback contract passed; see [setup and limits](inference-observability.md#validate-a-self-hosted-endpoint).

## Version 0.8 source candidate

The source candidate adds `edge quest` and `resources quest`, both offline with
temporary state and no model. Native publication checks run them with the same
network-disabled, read-only settings above. The optional TypeSafe decision bridge
requires a separately prepared Python environment and installed Ollama weights;
the small broker image contains neither Python nor model weights. Follow the
[local decision guide](system-one.md) on the host. Resource fixtures are not
GPU emulation or a multi-host performance benchmark. Current verified published
image commands above retain v0.7.0 until v0.8 publication is verified.
