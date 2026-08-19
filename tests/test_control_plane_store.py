import hashlib
import os
import sqlite3
import tempfile
import threading
import time
import unittest
from pathlib import Path

try:
    from fixtures import seed_control_plane
except ImportError:
    from tests.fixtures import seed_control_plane


ROOT = Path(__file__).parents[1]
import sys
sys.path.insert(0, str(ROOT / "scripts"))
from control_plane_store import ControlPlaneStore


class ControlPlaneStoreTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        seed_control_plane(self.root, domains=["example.com", "example.org"])

    def tearDown(self):
        self.temporary.cleanup()

    def test_sqlite_state_persists_across_restart_without_json_projections(self):
        store = ControlPlaneStore(self.root)
        routes = {"alice@example.com": "alpha"}
        store.write_routes(routes)
        store.write_runtime_state("deployment", {"commit": "abc123"})

        self.assertEqual([item["id"] for item in store.read_accounts()], ["alpha", "beta", "gamma", "delta"])
        self.assertEqual(
            store.read_settings()["identity.allowed_email_domains"],
            ["example.com", "example.org"],
        )
        self.assertEqual(store.path.stat().st_mode & 0o777, 0o600)
        restarted = ControlPlaneStore(self.root)
        self.assertEqual(restarted.read_routes(), routes)
        self.assertEqual(restarted.read_runtime_state("deployment"), {"commit": "abc123"})
        self.assertTrue(restarted.verify()["ok"])
        self.assertTrue(
            all(not (self.root / relative).exists() for relative in self.module_obsolete_paths())
        )

    def test_schema_upgrade_adds_inheriting_proxy_mode_without_losing_accounts(self):
        legacy_root = self.root / "legacy-proxy-schema"
        (legacy_root / "state").mkdir(parents=True)
        with sqlite3.connect(str(legacy_root / "state/control-plane.sqlite3")) as connection:
            connection.execute(
                """
                CREATE TABLE accounts (
                    id TEXT PRIMARY KEY, email TEXT NOT NULL UNIQUE,
                    port INTEGER NOT NULL UNIQUE, gost_port INTEGER,
                    created_at INTEGER NOT NULL, group_enabled INTEGER NOT NULL,
                    default_group INTEGER NOT NULL, position INTEGER NOT NULL
                )
                """
            )
            connection.execute(
                "INSERT INTO accounts VALUES ('alpha','alpha@example.com',18319,NULL,1,1,1,0)"
            )
            connection.execute(
                "CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)"
            )
            connection.execute("INSERT INTO schema_migrations VALUES (5, 1)")
            connection.execute(
                """
                CREATE TABLE encrypted_secrets (
                    name TEXT PRIMARY KEY, nonce BLOB NOT NULL,
                    ciphertext BLOB NOT NULL, value_sha256 TEXT NOT NULL,
                    updated_at INTEGER NOT NULL
                )
                """
            )
            connection.execute(
                "INSERT INTO encrypted_secrets VALUES ('gost_tunnel_auth', X'00', X'00', 'unused', 1)"
            )
            connection.execute(
                "INSERT INTO encrypted_secrets VALUES ('future_secret', X'00', X'00', 'preserved', 1)"
            )
        (legacy_root / "secrets").mkdir()
        key_path = legacy_root / "secrets/control-plane.key"
        key_path.write_bytes(b"k" * 32)
        key_path.chmod(0o600)

        store = ControlPlaneStore(legacy_root)

        self.assertEqual(store.read_accounts()[0]["proxy_mode"], "inherit")
        with sqlite3.connect(str(store.path)) as connection:
            columns = {
                row[1] for row in connection.execute("PRAGMA table_info(accounts)")
            }
        self.assertIn("proxy_mode", columns)
        self.assertNotIn("gost_port", columns)
        self.assertNotIn("gost_tunnel_auth", store.secret_status())
        self.assertIn("future_secret", store.secret_status())

    @staticmethod
    def module_obsolete_paths():
        from control_plane_store import OBSOLETE_PROJECTION_PATHS

        return OBSOLETE_PROJECTION_PATHS

    def test_cleanup_removes_only_obsolete_projection_files(self):
        store = ControlPlaneStore(self.root)
        for relative in self.module_obsolete_paths():
            path = self.root / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("{}\n", encoding="utf-8")
        public = self.root / "state/public/accounts.json"
        public.parent.mkdir(parents=True, exist_ok=True)
        public.write_text("{}\n", encoding="utf-8")
        store.write_metadata("legacy_import_complete", "1")
        for relative in self.module_obsolete_paths():
            store.write_metadata(
                "legacy_hash:" + relative,
                hashlib.sha256((self.root / relative).read_bytes()).hexdigest(),
            )

        self.assertFalse(store.verify()["ok"])
        result = store.cleanup_obsolete_projections()

        self.assertEqual(result["cleaned"], sorted(self.module_obsolete_paths()))
        self.assertEqual(result["remaining"], [])
        self.assertEqual(result["metadata_cleaned"], 10)
        self.assertTrue(public.exists())
        self.assertTrue(store.verify()["ok"])

    def test_cleanup_refuses_json_changed_after_last_database_sync(self):
        store = ControlPlaneStore(self.root)
        obsolete = self.root / "state/accounts.json"
        obsolete.write_text('{"accounts":[]}\n', encoding="utf-8")
        store.write_metadata("legacy_import_complete", "1")
        store.write_metadata(
            "legacy_hash:state/accounts.json",
            hashlib.sha256(obsolete.read_bytes()).hexdigest(),
        )
        obsolete.write_text('{"accounts":[{"id":"new"}]}\n', encoding="utf-8")

        with self.assertRaisesRegex(ValueError, "最后一次数据库同步后发生变化"):
            store.cleanup_obsolete_projections()

        self.assertTrue(obsolete.exists())

    def test_cleanup_refuses_unproven_json_only_migration(self):
        isolated = self.root / "unproven"
        store = ControlPlaneStore(isolated)
        obsolete = isolated / "state/accounts.json"
        obsolete.write_text('{"accounts":[]}\n', encoding="utf-8")

        with self.assertRaisesRegex(ValueError, "没有旧 JSON 导入/清理记录"):
            store.cleanup_obsolete_projections()

        self.assertTrue(obsolete.exists())

    def test_online_backup_is_a_readable_consistent_database(self):
        store = ControlPlaneStore(self.root)
        destination = self.root / "backups" / "control-plane.sqlite3"

        store.backup_to(destination)

        with sqlite3.connect(str(destination)) as connection:
            count = connection.execute("SELECT COUNT(*) FROM accounts").fetchone()[0]
        self.assertEqual(count, 4)
        self.assertEqual(destination.stat().st_mode & 0o777, 0o600)

    def test_team_membership_and_tags_are_normalized_and_persistent(self):
        store = ControlPlaneStore(self.root)
        team = store.create_team("  Platform   Team ", " Core platform owners ", now=100)
        tag = store.create_tag("Pilot", "#336699", now=101)

        assignment = store.set_user_teams(
            ["Alice@Example.com"], team["id"], now=102
        )[0]
        store.set_user_tags("Alice@Example.com", [tag["id"]], now=103)
        classification = store.read_user_classifications(
            ["alice@example.com", "idle@example.com"]
        )

        self.assertEqual(team["name"], "Platform Team")
        self.assertEqual(assignment["membership_version"], 1)
        self.assertEqual(classification["alice@example.com"]["team_id"], team["id"])
        self.assertEqual(classification["alice@example.com"]["tags"][0]["name"], "Pilot")
        self.assertIsNone(classification["idle@example.com"]["team_id"])
        with self.assertRaisesRegex(ValueError, "仍有 1 位用户"):
            store.delete_team(team["id"])

        unassigned = store.set_user_teams(
            ["alice@example.com"], None, now=104
        )[0]
        self.assertEqual(unassigned["membership_version"], 2)
        self.assertTrue(store.delete_team(team["id"])["deleted"])
        self.assertTrue(store.delete_tag(tag["id"])["deleted"])
        restarted = ControlPlaneStore(self.root)
        self.assertEqual(restarted.list_teams(), [])
        self.assertEqual(
            restarted.read_user_classifications(["alice@example.com"])[
                "alice@example.com"
            ]["tags"],
            [],
        )

    def test_initialization_waits_for_inflight_database_write(self):
        store = ControlPlaneStore(self.root)
        records = store.read_accounts()
        records[0] = {**records[0], "email": "updated-alpha@accounts.example.com"}
        write_started = threading.Event()
        allow_write = threading.Event()
        original_replace = store._replace_accounts

        def delayed_replace(connection, replacement):
            write_started.set()
            self.assertTrue(allow_write.wait(timeout=2))
            original_replace(connection, replacement)

        store._replace_accounts = delayed_replace
        writer = threading.Thread(target=store.write_accounts, args=(records,))
        writer.start()
        self.assertTrue(write_started.wait(timeout=2))

        restarted = []
        reader = threading.Thread(
            target=lambda: restarted.append(ControlPlaneStore(self.root))
        )
        reader.start()
        time.sleep(0.05)
        self.assertTrue(reader.is_alive())
        allow_write.set()
        writer.join(timeout=2)
        reader.join(timeout=2)

        self.assertFalse(writer.is_alive())
        self.assertFalse(reader.is_alive())
        self.assertEqual(
            restarted[0].read_accounts()[0]["email"],
            "updated-alpha@accounts.example.com",
        )

    def test_legacy_secrets_are_encrypted_and_plaintext_files_can_be_cleaned(self):
        secrets = self.root / "secrets"
        secrets.mkdir(parents=True, exist_ok=True)
        legacy = {
            "cpa-management.key": "test-management-key",
            "wecom-webhook.url": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test-placeholder",
        }
        for name, value in legacy.items():
            path = secrets / name
            path.write_text(value + "\n", encoding="utf-8")
            os.chmod(path, 0o600)

        store = ControlPlaneStore(self.root)

        self.assertEqual(store.read_secret("cpa_management_key"), "test-management-key")
        self.assertEqual(store.encryption_key_path.stat().st_mode & 0o777, 0o600)
        database_bytes = store.path.read_bytes()
        self.assertNotIn(b"test-management-key", database_bytes)
        result = store.migrate_legacy_secrets(cleanup=True)
        self.assertEqual(len(result["cleaned"]), 2)
        self.assertTrue(all(not (secrets / name).exists() for name in legacy))

    def test_encrypted_secrets_refuse_start_without_original_master_key(self):
        store = ControlPlaneStore(self.root)
        store.write_secret("cpa_management_key", "test-management-key")
        store.encryption_key_path.unlink()

        with self.assertRaisesRegex(ValueError, "加密主密钥缺失"):
            ControlPlaneStore(self.root)
