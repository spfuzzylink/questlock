"""Guard the deliberately frozen project identity across future releases."""

import hashlib
from pathlib import Path
import re
import unittest


ROOT = Path(__file__).resolve().parents[1]
CANONICAL_ASSET = "assets/questlock-brand.svg"
CANONICAL_SHA256 = "e8121dd023da8f1d76aa33e633ae03c44fffcc5f7794377e3b2ccb89b145ab29"


class BrandingTests(unittest.TestCase):
    def test_canonical_artwork_bytes_are_unchanged(self):
        actual = hashlib.sha256((ROOT / CANONICAL_ASSET).read_bytes()).hexdigest()
        self.assertEqual(
            actual,
            CANONICAL_SHA256,
            "Upstream canonical branding is frozen; reuse it unchanged. A reviewed "
            "maintainer decision is required before changing the identity or checksum.",
        )

    def test_readme_displays_the_canonical_artwork(self):
        readme = (ROOT / "README.md").read_text(encoding="utf-8")
        references = re.findall(r"!\[[^\]]*\]\(<?([^\s)>]+)>?(?:\s+[^)]*)?\)", readme)
        references.extend(
            re.findall(r"<img\b[^>]*\bsrc=[\"']([^\"']+)[\"']", readme, re.I)
        )
        self.assertTrue(
            any(path.removeprefix("./") == CANONICAL_ASSET for path in references),
            "README must display the canonical assets/questlock-brand.svg image.",
        )

    def test_public_guidance_pins_the_canonical_identity(self):
        for name in ("BRANDING.md",):
            with self.subTest(file=name):
                guidance = (ROOT / name).read_text(encoding="utf-8")
                self.assertIn(CANONICAL_ASSET, guidance)
                self.assertIn(CANONICAL_SHA256, guidance)


if __name__ == "__main__":
    unittest.main()
