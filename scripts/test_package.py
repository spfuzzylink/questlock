"""Release-boundary tests: no host metadata or accidental private inputs."""

import importlib.util
from pathlib import Path
import tarfile
import tempfile
import unittest


spec = importlib.util.spec_from_file_location("package", Path(__file__).with_name("package.py"))
package = importlib.util.module_from_spec(spec)
spec.loader.exec_module(package)


class PackageTests(unittest.TestCase):
    def test_inference_release_material_is_packaged_without_private_inputs(self):
        required = {
            "docs/execution-validation-v0.10.0.md", "docs/execution-validation-v0.10.0.json",
            "docs/go-integration.md", "examples/execution/README.md",
            "examples/execution/main.go", "examples/execution/local-task.json",
            "examples/execution/sglang-task.json", "examples/execution/hosted-budget.example.json",
            "examples/execution/admission.json",
            "docs/local-agent-teams.md", "docs/diagrams/local-agent-teams.drawio",
            "docs/diagrams/local-agent-teams.svg", "examples/teams/README.md",
            "docs/local-team-validation-v0.9.0.md", "docs/local-team-validation-v0.9.0.json",
            "assets/questlock-brand.svg", "docs/onboarding.md", "docs/success-report-v0.7.0.md",
            "docs/diagrams/onboarding.drawio", "docs/diagrams/onboarding.svg",
            "docs/second-gate.md", "docs/diagrams/second-gate.drawio", "docs/diagrams/second-gate.svg",
            "examples/edge/README.md",
            "docs/adaptive-admission.md",
            "docs/system-one.md",
            "docs/diagrams/system-one.drawio",
            "docs/diagrams/system-one.svg",
            "optional/system-one/requirements.txt",
            "optional/system-one/TypeSafe-LICENSE",
            "optional/system-one/TypeSafe-SDK-LICENSE",
            "optional/system-one/reuse.json",
            "optional/system-one/route.json",
            "optional/system-one/approved-local-task.json",
            "docs/local-coordination.md",
            "docs/diagrams/local-coordination.drawio",
            "docs/diagrams/local-coordination.svg",
            "examples/preferences/README.md",
            "examples/preferences/local-only.request.json",
            "examples/preferences/personal-instructions.example.md",
            "docs/inference-observability.md", "docs/delegation.md", "docs/measurements-v0.3.0.md",
            "assets/questlock-v0.3.0.svg",
            "assets/questlock-v0.4.0.svg",
            "assets/questlock-v0.5.0.svg", "docs/runtime-cache.md",
            "assets/questlock-v0.6.0.svg", "docs/checked-revisions.md",
            "docs/diagrams/checked-revisions.drawio", "docs/diagrams/checked-revisions.svg",
            "examples/revisions/README.md", "examples/revisions/bold-product.json",
            "examples/revisions/bold-mission.json", "examples/revisions/json-status.json",
            "examples/revisions/comparison.json",
            "docs/diagrams/runtime-layers.drawio", "docs/diagrams/runtime-layers.svg",
            "deploy/sglang/README.md", "deploy/sglang/dense.json", "deploy/sglang/hicache.json",
            "deploy/sglang/hisparse.json", "deploy/sglang/hisparse-prefill.json", "deploy/sglang/task.json",
            "examples/inference/openrouter-summary.json",
            "docs/diagrams/inference-runtime.drawio", "docs/diagrams/inference-runtime.svg",
            "evaluation/fixtures/quickstart.json",
            "docs/outcomes.md", "docs/diagrams/human-outcomes.drawio", "docs/diagrams/human-outcomes.svg",
            "examples/outcomes/README.md", "examples/outcomes/completed-run.json", "examples/outcomes/accept.json",
        }
        self.assertTrue(required.issubset(package.PUBLIC_FILES))
        self.assertEqual(len(package.PUBLIC_FILES), len(set(package.PUBLIC_FILES)))
        self.assertEqual(package.TARGETS, ("darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"))
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            for name in package.PUBLIC_FILES:
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(("public fixture: " + name).encode())
            for name in ("scripts/langfuse/private.env", "scripts/langfuse/request.json",
                         "evaluation/fixtures/private-request.json", "evaluation/fixtures/nested/quickstart.json"):
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("private fixture never packaged")
            documents = {name: package.public_file(root, name) for name in package.PUBLIC_FILES}
            output = root / "release.tar.gz"
            package.archive(output, b"binary fixture", documents, {"version": "0.3.0"})
            with tarfile.open(output) as bundle:
                self.assertEqual(set(bundle.getnames()), {"questlock", "BUILDINFO.json", *package.PUBLIC_FILES})
                for name in required:
                    self.assertEqual(bundle.extractfile(name).read(), documents[name])

    def test_release_allowlist_preserves_third_party_notices_verbatim(self):
        self.assertIn("THIRD_PARTY_NOTICES.md", package.PUBLIC_FILES)
        notice = package.public_file(package.ROOT, "THIRD_PARTY_NOTICES.md")
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / "release.tar.gz"
            package.archive(output, b"binary fixture", {"THIRD_PARTY_NOTICES.md": notice}, {"version": "0.1.1"})
            with tarfile.open(output) as bundle:
                self.assertEqual(bundle.extractfile("THIRD_PARTY_NOTICES.md").read(), notice)

    def test_archive_has_only_explicit_inputs_and_normalized_ownership(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / "private.token").write_text("local fixture never packaged")
            first, second = root / "first.tar.gz", root / "second.tar.gz"
            for output in (first, second):
                package.archive(output, b"binary fixture", {"LICENSE": b"license"}, {"version": "0.1.0"})
            self.assertEqual(first.read_bytes(), second.read_bytes())
            with tarfile.open(first) as bundle:
                self.assertEqual(bundle.getnames(), ["questlock", "LICENSE", "BUILDINFO.json"])
                for entry in bundle:
                    self.assertTrue(entry.isfile())
                    self.assertEqual((entry.uid, entry.gid, entry.mtime, entry.uname, entry.gname),
                                     (0, 0, 0, "", ""))
                self.assertEqual(bundle.getmember("questlock").mode, 0o755)
                self.assertEqual(bundle.extractfile("questlock").read(), b"binary fixture")

    def test_symlinked_public_inputs_cannot_copy_private_files(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / "private.txt").write_text("private fixture")
            (root / "README.md").symlink_to("private.txt")
            with self.assertRaises(ValueError):
                package.public_file(root, "README.md")
            (root / "real-docs").mkdir()
            (root / "real-docs/installation.md").write_text("private fixture")
            (root / "docs").symlink_to("real-docs", target_is_directory=True)
            with self.assertRaises(ValueError):
                package.public_file(root, "docs/installation.md")


if __name__ == "__main__":
    unittest.main()
