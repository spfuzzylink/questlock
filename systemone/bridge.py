"""Optional TypeSafe adapter transport. Policy and action permissions stay in Go.

The official MIT-licensed system-one-adapter 0.2.1 builds and validates the typed
question. This custom provider makes one bounded, credential-free Ollama call.
"""
import importlib.metadata
import json
import re
import sys
import time
import urllib.request

URL = "http://127.0.0.1:11434/api/chat"


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result


def decode(data):
    return json.loads(data, object_pairs_hook=unique_object,
                      parse_constant=lambda _: (_ for _ in ()).throw(ValueError("nonfinite JSON")))


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, newurl):
        return None


class LocalProvider:
    def __init__(self, model):
        self.model_name = model
        self.calls = 0
        self.input_tokens = None
        self.output_tokens = None

    def translate_error(self, error):
        from typesafe_sdk import TypeSafeError
        return TypeSafeError("bounded local provider failed")

    def request(self, messages, *, schema, structured):
        from system_one_adapter.providers.base import ProviderResult
        if self.calls != 0 or not structured:
            raise ValueError("only one structured local request is permitted")
        body = {"model": self.model_name, "stream": False, "think": False,
                "messages": [{"role": m.role, "content": m.content} for m in messages],
                "format": schema,
                "options": {"num_ctx": 8192, "num_predict": 256, "temperature": 0}}
        encoded = json.dumps(body, allow_nan=False).encode()
        if len(encoded) > 16384:
            raise ValueError("prepared prompt exceeds bound")
        request = urllib.request.Request(URL, data=encoded, headers={"Content-Type": "application/json"})
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        self.calls += 1
        with opener.open(request, timeout=50) as response:
            data = response.read(32769)
        if len(data) > 32768:
            raise ValueError("local response exceeds bound")
        result = decode(data)
        self.input_tokens = result.get("prompt_eval_count")
        self.output_tokens = result.get("eval_count")
        message = result.get("message", {})
        if (result.get("model") != self.model_name or result.get("done") is not True
                or result.get("done_reason") != "stop" or message.get("tool_calls")
                or message.get("thinking")):
            raise ValueError("incomplete or unsupported local response")
        content = message.get("content")
        if not isinstance(content, str) or len(content.encode()) > 8192:
            raise ValueError("invalid local content")
        # Reject duplicate keys and nonfinite numbers before adapter decoding.
        content = json.dumps(decode(content), allow_nan=False)
        return ProviderResult(content, self.input_tokens, self.output_tokens)


def main():
    started = time.perf_counter()
    output = {"schema": "questlock.system-one.bridge.v1", "status": "abstained",
              "reason": "adapter_unavailable", "model": "", "adapter_version": "",
              "scores": None, "input_tokens": None, "output_tokens": None,
              "calls": 0, "retries": 0, "latency_ms": 0}
    provider = None
    try:
        data = sys.stdin.buffer.read(16385)
        if len(data) > 16384:
            raise ValueError("oversized request")
        request = decode(data)
        model = request["model"]
        output["model"] = model
        if (not isinstance(model, str) or not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.:/-]{0,127}", model)
                or "cloud" in model.lower() or ":" not in model.rsplit("/", 1)[-1]):
            raise ValueError("explicit local model tag required")
        output["adapter_version"] = importlib.metadata.version("system-one-adapter")
        if output["adapter_version"] != "0.2.1" or importlib.metadata.version("typesafe-sdk") != "0.7.0":
            raise ValueError("pinned optional dependencies required")
        from system_one_adapter import Choice, RetryPolicy, SystemOneAdapterClient
        provider = LocalProvider(model)
        with SystemOneAdapterClient(structured_outputs=True, llm_answer_mode="probabilities",
                                    normalize_probabilities=False, n_retry_malformed_structure=0,
                                    retry=RetryPolicy(max_retries=0)) as client:
            response = client.system_one(request["state"], {"decision": Choice(
                instructions=request["question"],
                criteria={c["id"]: c["description"] for c in request["choices"]})}, model=provider)
        output["scores"] = response.answers["decision"].probabilities
        output["retries"] = response.usage.n_retries + response.usage.n_retries_malformed_structure
        output["status"], output["reason"] = "ok", "typed_response"
    except Exception:
        # Exceptions/debug traces may contain prompts. Emit no raw error or trace.
        output["reason"] = "malformed_or_unsupported_adapter_result"
    if provider is not None:
        output.update(calls=provider.calls, input_tokens=provider.input_tokens, output_tokens=provider.output_tokens)
    output["latency_ms"] = (time.perf_counter() - started) * 1000
    print(json.dumps(output, allow_nan=False))


if __name__ == "__main__":
    main()
