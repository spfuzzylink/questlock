#!/usr/bin/env python3
"""Offline installer tests; mocked curl never reaches the network."""

import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest


INSTALLER = Path(__file__).resolve().with_name("install.sh")
MOCK_CURL = r'''#!/usr/bin/env python3
import json, os, pathlib, re, shutil, sys
args = sys.argv[1:]
assert args[0] == "--disable", "curl configuration was not disabled first"
assert args[args.index("--proto") + 1] == "=https"
assert args[args.index("--proto-redir") + 1] == "=https"
assert "--location" in args and "--fail" in args
url = args[-1]
assert re.fullmatch(r"https://github\.com/spfuzzylink/questlock/releases/download/v[0-9]+\.[0-9]+\.[0-9]+/(checksums\.txt|questlock_[0-9]+\.[0-9]+\.[0-9]+_(darwin|linux)_(amd64|arm64)\.tar\.gz)", url), url
asset = url.rsplit("/", 1)[-1]
with open(os.environ["QUESTLOCK_CURL_LOG"], "a") as log:
    log.write(json.dumps(args) + "\n")
if os.environ.get("QUESTLOCK_CURL_FAIL") == asset:
    sys.exit(22)
shutil.copyfile(pathlib.Path(os.environ["QUESTLOCK_FIXTURES"]) / asset, args[args.index("--output") + 1])
'''
MOCK_UNAME = r'''#!/bin/sh
case "$1" in
  -s) printf '%s\n' "$QUESTLOCK_TEST_OS" ;;
  -m) printf '%s\n' "$QUESTLOCK_TEST_ARCH" ;;
  *) exit 1 ;;
esac
'''


class InstallTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="questlock-install-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "repo with spaces"
        scripts = self.repo / "scripts"
        scripts.mkdir(parents=True)
        self.script = scripts / "install.sh"
        shutil.copyfile(INSTALLER, self.script)
        (self.repo / "VERSION").write_text("0.1.0\n")
        self.fixtures = self.root / "fixtures"
        self.fixtures.mkdir()
        mock_bin = self.root / "mock tools"
        mock_bin.mkdir()
        for name, content in (("curl", MOCK_CURL), ("uname", MOCK_UNAME)):
            path = mock_bin / name
            path.write_text(content)
            path.chmod(0o755)
        self.log = self.root / "curl.jsonl"
        self.env = dict(os.environ)
        self.env.update({
            "PATH": str(mock_bin) + os.pathsep + os.environ["PATH"],
            "QUESTLOCK_FIXTURES": str(self.fixtures),
            "QUESTLOCK_CURL_LOG": str(self.log),
            "QUESTLOCK_TEST_OS": "Darwin",
            "QUESTLOCK_TEST_ARCH": "arm64",
        })
        self.destination = self.repo / "bin" / "questlock"
        self.destination.parent.mkdir()
        self.destination.write_bytes(b"previous binary")
        self.destination.chmod(0o755)
        self.binary = b"fixture binary: this is data, never executed\n"
        self.asset = self.make_release()

    def make_release(self, version="0.1.0", os_name="darwin", arch="arm64", kind="regular", extra_path=None):
        asset = f"questlock_{version}_{os_name}_{arch}.tar.gz"
        archive = self.fixtures / asset
        with tarfile.open(archive, "w:gz") as output:
            member = tarfile.TarInfo("questlock")
            member.mode = 0o755
            if kind == "symlink":
                member.type = tarfile.SYMTYPE
                member.linkname = str(self.root / "outside-target")
                output.addfile(member)
            elif kind == "directory":
                member.type = tarfile.DIRTYPE
                output.addfile(member)
            else:
                member.size = len(self.binary)
                output.addfile(member, io.BytesIO(self.binary))
                if kind == "duplicate":
                    output.addfile(member, io.BytesIO(self.binary))
            if extra_path:
                extra = tarfile.TarInfo(extra_path)
                extra.size = 6
                output.addfile(extra, io.BytesIO(b"escape"))
        self.write_checksum(archive)
        return asset

    def write_checksum(self, archive):
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        (self.fixtures / "checksums.txt").write_text(f"{digest}  {archive.name}\n")

    def run_installer(self, *args):
        result = subprocess.run(["sh", str(self.script), *args], cwd=self.root, env=self.env, text=True, capture_output=True, timeout=15)
        self.assertFalse(list(self.destination.parent.glob(".questlock-install.*")), "staging directory was not cleaned up")
        return result

    def assert_preserved(self, result):
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.destination.read_bytes(), b"previous binary")

    def test_success_defaults_to_repo_bin_from_another_working_directory(self):
        result = self.run_installer()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.destination.read_bytes(), self.binary)
        self.assertEqual(self.destination.stat().st_mode & 0o777, 0o755)
        calls = [json.loads(line) for line in self.log.read_text().splitlines()]
        self.assertEqual(len(calls), 2)
        self.assertTrue(calls[-1][-1].endswith("/v0.1.0/" + self.asset))

    def test_version_and_destination_with_spaces(self):
        self.make_release(version="0.2.3")
        target = self.root / "custom tools with spaces"
        result = self.run_installer("--version", "v0.2.3", "--dir", str(target))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((target / "questlock").read_bytes(), self.binary)
        self.assertEqual(self.destination.read_bytes(), b"previous binary")
        self.assertFalse(list(target.glob(".questlock-install.*")))

    def test_linux_architecture_mapping(self):
        self.env.update(QUESTLOCK_TEST_OS="Linux", QUESTLOCK_TEST_ARCH="x86_64")
        self.make_release(os_name="linux", arch="amd64")
        result = self.run_installer()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("linux/amd64", result.stdout)

    def test_download_failures_preserve_existing_binary(self):
        for asset in ("checksums.txt", self.asset):
            with self.subTest(asset=asset):
                self.env["QUESTLOCK_CURL_FAIL"] = asset
                self.assert_preserved(self.run_installer())

    def test_checksum_mismatch_preserves_existing_binary(self):
        (self.fixtures / "checksums.txt").write_text(f"{'0' * 64}  {self.asset}\n")
        result = self.run_installer()
        self.assert_preserved(result)
        self.assertIn("SHA256 mismatch", result.stderr)

    def test_checksum_entry_must_be_exact_and_unique(self):
        valid = (self.fixtures / "checksums.txt").read_text()
        for manifest in (valid.replace(self.asset, "prefix-" + self.asset), valid + valid, valid.replace("  ", "  extra "), "bad-digest  " + self.asset + "\n"):
            with self.subTest(manifest=manifest):
                (self.fixtures / "checksums.txt").write_text(manifest)
                self.assert_preserved(self.run_installer())

    def test_malformed_archive_preserves_existing_binary(self):
        archive = self.fixtures / self.asset
        archive.write_bytes(b"not an archive")
        self.write_checksum(archive)
        self.assert_preserved(self.run_installer())

    def test_nonregular_and_duplicate_archive_members_are_rejected(self):
        for kind in ("symlink", "directory", "duplicate"):
            with self.subTest(kind=kind):
                self.make_release(kind=kind)
                self.assert_preserved(self.run_installer())

    def test_unselected_archive_paths_cannot_escape(self):
        escape = self.root / "should-not-exist"
        self.make_release(extra_path=str(escape))
        result = self.run_installer()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(escape.exists())
        self.assertEqual(self.destination.read_bytes(), self.binary)

    def test_symlink_binary_and_directory_are_rejected(self):
        outside = self.root / "outside-binary"
        outside.write_bytes(b"outside unchanged")
        self.destination.unlink()
        self.destination.symlink_to(outside)
        result = self.run_installer()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(outside.read_bytes(), b"outside unchanged")
        linked = self.root / "linked-bin"
        linked.symlink_to(self.destination.parent, target_is_directory=True)
        for suffix in ("", "/", "/.", "/./"):
            result = self.run_installer("--dir", str(linked) + suffix)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("symlink destination directory", result.stderr)
        self.assertFalse(self.log.exists())

    def test_unsupported_architecture_fails_before_download(self):
        self.env["QUESTLOCK_TEST_ARCH"] = "riscv64"
        result = self.run_installer()
        self.assert_preserved(result)
        self.assertIn("unsupported architecture", result.stderr)
        self.assertFalse(self.log.exists())

    def test_unsupported_os_fails_before_download(self):
        self.env["QUESTLOCK_TEST_OS"] = "FreeBSD"
        result = self.run_installer()
        self.assert_preserved(result)
        self.assertIn("unsupported operating system", result.stderr)
        self.assertFalse(self.log.exists())

    def test_invalid_version_cannot_change_download_source(self):
        for version in ("../latest", "https://example.invalid", "0.1.0/evil", "0.1.0\n1.2.3"):
            with self.subTest(version=version):
                result = self.run_installer("--version", version)
                self.assert_preserved(result)
                self.assertIn("version must have the form", result.stderr)
        self.assertFalse(self.log.exists())

    def test_help_does_not_download_or_modify_binary(self):
        result = self.run_installer("--help")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("--dir DIRECTORY", result.stdout)
        self.assertEqual(self.destination.read_bytes(), b"previous binary")
        self.assertFalse(self.log.exists())


if __name__ == "__main__":
    unittest.main()
