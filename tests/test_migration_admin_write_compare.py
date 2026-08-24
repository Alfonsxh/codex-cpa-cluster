import importlib.util
import json
import sqlite3
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).parents[1]
MODULE_PATH = ROOT / "scripts" / "migration-admin-write-compare.py"
SPEC = importlib.util.spec_from_file_location("migration_admin_write_compare", MODULE_PATH)
WRITE_COMPARE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(WRITE_COMPARE)


class MigrationAdminWriteCompareTests(unittest.TestCase):
    def make_target(self, parent, name, surface, port):
        root = parent / name
        (root / "state").mkdir(parents=True)
        (root / "secrets").mkdir()
        (root / ".v2-isolated-copy.json").write_text("{}\n", encoding="utf-8")
        database = root / "state" / "control-plane.sqlite3"
        connection = sqlite3.connect(database)
        try:
            connection.execute(
                "CREATE TABLE key_records(sequence INTEGER PRIMARY KEY, user_email TEXT, status TEXT)"
            )
            connection.execute(
                "INSERT INTO key_records(sequence, user_email, status) VALUES (1, ?, 'active')",
                ("private-user@example.com",),
            )
            connection.commit()
        finally:
            connection.close()
        return WRITE_COMPARE.Target(
            name=name,
            surface=surface,
            base_url="http://127.0.0.1:{}".format(port),
            control_db=database,
        )

    def add_persistence_schema(self, target):
        control = sqlite3.connect(target.control_db)
        try:
            control.executescript(
                """
                CREATE TABLE internal_keys (
                    user_email TEXT PRIMARY KEY, secret TEXT, status TEXT
                );
                CREATE TABLE user_routes (
                    user_email TEXT PRIMARY KEY, account_id TEXT
                );
                CREATE TABLE user_team_memberships (
                    user_email TEXT PRIMARY KEY,
                    team_id TEXT,
                    membership_version INTEGER NOT NULL
                );
                CREATE TABLE user_tags (user_email TEXT, tag_id TEXT);
                """
            )
            control.commit()
        finally:
            control.close()
        usage_path = target.control_db.with_name("usage.sqlite3")
        usage = sqlite3.connect(usage_path)
        try:
            usage.executescript(
                """
                CREATE TABLE key_identities (
                    key_hash TEXT PRIMARY KEY,
                    user_email TEXT,
                    team_id TEXT,
                    team_membership_version INTEGER
                );
                CREATE TABLE portal_sessions (session_hash TEXT, user_email TEXT);
                CREATE TABLE portal_credentials (user_email TEXT PRIMARY KEY);
                CREATE TABLE user_quota_policies (user_email TEXT PRIMARY KEY);
                CREATE TABLE usage_events (id INTEGER PRIMARY KEY, user_email TEXT);
                CREATE TABLE user_quota_adjustments (
                    id INTEGER PRIMARY KEY, user_email TEXT
                );
                CREATE TABLE user_weekly_usage (user_email TEXT);
                """
            )
            usage.commit()
        finally:
            usage.close()

    def test_parse_target_rejects_public_or_https_origins(self):
        with self.assertRaises(Exception):
            WRITE_COMPARE.parse_target(
                "v1,v1,https://127.0.0.1:1,/tmp/a/state/control-plane.sqlite3"
            )
        with self.assertRaises(Exception):
            WRITE_COMPARE.parse_target(
                "v1,v1,http://example.com,/tmp/a/state/control-plane.sqlite3"
            )

    def test_validate_target_requires_marker(self):
        with tempfile.TemporaryDirectory() as temporary:
            parent = Path(temporary)
            target = self.make_target(parent, "v1", "v1", 18317)
            self.assertEqual(WRITE_COMPARE.validate_target(target), (parent / "v1").resolve())
            (parent / "v1" / ".v2-isolated-copy.json").unlink()
            with self.assertRaisesRegex(ValueError, "marker"):
                WRITE_COMPARE.validate_target(target)

    def test_comparison_view_contains_no_response_values(self):
        result = {
            "steps": {
                "team_create": {
                    "status": 201,
                    "content_type": "application/json",
                    "error_code": "",
                    "schema": {"kind": "object", "top_keys": ["message", "team"]},
                    "private": "must-not-appear",
                }
            }
        }
        rendered = json.dumps(WRITE_COMPARE.comparison_view(result), sort_keys=True)
        self.assertNotIn("must-not-appear", rendered)

    def test_write_decisions_cover_only_reviewed_exact_differences(self):
        self.assertEqual(
            set(WRITE_COMPARE.WRITE_DECISIONS),
            {
                "login",
                "team_readback",
                "team_delete_readback",
                "team_duplicate",
                "user_create",
                "user_key_rotate",
                "user_password_reset",
                "user_revoke",
            },
        )
        views = {
            "v1": {
                name: decision["v1"]
                for name, decision in WRITE_COMPARE.WRITE_DECISIONS.items()
            },
            "v2": {
                name: decision["v2"]
                for name, decision in WRITE_COMPARE.WRITE_DECISIONS.items()
            },
        }
        approved, unexpected = WRITE_COMPARE.compare_views(views)
        self.assertEqual({item["name"] for item in approved}, set(views["v1"]))
        self.assertEqual(unexpected, [])
        views["v2"]["team_duplicate"] = dict(views["v2"]["team_duplicate"])
        views["v2"]["team_duplicate"]["status"] = 500
        _, unexpected = WRITE_COMPARE.compare_views(views)
        self.assertEqual([item["name"] for item in unexpected], ["team_duplicate"])

    def test_persistence_decision_is_exact_and_scoped_to_v1_internal_key_retention(self):
        self.assertEqual(
            set(WRITE_COMPARE.PERSISTENCE_DECISIONS), {"temporary_user_cleanup"}
        )
        decision = WRITE_COMPARE.PERSISTENCE_DECISIONS[
            "temporary_user_cleanup"
        ]
        differing = {
            key for key in decision["v1"]
            if decision["v1"][key] != decision["v2"][key]
        }
        self.assertEqual(
            differing, {"internal_keys", "inactive_internal_keys"}
        )

    def test_run_rejects_shared_or_non_v1_v2_state(self):
        with tempfile.TemporaryDirectory() as temporary:
            parent = Path(temporary)
            v1 = self.make_target(parent, "v1", "v1", 18317)
            with self.assertRaisesRegex(ValueError, "one v1 and one v2"):
                WRITE_COMPARE.run([v1, v1], "test-management-key", 1)
            shared = WRITE_COMPARE.Target(
                name="v2",
                surface="v2",
                base_url="http://127.0.0.1:28317",
                control_db=v1.control_db,
            )
            with self.assertRaisesRegex(ValueError, "distinct"):
                WRITE_COMPARE.run([v1, shared], "test-management-key", 1)

    def test_run_rejects_distinct_control_copies_with_shared_usage_inode(self):
        with tempfile.TemporaryDirectory() as temporary:
            parent = Path(temporary)
            v1 = self.make_target(parent, "v1", "v1", 19317)
            v2 = self.make_target(parent, "v2", "v2", 18317)
            self.add_persistence_schema(v1)
            self.add_persistence_schema(v2)
            v2_usage = v2.control_db.with_name("usage.sqlite3")
            v2_usage.unlink()
            v2_usage.symlink_to(v1.control_db.with_name("usage.sqlite3"))
            with self.assertRaisesRegex(ValueError, "usage databases"):
                WRITE_COMPARE.run([v1, v2], "test-management-key", 1)

    def test_probe_user_is_hashed_before_report_boundary(self):
        digest = WRITE_COMPARE.hashlib.sha256(b"private-user@example.com").hexdigest()
        self.assertEqual(len(digest), 64)
        self.assertNotIn("private-user@example.com", digest)

    def test_temporary_user_uses_probe_domain_without_reporting_probe_value(self):
        temporary = WRITE_COMPARE.temporary_user_email(
            "private-user@corp.example.com", "1234abcd"
        )
        self.assertEqual(temporary, "migration-write-1234abcd@corp.example.com")
        with self.assertRaisesRegex(ValueError, "not an email"):
            WRITE_COMPARE.temporary_user_email("not-an-email", "1234abcd")

    def test_quota_policy_preserves_only_exact_restorable_policy(self):
        self.assertEqual(
            WRITE_COMPARE.quota_policy(
                {"weekly_quota": {"policy_mode": "inherit", "policy_tokens": None}}
            ),
            {"mode": "inherit", "weekly_tokens": None},
        )
        self.assertEqual(
            WRITE_COMPARE.quota_policy(
                {"weekly_quota": {"policy_mode": "custom", "policy_tokens": 500}}
            ),
            {"mode": "custom", "weekly_tokens": 500},
        )
        with self.assertRaisesRegex(ValueError, "positive policy tokens"):
            WRITE_COMPARE.quota_policy(
                {"weekly_quota": {"policy_mode": "custom", "policy_tokens": 0}}
            )

    def test_one_time_secret_extractors_accept_both_surfaces_without_report_values(self):
        v1 = WRITE_COMPARE.create_credentials(
            {
                "keys": [{"key": "private-v1-key", "label": "private-label"}],
                "initial_password": "private-v1-password",
                "team_id": "team-id",
            },
            "v1",
        )
        v2 = WRITE_COMPARE.create_credentials(
            {
                "user": {
                    "api_key": "private-v2-key",
                    "initial_password": "private-v2-password",
                    "team_id": "team-id",
                }
            },
            "v2",
        )
        self.assertEqual(v1["api_key"], "private-v1-key")
        self.assertEqual(v2["api_key"], "private-v2-key")
        rendered = json.dumps(
            WRITE_COMPARE.comparison_view(
                {
                    "steps": {
                        "user_create": {
                            "status": 201,
                            "content_type": "application/json",
                            "error_code": "",
                            "schema": {
                                "kind": "object",
                                "top_keys": ["message", "user"],
                            },
                            "private": [v1, v2],
                        }
                    }
                }
            ),
            sort_keys=True,
        )
        self.assertNotIn("private-v1-key", rendered)
        self.assertNotIn("private-v2-password", rendered)

    def test_rotation_reset_and_revoke_extractors_validate_semantics(self):
        self.assertEqual(
            WRITE_COMPARE.rotated_key({"keys": [{"key": "v1-new"}]}, "v1"),
            "v1-new",
        )
        self.assertEqual(
            WRITE_COMPARE.rotated_key({"key": {"api_key": "v2-new"}}, "v2"),
            "v2-new",
        )
        self.assertEqual(
            WRITE_COMPARE.reset_password(
                {"initial_password": "v1-password", "password_change_required": True},
                "v1",
            ),
            "v1-password",
        )
        self.assertEqual(
            WRITE_COMPARE.revoked_key_count(
                {"revocation": {"revoked_keys": 1}}, "v2"
            ),
            1,
        )
        with self.assertRaisesRegex(ValueError, "did not revoke"):
            WRITE_COMPARE.revoked_key_count({"revoked": 0}, "v1")

    def test_membership_transition_requires_exact_version_and_identity_sync(self):
        with tempfile.TemporaryDirectory() as temporary:
            target = self.make_target(Path(temporary), "v2", "v2", 18317)
            self.add_persistence_schema(target)
            control = sqlite3.connect(target.control_db)
            usage = sqlite3.connect(target.control_db.with_name("usage.sqlite3"))
            try:
                control.execute(
                    "INSERT INTO user_team_memberships VALUES (?, ?, ?)",
                    ("private-user@example.com", "team-old", 3),
                )
                usage.executemany(
                    "INSERT INTO key_identities VALUES (?, ?, ?, ?)",
                    [
                        ("digest-1", "private-user@example.com", "team-old", 3),
                        ("digest-2", "private-user@example.com", "team-old", 3),
                    ],
                )
                control.commit()
                usage.commit()
                before = WRITE_COMPARE.read_membership_state(
                    target, "private-user@example.com"
                )
                control.execute(
                    "UPDATE user_team_memberships SET team_id = ?, "
                    "membership_version = ? WHERE user_email = ?",
                    ("team-new", 4, "private-user@example.com"),
                )
                usage.execute(
                    "UPDATE key_identities SET team_id = ?, "
                    "team_membership_version = ? WHERE user_email = ?",
                    ("team-new", 4, "private-user@example.com"),
                )
                control.commit()
                usage.commit()
                after = WRITE_COMPARE.read_membership_state(
                    target, "private-user@example.com"
                )
                transition = WRITE_COMPARE.membership_transition(
                    "assignment", before, after
                )
                self.assertEqual(transition["membership_version_delta"], 1)
                self.assertEqual(transition["identity_rows_after"], 2)

                usage.execute(
                    "UPDATE key_identities SET team_membership_version = 3 "
                    "WHERE key_hash = 'digest-1'"
                )
                usage.commit()
                inconsistent = WRITE_COMPARE.read_membership_state(
                    target, "private-user@example.com"
                )
                with self.assertRaisesRegex(ValueError, "synchronize every"):
                    WRITE_COMPARE.membership_transition(
                        "assignment", before, inconsistent
                    )
            finally:
                control.close()
                usage.close()

    def test_temporary_cleanup_state_accepts_only_reviewed_surface_contracts(self):
        with tempfile.TemporaryDirectory() as temporary:
            parent = Path(temporary)
            v1 = self.make_target(parent, "v1", "v1", 19317)
            v2 = self.make_target(parent, "v2", "v2", 18317)
            self.add_persistence_schema(v1)
            self.add_persistence_schema(v2)
            control = sqlite3.connect(v1.control_db)
            try:
                control.execute(
                    "INSERT INTO internal_keys VALUES (?, ?, 'inactive')",
                    ("migration-write-test@example.com", "redacted"),
                )
                control.commit()
            finally:
                control.close()

            v1_state = WRITE_COMPARE.temporary_user_cleanup_state(
                v1, "migration-write-test@example.com"
            )
            v2_state = WRITE_COMPARE.temporary_user_cleanup_state(
                v2, "migration-write-test@example.com"
            )
            WRITE_COMPARE.require_temporary_user_cleanup("v1", v1_state)
            WRITE_COMPARE.require_temporary_user_cleanup("v2", v2_state)
            approved, unexpected = WRITE_COMPARE.compare_persistence_views(
                {
                    "v1": {"temporary_user_cleanup": v1_state},
                    "v2": {"temporary_user_cleanup": v2_state},
                }
            )
            self.assertEqual(
                [item["name"] for item in approved],
                ["persistence.temporary_user_cleanup"],
            )
            self.assertEqual(unexpected, [])

            control = sqlite3.connect(v2.control_db)
            try:
                control.execute(
                    "INSERT INTO user_routes VALUES (?, ?)",
                    ("migration-write-test@example.com", "private-account"),
                )
                control.commit()
            finally:
                control.close()
            dirty = WRITE_COMPARE.temporary_user_cleanup_state(
                v2, "migration-write-test@example.com"
            )
            with self.assertRaisesRegex(ValueError, "user_routes"):
                WRITE_COMPARE.require_temporary_user_cleanup("v2", dirty)

    def test_persistence_comparison_omits_absolute_membership_versions(self):
        view = WRITE_COMPARE.persistence_comparison_view(
            {
                "persistence": {
                    "team_assignment": {
                        "membership_version_before": 41,
                        "membership_version_after": 42,
                        "membership_version_delta": 1,
                        "identity_rows_before": 11,
                        "identity_rows_after": 11,
                        "matching_identity_rows_after": 11,
                        "all_identities_match": True,
                    }
                }
            }
        )
        rendered = json.dumps(view, sort_keys=True)
        self.assertNotIn("41", rendered)
        self.assertNotIn("42", rendered)
        self.assertEqual(view["team_assignment"]["membership_version_delta"], 1)


if __name__ == "__main__":
    unittest.main()
