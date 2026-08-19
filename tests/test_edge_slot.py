import tempfile
import unittest
from pathlib import Path

from scripts import edge_slot


class EdgeSlotTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)

    def tearDown(self):
        self.temporary.cleanup()

    def test_missing_file_uses_only_explicit_valid_fallback(self):
        self.assertEqual(edge_slot.read_active_slot(self.root, fallback="blue"), "blue")
        with self.assertRaisesRegex(ValueError, "missing"):
            edge_slot.read_active_slot(self.root)
        with self.assertRaisesRegex(ValueError, "blue or green"):
            edge_slot.read_active_slot(self.root, fallback="legacy")

    def test_write_is_atomic_and_round_trips_both_slots(self):
        path = edge_slot.write_active_slot(self.root, "blue")
        self.assertEqual(edge_slot.read_active_slot(self.root), "blue")
        self.assertEqual(path.stat().st_mode & 0o777, 0o644)
        edge_slot.write_active_slot(self.root, "green")
        self.assertEqual(edge_slot.read_active_slot(self.root), "green")
        self.assertEqual(edge_slot.inactive_slot("green"), "blue")

    def test_ensure_creates_missing_file_without_overwriting_valid_selection(self):
        self.assertEqual(edge_slot.ensure_active_slot(self.root, fallback="green"), "green")
        self.assertEqual(edge_slot.read_active_slot(self.root), "green")
        self.assertEqual(edge_slot.ensure_active_slot(self.root, fallback="blue"), "green")

    def test_reader_rejects_additional_or_unsafe_nginx_directives(self):
        path = self.root / edge_slot.ACTIVE_CONFIG
        path.parent.mkdir(parents=True)
        for content in (
            "set $active_gateway_backend gateway-blue:8317;\nreturn 200;\n",
            "set $active_gateway_backend attacker:8317;\n",
            "set $active_gateway_backend gateway-blue:8317$request_uri;\n",
        ):
            path.write_text(content, encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "directive"):
                edge_slot.read_active_slot(self.root)

    def test_reader_rejects_symlinked_slot_file(self):
        external = self.root / "external.conf"
        external.write_text(edge_slot.render("blue"), encoding="utf-8")
        path = self.root / edge_slot.ACTIVE_CONFIG
        path.parent.mkdir(parents=True)
        path.symlink_to(external)
        with self.assertRaisesRegex(ValueError, "symlink"):
            edge_slot.ensure_active_slot(self.root)


if __name__ == "__main__":
    unittest.main()
