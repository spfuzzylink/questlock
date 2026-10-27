#!/usr/bin/env python3
"""Recompute preserved diagnostic counts without models or network access."""
import hashlib
import json
import math
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
HERE = Path(__file__).resolve().parent


def read_public(name):
    path = ROOT / name
    assert not path.is_symlink() and path.resolve().is_relative_to(ROOT)
    return path.read_bytes()


def ratio(n, d):
    return n / d if d else None


def close(actual, expected):
    assert (actual is None) == (expected is None)
    if actual is not None:
        assert math.isclose(actual, expected, rel_tol=1e-12, abs_tol=1e-12)


index = json.loads((HERE / "index.json").read_text())
assert index["provider_api_charge_usd"] == 0
assert index["paid_fallback_enabled"] is False
for entry in index["runs"]:
    raw = read_public(entry["file"])
    assert hashlib.sha256(raw).hexdigest() == entry["sha256"]
    fixture_raw = read_public(entry["fixture"])
    assert hashlib.sha256(fixture_raw).hexdigest() == entry["fixture_sha256"]
    fixture = json.loads(fixture_raw)
    labels = {v["id"]: (c["id"], v["should_reuse"])
              for c in fixture["cases"] for v in c["variants"]}
    report = json.loads(raw)
    rows, totals, usage = report["pairs"], report["totals"], report["usage"]
    assert report["source_sha256"] == entry["fixture_sha256"]
    assert report["provider_api_charge_usd"] == 0
    assert report["paid_fallback_enabled"] is False
    assert len(rows) == len(labels) == totals["total"]
    assert len({r["id"] for r in rows}) == len(rows)
    for r in rows:
        assert labels[r["id"]] == (r["case_id"], r["expected_reuse"])
    counts = {
        "positive_labels": sum(r["expected_reuse"] for r in rows),
        "negative_labels": sum(not r["expected_reuse"] for r in rows),
        "true_positives": sum(r["expected_reuse"] and r["served"] for r in rows),
        "false_positives": sum(not r["expected_reuse"] and r["served"] for r in rows),
        "true_negatives": sum(not r["expected_reuse"] and not r["served"] for r in rows),
        "false_negatives": sum(r["expected_reuse"] and not r["served"] for r in rows),
        "errors": sum(bool(r.get("error_category")) for r in rows),
        "abstentions": sum(r["abstained"] for r in rows),
        "unattempted": sum(r["status"] == "not_run" for r in rows),
        "completed": sum(r["status"] != "not_run" and not r.get("error_category") for r in rows),
        "retrieval_misses": sum(r["status"] == "retrieval_miss" for r in rows),
        "guard_rejections": sum(r["status"] == "guard_rejected" for r in rows),
        "unknown_verdicts": sum((r.get("verification") or {}).get("verdict") == "unknown" for r in rows),
    }
    for key, value in counts.items():
        assert totals[key] == value, (entry["name"], key)
    tp, fp, tn = counts["true_positives"], counts["false_positives"], counts["true_negatives"]
    close(totals["precision"], ratio(tp, tp + fp))
    close(totals["recall"], ratio(tp, counts["positive_labels"]))
    close(totals["accuracy"], ratio(tp + tn, len(rows)))
    close(totals["coverage"], ratio(tp + fp, len(rows)))
    for case_id, case in entry["per_case"].items():
        group = [r for r in rows if r["case_id"] == case_id]
        assert case["tp"] == sum(r["expected_reuse"] and r["served"] for r in group)
        assert case["fp"] == sum(not r["expected_reuse"] and r["served"] for r in group)
        close(case["precision"], ratio(case["tp"], case["tp"] + case["fp"]))
        close(case["recall"], ratio(case["tp"], case["positive"]))
    work = list(report.get("batch_verification_work") or [])
    work += [r["verification"] for r in rows if r.get("verification")
             and not r["verification"].get("shared_work")]
    for r in rows:
        v = r.get("verification") or {}
        if v.get("shared_work"):
            assert not v["generation_called"] and v["reused"]
            assert v["input_tokens"] == v["output_tokens"] == 0
    assert usage["verifier_generation_calls"] == sum(w["generation_called"] for w in work)
    assert usage["verifier_known_usage_calls"] == sum(w["input_tokens"] is not None and w["output_tokens"] is not None for w in work)
    assert usage["verifier_unknown_usage_calls"] == sum(w["input_tokens"] is None or w["output_tokens"] is None for w in work)
    assert usage["verifier_input_tokens"] == sum(w["input_tokens"] for w in work if w["input_tokens"] is not None and w["output_tokens"] is not None)
    assert usage["verifier_output_tokens"] == sum(w["output_tokens"] for w in work if w["input_tokens"] is not None and w["output_tokens"] is not None)
    embeddings = [(s["attempted"], s["input_tokens"]) for s in report["source_embeddings"]]
    embeddings += [(r["embedding_attempted"], r["embedding_input_tokens"]) for r in rows]
    assert usage["embedding_calls"] == sum(a for a, _ in embeddings)
    assert usage["embedding_input_tokens"] == sum(t for a, t in embeddings if a and t is not None)
    assert usage["logical_model_calls"] == usage["embedding_calls"] + usage["verifier_generation_calls"]

