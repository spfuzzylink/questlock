"""Fault controls for retained diagnostic evidence, without inference or private state."""
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


SOURCE = Path(__file__).resolve().parents[1] / "docs/benchmarks/factorized-reuse"


class RetainedEvidenceTests(unittest.TestCase):
    def check_fault(self, mutate, name="target-contract-pilot24"):
        with tempfile.TemporaryDirectory() as temporary:
            target = Path(temporary) / "evidence"
            shutil.copytree(SOURCE, target)
            index = json.loads((target / "index.json").read_text())
            latest = next(entry for entry in index["runs"] if entry["name"] == name)
            report = json.loads((target / latest["file"]).read_text())
            mutate(index, latest, report)
            raw = (json.dumps(report, indent=2) + "\n").encode()
            (target / latest["file"]).write_bytes(raw)
            latest["sha256"] = hashlib.sha256(raw).hexdigest()
            (target / "index.json").write_text(json.dumps(index))
            result = subprocess.run([sys.executable, str(target / "verify.py")],
                                    capture_output=True, timeout=20)
            self.assertNotEqual(result.returncode, 0, "fault passed after valid rehash")

    def test_completed_false_acceptance_cannot_be_hidden(self):
        def mutate(index, entry, report):
            row = next(r for r in report["pairs"] if r["served"] and not r["expected_reuse"])
            row["served"] = False
        self.check_fault(mutate)

    def test_zeroing_current_verifier_work_does_not_create_savings(self):
        def mutate(index, entry, report):
            work = next(w for r in report["pairs"] for w in (r.get("verification") or {}).get("work", []) if w["generation_called"])
            work["input_tokens"] = 0
            work["output_tokens"] = 0
        self.check_fault(mutate)

    def test_null_is_not_the_original_omitted_false(self):
        self.check_fault(lambda index, entry, report: report.update(verifier_pair_think=None))

    def test_qualification_cannot_be_inferred_from_closed_accounting(self):
        self.check_fault(lambda index, entry, report: entry.update(qualified=True))

    def test_historical_unknowns_cannot_be_removed(self):
        def mutate(index, entry, report):
            index["closed_boundary"]["usage_unknown"] -= 1
        self.check_fault(mutate)


    def test_operational_stop_cannot_claim_server_completion(self):
        def mutate(index, entry, report):
            entry["audit_summary"]["actual_server_delivery_or_execution_completion_proved"] = True
        self.check_fault(mutate, "assessment-memory-stop-pilot24")

    def test_missing_final_resource_sample_cannot_be_fabricated(self):
        def mutate(index, entry, report):
            entry["resource_stop"]["after_child_sample_present"] = True
        self.check_fault(mutate, "assessment-memory-stop-pilot24")

    def test_unscored_operational_stop_cannot_become_correct_rejections(self):
        def mutate(index, entry, report):
            report["totals"]["true_negatives"] = 12
            report["totals"]["confusion_pairs"] = 12
        self.check_fault(mutate, "assessment-memory-stop-pilot24")

    def test_uncertain_native_call_cannot_be_recorded_as_zero_known_work(self):
        def mutate(index, entry, report):
            work = next(w for row in report["pairs"] for w in (row.get("verification") or {}).get("work", []))
            work["input_tokens"] = 0
            work["output_tokens"] = 0
            report["usage"]["verifier_unknown_usage_calls"] = 0
            report["usage"]["verifier_known_usage_calls"] = 1
        self.check_fault(mutate, "assessment-memory-stop-pilot24")


if __name__ == "__main__":
    unittest.main()
