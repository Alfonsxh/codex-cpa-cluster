#!/usr/bin/env python3
"""SQLite persistence for authoritative control-plane state."""

import hashlib
import json
import os
import sqlite3
import time
import uuid
from contextlib import contextmanager
import fcntl
from pathlib import Path

from cryptography.exceptions import InvalidTag
from cryptography.hazmat.primitives.ciphers.aead import AESGCM


SCHEMA_VERSION = 6
OBSOLETE_PROJECTION_PATHS = (
    "state/accounts.json",
    "state/keys.json",
    "state/user-routes.json",
    "state/configuration.json",
    "state/account-failover.json",
    "state/notification-state.json",
    "state/log-maintenance.json",
    "state/deployment.json",
    "secrets/user-internal-keys.json",
)
LEGACY_SECRET_FILES = {
    "cpa_management_key": "cpa-management.key",
    "wecom_webhook": "wecom-webhook.url",
}
RETIRED_SECRET_NAMES = {"gost_tunnel_auth"}
PROFILE_DIGEST_METADATA_KEY = "deployment_profile_sha256"
PROJECTION_CLEANUP_METADATA_KEY = "retired_projection_cleanup_complete"
REQUIRED_TABLES = {
    "schema_migrations",
    "metadata",
    "settings",
    "accounts",
    "user_routes",
    "key_records",
    "internal_keys",
    "runtime_state",
    "branding_assets",
    "encrypted_secrets",
    "teams",
    "user_team_memberships",
    "tags",
    "user_tags",
}


