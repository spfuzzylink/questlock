#!/usr/bin/env python3
"""Create fresh disposable Compose credentials without printing their values."""

import argparse
import os
from pathlib import Path
import secrets


def prepare(destination):
    values = {
        name: secrets.token_hex(32)
        for name in (
            "POSTGRES_PASSWORD", "CLICKHOUSE_PASSWORD", "REDIS_AUTH",
            "SALT", "ENCRYPTION_KEY", "NEXTAUTH_SECRET",
        )
    }
    values.update({
        "MINIO_ROOT_USER": "ql" + secrets.token_hex(9),
        "MINIO_ROOT_PASSWORD": secrets.token_hex(20),
        "LANGFUSE_PUBLIC_KEY": "pk-lf-" + secrets.token_hex(16),
        "LANGFUSE_SECRET_KEY": "sk-lf-" + secrets.token_hex(32),
        "LANGFUSE_INIT_ORG_ID": "questlock-" + secrets.token_hex(12),
        "LANGFUSE_INIT_PROJECT_ID": "questlock-" + secrets.token_hex(12),
        "LANGFUSE_BASE_URL": "http://127.0.0.1:3000",
    })
    values["DATABASE_URL"] = (
        "postgresql://questlock:" + values["POSTGRES_PASSWORD"]
        + "@postgres:5432/questlock"
    )
    # Exclusive creation prevents silently replacing credentials of another run.
    fd = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as output:
        for name, value in values.items():
            output.write(f"{name}={value}\n")
    # Actions mask commands register secrets without displaying their values.
    if os.environ.get("GITHUB_ACTIONS") == "true":
        for value in values.values():
            if value != values["LANGFUSE_BASE_URL"]:
                print("::add-mask::" + value)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("destination", type=Path)
    prepare(parser.parse_args().destination)
