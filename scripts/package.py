#!/usr/bin/env python3
"""Build portable release archives from an explicit public-file allowlist."""

import argparse
import gzip
import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
import os
import sys
import stat
import zipfile


ROOT = Path(__file__).resolve().parents[1]
TARGETS = ("darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64")
PUBLIC_FILES = (
    "docs/semantic-shadow.md", "docs/semantic-shadow-validation-v0.14.0.json",
    "docs/diagrams/semantic-shadow.drawio", "docs/diagrams/semantic-shadow.svg",
    "examples/semantic/README.md", "examples/semantic/support-faq.request.json",
    "examples/semantic/support-faq.paraphrase.request.json", "examples/semantic/support-faq.changed-evidence.request.json",
    "docs/writing-reuse-success-report.md",
    "docs/benchmarks/writing-reuse/observation.json", "docs/benchmarks/writing-reuse/request.json",
    "docs/benchmarks/writing-reuse/answer.txt", "docs/benchmarks/writing-reuse/verify.py",
    "docs/writing-workflows.md", "examples/writing/README.md",
    "examples/writing/reference.json", "examples/writing/evidence.json", "examples/writing/changed-evidence.json",
    "docs/diagrams/writing-workflows.drawio", "docs/diagrams/writing-workflows.svg",
    "docs/storage-retention.md", "examples/storage/retention.json",
    "docs/storage-validation-v0.12.0.md",
    "docs/diagrams/storage-retention.drawio", "docs/diagrams/storage-retention.svg",
    "docs/plugins.md", "docs/plugin-validation-v0.11.0.md",
    "docs/plugin-validation-v0.11.0.json", "docs/diagrams/plugins.drawio", "docs/diagrams/plugins.svg",
    "docs/execution-validation-v0.10.0.md", "docs/execution-validation-v0.10.0.json",
    "docs/go-integration.md", "examples/execution/README.md",
    "examples/execution/main.go", "examples/execution/local-task.json",
    "examples/execution/sglang-task.json", "examples/execution/hosted-budget.example.json",
    "examples/execution/admission.json",
    "docs/local-agent-teams.md", "docs/diagrams/local-agent-teams.drawio",
    "docs/diagrams/local-agent-teams.svg", "examples/teams/README.md",
    "docs/local-team-validation-v0.9.0.md", "docs/local-team-validation-v0.9.0.json",
    "docs/local-team-validation-v0.9.1.md", "docs/local-team-validation-v0.9.1.json",
    "examples/teams/qualified-suite-v0.9.1.json",
    "examples/teams/independent-validation-v0.9.1.json",
    "examples/teams/local-uncertain.request.json", "examples/teams/reasoning-resources.json",
    "examples/teams/codex-baseline-v0.9.0/ticket.json",
    "examples/teams/codex-baseline-v0.9.0/route.json",
    "examples/teams/codex-baseline-v0.9.0/queue.json",
    "examples/teams/codex-baseline-v0.9.0/uncertain.json",
    "examples/teams/codex-baseline-v0.9.0/resources.json",
    "examples/teams/codex-baseline-v0.9.0/rework.json",
    "examples/teams/local-observations-v0.9.1/ticket.json",
    "examples/teams/local-observations-v0.9.1/route.json",
    "examples/teams/local-observations-v0.9.1/queue.json",
    "examples/teams/local-observations-v0.9.1/uncertain.json",
    "examples/teams/local-observations-v0.9.1/resources.json",
    "examples/teams/local-observations-v0.9.1/rework.json",
    "assets/questlock-brand.svg", "docs/onboarding.md", "docs/success-report-v0.7.0.md",
    "docs/diagrams/onboarding.drawio", "docs/diagrams/onboarding.svg",
    "docs/second-gate.md", "docs/diagrams/second-gate.drawio", "docs/diagrams/second-gate.svg",
    "examples/edge/README.md",
    "docs/adaptive-admission.md",
    "docs/system-one.md",
    "docs/diagrams/system-one.drawio",
    "docs/diagrams/system-one.svg",
    "optional/system-one/requirements.txt",
    "optional/system-one/TypeSafe-LICENSE",
    "optional/system-one/TypeSafe-SDK-LICENSE",
    "optional/system-one/reuse.json",
    "optional/system-one/route.json",
    "optional/system-one/approved-local-task.json",
    "docs/local-coordination.md",
    "docs/diagrams/local-coordination.drawio",
    "docs/diagrams/local-coordination.svg",
    "examples/preferences/README.md",
    "examples/preferences/local-only.request.json",
    "examples/preferences/personal-instructions.example.md",
    "LICENSE", "ATTRIBUTION.md", "BRANDING.md", "THIRD_PARTY_NOTICES.md",
    "README.md", "CONTRIBUTING.md", "SECURITY.md", "VERSION",
    "assets/questlock-banner.svg", "assets/questlock-evolution.svg", "assets/questlock-v0.3.0.svg", "assets/questlock-v0.4.0.svg", "docs/release-notes.md",
    "docs/installation.md", "docs/containers.md", "docs/use-cases.md", "docs/api.md",
    "docs/architecture.md", "docs/deployment.md", "docs/validation.md", "docs/maintenance.md",
    "docs/model-workflows.md", "docs/model-architecture.md", "docs/positioning.md", "docs/local-models.md",
    "docs/inference-observability.md", "docs/delegation.md", "docs/measurements-v0.3.0.md",
    "docs/outcomes.md",
    "assets/questlock-v0.6.0.svg", "docs/checked-revisions.md",
    "docs/diagrams/checked-revisions.drawio", "docs/diagrams/checked-revisions.svg",
    "examples/revisions/README.md", "examples/revisions/bold-product.json",
    "examples/revisions/bold-mission.json", "examples/revisions/json-status.json",
    "examples/revisions/comparison.json",
    "assets/questlock-v0.5.0.svg", "docs/runtime-cache.md",
    "docs/diagrams/runtime-layers.drawio", "docs/diagrams/runtime-layers.svg",
    "deploy/sglang/README.md", "deploy/sglang/dense.json", "deploy/sglang/hicache.json",
    "deploy/sglang/hisparse.json", "deploy/sglang/hisparse-prefill.json", "deploy/sglang/task.json",
    "examples/inference/openrouter-summary.json",
    "docs/diagrams/human-outcomes.drawio", "docs/diagrams/human-outcomes.svg",
    "docs/diagrams/model-workflows.drawio", "docs/diagrams/model-system.svg", "docs/diagrams/model-lifecycle.svg",
    "docs/diagrams/inference-runtime.drawio", "docs/diagrams/inference-runtime.svg",
    "evaluation/fixtures/quickstart.json",
    "examples/model-workflows/openai-handoff.json", "examples/model-workflows/claude-handoff.json",
    "examples/model-workflows/local-handoff.json",
    "examples/outcomes/README.md", "examples/outcomes/completed-run.json",
    "examples/outcomes/failed-run.json", "examples/outcomes/reuse-run.json",
    "examples/outcomes/unknown-run.json", "examples/outcomes/accept.json",
    "examples/outcomes/rewrite.json", "examples/outcomes/reaccept.json",
    "examples/outcomes/reject.json", "examples/outcomes/unknown-effort.json",
    "examples/outcomes/cohort.json",
)

