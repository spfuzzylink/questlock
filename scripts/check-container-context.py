#!/usr/bin/env python3
"""Inspect Docker's actual filtered context, including disposable private sentinels."""

import argparse
from contextlib import contextmanager
import os
from pathlib import Path, PurePosixPath
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parents[1]
TOP_LEVEL = {"go.mod", "go.sum", "LICENSE", "ATTRIBUTION.md", "THIRD_PARTY_NOTICES.md"}
GO_DIRECTORIES = {
    "cmd/questlock", "internal/store", "internal/httpapi", "internal/agentsetup", "internal/configguard", "internal/onboardingclient",
    "internal/localruntime", "internal/desktopbundle", "internal/nativeagent", "internal/teamquest", "team",
    "client", "protocol", "inference", "admission", "edge", "internal/edgequest", "systemone", "observability", "evaluation", "delegation", "worker", "outcomes", "revision", "retention", "writing",
}
EMBEDDED_FILES = {
    "systemone/bridge.py",
    "internal/desktopbundle/legalassets/LICENSE",
    "internal/desktopbundle/legalassets/ATTRIBUTION.md",
    "internal/desktopbundle/legalassets/THIRD_PARTY_NOTICES.md",
    "internal/agentsetup/assets/SKILL.md",
    "internal/agentsetup/assets/scripts/run.sh",
    "internal/agentsetup/assets/agents/openai.yaml",
    "internal/agentsetup/assets/references/workflow.md",
    "internal/agentsetup/assets/references/local-summary.json",
    "internal/agentsetup/assets/references/local-worker.json",
    "internal/agentsetup/assets/references/checked-revision.json",
    "internal/agentsetup/assets/references/openai-summary.json",
    "internal/agentsetup/assets/references/claude-summary.json",
    "evaluation/fixtures/quickstart.json",
}
REQUIRED_FILES = TOP_LEVEL | EMBEDDED_FILES | {
    "writing/writing.go", "writing/index.go", "cmd/questlock/writing.go",
    "retention/config.go", "retention/cold.go", "retention/cold_files_unix.go", "retention/cold_files_other.go",
    "internal/teamquest/quest.go", "team/engine.go", "internal/nativeagent/provider.go", "protocol/data_json.go",
    "internal/configguard/guard.go", "internal/onboardingclient/client.go",
    "internal/localruntime/runtime.go", "internal/desktopbundle/bundle.go",
    "edge/edge.go", "internal/edgequest/quest.go", "systemone/runner.go",
    "cmd/questlock/main.go", "internal/store/store.go", "internal/httpapi/httpapi.go",
    "internal/agentsetup/install.go", "client/client.go", "protocol/protocol.go", "inference/runner.go",
    "observability/langfuse.go", "evaluation/fixtures.go", "delegation/router.go", "worker/worker.go", "outcomes/store.go", "revision/types.go", "admission/scheduler.go",
}
PRIVATE_SENTINELS = (
    "writing/context-private.json", "writing/nested/context-private.go",
    "retention/context-private.gzip", "retention/context-private.json", "retention/nested/context-private.go",
    "internal/teamquest/context-private.token", "internal/teamquest/nested/context-private.go",
    "team/context-private-request.json", "team/nested/context-private.go",
    "internal/nativeagent/context-private.token", "internal/nativeagent/nested/context-private.go",
    "edge/context-private.db", "edge/nested/context-private.go",
    "systemone/context-private-request.json", "systemone/nested/context-private.go",
    "systemone/context-private.py", "internal/edgequest/context-private.db",
    ".local/context-private.txt", "cmd/.DS_Store", "cmd/questlock/.context-private.go",
    "internal/store/context-private.db", "internal/agentsetup/assets/context-private.json",
    "observability/context-private.env", "evaluation/context-private.json",
    "evaluation/fixtures/context-private.json", "evaluation/fixtures/nested/context-private.go",
    "evaluation/fixtures/nested/quickstart.json", "evaluation/nested/context-private.go",
    "delegation/context-private.env", "worker/context-private-request.json",
    "worker/nested/context-private.go", "scripts/langfuse/context-private.env",
    "revision/context-private-request.json", "revision/nested/context-private.go",
    "admission/context-private-request.json", "admission/nested/context-private.go",
    "outcomes/context-private-feedback.json", "outcomes/nested/context-private.go",
    "scripts/langfuse/context-private-request.json",
)
SENTINEL_BYTES = b"private build-context fixture never published\n"


class ContextError(Exception):
    pass


def allowed_file(name):
    path = PurePosixPath(name)
    if path.is_absolute() or any(part in (".", "..") or part.startswith(".") for part in path.parts):
        return False
    if name in TOP_LEVEL or name in EMBEDDED_FILES:
        return True
    return (str(path.parent) in GO_DIRECTORIES and path.name.endswith(".go")
            and not path.name.endswith("_test.go"))


def validate_context(root):
    """Check exported bytes instead of approximating .dockerignore's glob rules."""
    found = set()
    for directory, subdirectories, files in os.walk(root, followlinks=False):
        for name in subdirectories:
            if (Path(directory) / name).is_symlink():
                raise ContextError("filtered context includes a symlinked directory")
        for name in files:
            path = Path(directory) / name
            relative = path.relative_to(root).as_posix()
            if path.is_symlink() or not path.is_file() or not allowed_file(relative):
                raise ContextError("filtered context includes an unexpected build input")
            found.add(relative)
    if not REQUIRED_FILES.issubset(found):
        raise ContextError("filtered context is missing required source or embedded public files")
    return found


@contextmanager
def private_sentinels(root):
    created_files, created_directories = [], []
    try:
        for name in PRIVATE_SENTINELS:
            path = root / name
            if path.exists() or path.is_symlink():
                raise ContextError("a disposable context sentinel path already exists")
            if any(parent.is_symlink() for parent in path.parents if parent != root and root in parent.parents):
                raise ContextError("a sentinel parent must not be a symlink")
            missing = []
            parent = path.parent
            while parent != root and not parent.exists():
                missing.append(parent)
                parent = parent.parent
            if parent.is_symlink():
                raise ContextError("a sentinel parent must not be a symlink")
            for directory in reversed(missing):
                directory.mkdir()
                created_directories.append(directory)
            with path.open("xb") as stream:
                stream.write(SENTINEL_BYTES)
            created_files.append(path)
        yield
    finally:
        for path in reversed(created_files):
            # Preserve a concurrent repair instead of deleting someone else's bytes.
            if not path.is_symlink() and path.is_file() and path.read_bytes() == SENTINEL_BYTES:
                path.unlink()
        for directory in reversed(created_directories):
            try:
                directory.rmdir()
            except OSError:
                pass


def check_context(root, engine):
    with private_sentinels(root), tempfile.TemporaryDirectory(prefix="questlock-filtered-context-") as temp:
        output = Path(temp) / "exported"
        subprocess.run([
            engine, "build", "--network=none", "--file", "-", "--output", "type=local,dest=" + str(output), ".",
        ], cwd=root, input="FROM scratch\nCOPY . /\n", text=True, check=True)
        found = validate_context(output)
        print(f"Verified {len(found)} explicit build inputs; private and nested sentinels were excluded.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--engine", default="docker", help="Docker CLI with BuildKit local output support")
    args = parser.parse_args()
    try:
        check_context(ROOT, args.engine)
    except (ContextError, OSError, subprocess.CalledProcessError) as error:
        message = str(error) if isinstance(error, ContextError) else "cannot export the filtered build context"
        print("Container context validation failed: " + message, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
