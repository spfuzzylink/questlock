#!/usr/bin/env python3
"""Run disposable Langfuse checks with isolated credentials and a local daemon.

No ambient provider, Langfuse, database, proxy, Docker context, or Go environment
is forwarded. Every operation uses the same generated private environment.
"""

import argparse
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tempfile
from urllib.parse import urlsplit


ROOT = Path(__file__).resolve().parents[2]
KEYS = frozenset({
    "POSTGRES_PASSWORD", "CLICKHOUSE_PASSWORD", "REDIS_AUTH",
    "MINIO_ROOT_PASSWORD", "SALT", "ENCRYPTION_KEY", "NEXTAUTH_SECRET",
    "MINIO_ROOT_USER", "LANGFUSE_PUBLIC_KEY", "LANGFUSE_SECRET_KEY",
    "LANGFUSE_INIT_ORG_ID", "LANGFUSE_INIT_PROJECT_ID", "LANGFUSE_BASE_URL",
    "DATABASE_URL",
})
OPERATIONS = {
    "config": ("config", "--quiet"),
    "up": ("up", "--detach", "--wait", "--wait-timeout", "600"),
    "ps": ("ps", "--all"),
    "down": ("down", "--volumes", "--remove-orphans", "--timeout", "20"),
}


class IsolationError(Exception):
    """Messages are fixed text and never include private source values."""


def read_environment(path):
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    try:
        before = os.lstat(path)
        fd = os.open(path, flags)
        with os.fdopen(fd, "r", encoding="utf-8") as source:
            info = os.fstat(source.fileno())
            if (not stat.S_ISREG(info.st_mode)
                    or stat.S_IMODE(info.st_mode) != 0o600
                    or info.st_uid != os.getuid()
                    or (before.st_dev, before.st_ino) != (info.st_dev, info.st_ino)
                    or stat.S_ISLNK(before.st_mode) or info.st_size > 16384):
                raise IsolationError("environment must be a private owned regular file with mode 0600")
            data = source.read(16385)
    except (OSError, UnicodeError) as exc:
        raise IsolationError("private environment could not be read safely") from exc
    values = {}
    for line in data.splitlines():
        if "=" not in line:
            raise IsolationError("invalid disposable environment schema")
        name, value = line.split("=", 1)
        if name in values:
            raise IsolationError("duplicate disposable environment field")
        values[name] = value
    if values.keys() != KEYS:
        raise IsolationError("unexpected or missing disposable environment field")
    for name in (
        "POSTGRES_PASSWORD", "CLICKHOUSE_PASSWORD", "REDIS_AUTH",
        "SALT", "ENCRYPTION_KEY", "NEXTAUTH_SECRET",
    ):
        if not re.fullmatch(r"[a-f0-9]{64}", values[name]):
            raise IsolationError("invalid disposable generated credential")
    patterns = {
        "MINIO_ROOT_USER": r"ql[a-f0-9]{18}",
        "MINIO_ROOT_PASSWORD": r"[a-f0-9]{40}",
        "LANGFUSE_PUBLIC_KEY": r"pk-lf-[a-f0-9]{32}",
        "LANGFUSE_SECRET_KEY": r"sk-lf-[a-f0-9]{64}",
        "LANGFUSE_INIT_ORG_ID": r"questlock-[a-f0-9]{24}",
        "LANGFUSE_INIT_PROJECT_ID": r"questlock-[a-f0-9]{24}",
    }
    for name, pattern in patterns.items():
        if not re.fullmatch(pattern, values[name]):
            raise IsolationError("invalid disposable generated identifier")
    expected_database = (
        "postgresql://questlock:" + values["POSTGRES_PASSWORD"]
        + "@postgres:5432/questlock"
    )
    if (values["DATABASE_URL"] != expected_database
            or values["LANGFUSE_BASE_URL"] != "http://127.0.0.1:3000"):
        raise IsolationError("disposable service targets must remain local and fixed")
    return values


def local_socket(value):
    u = urlsplit(value)
    if (u.scheme != "unix" or u.netloc or u.query or u.fragment
            or not u.path.startswith("/") or "%" in u.path
            or any(part in {".", ".."} for part in u.path.split("/"))
            or any(ord(char) < 33 for char in value)):
        raise IsolationError("Docker requires an explicit absolute local Unix socket")
    try:
        if not stat.S_ISSOCK(os.stat(u.path).st_mode):
            raise IsolationError("Docker target must be a local Unix socket")
    except OSError as exc:
        raise IsolationError("local Docker socket is unavailable") from exc
    return value


def isolated_environment(values, operation, private_root, executable, socket):
    # Only executable discovery uses the current PATH. The child receives that
    # executable's directory and the platform's standard executable directories.
    env = {
        "PATH": str(Path(executable).parent) + os.pathsep + os.defpath,
        "HOME": str(private_root),
        "TMPDIR": str(private_root),
        "LANG": "C.UTF-8",
        "LC_ALL": "C.UTF-8",
    }
    if operation == "smoke":
        env.update({name: values[name] for name in (
            "LANGFUSE_BASE_URL", "LANGFUSE_PUBLIC_KEY", "LANGFUSE_SECRET_KEY",
        )})
        env.update({
            "GOENV": "off", "GOTOOLCHAIN": "local",
            "GOPROXY": "https://proxy.golang.org", "GOSUMDB": "sum.golang.org",
        })
    else:
        env.update(values)
        env.update({
            "DOCKER_CONFIG": str(private_root / "docker"),
            "DOCKER_HOST": socket,
            "COMPOSE_PROJECT_NAME": values["LANGFUSE_INIT_PROJECT_ID"],
        })
    return env


def execute(path, operation, socket="unix:///var/run/docker.sock"):
    values = read_environment(path)
    command = "go" if operation == "smoke" else "docker"
    executable = shutil.which(command)
    if not executable:
        raise IsolationError("required executable is unavailable on PATH")
    if operation != "smoke":
        socket = local_socket(socket)
    with tempfile.TemporaryDirectory(prefix="questlock-langfuse-isolated-") as temp:
        private_root = Path(temp)
        private_root.chmod(0o700)
        (private_root / "docker").mkdir(mode=0o700)
        env = isolated_environment(values, operation, private_root, executable, socket)
        if operation == "smoke":
            argv = [executable, "run", "./scripts/langfuse-smoke"]
            timeout = 420
        else:
            # Fixed subcommands prevent accidental unredacted `compose config`
            # or forwarding --context/--host/--env-file flags from caller input.
            argv = [executable, "--host", socket, "compose", "--env-file",
                    os.path.abspath(path), "-f", str(ROOT / "scripts/langfuse/compose.yml"),
                    *OPERATIONS[operation]]
            timeout = 720 if operation == "up" else 60
        try:
            result = subprocess.run(argv, env=env, cwd=ROOT, timeout=timeout, check=False)
        except (OSError, subprocess.TimeoutExpired) as exc:
            raise IsolationError("isolated operation failed or reached its time limit") from exc
        return result.returncode


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env-file", type=Path, required=True)
    parser.add_argument("--docker-socket", default="unix:///var/run/docker.sock")
    parser.add_argument("operation", choices=(*OPERATIONS, "smoke"))
    args = parser.parse_args()
    try:
        return execute(args.env_file, args.operation, args.docker_socket)
    except IsolationError as exc:
        print("Langfuse isolated check:", exc, file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
