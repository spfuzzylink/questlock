#!/usr/bin/env python3
"""Check tracked source, the index, and all locally reachable Git history.

This is a conservative publication guard, not a complete secret detector. It
does not inspect untracked files, the host's home directory, or unreachable Git
objects. Run a dedicated secret scanner and review the public diff as well.

QUESTLOCK_FORBIDDEN_STRINGS may contain a JSON array of private strings to reject
case-insensitively. Supply it outside the repository; values and matching source
text are never printed. Keep the checkout complete: shallow history is rejected.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
import sys


MAX_FILE_BYTES = 16 * 1024 * 1024
OBJECT_ID = re.compile(rb"^[0-9a-f]{40}(?:[0-9a-f]{24})?$")
SENSITIVE_EXTENSIONS = (
    ".token", ".pem", ".key", ".p12", ".pfx", ".keystore", ".jks",
    ".db", ".db-wal", ".db-shm", ".db-journal", ".sqlite", ".sqlite3",
    ".sqlite-wal", ".sqlite-shm", ".sqlite-journal", ".sqlite3-wal",
    ".sqlite3-shm", ".sqlite3-journal", ".log",
)
SENSITIVE_COMPONENTS = {
    ".questlock", ".agent-fence", ".ssh", ".aws", ".gnupg",
    "secrets", "credentials", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519",
    ".netrc", ".npmrc", ".pypirc", ".git-credentials", "credentials.json",
}
TOKEN_PATTERNS = (
    ("GitHub credential", rb"\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})\b"),
    ("AWS access key", rb"\b(?:AKIA|ASIA)[A-Z0-9]{16}\b"),
    ("API credential", rb"\bsk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{20,}\b"),
    ("Slack credential", rb"\bxox[baprs]-[A-Za-z0-9-]{10,}\b"),
    ("GitLab credential", rb"\bglpat-[A-Za-z0-9_-]{20,}\b"),
    ("Questlock credential", rb"\b(?:af|ql)_[A-Za-z0-9_-]{43}\b"),
    ("private key", rb"-----BEGIN (?:[A-Z0-9 ]+ )?PRIVATE KEY-----"),
    ("SQLite database content", rb"SQLite format 3\x00"),
)
PATTERNS = [(name, re.compile(pattern)) for name, pattern in TOKEN_PATTERNS]
# Build these prefixes in pieces so the guard does not match its own source.
HOME_PREFIXES = (b"/" + b"Users" + b"/", b"/" + b"home" + b"/")
HOME_PATTERNS = [
    re.compile(re.escape(prefix) + rb"[A-Za-z0-9_.-]+(?:/|(?=[\s\"'<>]|$))")
    for prefix in HOME_PREFIXES
] + [
    re.compile(rb"[A-Za-z]:[\\/]" + b"Users" + rb"[\\/][A-Za-z0-9_. -]+[\\/]")
]
EMAIL = re.compile(rb"[A-Za-z0-9._%+\[\]-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}")
# Exact, reviewed third-party notice documents contain upstream public copyright
# emails. Keep old digests when regenerating notices: reachable historical blobs
# are immutable and remain subject to this guard. This only permits those email
# addresses; credentials, home paths, and configured private strings still fail.
REVIEWED_PUBLIC_NOTICE_SHA256 = frozenset({
    "dba84d51f9acb52be9d6494effa48069be60ac4731b805fe16f2c4259f028cb2",
})


class CheckError(Exception):
    """A check could not complete; its message must not contain source text."""


def git(root, *args):
    env = dict(os.environ, GIT_NO_REPLACE_OBJECTS="1")
    result = subprocess.run(
        ["git", "-c", "core.quotePath=false", *args], cwd=root,
        stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, check=False, env=env,
    )
    if result.returncode:
        raise CheckError("Git inspection failed; run from a readable Git checkout")
    return result.stdout


def load_forbidden():
    try:
        values = json.loads(os.environ.get("QUESTLOCK_FORBIDDEN_STRINGS", "[]"))
    except (ValueError, TypeError) as exc:
        raise CheckError("QUESTLOCK_FORBIDDEN_STRINGS must be a JSON array of nonempty strings") from exc
    if not isinstance(values, list) or any(not isinstance(v, str) or not v for v in values):
        raise CheckError("QUESTLOCK_FORBIDDEN_STRINGS must be a JSON array of nonempty strings")
    return tuple(value.casefold() for value in values)


def public_email(address, metadata=False):
    address = address.lower()
    if address.endswith(b"@users.noreply.github.com") or address == b"noreply@github.com":
        return True
    if not metadata:
        return address == b"git@github.com" or address.rsplit(b"@", 1)[-1] in {
            b"example.com", b"example.net", b"example.org",
        }
    return False


def content_issues(data, forbidden=()):
    issues = {name for name, pattern in PATTERNS if pattern.search(data)}
    if any(pattern.search(data) for pattern in HOME_PATTERNS):
        issues.add("absolute home-directory path")
    reviewed_notice = hashlib.sha256(data).hexdigest() in REVIEWED_PUBLIC_NOTICE_SHA256
    if not reviewed_notice and any(not public_email(match.group()) for match in EMAIL.finditer(data)):
        issues.add("email address outside public/example allowlist")
    if forbidden:
        text = data.decode("utf-8", errors="replace").casefold()
        if any(value in text for value in forbidden):
            issues.add("configured private string")
    return issues


def path_issues(path):
    parts = PurePosixPath(path.decode("utf-8", errors="surrogateescape")).parts
    lowered = [part.lower() for part in parts]
    issues = set()
    if lowered and lowered[0] == ".local":
        issues.add("credential or local-state path")
    if any(part in SENSITIVE_COMPONENTS or part == ".env" or part.startswith(".env.") for part in lowered):
        issues.add("credential or local-state path")
    if lowered and lowered[-1].endswith(SENSITIVE_EXTENSIONS):
        issues.add("credential, database, or log file")
    if lowered and lowered[0] in {"bin", "dist"}:
        issues.add("build-output path")
    return issues


class ObjectReader:
    def __init__(self, root):
        self.process = subprocess.Popen(
            ["git", "cat-file", "--batch"], cwd=root,
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            env=dict(os.environ, GIT_NO_REPLACE_OBJECTS="1"),
        )

    def read(self, oid):
        if not OBJECT_ID.fullmatch(oid):
            raise CheckError("Git returned an invalid object identifier")
        self.process.stdin.write(oid + b"\n")
        self.process.stdin.flush()
        header = self.process.stdout.readline().split()
        if len(header) != 3 or header[0] != oid:
            raise CheckError("Git history contains an unreadable object")
        try:
            size = int(header[2])
        except ValueError as exc:
            raise CheckError("Git returned an invalid object size") from exc
        if size < 0 or size > MAX_FILE_BYTES:
            raise CheckError("Tracked Git object exceeds the 16 MiB inspection limit")
        data = self.process.stdout.read(size)
        if len(data) != size or self.process.stdout.read(1) != b"\n":
            raise CheckError("Git returned incomplete object contents")
        return header[1], data

    def close(self):
        # Also terminates a cat-file process blocked on a rejected large object.
        self.process.kill()
        self.process.communicate()


class Guard:
    def __init__(self, root, forbidden):
        self.root = root
        self.forbidden = forbidden
        self.findings = set()
        self.blob_results = {}
        self.trees = set()
        self.reader = ObjectReader(root)
        self.commit_count = 0
        self.path_count = 0

    def label(self, path, location):
        if content_issues(path, self.forbidden):
            return f"<redacted path> ({location})"
        # Escaping prevents filenames from injecting terminal control sequences.
        return f"{json.dumps(os.fsdecode(path), ensure_ascii=True)} ({location})"

    def record(self, label, issues):
        self.findings.update((label, issue) for issue in issues)

    def path(self, path, location):
        label = self.label(path, location)
        self.record(label, path_issues(path) | content_issues(path, self.forbidden))
        return label

    def blob(self, oid, label):
        if oid not in self.blob_results:
            kind, content = self.reader.read(oid)
            if kind != b"blob":
                raise CheckError("Expected a tracked file blob")
            self.blob_results[oid] = content_issues(content, self.forbidden)
        self.record(label, self.blob_results[oid])

    def working_file(self, path, label):
        file = self.root / os.fsdecode(path)
        # Never follow a tracked symlink (or a changed parent symlink) outside
        # the checkout to inspect arbitrary host files.
        if not file.parent.resolve().is_relative_to(self.root):
            self.record(label, {"tracked path escapes checkout"})
            return
        try:
            info = file.lstat()
        except FileNotFoundError:
            return  # A pending deletion is still inspected through its index blob.
        if stat.S_ISLNK(info.st_mode):
            content = os.fsencode(os.readlink(file))
        elif stat.S_ISREG(info.st_mode):
            if info.st_size > MAX_FILE_BYTES:
                self.record(label, {"file exceeds 16 MiB inspection limit"})
                return
            with file.open("rb") as stream:
                content = stream.read(MAX_FILE_BYTES + 1)
            if len(content) > MAX_FILE_BYTES:
                self.record(label, {"file exceeds 16 MiB inspection limit"})
                return
        else:
            self.record(label, {"tracked path is not a regular file or symlink"})
            return
        self.record(label, content_issues(content, self.forbidden))

    def index_and_worktree(self):
        for entry in git(self.root, "ls-files", "--stage", "-z").split(b"\0"):
            if not entry:
                continue
            header, path = entry.split(b"\t", 1)
            mode, oid, stage = header.split()
            self.path_count += 1
            label = self.path(path, "index")
            if stage != b"0":
                self.record(label, {"unmerged index entry"})
            if mode == b"160000":
                self.record(label, {"submodule contents require a separate review"})
                continue
            self.blob(oid, label)
            self.working_file(path, self.path(path, "working tree"))

    def metadata(self, oid, kind, content):
        label = f"{kind} {oid.decode('ascii')[:12]} metadata"
        self.record(label, content_issues(content, self.forbidden))
        for line in content.split(b"\n\n", 1)[0].splitlines():
            if line.startswith((b"author ", b"committer ", b"tagger ")):
                match = re.search(rb"<([^<>]*)> [0-9]+ [+-][0-9]{4}$", line)
                if not match or not public_email(match.group(1), metadata=True):
                    self.record(label, {"author/committer email is not a GitHub noreply address"})

    def tree(self, tree):
        if tree in self.trees:
            return
        self.trees.add(tree)
        for entry in git(self.root, "ls-tree", "-r", "-z", tree.decode("ascii")).split(b"\0"):
            if not entry:
                continue
            header, path = entry.split(b"\t", 1)
            _mode, kind, oid = header.split()
            label = self.path(path, "history")
            if kind == b"blob":
                self.blob(oid, label)
            else:
                self.record(label, {"submodule contents require a separate review"})

    def history(self):
        head = git(self.root, "rev-parse", "--revs-only", "HEAD").strip()
        revisions = [head.decode("ascii")] if OBJECT_ID.fullmatch(head) else []
        for oid in git(self.root, "rev-list", "--all", *revisions).splitlines():
            kind, content = self.reader.read(oid)
            if kind != b"commit":
                raise CheckError("Expected a commit in reachable history")
            self.commit_count += 1
            self.metadata(oid, "commit", content)
            tree = content.splitlines()[0].removeprefix(b"tree ")
            if not OBJECT_ID.fullmatch(tree):
                raise CheckError("Commit has no readable tree")
            self.tree(tree)
        seen_tags = set()
        for line in git(self.root, "for-each-ref", "--format=%(objecttype) %(objectname) %(refname)").splitlines():
            kind, oid, name = line.split(b" ", 2)
            self.record(self.label(name, "ref"), content_issues(name, self.forbidden))
            while kind == b"tag" and oid not in seen_tags:
                seen_tags.add(oid)
                _, content = self.reader.read(oid)
                self.metadata(oid, "tag", content)
                headers = dict(line.split(b" ", 1) for line in content.split(b"\n\n", 1)[0].splitlines()[:2])
                oid, kind = headers.get(b"object", b""), headers.get(b"type", b"")
            if kind == b"blob":
                self.blob(oid, "tag target")
            elif kind == b"tree":
                self.tree(oid)

    def run(self):
        try:
            self.index_and_worktree()
            self.history()
            return sorted(self.findings)
        finally:
            self.reader.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", default=".", help="Git checkout to inspect (default: current directory)")
    args = parser.parse_args()
    try:
        root = Path(os.fsdecode(git(args.repo, "rev-parse", "--show-toplevel").strip())).resolve()
        if git(root, "rev-parse", "--is-shallow-repository").strip() != b"false":
            raise CheckError("Shallow history cannot be fully inspected; fetch full history first")
        guard = Guard(root, load_forbidden())
        findings = guard.run()
        if findings:
            for label, issue in findings:
                print(f"FAIL {label}: {issue}", file=sys.stderr)
            print("Publication guard failed; matched contents were withheld.", file=sys.stderr)
            return 1
        print(f"Public-source guard passed: {guard.path_count} tracked paths, {guard.commit_count} reachable commits, {len(guard.blob_results)} unique blobs.")
        print("Untracked files and unreachable objects were not inspected; pattern checks do not prove absence of secrets or identity.")
        return 0
    except Exception as exc:
        # OS errors may embed private absolute paths, so only our safe custom
        # diagnostics may be displayed verbatim.
        message = str(exc) if isinstance(exc, CheckError) else "Inspection could not complete; check repository access and file integrity"
        print(f"FAIL {message}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
