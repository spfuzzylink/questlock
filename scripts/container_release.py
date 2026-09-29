#!/usr/bin/env python3
"""Validate published release inputs for the repository's GHCR workflow."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tarfile
import tempfile


REPOSITORY = "spfuzzylink/questlock"
TAG_PATTERN = re.compile(r"v[0-9]+\.[0-9]+\.[0-9]+")
SHA_PATTERN = re.compile(r"[0-9a-f]{40}")
CONTEXT_FILES = {"questlock", "LICENSE", "THIRD_PARTY_NOTICES.md", "BUILDINFO.json"}


class ReleaseError(Exception):
    pass


def validate_tag(tag):
    if not TAG_PATTERN.fullmatch(tag):
        raise ReleaseError("release version must have the form vX.Y.Z")
    return tag[1:]


def github_api(path):
    result = subprocess.run(["gh", "api", path], check=True, capture_output=True, text=True)
    return json.loads(result.stdout)


def resolve_release(tag, expected_commit):
    version = validate_tag(tag)
    if expected_commit and not SHA_PATTERN.fullmatch(expected_commit):
        raise ReleaseError("the triggering CI run did not supply a full commit SHA")
    release = github_api(f"repos/{REPOSITORY}/releases/tags/{tag}")
    if release.get("draft") or release.get("tag_name") != tag or not release.get("published_at"):
        raise ReleaseError("the requested release is not published")
    created = release["published_at"]
    if not re.fullmatch(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z", created):
        raise ReleaseError("release publication timestamp is invalid")
    reference = github_api(f"repos/{REPOSITORY}/git/ref/tags/{tag}")["object"]
    for _ in range(5):
        if reference.get("type") == "commit":
            break
        if reference.get("type") != "tag" or not SHA_PATTERN.fullmatch(reference.get("sha", "")):
            raise ReleaseError("release tag does not resolve to a commit")
        reference = github_api(f"repos/{REPOSITORY}/git/tags/{reference['sha']}")["object"]
    commit = reference.get("sha", "")
    if reference.get("type") != "commit" or not SHA_PATTERN.fullmatch(commit):
        raise ReleaseError("release tag does not resolve to a commit")
    if expected_commit and commit != expected_commit:
        raise ReleaseError("release commit differs from the successful triggering CI run")
    names = [asset["name"] for asset in release.get("assets", [])]
    for name in ("checksums.txt", f"questlock_{version}_linux_amd64.tar.gz", f"questlock_{version}_linux_arm64.tar.gz"):
        if names.count(name) != 1:
            raise ReleaseError("the published release is missing a unique required asset")
    return {"tag": tag, "version": version, "commit": commit, "created": created}


def download(url, output, maximum):
    # Fixed public release URLs; no credentials, curlrc settings, or HTTP redirects.
    subprocess.run([
        "curl", "--disable", "--fail", "--silent", "--show-error", "--location",
        "--proto", "=https", "--proto-redir", "=https", "--connect-timeout", "15",
        "--max-time", "120", "--max-filesize", str(maximum), "--output", str(output), url,
    ], check=True)


def verify_checksum(manifest, archive, asset):
    matches = []
    for line in manifest.splitlines():
        fields = line.split()
        if len(fields) >= 2 and fields[1] in (asset, "*" + asset):
            if len(fields) != 2 or not re.fullmatch(r"[0-9a-fA-F]{64}", fields[0]):
                raise ReleaseError("release checksum entry is malformed")
            matches.append(fields[0].lower())
    if len(matches) != 1 or hashlib.sha256(archive.read_bytes()).hexdigest() != matches[0]:
        raise ReleaseError("release asset checksum is missing, ambiguous, or does not match")


def read_archive(archive):
    selected = {}
    with tarfile.open(archive, "r:gz") as bundle:
        for index, member in enumerate(bundle):
            if index >= 1000:
                raise ReleaseError("release archive has too many members")
            if member.name not in CONTEXT_FILES:
                continue
            if member.name in selected or not member.isfile() or member.issym() or member.islnk():
                raise ReleaseError("a required archive member is duplicated or not a regular file")
            maximum = 64 << 20 if member.name == "questlock" else 16 << 20
            if member.size < 1 or member.size > maximum:
                raise ReleaseError("a required archive member has an invalid size")
            with bundle.extractfile(member) as stream:
                selected[member.name] = stream.read(maximum + 1)
            if len(selected[member.name]) != member.size:
                raise ReleaseError("a required archive member is truncated")
    if set(selected) != CONTEXT_FILES:
        raise ReleaseError("release archive is missing binary, build metadata, or license notices")
    return selected


def verify_metadata(files, source, version, commit, architecture):
    metadata = json.loads(files["BUILDINFO.json"])
    if not isinstance(metadata, dict):
        raise ReleaseError("release build metadata must be a JSON object")
    required = {"version": version, "commit": commit, "os": "linux", "arch": architecture,
                "source_dirty": False, "cgo_enabled": False}
    if any(metadata.get(key) != value for key, value in required.items()):
        raise ReleaseError("release build metadata differs from the requested clean source/target")
    for name in ("LICENSE", "THIRD_PARTY_NOTICES.md"):
        path = source / name
        if path.is_symlink() or not path.is_file() or path.read_bytes() != files[name]:
            raise ReleaseError("release license notices differ from the pinned source commit")
    version_path = source / "VERSION"
    if version_path.is_symlink() or version_path.read_text().strip() != version:
        raise ReleaseError("release version differs from the pinned source commit")


def prepare_context(tag, commit, architecture, source, output):
    version = validate_tag(tag)
    if not SHA_PATTERN.fullmatch(commit):
        raise ReleaseError("a full source commit SHA is required")
    source_commit = subprocess.check_output(["git", "-C", str(source), "rev-parse", "HEAD"], text=True).strip()
    if source_commit != commit:
        raise ReleaseError("source checkout does not match the resolved release commit")
    if output.exists() or output.is_symlink():
        raise ReleaseError("container context destination must not already exist")
    asset = f"questlock_{version}_linux_{architecture}.tar.gz"
    base = f"https://github.com/{REPOSITORY}/releases/download/{tag}"
    with tempfile.TemporaryDirectory(prefix="questlock-container-release-") as directory:
        directory = Path(directory)
        manifest, archive = directory / "checksums.txt", directory / asset
        download(base + "/checksums.txt", manifest, 64 << 10)
        download(base + "/" + asset, archive, 64 << 20)
        verify_checksum(manifest.read_text(), archive, asset)
        files = read_archive(archive)
        verify_metadata(files, source, version, commit, architecture)
    output.mkdir(mode=0o700)
    for name, data in files.items():
        (output / name).write_bytes(data)
        (output / name).chmod(0o755 if name == "questlock" else 0o644)
    (output / "data").mkdir(mode=0o700)
    print("Verified release binary, target metadata, and pinned license notices.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    resolve = commands.add_parser("resolve")
    resolve.add_argument("--tag", required=True)
    resolve.add_argument("--expected-commit", default="")
    prepare = commands.add_parser("prepare")
    prepare.add_argument("--tag", required=True)
    prepare.add_argument("--commit", required=True)
    prepare.add_argument("--arch", choices=("amd64", "arm64"), required=True)
    prepare.add_argument("--source", type=Path, required=True)
    prepare.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        if args.command == "resolve":
            result = resolve_release(args.tag, args.expected_commit)
            if "GITHUB_OUTPUT" in os.environ:
                with open(os.environ["GITHUB_OUTPUT"], "a") as stream:
                    for key, value in result.items():
                        stream.write(key + "=" + value + "\n")
            print(json.dumps(result, sort_keys=True))
        else:
            prepare_context(args.tag, args.commit, args.arch, args.source, args.output)
    except (ReleaseError, subprocess.CalledProcessError, OSError, ValueError, KeyError, tarfile.TarError) as error:
        message = str(error) if isinstance(error, ReleaseError) else "cannot retrieve or validate the published release"
        print("Container release validation failed: " + message, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
