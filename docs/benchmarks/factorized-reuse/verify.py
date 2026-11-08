#!/usr/bin/env python3
"""Check retained diagnostic accounting offline; this does not certify model quality."""
import hashlib
import json
import math
from collections import Counter, defaultdict
from pathlib import Path

HERE = Path(__file__).resolve().parent


def pinned(name, digest):
    path = HERE / name
    assert path.parent == HERE and not path.is_symlink()
    raw = path.read_bytes()
    assert hashlib.sha256(raw).hexdigest() == digest, name
    return json.loads(raw)


def metric(actual, numerator, denominator):
    if denominator:
        assert actual is not None and math.isclose(actual, numerator / denominator)
    else:
        assert actual is None


index = json.loads((HERE / "index.json").read_text())
assert not index["paid_fallback_enabled"] and index["provider_api_charge_usd"] == 0
combined = Counter()
unknown_keys = []
for entry in index["runs"]:
    report = pinned(entry["file"], entry["sha256"])
    fixture = pinned(entry["fixture"], entry["fixture_sha256"])
    assert not entry["qualified"]
    assert report["source_sha256"] == entry["fixture_sha256"]
    assert report["answer_generation_calls"] == 0 and not report["savings_measured"]
    assert not report["paid_fallback_enabled"] and report["provider_api_charge_usd"] == 0
    labels = {v["id"]: (c["id"], v["should_reuse"])
              for c in fixture["cases"] for v in c["variants"]}
    rows, totals, usage = report["pairs"], report["totals"], report["usage"]
    assert len(rows) == len(labels) == totals["total"]
    assert len({r["id"] for r in rows}) == len(rows)
    counts = Counter()
    work = []
    for row in rows:
        assert labels[row["id"]] == (row["case_id"], row["expected_reuse"])
        positive, served = row["expected_reuse"], row["served"]
        counts["positive_labels" if positive else "negative_labels"] += 1
        unscored = not served and (bool(row.get("error_category")) or row["status"] in
                                  ("error", "not_run", "queued", "batch_required", "resource_deferred"))
        if unscored:
            counts["unscored"] += 1
            counts["unscored_positive_labels" if positive else "unscored_negative_labels"] += 1
        else:
            counts["true_positives" if positive and served else
                   "false_positives" if served else
                   "false_negatives" if positive else "true_negatives"] += 1
        counts["unattempted"] += row["status"] == "not_run"
        counts["errors"] += bool(row.get("error_category")) and row["status"] != "resource_deferred"
        counts["abstentions"] += row["abstained"]
        counts["unknown_verdicts"] += (row.get("verification") or {}).get("verdict") == "unknown"
        work.extend((row.get("verification") or {}).get("work") or [])
    for key in ("positive_labels", "negative_labels", "true_positives", "false_positives",
                "true_negatives", "false_negatives", "unscored", "unattempted", "errors",
                "abstentions", "unknown_verdicts", "unscored_positive_labels", "unscored_negative_labels"):
        assert totals.get(key, 0) == counts[key], (entry["name"], key)
    assert totals.get("confusion_pairs", len(rows)) == len(rows) - counts["unscored"]
    assert totals["completed"] == len(rows) - counts["unattempted"] - counts["errors"]
    tp, fp, tn = counts["true_positives"], counts["false_positives"], counts["true_negatives"]
    metric(totals["precision"], tp, tp + fp)
    metric(totals["recall"], tp, counts["positive_labels"] - counts["unscored_positive_labels"])
    metric(totals["accuracy"], tp + tn, len(rows) - counts["unscored"])
    metric(totals["coverage"], tp + fp, len(rows))
    fresh = [w for w in work if w["generation_called"]]
    cached = [w for w in work if w["reused"]]
    assert len({w["receipt_key"] for w in fresh}) == len(fresh)
    known = [w for w in work if w["input_tokens"] is not None and w["output_tokens"] is not None]
    unknown = [w for w in work if w not in known]
    assert all(type(w["input_tokens"]) is int and type(w["output_tokens"]) is int and
               w["input_tokens"] >= 0 and w["output_tokens"] >= 0 and w["durable"] for w in known)
    # These retained unknown records have no response usage. Do not value them at zero.
    assert all(w["input_tokens"] is None and w["output_tokens"] is None and
               w["generation_called"] and not w["reused"] and not w["complete"] for w in unknown)
    unknown_keys.extend(w["receipt_key"] for w in unknown)
    assert all(w["stage"] == "source" and w["complete"] and w["durable"] and
               not w["generation_called"] and w["input_tokens"] == w["output_tokens"] == 0
               for w in cached)
    assert all(w["receipt_key"] in {f["receipt_key"] for f in fresh} for w in cached)
    assert len(fresh) == usage["verifier_generation_calls"]
    assert len(cached) == usage["cached_verifications"]
    assert len(known) == usage["verifier_known_usage_calls"]
    assert len(unknown) == usage["verifier_unknown_usage_calls"]
    assert usage["verifier_input_tokens"] == sum(w["input_tokens"] for w in known)
    assert usage["verifier_output_tokens"] == sum(w["output_tokens"] for w in known)
    models = defaultdict(Counter)
    for w in fresh:
        bucket = models[(w["model"], w["runtime_identity"])]
        bucket.update(generation_calls=1)
        if w in known:
            bucket.update(input_tokens=w["input_tokens"], output_tokens=w["output_tokens"])
    for model in usage["verifier_models"]:
        actual = models.pop((model["model"], model["runtime_identity"]), Counter())
        assert all(actual[k] == model[k] for k in ("generation_calls", "input_tokens", "output_tokens"))
        group = [w for w in work if (w["model"], w["runtime_identity"]) ==
                 (model["model"], model["runtime_identity"])]
        assert model["cached_verifications"] == sum(w["reused"] for w in group)
        assert model["known_usage_calls"] == sum(w in known for w in group)
        assert model["unknown_usage_calls"] == sum(w in unknown for w in group)
    assert not models
    embeddings = report["source_embeddings"] + rows
    calls = sum(e.get("attempted", e.get("embedding_attempted", False)) for e in embeddings)
    tokens = sum((e.get("input_tokens", e.get("embedding_input_tokens")) or 0) for e in embeddings)
    assert calls == usage["embedding_calls"] and tokens == usage["embedding_input_tokens"]
    assert usage["embedding_unknown_usage_calls"] == 0
    assert usage["logical_model_calls"] == calls + len(fresh)
    combined.update(native_generation_attempts=len(fresh), cached_source_checks=len(cached),
                    incomplete_known_generations=sum(not w["complete"] for w in fresh if w in known),
                    unknown_usage_attempts=len(unknown))
    print(entry["name"], dict(counts), "native", len(fresh), "cached", len(cached))
assert combined == Counter(native_generation_attempts=346, cached_source_checks=181,
                           incomplete_known_generations=22, unknown_usage_attempts=1)
assert sorted(unknown_keys) == index["recent_unknown_usage_job_keys"]
assert index["retained_prior_native_attempts"] == 201
assert index["retained_prior_complete_known"] == 195
assert index["retained_prior_incomplete_known"] == 1
assert index["retained_prior_usage_unknown"] == 5
assert 195 + 1 + 5 == 201
assert 201 + combined["native_generation_attempts"] == 547
assert 195 + combined["native_generation_attempts"] - combined["incomplete_known_generations"] - combined["unknown_usage_attempts"] == 518
assert 1 + combined["incomplete_known_generations"] == 23
assert 23 + index["retained_prior_usage_unknown"] + combined["unknown_usage_attempts"] == 29
print("PASS:", dict(combined), "Paid fallback off; provider API $0. No fresh-answer savings measured.")
