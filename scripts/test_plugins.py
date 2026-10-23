"""Offline plugin release boundaries and clean-profile verifier regressions."""

import copy
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import zipfile


ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("verify_plugins", Path(__file__).with_name("verify_plugins.py"))
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)
installer_spec = importlib.util.spec_from_file_location("plugin_installer_fixture", Path(__file__).with_name("test_install.py"))
installer_fixture = importlib.util.module_from_spec(installer_spec)
installer_spec.loader.exec_module(installer_fixture)


def read_json(name):
    return json.loads((ROOT / name).read_text())


class PluginReleaseTests(unittest.TestCase):
    def test_catalogs_resolve_to_matching_contained_plugins(self):
        codex = read_json(".agents/plugins/marketplace.json")
        claude = read_json(".claude-plugin/marketplace.json")
        self.assertEqual(codex["name"], "questlock")
        self.assertEqual(claude["name"], "questlock")
        self.assertTrue(claude["description"])
        for catalog, expected in ((codex, "./plugins/openai"), (claude, "./plugins/claude")):
            self.assertEqual(len(catalog["plugins"]), 1)
            entry = catalog["plugins"][0]
            self.assertEqual(entry["name"], "questlock")
            source = entry["source"]
            if isinstance(source, dict):
                self.assertEqual(source["source"], "local")
                source = source["path"]
            self.assertEqual(source, expected)
            path = ROOT / source
            self.assertTrue(path.is_dir() and not path.is_symlink())
        self.assertEqual(codex["plugins"][0]["policy"]["installation"], "AVAILABLE")

    def test_manifests_and_mcp_commands_are_portable_and_consistent(self):
        version = (ROOT / "VERSION").read_text().strip()
        portable = read_json("plugins/openai/plugin.json")
        claude = read_json("plugins/claude/.claude-plugin/plugin.json")
        for manifest in (portable, claude):
            self.assertEqual((manifest["name"], manifest["version"], manifest["license"]),
                             ("questlock", version, "MIT"))
            self.assertEqual(manifest["repository"], "https://github.com/spfuzzylink/questlock")
            self.assertTrue(manifest["description"])
            self.assertFalse(set(manifest) & {"hooks", "permissions", "settings"})
        extension = portable["extensions"]["com.openai"]
        self.assertTrue(extension["requires_local_executor"])
        self.assertEqual(extension["onboardingSkill"], "./skills/questlock/SKILL.md")
        for name in ("composerIcon", "logo"):
            path = extension["interface"][name]
            self.assertTrue(path.startswith("./assets/"))
            self.assertTrue((ROOT / "plugins/openai" / path).is_file())
        for folder, variable, client, filename in (
                ("openai", "PLUGIN_ROOT", "codex", "mcp.json"),
                ("claude", "CLAUDE_PLUGIN_ROOT", "claude-code", ".mcp.json")):
            plugin = ROOT / "plugins" / folder
            self.assertEqual((plugin / "VERSION").read_text().strip(), version)
            mcp = read_json(f"plugins/{folder}/{filename}")
            self.assertEqual(set(mcp["mcpServers"]), {"questlock"})
            self.assertEqual(mcp["mcpServers"]["questlock"], {
                "type": "stdio", "command": "sh",
                "args": ["${" + variable + "}/scripts/run.sh", "serve", client]})
            self.assertTrue((plugin / "scripts/run.sh").is_file())
            self.assertTrue((plugin / "README.md").is_file())

    def test_both_skills_keep_work_reviewed_and_fallback_disabled(self):
        skills = [(ROOT / "plugins" / name / "skills/questlock/SKILL.md").read_bytes()
                  for name in ("openai", "claude")]
        self.assertEqual(skills[0], skills[1])
        text = skills[0].decode()
        for required in ("first-task.json", "questlock_revise", "questlock_delegate",
                         "Paid fallback is disabled", "request_path", "untrusted",
                         "human acceptance", "model call"):
            self.assertIn(required, text)

    def test_catalog_copy_excludes_project_rules_and_host_state(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source, target = root / "source", root / "copy"
            for name in ("plugins/openai/README.md", "plugins/claude/README.md",
                         ".agents/plugins/marketplace.json", ".claude-plugin/marketplace.json",
                         "AGENTS.md", "CLAUDE.md", ".git/config", ".codex/auth.json",
                         ".claude/settings.json", ".questlock/private.token"):
                path = source / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("synthetic fixture")
            verify.copy_catalog(source, target)
            self.assertEqual({str(path.relative_to(target)) for path in target.rglob("*") if path.is_file()}, {
                "plugins/openai/README.md", "plugins/claude/README.md",
                ".agents/plugins/marketplace.json", ".claude-plugin/marketplace.json"})
            (source / "plugins/openai/private.token").symlink_to(source / "AGENTS.md")
            with self.assertRaisesRegex(ValueError, "regular files"):
                verify.copy_catalog(source, root / "symlink-copy")

    def test_profile_environment_never_imports_credentials_or_selectors(self):
        with tempfile.TemporaryDirectory() as temp, patch.dict(os.environ, {
                "OPENAI_API_KEY": "synthetic-secret", "ANTHROPIC_API_KEY": "synthetic-secret",
                "CODEX_HOME": "/must-not-import", "CLAUDE_CONFIG_DIR": "/must-not-import",
                "QUESTLOCK_TOKEN": "synthetic-secret", "HTTP_PROXY": "http://invalid.test"}):
            env = verify.isolated_env(Path(temp))
            self.assertEqual(env["CODEX_HOME"], str(Path(temp).resolve() / ".codex"))
            self.assertEqual(env["CLAUDE_CONFIG_DIR"], str(Path(temp).resolve() / ".claude"))
            self.assertTrue(Path(env["CODEX_HOME"]).is_dir())
            for key in ("OPENAI_API_KEY", "ANTHROPIC_API_KEY", "QUESTLOCK_TOKEN", "HTTP_PROXY"):
                self.assertNotIn(key, env)
            self.assertEqual(env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"], "1")

    def test_archive_refuses_escape_duplicate_symlink_and_ambiguous_paths(self):
        for names, symlink in ((["../escape"], False), (["/absolute"], False),
                               (["C:/windows"], False), (["bad\\path"], False),
                               (["same", "same"], False), (["a/./b"], False),
                               (["a//b"], False), (["symlink"], True)):
            with self.subTest(names=names), tempfile.TemporaryDirectory() as temp:
                archive = io.BytesIO()
                with zipfile.ZipFile(archive, "w") as bundle:
                    for name in names:
                        entry = zipfile.ZipInfo(name)
                        entry.external_attr = ((stat.S_IFLNK | 0o777) if symlink
                                               else (stat.S_IFREG | 0o644)) << 16
                        with self.assertWarns(UserWarning) if name == "same" and len(bundle.infolist()) else contextlib.nullcontext():
                            bundle.writestr(entry, b"synthetic fixture")
                archive.seek(0)
                with self.assertRaises(ValueError):
                    verify.safe_extract(archive, Path(temp) / "out")
                self.assertFalse((Path(temp) / "out").exists())

    def test_archive_preserves_executable_runtime_without_host_metadata(self):
        archive = io.BytesIO()
        with zipfile.ZipFile(archive, "w") as bundle:
            for name, mode in (("plugins/openai/bin/questlock", 0o755), ("LICENSE", 0o644)):
                entry = zipfile.ZipInfo(name)
                entry.external_attr = (stat.S_IFREG | mode) << 16
                bundle.writestr(entry, b"synthetic fixture")
        archive.seek(0)
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            verify.safe_extract(archive, root)
            self.assertEqual((root / "plugins/openai/bin/questlock").stat().st_mode & 0o777, 0o700)
            self.assertEqual((root / "LICENSE").stat().st_mode & 0o777, 0o600)

    def test_zero_generation_evidence_rejects_usage_or_failed_checks(self):
        digest = hashlib.sha256(b'{"status":"ready","model_calls":0}').hexdigest()
        valid = {"status": "completed", "durable": True, "generation_called": False,
                 "text": '{"status":"ready","model_calls":0}', "provider_cost_usd": 0,
                 "output_sha256": digest,
                 "artifact": {"key": "fixture", "version": 1, "sha256": digest},
                 "this_run_usage": {"input_tokens": 0, "output_tokens": 0},
                 "source_checks": [{"passed": True}], "checks": [{"passed": True}],
                 "outcome_tracking": {"status": "recorded"}}
        self.assertEqual(verify.checked_result({"structuredContent": valid}), valid)
        for change in ({"generation_called": True}, {"provider_cost_usd": 0.01},
                       {"this_run_usage": {"input_tokens": 1, "output_tokens": 0}},
                       {"this_run_usage": {}},
                       {"checks": [{"passed": False}]}, {"outcome_tracking": {"status": "pending"}},
                       {"output_sha256": "incorrect"},
                       {"artifact": None}, {"status": "quality_failed"}):
            result = copy.deepcopy(valid)
            result.update(change)
            with self.subTest(change=change), self.assertRaises(ValueError):
                verify.checked_result({"structuredContent": result})


class PluginBootstrapTests(unittest.TestCase):
    def setUp(self):
        self.fixture = installer_fixture.InstallTests()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.root = self.fixture.root.resolve()
        self.home = self.root / "private home with spaces"
        self.home.mkdir(mode=0o700)
        self.plugin = self.root / "plugin with spaces"
        scripts = self.plugin / "scripts"
        scripts.mkdir(parents=True)
        for name in ("run.sh", "install.sh"):
            source = ROOT / "plugins/openai/scripts" / name
            (scripts / name).write_bytes(source.read_bytes())
        self.version = (ROOT / "VERSION").read_text().strip()
        self.args_log = self.root / "binary-args.jsonl"
        self.runtime = ("#!/usr/bin/env python3\nimport json,os,sys\n"
                        "if sys.argv[1:] == ['version']:\n"
                        " print(" + repr(self.version) + ");sys.exit(0)\n"
                        "with open(os.environ['QUESTLOCK_BINARY_ARGS'],'a') as output:\n"
                        " output.write(json.dumps(sys.argv[1:])+'\\n')\n").encode()
        self.fixture.binary = self.runtime
        platform = "darwin" if sys.platform == "darwin" else "linux"
        machine = "arm64" if os.uname().machine in ("arm64", "aarch64") else "amd64"
        self.fixture.env = {
            "PATH": str(self.fixture.root / "mock tools") + os.pathsep + os.defpath,
            "HOME": str(self.home), "QUESTLOCK_BINARY_ARGS": str(self.args_log),
            "QUESTLOCK_TEST_OS": "Darwin" if platform == "darwin" else "Linux",
            "QUESTLOCK_TEST_ARCH": "arm64" if machine == "arm64" else "x86_64",
            "QUESTLOCK_FIXTURES": str(self.fixture.fixtures),
            "QUESTLOCK_CURL_LOG": str(self.fixture.log),
        }
        self.asset = self.fixture.make_release(self.version, platform, machine)
        self.cache = self.home / ".local/share/questlock/plugins/runtime" / self.version / (platform + "_" + machine)

    def run_launcher(self, *args):
        return subprocess.run(["/bin/sh", str(self.plugin / "scripts/run.sh"), *args],
                              cwd=self.root, env=self.fixture.env, capture_output=True,
                              text=True, timeout=15)

    def bundle_runtime(self, content=None):
        binary = self.plugin / "bin/questlock"
        binary.parent.mkdir()
        binary.write_bytes(content if content is not None else self.runtime)
        binary.chmod(0o700)
        return binary

    def test_bundled_runtime_uses_literal_arguments_and_no_download(self):
        self.bundle_runtime()
        self.fixture.env.update({"QUESTLOCK_PLUGIN_STATE_DIR": str(self.home / "state ; literal"),
                                 "QUESTLOCK_PLUGIN_WORKSPACE": str(self.home / "requests $(literal)")})
        result = self.run_launcher("serve", "codex")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(self.args_log.read_text()), [
            "plugin", "serve", "--client", "codex", "--state-dir",
            str(self.home / "state ; literal"), "--workspace", str(self.home / "requests $(literal)")])
        self.assertFalse(self.fixture.log.exists())
        self.assertFalse(self.cache.exists())

    def test_missing_runtime_doctor_is_read_only_and_never_downloads(self):
        result = self.run_launcher("doctor", "codex")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("runtime is not installed", result.stderr)
        self.assertFalse(self.fixture.log.exists())
        self.assertEqual(list(self.home.iterdir()), [])

    def test_checked_bootstrap_is_pinned_private_and_reused(self):
        first = self.run_launcher("try", "claude-code")
        self.assertEqual(first.returncode, 0, first.stderr)
        cached = self.cache / "questlock"
        self.assertEqual(cached.read_bytes(), self.runtime)
        calls = [json.loads(line) for line in self.fixture.log.read_text().splitlines()]
        self.assertEqual(len(calls), 2)
        self.assertTrue(all("/v" + self.version + "/" in call[-1] for call in calls))
        self.assertFalse(list(self.cache.glob(".bootstrap.*")))
        self.assertFalse((self.cache / "bootstrap.lock").exists())
        second = self.run_launcher("serve", "codex")
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(len(self.fixture.log.read_text().splitlines()), 2)
        self.assertFalse((self.home / ".codex").exists())
        self.assertFalse((self.home / ".claude").exists())
        self.assertFalse((self.home / ".local/bin").exists())

    def test_wrong_version_and_symlink_runtime_are_preserved(self):
        binary = self.bundle_runtime(b"#!/bin/sh\nprintf '0.0.0\\n'\n")
        before = binary.read_bytes()
        result = self.run_launcher("serve", "codex")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("version does not match", result.stderr)
        self.assertEqual(binary.read_bytes(), before)
        binary.unlink()
        external = self.root / "existing-runtime"
        external.write_bytes(self.runtime)
        external.chmod(0o700)
        binary.symlink_to(external)
        result = self.run_launcher("serve", "codex")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(external.read_bytes(), self.runtime)
        self.assertFalse(self.fixture.log.exists())

    def test_corrupt_download_is_never_published_and_staging_is_removed(self):
        (self.fixture.fixtures / "checksums.txt").write_text("0" * 64 + "  " + self.asset + "\n")
        result = self.run_launcher("try", "codex")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("SHA256 mismatch", result.stderr)
        self.assertFalse((self.cache / "questlock").exists())
        self.assertFalse((self.cache / "bootstrap.lock").exists())
        self.assertFalse(list(self.cache.glob(".bootstrap.*")))
        self.assertFalse(self.args_log.exists())

    def test_symlink_cache_is_refused_before_download(self):
        outside = self.root / "outside"
        outside.mkdir()
        (self.home / ".local").symlink_to(outside, target_is_directory=True)
        result = self.run_launcher("try", "codex")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("symlink", result.stderr)
        self.assertFalse(self.fixture.log.exists())
        self.assertEqual(list(outside.iterdir()), [])

    def test_concurrent_bootstrap_publishes_once_and_reuses_verified_runtime(self):
        command = ["/bin/sh", str(self.plugin / "scripts/run.sh"), "try", "codex"]
        processes = [subprocess.Popen(command, cwd=self.root, env=self.fixture.env,
                                      stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
                     for _ in range(2)]
        try:
            for process in processes:
                stdout, stderr = process.communicate(timeout=15)
                self.assertEqual(process.returncode, 0, stdout + stderr)
        finally:
            for process in processes:
                if process.poll() is None:
                    process.kill()
                    process.communicate()
        self.assertEqual((self.cache / "questlock").read_bytes(), self.runtime)
        self.assertEqual(len(self.fixture.log.read_text().splitlines()), 2)
        self.assertEqual(len(self.args_log.read_text().splitlines()), 2)
        self.assertFalse((self.cache / "bootstrap.lock").exists())
        self.assertFalse(list(self.cache.glob(".bootstrap.*")))

    def test_bad_arguments_fail_before_any_runtime_or_download(self):
        for args in (("serve", "codex", "--allow-paid"), ("serve", "codex;touch unexpected"),
                     ("--allow-paid", "codex"), ("serve",)):
            result = self.run_launcher(*args)
            self.assertNotEqual(result.returncode, 0, args)
        self.assertFalse(self.fixture.log.exists())
        self.assertFalse(self.args_log.exists())
        self.assertEqual(list(self.home.iterdir()), [])


if __name__ == "__main__":
    unittest.main()
