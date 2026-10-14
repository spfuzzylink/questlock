#!/usr/bin/env python3
"""Ensure disposable checks cannot consume ambient credentials or targets."""

import importlib.util
import io
import json
import os
from pathlib import Path
import socket
import stat
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch


def load(name):
    spec = importlib.util.spec_from_file_location(
        "langfuse_" + name, Path(__file__).parent / "langfuse" / (name + ".py")
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


PREPARE, RUN = load("prepare"), load("run")


class IsolationTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.env_file = self.root / "validation.env"
        with patch.dict(os.environ, {"GITHUB_ACTIONS": "false"}):
            PREPARE.prepare(self.env_file)
        self.generated = RUN.read_environment(self.env_file)

    def test_hostile_ambient_environment_is_not_forwarded(self):
        hostile = {
            "DATABASE_URL": "postgresql://production.example.com/private",
            "CLICKHOUSE_URL": "https://production.example.com/private",
            "LANGFUSE_BASE_URL": "https://production.example.com",
            "LANGFUSE_PUBLIC_KEY": "ambient-public-fixture",
            "LANGFUSE_SECRET_KEY": "ambient-secret-fixture",
            "LANGFUSE_INIT_PROJECT_ID": "ambient-production-fixture",
            "LANGFUSE_AI_API_KEY": "ambient-model-fixture",
            "OPENAI_API_KEY": "ambient-model-fixture",
            "ANTHROPIC_API_KEY": "ambient-model-fixture",
            "DOCKER_HOST": "tcp://production.example.com:2376",
            "DOCKER_CONTEXT": "production-fixture",
            "DOCKER_CONFIG": "/ambient-docker-config-fixture",
            "COMPOSE_FILE": "/ambient-compose-fixture",
            "COMPOSE_PROJECT_NAME": "production-fixture",
            "GOFLAGS": "-toolexec=ambient-fixture",
            "GOPROXY": "https://production.example.com/private",
            "GITHUB_TOKEN": "ambient-token-fixture",
            "HTTP_PROXY": "http://production.example.com:8080",
            "HTTPS_PROXY": "http://production.example.com:8080",
            "HOME": "/ambient-home-fixture",
        }

        def inspect(argv, *, env, cwd, timeout, check):
            self.assertEqual(cwd, RUN.ROOT)
            self.assertFalse(check)
            self.assertGreater(timeout, 0)
            for key in hostile:
                if key in env:
                    self.assertNotEqual(env[key], hostile[key])
            private_root = Path(env["HOME"])
            self.assertEqual(stat.S_IMODE(private_root.stat().st_mode), 0o700)
            self.assertEqual(list((private_root / "docker").iterdir()), [])
            if argv[0] == "/fixture/go":
                self.assertEqual(argv[1:], ["run", "./scripts/langfuse-smoke"])
                self.assertEqual(env["LANGFUSE_SECRET_KEY"], self.generated["LANGFUSE_SECRET_KEY"])
                self.assertEqual(env["LANGFUSE_BASE_URL"], "http://127.0.0.1:3000")
                self.assertNotIn("DATABASE_URL", env)
                self.assertNotIn("DOCKER_CONFIG", env)
                self.assertEqual(env["GOENV"], "off")
                self.assertEqual(env["GOTOOLCHAIN"], "local")
            else:
                self.assertEqual(argv[:4], ["/fixture/docker", "--host", "unix:///fixture/local.sock", "compose"])
                self.assertEqual(env["DOCKER_HOST"], "unix:///fixture/local.sock")
                self.assertEqual(env["DATABASE_URL"], self.generated["DATABASE_URL"])
                self.assertEqual(env["COMPOSE_PROJECT_NAME"], self.generated["LANGFUSE_INIT_PROJECT_ID"])
                self.assertEqual(env["DOCKER_CONFIG"], str(private_root / "docker"))
                self.assertNotIn("DOCKER_CONTEXT", env)
            return SimpleNamespace(returncode=0)

        with (patch.dict(os.environ, hostile, clear=True),
              patch.object(RUN.shutil, "which", side_effect=lambda name: "/fixture/" + name),
              patch.object(RUN, "local_socket", return_value="unix:///fixture/local.sock"),
              patch.object(RUN.subprocess, "run", side_effect=inspect)):
            for operation in ["config", "up", "ps", "down", "smoke"]:
                self.assertEqual(RUN.execute(self.env_file, operation), 0)

    def test_environment_permissions_and_symlink_are_rejected(self):
        self.env_file.chmod(0o644)
        with self.assertRaises(RUN.IsolationError):
            RUN.read_environment(self.env_file)
        self.env_file.chmod(0o600)
        link = self.root / "link.env"
        link.symlink_to(self.env_file)
        with self.assertRaises(RUN.IsolationError):
            RUN.read_environment(link)

    def test_tampered_target_and_extra_field_are_rejected(self):
        original = self.env_file.read_text()
        for content in [
            original.replace("@postgres:5432/questlock", "@production.example.com:5432/questlock"),
            original.replace("http://127.0.0.1:3000", "https://production.example.com"),
            original + "CLICKHOUSE_URL=https://production.example.com\n",
            original + "LANGFUSE_SECRET_KEY=duplicate-fixture\n",
        ]:
            self.env_file.write_text(content)
            with self.assertRaises(RUN.IsolationError):
                RUN.read_environment(self.env_file)

    def test_only_real_local_unix_socket_is_accepted(self):
        socket_path = self.root / "docker.sock"
        with socket.socket(socket.AF_UNIX) as local:
            local.bind(str(socket_path))
            self.assertEqual(RUN.local_socket("unix://" + str(socket_path)), "unix://" + str(socket_path))
        for target in ["tcp://production.example.com:2376", "unix://production.example.com/socket", "unix:///nonexistent-questlock-fixture.sock", "unix:///tmp/../production.sock", "unix:///tmp/socket?host=production"]:
            with self.assertRaises(RUN.IsolationError):
                RUN.local_socket(target)
        ordinary = self.root / "ordinary-file"
        ordinary.touch()
        with self.assertRaises(RUN.IsolationError):
            RUN.local_socket("unix://" + str(ordinary))

    def test_diagnostics_emit_only_allowlisted_state_and_fixed_causes(self):
        state = {
            "service": "langfuse-worker", "status": "running", "health": "unhealthy",
            "restart_count": 0, "oom": False, "exit_code": 0,
            "health_exit_codes": [1, 1],
            "private_env_fixture": "diagnostic-secret-fixture",
        }
        logs = "ECONNREFUSED; DATABASE_URL=diagnostic-secret-fixture; arbitrary secret content"
        summary = RUN.diagnostic_summary(state, logs)
        self.assertEqual(summary["log_causes"], ["connection_refused"])
        self.assertEqual(summary["last_health_exit_code"], 1)
        self.assertNotIn("diagnostic-secret-fixture", json.dumps(summary))
        self.assertNotIn("arbitrary secret content", json.dumps(summary))
        self.assertNotIn("private_env_fixture", summary)

    def test_diagnostic_subprocesses_are_isolated_and_raw_output_is_captured(self):
        state = {
            "service": "langfuse-web", "status": "running", "health": "unhealthy",
            "restart_count": 0, "oom": False, "exit_code": 0, "health_exit_codes": [1],
        }
        replies = iter([
            SimpleNamespace(returncode=0, stdout="a" * 64 + "\n", stderr=""),
            SimpleNamespace(returncode=0, stdout=json.dumps(state), stderr=""),
            SimpleNamespace(returncode=0, stdout="Ready in 100ms; diagnostic-secret-fixture", stderr="diagnostic-secret-fixture"),
        ])

        def inspect(argv, *, env, cwd, timeout, capture_output, text, check):
            self.assertTrue(capture_output)
            self.assertTrue(text)
            self.assertFalse(check)
            self.assertEqual(argv[:3], ["/fixture/docker", "--host", "unix:///fixture/local.sock"])
            self.assertEqual(env["DOCKER_HOST"], "unix:///fixture/local.sock")
            self.assertNotIn("DOCKER_CONTEXT", env)
            self.assertNotIn("LANGFUSE_AI_API_KEY", env)
            return next(replies)

        output = io.StringIO()
        with (patch.dict(os.environ, {"DOCKER_CONTEXT": "hostile-fixture", "LANGFUSE_AI_API_KEY": "diagnostic-secret-fixture"}, clear=True),
              patch.object(RUN.shutil, "which", return_value="/fixture/docker"),
              patch.object(RUN, "local_socket", return_value="unix:///fixture/local.sock"),
              patch.object(RUN.subprocess, "run", side_effect=inspect),
              patch("sys.stdout", output)):
            self.assertEqual(RUN.execute(self.env_file, "diagnose"), 0)
        self.assertNotIn("diagnostic-secret-fixture", output.getvalue())
        self.assertEqual(json.loads(output.getvalue())["log_causes"], ["web_ready"])


if __name__ == "__main__":
    unittest.main()
