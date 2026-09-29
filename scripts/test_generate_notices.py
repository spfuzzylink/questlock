"""Deterministic dependency inventory and verbatim notice preservation tests."""

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


spec = importlib.util.spec_from_file_location("notices", Path(__file__).with_name("generate-notices.py"))
notices = importlib.util.module_from_spec(spec)
spec.loader.exec_module(notices)


class NoticeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="questlock-notices-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.goroot = self.root / "toolchain"
        self.goroot.mkdir()
        (self.goroot / "LICENSE").write_bytes(b"Go license fixture\r\nCopyright fixture authors\r\n")
        (self.goroot / "PATENTS").write_bytes(b"Patent grant fixture\n")

    def module(self, name, version, license_text="Copyright upstream@example.org\nTerms verbatim.\n"):
        path = self.root / name.replace("/", "_")
        path.mkdir()
        (path / "LICENSE").write_text(license_text)
        return {"ImportPath": name, "Dir": str(path), "Module": {"Path": name, "Version": version, "Dir": str(path)}}

    def test_consecutive_json_objects_are_parsed_without_tests(self):
        objects = [{"ImportPath": "one"}, {"ImportPath": "two"}]
        self.assertEqual(notices.parse_go_json("\n" + "\n\n".join(json.dumps(x) for x in objects) + "\n"), objects)

    def test_target_union_is_deterministic_and_contains_no_host_paths(self):
        common = self.module("example.org/common", "v1.2.3")
        darwin = self.module("example.org/darwin", "v4.5.6")
        graphs = {"linux/amd64": [common], "darwin/arm64": [darwin, common]}
        first = notices.render_notices(graphs, self.goroot, "go1.27.1")
        second = notices.render_notices({"darwin/arm64": [common, darwin], "linux/amd64": [common]}, self.goroot, "go1.27.1")
        self.assertEqual(first, second)
        self.assertIn(b"Copyright upstream@example.org\nTerms verbatim.\n", first)
        self.assertIn((self.goroot / "LICENSE").read_bytes(), first)
        self.assertNotIn(str(self.root).encode(), first)
        self.assertIn(b"| `example.org/darwin` | `v4.5.6` | darwin/arm64 |", first)

    def test_selected_vendor_and_modernc_supplements_are_preserved(self):
        sqlite = self.module("modernc.org/sqlite", "v1.60.0")
        root = Path(sqlite["Dir"])
        (root / "LICENSE-SQLITE").write_text("SQLite public domain fixture\n")
        (root / "LICENSE-3RD-PARTY.md").write_text("Unselected build dependency fixture\n")
        (root / "LICENSE-SQLITE_VEC").write_text("Unselected vec fixture\n")
        vendor = self.goroot / "src/vendor/example.org/crypto"
        vendor.mkdir(parents=True)
        (vendor / "LICENSE").write_text("Vendored license fixture\n")
        package = {"Standard": True, "ImportPath": "vendor/example.org/crypto", "Dir": str(vendor)}
        result = notices.render_notices({"linux/amd64": [sqlite, package]}, self.goroot, "go1.27.1")
        self.assertIn(b"SQLite public domain fixture", result)
        self.assertIn(b"Vendored license fixture", result)
        self.assertNotIn(b"Unselected build dependency fixture", result)
        self.assertNotIn(b"Unselected vec fixture", result)

    def test_missing_or_symlinked_licenses_fail_closed(self):
        package = self.module("example.org/module", "v1.0.0")
        license_path = Path(package["Dir"]) / "LICENSE"
        license_path.unlink()
        with self.assertRaises(notices.NoticeError):
            notices.render_notices({"linux/amd64": [package]}, self.goroot, "go1.27.1")
        outside = self.root / "outside-notice"
        outside.write_text("Not part of the pinned module")
        license_path.symlink_to(outside)
        with self.assertRaises(notices.NoticeError):
            notices.render_notices({"linux/amd64": [package]}, self.goroot, "go1.27.1")

    def test_replacements_and_test_packages_require_review(self):
        package = self.module("example.org/module", "v1.0.0")
        package["Module"]["Replace"] = {"Path": "../local"}
        with self.assertRaises(notices.NoticeError):
            notices.render_notices({"linux/amd64": [package]}, self.goroot, "go1.27.1")
        with self.assertRaises(notices.NoticeError):
            notices.render_notices({"linux/amd64": [{"ForTest": "example.org/module"}]}, self.goroot, "go1.27.1")

    def test_source_notices_preserve_complete_comments_and_ignore_strings(self):
        source = '''// Copyright 2020 Author.
// All rights reserved.
package example
var fake = "/* Copyright in a string, not a notice */"
var raw = `// Copyright in a raw string, not a notice`
/*
Copyright 2019 Another Author
Permission is hereby granted to use this software.
*/
// An ordinary nonlegal implementation comment.
'''
        blocks = notices.legal_comments(source)
        self.assertEqual(len(blocks), 2)
        self.assertEqual(blocks[0], "// Copyright 2020 Author.\n// All rights reserved.")
        self.assertIn("Permission is hereby granted", blocks[1])
        self.assertNotIn("in a string", "".join(blocks))


if __name__ == "__main__":
    unittest.main()