class ControlPlaneStore:
    """Own low-frequency control state without compatibility JSON mirrors."""

    def __init__(self, root):
        self.root = Path(root).resolve()
        self.state_dir = self.root / "state"
        self.secrets_dir = self.root / "secrets"
        self.path = self.state_dir / "control-plane.sqlite3"
        self.encryption_key_path = Path(
            os.environ.get(
                "CLIPROXY_SECRET_KEY_FILE",
                self.secrets_dir / "control-plane.key",
            )
        )
        self.lock_path = self.state_dir / ".control-plane.lock"
        self.state_dir.mkdir(parents=True, exist_ok=True)
        os.chmod(self.state_dir, 0o700)
        self.secrets_dir.mkdir(parents=True, exist_ok=True)
        os.chmod(self.secrets_dir, 0o700)
        self._encryption_key = None
        self._initialize()

    @contextmanager
    def _exclusive(self):
        """Serialize control-plane mutations across processes."""
        descriptor = os.open(str(self.lock_path), os.O_CREAT | os.O_RDWR, 0o600)
        try:
            os.chmod(self.lock_path, 0o600)
            fcntl.flock(descriptor, fcntl.LOCK_EX)
            yield
        finally:
            fcntl.flock(descriptor, fcntl.LOCK_UN)
            os.close(descriptor)

    def _connect(self):
        connection = sqlite3.connect(str(self.path), timeout=30)
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA busy_timeout = 30000")
        connection.execute("PRAGMA foreign_keys = ON")
        connection.execute("PRAGMA journal_mode = WAL")
        connection.execute("PRAGMA synchronous = FULL")
        return connection

    def _initialize(self):
        with self._exclusive():
            with self._connect() as connection:
                connection.executescript(
                    """
                    CREATE TABLE IF NOT EXISTS schema_migrations (
                    version INTEGER PRIMARY KEY,
                    applied_at INTEGER NOT NULL
                );
                CREATE TABLE IF NOT EXISTS metadata (
                    key TEXT PRIMARY KEY,
                    value TEXT NOT NULL
                );
                CREATE TABLE IF NOT EXISTS settings (
                    key TEXT PRIMARY KEY,
                    value_json TEXT NOT NULL,
                    updated_at INTEGER NOT NULL
                );
                CREATE TABLE IF NOT EXISTS accounts (
                    id TEXT PRIMARY KEY,
                    email TEXT NOT NULL UNIQUE,
                    port INTEGER NOT NULL UNIQUE,
                    proxy_mode TEXT NOT NULL DEFAULT 'inherit',
                    created_at INTEGER NOT NULL,
                    group_enabled INTEGER NOT NULL DEFAULT 1,
                    default_group INTEGER NOT NULL DEFAULT 0,
                    position INTEGER NOT NULL
                );
                CREATE TABLE IF NOT EXISTS user_routes (
                    user_email TEXT PRIMARY KEY,
                    account_id TEXT NOT NULL
                );
                CREATE TABLE IF NOT EXISTS key_records (
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
                CREATE INDEX IF NOT EXISTS idx_key_records_user_status
                    ON key_records(user_email, status);
                CREATE INDEX IF NOT EXISTS idx_key_records_secret
                    ON key_records(secret);
                CREATE TABLE IF NOT EXISTS internal_keys (
                    user_email TEXT PRIMARY KEY,
                    secret TEXT NOT NULL UNIQUE,
                    created_at INTEGER NOT NULL,
                    status TEXT NOT NULL
                );
                CREATE TABLE IF NOT EXISTS runtime_state (
                    name TEXT PRIMARY KEY,
                    payload_json TEXT NOT NULL,
                    updated_at INTEGER NOT NULL
                );
                CREATE TABLE IF NOT EXISTS branding_assets (
                    name TEXT PRIMARY KEY,
                    filename TEXT NOT NULL,
                    content_type TEXT NOT NULL,
                    content BLOB NOT NULL,
                    sha256 TEXT NOT NULL,
                    updated_at INTEGER NOT NULL
                );
                CREATE TABLE IF NOT EXISTS encrypted_secrets (
                    name TEXT PRIMARY KEY,
                    nonce BLOB NOT NULL,
                    ciphertext BLOB NOT NULL,
                    value_sha256 TEXT NOT NULL,
                    updated_at INTEGER NOT NULL
                );
                CREATE TABLE IF NOT EXISTS teams (
                    id TEXT PRIMARY KEY,
                    name TEXT NOT NULL UNIQUE COLLATE NOCASE,
                    description TEXT NOT NULL DEFAULT '',
                    created_at INTEGER NOT NULL,
                    updated_at INTEGER NOT NULL
                );
                CREATE TABLE IF NOT EXISTS user_team_memberships (
                    user_email TEXT PRIMARY KEY,
                    team_id TEXT REFERENCES teams(id) ON DELETE RESTRICT,
                    membership_version INTEGER NOT NULL DEFAULT 0,
                    updated_at INTEGER NOT NULL
                );
                CREATE INDEX IF NOT EXISTS idx_user_team_memberships_team
                    ON user_team_memberships(team_id, user_email);
                CREATE TABLE IF NOT EXISTS tags (
                    id TEXT PRIMARY KEY,
                    name TEXT NOT NULL UNIQUE COLLATE NOCASE,
                    color TEXT NOT NULL DEFAULT '#6374d8',
                    created_at INTEGER NOT NULL,
                    updated_at INTEGER NOT NULL
                );
                CREATE TABLE IF NOT EXISTS user_tags (
                    user_email TEXT NOT NULL,
                    tag_id TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
                    assigned_at INTEGER NOT NULL,
                    PRIMARY KEY(user_email, tag_id)
                );
                CREATE INDEX IF NOT EXISTS idx_user_tags_tag
                    ON user_tags(tag_id, user_email);
                    """
                )
                previous_schema_version = int(
                    connection.execute(
                        "SELECT COALESCE(MAX(version), 0) FROM schema_migrations"
                    ).fetchone()[0]
                )
                account_columns = {
                    row["name"]
                    for row in connection.execute("PRAGMA table_info(accounts)").fetchall()
                }
                if "proxy_mode" not in account_columns:
                    connection.execute(
                        "ALTER TABLE accounts ADD COLUMN proxy_mode TEXT NOT NULL DEFAULT 'inherit'"
                    )
                    account_columns.add("proxy_mode")
                expected_account_columns = {
                    "id",
                    "email",
                    "port",
                    "proxy_mode",
                    "created_at",
                    "group_enabled",
                    "default_group",
                    "position",
                }
                retired_account_columns = {"gost_port"}
                unexpected_account_columns = (
                    account_columns - expected_account_columns - retired_account_columns
                )
                missing_account_columns = expected_account_columns - account_columns
                if unexpected_account_columns or missing_account_columns:
                    raise ValueError(
                        "accounts 表结构不受支持：缺少 {}，未知 {}".format(
                            ", ".join(sorted(missing_account_columns)) or "无",
                            ", ".join(sorted(unexpected_account_columns)) or "无",
                        )
                    )
                if account_columns & retired_account_columns:
                    connection.executescript(
                        """
                        DROP TABLE IF EXISTS accounts_v6;
                        CREATE TABLE accounts_v6 (
                            id TEXT PRIMARY KEY,
                            email TEXT NOT NULL UNIQUE,
                            port INTEGER NOT NULL UNIQUE,
                            proxy_mode TEXT NOT NULL DEFAULT 'inherit',
                            created_at INTEGER NOT NULL,
                            group_enabled INTEGER NOT NULL DEFAULT 1,
                            default_group INTEGER NOT NULL DEFAULT 0,
                            position INTEGER NOT NULL
                        );
                        INSERT INTO accounts_v6(
                            id, email, port, proxy_mode, created_at,
                            group_enabled, default_group, position
                        )
                        SELECT
                            id, email, port, proxy_mode, created_at,
                            group_enabled, default_group, position
                        FROM accounts;
                        DROP TABLE accounts;
                        ALTER TABLE accounts_v6 RENAME TO accounts;
                        """
                    )
                if previous_schema_version < SCHEMA_VERSION:
                    for name in RETIRED_SECRET_NAMES:
                        connection.execute(
                            "DELETE FROM encrypted_secrets WHERE name = ?", (name,)
                        )
                connection.execute(
                    "INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (?, ?)",
                    (SCHEMA_VERSION, int(time.time())),
                )
                self._load_or_create_encryption_key(connection)
                self._import_legacy_secrets(connection)
            os.chmod(self.path, 0o600)

    def _load_or_create_encryption_key(self, connection=None):
        if self._encryption_key is not None:
            return self._encryption_key
        path = self.encryption_key_path
        try:
            key = path.read_bytes()
        except FileNotFoundError:
            encrypted_count = 0
            if connection is not None:
                encrypted_count = int(
                    connection.execute("SELECT COUNT(*) FROM encrypted_secrets").fetchone()[0]
                )
            if encrypted_count:
                raise ValueError(
                    "控制面加密主密钥缺失，拒绝生成新密钥覆盖现有秘密：{}".format(path)
                )
            path.parent.mkdir(parents=True, exist_ok=True)
            os.chmod(path.parent, 0o700)
            key = os.urandom(32)
            try:
                descriptor = os.open(str(path), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            except FileExistsError:
                key = path.read_bytes()
            else:
                try:
                    os.write(descriptor, key)
                finally:
                    os.close(descriptor)
        if len(key) != 32:
            raise ValueError("控制面加密主密钥必须正好为 32 字节：{}".format(path))
        if path.stat().st_mode & 0o077:
            raise ValueError("控制面加密主密钥权限必须为 0600：{}".format(path))
        self._encryption_key = key
        return key

    @staticmethod
    def secret_digest(value):
        return hashlib.sha256(str(value).encode("utf-8")).hexdigest()

    def _encrypt_secret(self, name, value):
        plaintext = str(value).encode("utf-8")
        nonce = os.urandom(12)
        ciphertext = AESGCM(self._load_or_create_encryption_key()).encrypt(
            nonce,
            plaintext,
            str(name).encode("utf-8"),
        )
        return nonce, ciphertext, hashlib.sha256(plaintext).hexdigest()

    def _decrypt_secret_row(self, row):
        try:
            plaintext = AESGCM(self._load_or_create_encryption_key()).decrypt(
                bytes(row["nonce"]),
                bytes(row["ciphertext"]),
                str(row["name"]).encode("utf-8"),
            )
            value = plaintext.decode("utf-8")
        except (InvalidTag, ValueError, UnicodeDecodeError) as error:
            raise ValueError("控制面秘密无法解密：{}".format(row["name"])) from error
        if self.secret_digest(value) != row["value_sha256"]:
            raise ValueError("控制面秘密完整性摘要不匹配：{}".format(row["name"]))
        return value

    def _write_secret(self, connection, name, value):
        nonce, ciphertext, digest = self._encrypt_secret(name, value)
        connection.execute(
            """
            INSERT INTO encrypted_secrets(name, nonce, ciphertext, value_sha256, updated_at)
            VALUES (?, ?, ?, ?, ?)
            ON CONFLICT(name) DO UPDATE SET
                nonce = excluded.nonce,
                ciphertext = excluded.ciphertext,
                value_sha256 = excluded.value_sha256,
                updated_at = excluded.updated_at
            """,
            (str(name), nonce, ciphertext, digest, int(time.time())),
        )

    def _import_legacy_secrets(self, connection):
        for name, filename in LEGACY_SECRET_FILES.items():
            exists = connection.execute(
                "SELECT 1 FROM encrypted_secrets WHERE name = ?", (name,)
            ).fetchone()
            if exists:
                continue
            path = self.secrets_dir / filename
            try:
                value = self._read_legacy_secret(path)
            except FileNotFoundError:
                continue
            if value:
                self._write_secret(connection, name, value)

    @staticmethod
    def _read_legacy_secret(path):
        return path.read_text(encoding="utf-8").strip()

    def read_secret(self, name, default=None):
        with self._connect() as connection:
            row = connection.execute(
                "SELECT * FROM encrypted_secrets WHERE name = ?", (str(name),)
            ).fetchone()
        if row is not None:
            return self._decrypt_secret_row(row)
        filename = LEGACY_SECRET_FILES.get(str(name))
        if not filename:
            return default
        path = self.secrets_dir / filename
        try:
            value = self._read_legacy_secret(path)
        except FileNotFoundError:
            return default
        if not value:
            return default
        self.write_secret(name, value)
        return value

    def write_secret(self, name, value):
        value = str(value)
        if not value:
            raise ValueError("控制面秘密不能为空：{}".format(name))
        with self._exclusive():
            with self._connect() as connection:
                self._write_secret(connection, name, value)

    def delete_secret(self, name):
        with self._exclusive():
            with self._connect() as connection:
                connection.execute(
                    "DELETE FROM encrypted_secrets WHERE name = ?", (str(name),)
                )

    def secret_status(self):
        with self._connect() as connection:
            rows = connection.execute(
                "SELECT name, value_sha256, updated_at FROM encrypted_secrets ORDER BY name"
            ).fetchall()
        return {
            row["name"]: {
                "sha256": row["value_sha256"],
                "updated_at": row["updated_at"],
            }
            for row in rows
        }

    def metadata_value(self, key, default=None):
        with self._connect() as connection:
            row = connection.execute(
                "SELECT value FROM metadata WHERE key = ?", (str(key),)
            ).fetchone()
        return row["value"] if row else default

    def write_metadata(self, key, value):
        with self._exclusive():
            with self._connect() as connection:
                connection.execute(
                    """
                    INSERT INTO metadata(key, value) VALUES (?, ?)
                    ON CONFLICT(key) DO UPDATE SET value = excluded.value
                    """,
                    (str(key), str(value)),
                )

    def migrate_legacy_secrets(self, cleanup=False):
        imported = []
        cleaned = []
        with self._exclusive():
            with self._connect() as connection:
                for name, filename in LEGACY_SECRET_FILES.items():
                    path = self.secrets_dir / filename
                    row = connection.execute(
                        "SELECT * FROM encrypted_secrets WHERE name = ?", (name,)
                    ).fetchone()
                    if row is None and path.is_file():
                        value = self._read_legacy_secret(path)
                        if value:
                            self._write_secret(connection, name, value)
                            imported.append(name)
                            row = connection.execute(
                                "SELECT * FROM encrypted_secrets WHERE name = ?", (name,)
                            ).fetchone()
                    if cleanup and path.is_file():
                        if row is None:
                            raise ValueError("秘密尚未写入数据库，拒绝删除：{}".format(path))
                        current = self._read_legacy_secret(path)
                        if self.secret_digest(current) != row["value_sha256"]:
                            raise ValueError("文件与数据库秘密不一致，拒绝删除：{}".format(path))
                        path.unlink()
                        cleaned.append(str(path.relative_to(self.root)))

                issued = self.secrets_dir / "issued-keys.tsv"
                if cleanup and issued.is_file():
                    issued.unlink()
                    cleaned.append(str(issued.relative_to(self.root)))

                profile = self.secrets_dir / "deployment-profile.json"
                profile_digest = connection.execute(
                    "SELECT value FROM metadata WHERE key = ?",
                    (PROFILE_DIGEST_METADATA_KEY,),
                ).fetchone()
                if cleanup and profile.is_file():
                    if profile_digest is None:
                        raise ValueError("部署配置档案尚未完成一次性导入，拒绝删除")
                    profile.unlink()
                    cleaned.append(str(profile.relative_to(self.root)))
        return {
            "imported": sorted(imported),
            "cleaned": sorted(cleaned),
            "encrypted": sorted(self.secret_status()),
        }

    @staticmethod
    def _replace_accounts(connection, records):
        if not isinstance(records, list):
            raise ValueError("账号数据必须为数组")
        connection.execute("DELETE FROM accounts")
        for position, item in enumerate(records):
            connection.execute(
                """
                INSERT INTO accounts(
                    id, email, port, proxy_mode, created_at,
                    group_enabled, default_group, position
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    str(item["id"]),
                    str(item["email"]),
                    int(item["port"]),
                    str(item.get("proxy_mode") or "inherit"),
                    int(item.get("created_at", 0)),
                    0 if item.get("group_enabled") is False else 1,
                    1 if item.get("default_group") is True else 0,
                    position,
                ),
            )

    @staticmethod
    def _replace_routes(connection, routes):
        if not isinstance(routes, dict):
            raise ValueError("用户路由数据必须为对象")
        connection.execute("DELETE FROM user_routes")
        connection.executemany(
            "INSERT INTO user_routes(user_email, account_id) VALUES (?, ?)",
            [(str(user), str(account)) for user, account in routes.items()],
        )

    @staticmethod
    def _replace_key_records(connection, records):
        connection.execute("DELETE FROM key_records")
        connection.executemany(
            """
            INSERT INTO key_records(
                sequence, label, account_id, account_email, user_email,
                status, secret, created_at, updated_at
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                (
                    sequence,
                    str(item["label"]),
                    str(item["account"]),
                    str(item["account_email"]),
                    str(item["user"]),
                    str(item["status"]),
                    str(item["key"]),
                    int(item.get("created_at", 0)),
                    int(item.get("updated_at", 0)),
                )
                for sequence, item in enumerate(records)
            ),
        )

    @staticmethod
    def _replace_internal_keys(connection, users):
        if not isinstance(users, dict):
            raise ValueError("内部 Key 数据必须为对象")
        connection.execute("DELETE FROM internal_keys")
        for user, item in users.items():
            connection.execute(
                """
                INSERT INTO internal_keys(user_email, secret, created_at, status)
                VALUES (?, ?, ?, ?)
                """,
                (
                    str(user),
                    str(item["key"]),
                    int(item.get("created_at", 0)),
                    str(item.get("status", "active")),
                ),
            )

    def read_accounts(self):
        with self._connect() as connection:
            rows = connection.execute(
                "SELECT * FROM accounts ORDER BY position, id"
            ).fetchall()
        records = []
        for row in rows:
            item = {
                "id": row["id"],
                "email": row["email"],
                "port": row["port"],
                "created_at": row["created_at"],
                "group_enabled": bool(row["group_enabled"]),
                "default_group": bool(row["default_group"]),
                "proxy_mode": row["proxy_mode"] or "inherit",
            }
            records.append(item)
        return records

    def write_accounts(self, records):
        with self._exclusive():
            with self._connect() as connection:
                self._replace_accounts(connection, records)

    def read_routes(self):
        with self._connect() as connection:
            rows = connection.execute(
                "SELECT user_email, account_id FROM user_routes ORDER BY user_email"
            ).fetchall()
        return {row["user_email"]: row["account_id"] for row in rows}

    def write_routes(self, routes):
        with self._exclusive():
            with self._connect() as connection:
                self._replace_routes(connection, routes)

    def read_key_records(self):
        with self._connect() as connection:
            rows = connection.execute(
                "SELECT * FROM key_records ORDER BY sequence"
            ).fetchall()
        return [
            {
                "label": row["label"],
                "account": row["account_id"],
                "account_email": row["account_email"],
                "user": row["user_email"],
                "status": row["status"],
                "key": row["secret"],
                "created_at": row["created_at"],
                "updated_at": row["updated_at"],
            }
            for row in rows
        ]

    def read_key_records_for_users(self, user_emails):
        users = sorted(
            {
                str(item).strip().lower()
                for item in user_emails
                if str(item).strip()
            }
        )
        if not users:
            return []
        rows = []
        with self._connect() as connection:
            for offset in range(0, len(users), 500):
                batch = users[offset : offset + 500]
                placeholders = ",".join("?" for _ in batch)
                rows.extend(
                    connection.execute(
                        "SELECT * FROM key_records "
                        "WHERE user_email IN ({}) ORDER BY sequence".format(
                            placeholders
                        ),
                        batch,
                    ).fetchall()
                )
        return [
            {
                "label": row["label"],
                "account": row["account_id"],
                "account_email": row["account_email"],
                "user": row["user_email"],
                "status": row["status"],
                "key": row["secret"],
                "created_at": row["created_at"],
                "updated_at": row["updated_at"],
            }
            for row in rows
        ]

    def read_user_summaries(self, search=""):
        search = str(search or "").strip().lower()
        clauses = []
        parameters = []
        if search:
            clauses.append("user_email LIKE ?")
            parameters.append("%{}%".format(search))
        where = "WHERE {}".format(" AND ".join(clauses)) if clauses else ""
        with self._connect() as connection:
            rows = connection.execute(
                """
                SELECT user_email AS email,
                       COUNT(*) AS total_records,
                       COUNT(DISTINCT CASE WHEN status = 'active' THEN secret END)
                           AS active_keys,
                       COUNT(DISTINCT CASE WHEN status = 'active' THEN account_id END)
                           AS active_accounts,
                       MIN(created_at) AS created_at,
                       MAX(updated_at) AS updated_at
                  FROM key_records
                  {}
                 GROUP BY user_email
                 ORDER BY user_email
                """.format(where),
                parameters,
            ).fetchall()
        return [
            {
                "email": str(row["email"]),
                "total_records": int(row["total_records"] or 0),
                "active_keys": int(row["active_keys"] or 0),
                "active_accounts": int(row["active_accounts"] or 0),
                "created_at": int(row["created_at"] or 0),
                "updated_at": int(row["updated_at"] or 0),
            }
            for row in rows
        ]

    @staticmethod
    def _catalog_name(value, label):
        name = " ".join(str(value or "").strip().split())
        if not name:
            raise ValueError("{}名称不能为空".format(label))
        if len(name) > 64:
            raise ValueError("{}名称不能超过 64 个字符".format(label))
        return name

    @staticmethod
    def _catalog_description(value):
        description = " ".join(str(value or "").strip().split())
        if len(description) > 200:
            raise ValueError("团队说明不能超过 200 个字符")
        return description

    @staticmethod
    def _tag_color(value):
        color = str(value or "#6374d8").strip().lower()
        if (
            len(color) != 7
            or not color.startswith("#")
            or any(character not in "0123456789abcdef" for character in color[1:])
        ):
            raise ValueError("标签颜色必须为 #RRGGBB")
        return color

    def list_teams(self):
        with self._connect() as connection:
            rows = connection.execute(
                """
                SELECT t.id, t.name, t.description, t.created_at, t.updated_at,
                       COUNT(m.user_email) AS user_count
                  FROM teams AS t
                  LEFT JOIN user_team_memberships AS m ON m.team_id = t.id
                 GROUP BY t.id
                 ORDER BY t.name COLLATE NOCASE, t.id
                """
            ).fetchall()
        return [
            {
                "id": str(row["id"]),
                "name": str(row["name"]),
                "description": str(row["description"] or ""),
                "user_count": int(row["user_count"] or 0),
                "created_at": int(row["created_at"]),
                "updated_at": int(row["updated_at"]),
            }
            for row in rows
        ]

    def create_team(self, name, description="", now=None):
        now = int(time.time()) if now is None else int(now)
        team = {
            "id": "team_{}".format(uuid.uuid4().hex[:16]),
            "name": self._catalog_name(name, "团队"),
            "description": self._catalog_description(description),
        }
        try:
            with self._exclusive():
                with self._connect() as connection:
                    connection.execute(
                        "INSERT INTO teams(id, name, description, created_at, updated_at) "
                        "VALUES (?, ?, ?, ?, ?)",
                        (team["id"], team["name"], team["description"], now, now),
                    )
        except sqlite3.IntegrityError as error:
            raise ValueError("团队名称已存在") from error
        return {**team, "user_count": 0, "created_at": now, "updated_at": now}

    def update_team(self, team_id, name, description="", now=None):
        now = int(time.time()) if now is None else int(now)
        team_id = str(team_id or "").strip()
        normalized_name = self._catalog_name(name, "团队")
        normalized_description = self._catalog_description(description)
        try:
            with self._exclusive():
                with self._connect() as connection:
                    cursor = connection.execute(
                        "UPDATE teams SET name = ?, description = ?, updated_at = ? "
                        "WHERE id = ?",
                        (normalized_name, normalized_description, now, team_id),
                    )
                    if not cursor.rowcount:
                        raise KeyError(team_id)
        except sqlite3.IntegrityError as error:
            raise ValueError("团队名称已存在") from error
        except KeyError as error:
            raise ValueError("团队不存在") from error
        return next(item for item in self.list_teams() if item["id"] == team_id)

    def delete_team(self, team_id):
        team_id = str(team_id or "").strip()
        with self._exclusive():
            with self._connect() as connection:
                row = connection.execute(
                    "SELECT name FROM teams WHERE id = ?", (team_id,)
                ).fetchone()
                if row is None:
                    raise ValueError("团队不存在")
                assigned = int(
                    connection.execute(
                        "SELECT COUNT(*) FROM user_team_memberships WHERE team_id = ?",
                        (team_id,),
                    ).fetchone()[0]
                )
                if assigned:
                    raise ValueError("团队仍有 {} 位用户，不能删除".format(assigned))
                connection.execute("DELETE FROM teams WHERE id = ?", (team_id,))
        return {"id": team_id, "name": str(row["name"]), "deleted": True}

    def list_tags(self):
        with self._connect() as connection:
            rows = connection.execute(
                """
                SELECT t.id, t.name, t.color, t.created_at, t.updated_at,
                       COUNT(ut.user_email) AS user_count
                  FROM tags AS t
                  LEFT JOIN user_tags AS ut ON ut.tag_id = t.id
                 GROUP BY t.id
                 ORDER BY t.name COLLATE NOCASE, t.id
                """
            ).fetchall()
        return [
            {
                "id": str(row["id"]),
                "name": str(row["name"]),
                "color": str(row["color"]),
                "user_count": int(row["user_count"] or 0),
                "created_at": int(row["created_at"]),
                "updated_at": int(row["updated_at"]),
            }
            for row in rows
        ]

    def create_tag(self, name, color="#6374d8", now=None):
        now = int(time.time()) if now is None else int(now)
        tag = {
            "id": "tag_{}".format(uuid.uuid4().hex[:16]),
            "name": self._catalog_name(name, "标签"),
            "color": self._tag_color(color),
        }
        try:
            with self._exclusive():
                with self._connect() as connection:
                    connection.execute(
                        "INSERT INTO tags(id, name, color, created_at, updated_at) "
                        "VALUES (?, ?, ?, ?, ?)",
                        (tag["id"], tag["name"], tag["color"], now, now),
                    )
        except sqlite3.IntegrityError as error:
            raise ValueError("标签名称已存在") from error
        return {**tag, "user_count": 0, "created_at": now, "updated_at": now}

    def update_tag(self, tag_id, name, color="#6374d8", now=None):
        now = int(time.time()) if now is None else int(now)
        tag_id = str(tag_id or "").strip()
        normalized_name = self._catalog_name(name, "标签")
        normalized_color = self._tag_color(color)
        try:
            with self._exclusive():
                with self._connect() as connection:
                    cursor = connection.execute(
                        "UPDATE tags SET name = ?, color = ?, updated_at = ? WHERE id = ?",
                        (normalized_name, normalized_color, now, tag_id),
                    )
                    if not cursor.rowcount:
                        raise KeyError(tag_id)
        except sqlite3.IntegrityError as error:
            raise ValueError("标签名称已存在") from error
        except KeyError as error:
            raise ValueError("标签不存在") from error
        return next(item for item in self.list_tags() if item["id"] == tag_id)

    def delete_tag(self, tag_id):
        tag_id = str(tag_id or "").strip()
        with self._exclusive():
            with self._connect() as connection:
                row = connection.execute(
                    "SELECT name FROM tags WHERE id = ?", (tag_id,)
                ).fetchone()
                if row is None:
                    raise ValueError("标签不存在")
                connection.execute("DELETE FROM tags WHERE id = ?", (tag_id,))
        return {"id": tag_id, "name": str(row["name"]), "deleted": True}

    def set_user_teams(self, user_emails, team_id, now=None):
        now = int(time.time()) if now is None else int(now)
        users = sorted(
            {
                str(item or "").strip().lower()
                for item in user_emails
                if str(item or "").strip()
            }
        )
        if not users:
            raise ValueError("请选择用户")
        normalized_team_id = str(team_id or "").strip() or None
        assignments = []
        with self._exclusive():
            with self._connect() as connection:
                if normalized_team_id is not None:
                    exists = connection.execute(
                        "SELECT 1 FROM teams WHERE id = ?", (normalized_team_id,)
                    ).fetchone()
                    if exists is None:
                        raise ValueError("团队不存在")
                for user in users:
                    current = connection.execute(
                        "SELECT team_id, membership_version, updated_at "
                        "FROM user_team_memberships "
                        "WHERE user_email = ?",
                        (user,),
                    ).fetchone()
                    current_team = current["team_id"] if current else None
                    current_version = int(current["membership_version"] or 0) if current else 0
                    if current_team == normalized_team_id:
                        version = current_version
                        changed = False
                    else:
                        version = current_version + 1
                        changed = True
                        connection.execute(
                            """
                            INSERT INTO user_team_memberships(
                                user_email, team_id, membership_version, updated_at
                            ) VALUES (?, ?, ?, ?)
                            ON CONFLICT(user_email) DO UPDATE SET
                                team_id = excluded.team_id,
                                membership_version = excluded.membership_version,
                                updated_at = excluded.updated_at
                            """,
                            (user, normalized_team_id, version, now),
                        )
                    assignments.append(
                        {
                            "user": user,
                            "team_id": normalized_team_id,
                            "membership_version": version,
                            "changed": changed,
                            "updated_at": now if changed else (
                                int(current["updated_at"]) if current else 0
                            ),
                        }
                    )
        return assignments

    def set_user_tags(self, user_email, tag_ids, now=None):
        now = int(time.time()) if now is None else int(now)
        user = str(user_email or "").strip().lower()
        if not user:
            raise ValueError("用户不能为空")
        if not isinstance(tag_ids, list):
            raise ValueError("标签必须为数组")
        normalized = sorted({str(item or "").strip() for item in tag_ids if str(item or "").strip()})
        if len(normalized) > 20:
            raise ValueError("单个用户最多分配 20 个标签")
        with self._exclusive():
            with self._connect() as connection:
                if normalized:
                    placeholders = ",".join("?" for _ in normalized)
                    found = {
                        str(row["id"])
                        for row in connection.execute(
                            "SELECT id FROM tags WHERE id IN ({})".format(placeholders),
                            normalized,
                        )
                    }
                    missing = [tag_id for tag_id in normalized if tag_id not in found]
                    if missing:
                        raise ValueError("标签不存在：{}".format("、".join(missing[:3])))
                connection.execute("DELETE FROM user_tags WHERE user_email = ?", (user,))
                connection.executemany(
                    "INSERT INTO user_tags(user_email, tag_id, assigned_at) VALUES (?, ?, ?)",
                    ((user, tag_id, now) for tag_id in normalized),
                )
        return self.read_user_classifications([user])[user]["tags"]

    def update_user_tag_memberships(self, user_emails, tag_id, assigned, now=None):
        now = int(time.time()) if now is None else int(now)
        users = sorted(
            {
                str(item or "").strip().lower()
                for item in user_emails
                if str(item or "").strip()
            }
        )
        if not users:
            raise ValueError("请选择用户")
        normalized_tag_id = str(tag_id or "").strip()
        if not normalized_tag_id:
            raise ValueError("请选择标签")
        with self._exclusive():
            with self._connect() as connection:
                exists = connection.execute(
                    "SELECT 1 FROM tags WHERE id = ?", (normalized_tag_id,)
                ).fetchone()
                if exists is None:
                    raise ValueError("标签不存在")
                if assigned:
                    placeholders = ",".join("?" for _ in users)
                    counts = {
                        str(row["user_email"]): int(row["tag_count"] or 0)
                        for row in connection.execute(
                            "SELECT user_email, COUNT(*) AS tag_count FROM user_tags "
                            "WHERE user_email IN ({}) GROUP BY user_email".format(placeholders),
                            users,
                        )
                    }
                    existing = {
                        str(row["user_email"])
                        for row in connection.execute(
                            "SELECT user_email FROM user_tags WHERE tag_id = ? "
                            "AND user_email IN ({})".format(placeholders),
                            (normalized_tag_id, *users),
                        )
                    }
                    over_limit = [
                        user for user in users
                        if user not in existing and counts.get(user, 0) >= 20
                    ]
                    if over_limit:
                        raise ValueError(
                            "以下用户已达到 20 个标签上限：{}".format(
                                "、".join(over_limit[:3])
                            )
                        )
                    connection.executemany(
                        "INSERT OR IGNORE INTO user_tags(user_email, tag_id, assigned_at) "
                        "VALUES (?, ?, ?)",
                        ((user, normalized_tag_id, now) for user in users),
                    )
                else:
                    connection.executemany(
                        "DELETE FROM user_tags WHERE user_email = ? AND tag_id = ?",
                        ((user, normalized_tag_id) for user in users),
                    )
        return self.read_user_classifications(users)

    def read_user_classifications(self, user_emails):
        users = sorted(
            {
                str(item or "").strip().lower()
                for item in user_emails
                if str(item or "").strip()
            }
        )
        result = {
            user: {
                "team_id": None,
                "team": None,
                "team_membership_version": 0,
                "tags": [],
            }
            for user in users
        }
        if not users:
            return result
        with self._connect() as connection:
            for offset in range(0, len(users), 500):
                batch = users[offset : offset + 500]
                placeholders = ",".join("?" for _ in batch)
                for row in connection.execute(
                    """
                    SELECT m.user_email, m.team_id, m.membership_version,
                           t.name AS team_name, t.description AS team_description
                      FROM user_team_memberships AS m
                      LEFT JOIN teams AS t ON t.id = m.team_id
                     WHERE m.user_email IN ({})
                    """.format(placeholders),
                    batch,
                ):
                    item = result[str(row["user_email"])]
                    item["team_id"] = row["team_id"]
                    item["team_membership_version"] = int(row["membership_version"] or 0)
                    if row["team_id"] is not None:
                        item["team"] = {
                            "id": str(row["team_id"]),
                            "name": str(row["team_name"]),
                            "description": str(row["team_description"] or ""),
                        }
                for row in connection.execute(
                    """
                    SELECT ut.user_email, t.id, t.name, t.color
                      FROM user_tags AS ut
                      JOIN tags AS t ON t.id = ut.tag_id
                     WHERE ut.user_email IN ({})
                     ORDER BY t.name COLLATE NOCASE, t.id
                    """.format(placeholders),
                    batch,
                ):
                    result[str(row["user_email"])]["tags"].append(
                        {
                            "id": str(row["id"]),
                            "name": str(row["name"]),
                            "color": str(row["color"]),
                        }
                    )
        return result

    def delete_user_classification(self, user_email):
        user = str(user_email or "").strip().lower()
        with self._exclusive():
            with self._connect() as connection:
                tag_count = connection.execute(
                    "DELETE FROM user_tags WHERE user_email = ?", (user,)
                ).rowcount
                team_count = connection.execute(
                    "DELETE FROM user_team_memberships WHERE user_email = ?", (user,)
                ).rowcount
        return {"team": int(team_count), "tags": int(tag_count)}

    def write_key_records(self, records):
        with self._exclusive():
            with self._connect() as connection:
                self._replace_key_records(connection, records)

    def read_internal_keys(self):
        with self._connect() as connection:
            rows = connection.execute(
                "SELECT * FROM internal_keys ORDER BY user_email"
            ).fetchall()
        return {
            row["user_email"]: {
                "key": row["secret"],
                "created_at": row["created_at"],
                "status": row["status"],
            }
            for row in rows
        }

    def write_internal_keys(self, users):
        with self._exclusive():
            with self._connect() as connection:
                self._replace_internal_keys(connection, users)

    def read_settings(self):
        with self._connect() as connection:
            rows = connection.execute("SELECT key, value_json FROM settings").fetchall()
        return {row["key"]: json.loads(row["value_json"]) for row in rows}

    def write_settings(self, values):
        now = int(time.time())
        with self._exclusive():
            with self._connect() as connection:
                connection.execute("DELETE FROM settings")
                connection.executemany(
                    "INSERT INTO settings(key, value_json, updated_at) VALUES (?, ?, ?)",
                    [
                        (
                            str(key),
                            json.dumps(value, ensure_ascii=False, separators=(",", ":")),
                            now,
                        )
                        for key, value in values.items()
                    ],
                )

    def read_runtime_state(self, name, default=None):
        with self._connect() as connection:
            row = connection.execute(
                "SELECT payload_json FROM runtime_state WHERE name = ?", (str(name),)
            ).fetchone()
        return json.loads(row["payload_json"]) if row else default

    def write_runtime_state(self, name, payload):
        now = int(time.time())
        with self._exclusive():
            with self._connect() as connection:
                connection.execute(
                    """
                    INSERT INTO runtime_state(name, payload_json, updated_at)
                    VALUES (?, ?, ?)
                    ON CONFLICT(name) DO UPDATE SET
                        payload_json = excluded.payload_json,
                        updated_at = excluded.updated_at
                    """,
                    (
                        str(name),
                        json.dumps(payload, ensure_ascii=False, separators=(",", ":")),
                        now,
                    ),
                )

    def delete_runtime_state(self, name):
        with self._exclusive():
            with self._connect() as connection:
                connection.execute(
                    "DELETE FROM runtime_state WHERE name = ?", (str(name),)
                )

    def branding_asset(self, name="logo"):
        with self._connect() as connection:
            row = connection.execute(
                "SELECT * FROM branding_assets WHERE name = ?", (str(name),)
            ).fetchone()
        if not row:
            return None
        return {
            "name": row["name"],
            "filename": row["filename"],
            "content_type": row["content_type"],
            "content": bytes(row["content"]),
            "sha256": row["sha256"],
            "updated_at": row["updated_at"],
        }

    def write_branding_asset(self, filename, content_type, content, name="logo"):
        content = bytes(content)
        now = int(time.time())
        digest = hashlib.sha256(content).hexdigest()
        with self._connect() as connection:
            connection.execute(
                """
                INSERT INTO branding_assets(
                    name, filename, content_type, content, sha256, updated_at
                ) VALUES (?, ?, ?, ?, ?, ?)
                ON CONFLICT(name) DO UPDATE SET
                    filename = excluded.filename,
                    content_type = excluded.content_type,
                    content = excluded.content,
                    sha256 = excluded.sha256,
                    updated_at = excluded.updated_at
                """,
                (str(name), str(filename), str(content_type), content, digest, now),
            )
        return self.branding_asset(name)

    def delete_branding_asset(self, name="logo"):
        with self._connect() as connection:
            connection.execute("DELETE FROM branding_assets WHERE name = ?", (str(name),))

    def backup_to(self, destination):
        destination = Path(destination).resolve()
        destination.parent.mkdir(parents=True, exist_ok=True)
        temporary = destination.with_name(".{}.{}.tmp".format(destination.name, os.getpid()))
        try:
            with self._connect() as source, sqlite3.connect(str(temporary)) as target:
                source.backup(target)
            os.chmod(temporary, 0o600)
            os.replace(temporary, destination)
            os.chmod(destination, 0o600)
        finally:
            try:
                temporary.unlink()
            except FileNotFoundError:
                pass
        return destination

    def _verification_details(self, check_obsolete=True):
        errors = []
        integrity = []
        available_tables = set()
        schema_version = None
        encrypted_rows = []
        try:
            with self._connect() as connection:
                integrity = [
                    str(row[0])
                    for row in connection.execute("PRAGMA integrity_check").fetchall()
                ]
                if integrity != ["ok"]:
                    errors.append("控制面数据库完整性检查失败")
                available_tables = {
                    str(row[0])
                    for row in connection.execute(
                        "SELECT name FROM sqlite_master WHERE type = 'table'"
                    ).fetchall()
                }
                missing_tables = sorted(REQUIRED_TABLES - available_tables)
                if missing_tables:
                    errors.append("控制面数据库缺少表：{}".format(", ".join(missing_tables)))
                if "schema_migrations" in available_tables:
                    row = connection.execute(
                        "SELECT MAX(version) FROM schema_migrations"
                    ).fetchone()
                    schema_version = int(row[0]) if row and row[0] is not None else None
                    if schema_version != SCHEMA_VERSION:
                        errors.append(
                            "控制面数据库版本不匹配：实际 {}，期望 {}".format(
                                schema_version, SCHEMA_VERSION
                            )
                        )
                if "encrypted_secrets" in available_tables:
                    encrypted_rows = connection.execute(
                        "SELECT * FROM encrypted_secrets ORDER BY name"
                    ).fetchall()
        except (OSError, sqlite3.DatabaseError) as error:
            errors.append("控制面数据库不可读：{}".format(error))

        try:
            database_mode = self.path.stat().st_mode & 0o777
        except OSError as error:
            database_mode = None
            errors.append("控制面数据库文件不可访问：{}".format(error))
        else:
            if database_mode != 0o600:
                errors.append("控制面数据库权限必须为 0600")

        try:
            key = self.encryption_key_path.read_bytes()
            key_mode = self.encryption_key_path.stat().st_mode & 0o777
            if len(key) != 32:
                errors.append("控制面加密主密钥必须正好为 32 字节")
            if key_mode != 0o600:
                errors.append("控制面加密主密钥权限必须为 0600")
        except OSError as error:
            key_mode = None
            errors.append("控制面加密主密钥不可访问：{}".format(error))

        decryptable = []
        for row in encrypted_rows:
            try:
                self._decrypt_secret_row(row)
            except ValueError as error:
                errors.append(str(error))
            else:
                decryptable.append(str(row["name"]))

        obsolete = sorted(
            relative
            for relative in OBSOLETE_PROJECTION_PATHS
            if (self.root / relative).exists() or (self.root / relative).is_symlink()
        )
        if check_obsolete and obsolete:
            errors.append("仍存在已废弃的控制面 JSON：{}".format(", ".join(obsolete)))
        return {
            "errors": errors,
            "integrity": integrity,
            "schema_version": schema_version,
            "required_schema_version": SCHEMA_VERSION,
            "database_mode": database_mode,
            "key_mode": key_mode,
            "tables": sorted(available_tables),
            "encrypted": [str(row["name"]) for row in encrypted_rows],
            "decryptable": decryptable,
            "obsolete_projections": obsolete,
        }

    def verify(self):
        details = self._verification_details(check_obsolete=True)
        return {
            "ok": not details["errors"],
            "database": str(self.path),
            **details,
        }

    def cleanup_obsolete_projections(self):
        """Remove only the retired control-plane JSON files after DB validation."""
        with self._exclusive():
            verification = self._verification_details(check_obsolete=False)
            if verification["errors"]:
                raise ValueError(
                    "控制面数据库验证失败，拒绝清理旧投影：{}".format(
                        "; ".join(verification["errors"])
                    )
                )
            candidates = [
                (relative, self.root / relative)
                for relative in OBSOLETE_PROJECTION_PATHS
                if (self.root / relative).exists()
                or (self.root / relative).is_symlink()
            ]
            for unused_relative, path in candidates:
                if path.is_dir() and not path.is_symlink():
                    raise ValueError("旧投影路径不是文件，拒绝删除：{}".format(path))

            with self._connect() as connection:
                migration_evidence = connection.execute(
                    "SELECT key FROM metadata WHERE key IN (?, ?)",
                    ("legacy_import_complete", PROJECTION_CLEANUP_METADATA_KEY),
                ).fetchone()
                legacy_hashes = {
                    str(row["key"])[len("legacy_hash:") :]: str(row["value"])
                    for row in connection.execute(
                        "SELECT key, value FROM metadata WHERE key LIKE 'legacy_hash:%'"
                    ).fetchall()
                }
            if candidates and migration_evidence is None:
                raise ValueError(
                    "数据库没有旧 JSON 导入/清理记录，拒绝删除；请先确认权威数据已迁移"
                )
            changed = []
            for relative, path in candidates:
                try:
                    actual = hashlib.sha256(path.read_bytes()).hexdigest()
                except OSError:
                    actual = None
                if legacy_hashes.get(relative) != actual:
                    changed.append(relative)
            if changed:
                raise ValueError(
                    "旧 JSON 在最后一次数据库同步后发生变化，拒绝删除：{}".format(
                        ", ".join(changed)
                    )
                )

            cleaned = []
            for relative, path in candidates:
                path.unlink()
                cleaned.append(relative)
            with self._connect() as connection:
                cursor = connection.execute(
                    "DELETE FROM metadata "
                    "WHERE key = 'legacy_import_complete' OR key LIKE 'legacy_hash:%'"
                )
                metadata_cleaned = max(0, int(cursor.rowcount))
                connection.execute(
                    """
                    INSERT INTO metadata(key, value) VALUES (?, ?)
                    ON CONFLICT(key) DO UPDATE SET value = excluded.value
                    """,
                    (PROJECTION_CLEANUP_METADATA_KEY, str(int(time.time()))),
                )
        return {
            "cleaned": sorted(cleaned),
            "metadata_cleaned": metadata_cleaned,
            "remaining": sorted(
                relative
                for relative in OBSOLETE_PROJECTION_PATHS
                if (self.root / relative).exists() or (self.root / relative).is_symlink()
            ),
        }