native = json.loads((HERE / "withheld-native-prototype.json").read_text())
for key, roles in {
    "seed_generation": {"seed"}, "baseline_generation": {"baseline"},
    "candidate_generation_including_shared_verifier": {"candidate_shared_verifier", "candidate_fresh"},
    "candidate_generation_including_seed_and_shared_verifier": {"seed", "candidate_shared_verifier", "candidate_fresh"},
    "exact_replay_generation": {"exact"},
}.items():
    work = [w for w in native["model_work"] if w["role"] in roles and w["generation_called"]]
    total = native["costs"][key]
    assert total["generation_calls"] == len(work)
    assert total["input_tokens"] == sum(w["input_tokens"] for w in work if w["usage_known"])
    assert total["output_tokens"] == sum(w["output_tokens"] for w in work if w["usage_known"])
    assert total["calls_with_unknown_usage"] == sum(not w["usage_known"] for w in work)
live = json.loads((HERE / "approved-forms-summary.json").read_text())
raw = read_public(live["raw_file"])
assert hashlib.sha256(raw).hexdigest() == live["raw_sha256"]
records = [json.loads(line) for line in raw.splitlines()]
case_raw = read_public("examples/approved-questions/cases.json")
assert hashlib.sha256(case_raw).hexdigest() == records[0]["cases_sha256"] == live["dataset_sha256"]
cases = {c["id"]: c for c in json.loads(case_raw)["cases"]}
counts = {phase: dict(calls=0, generation_calls=0, quality_passed=0,
                      withheld=0, input_tokens=0, output_tokens=0, usage_unknown=False)
          for phase in ("seed", "replay", "baseline")}
sources = {}
for row in records:
    if not row.get("case_id"):
        continue
    phase, case_id = row["phase"], row["case_id"]
    case, count = cases[case_id], counts[phase]
    assert row["input"] in case["approved_questions"]["forms"]
    result = row.get("result")
    if not result:
        assert phase == "replay" and row["withheld"] == "source_not_fresh_and_quality_passing"
        count["withheld"] += 1
        continue
    count["calls"] += 1
    passed = result["status"] == "completed"
    count["quality_passed"] += passed
    assert len(result["attempts"]) == 1
    attempt = result["attempts"][0]
    actual = attempt["result"]
    text = row.get("model_output") or result.get("response", {}).get("text") or actual["response"]["text"]
    expected = next(c["expected"] for c in case["checks"] if c["kind"] == "json_equals")
    assert (json.loads(text) == expected) == passed
    assert hashlib.sha256(text.encode()).hexdigest() == attempt["output_sha256"]
    measurement = actual.get("measurement") or {}
    generated = measurement.get("generation_called", False)
    count["generation_calls"] += generated
    if generated:
        assert measurement["known_generation_usage"]
        usage = measurement["generation_usage"]
        count["input_tokens"] += usage["input_tokens"]
        count["output_tokens"] += usage["output_tokens"]
    if phase == "seed":
        sources[case_id] = actual
        assert generated and not actual["reused"]
    elif phase == "replay":
        assert passed and not generated and actual["reused"]
        assert actual["fingerprint"] == sources[case_id]["fingerprint"]
        assert all(value == 0 for value in actual["this_run_usage"].values())
        assert not result.get("semantic_reuse") and not result.get("semantic_shadow")
    else:
        assert generated and not actual["reused"]
assert counts == records[-1]["totals"] == live["totals"]
assert live["provider_api_charge_usd"] == 0 and not live["paid_fallback_enabled"]
subset = live["matched_comparison"]
qualified = [k for k, v in live["per_case"].items()
             if v["seed"]["quality_passed"] == 1 and v["replay"]["quality_passed"] == 2
             and v["baseline"]["quality_passed"] == 2]
assert subset["cases"] == qualified
fresh = sum(live["per_case"][k]["baseline"]["input_tokens"] + live["per_case"][k]["baseline"]["output_tokens"] for k in qualified)
seed = sum(live["per_case"][k]["seed"]["input_tokens"] + live["per_case"][k]["seed"]["output_tokens"] for k in qualified)
assert (fresh, seed) == (subset["fresh_generation_tokens"], subset["seed_generation_tokens"])
close(subset["seed_plus_replay_reduction"], 1 - seed / fresh)
print(f"Verified {len(index['runs'])} diagnostic populations, withheld native accounting and all live approved-form rows; no model calls.")
