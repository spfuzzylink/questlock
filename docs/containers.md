# Run the Questlock container

This guide is prepared for the **unpublished v0.3.0 candidate**. Its image commands
become available after release/container CI and publication are confirmed.
The preserved [v0.2.0 release](https://github.com/spfuzzylink/questlock/releases/tag/v0.2.0)
and `ghcr.io/spfuzzylink/questlock:v0.2.0` remain the historical published path.

The target experimental image in [GitHub Packages](https://github.com/spfuzzylink/questlock/pkgs/container/questlock)
is `ghcr.io/spfuzzylink/questlock:v0.3.0`, with native Linux ARM64 and x86-64 images.
Podman and Docker select the matching CPU image. On macOS, the container engine
runs Linux in its own VM; native macOS binaries use the separate release archives.

## Run the recovery quest

With Podman installed and running:

```sh
podman run --rm --network none --read-only \
  --cap-drop all --security-opt no-new-privileges \
  --memory 256m --cpus 1 --pids-limit 128 \
  --tmpfs /data:rw,nosuid,nodev,noexec,size=64m,mode=1777 \
  ghcr.io/spfuzzylink/questlock:v0.3.0 quest
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
  ghcr.io/spfuzzylink/questlock:v0.3.0 model quest
```

Look for six `PASS` lines and `QUEST CLEARED`. This quest starts a real local HTTP
broker with temporary SQLite state and uses a deterministic model fixture. It
needs no API key, model download, provider credits, or separate broker. The six
checks cover exact result reuse, bounded context, preflight cost rejection,
concurrent task admission, restart/revision behavior, and shared budgets with an
unknown provider outcome. Fixture token counts and dollar rates demonstrate
correctness; they are not measured provider savings. See the
[model architecture and scenario details](model-architecture.md).

## Run a persistent broker

The command above is a disposable demo. A persistent broker needs its own named
volume, explicitly provisioned agent credentials, and a loopback-only host port.
Follow the [rootless Podman deployment guide](deployment.md). Replace its initial
image-build command with:

```sh
podman pull ghcr.io/spfuzzylink/questlock:v0.3.0
podman tag ghcr.io/spfuzzylink/questlock:v0.3.0 localhost/questlock:dev
```

Then continue with the volume, credential provisioning, and Quadlet steps in that
guide. No other change to its security settings is required. The image's default
command starts a broker on port 8080 inside the container; it does not publish a
host port automatically. Keep any explicit host binding on `127.0.0.1`.

## Tags, updates, and provenance

- `v0.3.0` is the candidate release tag; `experimental` is a moving tag
  for subsequent experimental publications. Neither implies a stable API.
- Registry tags can change. For a repeatable deployment, record the image digest
  shown by `podman images --digests` and use the corresponding digest reference.
- The image uses the published release binary, rather than recompiling it. Its
  OCI labels identify the release and source commit. `/BUILDINFO.json`, `/LICENSE`,
  and `/THIRD_PARTY_NOTICES.md` accompany the binary.
- Native Linux ARM64 and x86-64 runners check the binary version, non-root user,
  writable data directory, seven recovery checks, and six offline model checks
  before publication. Both quests run from the image's packaged executable. See the
  [container workflow](https://github.com/spfuzzylink/questlock/actions/workflows/container.yml).

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
Actual-server/CI proof remains pending; see [setup and limits](inference-observability.md#validate-a-self-hosted-endpoint).
