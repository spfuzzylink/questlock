#!/usr/bin/env python3
"""Verify packaged plugins in private profiles, without inference or credentials.

Native clients are explicit optional inputs. This script never installs a client
or downloads a runtime; source checks can inject a built executable with
--runtime-bin. Native inventory and direct MCP execution are separate evidence.
"""

import argparse
import contextlib
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import queue
import shutil
import signal
import stat
import subprocess
import tempfile
import threading
import time
import zipfile


ROOT = Path(__file__).resolve().parents[1]
CLIENTS = {"codex": "openai", "claude-code": "claude"}
TOOLS = {"questlock_status", "questlock_revise", "questlock_delegate"}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def isolated_env(home):
    """Construct, rather than filter, the environment; import no host selectors."""
    home = Path(home).resolve()
    for name in (".codex", ".claude", ".config", "tmp"):
        (home / name).mkdir(parents=True, mode=0o700, exist_ok=True)
    return {
        "HOME": str(home), "USERPROFILE": str(home),
        "CODEX_HOME": str(home / ".codex"),
        "CLAUDE_CONFIG_DIR": str(home / ".claude"),
        "XDG_CONFIG_HOME": str(home / ".config"),
        "TMPDIR": str(home / "tmp"), "PATH": "/usr/bin:/bin",
        "NO_COLOR": "1", "DISABLE_TELEMETRY": "1",
        "DISABLE_ERROR_REPORTING": "1", "DISABLE_AUTOUPDATER": "1",
        "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
    }


def safe_extract(archive, destination):
    """Extract only bounded regular files, with no ambiguous or escaping paths."""
    destination = Path(destination)
    with zipfile.ZipFile(archive) as bundle:
        seen, total = set(), 0
        entries = bundle.infolist()
        require(len(entries) <= 2000, "plugin archive has too many entries")
        for entry in entries:
            name = PurePosixPath(entry.filename)
            require(entry.filename and "\\" not in entry.filename
                    and not name.is_absolute()
                    and all(part not in ("", ".", "..") for part in name.parts)
                    and str(name) == entry.filename.rstrip("/")
                    and ":" not in name.parts[0], "unsafe plugin archive path")
            require(str(name) not in seen, "duplicate plugin archive path")
            seen.add(str(name))
            mode = entry.external_attr >> 16
            require(not stat.S_ISLNK(mode)
                    and (stat.S_IFMT(mode) in (0, stat.S_IFREG, stat.S_IFDIR)),
                    "plugin archive contains an unsupported file type")
            total += entry.file_size
            require(entry.file_size <= 256 << 20 and total <= 1 << 30,
                    "plugin archive exceeds its extraction limit")
        for entry in entries:
            target = destination / entry.filename
            if entry.is_dir():
                target.mkdir(parents=True, mode=0o700, exist_ok=True)
                continue
            target.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
            with bundle.open(entry) as source, target.open("xb") as output:
                shutil.copyfileobj(source, output, 1 << 20)
            target.chmod(0o700 if (entry.external_attr >> 16) & 0o111 else 0o600)


def copy_catalog(source, destination):
    """Copy only public plugin inputs; never copy .git, project rules or state."""
    source, destination = Path(source).resolve(strict=True), Path(destination)
    names = [".agents/plugins/marketplace.json", ".claude-plugin/marketplace.json",
             "LICENSE", "THIRD_PARTY_NOTICES.md", "VERSION", "BUILDINFO.json"]
    for name in names:
        path = source / name
        current = path
        while current != source:
            require(not current.is_symlink(), "unsafe catalog input")
            current = current.parent
        if path.exists():
            require(path.is_file() and not path.is_symlink(), "unsafe catalog input")
            target = destination / name
            target.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
            shutil.copyfile(path, target)
    for client in CLIENTS.values():
        plugin = source / "plugins" / client
        require(plugin.is_dir() and not plugin.is_symlink(), "plugin directory missing")
        for path in plugin.rglob("*"):
            require(not path.is_symlink() and (path.is_dir() or path.is_file()),
                    "plugin inputs must be regular files or directories")
        shutil.copytree(plugin, destination / "plugins" / client)


