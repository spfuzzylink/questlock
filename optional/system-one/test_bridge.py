"""Offline contracts against the installed official adapter; no Ollama requests."""
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("bridge", Path(__file__).resolve().parents[2] / "systemone/bridge.py")
bridge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)
REQUEST = json.loads((Path(__file__).parent / "route.json").read_text())


class Input:
    def __init__(self, request):
        self.buffer = io.BytesIO(json.dumps(request).encode())


class Response(io.BytesIO):
    pass


class BridgeTests(unittest.TestCase):
    def run_bridge(self, content=None, change=None, request=None, failure=None):
        calls, bodies = [], []
        content = content if content is not None else json.dumps({"answers": {"decision": {
            "reuse": 1.0, "checked_edit": 0.0, "local_reasoning": 0.0, "abstain": 0.0}}})
        result = {"model": "qwen3.5:4b", "done": True, "done_reason": "stop",
                  "message": {"content": content}, "prompt_eval_count": 123, "eval_count": 45}
        if change:
            change(result)

        def open_request(_, req, timeout):
            calls.append(req.full_url)
            bodies.append(json.loads(req.data))
            self.assertEqual(timeout, 50)
            if failure:
                raise failure
            return Response(json.dumps(result).encode())

        class Opener:
            open = open_request

        output = io.StringIO()
        with patch.object(sys, "stdin", Input(request or REQUEST)), contextlib.redirect_stdout(output), \
                patch.object(bridge.urllib.request, "build_opener", return_value=Opener()) as build:
            bridge.main()
        report = json.loads(output.getvalue())
        self.assertNotIn("answers", report)
        self.assertNotIn("state", report)
        if calls:
            self.assertEqual(calls, ["http://127.0.0.1:11434/api/chat"])
            args = build.call_args.args
            self.assertEqual(args[0].proxies, {})
            self.assertIsInstance(args[1], bridge.NoRedirect)
            self.assertFalse(bodies[0]["think"])
            self.assertFalse(bodies[0]["stream"])
            self.assertEqual(bodies[0]["options"], {"num_ctx": 8192, "num_predict": 256, "temperature": 0})
            self.assertIn("answers", bodies[0]["format"]["properties"])
        return report

    def test_actual_adapter_schema_and_usage(self):
        report = self.run_bridge()
        self.assertEqual(report["status"], "ok")
        self.assertEqual(report["scores"]["reuse"], 1)
        self.assertEqual((report["calls"], report["retries"]), (1, 0))
        self.assertEqual((report["input_tokens"], report["output_tokens"]), (123, 45))

    def test_missing_usage_stays_unknown(self):
        report = self.run_bridge(change=lambda result: (result.pop("prompt_eval_count"), result.pop("eval_count")))
        self.assertEqual(report["status"], "ok")
        self.assertIsNone(report["input_tokens"])
        self.assertIsNone(report["output_tokens"])

    def test_malformed_or_unsupported_abstains_without_retries(self):
        cases = [
            {"content": "not JSON"}, {"content": '{"answers":{},"answers":{}}'},
            {"content": '{"answers":{"decision":{"reuse":NaN}}}'},
            {"content": '{"answers":{"decision":{"unsupported":1.0}}}'},
            {"change": lambda result: result.update(done=False)},
            {"change": lambda result: result.update(done_reason="length")},
            {"change": lambda result: result.update(model="other:tag")},
            {"change": lambda result: result["message"].update(tool_calls=[{}])},
            {"change": lambda result: result["message"].update(thinking="private reasoning")},
            {"content": "x" * 33000},
            {"failure": TimeoutError("sensitive prompt must never appear")},
        ]
        for case in cases:
            with self.subTest(case=list(case)):
                report = self.run_bridge(**case)
                self.assertEqual(report["status"], "abstained")
                self.assertEqual((report["calls"], report["retries"]), (1, 0))
                self.assertNotIn("sensitive", json.dumps(report))

    def test_cloud_rejected_before_call(self):
        request = dict(REQUEST, model="qwen3.5:cloud")
        report = self.run_bridge(request=request)
        self.assertEqual(report["status"], "abstained")
        self.assertEqual(report["calls"], 0)

    def test_redirect_refused(self):
        self.assertIsNone(bridge.NoRedirect().redirect_request(None, None, 302, "", {}, "https://remote.example"))


if __name__ == "__main__":
    unittest.main()
