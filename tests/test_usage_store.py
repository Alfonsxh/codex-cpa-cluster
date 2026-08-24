import importlib.util
import datetime
import sqlite3
import tempfile
import time
import unittest
from contextlib import closing
from pathlib import Path
from unittest import mock


ROOT = Path(__file__).parents[1]


def load_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class UsageStoreTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.module = load_module("cliproxy_usage_store_test", ROOT / "admin" / "usage_store.py")

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.db = Path(self.tmp.name) / "usage.sqlite3"
        self.store = self.module.UsageStore(self.db)
        self.records = [
            {
                "key": "cpa_alpha_alice_0123456789abcdef",
                "label": "alice@example.com:gamma",
                "user": "alice@example.com",
                "account": "gamma",
            },
            {
                "key": "cpa_alpha_alice_fedcba9876543210",
                "label": "alice@example.com:alpha",
                "user": "alice@example.com",
                "account": "alpha",
            },
        ]
        self.store.sync_identities(self.records, now=1_000)

    def tearDown(self):
        self.tmp.cleanup()

    def test_weekly_quota_reads_are_batched_for_large_user_sets(self):
        users = ["user{:04d}@example.com".format(index) for index in range(1200)]

        quotas = self.store.weekly_quotas(users, 1000000, now=1_700_000_000)

        self.assertEqual(len(quotas), 1200)
        self.assertEqual(quotas[users[0]]["limit_tokens"], 1000000)
        self.assertEqual(quotas[users[-1]]["used_tokens"], 0)

    def test_user_summary_honors_bounded_and_all_history_ranges(self):
        now = 20_000
        self.store.ingest_events(
            "gamma",
            [
                self.event(self.records[0]["key"], "old", now - 10_000),
                self.event(self.records[0]["key"], "recent", now - 100),
            ],
            now=now,
        )

        recent = self.store.usage_summaries_for_users(
            window_seconds=3_600,
            now=now,
        )
        explicit = self.store.usage_summaries_for_users(
            window_seconds=None,
            start_at=now - 200,
            end_at=now,
            now=now,
        )
        all_history = self.store.usage_summaries_for_users(
            window_seconds=None,
            now=now,
        )

        self.assertEqual(recent["alice@example.com"]["request_count"], 1)
        self.assertEqual(explicit["alice@example.com"]["request_count"], 1)
        self.assertEqual(all_history["alice@example.com"]["request_count"], 2)

    @staticmethod
    def event(key, request_id, timestamp, **overrides):
        payload = {
            "timestamp": timestamp,
            "latency_ms": 123,
            "provider": "openai",
            "model": "gpt-5.6-sol",
            "alias": "gpt-5.6-sol",
            "reasoning_effort": "xhigh",
            "endpoint": "POST /v1/responses",
            "auth_type": "apikey",
            "api_key": key,
            "request_id": request_id,
            "failed": False,
            "tokens": {
                "input_tokens": 100,
                "output_tokens": 40,
                "reasoning_tokens": 10,
                "cached_tokens": 20,
                "total_tokens": 140,
            },
        }
        payload.update(overrides)
        return payload

    def test_portal_credentials_are_persistent_and_password_resets_revoke_sessions(self):
        user = "alice@example.com"
        created = self.store.ensure_portal_credential(user, "initial-hash", now=100)
        self.assertEqual(created["password_hash"], "initial-hash")
        self.assertTrue(created["must_change"])

        unchanged = self.store.ensure_portal_credential(user, "ignored-hash", now=110)
        self.assertEqual(unchanged["password_hash"], "initial-hash")

        first = self.store.create_session(user, now=120)
        second = self.store.create_session(user, now=120)
        updated = self.store.set_portal_credential(
            user,
            "changed-hash",
            must_change=False,
            now=130,
            keep_session_token=first["token"],
        )
        self.assertFalse(updated["must_change"])
        self.assertIsNotNone(self.store.resolve_session(first["token"], now=131))
        self.assertIsNone(self.store.resolve_session(second["token"], now=131))

        self.store.set_portal_credential(user, "reset-hash", must_change=True, now=140)
        self.assertIsNone(self.store.resolve_session(first["token"], now=141))
        removed = self.store.delete_portal_identity(user)
        self.assertEqual(removed["credentials"], 1)
        self.assertIsNone(self.store.portal_credential(user))

    def test_password_schema_upgrade_revokes_legacy_email_only_sessions(self):
        legacy_path = Path(self.tmp.name) / "legacy-usage.sqlite3"
        with closing(sqlite3.connect(str(legacy_path))) as connection:
            connection.executescript(
                """
                CREATE TABLE portal_sessions (
                    session_hash TEXT PRIMARY KEY,
                    user_email TEXT NOT NULL,
                    created_at INTEGER NOT NULL,
                    expires_at INTEGER NOT NULL
                );
                INSERT INTO portal_sessions VALUES (
                    'legacy-session', 'alice@example.com', 100, 9999999999
                );
                PRAGMA user_version = 5;
                """
            )

        migrated = self.module.UsageStore(legacy_path)

        with closing(sqlite3.connect(str(legacy_path))) as connection:
            remaining = connection.execute(
                "SELECT COUNT(*) FROM portal_sessions"
            ).fetchone()[0]
            credential_table = connection.execute(
                "SELECT COUNT(*) FROM portal_credentials"
            ).fetchone()[0]
        self.assertEqual(remaining, 0)
        self.assertEqual(credential_table, 0)
        self.assertEqual(migrated.path, legacy_path.resolve())

    def test_ingest_deduplicates_and_aggregates_by_user_and_account(self):
        now = 10_000
        first = self.event(self.records[0]["key"], "request-1", now - 30)
        failed = self.event(
            self.records[0]["key"],
            "request-2",
            now - 20,
            failed=True,
            tokens={"total_tokens": 0},
        )
        other_account = self.event(
            self.records[1]["key"],
            "request-3",
            now - 10,
            tokens={"input_tokens": 50, "output_tokens": 25, "total_tokens": 75},
        )

        plus = self.store.ingest_events("gamma", [first, failed, first], now=now)
        arch = self.store.ingest_events("alpha", [other_account], now=now)

        self.assertEqual(plus["inserted"], 2)
        self.assertEqual(plus["duplicate"], 1)
        self.assertEqual(arch["inserted"], 1)
        usage = self.store.usage_for_users(
            ["alice@example.com"],
            ["gamma", "alpha"],
            window_seconds=3600,
            now=now,
        )["alice@example.com"]
        self.assertEqual(usage["request_count"], 3)
        self.assertEqual(usage["failed_count"], 1)
        self.assertEqual(usage["total_tokens"], 215)
        self.assertEqual(usage["accounts"]["gamma"]["request_count"], 2)
        self.assertEqual(usage["accounts"]["alpha"]["total_tokens"], 75)
        self.assertEqual(usage["last_used_at"], now - 10)

        accounts = self.store.usage_for_accounts(
            ["gamma", "alpha"], window_seconds=3600, now=now
        )
        self.assertEqual(accounts["gamma"]["request_count"], 2)
        self.assertEqual(accounts["gamma"]["active_users"], 1)
        self.assertEqual(accounts["gamma"]["failed_count"], 1)
        self.assertEqual(accounts["alpha"]["total_tokens"], 75)

        with closing(sqlite3.connect(str(self.db))) as connection:
            stored = connection.execute(
                "SELECT model, alias, reasoning_effort FROM usage_events "
                "WHERE request_id = 'request-1'"
            ).fetchone()
        self.assertEqual(stored, ("gpt-5.6-sol", "gpt-5.6-sol", "xhigh"))

    def test_schema_upgrade_adds_breakdown_columns_without_starting_history(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "legacy.sqlite3"
            with closing(sqlite3.connect(str(path))) as connection:
                connection.executescript(
                    """
                    CREATE TABLE usage_events (
                        id INTEGER PRIMARY KEY AUTOINCREMENT,
                        event_key TEXT NOT NULL UNIQUE,
                        account TEXT NOT NULL,
                        user_email TEXT NOT NULL,
                        key_label TEXT NOT NULL,
                        occurred_at INTEGER NOT NULL,
                        request_id TEXT NOT NULL DEFAULT '',
                        provider TEXT NOT NULL DEFAULT '',
                        model TEXT NOT NULL DEFAULT '',
                        endpoint TEXT NOT NULL DEFAULT '',
                        failed INTEGER NOT NULL DEFAULT 0,
                        latency_ms INTEGER NOT NULL DEFAULT 0,
                        input_tokens INTEGER NOT NULL DEFAULT 0,
                        output_tokens INTEGER NOT NULL DEFAULT 0,
                        reasoning_tokens INTEGER NOT NULL DEFAULT 0,
                        cached_tokens INTEGER NOT NULL DEFAULT 0,
                        total_tokens INTEGER NOT NULL DEFAULT 0
                    );
                    PRAGMA user_version = 2;
                    """
                )

            store = self.module.UsageStore(path)

            with closing(sqlite3.connect(str(path))) as connection:
                columns = {
                    row[1] for row in connection.execute("PRAGMA table_info(usage_events)")
                }
                weekly_columns = {
                    row[1]
                    for row in connection.execute(
                        "PRAGMA table_info(user_weekly_usage)"
                    )
                }
                quota_policy_columns = {
                    row[1]
                    for row in connection.execute(
                        "PRAGMA table_info(user_quota_policies)"
                    )
                }
                version = connection.execute("PRAGMA user_version").fetchone()[0]
            self.assertIn("alias", columns)
            self.assertIn("reasoning_effort", columns)
            self.assertIn("quota_multiplier", columns)
            self.assertIn("weighted_tokens", columns)
            self.assertIn("weight_policy_version", columns)
            self.assertIn("team_id", columns)
            self.assertIn("team_membership_version", columns)
            self.assertIn("weighted_tokens", weekly_columns)
            self.assertIn("reset_at", quota_policy_columns)
            self.assertEqual(version, 10)
            self.assertEqual(store.usage_breakdown_started_at(), 0)

    def test_schema_upgrade_preserves_existing_personal_policy_until_next_week(self):
        path = Path(self.tmp.name) / "legacy-quota.sqlite3"
        zone = datetime.timezone(datetime.timedelta(hours=8))
        now = int(datetime.datetime(2026, 8, 20, 12, 0, tzinfo=zone).timestamp())
        unused_start, week_end = self.module.natural_week_bounds(
            now,
            "Asia/Shanghai",
        )
        with closing(sqlite3.connect(str(path))) as connection:
            connection.executescript(
                """
                CREATE TABLE user_quota_policies (
                    user_email TEXT PRIMARY KEY,
                    weekly_tokens INTEGER,
                    created_at INTEGER NOT NULL,
                    updated_at INTEGER NOT NULL,
                    created_by TEXT NOT NULL DEFAULT 'admin'
                );
                INSERT INTO user_quota_policies VALUES (
                    'alice@example.com', 500, 100, 100, 'admin'
                );
                PRAGMA user_version = 9;
                """
            )

        with mock.patch.object(self.module.time, "time", return_value=now):
            self.module.UsageStore(path, week_timezone="Asia/Shanghai")

        with closing(sqlite3.connect(str(path))) as connection:
            policy = connection.execute(
                "SELECT weekly_tokens, reset_at FROM user_quota_policies "
                "WHERE user_email = 'alice@example.com'"
            ).fetchone()
            version = connection.execute("PRAGMA user_version").fetchone()[0]
        self.assertEqual(policy, (500, week_end))
        self.assertEqual(version, 10)

    def test_team_reports_follow_current_membership_without_rewriting_events(self):
        now = 20_000
        self.store.sync_user_teams(
            {
                "alice@example.com": {
                    "team_id": "team_alpha",
                    "team_membership_version": 1,
                }
            }
        )
        self.store.ingest_events(
            "gamma",
            [self.event(self.records[0]["key"], "team-alpha", now - 20)],
            now=now,
        )
        self.store.sync_user_teams(
            {
                "alice@example.com": {
                    "team_id": "team_beta",
                    "team_membership_version": 2,
                }
            }
        )
        self.store.ingest_events(
            "gamma",
            [self.event(self.records[0]["key"], "team-beta", now - 10)],
            now=now,
        )

        usage = self.store.usage_for_teams(
            ["team_alpha", "team_beta"],
            {"alice@example.com": "team_beta"},
            window_seconds=None,
            now=now,
        )
        self.assertEqual(usage["team_alpha"]["total_tokens"], 0)
        self.assertEqual(usage["team_beta"]["total_tokens"], 280)
        self.assertEqual(usage["unassigned"]["total_tokens"], 0)
        breakdown = self.store.team_usage_breakdown(
            "team_beta", ["alice@example.com"], window_seconds=None, now=now
        )
        self.assertEqual(breakdown["attribution"], "current_membership")
        self.assertEqual(breakdown["users"][0]["user"], "alice@example.com")
        self.assertEqual(breakdown["models"][0]["model"], "gpt-5.6-sol")
        self.assertEqual(
            (breakdown["combinations"][0]["model"], breakdown["combinations"][0]["reasoning_effort"]),
            ("gpt-5.6-sol", "xhigh"),
        )
        self.assertEqual(breakdown["combinations"][0]["weighted_tokens"], 280)
        self.assertEqual(sum(breakdown["series"]["values"]), 280)
        self.assertEqual(breakdown["series"]["bucket_seconds"], 300)
        with closing(sqlite3.connect(str(self.db))) as connection:
            rows = connection.execute(
                "SELECT request_id, team_id, team_membership_version "
                "FROM usage_events ORDER BY id"
            ).fetchall()
        self.assertEqual(
            rows,
            [
                ("team-alpha", "team_alpha", 1),
                ("team-beta", "team_beta", 2),
            ],
        )

        unassigned = self.store.usage_for_teams(
            ["team_alpha", "team_beta"],
            {"alice@example.com": None},
            window_seconds=None,
            now=now,
        )
        self.assertEqual(unassigned["unassigned"]["total_tokens"], 280)

    def test_team_breakdown_supports_more_than_sqlite_variable_limit_members(self):
        users = [
            "member{:04d}@example.com".format(index)
            for index in range(1_100)
        ]
        breakdown = self.store.team_usage_breakdown(
            "team_large",
            users,
            window_seconds=None,
            now=20_000,
        )

        self.assertEqual(breakdown["attribution"], "current_membership")
        self.assertEqual(breakdown["totals"]["total_tokens"], 0)
        self.assertEqual(breakdown["users"], [])

    def test_reasoning_multipliers_are_frozen_per_event_and_report_both_totals(self):
        zone = datetime.timezone(datetime.timedelta(hours=8))
        now = int(datetime.datetime(2026, 8, 5, 12, 0, tzinfo=zone).timestamp())
        first_policy = {
            "user_quota.reasoning_multiplier.max": 2.0,
            "user_quota.reasoning_multiplier.high": 1.5,
        }
        second_policy = {
            "user_quota.reasoning_multiplier.max": 3.0,
            "user_quota.reasoning_multiplier.high": 1.5,
        }
        events = [
            self.event(
                self.records[0]["key"],
                "weighted-max-first",
                now - 30,
                reasoning_effort="MAX",
                tokens={"total_tokens": 101},
            ),
            self.event(
                self.records[0]["key"],
                "weighted-high-half-up",
                now - 20,
                reasoning_effort="high",
                tokens={"total_tokens": 3},
            ),
            self.event(
                self.records[0]["key"],
                "weighted-unknown",
                now - 10,
                reasoning_effort="future-effort",
                tokens={"total_tokens": 7},
            ),
        ]
        self.store.ensure_usage_breakdown_started(now=now - 60)
        self.store.ingest_events(
            "gamma",
            events,
            now=now,
            reasoning_multipliers=first_policy,
        )
        self.store.ingest_events(
            "gamma",
            [
                self.event(
                    self.records[0]["key"],
                    "weighted-max-second",
                    now,
                    reasoning_effort="max",
                    tokens={"total_tokens": 50},
                )
            ],
            now=now,
            reasoning_multipliers=second_policy,
        )

        quota = self.store.weekly_quotas(
            ["alice@example.com"], 1_000, now=now
        )["alice@example.com"]
        self.assertEqual(quota["raw_used_tokens"], 161)
        self.assertEqual(quota["weighted_raw_used_tokens"], 364)
        self.assertEqual(quota["used_tokens"], 364)
        self.assertEqual(quota["weighted_used_tokens"], 364)
        self.assertEqual(quota["quota_unit"], "weighted_tokens")

        breakdown = self.store.usage_breakdown_for_user(
            "alice@example.com", window_seconds=None, now=now + 1
        )
        self.assertEqual(breakdown["totals"]["total_tokens"], 161)
        self.assertEqual(breakdown["totals"]["weighted_tokens"], 364)
        with closing(sqlite3.connect(str(self.db))) as connection:
            rows = connection.execute(
                "SELECT request_id, reasoning_effort, quota_multiplier, "
                "weighted_tokens, weight_policy_version FROM usage_events "
                "ORDER BY id"
            ).fetchall()
        self.assertEqual(
            [(row[0], row[1], row[2], row[3]) for row in rows],
            [
                ("weighted-max-first", "max", 2.0, 202),
                ("weighted-high-half-up", "high", 1.5, 5),
                ("weighted-unknown", "unknown", 1.0, 7),
                ("weighted-max-second", "max", 3.0, 150),
            ],
        )
        self.assertEqual(rows[0][4], rows[1][4])
        self.assertNotEqual(rows[0][4], rows[3][4])
        self.assertEqual(
            self.module.weighted_token_count(3, "high", {"high": 1.5}),
            5,
        )

    def test_weekly_usage_reset_uses_weighted_tokens_and_preserves_raw_audit_total(self):
        zone = datetime.timezone(datetime.timedelta(hours=8))
        now = int(datetime.datetime(2026, 8, 5, 12, 0, tzinfo=zone).timestamp())
        self.store.ingest_events(
            "gamma",
            [
                self.event(
                    self.records[0]["key"],
                    "reset-weighted-max",
                    now,
                    reasoning_effort="max",
                    tokens={"total_tokens": 100},
                )
            ],
            now=now,
            reasoning_multipliers={
                "user_quota.reasoning_multiplier.max": 2.0,
            },
        )

        reset = self.store.reset_weekly_usage(
            ["alice@example.com"], "加权额度补偿", now=now
        )
        self.assertEqual(reset["token_amount"], 200)
        after_reset = self.store.weekly_quotas(
            ["alice@example.com"], 500, now=now
        )["alice@example.com"]
        self.assertEqual(after_reset["raw_used_tokens"], 100)
        self.assertEqual(after_reset["weighted_raw_used_tokens"], 200)
        self.assertEqual(after_reset["usage_reset_tokens"], 200)
        self.assertEqual(after_reset["used_tokens"], 0)

        self.store.ingest_events(
            "gamma",
            [
                self.event(
                    self.records[0]["key"],
                    "reset-weighted-later",
                    now + 60,
                    reasoning_effort="medium",
                    tokens={"total_tokens": 50},
                )
            ],
            now=now + 60,
        )
        quota = self.store.weekly_quotas(
            ["alice@example.com"], 500, now=now + 60
        )["alice@example.com"]
        self.assertEqual(quota["raw_used_tokens"], 150)
        self.assertEqual(quota["weighted_raw_used_tokens"], 250)
        self.assertEqual(quota["used_tokens"], 50)

    def test_natural_week_uses_monday_midnight_in_asia_shanghai(self):
        zone = datetime.timezone(datetime.timedelta(hours=8))
        sunday = int(datetime.datetime(2026, 8, 2, 23, 59, tzinfo=zone).timestamp())
        monday = int(datetime.datetime(2026, 8, 3, 0, 0, tzinfo=zone).timestamp())

        sunday_start, sunday_end = self.module.natural_week_bounds(
            sunday, "Asia/Shanghai"
        )
        monday_start, monday_end = self.module.natural_week_bounds(
            monday, "Asia/Shanghai"
        )

        self.assertEqual(
            sunday_start,
            int(datetime.datetime(2026, 7, 27, 0, 0, tzinfo=zone).timestamp()),
        )
        self.assertEqual(sunday_end, monday)
        self.assertEqual(monday_start, monday)
        self.assertEqual(monday_end - monday_start, 7 * 24 * 60 * 60)

    def test_week_timezone_change_rebuilds_materialized_usage(self):
        event_at = int(
            datetime.datetime(
                2026, 8, 2, 18, 0, tzinfo=datetime.timezone.utc
            ).timestamp()
        )
        self.store.set_quota_policy(
            "alice@example.com", "custom", weekly_tokens=500, now=event_at
        )
        self.store.ingest_events(
            "gamma",
            [self.event(self.records[0]["key"], "timezone-change", event_at)],
            now=event_at,
        )

        before = self.store.weekly_quotas(
            ["alice@example.com"], 500, now=event_at
        )["alice@example.com"]
        changed = self.store.set_week_timezone("Asia/Shanghai", now=event_at)
        after = self.store.weekly_quotas(
            ["alice@example.com"], 500, now=event_at
        )["alice@example.com"]

        self.assertTrue(changed["changed"])
        self.assertEqual(before["timezone"], "UTC")
        self.assertEqual(after["timezone"], "Asia/Shanghai")
        self.assertNotEqual(before["week_start_at"], after["week_start_at"])
        self.assertEqual(after["weighted_raw_used_tokens"], 140)
        self.assertEqual(before["policy_reset_at"], before["week_end_at"])
        self.assertEqual(after["policy_reset_at"], after["week_end_at"])
        self.assertNotEqual(before["policy_reset_at"], after["policy_reset_at"])

    def test_weekly_quota_aggregates_accounts_and_persists_policy_modes(self):
        zone = datetime.timezone(datetime.timedelta(hours=8))
        now = int(datetime.datetime(2026, 7, 29, 12, 0, tzinfo=zone).timestamp())
        plus = self.event(self.records[0]["key"], "weekly-plus", now - 30)
        arch = self.event(
            self.records[1]["key"],
            "weekly-arch",
            now - 20,
            tokens={"input_tokens": 50, "output_tokens": 25, "total_tokens": 75},
        )

        self.store.ingest_events("gamma", [plus, plus], now=now)
        self.store.ingest_events("alpha", [arch], now=now)
        inherited = self.store.weekly_quotas(
            ["alice@example.com"], 200, now=now
        )["alice@example.com"]

        self.assertEqual(inherited["used_tokens"], 215)
        self.assertEqual(inherited["source"], "default")
        self.assertEqual(inherited["policy_mode"], "inherit")
        self.assertTrue(inherited["limit_reached"])

        self.store.set_quota_policy("alice@example.com", "unlimited", now=now)
        unlimited = self.store.weekly_quotas(
            ["alice@example.com"], 200, now=now
        )["alice@example.com"]
        self.assertTrue(unlimited["unlimited"])
        self.assertEqual(unlimited["source"], "user_unlimited")

        self.store.set_quota_policy(
            "alice@example.com", "custom", weekly_tokens=500, now=now
        )
        current_custom = self.store.weekly_quotas(
            ["alice@example.com"], None, now=now
        )["alice@example.com"]
        self.assertEqual(current_custom["policy_mode"], "custom")
        self.assertEqual(
            current_custom["policy_reset_at"],
            current_custom["week_end_at"],
        )
        next_week = now + 7 * 24 * 60 * 60
        restored = self.store.weekly_quotas(
            ["alice@example.com"], None, now=next_week
        )["alice@example.com"]
        self.assertEqual(restored["policy_mode"], "inherit")
        self.assertTrue(restored["unlimited"])

        self.store.configure_personal_quota_weekly_reset(False, now=next_week)
        self.store.set_quota_policy(
            "alice@example.com", "custom", weekly_tokens=700, now=next_week
        )
        persistent = self.store.weekly_quotas(
            ["alice@example.com"], None, now=next_week + 7 * 24 * 60 * 60
        )["alice@example.com"]
        self.assertEqual(persistent["policy_mode"], "custom")
        self.assertEqual(persistent["limit_tokens"], 700)
        self.assertIsNone(persistent["policy_reset_at"])

        self.store.clear_quota_policy("alice@example.com")
        inherited_unlimited = self.store.weekly_quotas(
            ["alice@example.com"], None, now=next_week
        )["alice@example.com"]
        self.assertEqual(inherited_unlimited["policy_mode"], "inherit")
        self.assertTrue(inherited_unlimited["unlimited"])

    def test_custom_weekly_quota_rejects_fractional_and_oversized_values(self):
        for value in (1.5, "1.5", self.module.MAX_WEEKLY_QUOTA_TOKENS + 1):
            with self.subTest(value=value):
                with self.assertRaisesRegex(ValueError, "正整数|不能超过"):
                    self.store.set_quota_policy(
                        "alice@example.com",
                        "custom",
                        weekly_tokens=value,
                    )

    def test_enabling_weekly_policy_reset_waits_for_next_boundary(self):
        zone = datetime.timezone(datetime.timedelta(hours=8))
        now = int(datetime.datetime(2026, 8, 20, 12, 0, tzinfo=zone).timestamp())
        self.store.set_week_timezone("Asia/Shanghai", now=now)
        self.store.configure_personal_quota_weekly_reset(False, now=now)
        self.store.set_quota_policy(
            "alice@example.com", "custom", weekly_tokens=500, now=now
        )

        scheduled = self.store.configure_personal_quota_weekly_reset(
            True,
            now=now,
        )
        current = self.store.weekly_quotas(
            ["alice@example.com"], 1_000, now=now
        )["alice@example.com"]

        self.assertEqual(scheduled["expired_policies"], 0)
        self.assertEqual(scheduled["scheduled_policies"], 1)
        self.assertEqual(current["policy_mode"], "custom")
        self.assertEqual(current["policy_reset_at"], current["week_end_at"])

        restored = self.store.weekly_quotas(
            ["alice@example.com"], 1_000, now=current["week_end_at"]
        )["alice@example.com"]
        cleanup = self.store.configure_personal_quota_weekly_reset(
            True,
            now=current["week_end_at"],
        )
        with self.store._connection() as connection:
            remaining = connection.execute(
                "SELECT COUNT(*) FROM user_quota_policies"
            ).fetchone()[0]
        self.assertEqual(restored["policy_mode"], "inherit")
        self.assertEqual(restored["limit_tokens"], 1_000)
        self.assertEqual(cleanup["expired_policies"], 1)
        self.assertEqual(remaining, 0)

    def test_disabling_reset_after_boundary_does_not_restore_expired_policy(self):
        zone = datetime.timezone(datetime.timedelta(hours=8))
        now = int(datetime.datetime(2026, 8, 20, 12, 0, tzinfo=zone).timestamp())
        self.store.set_week_timezone("Asia/Shanghai", now=now)
        self.store.set_quota_policy(
            "alice@example.com", "custom", weekly_tokens=500, now=now
        )
        current = self.store.weekly_quotas(
            ["alice@example.com"], 1_000, now=now
        )["alice@example.com"]

        disabled = self.store.configure_personal_quota_weekly_reset(
            False,
            now=current["week_end_at"],
        )
        restored = self.store.weekly_quotas(
            ["alice@example.com"], 1_000, now=current["week_end_at"]
        )["alice@example.com"]

        self.assertEqual(disabled["expired_policies"], 1)
        self.assertEqual(disabled["cancelled_schedules"], 0)
        self.assertEqual(restored["policy_mode"], "inherit")
        self.assertEqual(restored["limit_tokens"], 1_000)

    def test_weekly_quota_adjustments_preserve_raw_usage_and_expire_next_week(self):
        zone = datetime.timezone(datetime.timedelta(hours=8))
        now = int(datetime.datetime(2026, 7, 29, 12, 0, tzinfo=zone).timestamp())
        first = self.event(self.records[0]["key"], "adjustment-first", now - 30)
        second = self.event(
            self.records[1]["key"],
            "adjustment-second",
            now - 20,
            tokens={"input_tokens": 50, "output_tokens": 25, "total_tokens": 75},
        )
        self.store.ingest_events("gamma", [first], now=now)
        self.store.ingest_events("alpha", [second], now=now)

        bonus = self.store.add_quota_bonus(
            ["alice@example.com"],
            100,
            "临时扩容",
            now=now,
        )
        self.assertEqual(bonus["token_amount"], 100)
        quota = self.store.weekly_quotas(
            ["alice@example.com"], 500, now=now
        )["alice@example.com"]
        self.assertEqual(quota["base_limit_tokens"], 500)
        self.assertEqual(quota["bonus_tokens"], 100)
        self.assertEqual(quota["limit_tokens"], 600)
        self.assertEqual(quota["raw_used_tokens"], 215)
        self.assertEqual(quota["used_tokens"], 215)

        reset = self.store.reset_weekly_usage(
            ["alice@example.com", "idle@example.com"],
            "异常流量补偿",
            now=now,
        )
        self.assertEqual(reset["token_amount"], 215)
        self.assertEqual(reset["applied_users"], ["alice@example.com"])
        self.assertEqual(reset["skipped_users"], ["idle@example.com"])
        quota = self.store.weekly_quotas(
            ["alice@example.com"], 500, now=now
        )["alice@example.com"]
        self.assertEqual(quota["raw_used_tokens"], 215)
        self.assertEqual(quota["usage_reset_tokens"], 215)
        self.assertEqual(quota["used_tokens"], 0)
        self.assertEqual(quota["remaining_tokens"], 600)

        later = self.event(
            self.records[0]["key"],
            "adjustment-later",
            now + 60,
            tokens={"input_tokens": 30, "output_tokens": 20, "total_tokens": 50},
        )
        self.store.ingest_events("gamma", [later], now=now + 60)
        quota = self.store.weekly_quotas(
            ["alice@example.com"], 500, now=now + 60
        )["alice@example.com"]
        self.assertEqual(quota["raw_used_tokens"], 265)
        self.assertEqual(quota["used_tokens"], 50)
        history = self.store.quota_adjustment_history(
            "alice@example.com",
            now=now + 60,
        )
        self.assertEqual([item["action"] for item in history], ["usage_reset", "bonus"])
        self.assertEqual(history[0]["reason"], "异常流量补偿")

        next_week = now + 7 * 24 * 60 * 60
        quota = self.store.weekly_quotas(
            ["alice@example.com"], 500, now=next_week
        )["alice@example.com"]
        self.assertEqual(quota["bonus_tokens"], 0)
        self.assertEqual(quota["usage_reset_tokens"], 0)
        self.assertEqual(quota["used_tokens"], 0)
        self.assertEqual(quota["limit_tokens"], 500)

    def test_bulk_clear_quota_policies_is_scoped_and_atomic(self):
        self.store.set_quota_policy("alice@example.com", "custom", 500)
        self.store.set_quota_policy("bob@example.com", "unlimited")
        self.store.set_quota_policy("carol@example.com", "custom", 900)

        cleared = self.store.clear_quota_policies(
            ["alice@example.com", "bob@example.com"]
        )

        self.assertEqual(cleared, 2)
        quotas = self.store.weekly_quotas(
            [
                "alice@example.com",
                "bob@example.com",
                "carol@example.com",
            ],
            1000,
        )
        self.assertEqual(quotas["alice@example.com"]["policy_mode"], "inherit")
        self.assertEqual(quotas["bob@example.com"]["policy_mode"], "inherit")
        self.assertEqual(quotas["carol@example.com"]["policy_mode"], "custom")

    def test_initialization_backfills_historical_weekly_usage_idempotently(self):
        zone = datetime.timezone(datetime.timedelta(hours=8))
        occurred_at = int(datetime.datetime(2026, 7, 28, 12, 0, tzinfo=zone).timestamp())
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "historical.sqlite3"
            with closing(sqlite3.connect(str(path))) as connection:
                connection.executescript(
                    """
                    CREATE TABLE usage_events (
                        id INTEGER PRIMARY KEY AUTOINCREMENT,
                        event_key TEXT NOT NULL UNIQUE,
                        account TEXT NOT NULL,
                        user_email TEXT NOT NULL,
                        key_label TEXT NOT NULL,
                        occurred_at INTEGER NOT NULL,
                        request_id TEXT NOT NULL DEFAULT '',
                        provider TEXT NOT NULL DEFAULT '',
                        model TEXT NOT NULL DEFAULT '',
                        endpoint TEXT NOT NULL DEFAULT '',
                        failed INTEGER NOT NULL DEFAULT 0,
                        latency_ms INTEGER NOT NULL DEFAULT 0,
                        input_tokens INTEGER NOT NULL DEFAULT 0,
                        output_tokens INTEGER NOT NULL DEFAULT 0,
                        reasoning_tokens INTEGER NOT NULL DEFAULT 0,
                        cached_tokens INTEGER NOT NULL DEFAULT 0,
                        total_tokens INTEGER NOT NULL DEFAULT 0
                    );
                    """
                )
                connection.execute(
                    "INSERT INTO usage_events(event_key, account, user_email, key_label, occurred_at, total_tokens) "
                    "VALUES (?, ?, ?, ?, ?, ?)",
                    ("historical-1", "alpha", "alice@example.com", "alice", occurred_at, 321),
                )
                connection.commit()

            store = self.module.UsageStore(path)
            quota = store.weekly_quotas(
                ["alice@example.com"], 1000, now=occurred_at
            )["alice@example.com"]
            self.assertEqual(quota["used_tokens"], 321)
            self.assertEqual(quota["raw_used_tokens"], 321)
            self.assertEqual(quota["weighted_raw_used_tokens"], 321)
            self.assertEqual(store.ensure_weekly_usage_backfilled(), {"backfilled": False})
            self.assertEqual(
                store.weekly_quotas(
                    ["alice@example.com"], 1000, now=occurred_at
                )["alice@example.com"]["used_tokens"],
                321,
            )
            with closing(sqlite3.connect(str(path))) as connection:
                connection.execute(
                    "INSERT INTO usage_events(event_key, account, user_email, key_label, occurred_at, total_tokens) "
                    "VALUES (?, ?, ?, ?, ?, ?)",
                    ("historical-2", "gamma", "alice@example.com", "alice", occurred_at + 60, 123),
                )
                connection.commit()

            reopened = self.module.UsageStore(path)
            self.assertEqual(
                reopened.weekly_quotas(
                    ["alice@example.com"], 1000, now=occurred_at
                )["alice@example.com"]["used_tokens"],
                444,
            )
            with closing(sqlite3.connect(str(path))) as connection:
                legacy = connection.execute(
                    "SELECT quota_multiplier, weighted_tokens, "
                    "weight_policy_version FROM usage_events "
                    "WHERE event_key = 'historical-2'"
                ).fetchone()
            self.assertEqual(legacy, (1.0, 0, "legacy-v1"))

    def test_usage_breakdown_only_counts_events_after_explicit_start(self):
        old = self.event(
            self.records[0]["key"],
            "old-before-breakdown",
            4_999,
            model="gpt-5.4",
            reasoning_effort="medium",
        )
        self.store.ingest_events("gamma", [old], now=4_999)
        self.assertEqual(self.store.ensure_usage_breakdown_started(now=5_000), 5_000)
        self.assertEqual(self.store.ensure_usage_breakdown_started(now=5_100), 5_000)

        plus_success = self.event(
            self.records[0]["key"],
            "plus-success",
            5_000,
            model="gpt-5.6-sol",
            reasoning_effort="xhigh",
        )
        plus_failed = self.event(
            self.records[0]["key"],
            "plus-failed",
            5_001,
            model="gpt-5.6-sol",
            reasoning_effort="xhigh",
            failed=True,
        )
        arch_high = self.event(
            self.records[1]["key"],
            "arch-high",
            5_002,
            model="gpt-5.6-terra",
            reasoning_effort="high",
            tokens={
                "input_tokens": 120,
                "output_tokens": 50,
                "reasoning_tokens": 20,
                "total_tokens": 170,
            },
        )
        arch_unknown = self.event(
            self.records[1]["key"],
            "arch-unknown",
            5_003,
            model="gpt-5.6-sol",
            reasoning_effort="",
            tokens={
                "input_tokens": 80,
                "output_tokens": 20,
                "reasoning_tokens": 5,
                "total_tokens": 100,
            },
        )
        self.store.ingest_events(
            "gamma",
            [plus_success, plus_failed],
            now=5_010,
        )
        self.store.ingest_events(
            "alpha",
            [arch_high, arch_unknown],
            now=5_010,
        )
        with self.store._connection() as connection:
            connection.execute(
                """
                INSERT INTO usage_events(
                    event_key, account, user_email, key_label, occurred_at,
                    request_id, provider, model, alias, reasoning_effort,
                    endpoint, failed, reasoning_tokens, total_tokens
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    "legacy-collector-after-start",
                    "gamma",
                    "alice@example.com",
                    "alice@example.com:gamma",
                    5_004,
                    "legacy-after-start",
                    "openai",
                    "gpt-5.4",
                    "",
                    "high",
                    "POST /v1/responses",
                    0,
                    999,
                    999,
                ),
            )

        breakdown = self.store.usage_breakdown_for_user(
            "alice@example.com",
            window_seconds=None,
            now=6_000,
        )

        self.assertEqual(breakdown["collection_started_at"], 5_000)
        self.assertEqual(breakdown["totals"]["success_count"], 3)
        self.assertEqual(breakdown["totals"]["failed_count"], 1)
        self.assertEqual(breakdown["totals"]["request_count"], 4)
        self.assertEqual(breakdown["totals"]["input_tokens"], 300)
        self.assertEqual(breakdown["totals"]["output_tokens"], 110)
        self.assertEqual(breakdown["totals"]["known_effort_count"], 2)
        self.assertEqual(breakdown["totals"]["reasoning_tokens"], 35)
        self.assertEqual(breakdown["totals"]["cached_tokens"], 20)
        self.assertEqual(breakdown["totals"]["total_tokens"], 410)
        self.assertEqual(
            [(item["model"], item["success_count"]) for item in breakdown["models"]],
            [("gpt-5.6-sol", 2), ("gpt-5.6-terra", 1)],
        )
        self.assertEqual(
            sum(item["success_count"] for item in breakdown["models"]),
            breakdown["totals"]["success_count"],
        )
        self.assertEqual(
            {
                item["reasoning_effort"]: item["success_count"]
                for item in breakdown["reasoning_efforts"]
            },
            {"xhigh": 1, "high": 1, "unknown": 1},
        )
        self.assertEqual(
            sum(item["success_count"] for item in breakdown["reasoning_efforts"]),
            breakdown["totals"]["success_count"],
        )

        plus_only = self.store.usage_breakdown_for_user(
            "alice@example.com",
            window_seconds=None,
            now=6_000,
            account="gamma",
        )
        self.assertEqual(plus_only["totals"]["success_count"], 1)
        self.assertEqual(plus_only["totals"]["failed_count"], 1)
        self.assertEqual(len(plus_only["combinations"]), 1)
        self.assertEqual(
            {
                field: plus_only["combinations"][0][field]
                for field in (
                    "request_count",
                    "input_tokens",
                    "output_tokens",
                    "reasoning_tokens",
                    "cached_tokens",
                    "total_tokens",
                )
            },
            {
                "request_count": 2,
                "input_tokens": 100,
                "output_tokens": 40,
                "reasoning_tokens": 10,
                "cached_tokens": 20,
                "total_tokens": 140,
            },
        )

    def test_account_usage_breakdown_aggregates_raw_tokens_by_model_and_effort(self):
        bob_key = "cpa_alpha_bob_0123456789abcdef"
        self.store.sync_identities(
            [
                *self.records,
                {
                    "key": bob_key,
                    "label": "bob@example.com:gamma",
                    "user": "bob@example.com",
                    "account": "gamma",
                },
            ],
            now=5_000,
        )
        self.store.ensure_usage_breakdown_started(now=5_000)
        self.store.ingest_events(
            "gamma",
            [
                self.event(
                    self.records[0]["key"],
                    "account-sol-xhigh",
                    5_001,
                    model="gpt-5.6-sol",
                    reasoning_effort="xhigh",
                    tokens={
                        "input_tokens": 100,
                        "output_tokens": 40,
                        "reasoning_tokens": 10,
                        "cached_tokens": 20,
                        "total_tokens": 140,
                    },
                ),
                self.event(
                    bob_key,
                    "account-sol-high",
                    5_002,
                    model="gpt-5.6-sol",
                    reasoning_effort="high",
                    tokens={
                        "input_tokens": 200,
                        "output_tokens": 60,
                        "reasoning_tokens": 30,
                        "cached_tokens": 80,
                        "total_tokens": 260,
                    },
                ),
                self.event(
                    bob_key,
                    "account-terra-medium",
                    5_003,
                    model="gpt-5.6-terra",
                    reasoning_effort="medium",
                    tokens={
                        "input_tokens": 50,
                        "output_tokens": 10,
                        "reasoning_tokens": 5,
                        "cached_tokens": 0,
                        "total_tokens": 60,
                    },
                ),
                self.event(
                    bob_key,
                    "account-sol-xhigh-failed",
                    5_004,
                    model="gpt-5.6-sol",
                    reasoning_effort="xhigh",
                    failed=True,
                    tokens={
                        "input_tokens": 3,
                        "output_tokens": 1,
                        "reasoning_tokens": 1,
                        "cached_tokens": 0,
                        "total_tokens": 4,
                    },
                ),
            ],
            now=5_010,
        )
        self.store.ingest_events(
            "alpha",
            [
                self.event(
                    self.records[1]["key"],
                    "other-account",
                    5_005,
                    tokens={"total_tokens": 999},
                )
            ],
            now=5_010,
        )

        breakdown = self.store.usage_breakdown_for_account(
            "gamma",
            window_seconds=None,
            now=6_000,
        )

        self.assertEqual(breakdown["collection_started_at"], 5_000)
        self.assertEqual(
            breakdown["totals"],
            {
                "request_count": 4,
                "success_count": 3,
                "failed_count": 1,
                "input_tokens": 353,
                "output_tokens": 111,
                "reasoning_tokens": 46,
                "cached_tokens": 100,
                "total_tokens": 464,
                "last_used_at": 5_004,
            },
        )
        self.assertEqual(
            [(item["model"], item["total_tokens"]) for item in breakdown["models"]],
            [("gpt-5.6-sol", 404), ("gpt-5.6-terra", 60)],
        )
        self.assertEqual(
            {
                (item["model"], item["reasoning_effort"]): (
                    item["request_count"],
                    item["failed_count"],
                    item["total_tokens"],
                )
                for item in breakdown["combinations"]
            },
            {
                ("gpt-5.6-sol", "xhigh"): (2, 1, 144),
                ("gpt-5.6-sol", "high"): (1, 0, 260),
                ("gpt-5.6-terra", "medium"): (1, 0, 60),
            },
        )
        self.assertNotIn("weighted", str(breakdown))

        recent = self.store.usage_breakdown_for_account(
            "gamma",
            window_seconds=None,
            now=6_000,
            start_at=5_003,
        )
        self.assertEqual(recent["totals"]["request_count"], 2)
        self.assertEqual(recent["totals"]["total_tokens"], 64)

    def test_internal_probe_and_unknown_key_are_not_persisted(self):
        probe = self.event(
            self.records[0]["key"],
            "probe-1",
            2_000,
            endpoint="GET /v1/models",
        )
        unknown = self.event("cpa_unknown_secret", "unknown-1", 2_000)

        result = self.store.ingest_events("gamma", [probe, unknown], now=2_000)

        self.assertEqual(result["ignored"], 1)
        self.assertEqual(result["unmapped"], 1)
        with closing(sqlite3.connect(str(self.db))) as connection:
            self.assertEqual(connection.execute("SELECT COUNT(*) FROM usage_events").fetchone()[0], 0)
            columns = [row[1] for row in connection.execute("PRAGMA table_info(key_identities)")]
        self.assertNotIn("api_key", columns)
        self.assertNotIn("key", columns)

    def test_same_request_id_from_different_keys_is_not_collapsed(self):
        bob = {
            "key": "cpa_alpha_bob_0011223344556677",
            "label": "bob@example.com:gamma",
            "user": "bob@example.com",
            "account": "gamma",
        }
        self.store.sync_identities([bob], now=3_000)

        result = self.store.ingest_events(
            "gamma",
            [
                self.event(self.records[0]["key"], "shared-request-id", 3_000),
                self.event(bob["key"], "shared-request-id", 3_000),
            ],
            now=3_000,
        )

        self.assertEqual(result["inserted"], 2)

    def test_window_filter_and_collector_status(self):
        now = 20_000
        old = self.event(self.records[0]["key"], "old", now - 7200)
        recent = self.event(self.records[0]["key"], "recent", now - 60)
        self.store.ingest_events("gamma", [old, recent], now=now)

        recent_usage = self.store.usage_for_users(
            ["alice@example.com"], ["gamma"], window_seconds=3600, now=now
        )["alice@example.com"]
        all_usage = self.store.usage_for_users(
            ["alice@example.com"], ["gamma"], window_seconds=None, now=now
        )["alice@example.com"]
        activity = self.store.account_activity(
            ["gamma"], window_seconds=3600, now=now
        )["gamma"]
        self.assertEqual(recent_usage["request_count"], 1)
        self.assertEqual(all_usage["request_count"], 2)
        self.assertEqual(activity["active_users"], 1)
        self.assertEqual(activity["request_count"], 1)

        self.store.update_collector_status(now=now)
        self.assertEqual(self.store.status(now=now + 5)["status"], "healthy")
        self.store.update_collector_status("gamma unavailable", now=now + 6)
        status = self.store.status(now=now + 7)
        self.assertEqual(status["status"], "degraded")
        self.assertEqual(status["event_count"], 2)

    def test_explicit_start_includes_boundary_and_overrides_window_seconds(self):
        now = 20_000
        before = self.event(self.records[0]["key"], "before-start", 4_999)
        boundary = self.event(self.records[0]["key"], "at-start", 5_000)
        self.store.ingest_events("gamma", [before, boundary], now=now)

        user_usage = self.store.usage_for_users(
            ["alice@example.com"],
            ["gamma"],
            window_seconds=1,
            now=now,
            start_at=5_000,
        )["alice@example.com"]
        account_usage = self.store.usage_for_accounts(
            ["gamma"],
            window_seconds=1,
            now=now,
            start_at=5_000,
        )["gamma"]

        self.assertEqual(user_usage["request_count"], 1)
        self.assertEqual(user_usage["last_used_at"], 5_000)
        self.assertEqual(account_usage["request_count"], 1)
        self.assertEqual(account_usage["active_users"], 1)

    def test_explicit_end_excludes_boundary_across_usage_aggregates(self):
        now = 20_000
        self.store.ensure_usage_breakdown_started(now=4_000)
        self.store.ingest_events(
            "gamma",
            [
                self.event(self.records[0]["key"], "before-custom", 4_999),
                self.event(self.records[0]["key"], "custom-start", 5_000),
                self.event(self.records[0]["key"], "custom-last", 5_999),
                self.event(self.records[0]["key"], "custom-end", 6_000),
            ],
            now=now,
        )

        user_usage = self.store.usage_for_users(
            ["alice@example.com"],
            ["gamma"],
            window_seconds=None,
            now=now,
            start_at=5_000,
            end_at=6_000,
        )["alice@example.com"]
        account_usage = self.store.usage_for_accounts(
            ["gamma"],
            window_seconds=None,
            now=now,
            start_at=5_000,
            end_at=6_000,
        )["gamma"]
        user_breakdown = self.store.usage_breakdown_for_user(
            "alice@example.com",
            window_seconds=None,
            now=now,
            start_at=5_000,
            end_at=6_000,
        )
        account_breakdown = self.store.usage_breakdown_for_account(
            "gamma",
            window_seconds=None,
            now=now,
            start_at=5_000,
            end_at=6_000,
        )

        self.assertEqual(user_usage["request_count"], 2)
        self.assertEqual(user_usage["last_used_at"], 5_999)
        self.assertEqual(account_usage["request_count"], 2)
        self.assertEqual(account_usage["last_used_at"], 5_999)
        self.assertEqual(user_breakdown["totals"]["request_count"], 2)
        self.assertEqual(account_breakdown["totals"]["request_count"], 2)

    def test_account_usage_supports_independent_start_times(self):
        now = 20_000
        self.store.ingest_events(
            "gamma",
            [self.event(self.records[0]["key"], "plus-in-window", 5_000)],
            now=now,
        )
        self.store.ingest_events(
            "alpha",
            [self.event(self.records[1]["key"], "arch-before-window", 6_000)],
            now=now,
        )

        usage = self.store.usage_for_accounts(
            ["gamma", "alpha"],
            now=now,
            start_at_by_account={"gamma": 5_000, "alpha": 7_000},
        )

        self.assertEqual(usage["gamma"]["request_count"], 1)
        self.assertEqual(usage["gamma"]["total_tokens"], 140)
        self.assertEqual(usage["alpha"]["request_count"], 0)

    def test_token_time_series_buckets_accounts_and_top_users(self):
        now = 10_000
        bob = {
            "key": "cpa_alpha_bob_0011223344556677",
            "label": "bob@example.com:gamma",
            "user": "bob@example.com",
            "account": "gamma",
        }
        self.store.sync_identities([bob], now=now)
        self.store.ingest_events(
            "gamma",
            [
                self.event(
                    self.records[0]["key"],
                    "alice-plus",
                    9_250,
                    tokens={"total_tokens": 100},
                ),
                self.event(
                    bob["key"],
                    "bob-plus",
                    9_620,
                    tokens={"total_tokens": 200},
                ),
            ],
            now=now,
        )
        self.store.ingest_events(
            "alpha",
            [
                self.event(
                    self.records[1]["key"],
                    "alice-arch",
                    9_610,
                    tokens={"total_tokens": 50},
                )
            ],
            now=now,
        )

        payload = self.store.token_time_series(
            ["gamma", "alpha"],
            ["alice@example.com", "bob@example.com"],
            window_seconds=900,
            bucket_seconds=300,
            now=now,
            user_limit=1,
        )

        self.assertEqual(payload["buckets"], [9_000, 9_300, 9_600, 9_900])
        accounts = {item["name"]: item for item in payload["accounts"]}
        self.assertEqual(accounts["gamma"]["values"], [100, 0, 200, 0])
        self.assertEqual(accounts["gamma"]["total"], 300)
        self.assertEqual(accounts["alpha"]["values"], [0, 0, 50, 0])
        self.assertEqual([item["name"] for item in payload["users"]], ["bob@example.com"])
        self.assertEqual(payload["users"][0]["maximum"], 200)

        filtered = self.store.token_time_series(
            ["gamma", "alpha"],
            ["alice@example.com", "bob@example.com"],
            window_seconds=900,
            bucket_seconds=300,
            now=now,
            account="gamma",
            user_email="alice@example.com",
        )
        self.assertEqual([item["name"] for item in filtered["accounts"]], ["gamma"])
        self.assertEqual(filtered["accounts"][0]["values"], [100, 0, 0, 0])
        self.assertEqual(filtered["users"][0]["values"], [100, 0, 0, 0])

        multi_filtered = self.store.token_time_series(
            ["gamma", "alpha"],
            ["alice@example.com", "bob@example.com"],
            window_seconds=900,
            bucket_seconds=300,
            now=now,
            account=["gamma", "alpha"],
            user_email=["alice@example.com", "bob@example.com"],
        )
        self.assertEqual(
            [item["name"] for item in multi_filtered["accounts"]],
            ["gamma", "alpha"],
        )
        self.assertEqual(
            [item["name"] for item in multi_filtered["users"]],
            ["alice@example.com", "bob@example.com"],
        )
        self.assertEqual(multi_filtered["users"][1]["total"], 200)

        with self.assertRaisesRegex(ValueError, "不能超过 400 个时间桶"):
            self.store.token_time_series(
                ["gamma"],
                ["alice@example.com"],
                window_seconds=400,
                bucket_seconds=1,
                now=now,
            )

    def test_token_time_series_supports_independent_account_windows(self):
        now = 20_000
        self.store.ingest_events(
            "gamma",
            [
                self.event(
                    self.records[0]["key"],
                    "plus-before-period",
                    9_400,
                    tokens={"total_tokens": 100},
                ),
                self.event(
                    self.records[0]["key"],
                    "plus-in-period",
                    9_600,
                    tokens={"total_tokens": 200},
                ),
            ],
            now=now,
        )
        self.store.ingest_events(
            "alpha",
            [
                self.event(
                    self.records[1]["key"],
                    "arch-before-period",
                    10_400,
                    tokens={"total_tokens": 50},
                ),
                self.event(
                    self.records[1]["key"],
                    "arch-in-period",
                    10_600,
                    tokens={"total_tokens": 80},
                ),
            ],
            now=now,
        )

        payload = self.store.token_time_series(
            ["gamma", "alpha"],
            ["alice@example.com"],
            window_seconds=10_000,
            bucket_seconds=1_000,
            now=now,
            start_at_by_account={"gamma": 9_500, "alpha": 10_500},
        )

        accounts = {item["name"]: item for item in payload["accounts"]}
        self.assertEqual(accounts["gamma"]["total"], 200)
        self.assertEqual(accounts["alpha"]["total"], 80)
        self.assertEqual(accounts["gamma"]["average"], 17)
        self.assertEqual(accounts["alpha"]["average"], 7)
        self.assertEqual(payload["users"][0]["total"], 280)

    def test_token_time_series_30_day_performance_and_index_plan(self):
        now = 2_000_000_000
        accounts = ["cpa-{}".format(index) for index in range(8)]
        users = ["user-{:03d}@example.com".format(index) for index in range(200)]
        row_count = 120_000
        rows = (
            (
                "synthetic-event-{}".format(index),
                accounts[index % len(accounts)],
                users[index % len(users)],
                "synthetic",
                now - (index % (30 * 24 * 60 * 60)),
                "synthetic-request-{}".format(index),
                100 + (index % 1_000),
            )
            for index in range(row_count)
        )
        with closing(sqlite3.connect(str(self.db))) as connection:
            connection.executemany(
                "INSERT INTO usage_events("
                "event_key, account, user_email, key_label, occurred_at, request_id, total_tokens"
                ") VALUES (?, ?, ?, ?, ?, ?, ?)",
                rows,
            )
            connection.commit()

        # A WAL reader must continue while the collector owns the independent
        # writer transaction; the overview path does not take an application
        # write lock merely to aggregate the chart.
        with closing(sqlite3.connect(str(self.db), timeout=0.1)) as writer:
            writer.execute("BEGIN IMMEDIATE")
            writer.execute(
                "INSERT OR REPLACE INTO usage_meta(key, value) VALUES (?, ?)",
                ("performance-probe", "pending"),
            )
            started = time.perf_counter()
            payload = self.store.token_time_series(
                accounts,
                users,
                window_seconds=30 * 24 * 60 * 60,
                bucket_seconds=6 * 60 * 60,
                now=now,
                user_limit=10,
            )
            elapsed = time.perf_counter() - started
            writer.rollback()

        self.assertLess(elapsed, 0.5, "30-day 120k-row series must complete within 500 ms")
        self.assertEqual(len(payload["buckets"]), 121)
        self.assertEqual(len(payload["accounts"]), len(accounts))
        self.assertEqual(len(payload["users"]), 10)

        placeholders = ",".join("?" for _ in accounts)
        with closing(sqlite3.connect(str(self.db))) as connection:
            plans = {
                "usage_events_account_time": connection.execute(
                    "EXPLAIN QUERY PLAN SELECT account, user_email, "
                    "CAST(occurred_at / 21600 AS INTEGER) * 21600 AS bucket_at, "
                    "SUM(total_tokens) FROM usage_events "
                    "WHERE account IN ({}) AND occurred_at >= ? AND occurred_at <= ? "
                    "GROUP BY account, user_email, bucket_at".format(placeholders),
                    (*accounts, now - 30 * 24 * 60 * 60, now),
                ).fetchall(),
                "usage_events_user_time": connection.execute(
                    "EXPLAIN QUERY PLAN SELECT SUM(total_tokens) FROM usage_events "
                    "WHERE user_email = ? AND occurred_at >= ? AND occurred_at <= ?",
                    (users[0], now - 30 * 24 * 60 * 60, now),
                ).fetchall(),
                "usage_events_time_user": connection.execute(
                    "EXPLAIN QUERY PLAN SELECT user_email, SUM(total_tokens) "
                    "FROM usage_events INDEXED BY usage_events_time_user "
                    "WHERE occurred_at >= ? AND occurred_at <= ? GROUP BY user_email",
                    (now - 30 * 24 * 60 * 60, now),
                ).fetchall(),
            }

        for index_name, rows in plans.items():
            detail = " ".join(str(row[3]) for row in rows)
            self.assertIn(index_name, detail, "query plan must use {}: {}".format(index_name, detail))

    def test_one_key_is_attributed_to_the_actual_cpa_and_account_activity(self):
        shared_key = "cpa_alice_0123456789abcdef"
        self.store.sync_identities(
            [
                {
                    "key": shared_key,
                    "label": "alice@example.com:gamma",
                    "user": "alice@example.com",
                    "account": "gamma",
                },
                {
                    "key": shared_key,
                    "label": "alice@example.com:alpha",
                    "user": "alice@example.com",
                    "account": "alpha",
                },
            ],
            now=30_000,
        )
        self.store.ingest_events(
            "gamma",
            [self.event(shared_key, "plus", 30_010)],
            now=30_020,
        )
        self.store.ingest_events(
            "alpha",
            [self.event(shared_key, "arch", 30_015)],
            now=30_020,
        )

        usage = self.store.usage_for_users(
            ["alice@example.com"],
            ["gamma", "alpha"],
            window_seconds=3600,
            now=30_020,
        )["alice@example.com"]
        self.assertEqual(usage["request_count"], 2)
        self.assertEqual(usage["accounts"]["gamma"]["request_count"], 1)
        self.assertEqual(usage["accounts"]["alpha"]["request_count"], 1)
        activity = self.store.account_activity(
            ["gamma", "alpha"],
            window_seconds=3600,
            now=30_020,
            include_user_emails=True,
        )
        self.assertEqual(activity["gamma"]["active_users"], 1)
        self.assertEqual(activity["gamma"]["active_user_emails"], ["alice@example.com"])
        self.assertEqual(activity["alpha"]["request_count"], 1)
        self.assertEqual(activity["alpha"]["active_user_emails"], ["alice@example.com"])

    def test_account_activity_counts_users_with_failed_requests(self):
        self.store.sync_identities(
            [
                {
                    "key": "success-key",
                    "label": "alice@example.com:alpha",
                    "user": "alice@example.com",
                    "account": "alpha",
                },
                {
                    "key": "failed-key",
                    "label": "bob@example.com:alpha",
                    "user": "bob@example.com",
                    "account": "alpha",
                },
            ],
            now=10_000,
        )
        self.store.ingest_events(
            "alpha",
            [
                {
                    "timestamp": 10_010,
                    "api_key": "success-key",
                    "request_id": "successful-request",
                    "tokens": {"total_tokens": 10},
                },
                {
                    "timestamp": 10_020,
                    "api_key": "failed-key",
                    "request_id": "failed-request",
                    "failed": True,
                    "tokens": {"total_tokens": 0},
                },
            ],
            now=10_020,
        )

        activity = self.store.account_activity(
            ["alpha"], now=10_030, include_user_emails=True
        )["alpha"]

        self.assertEqual(activity["active_users"], 2)
        self.assertEqual(
            activity["active_user_emails"],
            ["alice@example.com", "bob@example.com"],
        )
        self.assertEqual(activity["request_count"], 2)

    def test_key_rotation_preserves_user_and_per_cpa_usage_history(self):
        old_key = "cpa_alice_legacy012345"
        new_key = "cpa_alice_12345678-1234-4123-8123-123456789abc"
        old_record = {
            "key": old_key,
            "label": "alice@example.com:gamma",
            "user": "alice@example.com",
            "account": "gamma",
        }
        new_records = [
            {
                "key": new_key,
                "label": "alice@example.com:" + account,
                "user": "alice@example.com",
                "account": account,
            }
            for account in ("gamma", "alpha")
        ]
        self.store.sync_identities([old_record], now=50_000)
        self.store.ingest_events(
            "gamma",
            [self.event(old_key, "before-rotation", 50_010)],
            now=50_010,
        )
        self.store.sync_identities(new_records, now=50_020)
        self.store.ingest_events(
            "alpha",
            [self.event(new_key, "after-rotation", 50_030)],
            now=50_030,
        )

        usage = self.store.usage_for_users(
            ["alice@example.com"],
            ["gamma", "alpha"],
            window_seconds=None,
            now=50_040,
        )["alice@example.com"]
        self.assertEqual(usage["request_count"], 2)
        self.assertEqual(usage["total_tokens"], 280)
        self.assertEqual(usage["accounts"]["gamma"]["request_count"], 1)
        self.assertEqual(usage["accounts"]["alpha"]["request_count"], 1)

    def test_portal_session_is_hashed_expires_and_can_be_revoked(self):
        session = self.store.create_session(
            "alice@example.com", ttl_seconds=60, now=40_000
        )
        self.assertEqual(
            self.store.resolve_session(session["token"], now=40_030)["user"],
            "alice@example.com",
        )
        with closing(sqlite3.connect(str(self.db))) as connection:
            stored = connection.execute(
                "SELECT session_hash FROM portal_sessions"
            ).fetchone()[0]
        self.assertNotEqual(stored, session["token"])
        self.assertIsNone(self.store.resolve_session(session["token"], now=40_061))

        session = self.store.create_session("alice@example.com", now=50_000)
        self.assertTrue(self.store.revoke_session(session["token"]))
        self.assertIsNone(self.store.resolve_session(session["token"], now=50_001))


if __name__ == "__main__":
    unittest.main()
