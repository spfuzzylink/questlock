"""Detailed startup diagnostics expose fixed error codes, never their payloads."""

import importlib.util
import io
import json
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location(
    "langfuse_runner_diagnostics", Path(__file__).parent / "langfuse" / "run.py"
)
RUN = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RUN)


class DiagnosticCodesTest(unittest.TestCase):
    def test_prisma_codes_remain_individually_distinguishable_without_messages(self):
        for code in RUN.PRISMA_CODES:
            with self.subTest(code=code):
                logs = (
                    "Applying database migrations failed\nError: " + code
                    + "\nDATABASE_URL=postgresql://private-user:private-password@private-host/private-db"
                    + "\nMigration name: /private/path/private-migration.sql\n"
                )
                labels = RUN.classify_logs(logs)
                self.assertEqual(labels, [
                    "postgres_migration_failed", "prisma_migration_error", "prisma_" + code,
                ])
                encoded = json.dumps(labels)
                for marker in ("private-user", "private-password", "private-host", "private-db", "private-migration"):
                    self.assertNotIn(marker, encoded)

    def test_postgres_sqlstates_are_fixed_and_require_an_error_code_label(self):
        for code in RUN.POSTGRES_SQLSTATES:
            with self.subTest(code=code):
                for prefix in ("Database error code: ", "SQLSTATE=", 'sqlstate: "'):
                    labels = RUN.classify_logs(prefix + code + "\nprivate schema and row values")
                    self.assertEqual(labels, ["postgres_sqlstate_" + code])
                self.assertEqual(RUN.classify_logs("password=" + code), [])
                self.assertEqual(RUN.classify_logs("SQLSTATE: " + code + "private"), [])
        self.assertEqual(RUN.classify_logs("Error P9999; SQLSTATE: ABCDE; arbitrary private failure"), [])
        self.assertEqual(RUN.classify_logs("prefixP1001suffix"), [])

    def test_diagnose_preserves_stderr_failure_after_large_stdout_without_leaking_it(self):
        state = {
            "service": "langfuse-web", "status": "exited", "health": "unhealthy",
            "restart_count": 0, "oom": False, "exit_code": 1, "health_exit_codes": [],
        }
        replies = iter([
            SimpleNamespace(returncode=0, stdout="a" * 64 + "\n", stderr=""),
            SimpleNamespace(returncode=0, stdout=json.dumps(state), stderr=""),
            SimpleNamespace(returncode=0, stdout="private-progress\n" * 10000,
                            stderr="Error: P3018\nDatabase error code: 42P07\nprivate database details"),
        ])

        def inspect(argv, *, env, cwd, timeout, capture_output, text, check):
            self.assertTrue(capture_output)
            self.assertTrue(text)
            self.assertFalse(check)
            self.assertLessEqual(timeout, 10)
            self.assertEqual(env, {"DOCKER_HOST": "unix:///fixture/local.sock"})
            return next(replies)

        output = io.StringIO()
        with patch.object(RUN.subprocess, "run", side_effect=inspect), patch("sys.stdout", output):
            self.assertEqual(RUN.diagnose("/fixture/docker", "unix:///fixture/local.sock", [],
                                          {"DOCKER_HOST": "unix:///fixture/local.sock"}), 0)
        report = json.loads(output.getvalue())
        self.assertEqual(report["log_causes"], ["prisma_migration_error", "prisma_P3018", "postgres_sqlstate_42P07"])
        self.assertNotIn("private", output.getvalue())


if __name__ == "__main__":
    unittest.main()
