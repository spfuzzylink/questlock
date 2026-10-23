#!/usr/bin/env python3
"""Smoke-test a packaged Questlock binary with private, offline retention state.

Uses the built-in checked revision through MCP and a managed loopback broker.
No native coding client, model, provider credential or external service is used.
Only the final content-free summary is printed; fixtures and credentials stay
inside a temporary private profile that is removed after the broker stops.
"""

import argparse
import contextlib
import hashlib
import json
import os
from pathlib import Path
import selectors
import signal
import stat
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request


MAX_OUTPUT = 128 << 10
EXPECTED_TEXT = '{"status":"ready","model_calls":0}'


class SmokeFailure(Exception):
    """Messages are fixed public labels, never command output or private paths."""


def require(condition, message):
    if not condition:
        raise SmokeFailure(message)


def terminate(process):
    if process.poll() is not None:
        return
    with contextlib.suppress(ProcessLookupError):
        os.killpg(process.pid, signal.SIGTERM)
    try:
        process.wait(timeout=2)
    except subprocess.TimeoutExpired:
        with contextlib.suppress(ProcessLookupError):
            os.killpg(process.pid, signal.SIGKILL)
        process.wait(timeout=2)


class Smoke:
    def __init__(self, binary, root):
        self.binary, self.root = binary, root
        self.state, self.workspace = root / "state", root / "requests"
        self.cold, self.held = root / "cold", root / "held"
        self.home, self.tmp = root / "home", root / "tmp"
        for path in (self.home, self.tmp, self.held):
            path.mkdir(mode=0o700)
        self.db = self.state / "state.db"
        self.env = {"HOME": str(self.home), "USERPROFILE": str(self.home),
                    "TMPDIR": str(self.tmp), "PATH": "/usr/bin:/bin",
                    "NO_COLOR": "1", "DISABLE_TELEMETRY": "1"}
        self.deadline = time.monotonic() + 180

    def command(self, args, *, timeout=50, cleanup=False):
        """Drain both streams with strict byte and elapsed-time bounds."""
        remaining = timeout if cleanup else min(timeout, self.deadline - time.monotonic())
        require(remaining > 0, "storage smoke exceeded its overall deadline")
        end = time.monotonic() + remaining
        try:
            process = subprocess.Popen([str(self.binary), *map(str, args)],
                                       cwd=self.home, env=self.env,
                                       stdin=subprocess.DEVNULL,
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                       start_new_session=True)
        except OSError:
            raise SmokeFailure("could not launch the packaged binary") from None
        output = {"stdout": bytearray(), "stderr": bytearray()}
        try:
            with selectors.DefaultSelector() as streams:
                for name in output:
                    streams.register(getattr(process, name), selectors.EVENT_READ, name)
                while streams.get_map():
                    require(time.monotonic() < end, "packaged command exceeded its deadline")
                    for event, _ in streams.select(min(0.1, max(0, end - time.monotonic()))):
                        chunk = os.read(event.fileobj.fileno(), 16384)
                        if not chunk:
                            streams.unregister(event.fileobj)
                            continue
                        buffer = output[event.data]
                        require(len(buffer) + len(chunk) <= MAX_OUTPUT,
                                "packaged command exceeded its output bound")
                        buffer.extend(chunk)
            try:
                code = process.wait(timeout=max(0.01, end - time.monotonic()))
            except subprocess.TimeoutExpired:
                raise SmokeFailure("packaged command exceeded its deadline") from None
            return code, bytes(output["stdout"]), bytes(output["stderr"])
        finally:
            terminate(process)
            process.stdout.close()
            process.stderr.close()

    def json(self, args, label, **kwargs):
        code, output, _ = self.command(args, **kwargs)
        require(code == 0, label + " command failed")
        try:
            value = json.loads(output)
        except (ValueError, UnicodeError):
            raise SmokeFailure(label + " returned invalid JSON") from None
        require(isinstance(value, dict), label + " returned a non-object")
        return value

    def storage(self, action, *args):
        return self.json(["storage", action, "--db", self.db, "--timeout", "15s", *args],
                         "storage " + action, timeout=20)

    def stop(self, *, cleanup=False):
        if not (self.state / "broker.json").exists():
            return
        code, _, _ = self.command(["local", "stop", "--state-dir", self.state],
                                  timeout=10, cleanup=cleanup)
        require(code == 0, "managed broker cleanup failed")
        end = time.monotonic() + 5
        while (self.state / "broker.json").exists() and time.monotonic() < end:
            time.sleep(0.05)
        require(not (self.state / "broker.json").exists(),
                "managed broker did not finish stopping")

    def checked_try(self, *, reused):
        report = self.json(["try", "--state-dir", self.state, "--workspace", self.workspace],
                           "checked MCP revision")
        require(report.get("status") == "completed"
                and report.get("mcp_protocol_self_test") is True,
                "checked MCP revision did not complete")
        result = report.get("result", {})
        require(result.get("status") == "completed" and result.get("durable") is True
                and result.get("generation_called") is False
                and result.get("provider_cost_usd") == 0
                and result.get("text") == EXPECTED_TEXT
                and result.get("reused") is reused,
                "checked revision did not preserve zero-generation behavior")
        usage = result.get("this_run_usage", {})
        require(usage.get("input_tokens") == 0 and usage.get("output_tokens") == 0
                and all(value == 0 for value in usage.values()),
                "checked revision reported generation usage")
        digest = hashlib.sha256(EXPECTED_TEXT.encode()).hexdigest()
        artifact = result.get("artifact", {})
        require(artifact.get("version") == 1 and artifact.get("sha256") == digest
                and result.get("output_sha256") == digest
                and artifact.get("key") == "revision/v1/results/" + result.get("request_sha256", ""),
                "checked revision identity changed")
        checks = result.get("source_checks", []) + result.get("checks", [])
        require(checks and all(check.get("passed") is True for check in checks)
                and result.get("outcome_tracking", {}).get("status") == "recorded",
                "checked revision evidence or outcome receipt is missing")
        return result

    def history(self, key):
        return self.storage("history", "--scope", "local-onboarding", "--key", key,
                            "--version", "1")

    def read_http(self, key):
        """Read only the managed fixture's literal loopback origin, without proxies."""
        try:
            connection = json.loads((self.state / "broker.json").read_bytes())
            origin = urllib.parse.urlsplit(connection["url"])
            require(origin.scheme == "http" and origin.hostname == "127.0.0.1"
                    and origin.port and not origin.username and not origin.password
                    and origin.path == "" and not origin.query and not origin.fragment,
                    "managed broker reported an unexpected origin")
            token = (self.state / "worker.token").read_text().strip()
            url = origin.geturl() + "/v1/artifact?" + urllib.parse.urlencode({"key": key})
            request = urllib.request.Request(url, headers={"Authorization": "Bearer " + token})
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
            try:
                response = opener.open(request, timeout=5)
            except urllib.error.HTTPError as response_error:
                response = response_error
            with response:
                body = response.read(MAX_OUTPUT + 1)
                require(len(body) <= MAX_OUTPUT, "loopback response exceeded its bound")
                return response.code, json.loads(body)
        except SmokeFailure:
            raise
        except (OSError, ValueError, KeyError, urllib.error.URLError):
            raise SmokeFailure("managed loopback read failed") from None

    def run(self):
        first = self.checked_try(reused=False)
        key = first["artifact"]["key"]
        require(self.checked_try(reused=True)["artifact"] == first["artifact"],
                "warm exact reuse changed the artifact")
        self.stop()
        original = self.history(key)
        require(json.loads(original["content"])["text"] == EXPECTED_TEXT,
                "stored revision bytes differ from the checked output")

        policy = self.json(["storage", "config", "--cold-dir", self.cold], "policy template")
        require(policy.get("hot_history_days") == 90 and policy.get("adaptive") is True,
                "policy template defaults changed")
        policy.update(hot_history_days=0, adaptive=False, keep_recent_versions=1)
        policy_path = self.root / "policy.json"
        policy_path.write_text(json.dumps(policy) + "\n")
        policy_path.chmod(0o600)
        require(self.storage("configure", "--config", policy_path).get("status") == "complete",
                "retention policy was not persisted")
        before = self.storage("info")
        dry = self.storage("archive")
        after_dry = self.storage("info")
        require(dry.get("dry_run") is True and dry.get("status") == "dry_run"
                and dry.get("items", 0) >= 1
                and before["archived_versions"] == after_dry["archived_versions"] == 0
                and before["hot_payload_bytes"] == after_dry["hot_payload_bytes"]
                and self.history(key) == original,
                "default archive changed hot content")
        applied = self.storage("archive", "--apply")
        archived = self.storage("info")
        require(applied.get("dry_run") is False and applied.get("items", 0) >= 1
                and archived["archived_versions"] == applied["items"]
                and archived["hot_payload_bytes"] < before["hot_payload_bytes"]
                and archived["versions"] == before["versions"]
                and archived["current_artifacts"] == before["current_artifacts"]
                and self.history(key) == original,
                "explicit archive did not preserve content and identities")
        require(self.storage("verify").get("status") == "complete",
                "archived payload verification failed")
        blob = self.cold / (hashlib.sha256(original["content"].encode()).hexdigest() + ".gzip")
        require(blob.is_file() and stat.S_IMODE(blob.stat().st_mode) == 0o600
                and stat.S_IMODE(self.cold.stat().st_mode) == 0o700,
                "cold payload or private permissions are missing")
        repeat = self.checked_try(reused=True)
        require(repeat["artifact"] == first["artifact"], "cold exact reuse changed the artifact")
        status, content = self.read_http(key)
        require(status == 200 and content == original, "cold HTTP read changed original bytes")

        held = self.held / blob.name
        blob.replace(held)
        try:
            status, error = self.read_http(key)
            require(status == 503 and error.get("code") == "archive_unavailable",
                    "missing cold payload did not fail closed with HTTP 503")
            code, output, diagnostics = self.command(
                ["try", "--state-dir", self.state, "--workspace", self.workspace])
            require(code != 0 and not output
                    and b"no model fallback was attempted" in diagnostics,
                    "missing cold payload permitted a successful task or fallback")
        finally:
            held.replace(blob)
        recovered = self.checked_try(reused=True)
        require(recovered["artifact"] == first["artifact"]
                and recovered["text"] == first["text"],
                "recovered cold payload did not reuse the original result")
        self.stop()
        require(self.history(key) == original and self.storage("verify").get("status") == "complete",
                "recovered storage failed verification")

        restored = 0
        for _ in range(64):
            if self.storage("info")["archived_versions"] == 0:
                break
            batch = self.storage("restore", "--max-items", "1")
            require(batch.get("status") == "complete" and batch.get("items") == 1,
                    "bounded restore did not make progress")
            restored += 1
        require(self.storage("info")["archived_versions"] == 0
                and restored == applied["items"] and blob.is_file()
                and self.history(key) == original,
                "restore lost content or deleted the immutable cold file")
        require(self.storage("compact").get("status") == "complete"
                and self.storage("verify").get("status") == "complete",
                "explicit compaction or final verification failed")
        require(self.checked_try(reused=True)["artifact"] == first["artifact"],
                "restored restart changed exact reuse")
        self.stop()
        return {"status": "completed", "evidence": "packaged_binary_private_loopback_mcp_storage",
                "model_calls": 0, "provider_cost_usd": 0, "archived_versions": applied["items"],
                "restored_versions": restored, "default_dry_run": True,
                "restart_exact_reuse": True, "history_bytes_preserved": True,
                "missing_archive_http_status": 503, "missing_archive_stops_task": True,
                "immutable_cold_file_retained": True, "explicit_compaction": True}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *_):
        return None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True,
                        help="absolute path to the packaged native Questlock executable")
    args = parser.parse_args()
    require(args.binary.is_absolute(), "--binary must be an absolute path")
    try:
        binary = args.binary.resolve(strict=True)
    except OSError:
        raise SmokeFailure("packaged binary is unavailable") from None
    require(binary.is_file() and os.access(binary, os.X_OK), "packaged binary is not executable")
    old_umask = os.umask(0o077)
    try:
        with tempfile.TemporaryDirectory(prefix="questlock-storage-check-") as directory:
            root = Path(directory).resolve(strict=True)
            root.chmod(0o700)
            smoke = Smoke(binary, root)
            try:
                report = smoke.run()
            finally:
                smoke.stop(cleanup=True)
    finally:
        os.umask(old_umask)
    print(json.dumps(report, sort_keys=True))


if __name__ == "__main__":
    try:
        main()
    except SmokeFailure as error:
        print(json.dumps({"status": "failed", "error": str(error)}, sort_keys=True))
        sys.exit(1)
    except Exception:
        print(json.dumps({"status": "failed", "error": "unexpected storage smoke failure"},
                         sort_keys=True))
        sys.exit(1)
