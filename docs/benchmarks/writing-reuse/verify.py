#!/usr/bin/env python3
"""Audit the saved public observation offline; no services or models are used."""

from datetime import datetime
from decimal import Decimal
import hashlib
import json
from pathlib import Path
import re


ROOT = Path(__file__).resolve().parent


def require(condition, message):
    if not condition:
        raise ValueError(message)


def verify():
    observation = json.loads((ROOT / "observation.json").read_text())
    request_bytes = (ROOT / "request.json").read_bytes()
    request = json.loads(request_bytes)
    answer_bytes = (ROOT / "answer.txt").read_bytes()
    answer = answer_bytes.decode("utf-8")
    digest = hashlib.sha256(answer_bytes).hexdigest()
    first, repeat = observation["invocations"]
    a, b = first["receipt"], repeat["receipt"]

    require(observation["schema"] == 1 and observation["kind"] == "single_live_observation", "unexpected observation schema")
    require(hashlib.sha256(request_bytes).hexdigest() == observation["publication"]["request_file_sha256"], "published request file changed")
    require(first["name"] == "first" and repeat["name"] == "exact_repeat", "unexpected invocation order")
    require(a["status"] == b["status"] == "completed" and a["durable"] and b["durable"], "answer was not complete and durable")
    require(not a["reused"] and b["reused"], "fresh/reuse evidence changed")
    require(a["answer_sha256"] == b["answer_sha256"] == digest, "answer bytes do not match both receipts")
    require(a["request_sha256"] == b["request_sha256"] and a["fingerprint"] == b["fingerprint"], "snapshot/runtime identity differs")
    require(a["created_at"] == b["created_at"] and a["expires_at"] == b["expires_at"], "reuse changed result lifetime")
    end = datetime.fromisoformat(a["expires_at"].replace("Z", "+00:00"))
    lookup = datetime.fromisoformat(b["measurement"]["started_at"].replace("Z", "+00:00"))
    require(lookup < end, "repeat fell outside the reuse window")

    # The saved English prose uses ordinary spaces/newlines; reject unusual
    # whitespace rather than pretending Python and Go have identical tables.
    require(all(not c.isspace() or c in " \t\r\n" for c in answer), "count rules need review for this text")
    paragraphs = len([p for p in re.split(r"\n[ \t\r]*\n", answer) if p.strip()])
    observed = {"word_count": len(answer.split()), "character_count": len(answer), "paragraph_count": paragraphs}
    for receipt in (a, b):
        require(receipt["quality_passed"] and all(c["passed"] for c in receipt["checks"]), "declared checks failed")
        for check in request["checks"]:
            if check["kind"] in observed:
                count = observed[check["kind"]]
                require(check.get("min", 0) <= count <= check.get("max", 4194304), "answer outside count contract")
                saved = next(c for c in receipt["checks"] if c["id"] == check["id"])
                require(saved["observed"] == count, "saved count differs from answer")
            elif check["kind"] == "not_contains":
                text, needle = answer, check["text"]
                if check.get("ignore_case"):
                    text, needle = text.lower(), needle.lower()
                require(needle not in text, "answer contains excluded phrase")
            else:
                raise ValueError("verifier needs an explicit rule for this check")

    require(a["this_run_usage"] == a["generation_usage"] == b["generation_usage"] == b["avoided_usage"], "new and historical usage were mixed")
    require(all(n == 0 for n in a["avoided_usage"].values()) and all(n == 0 for n in b["this_run_usage"].values()), "reuse records new usage")
    require(a["measurement"]["generation_called"] and not b["measurement"]["generation_called"], "provider Generate boundary changed")
    require(first["generation_http_requests"] == 1 and repeat["generation_http_requests"] == 0, "duplicate generation observed")
    audit = observation["ollama_audit"]
    require(audit["generation_requests"] == 1 and len(audit["generations"]) == 1, "unexpected live generation total")
    generation = audit["generations"][0]
    require(generation["done"] and generation["done_reason"] == "stop", "live generation incomplete")
    require(generation["model"] == request["policy"]["local"]["spec"]["model"], "local model changed")
    require(generation["prompt_eval_count"] == a["this_run_usage"]["input_tokens"] and generation["eval_count"] == a["this_run_usage"]["output_tokens"], "audit usage differs from receipt")
    require(set(request["policy"]) == {"local"} and request["policy"]["local"]["spec"]["provider"] == "ollama", "paid route present")
    require(not observation["paid_fallback_enabled"] and observation["provider_api_spend_usd"] == 0, "paid policy/spend changed")
    require(observation["review"]["human_acceptance"] == "not_asserted" and observation["review"]["human_effort_minutes"] is None, "unknown human outcome became accepted/free")

    negative = observation["negative_controls"]
    stop = negative["budget_stop"]
    d = stop["diagnostic"]
    require(stop["status"] == "stopped" and stop["error_category"] == "budget_exceeded" and not d["generation_called"] and stop["generation_http_requests"] == 0, "budget control generated")
    require(d["estimated_input_tokens"] > d["configured_limit"] and d["count_basis"] == "conservative_byte_estimate", "estimate lost its basis")
    require(all(c["status"] == "not_evaluated" and "observed" not in c for c in stop["checks"]), "stopped output was graded")
    overlong = negative["length_rejection"]
    checks = {c["id"]: c for c in overlong["checks"]}
    require(overlong["status"] == "quality_failed" and not overlong["generation_called"] and checks["old-keyword"]["passed"] and not checks["words"]["passed"] and checks["words"]["observed"] == 193, "length negative control changed")
    require(negative["changed_evidence_changes_identity"] and negative["reference_drift_stops_plan"], "source-change controls failed")

    before, after = Decimal(str(first["end_to_end_ms"])), Decimal(str(repeat["end_to_end_ms"]))
    require(before > after > 0, "invalid caller timing")
    summary = {"status": "verified_saved_observation", "words": observed["word_count"], "paragraphs": paragraphs,
               "first_new_tokens": a["this_run_usage"]["input_tokens"] + a["this_run_usage"]["output_tokens"],
               "repeat_new_tokens": 0, "observed_generation_requests": 1,
               "observed_call_latency_reduction_percent": str(((1 - after / before) * 100).quantize(Decimal("0.01"))),
               "first_model_load_seconds": str(Decimal(generation["load_duration"]) / 1000000000),
               "verification_model_calls": 0, "performance_experiment_rerun": False}
    print(json.dumps(summary, sort_keys=True))


if __name__ == "__main__":
    verify()