def run(executable, args, home, env, timeout=30, acceptable=(0,)):
    result = subprocess.run([str(executable), *map(str, args)], cwd=home, env=env,
                            stdin=subprocess.DEVNULL, capture_output=True,
                            text=True, timeout=timeout)
    require(result.returncode in acceptable,
            "command failed: " + " ".join(map(str, args[:3]))
            + ": " + (result.stderr or result.stdout)[-1500:])
    return result.stdout


def last_json(text):
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        for line in reversed(text.splitlines()):
            try:
                return json.loads(line)
            except json.JSONDecodeError:
                continue
    raise ValueError("native command returned no JSON result")


class RPC:
    """Bounded JSON-lines transport; no prompt or inference method is exposed."""

    def __init__(self, executable, args, home, env):
        self.process = subprocess.Popen([str(executable), *map(str, args)],
                                        cwd=home, env=env, stdin=subprocess.PIPE,
                                        stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                        text=True, bufsize=1, start_new_session=True)
        self.messages, self.errors, self.serial = queue.Queue(), [], 0
        self.reader = threading.Thread(target=self._read, daemon=True)
        self.error_reader = threading.Thread(target=self._drain_errors, daemon=True)
        self.reader.start()
        self.error_reader.start()

    def _read(self):
        try:
            for line in self.process.stdout:
                require(len(line) <= 8 << 20, "RPC response exceeds limit")
                self.messages.put(json.loads(line))
        except Exception as error:
            self.messages.put(error)
        finally:
            self.messages.put(EOFError("RPC process stopped"))

    def _drain_errors(self):
        for line in self.process.stderr:
            self.errors.append(line[:1000])
            self.errors = self.errors[-8:]

    def notify(self, method, params=None):
        message = {"jsonrpc": "2.0", "method": method}
        if params is not None:
            message["params"] = params
        self.process.stdin.write(json.dumps(message) + "\n")
        self.process.stdin.flush()

    def call(self, method, params, timeout=30):
        allowed = {"initialize", "tools/list", "tools/call", "plugin/read",
                   "skills/list", "mcpServerStatus/list"}
        require(method in allowed, "RPC method is outside the no-inference check")
        self.serial += 1
        self.process.stdin.write(json.dumps({"jsonrpc": "2.0", "id": self.serial,
                                            "method": method, "params": params}) + "\n")
        self.process.stdin.flush()
        deadline = time.monotonic() + timeout
        while True:
            try:
                message = self.messages.get(timeout=max(0, deadline - time.monotonic()))
            except queue.Empty:
                raise TimeoutError("RPC deadline: " + method) from None
            if isinstance(message, Exception):
                raise ValueError("RPC stopped: " + "".join(self.errors)[-1200:]) from message
            if message.get("id") != self.serial:
                continue
            require("error" not in message, "RPC rejected " + method + ": "
                    + json.dumps(message.get("error")))
            return message["result"]

    def close(self):
        with contextlib.suppress(OSError):
            self.process.stdin.close()
        try:
            self.process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            with contextlib.suppress(ProcessLookupError):
                os.killpg(self.process.pid, signal.SIGTERM)
            try:
                self.process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                with contextlib.suppress(ProcessLookupError):
                    os.killpg(self.process.pid, signal.SIGKILL)
                self.process.wait(timeout=3)
        self.reader.join(timeout=1)
        self.error_reader.join(timeout=1)
        self.process.stdout.close()
        self.process.stderr.close()

    def __enter__(self):
        return self

    def __exit__(self, *_):
        self.close()