PLUGIN_FILES = (
    ".agents/plugins/marketplace.json", ".claude-plugin/marketplace.json",
    "plugins/openai/plugin.json", "plugins/openai/mcp.json",
    "plugins/claude/.claude-plugin/plugin.json", "plugins/claude/.mcp.json",
) + tuple(
    f"plugins/{client}/{name}"
    for client in ("openai", "claude")
    for name in ("README.md", "VERSION", "skills/questlock/SKILL.md",
                 "scripts/run.sh", "scripts/install.sh", "assets/questlock-icon.svg",
                 "examples/checked-json.json", "examples/local-extraction.json",
                 "LICENSE", "ATTRIBUTION.md", "THIRD_PARTY_NOTICES.md")
)


def public_file(root, name):
    path = root
    for part in Path(name).parts:
        path = path / part
        if path.is_symlink():
            raise ValueError("release inputs must not be symlinks")
    if not path.is_file():
        raise ValueError("a required release document is missing")
    return path.read_bytes()


def archive(output, binary, documents, metadata):
    """Normalize archive metadata so local usernames/paths never enter packages."""
    members = {"questlock": binary, **documents,
               "BUILDINFO.json": (json.dumps(metadata, sort_keys=True, indent=2) + "\n").encode()}
    with output.open("wb") as raw:
        with gzip.GzipFile(fileobj=raw, mode="wb", filename="", mtime=0) as compressed:
            with tarfile.open(fileobj=compressed, mode="w", format=tarfile.USTAR_FORMAT) as bundle:
                for name, data in members.items():
                    info = tarfile.TarInfo(name)
                    info.size = len(data)
                    info.mode = 0o755 if name == "questlock" else 0o644
                    info.uid = info.gid = info.mtime = 0
                    info.uname = info.gname = ""
                    bundle.addfile(info, io.BytesIO(data))


