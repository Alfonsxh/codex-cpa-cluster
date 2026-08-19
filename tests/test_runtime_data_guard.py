import importlib.util
import hashlib
import sqlite3
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).parents[1]
MODULE_PATH = ROOT / "scripts" / "runtime_data_guard.py"


def load_module():
    spec = importlib.util.spec_from_file_location("runtime_data_guard", MODULE_PATH)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class RuntimeDataGuardTests(unittest.TestCase):
    def setUp(self):
        self.module = load_module()
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        (self.root / "state").mkdir()
        (self.root / "auth" / "alpha").mkdir(parents=True)
        (self.root / "auth" / "alpha" / "oauth.json").write_text("one")
        (self.root / "secrets").mkdir()
        (self.root / "secrets" / "cpa-management.key").write_text("secret")
        self._create_control_database()
        self._create_usage_database()

    def tearDown(self):
        self.temporary.cleanup()

    def _create_control_database(self):
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.executescript(
                """
                CREATE TABLE accounts (
                    id TEXT, email TEXT, port INTEGER,
                    created_at INTEGER, group_enabled INTEGER,
                    default_group INTEGER, position INTEGER
                );
                CREATE TABLE user_routes (user_email TEXT, account_id TEXT);
                CREATE TABLE key_records (
                    sequence INTEGER, label TEXT, account_id TEXT,
                    account_email TEXT, user_email TEXT, status TEXT,
                    secret TEXT, created_at INTEGER, updated_at INTEGER
                );
                CREATE TABLE internal_keys (
                    user_email TEXT, secret TEXT, created_at INTEGER, status TEXT
                );
                CREATE TABLE settings (key TEXT PRIMARY KEY, value_json TEXT);
                INSERT INTO accounts VALUES ('alpha', 'alpha@example.com', 18319, 1, 1, 1, 0);
                INSERT INTO user_routes VALUES ('user@example.com', 'alpha');
                INSERT INTO key_records VALUES (0, 'user:alpha', 'alpha', 'alpha@example.com', 'user@example.com', 'active', 'key', 1, 1);
                INSERT INTO internal_keys VALUES ('user@example.com', 'internal', 1, 'active');
                INSERT INTO settings VALUES ('gateway.listen_address', '"0.0.0.0"');
                INSERT INTO settings VALUES ('portal.session_ttl_seconds', '2592000');
                INSERT INTO settings VALUES ('branding.short_name', '"CPA"');
                """
            )

    def _create_usage_database(self):
        with sqlite3.connect(self.root / "state" / "usage.sqlite3") as connection:
            connection.executescript(
                """
                CREATE TABLE usage_events (id INTEGER PRIMARY KEY, total_tokens INTEGER);
                INSERT INTO usage_events VALUES (1, 10);
                PRAGMA user_version = 7;
                """
            )

    def test_compare_allows_oauth_refresh_and_usage_growth(self):
        before = self.module.snapshot(self.root)
        (self.root / "auth" / "alpha" / "oauth.json").write_text("two")
        with sqlite3.connect(self.root / "state" / "usage.sqlite3") as connection:
            connection.execute("INSERT INTO usage_events VALUES (2, 20)")
        after = self.module.snapshot(self.root)

        result = self.module.compare(before, after)

        self.assertTrue(result["ok"])
        self.assertEqual(
            result["changed_preserved_files"],
            ["auth/alpha/oauth.json"],
        )
        self.assertEqual(result["usage_events"]["count"], 2)

    def test_compare_rejects_missing_runtime_file(self):
        before = self.module.snapshot(self.root)
        (self.root / "auth" / "alpha" / "oauth.json").unlink()

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertFalse(result["ok"])
        self.assertIn("preserved runtime files disappeared", " ".join(result["errors"]))

    def test_compare_rejects_missing_generated_account_config(self):
        (self.root / "configs" / "alpha").mkdir(parents=True)
        config = self.root / "configs" / "alpha" / "config.yaml"
        config.write_text("port: 8317\n")
        before = self.module.snapshot(self.root)
        config.unlink()

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertFalse(result["ok"])
        self.assertIn("configs/alpha/config.yaml", " ".join(result["errors"]))

    def test_compare_allows_one_time_profile_cleanup(self):
        secret = self.root / "secrets" / "deployment-profile.json"
        secret.write_text("{}")
        before = self.module.snapshot(self.root)
        secret.unlink()

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertTrue(result["ok"])

    def test_compare_allows_obsolete_projection_cleanup(self):
        state_projection = self.root / "state/accounts.json"
        secret_projection = self.root / "secrets/user-internal-keys.json"
        state_projection.write_text("{}\n")
        secret_projection.write_text("{}\n")
        before = self.module.snapshot(self.root)
        self.assertNotIn(
            "secrets/user-internal-keys.json",
            before["preserved_files"],
        )
        state_projection.unlink()
        secret_projection.unlink()

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertTrue(result["ok"])

    def test_compare_preserves_logical_secret_during_encrypted_migration(self):
        before = self.module.snapshot(self.root)
        value = (self.root / "secrets" / "cpa-management.key").read_text().strip()
        digest = hashlib.sha256(value.encode("utf-8")).hexdigest()
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                """
                CREATE TABLE encrypted_secrets (
                    name TEXT PRIMARY KEY, nonce BLOB, ciphertext BLOB,
                    value_sha256 TEXT, updated_at INTEGER
                )
                """
            )
            connection.execute(
                "INSERT INTO encrypted_secrets VALUES (?, ?, ?, ?, ?)",
                ("cpa_management_key", b"nonce", b"ciphertext", digest, 1),
            )
        (self.root / "secrets" / "control-plane.key").write_bytes(b"k" * 32)
        (self.root / "secrets" / "cpa-management.key").unlink()

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertTrue(result["ok"])

    def test_compare_rejects_existing_master_key_change(self):
        key = self.root / "secrets" / "control-plane.key"
        key.write_bytes(b"a" * 32)
        before = self.module.snapshot(self.root)
        key.write_bytes(b"b" * 32)

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertFalse(result["ok"])
        self.assertIn("control-plane encryption key changed", " ".join(result["errors"]))

    def test_compare_rejects_key_record_change(self):
        before = self.module.snapshot(self.root)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute("UPDATE key_records SET secret = 'changed'")

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertFalse(result["ok"])
        self.assertIn("control-plane table changed: key_records", result["errors"])

    def test_compare_allows_live_user_route_failover(self):
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                "INSERT INTO accounts VALUES (?, ?, ?, ?, ?, ?, ?)",
                ("beta", "beta@example.com", 18320, 2, 1, 0, 1),
            )
        before = self.module.snapshot(self.root)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                "UPDATE user_routes SET account_id = 'beta' WHERE user_email = 'user@example.com'"
            )

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertTrue(result["ok"])
        self.assertTrue(result["user_routes_changed"])

    def test_compare_rejects_user_route_loss(self):
        before = self.module.snapshot(self.root)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute("DELETE FROM user_routes")

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertFalse(result["ok"])
        self.assertIn("control-plane route users changed: user_routes", result["errors"])

    def test_compare_rejects_route_to_unknown_account(self):
        before = self.module.snapshot(self.root)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute("UPDATE user_routes SET account_id = 'missing'")

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertFalse(result["ok"])
        self.assertIn(
            "control-plane routes reference unknown accounts",
            result["errors"],
        )

    def test_compare_allows_live_account_policy_change(self):
        before = self.module.snapshot(self.root)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                "UPDATE accounts SET group_enabled = 0, default_group = 0 WHERE id = 'alpha'"
            )

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertTrue(result["ok"])
        self.assertTrue(result["account_policy_changed"])

    def test_compare_rejects_live_account_identity_change(self):
        before = self.module.snapshot(self.root)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute("UPDATE accounts SET port = 18321 WHERE id = 'alpha'")

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertFalse(result["ok"])
        self.assertIn("control-plane table changed: accounts", result["errors"])

    def test_compare_rejects_disabled_default_account(self):
        before = self.module.snapshot(self.root)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                "UPDATE accounts SET group_enabled = 0, default_group = 1 WHERE id = 'alpha'"
            )

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertFalse(result["ok"])
        self.assertIn("control-plane table changed: accounts", result["errors"])

    def test_compare_rejects_team_membership_change(self):
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.executescript(
                """
                CREATE TABLE teams (id TEXT, name TEXT);
                CREATE TABLE user_team_memberships (
                    user_email TEXT, team_id TEXT, membership_version INTEGER
                );
                INSERT INTO teams VALUES ('team_alpha', 'Alpha');
                INSERT INTO user_team_memberships
                    VALUES ('user@example.com', 'team_alpha', 1);
                """
            )
        before = self.module.snapshot(self.root)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                "UPDATE user_team_memberships SET membership_version = 2"
            )

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertFalse(result["ok"])
        self.assertIn(
            "control-plane table changed: user_team_memberships",
            result["errors"],
        )

    def test_compare_allows_only_the_bounded_security_settings_migration(self):
        before = self.module.snapshot(self.root)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                "UPDATE settings SET value_json = ? WHERE key = ?",
                ('"127.0.0.1"', "gateway.listen_address"),
            )
            connection.execute(
                "UPDATE settings SET value_json = ? WHERE key = ?",
                ("43200", "portal.session_ttl_seconds"),
            )

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertTrue(result["ok"])
        self.assertTrue(result["security_settings_migrated"])

    def test_compare_allows_retired_settings_with_security_migration(self):
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                "INSERT INTO settings VALUES (?, ?)",
                ("gost.enabled", "true"),
            )
            connection.execute(
                "INSERT INTO settings VALUES (?, ?)",
                ("runtime.gost_image", '"example.invalid/gost:retired"'),
            )
        before = self.module.snapshot(self.root)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute("DELETE FROM settings WHERE key LIKE 'gost.%'")
            connection.execute("DELETE FROM settings WHERE key = 'runtime.gost_image'")
            connection.execute(
                "UPDATE settings SET value_json = ? WHERE key = ?",
                ('"127.0.0.1"', "gateway.listen_address"),
            )

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertTrue(result["ok"])
        self.assertTrue(result["security_settings_migrated"])
        self.assertTrue(result["retired_settings_migrated"])

    def test_compare_allows_bounded_env_to_sqlite_projection_migration(self):
        before = self.module.snapshot(self.root)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                "INSERT INTO settings VALUES (?, ?)",
                ("runtime.cliproxy_image", '"example.invalid/cpa:latest"'),
            )
            connection.execute(
                "INSERT INTO settings VALUES (?, ?)",
                ("gateway.port", "19317"),
            )

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertTrue(result["ok"])
        self.assertFalse(result["security_settings_migrated"])

    def test_compare_rejects_invalid_env_to_sqlite_projection_value(self):
        before = self.module.snapshot(self.root)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                "INSERT INTO settings VALUES (?, ?)",
                ("runtime.cliproxy_image", '"invalid image; command"'),
            )

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertFalse(result["ok"])

    def test_compare_allows_release_metadata_projection_change(self):
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                "INSERT INTO settings VALUES (?, ?)",
                (
                    "delivery.release_metadata_image",
                    '"registry.example.com/cpa-release:old"',
                ),
            )
        before = self.module.snapshot(self.root)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                "UPDATE settings SET value_json = ? WHERE key = ?",
                (
                    '"registry.example.com/cpa-release:latest"',
                    "delivery.release_metadata_image",
                ),
            )

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertTrue(result["ok"])
        self.assertFalse(result["security_settings_migrated"])

    def test_compare_rejects_unrelated_setting_addition(self):
        before = self.module.snapshot(self.root)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                "INSERT INTO settings VALUES (?, ?)",
                ("branding.product_name", '"changed"'),
            )

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertFalse(result["ok"])
        self.assertIn("control-plane table changed: settings", result["errors"])

    def test_compare_rejects_unrelated_setting_change(self):
        before = self.module.snapshot(self.root)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                "UPDATE settings SET value_json = ? WHERE key = ?",
                ('"changed"', "branding.short_name"),
            )

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertFalse(result["ok"])
        self.assertIn("control-plane table changed: settings", result["errors"])

    def test_compare_allows_only_default_inherit_account_proxy_mode_migration(self):
        before = self.module.snapshot(self.root)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                "ALTER TABLE accounts ADD COLUMN proxy_mode TEXT NOT NULL DEFAULT 'inherit'"
            )

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertTrue(result["ok"])
        self.assertTrue(result["account_proxy_mode_migrated"])

    def test_compare_allows_only_retired_account_column_removal(self):
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute("ALTER TABLE accounts ADD COLUMN gost_port INTEGER")
            connection.execute("UPDATE accounts SET gost_port = 16169")
            connection.execute(
                "ALTER TABLE accounts ADD COLUMN proxy_mode TEXT NOT NULL DEFAULT 'inherit'"
            )
        before = self.module.snapshot(self.root)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute("ALTER TABLE accounts DROP COLUMN gost_port")

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertTrue(result["ok"])
        self.assertTrue(result["retired_account_state_migrated"])

    def test_compare_allows_retired_secret_removal_only(self):
        (self.root / "secrets" / "control-plane.key").write_bytes(b"k" * 32)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                """
                CREATE TABLE encrypted_secrets (
                    name TEXT PRIMARY KEY, nonce BLOB, ciphertext BLOB,
                    value_sha256 TEXT, updated_at INTEGER
                )
                """
            )
            connection.execute(
                "INSERT INTO encrypted_secrets VALUES (?, ?, ?, ?, ?)",
                ("gost_tunnel_auth", b"nonce", b"ciphertext", "retired", 1),
            )
            connection.execute(
                "INSERT INTO encrypted_secrets VALUES (?, ?, ?, ?, ?)",
                ("future_secret", b"nonce", b"ciphertext", "preserved", 1),
            )
        before = self.module.snapshot(self.root)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                "DELETE FROM encrypted_secrets WHERE name = 'gost_tunnel_auth'"
            )

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertTrue(result["ok"])
        self.assertTrue(result["retired_secrets_migrated"])

    def test_compare_rejects_account_proxy_mode_business_change(self):
        before = self.module.snapshot(self.root)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                "ALTER TABLE accounts ADD COLUMN proxy_mode TEXT NOT NULL DEFAULT 'inherit'"
            )
            connection.execute(
                "UPDATE accounts SET proxy_mode = 'direct' WHERE id = 'alpha'"
            )

        result = self.module.compare(before, self.module.snapshot(self.root))

        self.assertFalse(result["ok"])
        self.assertIn("control-plane table changed: accounts", result["errors"])

    def test_compare_allows_new_internal_key_but_not_replacement(self):
        before = self.module.snapshot(self.root)
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                "INSERT INTO internal_keys VALUES (?, ?, ?, ?)",
                ("new@example.com", "new-internal", 2, "active"),
            )
        added = self.module.snapshot(self.root)

        self.assertTrue(self.module.compare(before, added)["ok"])

        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                "UPDATE internal_keys SET secret = 'replaced' WHERE user_email = 'user@example.com'"
            )
        replaced = self.module.snapshot(self.root)

        result = self.module.compare(before, replaced)
        self.assertFalse(result["ok"])
        self.assertIn(
            "control-plane table lost or changed rows: internal_keys",
            result["errors"],
        )


if __name__ == "__main__":
    unittest.main()