def checked_result(response):
    require(not response.get("isError"), "first task returned a tool error")
    result = response.get("structuredContent")
    if not isinstance(result, dict):
        result = json.loads(response["content"][0]["text"])
    require(result.get("status") == "completed" and result.get("durable") is True
            and result.get("generation_called") is False
            and result.get("text") == '{"status":"ready","model_calls":0}'
            and result.get("provider_cost_usd") == 0 and result.get("artifact"),
            "first task did not meet durable zero-generation checks")
    digest = hashlib.sha256(result["text"].encode()).hexdigest()
    require(result.get("output_sha256") == digest
            and result["artifact"].get("sha256") == digest
            and result["artifact"].get("version") == 1,
            "first task artifact does not match the checked text")
    usage = result.get("this_run_usage", {})
    require(usage.get("input_tokens") == 0 and usage.get("output_tokens") == 0
            and all(value == 0 for value in usage.values()),
            "first task reported generation usage")
    checks = result.get("source_checks", []) + result.get("checks", [])
    require(checks and all(check.get("passed") is True for check in checks),
            "first task checks did not pass")
    require(result.get("outcome_tracking", {}).get("status") == "recorded",
            "first task outcome receipt was not recorded")
    return result


def protocol_check(plugin, client, home, env, version):
    with RPC("/bin/sh", [plugin / "scripts/run.sh", "serve", client], home, env) as rpc:
        rpc.call("initialize", {"protocolVersion": "2025-11-25", "capabilities": {},
                                "clientInfo": {"name": "questlock-plugin-check", "version": "1"}})
        rpc.notify("notifications/initialized")
        tools = rpc.call("tools/list", {})["tools"]
        require({tool["name"] for tool in tools} == TOOLS, "MCP tool inventory differs")
        for tool in tools:
            schema = tool["inputSchema"]
            if tool["name"] == "questlock_status":
                require(schema.get("additionalProperties") is False
                        and not schema.get("required")
                        and not schema.get("properties"),
                        "worker status accepts caller authority")
                annotations = tool.get("annotations", {})
                require(annotations.get("readOnlyHint") is True
                        and annotations.get("idempotentHint") is True
                        and annotations.get("destructiveHint") is False
                        and annotations.get("openWorldHint") is False,
                        "worker status annotations permit effects")
                continue
            require(schema.get("additionalProperties") is False
                    and schema.get("required") == ["request_path"]
                    and set(schema.get("properties", {})) == {"request_path"},
                    "MCP tool request surface broadened")
        discovery = rpc.call("tools/call", {"name": "questlock_status", "arguments": {}})
        require(not discovery.get("isError"), "worker status failed")
        contract = discovery.get("structuredContent")
        if not isinstance(contract, dict):
            contract = json.loads(discovery["content"][0]["text"])
        require(set(contract) == {"schema", "runtime_version", "request_root", "profile",
                                "policy", "request_schemas", "checks", "tools",
                                "workflow_configs", "readiness", "read_only", "model_calls"},
                "worker status surface differs from its safe contract")
        require(contract.get("schema") == 1 and contract.get("runtime_version") == version
                and contract.get("request_root") == str(home / ".local/share/questlock/plugins/requests")
                and contract.get("profile") == {"id": "managed-" + client, "kind": "managed"}
                and contract.get("policy", {}).get("allow_paid") is False
                and contract.get("request_schemas") == {"delegation": ["unversioned"], "revision": [1], "semantic_shadow_modes": ["shadow"]}
                and set(contract.get("tools", [])) == TOOLS
                and contract.get("read_only") is True and contract.get("model_calls") == 0,
                "worker status did not identify the connected runtime contract")
        require(contract.get("readiness") == {"worker": "ready", "request_root": "opened",
                                               "broker": "not_checked", "providers": "not_checked"}
                and contract.get("workflow_configs") == {"writing": "writing-workflow.json"},
                "worker status inferred external readiness or read a workflow marker")
        require(contract.get("checks", {}).get("schema") == 1
                and set(contract.get("checks", {}).get("kinds", [])) == {
                    "json_valid", "json_equals", "contains", "not_contains",
                    "word_count", "character_count", "paragraph_count"},
                "worker check capabilities differ")
        first_path = str(Path(contract["request_root"]) / "first-task.json")
        first = checked_result(rpc.call("tools/call", {
            "name": "questlock_revise", "arguments": {"request_path": first_path}}))
        repeat = checked_result(rpc.call("tools/call", {
            "name": "questlock_revise", "arguments": {"request_path": first_path}}))
        require(not first.get("reused") and repeat.get("reused") is True
                and first["artifact"] == repeat["artifact"], "exact durable reuse was not confirmed")
    return {"status": "completed", "evidence": "direct_packaged_launcher_mcp",
            "tools": sorted(TOOLS), "worker_contract_discovered": True,
            "checked_revision": True, "exact_reuse": True,
            "model_calls": 0, "provider_cost_usd": 0,
            "native_chat_tool_execution_verified": False}