def plugin_archive(output, binary, files, metadata):
    """One offline catalog for both clients, with no host installation state."""
    members = dict(files)
    for client in ("openai", "claude"):
        members[f"plugins/{client}/bin/questlock"] = binary
        for name in ("LICENSE", "ATTRIBUTION.md", "THIRD_PARTY_NOTICES.md"):
            members[f"plugins/{client}/{name}"] = files[name]
    members["BUILDINFO.json"] = (json.dumps(metadata, sort_keys=True, indent=2) + "\n").encode()
    with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as bundle:
        for name, data in sorted(members.items()):
            entry = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
            mode = 0o755 if name.endswith(("/bin/questlock", ".sh")) else 0o644
            entry.create_system = 3
            entry.external_attr = (stat.S_IFREG | mode) << 16
            entry.compress_type = zipfile.ZIP_DEFLATED
            bundle.writestr(entry, data)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", action="append", choices=TARGETS,
                        help="build only this target; repeat to select several (default: all four)")
    parser.add_argument("--output", type=Path, default=ROOT / "dist")
    args = parser.parse_args()
    version = (ROOT / "VERSION").read_text().strip()
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", version):
        parser.error("VERSION must contain a plain semantic version such as 0.1.0")
    # A dependency/toolchain change must not silently ship stale legal notices.
    subprocess.run([sys.executable, str(ROOT / "scripts/generate-notices.py"), "--check"], cwd=ROOT, check=True)
    documents = {name: public_file(ROOT, name) for name in PUBLIC_FILES}
    plugin_files = {**documents, **{name: public_file(ROOT, name) for name in PLUGIN_FILES}}
    commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    dirty = subprocess.run(["git", "diff", "--quiet", "HEAD"], cwd=ROOT).returncode != 0
    go_version = subprocess.check_output(["go", "env", "GOVERSION"], cwd=ROOT, text=True).strip()
    args.output.mkdir(parents=True, exist_ok=True)
    checksums = []
    for target in dict.fromkeys(args.target or TARGETS):
        goos, goarch = target.split("/")
        with tempfile.TemporaryDirectory(prefix="questlock-package-") as temp:
            binary = Path(temp) / "questlock"
            env = dict(os.environ, CGO_ENABLED="0", GOOS=goos, GOARCH=goarch,
                       GOAMD64="v1", GOARM64="v8.0", GOFLAGS="", GOWORK="off", GOEXPERIMENT="")
            subprocess.run([
                "go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false",
                "-ldflags", "-s -w -X main.version=" + version,
                "-o", str(binary), "./cmd/questlock",
            ], cwd=ROOT, env=env, check=True)
            output = args.output / f"questlock_{version}_{goos}_{goarch}.tar.gz"
            metadata = {
                "version": version, "commit": commit, "source_dirty": dirty,
                "go_version": go_version, "os": goos, "arch": goarch,
                "cgo_enabled": False,
            }
            archive(output, binary.read_bytes(), documents, metadata)
            digest = hashlib.sha256(output.read_bytes()).hexdigest()
            checksums.append(f"{digest}  {output.name}\n")
            print(output.name, flush=True)
            plugin_output = args.output / f"questlock-plugin_{version}_{goos}_{goarch}.zip"
            plugin_archive(plugin_output, binary.read_bytes(), plugin_files, metadata)
            digest = hashlib.sha256(plugin_output.read_bytes()).hexdigest()
            checksums.append(f"{digest}  {plugin_output.name}\n")
            print(plugin_output.name, flush=True)
    (args.output / "checksums.txt").write_text("".join(checksums))


if __name__ == "__main__":
    main()
