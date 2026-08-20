#!/usr/bin/env python3
"""Persistent per-user usage accounting for CLIProxyAPI usage queue events."""

import datetime
import hashlib
import json
import math
import os
import secrets
import sqlite3
import time
from contextlib import contextmanager
from pathlib import Path
try:
    from zoneinfo import ZoneInfo
except ImportError:  # pragma: no cover - production and CI use Python 3.9+
    ZoneInfo = None


SCHEMA_VERSION = 10
USAGE_BREAKDOWN_STARTED_AT_KEY = "usage_breakdown_started_at"
WEEKLY_USAGE_BACKFILL_KEY = "weekly_usage_backfill_version"
WEEKLY_USAGE_BACKFILL_VERSION = "2"
WEEKLY_USAGE_LAST_EVENT_ID_KEY = "weekly_usage_last_event_id"
DEFAULT_WEEK_TIMEZONE = "UTC"
WEEK_TIMEZONE_META_KEY = "weekly_usage_timezone"
MAX_WEEKLY_QUOTA_TOKENS = 1_000_000_000_000
REASONING_EFFORTS = (
    "none",
    "minimal",
    "low",
    "medium",
    "high",
    "xhigh",
    "max",
    "ultra",
    "auto",
    "unknown",
)
DEFAULT_REASONING_EFFORT_MULTIPLIERS = {
    effort: (2.0 if effort == "max" else 1.0)
    for effort in REASONING_EFFORTS
}
REASONING_MULTIPLIER_CONFIG_PREFIX = "user_quota.reasoning_multiplier."


def _non_negative_int(value):
    try:
        return max(0, int(value or 0))
    except (TypeError, ValueError, OverflowError):
        return 0


def _timestamp(value, fallback=None):
    fallback = int(time.time()) if fallback is None else int(fallback)
    if isinstance(value, (int, float)):
        return int(value)
    raw = str(value or "").strip()
    if not raw:
        return fallback
    try:
        return int(datetime.datetime.fromisoformat(raw.replace("Z", "+00:00")).timestamp())
    except (TypeError, ValueError, OverflowError):
        return fallback


def _key_hash(value):
    raw = str(value or "").strip()
    if raw.lower().startswith("bearer "):
        raw = raw[7:].strip()
    return hashlib.sha256(raw.encode("utf-8")).hexdigest() if raw else ""


def _filter_values(value, lower=False):
    raw_values = value if isinstance(value, (list, tuple)) else [value]
    values = []
    for raw_value in raw_values:
        for part in str(raw_value or "").split(","):
            normalized = part.strip().lower() if lower else part.strip()
            if normalized and normalized not in values:
                values.append(normalized)
    return values


def normalize_reasoning_effort(value):
    effort = str(value or "").strip().lower()
    return effort if effort in REASONING_EFFORTS else "unknown"


def reasoning_effort_multipliers(configuration=None):
    configuration = configuration if isinstance(configuration, dict) else {}
    result = {}
    for effort in REASONING_EFFORTS:
        raw = configuration.get(
            REASONING_MULTIPLIER_CONFIG_PREFIX + effort,
            configuration.get(
                effort,
                DEFAULT_REASONING_EFFORT_MULTIPLIERS[effort],
            ),
        )
        try:
            multiplier = float(raw)
        except (TypeError, ValueError, OverflowError):
            multiplier = DEFAULT_REASONING_EFFORT_MULTIPLIERS[effort]
        if not math.isfinite(multiplier) or multiplier <= 0:
            multiplier = DEFAULT_REASONING_EFFORT_MULTIPLIERS[effort]
        result[effort] = multiplier
    return result


def reasoning_weight_policy_version(multipliers):
    normalized = reasoning_effort_multipliers(multipliers)
    semantic = json.dumps(
        normalized,
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    )
    return "reasoning-{}".format(
        hashlib.sha256(semantic.encode("utf-8")).hexdigest()[:12]
    )


def weighted_token_count(total_tokens, reasoning_effort, multipliers=None):
    total = _non_negative_int(total_tokens)
    effort = normalize_reasoning_effort(reasoning_effort)
    normalized = reasoning_effort_multipliers(multipliers)
    return int(math.floor(total * normalized[effort] + 0.5))


def _empty_usage():
    return {
        "request_count": 0,
        "success_count": 0,
        "failed_count": 0,
        "input_tokens": 0,
        "output_tokens": 0,
        "reasoning_tokens": 0,
        "cached_tokens": 0,
        "total_tokens": 0,
        "weighted_tokens": 0,
        "last_used_at": 0,
    }


def _week_timezone(value):
    name = str(value or DEFAULT_WEEK_TIMEZONE).strip()
    if ZoneInfo is None:
        if name == "UTC":
            return name, datetime.timezone.utc
        if name == "Asia/Shanghai":
            return name, datetime.timezone(datetime.timedelta(hours=8))
        raise ValueError("该用户额度时区需要 Python 3.9 或更高版本")
    try:
        return name, ZoneInfo(name)
    except (KeyError, ValueError) as error:
        raise ValueError("用户额度时区无效：{}".format(name)) from error


def natural_week_bounds(value=None, timezone_name=DEFAULT_WEEK_TIMEZONE):
    """Return the configured natural-week [start, end) epoch range."""
    timestamp = int(time.time()) if value is None else int(value)
    unused_name, zone = _week_timezone(timezone_name)
    local = datetime.datetime.fromtimestamp(timestamp, zone)
    start = (local - datetime.timedelta(days=local.weekday())).replace(
        hour=0,
        minute=0,
        second=0,
        microsecond=0,
    )
    end = start + datetime.timedelta(days=7)
    return int(start.timestamp()), int(end.timestamp())


