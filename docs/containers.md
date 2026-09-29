# Run the published container

The experimental image is available from [GitHub Packages](https://github.com/spfuzzylink/questlock/pkgs/container/questlock)
as `ghcr.io/spfuzzylink/questlock:v0.1.1`, with native Linux ARM64 and x86-64 images.
Podman and Docker select the matching CPU image. On macOS, the container engine
runs Linux in its own VM; native macOS binaries remain available from [Releases](https://github.com/spfuzzylink/questlock/releases/tag/v0.1.1).

## Run the recovery quest

With Podman installed and running:

```sh
podman run --rm --network none --read-only \
  --cap-drop all --security-opt no-new-privileges \
  --memory 256m --cpus 1 --pids-limit 128 \
  --tmpfs /data:rw,nosuid,nodev,noexec,size=64m,mode=1777 \
  ghcr.io/spfuzzylink/questlock:v0.1.1 quest
```

The same command works with `docker` in place of `podman`. Pulling the public
image needs an internet connection but no registry login. The demo then runs
without container networking, host mounts, published ports, or a model account.
Its writable temporary state disappears when the container exits. Look for seven
`PASS` lines and `QUEST CLEARED`.

## Run a persistent broker

The command above is a disposable demo. A persistent broker needs its own named
volume, explicitly provisioned agent credentials, and a loopback-only host port.
Follow the [rootless Podman deployment guide](deployment.md). Replace its initial
image-build command with:

```sh
podman pull ghcr.io/spfuzzylink/questlock:v0.1.1
podman tag ghcr.io/spfuzzylink/questlock:v0.1.1 localhost/questlock:dev
```

Then continue with the volume, credential provisioning, and Quadlet steps in that
guide. No other change to its security settings is required. The image's default
command starts a broker on port 8080 inside the container; it does not publish a
host port automatically. Keep any explicit host binding on `127.0.0.1`.

## Tags, updates, and provenance

- `v0.1.1` identifies this experimental release; `experimental` is a moving tag
  for subsequent experimental publications. Neither implies a stable API.
- Registry tags can change. For a repeatable deployment, record the image digest
  shown by `podman images --digests` and use the corresponding digest reference.
- The image uses the published release binary, rather than recompiling it. Its
  OCI labels identify the release and source commit. `/BUILDINFO.json`, `/LICENSE`,
  and `/THIRD_PARTY_NOTICES.md` accompany the binary.
- Native Linux ARM64 and x86-64 runners check the binary version, non-root user,
  writable data directory, and full recovery quest before publication. See the
  [container workflow](https://github.com/spfuzzylink/questlock/actions/workflows/container.yml).

The broker runs as UID/GID 65532. Containers are an operating environment, not a
claim that Questlock sandboxes agents. Read the [security boundaries](../SECURITY.md).

## Publishing a later image

Repository maintainers can run **Actions → Experimental container → Run workflow**
with a published release tag such as `v0.1.1`. Successful CI for future version
tags also triggers the workflow. Only the final publication job has package-write
permission, using GitHub's built-in job token.

GitHub may make a newly created package private initially. Before advertising a
new package, set its visibility to public in GitHub Packages and verify pulling
with an empty registry authentication configuration. The workflow reports that
check; a successful upload alone does not establish anonymous access.
