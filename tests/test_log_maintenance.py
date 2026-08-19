import tempfile
import unittest
from pathlib import Path


from admin import log_maintenance


class LogMaintenanceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        for relative in log_maintenance.DEFAULT_TARGETS:
            path = self.root / relative
            path.parent.mkdir(parents=True, exist_ok=True)

    def tearDown(self):
        self.temporary.cleanup()

    def test_run_once_copy_truncates_and_bounds_backups(self):
        path = self.root / "logs/gateway/access.tsv"
        path.write_bytes(b"a" * (1024 * 1024 + 1))

        first = log_maintenance.run_once(
            self.root,
            max_file_size_mb=1,
            backups=2,
            now=100,
        )

        self.assertEqual(path.stat().st_size, 0)
        self.assertEqual(path.with_name("access.tsv.1").stat().st_size, 1024 * 1024 + 1)
        self.assertEqual(first["last_rotated"], ["logs/gateway/access.tsv"])
        self.assertTrue(log_maintenance.healthy(self.root, now=101))

        path.write_bytes(b"b" * (1024 * 1024 + 2))
        log_maintenance.run_once(
            self.root,
            max_file_size_mb=1,
            backups=2,
            now=102,
        )
        path.write_bytes(b"c" * (1024 * 1024 + 3))
        log_maintenance.run_once(
            self.root,
            max_file_size_mb=1,
            backups=2,
            now=103,
        )

        self.assertEqual(path.with_name("access.tsv.1").read_bytes()[:1], b"c")
        self.assertEqual(path.with_name("access.tsv.2").read_bytes()[:1], b"b")
        self.assertFalse(path.with_name("access.tsv.3").exists())
        state = log_maintenance.ControlPlaneStore(self.root).read_runtime_state(
            "log_maintenance"
        )
        self.assertEqual(state["rotations"], 3)

    def test_small_or_missing_logs_are_left_untouched(self):
        path = self.root / "logs/admin/audit.jsonl"
        path.write_text("ok\n", encoding="utf-8")

        payload = log_maintenance.run_once(
            self.root,
            max_file_size_mb=1,
            backups=2,
            now=200,
        )

        self.assertEqual(path.read_text(encoding="utf-8"), "ok\n")
        self.assertEqual(payload["last_rotated"], [])
        self.assertEqual(payload["last_error"], "")
        self.assertFalse(log_maintenance.healthy(self.root, now=501))


if __name__ == "__main__":
    unittest.main()
