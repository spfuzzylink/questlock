#!/usr/bin/env python3
"""Validate the disposable credential file boundary, never real credentials."""

import importlib.util
import os
from pathlib import Path
import stat
import tempfile
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location(
    "langfuse_prepare", Path(__file__).parent / "langfuse" / "prepare.py"
)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class PrepareTest(unittest.TestCase):
    def test_private_complete_fresh_credentials(self):
        with tempfile.TemporaryDirectory() as root:
            first, second = Path(root) / "one.env", Path(root) / "two.env"
            with patch.dict(os.environ, {"GITHUB_ACTIONS": "false"}):
                MODULE.prepare(first)
                MODULE.prepare(second)
            self.assertEqual(stat.S_IMODE(first.stat().st_mode), 0o600)
            a = dict(line.split("=", 1) for line in first.read_text().splitlines())
            b = dict(line.split("=", 1) for line in second.read_text().splitlines())
            self.assertEqual(len(a["ENCRYPTION_KEY"]), 64)
            self.assertEqual(len(a["MINIO_ROOT_USER"]), 20)
            self.assertEqual(len(a["MINIO_ROOT_PASSWORD"]), 40)
            self.assertTrue(a["LANGFUSE_PUBLIC_KEY"].startswith("pk-lf-"))
            self.assertTrue(a["LANGFUSE_SECRET_KEY"].startswith("sk-lf-"))
            self.assertIn(a["POSTGRES_PASSWORD"], a["DATABASE_URL"])
            for key in a.keys() - {"LANGFUSE_BASE_URL"}:
                self.assertNotEqual(a[key], b[key])
            # Generated values can be sourced by a shell and Compose without
            # expansion, quoting ambiguity, or embedded newlines.
            for value in a.values():
                self.assertNotRegex(value, r"[\s'\"$`\\]")

    def test_existing_environment_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as root:
            destination = Path(root) / "existing.env"
            destination.write_text("human-owned fixture\n")
            with patch.dict(os.environ, {"GITHUB_ACTIONS": "false"}):
                with self.assertRaises(FileExistsError):
                    MODULE.prepare(destination)
            self.assertEqual(destination.read_text(), "human-owned fixture\n")


if __name__ == "__main__":
    unittest.main()
