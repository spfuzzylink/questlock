"""Release-boundary tests: no host metadata or accidental private inputs."""

import importlib.util
from pathlib import Path
import tarfile
import tempfile
import unittest


spec = importlib.util.spec_from_file_location("package", Path(__file__).with_name("package.py"))
package = importlib.util.module_from_spec(spec)
spec.loader.exec_module(package)


class PackageTests(unittest.TestCase):
    def test_release_allowlist_preserves_third_party_notices_verbatim(self):
        self.assertIn("THIRD_PARTY_NOTICES.md", package.PUBLIC_FILES)
        notice = package.public_file(package.ROOT, "THIRD_PARTY_NOTICES.md")
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / "release.tar.gz"
            package.archive(output, b"binary fixture", {"THIRD_PARTY_NOTICES.md": notice}, {"version": "0.1.1"})
            with tarfile.open(output) as bundle:
                self.assertEqual(bundle.extractfile("THIRD_PARTY_NOTICES.md").read(), notice)

    def test_archive_has_only_explicit_inputs_and_normalized_ownership(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / "private.token").write_text("local fixture never packaged")
            first, second = root / "first.tar.gz", root / "second.tar.gz"
            for output in (first, second):
                package.archive(output, b"binary fixture", {"LICENSE": b"license"}, {"version": "0.1.0"})
            self.assertEqual(first.read_bytes(), second.read_bytes())
            with tarfile.open(first) as bundle:
                self.assertEqual(bundle.getnames(), ["questlock", "LICENSE", "BUILDINFO.json"])
                for entry in bundle:
                    self.assertTrue(entry.isfile())
                    self.assertEqual((entry.uid, entry.gid, entry.mtime, entry.uname, entry.gname),
                                     (0, 0, 0, "", ""))
                self.assertEqual(bundle.getmember("questlock").mode, 0o755)
                self.assertEqual(bundle.extractfile("questlock").read(), b"binary fixture")

    def test_symlinked_public_inputs_cannot_copy_private_files(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / "private.txt").write_text("private fixture")
            (root / "README.md").symlink_to("private.txt")
            with self.assertRaises(ValueError):
                package.public_file(root, "README.md")
            (root / "real-docs").mkdir()
            (root / "real-docs/installation.md").write_text("private fixture")
            (root / "docs").symlink_to("real-docs", target_is_directory=True)
            with self.assertRaises(ValueError):
                package.public_file(root, "docs/installation.md")


if __name__ == "__main__":
    unittest.main()
