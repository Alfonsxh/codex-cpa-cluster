import importlib.util
import json
import os
import sqlite3
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).parents[1]
MODULE_PATH = ROOT / "scripts" / "v2-target-data.py"
SPEC = importlib.util.spec_from_file_location("v2_target_data", MODULE_PATH)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class V2TargetDataTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.base = Path(self.temporary.name)
        self.source = self.base / "live"
        (self.source / "state" / "gateway").mkdir(parents=True)
        (self.source / "state" / "edge").mkdir(parents=True)
        (self.source / "secrets").mkdir()
        (self.source / "logs" / "gateway").mkdir(parents=True)
        (self.source / "configs").mkdir()
        (self.source / "auth").mkdir()
        (self.source / "management").mkdir()
        (self.source / "secrets" / "control-plane.key").write_text(
            "fixture-master-key\n", encoding="utf-8"
        )
        (self.source / "state" / "gateway" / "auth-snapshot.json").write_text(
            '{"version":1,"generation":"fixture","generated_at":1,"records":[]}\n',
            encoding="utf-8",
        )
        (self.source / "state" / "gateway" / "quota-snapshot.json").write_text(
            '{"version":1,"generation":"fixture","generated_at":1,"records":[]}\n',
            encoding="utf-8",
        )
        (self.source / "state" / "gateway" / "quota-heartbeat.json").write_text(
            '{"version":1,"updated_at":1,"ok":true,"error":"","stale_after_seconds":15,"last_success_at":1,"fail_open_after_seconds":300}\n',
            encoding="utf-8",
        )
        (self.source / "state" / "edge" / "active-gateway.conf").write_text(
            "set $active_gateway_backend gateway-blue:8317;\n", encoding="utf-8"
        )
        (self.source / "logs" / "gateway" / "access.tsv").write_text(
            "", encoding="utf-8"
        )
        self._seed_control()
        self._seed_usage()

    def _seed_control(self):
        with sqlite3.connect(self.source / "state" / "control-plane.sqlite3") as connection:
            connection.executescript(
                """
                CREATE TABLE settings (key TEXT PRIMARY KEY, value_json TEXT NOT NULL);
                CREATE TABLE key_records (
                    sequence INTEGER PRIMARY KEY,
                    label TEXT NOT NULL,
                    account_id TEXT NOT NULL,
                    account_email TEXT NOT NULL,
                    user_email TEXT NOT NULL,
                    status TEXT NOT NULL,
                    secret TEXT NOT NULL,
                    created_at INTEGER NOT NULL,
                    updated_at INTEGER NOT NULL
                );
                INSERT INTO settings VALUES ('user_quota.timezone', '"Asia/Shanghai"');
                INSERT INTO settings VALUES ('user_quota.reset_personal_weekly_on_new_week', 'true');
                INSERT INTO key_records VALUES (
                    0, 'fixture', 'alpha', 'alpha@example.com', 'user@example.com',
                    'active', 'fixture-secret-key', 1, 1
                );
                """
            )

    def _seed_usage(self):
        spec = importlib.util.spec_from_file_location(
            "fixture_usage_store", ROOT / "admin" / "usage_store.py"
        )
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        module.UsageStore(
            self.source / "state" / "usage.sqlite3",
            week_timezone="Asia/Shanghai",
        )
        with sqlite3.connect(self.source / "state" / "usage.sqlite3") as connection:
            connection.execute(
                "INSERT INTO usage_events("
                "event_key, account, user_email, key_label, occurred_at, total_tokens"
                ") VALUES (?, ?, ?, ?, ?, ?)",
                ("fixture-event", "alpha", "user@example.com", "fixture", 1, 12),
            )
            connection.execute("PRAGMA user_version = 9")

    def test_snapshot_migration_and_permissions_preserve_keys_and_usage(self):
        target = self.base / "candidate"
        snapshot = MODULE.snapshot_runtime(
            self.source,
            target,
            str(self.source.resolve()),
        )
        self.assertEqual(snapshot["control_integrity"], ["ok"])
        self.assertEqual(snapshot["usage"]["user_version"], 9)
        self.assertEqual(snapshot["usage"]["usage_events"], 1)
        self.assertTrue((target / MODULE.ISOLATED_MARKER).is_file())
        self.assertFalse((target / "state" / "control-plane.sqlite3-wal").exists())

        migrated = MODULE.migrate_usage(
            target,
            ROOT,
            self.base / "backups",
            True,
            "",
        )
        self.assertEqual(migrated["before"]["user_version"], 9)
        self.assertEqual(migrated["after"]["user_version"], 10)
        self.assertEqual(migrated["after"]["usage_events"], 1)
        self.assertEqual(migrated["before"]["usage_event_total_tokens"], 12)
        self.assertEqual(migrated["keys"], snapshot["keys"])
        self.assertTrue(Path(migrated["backup"]).is_file())

        prepared = MODULE.prepare_permissions(target, True, "")
        self.assertEqual(prepared["gateway_group"], 65534)
        self.assertEqual(
            os.stat(target / "state" / "edge" / "active-gateway.conf").st_mode & 0o777,
            0o644,
        )
        self.assertEqual(
            os.stat(target / "logs" / "gateway" / "access.tsv").st_mode & 0o777,
            0o660,
        )

    def test_snapshot_refuses_overwrite_symlink_and_unconfirmed_live_mutation(self):
        target = self.base / "existing"
        target.mkdir()
        with self.assertRaisesRegex(RuntimeError, "overwrite"):
            MODULE.snapshot_runtime(self.source, target, str(self.source.resolve()))

        with self.assertRaisesRegex(RuntimeError, "live mutation requires"):
            MODULE.prepare_permissions(self.source, False, "")

        linked_source = self.base / "linked"
        linked_source.mkdir()
        os.symlink(self.source / "state", linked_source / "state")
        (linked_source / "secrets").mkdir()
        (linked_source / "secrets" / "control-plane.key").write_text(
            "fixture", encoding="utf-8"
        )
        with self.assertRaisesRegex(RuntimeError, "symlink"):
            MODULE.snapshot_runtime(
                linked_source,
                self.base / "linked-target",
                str(linked_source.resolve()),
            )


if __name__ == "__main__":
    unittest.main()
