import sys
import unittest
from pathlib import Path


ROOT = Path(__file__).parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
from branding import validate_logo


class BrandingTests(unittest.TestCase):
    def test_safe_svg_and_raster_signatures_are_supported(self):
        svg = b'<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0h10v10z"/></svg>'
        result = validate_logo("brand.svg", "image/svg+xml", svg)
        self.assertEqual(result["content_type"], "image/svg+xml")
        self.assertEqual(result["filename"], "brand.svg")

        png = validate_logo("brand.png", "image/png", b"\x89PNG\r\n\x1a\nfixture")
        self.assertEqual(png["content_type"], "image/png")

    def test_svg_rejects_script_event_and_external_content(self):
        unsafe = (
            b'<svg xmlns="http://www.w3.org/2000/svg">'
            b'<script>alert(1)</script><a href="https://example.com"><path d="M0 0"/></a></svg>'
        )
        with self.assertRaisesRegex(ValueError, "不允许|外部"):
            validate_logo("unsafe.svg", "image/svg+xml", unsafe)

        event = b'<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>'
        with self.assertRaisesRegex(ValueError, "事件"):
            validate_logo("event.svg", "image/svg+xml", event)

        external_paint = (
            b'<svg xmlns="http://www.w3.org/2000/svg">'
            b'<path fill="url(https://example.com/p.svg)"/></svg>'
        )
        with self.assertRaisesRegex(ValueError, "外部资源"):
            validate_logo("external.svg", "image/svg+xml", external_paint)

        animation = (
            b'<svg xmlns="http://www.w3.org/2000/svg">'
            b'<animate attributeName="href" values="#safe;javascript:alert(1)"/></svg>'
        )
        with self.assertRaisesRegex(ValueError, "不允许的元素"):
            validate_logo("animation.svg", "image/svg+xml", animation)
