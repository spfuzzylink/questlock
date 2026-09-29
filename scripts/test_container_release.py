"""Offline checks for the GHCR release-input boundary; no registry writes."""

import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("container_release", Path(__file__).with_name("container_release.py"))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ContainerReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="questlock-container-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.commit = "a" * 40
        self.version = "0.1.1"
        self.metadata = {"version": self.version, "commit": self.commit, "os": "linux", "arch": "amd64", "source_dirty": False, "cgo_enabled": False}
        self.files = {"questlock": b"binary fixture never executed", "LICENSE": b"license fixture", "THIRD_PARTY_NOTICES.md": b"notice fixture", "BUILDINFO.json": json.dumps(self.metadata).encode()}
        self.source = self.root / "source"
        self.source.mkdir()
        for name in ("LICENSE", "THIRD_PARTY_NOTICES.md"):
            (self.source / name).write_bytes(self.files[name])
        (self.source / "VERSION").write_text(self.version + "\n")

    def published(self):
        return {"draft": False, "tag_name": "v0.1.1", "published_at": "2026-09-29T02:00:46Z", "assets": [{"name": name} for name in ("checksums.txt", "questlock_0.1.1_linux_amd64.tar.gz", "questlock_0.1.1_linux_arm64.tar.gz")]}

    def archive(self, duplicate=False, symlink=False):
        path = self.root / "release.tar.gz"
        with tarfile.open(path, "w:gz") as bundle:
            for name, data in self.files.items():
                member = tarfile.TarInfo(name)
                member.size = len(data)
                if symlink and name == "questlock":
                    member.type = tarfile.SYMTYPE
                    member.linkname = "/outside"
                    member.size = 0
                    bundle.addfile(member)
                else:
                    bundle.addfile(member, io.BytesIO(data))
                if duplicate and name == "questlock":
                    bundle.addfile(member, io.BytesIO(data))
            # An unselected archive path is never extracted to the filesystem.
            member = tarfile.TarInfo("../../outside")
            member.size = 4
            bundle.addfile(member, io.BytesIO(b"nope"))
        return path

    def test_version_rejects_shell_and_url_injection(self):
        for tag in ("v0.1.1;echo bad", "v0.1.1\nEVIL=value", "../../latest", "https://example.org", "main"):
            with self.subTest(tag=tag), self.assertRaises(release.ReleaseError):
                release.validate_tag(tag)

    def test_resolver_requires_published_release_and_exact_triggering_commit(self):
        replies = [self.published(), {"object": {"type": "tag", "sha": "b" * 40}}, {"object": {"type": "commit", "sha": self.commit}}]
        with patch.object(release, "github_api", side_effect=replies):
            result = release.resolve_release("v0.1.1", self.commit)
        self.assertEqual(result["commit"], self.commit)
        with patch.object(release, "github_api", side_effect=[self.published(), {"object": {"type": "commit", "sha": self.commit}}]):
            with self.assertRaises(release.ReleaseError):
                release.resolve_release("v0.1.1", "b" * 40)
        draft = self.published()
        draft["draft"] = True
        with patch.object(release, "github_api", return_value=draft), self.assertRaises(release.ReleaseError):
            release.resolve_release("v0.1.1", "")

    def test_checksum_must_match_one_exact_asset(self):
        archive = self.archive()
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        valid = digest + "  release.tar.gz\n"
        release.verify_checksum(valid, archive, "release.tar.gz")
        for manifest in (valid + valid, valid.replace("release.tar.gz", "other.tar.gz"), "0" * 64 + "  release.tar.gz\n"):
            with self.assertRaises(release.ReleaseError):
                release.verify_checksum(manifest, archive, "release.tar.gz")

    def test_archive_allowlist_reads_bytes_without_extracting_paths(self):
        self.assertEqual(release.read_archive(self.archive()), self.files)
        self.assertFalse((self.root / "outside").exists())
        for options in ({"duplicate": True}, {"symlink": True}):
            with self.assertRaises(release.ReleaseError):
                release.read_archive(self.archive(**options))

    def test_metadata_and_notices_must_match_pinned_source(self):
        release.verify_metadata(self.files, self.source, self.version, self.commit, "amd64")
        for key, value in (("commit", "b" * 40), ("arch", "arm64"), ("source_dirty", True), ("cgo_enabled", True)):
            files = dict(self.files, **{"BUILDINFO.json": json.dumps(dict(self.metadata, **{key: value})).encode()})
            with self.assertRaises(release.ReleaseError):
                release.verify_metadata(files, self.source, self.version, self.commit, "amd64")
        (self.source / "THIRD_PARTY_NOTICES.md").write_text("changed notice")
        with self.assertRaises(release.ReleaseError):
            release.verify_metadata(self.files, self.source, self.version, self.commit, "amd64")

    def test_non_object_build_metadata_fails_cleanly(self):
        files = dict(self.files, **{"BUILDINFO.json": b"[]"})
        with self.assertRaises(release.ReleaseError):
            release.verify_metadata(files, self.source, self.version, self.commit, "amd64")


if __name__ == "__main__":
    unittest.main()