def native_codex(executable, catalog, home, env):
    version = run(executable, ["--version"], home, env).strip()
    added = last_json(run(executable, ["plugin", "marketplace", "add", catalog, "--json"], home, env))
    require(added.get("marketplaceName") == "questlock", "native Codex registered wrong marketplace")
    installed = last_json(run(executable, ["plugin", "add", "questlock@questlock", "--json"], home, env))
    require(installed.get("pluginId") == "questlock@questlock", "native Codex installed wrong plugin")
    listed = last_json(run(executable, ["plugin", "list", "--json", "--marketplace", "questlock"], home, env))
    require(any(item.get("pluginId") == "questlock@questlock" and item.get("enabled")
                and item.get("installed") for item in listed.get("installed", [])),
            "native Codex did not enable the installed plugin")
    with RPC(executable, ["app-server"], home, env) as rpc:
        rpc.call("initialize", {"clientInfo": {"name": "questlock_plugin_check", "version": "1"},
                                "capabilities": {"experimentalApi": True}})
        rpc.notify("initialized")
        detail = rpc.call("plugin/read", {"pluginName": "questlock",
                          "marketplacePath": str(catalog / ".agents/plugins/marketplace.json")})["plugin"]
        require(detail.get("mcpServers") and detail.get("skills"), "native Codex omitted plugin components")
        skills = rpc.call("skills/list", {"cwds": [str(home)], "forceReload": True})
        loaded = [skill for entry in skills["data"] for skill in entry["skills"]
                  if skill.get("pluginId") == "questlock@questlock"]
        require(loaded and all(skill.get("enabled") for skill in loaded), "native Codex did not load plugin skill")
        status = rpc.call("mcpServerStatus/list", {"detail": "toolsAndAuthOnly"}, timeout=45)
        inventories = [item for item in status["data"]
                       if "questlock" in item.get("name", "")]
        names = {name for item in inventories for name in item.get("tools", {})}
        require(TOOLS.issubset(names), "native Codex did not discover all three MCP tools")
    return {"status": "completed", "version": version,
            "evidence": "native_marketplace_install_app_server_skill_and_mcp_inventory",
            "installed": True, "enabled": True, "skill_loaded": True,
            "mcp_tools_discovered": sorted(TOOLS), "model_calls": 0,
            "native_chat_tool_execution_verified": False}


