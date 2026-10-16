"""Offline checks for the actual Docker context validator's allowlist and cleanup."""

import importlib.util
from pathlib import Path
import tempfile
import unittest


spec = importlib.util.spec_from_file_location("container_context", Path(__file__).with_name("check-container-context.py"))
context = importlib.util.module_from_spec(spec)
spec.loader.exec_module(context)


class ContainerContextTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="questlock-context-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for name in context.REQUIRED_FILES:
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("public build input fixture")

    def test_required_inputs_exist_in_the_checkout(self):
        for name in context.REQUIRED_FILES:
            with self.subTest(name=name):
                self.assertTrue((context.ROOT / name).is_file())

    def test_required_sources_and_only_named_nested_embedded_files_are_allowed(self):
        self.assertTrue(all(context.allowed_file(name) for name in context.REQUIRED_FILES))
        self.assertEqual(context.validate_context(self.root), context.REQUIRED_FILES)
        for name in ("observability/score.go", "delegation/validate.go", "worker/path_unix.go", "evaluation/checks.go", "revision/types.go"):
            self.assertTrue(context.allowed_file(name))
        for name in (*context.PRIVATE_SENTINELS, "evaluation/fixtures/other.json",
                     "evaluation/fixtures/nested/quickstart.json", "worker/nested/worker.go",
                     "evaluation/fixtures.go/private.go", "worker/worker_test.go", "/worker/worker.go", "../worker/worker.go"):
            with self.subTest(name=name):
                self.assertFalse(context.allowed_file(name))

    def test_unexpected_private_file_and_missing_fixture_fail_validation(self):
        private = self.root / "evaluation/fixtures/request.json"
        private.write_text("private fixture")
        with self.assertRaises(context.ContextError):
            context.validate_context(self.root)
        private.unlink()
        (self.root / "evaluation/fixtures/quickstart.json").unlink()
        with self.assertRaises(context.ContextError):
            context.validate_context(self.root)

    def test_symlinks_in_the_exported_context_fail_validation(self):
        path = self.root / "evaluation/fixtures/quickstart.json"
        path.unlink()
        path.symlink_to("../../go.mod")
        with self.assertRaises(context.ContextError):
            context.validate_context(self.root)
        path.unlink()
        directory = self.root / "unlisted"
        directory.symlink_to(self.root / "worker", target_is_directory=True)
        with self.assertRaises(context.ContextError):
            context.validate_context(self.root)

    def test_disposable_sentinels_are_removed_after_success_or_failure(self):
        for fail in (False, True):
            with self.subTest(fail=fail):
                try:
                    with context.private_sentinels(self.root):
                        self.assertTrue(all((self.root / name).is_file() for name in context.PRIVATE_SENTINELS))
                        if fail:
                            raise RuntimeError("synthetic export failure")
                except RuntimeError:
                    pass
                self.assertTrue(all(not (self.root / name).exists() for name in context.PRIVATE_SENTINELS))
                self.assertFalse((self.root / ".local").exists())
                self.assertEqual(context.validate_context(self.root), context.REQUIRED_FILES)

    def test_existing_sentinel_is_never_overwritten(self):
        path = self.root / context.PRIVATE_SENTINELS[-1]
        path.parent.mkdir(parents=True)
        path.write_text("preexisting local content")
        with self.assertRaises(context.ContextError):
            with context.private_sentinels(self.root):
                self.fail("must reject before exporting")
        self.assertEqual(path.read_text(), "preexisting local content")
        self.assertFalse((self.root / context.PRIVATE_SENTINELS[0]).exists())

    def test_sentinel_parent_cannot_redirect_writes_outside_checkout(self):
        with tempfile.TemporaryDirectory() as external:
            directory = self.root / "scripts"
            directory.symlink_to(external, target_is_directory=True)
            with self.assertRaises(context.ContextError):
                with context.private_sentinels(self.root):
                    self.fail("must reject a parent symlink")
            self.assertEqual(list(Path(external).iterdir()), [])


if __name__ == "__main__":
    unittest.main()