class UsageStore:
    def __init__(
        self,
        path,
        week_timezone=DEFAULT_WEEK_TIMEZONE,
        reset_personal_weekly_on_new_week=True,
    ):
        self.path = Path(path).resolve()
        self.week_timezone, unused_zone = _week_timezone(week_timezone)
        self.reset_personal_weekly_on_new_week = bool(
            reset_personal_weekly_on_new_week
        )
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self._initialize()
        self.configure_personal_quota_weekly_reset(
            self.reset_personal_weekly_on_new_week,
            reschedule=True,
        )

    def _connect(self):
        connection = sqlite3.connect(str(self.path), timeout=10)
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA busy_timeout = 10000")
        return connection

    @contextmanager
    def _connection(self):
        connection = self._connect()
        try:
            with connection:
                yield connection
        finally:
            connection.close()

    def _initialize(self):
        with self._connection() as connection:
            previous_version = connection.execute("PRAGMA user_version").fetchone()[0]
            connection.execute("PRAGMA journal_mode = WAL")
            connection.executescript(
                """
                CREATE TABLE IF NOT EXISTS key_identities (
                    key_hash TEXT PRIMARY KEY,
                    key_label TEXT NOT NULL,
                    user_email TEXT NOT NULL,
                    account TEXT NOT NULL,
                    team_id TEXT NOT NULL DEFAULT '',
                    team_membership_version INTEGER NOT NULL DEFAULT 0,
                    first_seen_at INTEGER NOT NULL,
                    last_seen_at INTEGER NOT NULL
                );

                CREATE TABLE IF NOT EXISTS usage_events (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    event_key TEXT NOT NULL UNIQUE,
                    account TEXT NOT NULL,
                    user_email TEXT NOT NULL,
                    key_label TEXT NOT NULL,
                    occurred_at INTEGER NOT NULL,
                    request_id TEXT NOT NULL DEFAULT '',
                    provider TEXT NOT NULL DEFAULT '',
                    model TEXT NOT NULL DEFAULT '',
                    alias TEXT NOT NULL DEFAULT '',
                    reasoning_effort TEXT NOT NULL DEFAULT '',
                    endpoint TEXT NOT NULL DEFAULT '',
                    failed INTEGER NOT NULL DEFAULT 0,
                    latency_ms INTEGER NOT NULL DEFAULT 0,
                    input_tokens INTEGER NOT NULL DEFAULT 0,
                    output_tokens INTEGER NOT NULL DEFAULT 0,
                    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
                    cached_tokens INTEGER NOT NULL DEFAULT 0,
                    total_tokens INTEGER NOT NULL DEFAULT 0,
                    quota_multiplier REAL NOT NULL DEFAULT 1.0,
                    weighted_tokens INTEGER NOT NULL DEFAULT 0,
                    weight_policy_version TEXT NOT NULL DEFAULT 'legacy-v1',
                    team_id TEXT NOT NULL DEFAULT '',
                    team_membership_version INTEGER NOT NULL DEFAULT 0
                );

                CREATE INDEX IF NOT EXISTS usage_events_user_time
                    ON usage_events(user_email, occurred_at);
                CREATE INDEX IF NOT EXISTS usage_events_account_time
                    ON usage_events(account, occurred_at);
                CREATE INDEX IF NOT EXISTS usage_events_time_user
                    ON usage_events(occurred_at, user_email);

                CREATE TABLE IF NOT EXISTS usage_meta (
                    key TEXT PRIMARY KEY,
                    value TEXT NOT NULL
                );

                CREATE TABLE IF NOT EXISTS portal_sessions (
                    session_hash TEXT PRIMARY KEY,
                    user_email TEXT NOT NULL,
                    created_at INTEGER NOT NULL,
                    expires_at INTEGER NOT NULL
                );

                CREATE INDEX IF NOT EXISTS portal_sessions_expiry
                    ON portal_sessions(expires_at);

                CREATE TABLE IF NOT EXISTS portal_credentials (
                    user_email TEXT PRIMARY KEY,
                    password_hash TEXT NOT NULL,
                    must_change INTEGER NOT NULL DEFAULT 1 CHECK(must_change IN (0, 1)),
                    created_at INTEGER NOT NULL,
                    updated_at INTEGER NOT NULL
                );

                CREATE TABLE IF NOT EXISTS user_quota_policies (
                    user_email TEXT PRIMARY KEY,
                    weekly_tokens INTEGER CHECK(weekly_tokens IS NULL OR weekly_tokens > 0),
                    created_at INTEGER NOT NULL,
                    updated_at INTEGER NOT NULL,
                    created_by TEXT NOT NULL DEFAULT 'admin',
                    reset_at INTEGER
                );

                CREATE TABLE IF NOT EXISTS user_weekly_usage (
                    user_email TEXT NOT NULL,
                    week_start_at INTEGER NOT NULL,
                    total_tokens INTEGER NOT NULL DEFAULT 0,
                    weighted_tokens INTEGER NOT NULL DEFAULT 0,
                    request_count INTEGER NOT NULL DEFAULT 0,
                    updated_at INTEGER NOT NULL,
                    PRIMARY KEY(user_email, week_start_at)
                );

                CREATE INDEX IF NOT EXISTS user_weekly_usage_week
                    ON user_weekly_usage(week_start_at, user_email);

                CREATE TABLE IF NOT EXISTS user_quota_adjustments (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    user_email TEXT NOT NULL,
                    week_start_at INTEGER NOT NULL,
                    action TEXT NOT NULL CHECK(action IN ('bonus', 'usage_reset')),
                    token_amount INTEGER NOT NULL CHECK(token_amount > 0),
                    reason TEXT NOT NULL,
                    created_at INTEGER NOT NULL,
                    created_by TEXT NOT NULL DEFAULT 'admin'
                );

                CREATE INDEX IF NOT EXISTS user_quota_adjustments_user_week
                    ON user_quota_adjustments(user_email, week_start_at, created_at);
                """
            )
            connection.execute("BEGIN IMMEDIATE")
            columns = {
                row["name"] for row in connection.execute("PRAGMA table_info(usage_events)")
            }
            if "alias" not in columns:
                connection.execute(
                    "ALTER TABLE usage_events ADD COLUMN alias TEXT NOT NULL DEFAULT ''"
                )
            if "reasoning_effort" not in columns:
                connection.execute(
                    "ALTER TABLE usage_events "
                    "ADD COLUMN reasoning_effort TEXT NOT NULL DEFAULT ''"
                )
            if "quota_multiplier" not in columns:
                connection.execute(
                    "ALTER TABLE usage_events "
                    "ADD COLUMN quota_multiplier REAL NOT NULL DEFAULT 1.0"
                )
            if "weighted_tokens" not in columns:
                connection.execute(
                    "ALTER TABLE usage_events "
                    "ADD COLUMN weighted_tokens INTEGER NOT NULL DEFAULT 0"
                )
                # Existing events remain unweighted. New multiplier rules are
                # intentionally prospective so deploying or editing a rule
                # cannot retroactively exhaust a user's current-week quota.
                connection.execute(
                    "UPDATE usage_events SET weighted_tokens = total_tokens"
                )
            if "weight_policy_version" not in columns:
                connection.execute(
                    "ALTER TABLE usage_events ADD COLUMN "
                    "weight_policy_version TEXT NOT NULL DEFAULT 'legacy-v1'"
                )
            if "team_id" not in columns:
                connection.execute(
                    "ALTER TABLE usage_events "
                    "ADD COLUMN team_id TEXT NOT NULL DEFAULT ''"
                )
            if "team_membership_version" not in columns:
                connection.execute(
                    "ALTER TABLE usage_events "
                    "ADD COLUMN team_membership_version INTEGER NOT NULL DEFAULT 0"
                )
            identity_columns = {
                row["name"]
                for row in connection.execute("PRAGMA table_info(key_identities)")
            }
            if "team_id" not in identity_columns:
                connection.execute(
                    "ALTER TABLE key_identities "
                    "ADD COLUMN team_id TEXT NOT NULL DEFAULT ''"
                )
            if "team_membership_version" not in identity_columns:
                connection.execute(
                    "ALTER TABLE key_identities "
                    "ADD COLUMN team_membership_version INTEGER NOT NULL DEFAULT 0"
                )
            connection.execute(
                "CREATE INDEX IF NOT EXISTS usage_events_team_time "
                "ON usage_events(team_id, occurred_at)"
            )
            weekly_columns = {
                row["name"]
                for row in connection.execute("PRAGMA table_info(user_weekly_usage)")
            }
            if "weighted_tokens" not in weekly_columns:
                connection.execute(
                    "ALTER TABLE user_weekly_usage "
                    "ADD COLUMN weighted_tokens INTEGER NOT NULL DEFAULT 0"
                )
            quota_policy_columns = {
                row["name"]
                for row in connection.execute(
                    "PRAGMA table_info(user_quota_policies)"
                )
            }
            if "reset_at" not in quota_policy_columns:
                connection.execute(
                    "ALTER TABLE user_quota_policies ADD COLUMN reset_at INTEGER"
                )
            # Email-only sessions issued before password authentication could
            # otherwise be used to claim another user's initial password. The
            # v8 security migration also revokes long-lived sessions created
            # before Secure cookies and the shorter TTL became mandatory.
            if previous_version < 8:
                connection.execute("DELETE FROM portal_sessions")
            timezone_marker = connection.execute(
                "SELECT value FROM usage_meta WHERE key = ?",
                (WEEK_TIMEZONE_META_KEY,),
            ).fetchone()
            if timezone_marker is None:
                # Databases created by earlier releases always used this
                # timezone. A new database starts with the configured value.
                original_timezone = (
                    "Asia/Shanghai" if previous_version > 0 else self.week_timezone
                )
                connection.execute(
                    "INSERT INTO usage_meta(key, value) VALUES (?, ?)",
                    (WEEK_TIMEZONE_META_KEY, original_timezone),
                )
            connection.execute("PRAGMA user_version = {}".format(SCHEMA_VERSION))
        os.chmod(self.path, 0o600)
        self.ensure_week_timezone()
        self.ensure_weekly_usage_backfilled()

    def sync_identities(self, records, now=None):
        now = int(time.time()) if now is None else int(now)
        rows = []
        for record in records:
            digest = _key_hash(record.get("key"))
            if not digest:
                continue
            rows.append(
                (
                    digest,
                    str(record.get("label", "")),
                    str(record.get("user", "")),
                    str(record.get("account", "")),
                    now,
                    now,
                )
            )
        if not rows:
            return 0
        with self._connection() as connection:
            connection.executemany(
                """
                INSERT OR IGNORE INTO key_identities(
                    key_hash, key_label, user_email, account, first_seen_at, last_seen_at
                ) VALUES (?, ?, ?, ?, ?, ?)
                """,
                rows,
            )
            connection.executemany(
                """
                UPDATE key_identities
                   SET key_label = ?, user_email = ?, account = ?, last_seen_at = ?
                 WHERE key_hash = ?
                """,
                ((row[1], row[2], row[3], row[5], row[0]) for row in rows),
            )
        return len(rows)

    def sync_user_teams(self, classifications):
        """Refresh current identity attribution used by future usage events."""
        if not isinstance(classifications, dict):
            raise ValueError("用户团队快照必须为对象")
        rows = []
        for user_email, classification in classifications.items():
            item = classification if isinstance(classification, dict) else {}
            rows.append(
                (
                    str(item.get("team_id") or ""),
                    _non_negative_int(item.get("team_membership_version")),
                    str(user_email or "").strip().lower(),
                )
            )
        if not rows:
            return 0
        with self._connection() as connection:
            connection.executemany(
                "UPDATE key_identities SET team_id = ?, team_membership_version = ? "
                "WHERE user_email = ?",
                rows,
            )
        return len(rows)

    @staticmethod
    def _business_event(payload):
        endpoint = str(payload.get("endpoint", "")).strip().lower()
        return not endpoint.endswith("/v1/models")

    @staticmethod
    def _event_key(account, request_id, key_digest, payload):
        if request_id:
            return "{}:{}:{}".format(account, key_digest, request_id)
        safe_payload = {
            "account": account,
            "key_hash": key_digest,
            "timestamp": payload.get("timestamp"),
            "endpoint": payload.get("endpoint"),
            "model": payload.get("model"),
            "alias": payload.get("alias"),
            "reasoning_effort": payload.get("reasoning_effort"),
            "failed": payload.get("failed"),
            "tokens": payload.get("tokens"),
        }
        encoded = json.dumps(safe_payload, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
        return "{}:sha256:{}".format(account, hashlib.sha256(encoded.encode("utf-8")).hexdigest())

    def ingest_events(self, account, payloads, now=None, reasoning_multipliers=None):
        now = int(time.time()) if now is None else int(now)
        normalized_multipliers = reasoning_effort_multipliers(reasoning_multipliers)
        weight_policy_version = reasoning_weight_policy_version(normalized_multipliers)
        counters = {"received": 0, "inserted": 0, "duplicate": 0, "unmapped": 0, "ignored": 0}
        prepared = []
        for payload in payloads:
            counters["received"] += 1
            if not isinstance(payload, dict) or not self._business_event(payload):
                counters["ignored"] += 1
                continue
            key_digest = _key_hash(payload.get("api_key"))
            if not key_digest:
                counters["unmapped"] += 1
                continue
            prepared.append((payload, key_digest))

        if not prepared:
            return counters

        with self._connection() as connection:
            timezone_marker = connection.execute(
                "SELECT value FROM usage_meta WHERE key = ?",
                (WEEK_TIMEZONE_META_KEY,),
            ).fetchone()
            if (
                timezone_marker is not None
                and timezone_marker["value"] != self.week_timezone
            ):
                self.week_timezone, unused_zone = _week_timezone(
                    timezone_marker["value"]
                )
            digests = sorted({item[1] for item in prepared})
            placeholders = ",".join("?" for _ in digests)
            identities = {
                row["key_hash"]: row
                for row in connection.execute(
                    "SELECT key_hash, key_label, user_email, account, team_id, "
                    "team_membership_version FROM key_identities "
                    "WHERE key_hash IN ({})".format(placeholders),
                    digests,
                )
            }
            for payload, key_digest in prepared:
                identity = identities.get(key_digest)
                if not identity:
                    counters["unmapped"] += 1
                    continue
                tokens = payload.get("tokens") if isinstance(payload.get("tokens"), dict) else {}
                input_tokens = _non_negative_int(tokens.get("input_tokens"))
                output_tokens = _non_negative_int(tokens.get("output_tokens"))
                total_tokens = _non_negative_int(tokens.get("total_tokens"))
                if not total_tokens and (input_tokens or output_tokens):
                    total_tokens = input_tokens + output_tokens
                request_id = str(payload.get("request_id", "")).strip()
                model = str(payload.get("model", "")).strip()
                alias = str(payload.get("alias", "")).strip() or model
                reasoning_effort = normalize_reasoning_effort(
                    payload.get("reasoning_effort")
                )
                quota_multiplier = normalized_multipliers[reasoning_effort]
                weighted_tokens = int(
                    math.floor(total_tokens * quota_multiplier + 0.5)
                )
                event_key = self._event_key(account, request_id, key_digest, payload)
                cursor = connection.execute(
                    """
                    INSERT OR IGNORE INTO usage_events(
                        event_key, account, user_email, key_label, occurred_at,
                        request_id, provider, model, alias, reasoning_effort,
                        endpoint, failed, latency_ms,
                        input_tokens, output_tokens, reasoning_tokens, cached_tokens,
                        total_tokens, quota_multiplier, weighted_tokens,
                        weight_policy_version, team_id, team_membership_version
                    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (
                        event_key,
                        account,
                        identity["user_email"],
                        identity["key_label"],
                        _timestamp(payload.get("timestamp"), fallback=now),
                        request_id,
                        str(payload.get("provider", "")),
                        model,
                        alias,
                        reasoning_effort,
                        str(payload.get("endpoint", "")),
                        1 if payload.get("failed") else 0,
                        _non_negative_int(payload.get("latency_ms")),
                        input_tokens,
                        output_tokens,
                        _non_negative_int(tokens.get("reasoning_tokens")),
                        _non_negative_int(tokens.get("cached_tokens")),
                        total_tokens,
                        quota_multiplier,
                        weighted_tokens,
                        weight_policy_version,
                        identity["team_id"],
                        int(identity["team_membership_version"] or 0),
                    ),
                )
                if cursor.rowcount:
                    counters["inserted"] += 1
                    occurred_at = _timestamp(payload.get("timestamp"), fallback=now)
                    week_start_at, unused_week_end_at = natural_week_bounds(
                        occurred_at, self.week_timezone
                    )
                    connection.execute(
                        """
                        INSERT INTO user_weekly_usage(
                            user_email, week_start_at, total_tokens, weighted_tokens,
                            request_count, updated_at
                        ) VALUES (?, ?, ?, ?, 1, ?)
                        ON CONFLICT(user_email, week_start_at) DO UPDATE SET
                            total_tokens = total_tokens + excluded.total_tokens,
                            weighted_tokens = weighted_tokens + excluded.weighted_tokens,
                            request_count = request_count + 1,
                            updated_at = excluded.updated_at
                        """,
                        (
                            identity["user_email"],
                            week_start_at,
                            total_tokens,
                            weighted_tokens,
                            now,
                        ),
                    )
                else:
                    counters["duplicate"] += 1
            if counters["inserted"]:
                last_event = connection.execute(
                    "SELECT MAX(id) AS id FROM usage_events"
                ).fetchone()
                connection.executemany(
                    "INSERT OR REPLACE INTO usage_meta(key, value) VALUES (?, ?)",
                    (
                        (WEEKLY_USAGE_BACKFILL_KEY, WEEKLY_USAGE_BACKFILL_VERSION),
                        (WEEKLY_USAGE_LAST_EVENT_ID_KEY, str(int(last_event["id"] or 0))),
                    ),
                )
        return counters

    def ensure_usage_breakdown_started(self, now=None):
        now = int(time.time()) if now is None else int(now)
        with self._connection() as connection:
            connection.execute(
                "INSERT OR IGNORE INTO usage_meta(key, value) VALUES (?, ?)",
                (USAGE_BREAKDOWN_STARTED_AT_KEY, str(now)),
            )
            row = connection.execute(
                "SELECT value FROM usage_meta WHERE key = ?",
                (USAGE_BREAKDOWN_STARTED_AT_KEY,),
            ).fetchone()
        return _non_negative_int(row["value"] if row else 0)

    def usage_breakdown_started_at(self):
        with self._connection() as connection:
            row = connection.execute(
                "SELECT value FROM usage_meta WHERE key = ?",
                (USAGE_BREAKDOWN_STARTED_AT_KEY,),
            ).fetchone()
        return _non_negative_int(row["value"] if row else 0)

    def usage_breakdown_for_user(
        self,
        user_email,
        window_seconds=86400,
        now=None,
        start_at=None,
        account=None,
        end_at=None,
    ):
        now = int(time.time()) if now is None else int(now)
        collection_started_at = self.usage_breakdown_started_at()
        if start_at is not None:
            requested_start_at = int(start_at)
        elif window_seconds is not None:
            requested_start_at = now - int(window_seconds)
        else:
            requested_start_at = 0
        effective_start_at = max(collection_started_at, requested_start_at)
        empty = {
            "collection_started_at": collection_started_at,
            "effective_start_at": effective_start_at,
            "totals": {
                "request_count": 0,
                "success_count": 0,
                "failed_count": 0,
                "known_effort_count": 0,
                "input_tokens": 0,
                "output_tokens": 0,
                "reasoning_tokens": 0,
                "cached_tokens": 0,
                "total_tokens": 0,
                "weighted_tokens": 0,
                "last_used_at": 0,
            },
            "models": [],
            "reasoning_efforts": [],
            "combinations": [],
        }
        if not collection_started_at:
            return empty

        clauses = ["user_email = ?", "occurred_at >= ?", "alias != ''"]
        parameters = [str(user_email), effective_start_at]
        if end_at is not None:
            clauses.append("occurred_at < ?")
            parameters.append(int(end_at))
        if account:
            clauses.append("account = ?")
            parameters.append(str(account))
        query = """
            SELECT account,
                   CASE WHEN model = '' THEN 'unknown' ELSE model END AS model,
                   CASE WHEN reasoning_effort = '' THEN 'unknown'
                        ELSE reasoning_effort END AS reasoning_effort,
                   COUNT(*) AS request_count,
                   SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END) AS success_count,
                   SUM(CASE WHEN failed = 1 THEN 1 ELSE 0 END) AS failed_count,
                   SUM(CASE WHEN failed = 0 THEN input_tokens ELSE 0 END)
                       AS input_tokens,
                   SUM(CASE WHEN failed = 0 THEN output_tokens ELSE 0 END)
                       AS output_tokens,
                   SUM(CASE WHEN failed = 0 THEN reasoning_tokens ELSE 0 END)
                       AS reasoning_tokens,
                   SUM(CASE WHEN failed = 0 THEN cached_tokens ELSE 0 END)
                       AS cached_tokens,
                   SUM(CASE WHEN failed = 0 THEN total_tokens ELSE 0 END)
                       AS total_tokens,
                   SUM(CASE WHEN failed = 0 THEN
                       CASE
                           WHEN weight_policy_version = 'legacy-v1'
                                AND weighted_tokens = 0 AND total_tokens > 0
                           THEN total_tokens
                           ELSE weighted_tokens
                       END
                       ELSE 0 END)
                       AS weighted_tokens,
                   MAX(CASE WHEN failed = 0 THEN occurred_at ELSE 0 END) AS last_used_at
              FROM usage_events
             WHERE {}
             GROUP BY account, model, reasoning_effort
             ORDER BY success_count DESC, model, reasoning_effort
        """.format(" AND ".join(clauses))
        with self._connection() as connection:
            rows = connection.execute(query, parameters).fetchall()

        models = {}
        efforts = {}
        combinations = []
        totals = empty["totals"]
        for row in rows:
            request_count = int(row["request_count"] or 0)
            success_count = int(row["success_count"] or 0)
            failed_count = int(row["failed_count"] or 0)
            input_tokens = int(row["input_tokens"] or 0)
            output_tokens = int(row["output_tokens"] or 0)
            reasoning_tokens = int(row["reasoning_tokens"] or 0)
            cached_tokens = int(row["cached_tokens"] or 0)
            total_tokens = int(row["total_tokens"] or 0)
            weighted_tokens = int(row["weighted_tokens"] or 0)
            last_used_at = int(row["last_used_at"] or 0)
            model = row["model"]
            effort = row["reasoning_effort"]
            totals["request_count"] += request_count
            totals["success_count"] += success_count
            totals["failed_count"] += failed_count
            totals["input_tokens"] += input_tokens
            totals["output_tokens"] += output_tokens
            totals["reasoning_tokens"] += reasoning_tokens
            totals["cached_tokens"] += cached_tokens
            totals["total_tokens"] += total_tokens
            totals["weighted_tokens"] += weighted_tokens
            totals["last_used_at"] = max(totals["last_used_at"], last_used_at)
            if effort != "unknown":
                totals["known_effort_count"] += success_count
            if success_count <= 0:
                continue
            model_item = models.setdefault(
                model,
                {
                    "model": model,
                    "request_count": 0,
                    "success_count": 0,
                    "failed_count": 0,
                    "input_tokens": 0,
                    "output_tokens": 0,
                    "reasoning_tokens": 0,
                    "cached_tokens": 0,
                    "total_tokens": 0,
                    "weighted_tokens": 0,
                    "last_used_at": 0,
                },
            )
            effort_item = efforts.setdefault(
                effort,
                {
                    "reasoning_effort": effort,
                    "request_count": 0,
                    "success_count": 0,
                    "failed_count": 0,
                    "input_tokens": 0,
                    "output_tokens": 0,
                    "reasoning_tokens": 0,
                    "cached_tokens": 0,
                    "total_tokens": 0,
                    "weighted_tokens": 0,
                    "last_used_at": 0,
                },
            )
            for item in (model_item, effort_item):
                item["request_count"] += request_count
                item["success_count"] += success_count
                item["failed_count"] += failed_count
                item["input_tokens"] += input_tokens
                item["output_tokens"] += output_tokens
                item["reasoning_tokens"] += reasoning_tokens
                item["cached_tokens"] += cached_tokens
                item["total_tokens"] += total_tokens
                item["weighted_tokens"] += weighted_tokens
                item["last_used_at"] = max(item["last_used_at"], last_used_at)
            combinations.append(
                {
                    "account": row["account"],
                    "model": model,
                    "reasoning_effort": effort,
                    "request_count": request_count,
                    "success_count": success_count,
                    "failed_count": failed_count,
                    "input_tokens": input_tokens,
                    "output_tokens": output_tokens,
                    "reasoning_tokens": reasoning_tokens,
                    "cached_tokens": cached_tokens,
                    "total_tokens": total_tokens,
                    "weighted_tokens": weighted_tokens,
                    "last_used_at": last_used_at,
                }
            )

        effort_order = {
            name: index
            for index, name in enumerate(
                (
                    "none",
                    "minimal",
                    "low",
                    "medium",
                    "high",
                    "xhigh",
                    "ultra",
                    "max",
                    "auto",
                    "unknown",
                )
            )
        }
        empty["models"] = sorted(
            models.values(),
            key=lambda item: (-item["success_count"], item["model"]),
        )
        empty["reasoning_efforts"] = sorted(
            efforts.values(),
            key=lambda item: (
                effort_order.get(item["reasoning_effort"], len(effort_order)),
                item["reasoning_effort"],
            ),
        )
        empty["combinations"] = sorted(
            combinations,
            key=lambda item: (
                -item["success_count"],
                item["model"],
                item["reasoning_effort"],
                item["account"],
            ),
        )
        return empty

    def usage_breakdown_for_account(
        self,
        account,
        window_seconds=86400,
        now=None,
        start_at=None,
        end_at=None,
    ):
        """Aggregate raw Token usage by model and reasoning effort for one account."""
        now = int(time.time()) if now is None else int(now)
        collection_started_at = self.usage_breakdown_started_at()
        if start_at is not None:
            requested_start_at = int(start_at)
        elif window_seconds is not None:
            requested_start_at = now - int(window_seconds)
        else:
            requested_start_at = 0
        effective_start_at = max(collection_started_at, requested_start_at)
        usage_fields = (
            "request_count",
            "success_count",
            "failed_count",
            "input_tokens",
            "output_tokens",
            "reasoning_tokens",
            "cached_tokens",
            "total_tokens",
        )

        def empty_usage():
            return {
                **{field: 0 for field in usage_fields},
                "last_used_at": 0,
            }

        result = {
            "collection_started_at": collection_started_at,
            "effective_start_at": effective_start_at,
            "totals": empty_usage(),
            "models": [],
            "combinations": [],
        }
        if not collection_started_at:
            return result

        clauses = ["account = ?", "occurred_at >= ?", "alias != ''"]
        parameters = [str(account), effective_start_at]
        if end_at is not None:
            clauses.append("occurred_at < ?")
            parameters.append(int(end_at))

        with self._connection() as connection:
            rows = connection.execute(
                """
                SELECT CASE WHEN model = '' THEN 'unknown' ELSE model END AS model,
                       CASE WHEN reasoning_effort = '' THEN 'unknown'
                            ELSE reasoning_effort END AS reasoning_effort,
                       COUNT(*) AS request_count,
                       SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END) AS success_count,
                       SUM(CASE WHEN failed = 1 THEN 1 ELSE 0 END) AS failed_count,
                       SUM(input_tokens) AS input_tokens,
                       SUM(output_tokens) AS output_tokens,
                       SUM(reasoning_tokens) AS reasoning_tokens,
                       SUM(cached_tokens) AS cached_tokens,
                       SUM(total_tokens) AS total_tokens,
                       MAX(occurred_at) AS last_used_at
                  FROM usage_events
                 WHERE {}
                 GROUP BY model, reasoning_effort
                """.format(" AND ".join(clauses)),
                parameters,
            ).fetchall()

        models = {}
        combinations = []
        for row in rows:
            combination = {
                "model": row["model"],
                "reasoning_effort": row["reasoning_effort"],
                **{field: int(row[field] or 0) for field in usage_fields},
                "last_used_at": int(row["last_used_at"] or 0),
            }
            combinations.append(combination)
            model = models.setdefault(
                combination["model"],
                {"model": combination["model"], **empty_usage()},
            )
            for target in (result["totals"], model):
                for field in usage_fields:
                    target[field] += combination[field]
                target["last_used_at"] = max(
                    target["last_used_at"], combination["last_used_at"]
                )

        result["models"] = sorted(
            models.values(),
            key=lambda item: (-item["total_tokens"], item["model"]),
        )
        result["combinations"] = sorted(
            combinations,
            key=lambda item: (
                -item["total_tokens"],
                item["model"],
                item["reasoning_effort"],
            ),
        )
        return result

    @staticmethod
    def _merge_usage(target, row):
        for field in (
            "request_count",
            "success_count",
            "failed_count",
            "input_tokens",
            "output_tokens",
            "reasoning_tokens",
            "cached_tokens",
            "total_tokens",
            "weighted_tokens",
        ):
            target[field] += int(row[field] or 0)
        target["last_used_at"] = max(target["last_used_at"], int(row["last_used_at"] or 0))

    def usage_for_users(
        self,
        user_emails,
        accounts,
        window_seconds=86400,
        now=None,
        start_at=None,
        end_at=None,
    ):
        now = int(time.time()) if now is None else int(now)
        users = [str(item) for item in user_emails]
        result = {
            user: {
                **_empty_usage(),
                "accounts": {account: _empty_usage() for account in accounts},
            }
            for user in users
        }
        if not users:
            return result

        clauses = ["user_email IN ({})".format(",".join("?" for _ in users))]
        parameters = list(users)
        if start_at is not None:
            clauses.append("occurred_at >= ?")
            parameters.append(int(start_at))
        elif window_seconds is not None:
            clauses.append("occurred_at >= ?")
            parameters.append(now - int(window_seconds))
        if end_at is not None:
            clauses.append("occurred_at < ?")
            parameters.append(int(end_at))
        query = """
            SELECT user_email, account,
                   COUNT(*) AS request_count,
                   SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END) AS success_count,
                   SUM(CASE WHEN failed = 1 THEN 1 ELSE 0 END) AS failed_count,
                   SUM(input_tokens) AS input_tokens,
                   SUM(output_tokens) AS output_tokens,
                   SUM(reasoning_tokens) AS reasoning_tokens,
                   SUM(cached_tokens) AS cached_tokens,
                   SUM(total_tokens) AS total_tokens,
                   SUM(CASE
                           WHEN weight_policy_version = 'legacy-v1'
                                AND weighted_tokens = 0 AND total_tokens > 0
                           THEN total_tokens
                           ELSE weighted_tokens
                       END) AS weighted_tokens,
                   MAX(occurred_at) AS last_used_at
              FROM usage_events
             WHERE {}
             GROUP BY user_email, account
        """.format(" AND ".join(clauses))
        with self._connection() as connection:
            rows = connection.execute(query, parameters).fetchall()
        for row in rows:
            user = row["user_email"]
            account = row["account"]
            if user not in result or account not in result[user]["accounts"]:
                continue
            self._merge_usage(result[user]["accounts"][account], row)
            self._merge_usage(result[user], row)
        return result

    def usage_summaries_for_users(
        self,
        window_seconds=86400,
        now=None,
        start_at=None,
        end_at=None,
    ):
        """Return one aggregate row per user without building user x account data."""
        now = int(time.time()) if now is None else int(now)
        clauses = ["user_email != ''"]
        parameters = []
        range_start_at = None
        if start_at is not None:
            range_start_at = int(start_at)
            clauses.append("occurred_at >= ?")
            parameters.append(range_start_at)
        elif window_seconds is not None:
            range_start_at = now - int(window_seconds)
            clauses.append("occurred_at >= ?")
            parameters.append(range_start_at)
        if end_at is not None:
            clauses.append("occurred_at < ?")
            parameters.append(int(end_at))
        range_end_at = int(end_at) if end_at is not None else now
        bounded_seconds = (
            max(0, range_end_at - range_start_at)
            if range_start_at is not None
            else None
        )
        # SQLite otherwise prefers the user/time index to satisfy GROUP BY,
        # even for a narrow recent range. The time/user index cuts the default
        # Admin ranges down before aggregation; long/all-history ranges keep
        # the planner's cheaper user-ordered scan.
        source = "usage_events"
        if bounded_seconds is not None and bounded_seconds <= 7 * 24 * 60 * 60:
            source += " INDEXED BY usage_events_time_user"
        with self._connection() as connection:
            rows = connection.execute(
                """
                SELECT user_email,
                       COUNT(*) AS request_count,
                       SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END) AS success_count,
                       SUM(CASE WHEN failed = 1 THEN 1 ELSE 0 END) AS failed_count,
                       SUM(input_tokens) AS input_tokens,
                       SUM(output_tokens) AS output_tokens,
                       SUM(reasoning_tokens) AS reasoning_tokens,
                       SUM(cached_tokens) AS cached_tokens,
                       SUM(total_tokens) AS total_tokens,
                       SUM(CASE
                               WHEN weight_policy_version = 'legacy-v1'
                                    AND weighted_tokens = 0 AND total_tokens > 0
                               THEN total_tokens
                               ELSE weighted_tokens
                           END) AS weighted_tokens,
                       MAX(occurred_at) AS last_used_at
                  FROM {}
                 WHERE {}
                 GROUP BY user_email
                """.format(source, " AND ".join(clauses)),
                parameters,
            ).fetchall()
        result = {}
        for row in rows:
            usage = _empty_usage()
            self._merge_usage(usage, row)
            result[str(row["user_email"])] = usage
        return result

    def usage_for_teams(
        self,
        team_ids,
        current_team_by_user,
        window_seconds=86400,
        now=None,
        start_at=None,
        end_at=None,
        include_unassigned=True,
        usage_by_user=None,
    ):
        """Aggregate usage by each user's current control-plane team."""
        now = int(time.time()) if now is None else int(now)
        normalized = [str(item or "").strip() for item in team_ids]
        storage_ids = list(dict.fromkeys(normalized))
        if include_unassigned and "" not in storage_ids:
            storage_ids.append("")
        result = {
            (team_id or "unassigned"): {**_empty_usage(), "active_users": 0}
            for team_id in storage_ids
        }
        if not storage_ids:
            return result
        membership = {
            str(user or "").strip().lower(): str(team_id or "").strip()
            for user, team_id in dict(current_team_by_user or {}).items()
            if str(user or "").strip()
        }
        if usage_by_user is None:
            usage_by_user = self.usage_summaries_for_users(
                window_seconds=window_seconds,
                now=now,
                start_at=start_at,
                end_at=end_at,
            )
        for user, usage in usage_by_user.items():
            if user not in membership:
                continue
            key = membership[user] or "unassigned"
            if key not in result:
                continue
            result[key]["active_users"] += 1
            self._merge_usage(result[key], usage)
        return result

    def team_usage_breakdown(
        self,
        team_id,
        current_user_emails,
        window_seconds=86400,
        now=None,
        start_at=None,
        end_at=None,
    ):
        """Return dimensions for the users currently assigned to one team."""
        now = int(time.time()) if now is None else int(now)
        storage_team_id = "" if str(team_id or "") == "unassigned" else str(team_id or "")
        users = sorted(
            {
                str(item or "").strip().lower()
                for item in current_user_emails
                if str(item or "").strip()
            }
        )
        clauses = [
            "user_email IN (SELECT user_email FROM selected_team_users)"
        ]
        parameters = []
        if start_at is not None:
            clauses.append("occurred_at >= ?")
            parameters.append(int(start_at))
        elif window_seconds is not None:
            clauses.append("occurred_at >= ?")
            parameters.append(now - int(window_seconds))
        if end_at is not None:
            clauses.append("occurred_at < ?")
            parameters.append(int(end_at))
        where = " AND ".join(clauses)
        metric_sql = """
            COUNT(*) AS request_count,
            SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END) AS success_count,
            SUM(CASE WHEN failed = 1 THEN 1 ELSE 0 END) AS failed_count,
            SUM(input_tokens) AS input_tokens,
            SUM(output_tokens) AS output_tokens,
            SUM(reasoning_tokens) AS reasoning_tokens,
            SUM(cached_tokens) AS cached_tokens,
            SUM(total_tokens) AS total_tokens,
            SUM(CASE
                    WHEN weight_policy_version = 'legacy-v1'
                         AND weighted_tokens = 0 AND total_tokens > 0
                    THEN total_tokens
                    ELSE weighted_tokens
                END) AS weighted_tokens,
            MAX(occurred_at) AS last_used_at
        """

        def usage_row(row, name_key, name):
            usage = _empty_usage()
            self._merge_usage(usage, row)
            return {name_key: str(name), **usage}

        with self._connection() as connection:
            connection.execute(
                "CREATE TEMP TABLE selected_team_users "
                "(user_email TEXT PRIMARY KEY) WITHOUT ROWID"
            )
            connection.executemany(
                "INSERT INTO selected_team_users(user_email) VALUES (?)",
                ((user,) for user in users),
            )
            event_range = connection.execute(
                "SELECT MIN(occurred_at) AS first_at "
                "FROM usage_events INDEXED BY usage_events_user_time WHERE {}".format(
                    where
                ),
                parameters,
            ).fetchone()
            total_row = connection.execute(
                "SELECT {} FROM usage_events INDEXED BY usage_events_user_time "
                "WHERE {}".format(metric_sql, where),
                parameters,
            ).fetchone()
            user_rows = connection.execute(
                "SELECT user_email, {} FROM usage_events INDEXED BY usage_events_user_time "
                "WHERE {} GROUP BY user_email".format(metric_sql, where),
                parameters,
            ).fetchall()
            account_rows = connection.execute(
                "SELECT account, {} FROM usage_events INDEXED BY usage_events_user_time "
                "WHERE {} GROUP BY account".format(metric_sql, where),
                parameters,
            ).fetchall()
            model_rows = connection.execute(
                "SELECT CASE WHEN model = '' THEN 'unknown' ELSE model END AS model, {} "
                "FROM usage_events INDEXED BY usage_events_user_time "
                "WHERE {} GROUP BY CASE WHEN model = '' THEN 'unknown' ELSE model END".format(
                    metric_sql,
                    where,
                ),
                parameters,
            ).fetchall()
            combination_rows = connection.execute(
                """
                SELECT CASE WHEN model = '' THEN 'unknown' ELSE model END AS model,
                       CASE WHEN reasoning_effort = '' THEN 'unknown'
                            ELSE reasoning_effort END AS reasoning_effort,
                       {}
                  FROM usage_events INDEXED BY usage_events_user_time
                 WHERE {}
                 GROUP BY CASE WHEN model = '' THEN 'unknown' ELSE model END,
                          CASE WHEN reasoning_effort = '' THEN 'unknown'
                               ELSE reasoning_effort END
                """.format(metric_sql, where),
                parameters,
            ).fetchall()
            first_event_at = int(event_range["first_at"] or now)
            series_start_at = (
                int(start_at)
                if start_at is not None
                else (
                    now - int(window_seconds)
                    if window_seconds is not None
                    else first_event_at
                )
            )
            series_end_at = int(end_at) if end_at is not None else now
            duration = max(1, series_end_at - series_start_at)
            if duration <= 6 * 60 * 60:
                bucket_seconds = 5 * 60
            elif duration <= 24 * 60 * 60:
                bucket_seconds = 15 * 60
            elif duration <= 7 * 24 * 60 * 60:
                bucket_seconds = 60 * 60
            elif duration <= 31 * 24 * 60 * 60:
                bucket_seconds = 6 * 60 * 60
            else:
                bucket_seconds = max(60 * 60, int(math.ceil(duration / 120.0)))
            first_bucket = (series_start_at // bucket_seconds) * bucket_seconds
            last_bucket = ((max(series_start_at, series_end_at - 1)) // bucket_seconds) * bucket_seconds
            buckets = list(range(first_bucket, last_bucket + bucket_seconds, bucket_seconds))
            series_rows = connection.execute(
                """
                SELECT CAST(occurred_at / ? AS INTEGER) * ? AS bucket_at,
                       SUM(CASE
                               WHEN weight_policy_version = 'legacy-v1'
                                    AND weighted_tokens = 0 AND total_tokens > 0
                               THEN total_tokens
                               ELSE weighted_tokens
                           END) AS weighted_tokens
                  FROM usage_events INDEXED BY usage_events_user_time
                 WHERE {}
                 GROUP BY bucket_at
                 ORDER BY bucket_at
                """.format(where),
                (bucket_seconds, bucket_seconds, *parameters),
            ).fetchall()
        totals = _empty_usage()
        if total_row is not None:
            self._merge_usage(totals, total_row)
        series_values = {
            int(row["bucket_at"]): int(row["weighted_tokens"] or 0)
            for row in series_rows
        }
        return {
            "team_id": storage_team_id or "unassigned",
            "attribution": "current_membership",
            "totals": totals,
            "users": sorted(
                (usage_row(row, "user", row["user_email"]) for row in user_rows),
                key=lambda item: (-item["weighted_tokens"], item["user"]),
            ),
            "accounts": sorted(
                (usage_row(row, "account", row["account"]) for row in account_rows),
                key=lambda item: (-item["weighted_tokens"], item["account"]),
            ),
            "models": sorted(
                (usage_row(row, "model", row["model"]) for row in model_rows),
                key=lambda item: (-item["weighted_tokens"], item["model"]),
            ),
            "combinations": sorted(
                (
                    {
                        **usage_row(row, "model", row["model"]),
                        "reasoning_effort": str(row["reasoning_effort"]),
                    }
                    for row in combination_rows
                ),
                key=lambda item: (
                    -item["weighted_tokens"],
                    item["model"],
                    item["reasoning_effort"],
                ),
            ),
            "series": {
                "start_at": series_start_at,
                "end_at": series_end_at,
                "bucket_seconds": bucket_seconds,
                "buckets": buckets,
                "values": [series_values.get(bucket, 0) for bucket in buckets],
            },
        }

    def usage_for_accounts(
        self,
        accounts,
        window_seconds=86400,
        now=None,
        start_at=None,
        start_at_by_account=None,
        end_at=None,
    ):
        now = int(time.time()) if now is None else int(now)
        account_ids = [str(item) for item in accounts]
        result = {
            account: {**_empty_usage(), "active_users": 0}
            for account in account_ids
        }
        if not account_ids:
            return result

        clauses = ["account IN ({})".format(",".join("?" for _ in account_ids))]
        parameters = list(account_ids)
        if start_at is not None and start_at_by_account is not None:
            raise ValueError("不能同时指定统一和按账号统计起点")
        if start_at_by_account is not None:
            account_windows = []
            for account in account_ids:
                account_start = start_at_by_account.get(account)
                if account_start is None:
                    continue
                account_windows.append((account, int(account_start)))
            if account_windows:
                clauses.append(
                    "({})".format(
                        " OR ".join(
                            "(account = ? AND occurred_at >= ?)"
                            for _ in account_windows
                        )
                    )
                )
                for account, account_start in account_windows:
                    parameters.extend((account, account_start))
            else:
                clauses.append("1 = 0")
        elif start_at is not None:
            clauses.append("occurred_at >= ?")
            parameters.append(int(start_at))
        elif window_seconds is not None:
            clauses.append("occurred_at >= ?")
            parameters.append(now - int(window_seconds))
        if end_at is not None:
            clauses.append("occurred_at < ?")
            parameters.append(int(end_at))
        query = """
            SELECT account,
                   COUNT(DISTINCT user_email) AS active_users,
                   COUNT(*) AS request_count,
                   SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END) AS success_count,
                   SUM(CASE WHEN failed = 1 THEN 1 ELSE 0 END) AS failed_count,
                   SUM(input_tokens) AS input_tokens,
                   SUM(output_tokens) AS output_tokens,
                   SUM(reasoning_tokens) AS reasoning_tokens,
                   SUM(cached_tokens) AS cached_tokens,
                   SUM(total_tokens) AS total_tokens,
                   SUM(CASE
                           WHEN weight_policy_version = 'legacy-v1'
                                AND weighted_tokens = 0 AND total_tokens > 0
                           THEN total_tokens
                           ELSE weighted_tokens
                       END) AS weighted_tokens,
                   MAX(occurred_at) AS last_used_at
              FROM usage_events
             WHERE {}
             GROUP BY account
        """.format(" AND ".join(clauses))
        with self._connection() as connection:
            rows = connection.execute(query, parameters).fetchall()
        for row in rows:
            account = row["account"]
            if account not in result:
                continue
            result[account]["active_users"] = int(row["active_users"] or 0)
            self._merge_usage(result[account], row)
        return result

    def account_activity(
        self,
        accounts,
        window_seconds=3600,
        now=None,
        include_user_emails=False,
    ):
        now = int(time.time()) if now is None else int(now)
        account_ids = [str(account) for account in accounts]
        result = {
            account: {
                "active_users": 0,
                "active_user_emails": [],
                "request_count": 0,
                "total_tokens": 0,
            }
            for account in account_ids
        }
        if not result:
            return result
        rows = []
        placeholders = ",".join("?" for _ in account_ids)
        if include_user_emails:
            select_fields = "user_email, COUNT(*) AS request_count"
            group_fields = "account, user_email"
            order_clause = "ORDER BY account, user_email"
        else:
            select_fields = (
                "COUNT(DISTINCT user_email) AS active_users, "
                "COUNT(*) AS request_count"
            )
            group_fields = "account"
            order_clause = ""
        with self._connection() as connection:
            rows = connection.execute(
                """
                SELECT account,
                       {},
                       SUM(total_tokens) AS total_tokens
                  FROM usage_events
                 WHERE account IN ({})
                   AND occurred_at >= ?
                   AND user_email != ''
                 GROUP BY {}
                 {}
                """.format(select_fields, placeholders, group_fields, order_clause),
                (*account_ids, now - int(window_seconds)),
            ).fetchall()
        for row in rows:
            if row["account"] not in result:
                continue
            account_activity = result[row["account"]]
            if include_user_emails:
                account_activity["active_user_emails"].append(row["user_email"])
                account_activity["active_users"] += 1
                account_activity["request_count"] += int(row["request_count"] or 0)
                account_activity["total_tokens"] += int(row["total_tokens"] or 0)
            else:
                account_activity["active_users"] = int(row["active_users"] or 0)
                account_activity["request_count"] = int(row["request_count"] or 0)
                account_activity["total_tokens"] = int(row["total_tokens"] or 0)
        return result

    @staticmethod
    def _token_series(names, buckets, rows, name_field, average_start_bucket_by_name=None):
        values_by_name = {str(name): {} for name in names}
        for row in rows:
            name = str(row[name_field])
            if name not in values_by_name:
                continue
            values_by_name[name][int(row["bucket_at"])] = int(
                row["total_tokens"] or 0
            )
        result = []
        for name in names:
            name = str(name)
            values = [values_by_name[name].get(bucket, 0) for bucket in buckets]
            total = sum(values)
            average_values = values
            if average_start_bucket_by_name is not None:
                average_start_bucket = average_start_bucket_by_name.get(name)
                if average_start_bucket is not None:
                    average_start_index = next(
                        (
                            index
                            for index, bucket in enumerate(buckets)
                            if bucket >= average_start_bucket
                        ),
                        len(values),
                    )
                    average_values = values[average_start_index:]
            result.append(
                {
                    "name": name,
                    "values": values,
                    "current": values[-1] if values else 0,
                    "average": round(sum(average_values) / len(average_values))
                    if average_values
                    else 0,
                    "maximum": max(values, default=0),
                    "total": total,
                }
            )
        return result

    @staticmethod
    def _token_account_scope(account_ids, now, window_seconds, start_at_by_account=None):
        if start_at_by_account is None:
            placeholders = ",".join("?" for _ in account_ids)
            start_at = now - int(window_seconds)
            return (
                "account IN ({}) AND occurred_at >= ? AND occurred_at <= ?".format(
                    placeholders
                ),
                [*account_ids, start_at, now],
                start_at,
            )

        account_windows = [
            (account, int(start_at_by_account[account]))
            for account in account_ids
            if account in start_at_by_account
        ]
        if not account_windows:
            return "1 = 0", [], now - int(window_seconds)
        return (
            "({}) AND occurred_at <= ?".format(
                " OR ".join(
                    "(account = ? AND occurred_at >= ?)"
                    for _ in account_windows
                )
            ),
            [value for window in account_windows for value in window] + [now],
            min(start_at for _, start_at in account_windows),
        )

    def token_time_series(
        self,
        accounts,
        user_emails,
        window_seconds,
        bucket_seconds,
        now=None,
        account=None,
        user_email=None,
        user_limit=10,
        start_at_by_account=None,
    ):
        """Aggregate recorded Token usage into aligned account and user buckets."""
        now = int(time.time()) if now is None else int(now)
        window_seconds = int(window_seconds)
        bucket_seconds = int(bucket_seconds)
        if window_seconds <= 0 or bucket_seconds <= 0:
            raise ValueError("时间范围和聚合间隔必须为正整数")
        account_ids = [str(item) for item in accounts]
        selected_account_values = _filter_values(account)
        if selected_account_values:
            if any(item not in account_ids for item in selected_account_values):
                raise ValueError("CPA 账号不存在")
            selected_accounts_set = set(selected_account_values)
            account_ids = [item for item in account_ids if item in selected_accounts_set]

        if start_at_by_account is not None:
            start_at_by_account = {
                str(account): int(start_at)
                for account, start_at in start_at_by_account.items()
                if start_at is not None
            }
            account_ids = [
                account for account in account_ids if account in start_at_by_account
            ]

        users = sorted({str(item).strip().lower() for item in user_emails if str(item).strip()})
        selected_user_values = _filter_values(user_email, lower=True)
        if any(item not in users for item in selected_user_values):
            raise ValueError("用户不存在")
        safe_user_limit = max(1, min(int(user_limit or 10), 50))

        if start_at_by_account is None:
            start_at = now - window_seconds
        else:
            start_at = min(
                (
                    start_at_by_account[account]
                    for account in account_ids
                    if account in start_at_by_account
                ),
                default=now - window_seconds,
            )
        first_bucket = (start_at // bucket_seconds) * bucket_seconds
        last_bucket = (now // bucket_seconds) * bucket_seconds
        buckets = list(range(first_bucket, last_bucket + bucket_seconds, bucket_seconds))
        if len(buckets) > 400:
            raise ValueError("时间序列不能超过 400 个时间桶")
        if not account_ids:
            return {
                "generated_at": now,
                "window_start_at": start_at,
                "window_seconds": window_seconds,
                "bucket_seconds": bucket_seconds,
                "buckets": buckets,
                "accounts": [],
                "users": [],
            }

        account_scope, account_scope_parameters, _ = self._token_account_scope(
            account_ids,
            now,
            window_seconds,
            start_at_by_account=start_at_by_account,
        )
        account_average_start_bucket_by_name = (
            {
                account: (start_at_by_account[account] // bucket_seconds) * bucket_seconds
                for account in account_ids
            }
            if start_at_by_account is not None
            else None
        )
        account_user_filter = ""
        account_user_parameters = []
        if selected_user_values:
            user_placeholders = ",".join("?" for _ in selected_user_values)
            account_user_filter = f" AND user_email IN ({user_placeholders})"
            account_user_parameters = selected_user_values
        if selected_user_values:
            selected_user_cte = "VALUES {}".format(
                ",".join("(?, ?)" for _ in selected_user_values)
            )
            selected_user_parameters = [
                value
                for position, user in enumerate(selected_user_values)
                for value in (user, position)
            ]
        elif users:
            user_placeholders = ",".join("?" for _ in users)
            selected_user_cte = """
                SELECT user_email,
                       ROW_NUMBER() OVER (
                           ORDER BY total_tokens DESC, user_email
                       ) - 1 AS position
                  FROM (
                      SELECT user_email, SUM(total_tokens) AS total_tokens
                        FROM scoped
                       WHERE user_email IN ({})
                       GROUP BY user_email
                       ORDER BY total_tokens DESC, user_email
                       LIMIT ?
                  )
            """.format(user_placeholders)
            selected_user_parameters = [*users, safe_user_limit]
        else:
            selected_user_cte = "SELECT NULL, NULL WHERE 1 = 0"
            selected_user_parameters = []

        with self._connection() as connection:
            rows = connection.execute(
                """
                WITH scoped AS MATERIALIZED (
                    SELECT account,
                           user_email,
                           CAST(occurred_at / ? AS INTEGER) * ? AS bucket_at,
                           SUM(total_tokens) AS total_tokens
                      FROM usage_events
                     WHERE {}
                     GROUP BY account, user_email, bucket_at
                ),
                selected_users(user_email, position) AS MATERIALIZED (
                    {}
                )
                SELECT 'account' AS row_kind,
                       account AS series_name,
                       bucket_at,
                       SUM(total_tokens) AS total_tokens,
                       NULL AS position
                  FROM scoped
                 WHERE 1 = 1 {}
                 GROUP BY account, bucket_at
                UNION ALL
                SELECT 'selected_user' AS row_kind,
                       user_email AS series_name,
                       NULL AS bucket_at,
                       NULL AS total_tokens,
                       position
                  FROM selected_users
                UNION ALL
                SELECT 'user' AS row_kind,
                       scoped.user_email AS series_name,
                       scoped.bucket_at,
                       SUM(scoped.total_tokens) AS total_tokens,
                       NULL AS position
                  FROM scoped
                  JOIN selected_users
                    ON selected_users.user_email = scoped.user_email
                 GROUP BY scoped.user_email, scoped.bucket_at
                """.format(
                    account_scope,
                    selected_user_cte,
                    account_user_filter,
                ),
                (
                    bucket_seconds,
                    bucket_seconds,
                    *account_scope_parameters,
                    *selected_user_parameters,
                    *account_user_parameters,
                ),
            ).fetchall()

        selected_users = [
            row["series_name"]
            for row in sorted(
                (row for row in rows if row["row_kind"] == "selected_user"),
                key=lambda row: int(row["position"]),
            )
        ]
        account_rows = [
            {
                "account": row["series_name"],
                "bucket_at": row["bucket_at"],
                "total_tokens": row["total_tokens"],
            }
            for row in rows
            if row["row_kind"] == "account"
        ]
        user_rows = [
            {
                "user_email": row["series_name"],
                "bucket_at": row["bucket_at"],
                "total_tokens": row["total_tokens"],
            }
            for row in rows
            if row["row_kind"] == "user"
        ]

        return {
            "generated_at": now,
            "window_start_at": start_at,
            "window_seconds": window_seconds,
            "bucket_seconds": bucket_seconds,
            "buckets": buckets,
            "accounts": self._token_series(
                account_ids,
                buckets,
                account_rows,
                "account",
                average_start_bucket_by_name=account_average_start_bucket_by_name,
            ),
            "users": self._token_series(
                selected_users,
                buckets,
                user_rows,
                "user_email",
            ),
        }

    def create_session(self, user_email, ttl_seconds=30 * 24 * 60 * 60, now=None):
        now = int(time.time()) if now is None else int(now)
        token = secrets.token_urlsafe(32)
        digest = _key_hash(token)
        expires_at = now + int(ttl_seconds)
        with self._connection() as connection:
            connection.execute("DELETE FROM portal_sessions WHERE expires_at <= ?", (now,))
            connection.execute(
                "INSERT INTO portal_sessions(session_hash, user_email, created_at, expires_at) "
                "VALUES (?, ?, ?, ?)",
                (digest, str(user_email), now, expires_at),
            )
        return {"token": token, "expires_at": expires_at}

    def resolve_session(self, token, now=None):
        now = int(time.time()) if now is None else int(now)
        digest = _key_hash(token)
        if not digest:
            return None
        with self._connection() as connection:
            connection.execute("DELETE FROM portal_sessions WHERE expires_at <= ?", (now,))
            row = connection.execute(
                "SELECT user_email, expires_at FROM portal_sessions "
                "WHERE session_hash = ? AND expires_at > ?",
                (digest, now),
            ).fetchone()
        if not row:
            return None
        return {"user": row["user_email"], "expires_at": int(row["expires_at"])}

    def revoke_session(self, token):
        digest = _key_hash(token)
        if not digest:
            return False
        with self._connection() as connection:
            cursor = connection.execute(
                "DELETE FROM portal_sessions WHERE session_hash = ?",
                (digest,),
            )
        return cursor.rowcount > 0

    def portal_credential(self, user_email):
        with self._connection() as connection:
            row = connection.execute(
                "SELECT password_hash, must_change, created_at, updated_at "
                "FROM portal_credentials WHERE user_email = ?",
                (str(user_email),),
            ).fetchone()
        if not row:
            return None
        return {
            "password_hash": row["password_hash"],
            "must_change": bool(row["must_change"]),
            "created_at": int(row["created_at"]),
            "updated_at": int(row["updated_at"]),
        }

    def portal_credentials_requiring_change(self):
        with self._connection() as connection:
            rows = connection.execute(
                "SELECT user_email, password_hash, created_at, updated_at "
                "FROM portal_credentials WHERE must_change = 1 "
                "ORDER BY user_email"
            ).fetchall()
        return [
            {
                "user": row["user_email"],
                "password_hash": row["password_hash"],
                "created_at": int(row["created_at"]),
                "updated_at": int(row["updated_at"]),
            }
            for row in rows
        ]

    def ensure_portal_credential(self, user_email, password_hash, now=None):
        now = int(time.time()) if now is None else int(now)
        with self._connection() as connection:
            connection.execute(
                "INSERT OR IGNORE INTO portal_credentials("
                "user_email, password_hash, must_change, created_at, updated_at"
                ") VALUES (?, ?, 1, ?, ?)",
                (str(user_email), str(password_hash), now, now),
            )
        return self.portal_credential(user_email)

    def set_portal_credential(
        self,
        user_email,
        password_hash,
        must_change,
        now=None,
        keep_session_token="",
    ):
        now = int(time.time()) if now is None else int(now)
        user_email = str(user_email)
        with self._connection() as connection:
            connection.execute(
                """
                INSERT INTO portal_credentials(
                    user_email, password_hash, must_change, created_at, updated_at
                ) VALUES (?, ?, ?, ?, ?)
                ON CONFLICT(user_email) DO UPDATE SET
                    password_hash = excluded.password_hash,
                    must_change = excluded.must_change,
                    updated_at = excluded.updated_at
                """,
                (user_email, str(password_hash), int(bool(must_change)), now, now),
            )
            keep_digest = _key_hash(keep_session_token)
            if keep_digest:
                connection.execute(
                    "DELETE FROM portal_sessions "
                    "WHERE user_email = ? AND session_hash != ?",
                    (user_email, keep_digest),
                )
            else:
                connection.execute(
                    "DELETE FROM portal_sessions WHERE user_email = ?",
                    (user_email,),
                )
        return self.portal_credential(user_email)

    def delete_portal_identity(self, user_email):
        user_email = str(user_email)
        with self._connection() as connection:
            sessions = connection.execute(
                "DELETE FROM portal_sessions WHERE user_email = ?",
                (user_email,),
            ).rowcount
            credentials = connection.execute(
                "DELETE FROM portal_credentials WHERE user_email = ?",
                (user_email,),
            ).rowcount
        return {"sessions": sessions, "credentials": credentials}

    def set_quota_policy(
        self,
        user_email,
        mode,
        weekly_tokens=None,
        now=None,
        created_by="admin",
        reset_on_new_week=None,
    ):
        """Persist a user quota policy; inherit is represented by no row."""
        now = int(time.time()) if now is None else int(now)
        user = str(user_email or "").strip().lower()
        if not user:
            raise ValueError("用户邮箱不能为空")
        mode = str(mode or "").strip().lower()
        if mode not in ("inherit", "unlimited", "custom"):
            raise ValueError("额度策略必须为 inherit、unlimited 或 custom")
        if mode == "inherit":
            self.clear_quota_policy(user)
            return {"mode": "inherit", "weekly_tokens": None}
        if mode == "unlimited":
            normalized_tokens = None
        else:
            if isinstance(weekly_tokens, bool) or not isinstance(
                weekly_tokens, (int, float, str)
            ):
                raise ValueError("自定义周额度必须为正整数")
            if isinstance(weekly_tokens, float) and not weekly_tokens.is_integer():
                raise ValueError("自定义周额度必须为正整数")
            if isinstance(weekly_tokens, str):
                raw_tokens = weekly_tokens.strip()
                if not raw_tokens.isascii() or not raw_tokens.isdigit():
                    raise ValueError("自定义周额度必须为正整数")
            try:
                normalized_tokens = int(weekly_tokens)
            except (TypeError, ValueError, OverflowError):
                raise ValueError("自定义周额度必须为正整数")
            if normalized_tokens <= 0:
                raise ValueError("自定义周额度必须为正整数")
            if normalized_tokens > MAX_WEEKLY_QUOTA_TOKENS:
                raise ValueError(
                    "自定义周额度不能超过 {:,} Token".format(
                        MAX_WEEKLY_QUOTA_TOKENS
                    )
                )
        reset_enabled = (
            self.reset_personal_weekly_on_new_week
            if reset_on_new_week is None
            else bool(reset_on_new_week)
        )
        reset_at = (
            natural_week_bounds(now, self.week_timezone)[1]
            if reset_enabled
            else None
        )
        with self._connection() as connection:
            connection.execute(
                """
                INSERT INTO user_quota_policies(
                    user_email, weekly_tokens, created_at, updated_at, created_by,
                    reset_at
                ) VALUES (?, ?, ?, ?, ?, ?)
                ON CONFLICT(user_email) DO UPDATE SET
                    weekly_tokens = excluded.weekly_tokens,
                    updated_at = excluded.updated_at,
                    created_by = excluded.created_by,
                    reset_at = excluded.reset_at
                """,
                (
                    user,
                    normalized_tokens,
                    now,
                    now,
                    str(created_by),
                    reset_at,
                ),
            )
        return {
            "mode": mode,
            "weekly_tokens": normalized_tokens,
            "updated_at": now,
            "updated_by": str(created_by),
            "reset_at": reset_at,
        }

    def clear_quota_policy(self, user_email):
        with self._connection() as connection:
            cursor = connection.execute(
                "DELETE FROM user_quota_policies WHERE user_email = ?",
                (str(user_email or "").strip().lower(),),
            )
        return cursor.rowcount > 0

    def clear_quota_policies(self, user_emails):
        users = sorted(
            {
                str(item or "").strip().lower()
                for item in user_emails
                if str(item or "").strip()
            }
        )
        if not users:
            return 0
        placeholders = ",".join("?" for _ in users)
        with self._connection() as connection:
            cursor = connection.execute(
                "DELETE FROM user_quota_policies WHERE user_email IN ({})".format(
                    placeholders
                ),
                users,
            )
        return cursor.rowcount

    def configure_personal_quota_weekly_reset(
        self,
        enabled,
        now=None,
        reschedule=False,
    ):
        """Apply the global personal-policy lifetime without resetting mid-week."""
        now = int(time.time()) if now is None else int(now)
        self.reset_personal_weekly_on_new_week = bool(enabled)
        week_start, week_end = natural_week_bounds(now, self.week_timezone)
        with self._connection() as connection:
            expired = connection.execute(
                "DELETE FROM user_quota_policies "
                "WHERE reset_at IS NOT NULL AND reset_at <= ?",
                (now,),
            ).rowcount
            scheduled = 0
            cancelled = 0
            if self.reset_personal_weekly_on_new_week:
                if reschedule:
                    scheduled = connection.execute(
                        "UPDATE user_quota_policies SET reset_at = ?",
                        (week_end,),
                    ).rowcount
                else:
                    scheduled = connection.execute(
                        "UPDATE user_quota_policies SET reset_at = ? "
                        "WHERE reset_at IS NULL",
                        (week_end,),
                    ).rowcount
            else:
                cancelled = connection.execute(
                    "UPDATE user_quota_policies SET reset_at = NULL "
                    "WHERE reset_at IS NOT NULL"
                ).rowcount
        return {
            "enabled": self.reset_personal_weekly_on_new_week,
            "week_start_at": week_start,
            "week_end_at": week_end,
            "expired_policies": expired,
            "scheduled_policies": scheduled,
            "cancelled_schedules": cancelled,
        }

    @staticmethod
    def _quota_adjustment_reason(value):
        reason = " ".join(str(value or "").strip().split())
        if not reason:
            raise ValueError("请填写额度操作原因")
        if len(reason) > 200:
            raise ValueError("额度操作原因不能超过 200 个字符")
        return reason

    @staticmethod
    def normalize_quota_adjustment_tokens(value):
        if isinstance(value, bool) or not isinstance(value, (int, float, str)):
            raise ValueError("追加额度必须为正整数")
        if isinstance(value, float) and not value.is_integer():
            raise ValueError("追加额度必须为正整数")
        if isinstance(value, str):
            raw = value.strip()
            if not raw.isascii() or not raw.isdigit():
                raise ValueError("追加额度必须为正整数")
        try:
            tokens = int(value)
        except (TypeError, ValueError, OverflowError):
            raise ValueError("追加额度必须为正整数")
        if tokens <= 0:
            raise ValueError("追加额度必须为正整数")
        if tokens > MAX_WEEKLY_QUOTA_TOKENS:
            raise ValueError(
                "追加额度不能超过 {:,} Token".format(MAX_WEEKLY_QUOTA_TOKENS)
            )
        return tokens

    def add_quota_bonus(
        self,
        user_emails,
        token_amount,
        reason,
        now=None,
        created_by="admin",
    ):
        now = int(time.time()) if now is None else int(now)
        week_start, unused_week_end = natural_week_bounds(now, self.week_timezone)
        users = sorted(
            {
                str(item or "").strip().lower()
                for item in user_emails
                if str(item or "").strip()
            }
        )
        if not users:
            raise ValueError("请选择用户")
        tokens = self.normalize_quota_adjustment_tokens(token_amount)
        normalized_reason = self._quota_adjustment_reason(reason)
        with self._connection() as connection:
            connection.executemany(
                """
                INSERT INTO user_quota_adjustments(
                    user_email, week_start_at, action, token_amount,
                    reason, created_at, created_by
                ) VALUES (?, ?, 'bonus', ?, ?, ?, ?)
                """,
                (
                    (
                        user,
                        week_start,
                        tokens,
                        normalized_reason,
                        now,
                        str(created_by),
                    )
                    for user in users
                ),
            )
        return {
            "action": "bonus",
            "applied_users": users,
            "skipped_users": [],
            "token_amount": tokens,
            "week_start_at": week_start,
            "reason": normalized_reason,
            "created_at": now,
        }

    def reset_weekly_usage(
        self,
        user_emails,
        reason,
        now=None,
        created_by="admin",
    ):
        now = int(time.time()) if now is None else int(now)
        week_start, unused_week_end = natural_week_bounds(now, self.week_timezone)
        users = sorted(
            {
                str(item or "").strip().lower()
                for item in user_emails
                if str(item or "").strip()
            }
        )
        if not users:
            raise ValueError("请选择用户")
        normalized_reason = self._quota_adjustment_reason(reason)
        placeholders = ",".join("?" for _ in users)
        with self._connection() as connection:
            # Freeze the usage snapshot while the reset offset is calculated.
            # Otherwise a collector write between the SELECT and INSERT could
            # make part of the pre-reset usage unexpectedly count again.
            connection.execute("BEGIN IMMEDIATE")
            weighted_usage = {
                row["user_email"]: int(row["weighted_tokens"] or 0)
                for row in connection.execute(
                    "SELECT user_email, weighted_tokens FROM user_weekly_usage "
                    "WHERE week_start_at = ? AND user_email IN ({})".format(
                        placeholders
                    ),
                    (week_start, *users),
                )
            }
            reset_offsets = {
                row["user_email"]: int(row["token_amount"] or 0)
                for row in connection.execute(
                    """
                    SELECT user_email, SUM(token_amount) AS token_amount
                    FROM user_quota_adjustments
                    WHERE week_start_at = ? AND action = 'usage_reset'
                      AND user_email IN ({})
                    GROUP BY user_email
                    """.format(placeholders),
                    (week_start, *users),
                )
            }
            applied = []
            skipped = []
            rows = []
            for user in users:
                effective_usage = max(
                    0,
                    int(weighted_usage.get(user, 0))
                    - int(reset_offsets.get(user, 0)),
                )
                if effective_usage <= 0:
                    skipped.append(user)
                    continue
                rows.append(
                    (
                        user,
                        week_start,
                        effective_usage,
                        normalized_reason,
                        now,
                        str(created_by),
                    )
                )
                applied.append({"user": user, "token_amount": effective_usage})
            connection.executemany(
                """
                INSERT INTO user_quota_adjustments(
                    user_email, week_start_at, action, token_amount,
                    reason, created_at, created_by
                ) VALUES (?, ?, 'usage_reset', ?, ?, ?, ?)
                """,
                rows,
            )
        return {
            "action": "usage_reset",
            "applied": applied,
            "applied_users": [item["user"] for item in applied],
            "skipped_users": skipped,
            "token_amount": sum(item["token_amount"] for item in applied),
            "week_start_at": week_start,
            "reason": normalized_reason,
            "created_at": now,
        }

    def quota_adjustment_history(self, user_email, now=None, limit=20):
        now = int(time.time()) if now is None else int(now)
        week_start, unused_week_end = natural_week_bounds(now, self.week_timezone)
        user = str(user_email or "").strip().lower()
        safe_limit = max(1, min(int(limit or 20), 100))
        with self._connection() as connection:
            rows = connection.execute(
                """
                SELECT action, token_amount, reason, created_at, created_by
                FROM user_quota_adjustments
                WHERE user_email = ? AND week_start_at = ?
                ORDER BY created_at DESC, id DESC
                LIMIT ?
                """,
                (user, week_start, safe_limit),
            ).fetchall()
        return [
            {
                "action": row["action"],
                "token_amount": int(row["token_amount"]),
                "reason": str(row["reason"]),
                "created_at": int(row["created_at"]),
                "created_by": str(row["created_by"]),
            }
            for row in rows
        ]

    def weekly_quotas(self, user_emails, default_weekly_tokens, now=None):
        now = int(time.time()) if now is None else int(now)
        users = sorted({str(item).strip().lower() for item in user_emails if str(item).strip()})
        week_start, week_end = natural_week_bounds(now, self.week_timezone)
        result = {}
        if not users:
            return result
        usage = {}
        policies = {}
        adjustments = {}
        with self._connection() as connection:
            # Keep each IN clause bounded for SQLite builds with conservative
            # host-parameter limits. Large Admin pages can legitimately cover
            # thousands of users.
            for offset in range(0, len(users), 500):
                batch = users[offset : offset + 500]
                placeholders = ",".join("?" for _ in batch)
                for row in connection.execute(
                    "SELECT user_email, total_tokens, weighted_tokens "
                    "FROM user_weekly_usage "
                    "WHERE week_start_at = ? AND user_email IN ({})".format(
                        placeholders
                    ),
                    (week_start, *batch),
                ):
                    usage[row["user_email"]] = {
                        "total_tokens": int(row["total_tokens"] or 0),
                        "weighted_tokens": int(row["weighted_tokens"] or 0),
                    }
                for row in connection.execute(
                    "SELECT user_email, weekly_tokens, updated_at, created_by, "
                    "reset_at FROM user_quota_policies "
                    "WHERE user_email IN ({}) "
                    "AND (reset_at IS NULL OR reset_at > ?)".format(placeholders),
                    (*batch, now),
                ):
                    policies[row["user_email"]] = row
                for row in connection.execute(
                    """
                    SELECT
                        user_email,
                        SUM(CASE WHEN action = 'bonus' THEN token_amount ELSE 0 END)
                            AS bonus_tokens,
                        SUM(CASE WHEN action = 'usage_reset' THEN token_amount ELSE 0 END)
                            AS usage_reset_tokens,
                        COUNT(*) AS adjustment_count
                    FROM user_quota_adjustments
                    WHERE week_start_at = ? AND user_email IN ({})
                    GROUP BY user_email
                    """.format(placeholders),
                    (week_start, *batch),
                ):
                    adjustments[row["user_email"]] = {
                        "bonus_tokens": int(row["bonus_tokens"] or 0),
                        "usage_reset_tokens": int(row["usage_reset_tokens"] or 0),
                        "adjustment_count": int(row["adjustment_count"] or 0),
                    }
        for user in users:
            policy = policies.get(user)
            adjustment = adjustments.get(
                user,
                {
                    "bonus_tokens": 0,
                    "usage_reset_tokens": 0,
                    "adjustment_count": 0,
                },
            )
            base_limit = (
                policy["weekly_tokens"] if policy is not None else default_weekly_tokens
            )
            base_limit = None if base_limit is None else int(base_limit)
            bonus_tokens = int(adjustment["bonus_tokens"])
            limit = None if base_limit is None else base_limit + bonus_tokens
            user_usage = usage.get(
                user,
                {"total_tokens": 0, "weighted_tokens": 0},
            )
            raw_used = int(user_usage["total_tokens"])
            weighted_raw_used = int(user_usage["weighted_tokens"])
            usage_reset_tokens = int(adjustment["usage_reset_tokens"])
            used = max(0, weighted_raw_used - usage_reset_tokens)
            remaining = None if limit is None else max(0, limit - used)
            percent = None if limit is None else round(used * 100.0 / limit, 2)
            if policy is None:
                policy_mode = "inherit"
                source = "default"
            elif policy["weekly_tokens"] is None:
                policy_mode = "unlimited"
                source = "user_unlimited"
            else:
                policy_mode = "custom"
                source = "user_custom"
            result[user] = {
                "period": "natural_week",
                "timezone": self.week_timezone,
                "week_start_at": week_start,
                "week_end_at": week_end,
                "limit_tokens": limit,
                "base_limit_tokens": base_limit,
                "bonus_tokens": bonus_tokens,
                "used_tokens": used,
                "weighted_used_tokens": used,
                "raw_used_tokens": raw_used,
                "unweighted_used_tokens": raw_used,
                "weighted_raw_used_tokens": weighted_raw_used,
                "usage_reset_tokens": usage_reset_tokens,
                "remaining_tokens": remaining,
                "used_percent": percent,
                "limit_reached": limit is not None and used >= limit,
                "source": source,
                "policy_mode": policy_mode,
                "policy_tokens": (
                    None if policy is None or policy["weekly_tokens"] is None
                    else int(policy["weekly_tokens"])
                ),
                "policy_updated_at": int(policy["updated_at"]) if policy is not None else None,
                "policy_updated_by": str(policy["created_by"]) if policy is not None else None,
                "policy_reset_at": (
                    int(policy["reset_at"])
                    if policy is not None and policy["reset_at"] is not None
                    else None
                ),
                "default_limit_tokens": (
                    None if default_weekly_tokens is None else int(default_weekly_tokens)
                ),
                "unlimited": limit is None,
                "soft_limit": True,
                "quota_unit": "weighted_tokens",
                "adjustment_count": int(adjustment["adjustment_count"]),
            }
        return result

    def _weekly_usage_rows(self, connection):
        rows = connection.execute(
            "SELECT user_email, occurred_at, total_tokens, weighted_tokens, "
            "weight_policy_version "
            "FROM usage_events ORDER BY id"
        ).fetchall()
        counters = {}
        for row in rows:
            week_start, unused_end = natural_week_bounds(
                row["occurred_at"], self.week_timezone
            )
            key = (row["user_email"], week_start)
            item = counters.setdefault(key, [0, 0, 0])
            raw_tokens = int(row["total_tokens"] or 0)
            weighted_tokens = int(row["weighted_tokens"] or 0)
            # A legacy writer can insert into an upgraded database without
            # knowing the v7 columns. Its schema defaults identify that row as
            # legacy-v1, so preserve the pre-weighting 1.0 semantics instead
            # of rebuilding the user's weekly quota with zero weighted usage.
            if (
                row["weight_policy_version"] == "legacy-v1"
                and raw_tokens > 0
                and weighted_tokens == 0
            ):
                weighted_tokens = raw_tokens
            item[0] += raw_tokens
            item[1] += weighted_tokens
            item[2] += 1
        return rows, counters

    def _rebuild_weekly_usage(self, connection, now):
        rows, counters = self._weekly_usage_rows(connection)
        connection.execute("DELETE FROM user_weekly_usage")
        connection.executemany(
            "INSERT INTO user_weekly_usage("
            "user_email, week_start_at, total_tokens, weighted_tokens, "
            "request_count, updated_at"
            ") VALUES (?, ?, ?, ?, ?, ?)",
            (
                (user, week_start, values[0], values[1], values[2], now)
                for (user, week_start), values in counters.items()
            ),
        )
        connection.execute(
            "INSERT OR REPLACE INTO usage_meta(key, value) VALUES (?, ?)",
            (WEEKLY_USAGE_BACKFILL_KEY, WEEKLY_USAGE_BACKFILL_VERSION),
        )
        last_event_id = connection.execute(
            "SELECT MAX(id) AS id FROM usage_events"
        ).fetchone()
        connection.execute(
            "INSERT OR REPLACE INTO usage_meta(key, value) VALUES (?, ?)",
            (WEEKLY_USAGE_LAST_EVENT_ID_KEY, str(int(last_event_id["id"] or 0))),
        )
        return {"events": len(rows), "counters": len(counters)}

    def ensure_week_timezone(self, now=None):
        """Re-bucket weekly usage and adjustments when the configured zone changes."""
        now = int(time.time()) if now is None else int(now)
        with self._connection() as connection:
            marker = connection.execute(
                "SELECT value FROM usage_meta WHERE key = ?",
                (WEEK_TIMEZONE_META_KEY,),
            ).fetchone()
            previous = str(marker["value"]) if marker is not None else ""
            if previous == self.week_timezone:
                return {"changed": False, "timezone": self.week_timezone}
            result = self._rebuild_weekly_usage(connection, now)
            adjustments = connection.execute(
                "SELECT id, created_at FROM user_quota_adjustments"
            ).fetchall()
            connection.executemany(
                "UPDATE user_quota_adjustments SET week_start_at = ? WHERE id = ?",
                (
                    (
                        natural_week_bounds(
                            row["created_at"], self.week_timezone
                        )[0],
                        row["id"],
                    )
                    for row in adjustments
                ),
            )
            connection.execute(
                "INSERT OR REPLACE INTO usage_meta(key, value) VALUES (?, ?)",
                (WEEK_TIMEZONE_META_KEY, self.week_timezone),
            )
        return {
            **result,
            "changed": True,
            "previous_timezone": previous,
            "timezone": self.week_timezone,
            "adjustments": len(adjustments),
        }

    def set_week_timezone(self, value, now=None):
        self.week_timezone, unused_zone = _week_timezone(value)
        result = self.ensure_week_timezone(now=now)
        if result["changed"]:
            result["quota_policy_reset"] = (
                self.configure_personal_quota_weekly_reset(
                    self.reset_personal_weekly_on_new_week,
                    now=now,
                    reschedule=True,
                )
            )
        return result

    def ensure_weekly_usage_backfilled(self, now=None):
        """Backfill historical usage once before any quota snapshot can be published."""
        now = int(time.time()) if now is None else int(now)
        with self._connection() as connection:
            marker = connection.execute(
                "SELECT value FROM usage_meta WHERE key = ?",
                (WEEKLY_USAGE_BACKFILL_KEY,),
            ).fetchone()
            last_marker = connection.execute(
                "SELECT value FROM usage_meta WHERE key = ?",
                (WEEKLY_USAGE_LAST_EVENT_ID_KEY,),
            ).fetchone()
            last_event = connection.execute(
                "SELECT MAX(id) AS id FROM usage_events"
            ).fetchone()
            if (
                marker
                and marker["value"] == WEEKLY_USAGE_BACKFILL_VERSION
                and _non_negative_int(last_marker["value"] if last_marker else 0)
                == int(last_event["id"] or 0)
            ):
                return {"backfilled": False}
            result = self._rebuild_weekly_usage(connection, now)
        return {"backfilled": True, **result}

    def rebuild_weekly_usage(self, now=None):
        now = int(time.time()) if now is None else int(now)
        with self._connection() as connection:
            result = self._rebuild_weekly_usage(connection, now)
        return {"backfilled": True, **result}

    def update_collector_status(self, last_error="", now=None):
        now = int(time.time()) if now is None else int(now)
        values = {
            "collector_heartbeat_at": str(now),
            "collector_last_error": str(last_error or "")[:500],
        }
        with self._connection() as connection:
            connection.executemany(
                "INSERT OR REPLACE INTO usage_meta(key, value) VALUES (?, ?)",
                values.items(),
            )

    def status(self, now=None):
        now = int(time.time()) if now is None else int(now)
        with self._connection() as connection:
            meta = {row["key"]: row["value"] for row in connection.execute("SELECT key, value FROM usage_meta")}
            event = connection.execute(
                "SELECT COUNT(*) AS count, MIN(occurred_at) AS first_at, MAX(occurred_at) AS last_at "
                "FROM usage_events"
            ).fetchone()
        heartbeat = _non_negative_int(meta.get("collector_heartbeat_at"))
        error = meta.get("collector_last_error", "")
        if heartbeat and now - heartbeat <= 15 and not error:
            state = "healthy"
        elif heartbeat:
            state = "degraded"
        else:
            state = "starting"
        return {
            "status": state,
            "heartbeat_at": heartbeat,
            "last_error": error,
            "event_count": int(event["count"] or 0),
            "collection_started_at": int(event["first_at"] or 0),
            "usage_breakdown_started_at": _non_negative_int(
                meta.get(USAGE_BREAKDOWN_STARTED_AT_KEY)
            ),
            "last_event_at": int(event["last_at"] or 0),
        }