def native_claude(executable, catalog, home, env):
    version = run(executable, ["--version"], home, env).strip()
    for target in (catalog / "plugins/claude", catalog):
        validated = last_json(run(executable, ["plugin", "validate", target, "--strict", "--json"], home, env))
        require(validated.get("success") is True, "native Claude rejected plugin manifest")
    added = last_json(run(executable, ["plugin", "marketplace", "add", catalog, "--json", "--scope", "user"], home, env))
    require(added.get("outcome") == "ok" and added.get("marketplace") == "questlock",
            "native Claude did not register marketplace")
    installed = last_json(run(executable, ["plugin", "install", "questlock@questlock", "--json", "--scope", "user"], home, env))
    require(installed.get("outcome") == "ok" and installed.get("pluginId") == "questlock@questlock",
            "native Claude did not install plugin")
    listed = last_json(run(executable, ["plugin", "list", "--json"], home, env))
    rows = listed if isinstance(listed, list) else listed.get("installed", [])
    require(any(item.get("id") == "questlock@questlock" and item.get("enabled")
                for item in rows), "native Claude did not enable plugin")
    details = run(executable, ["plugin", "details", "questlock@questlock"], home, env)
    require("questlock" in details and "MCP" in details and "Skills" in details,
            "native Claude component inventory missing")
    connections = run(executable, ["mcp", "list"], home, env, timeout=45)
    require("questlock" in connections and "Connected" in connections,
            "native Claude did not connect plugin MCP server")
    return {"status": "completed", "version": version,
            "evidence": "native_strict_validation_marketplace_install_component_inventory_mcp_health",
            "installed": True, "enabled": True, "skill_loaded": True,
            "mcp_connection_verified": True, "model_calls": 0,
            "native_chat_tool_execution_verified": False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    source = parser.add_mutually_exclusive_group()
    source.add_argument("--root", type=Path, default=ROOT, help="source or extracted catalog root")
    source.add_argument("--archive", type=Path, help="platform plugin ZIP to extract privately")
    parser.add_argument("--runtime-bin", type=Path, help="inject a built runtime into private catalog copies")
    parser.add_argument("--codex-bin", type=Path, help="explicit native Codex executable")
    parser.add_argument("--claude-bin", type=Path, help="explicit native Claude Code executable")
    parser.add_argument("--output", type=Path, help="write the redacted JSON report")
    args = parser.parse_args()
    report = {"status": "completed", "model_calls": 0, "provider_cost_usd": 0,
              "paid_fallback_enabled": False, "active_client_settings_changed": False,
              "native_chat_tool_execution_verified": False, "clients": {}}
    with tempfile.TemporaryDirectory(prefix="questlock-plugins-check-") as temp:
        private = Path(temp).resolve()
        catalog = private / "catalog"
        catalog.mkdir(mode=0o700)
        if args.archive:
            safe_extract(args.archive, catalog)
        else:
            copy_catalog(args.root.resolve(), catalog)
        for client, folder in CLIENTS.items():
            plugin = catalog / "plugins" / folder
            runtime = plugin / "bin/questlock"
            if args.runtime_bin:
                runtime.parent.mkdir(mode=0o700, exist_ok=True)
                shutil.copyfile(args.runtime_bin.resolve(strict=True), runtime)
                runtime.chmod(0o700)
            require(runtime.is_file() and not runtime.is_symlink(),
                    "bundled runtime missing; use --runtime-bin for source validation")
            home = private / client
            home.mkdir(mode=0o700)
            env = isolated_env(home)
            expected = json.loads((plugin / ("plugin.json" if client == "codex" else ".claude-plugin/plugin.json")).read_text())["version"]
            actual = run(runtime, ["version"], home, env).strip()
            require(actual == expected, "bundled runtime version does not match manifest")
            try:
                row = {"protocol": protocol_check(plugin, client, home, env, expected)}
                # Use another empty profile so native discovery proves cold startup.
                native_home = private / (client + "-native")
                native_home.mkdir(mode=0o700)
                native_env = isolated_env(native_home)
                executable = args.codex_bin if client == "codex" else args.claude_bin
                if executable:
                    try:
                        check = native_codex if client == "codex" else native_claude
                        row["native"] = check(executable.resolve(strict=True), catalog, native_home, native_env)
                    finally:
                        run(runtime, ["plugin", "stop", "--client", client], native_home, native_env,
                            timeout=10, acceptable=(0, 1))
                else:
                    row["native"] = {"status": "not_requested", "native_client_connection_verified": False}
                report["clients"][client] = row
            finally:
                run(runtime, ["plugin", "stop", "--client", client], home, env,
                    timeout=10, acceptable=(0, 1))
    encoded = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.write_text(encoded)
    print(encoded, end="")


if __name__ == "__main__":
    main()
