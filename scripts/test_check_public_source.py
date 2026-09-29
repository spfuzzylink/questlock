"""Regression tests use only disposable Git repositories and synthetic data."""

import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("check-public-source.py").resolve()
SPEC = importlib.util.spec_from_file_location("public_source_guard", SCRIPT)
GUARD = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(GUARD)


class PatternTests(unittest.TestCase):
    def test_known_credentials_and_private_keys(self):
        fixtures = [
            "github_" + "pat_" + "a1" * 30,
            "ghp" + "_" + "a" * 36,
            "af" + "_" + "b" * 43,
            "ql" + "_" + "c" * 43,
            "AKIA" + "A" * 16,
            "sk" + "-proj-" + "A" * 40,
            "xoxb" + "-" + "1234567890123456",
            "glpat" + "-" + "d" * 30,
            "-----BEGIN " + "OPENSSH PRIVATE KEY-----",
            "SQLite format 3" + chr(0),
        ]
        for fixture in fixtures:
            with self.subTest(prefix=fixture[:4]):
                self.assertTrue(GUARD.content_issues(fixture.encode()))

    def test_home_paths_and_email_patterns(self):
        for value in [
            "/" + "Users" + "/fictional/repo",
            "/" + "home" + "/fictional/repo",
            "C:" + "\\Users\\fictional\\repo",
            "fictional.person" + "@" + "private.invalid",
        ]:
            self.assertTrue(GUARD.content_issues(value.encode()))
        self.assertFalse(GUARD.content_issues(b"$HOME/project public-builder@users.noreply.github.com"))
        self.assertFalse(GUARD.content_issues(b"contact@example.com git@github.com"))

    def test_guard_and_tests_do_not_contain_matching_fixture_literals(self):
        for path in (SCRIPT, Path(__file__)):
            self.assertEqual(GUARD.content_issues(path.read_bytes()), set())


class RepositoryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="questlock-public-guard-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.env = dict(os.environ)
        self.env.update({"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull})
        self.env.pop("QUESTLOCK_FORBIDDEN_STRINGS", None)
        for key in ("GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"):
            self.env.pop(key, None)
        self.git("init", "--initial-branch=main")
        self.git("config", "user.name", "public-builder")
        self.git("config", "user.email", "public-builder@users.noreply.github.com")
        self.write("README.md", "A public experiment.\n")
        self.git("add", "README.md")
        self.git("commit", "-m", "Initial source")

    def git(self, *args, input=None):
        return subprocess.run(
            ["git", *args], cwd=self.repo, env=self.env, input=input,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True,
        ).stdout

    def write(self, relative, text):
        path = self.repo / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def check(self, expected=0, forbidden=None):
        env = dict(self.env)
        if forbidden is not None:
            env["QUESTLOCK_FORBIDDEN_STRINGS"] = json.dumps(forbidden)
        result = subprocess.run(
            [sys.executable, str(SCRIPT)], cwd=self.repo, env=env,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )
        self.assertEqual(result.returncode, expected, result.stdout + result.stderr)
        return result.stdout + result.stderr

    def test_clean_and_untracked_files_are_out_of_scope(self):
        self.write("local.token", "af" + "_" + "q" * 43)
        output = self.check()
        self.assertIn("1 tracked paths", output)
        self.assertNotIn("local.token", output)

    def test_current_working_changes_are_checked(self):
        secret = "ghp" + "_" + "A" * 36
        self.write("README.md", secret)
        output = self.check(1)
        self.assertIn("working tree", output)
        self.assertNotIn(secret, output)

    def test_staged_content_cannot_be_hidden_by_clean_working_file(self):
        secret = "github_" + "pat_" + "B" * 50
        self.write("README.md", secret)
        self.git("add", "README.md")
        self.write("README.md", "Clean working copy")
        output = self.check(1)
        self.assertIn("index", output)
        self.assertNotIn(secret, output)

    def test_deleted_historical_secret_is_detected(self):
        secret = "ql" + "_" + "C" * 43
        self.write("old.txt", secret)
        self.git("add", "old.txt")
        self.git("commit", "-m", "Add fixture")
        self.git("rm", "old.txt")
        self.git("commit", "-m", "Remove fixture")
        output = self.check(1)
        self.assertIn("history", output)
        self.assertNotIn(secret, output)

    def test_other_branch_history_is_detected(self):
        self.git("checkout", "-b", "side-branch")
        self.write("only-on-side.txt", "af" + "_" + "D" * 43)
        self.git("add", "only-on-side.txt")
        self.git("commit", "-m", "Side fixture")
        self.git("checkout", "main")
        self.assertIn("only-on-side.txt", self.check(1))

    def test_replace_refs_cannot_hide_original_history(self):
        self.write("original.txt", "af" + "_" + "R" * 43)
        self.git("add", "original.txt")
        self.git("commit", "-m", "Original fixture")
        self.git("replace", "HEAD", "HEAD~1")
        output = self.check(1)
        self.assertIn("original.txt", output)
        self.assertIn("history", output)

    def test_sensitive_filenames_are_rejected_even_with_empty_content(self):
        for filename in ("credential.pem", "worker.token", ".env.production", "local.db", "events.log", "scripts/worker.token", "scripts/credentials/account.json"):
            self.write(filename, "")
            self.git("add", filename)
        output = self.check(1)
        self.assertIn("credential, database, or log file", output)
        self.assertIn("scripts/worker.token", output)
        self.assertIn("scripts/credentials/account.json", output)

    def test_ignored_local_auth_files_are_not_inspected(self):
        self.write(".gitignore", "/.local/\n")
        self.git("add", ".gitignore")
        self.write(".local/auth.sh", "af" + "_" + "L" * 43)
        self.write(".local/scripts/credential-helper.py", "private local helper")
        self.git("check-ignore", ".local/auth.sh", ".local/scripts/credential-helper.py")
        output = self.check()
        self.assertNotIn(".local/auth.sh", output)
        self.assertNotIn("credential-helper.py", output)

    def test_force_added_local_auth_helpers_are_rejected_by_path(self):
        self.write(".gitignore", "/.local/\n")
        self.write(".locality/notes.txt", "Public file with a similar directory name")
        self.git("add", ".gitignore", ".locality/notes.txt")
        self.check()
        for filename in (".local/auth.sh", ".local/scripts/credential-helper.py", ".local/notes.txt"):
            self.write(filename, "No credential needs to be present to reject this path")
            self.git("add", "--force", filename)
        output = self.check(1)
        self.assertIn("credential or local-state path", output)
        self.assertIn(".local/auth.sh", output)
        self.assertIn(".local/scripts/credential-helper.py", output)
        self.assertIn(".local/notes.txt", output)
        self.assertNotIn(".locality/notes.txt", output)

    def test_deleted_local_auth_helper_remains_rejected_in_history(self):
        self.write(".gitignore", "/.local/\n")
        self.git("add", ".gitignore")
        self.write(".local/scripts/auth.sh", "Local authentication helper")
        self.git("add", "--force", ".local/scripts/auth.sh")
        self.git("commit", "-m", "Local helper fixture")
        self.git("rm", ".local/scripts/auth.sh")
        self.git("commit", "-m", "Remove local fixture")
        output = self.check(1)
        self.assertIn('".local/scripts/auth.sh" (history)', output)
        self.assertIn("credential or local-state path", output)

    def test_private_metadata_and_custom_strings_are_not_echoed(self):
        private = "Fictional Private Marker"
        self.git("commit", "--allow-empty", "-m", private)
        output = self.check(1, forbidden=[private.lower()])
        self.assertIn("commit", output)
        self.assertIn("configured private string", output)
        self.assertNotIn(private, output)

    def test_non_noreply_commit_email_is_rejected(self):
        email = "fictional.person" + "@" + "private.invalid"
        self.git("config", "user.email", email)
        self.git("commit", "--allow-empty", "-m", "Metadata fixture")
        output = self.check(1)
        self.assertIn("author/committer email", output)
        self.assertNotIn(email, output)

    def test_annotated_tag_metadata_is_checked(self):
        marker = "Private Tag Fixture"
        self.git("tag", "-a", "fixture", "-m", marker)
        output = self.check(1, forbidden=[marker])
        self.assertIn("tag", output)
        self.assertNotIn(marker, output)

    def test_lightweight_tag_to_blob_is_checked(self):
        secret = ("ghp" + "_" + "T" * 36).encode()
        oid = self.git("hash-object", "-w", "--stdin", input=secret).strip().decode()
        self.git("tag", "blob-fixture", oid)
        output = self.check(1)
        self.assertIn("tag target", output)
        self.assertNotIn(secret.decode(), output)

    def test_symlink_does_not_read_outside_checkout(self):
        (self.root / "outside.txt").write_text("af" + "_" + "S" * 43)
        (self.repo / "reference").symlink_to("../outside.txt")
        self.git("add", "reference")
        self.check()

    def test_changed_parent_symlink_cannot_escape_checkout(self):
        self.write("nested/file.txt", "public")
        self.git("add", "nested/file.txt")
        (self.repo / "nested/file.txt").unlink()
        (self.repo / "nested").rmdir()
        external = self.root / "outside"
        external.mkdir()
        (external / "file.txt").write_text("af" + "_" + "E" * 43)
        (self.repo / "nested").symlink_to("../outside", target_is_directory=True)
        self.assertIn("tracked path escapes checkout", self.check(1))

    def test_secret_filename_is_redacted(self):
        secret = "ghp" + "_" + "F" * 36
        self.write(secret, "public")
        self.git("add", secret)
        output = self.check(1)
        self.assertIn("<redacted path>", output)
        self.assertNotIn(secret, output)

    def test_shallow_history_is_rejected(self):
        head = self.git("rev-parse", "HEAD")
        (self.repo / ".git/shallow").write_bytes(head)
        self.assertIn("Shallow history", self.check(2))


if __name__ == "__main__":
    unittest.main()
