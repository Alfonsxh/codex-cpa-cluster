#!/usr/bin/env python3
"""Create and compare secret-free production data manifests around a release."""

import argparse
import hashlib
import ipaddress
import json
import os
import re
import sqlite3
from pathlib import Path


VERSION = 5
CONTROL_TABLES = {
    "settings": "SELECT key, value_json FROM settings ORDER BY key",
    "accounts": "SELECT * FROM accounts ORDER BY position, id",
    "user_routes": "SELECT * FROM user_routes ORDER BY user_email",
    "key_records": "SELECT * FROM key_records ORDER BY sequence",
    "internal_keys": "SELECT * FROM internal_keys ORDER BY user_email",
    "teams": "SELECT * FROM teams ORDER BY id",
    "user_team_memberships": (
        "SELECT * FROM user_team_memberships ORDER BY user_email"
    ),
    "tags": "SELECT * FROM tags ORDER BY id",
    "user_tags": "SELECT * FROM user_tags ORDER BY user_email, tag_id",
}
PRESERVED_TREES = (
    "auth",
    "configs",
    "management/auth",
    "management/config",
    "management/plugins",
)
MIGRATED_SECRET_FILES = {
    "cpa_management_key": "secrets/cpa-management.key",
    "wecom_webhook": "secrets/wecom-webhook.url",
}
MIGRATION_ONLY_SECRET_FILES = {
    *MIGRATED_SECRET_FILES.values(),
    "secrets/user-internal-keys.json",
    "secrets/issued-keys.tsv",
    "secrets/deployment-profile.json",
    "secrets/control-plane.key",
}
RETIRED_SETTING_KEYS = {
    "gost.enabled",
    "gost.remote_hosts",
    "gost.remote_host",
    "gost.port_start",
    "gost.port_end",
    "runtime.gost_image",
}
LEGACY_ENV_SETTING_KEYS = {
    "runtime.cliproxy_image",
    "runtime.gateway_image",
    "runtime.admin_base_image",
    "accounts.listen_address",
    "management.listen_address",
    "gateway.listen_address",
    "gateway.port",
    "gateway.internal_port",
    "management.port",
    "delivery.gateway_drain_timeout_seconds",
    "delivery.release_metadata_image",
}
RETIRED_SECRET_NAMES = {"gost_tunnel_auth"}


def _valid_legacy_setting_value(key, value_json):
    try:
        value = json.loads(value_json)
    except (TypeError, ValueError):
        return False
    if key in {
        "runtime.cliproxy_image",
        "runtime.gateway_image",
        "runtime.admin_base_image",
        "delivery.release_metadata_image",
    }:
        if not isinstance(value, str) or len(value) > 255:
            return False
        if key == "delivery.release_metadata_image" and not value:
            return True
        if not value or not re.fullmatch(r"[A-Za-z0-9._:/@-]+", value):
            return False
        if key in {"runtime.gateway_image", "runtime.admin_base_image"}:
            return bool(
                re.fullmatch(r"[A-Za-z0-9._:/-]+@sha256:[0-9a-f]{64}", value)
            )
        return True
    if key in {
        "accounts.listen_address",
        "management.listen_address",
        "gateway.listen_address",
    }:
        try:
            address = ipaddress.ip_address(value)
        except ValueError:
            return False
        return address.version == 4 and address.is_loopback
    if key in {"gateway.port", "gateway.internal_port", "management.port"}:
        return isinstance(value, int) and not isinstance(value, bool) and 1024 <= value <= 65535
    if key == "delivery.gateway_drain_timeout_seconds":
        return isinstance(value, int) and not isinstance(value, bool) and 30 <= value <= 7200
    return False


def _digest(payload):
    raw = json.dumps(
        payload,
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    return hashlib.sha256(raw).hexdigest()


def _file_digest(path):
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        while True:
            chunk = handle.read(1024 * 1024)
            if not chunk:
                break
            digest.update(chunk)
    return digest.hexdigest()


def _integrity(connection):
    rows = connection.execute("PRAGMA integrity_check").fetchall()
    return [str(row[0]) for row in rows]


def _control_snapshot(path):
    if not path.is_file():
        return {"present": False}
    with sqlite3.connect(str(path), timeout=30) as connection:
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA busy_timeout = 30000")
        integrity = _integrity(connection)
        available_tables = {
            row[0]
            for row in connection.execute(
                "SELECT name FROM sqlite_master WHERE type = 'table'"
            ).fetchall()
        }
        tables = {}
        for name, query in CONTROL_TABLES.items():
            rows = (
                [dict(row) for row in connection.execute(query).fetchall()]
                if name in available_tables
                else []
            )
            tables[name] = {"count": len(rows), "sha256": _digest(rows)}
            if name in ("settings", "accounts"):
                tables[name]["rows"] = rows
            if name == "user_routes":
                tables[name]["row_key_sha256"] = sorted(
                    _digest({"user_email": row.get("user_email")}) for row in rows
                )
                tables[name]["account_ids"] = sorted(
                    {
                        str(row.get("account_id"))
                        for row in rows
                        if row.get("account_id") is not None
                    }
                )
            if name == "internal_keys":
                tables[name]["row_sha256"] = sorted(_digest(row) for row in rows)
    return {
        "present": True,
        "integrity": integrity,
        "tables": tables,
    }


def _logical_secrets(root):
    result = {}
    database = root / "state" / "control-plane.sqlite3"
    if database.is_file():
        with sqlite3.connect(str(database), timeout=30) as connection:
            tables = {
                row[0]
                for row in connection.execute(
                    "SELECT name FROM sqlite_master WHERE type = 'table'"
                ).fetchall()
            }
            if "encrypted_secrets" in tables:
                for name, digest in connection.execute(
                    "SELECT name, value_sha256 FROM encrypted_secrets"
                ).fetchall():
                    result[str(name)] = str(digest)
    for name, relative in MIGRATED_SECRET_FILES.items():
        if name in result:
            continue
        path = root / relative
        if path.is_file():
            value = path.read_text(encoding="utf-8").strip()
            if value:
                result[name] = hashlib.sha256(value.encode("utf-8")).hexdigest()
    return result


def _encrypted_secret_count(root):
    database = root / "state" / "control-plane.sqlite3"
    if not database.is_file():
        return 0
    with sqlite3.connect(str(database), timeout=30) as connection:
        table = connection.execute(
            "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'encrypted_secrets'"
        ).fetchone()
        if table is None:
            return 0
        return int(connection.execute("SELECT COUNT(*) FROM encrypted_secrets").fetchone()[0])


def _preserved_secret_files(root):
    files = _relative_files(root, "secrets")
    return {
        name: metadata
        for name, metadata in files.items()
        if name not in MIGRATION_ONLY_SECRET_FILES
    }


def _usage_snapshot(path):
    if not path.is_file():
        return {"present": False}
    with sqlite3.connect(str(path), timeout=30) as connection:
        connection.execute("PRAGMA busy_timeout = 30000")
        integrity = _integrity(connection)
        user_version = int(connection.execute("PRAGMA user_version").fetchone()[0])
        tables = {
            row[0]
            for row in connection.execute(
                "SELECT name FROM sqlite_master WHERE type = 'table'"
            ).fetchall()
        }
        usage_events = {"count": 0, "max_id": 0, "total_tokens": 0}
        if "usage_events" in tables:
            row = connection.execute(
                """
                SELECT COUNT(*), COALESCE(MAX(id), 0),
                       COALESCE(SUM(total_tokens), 0)
                FROM usage_events
                """
            ).fetchone()
            usage_events = {
                "count": int(row[0]),
                "max_id": int(row[1]),
                "total_tokens": int(row[2]),
            }
    return {
        "present": True,
        "integrity": integrity,
        "user_version": user_version,
        "usage_events": usage_events,
    }


def _relative_files(root, relative):
    path = root / relative
    if not path.exists():
        return {}
    if path.is_file() and not path.is_symlink():
        return {
            relative: {
                "size": path.stat().st_size,
                "sha256": _file_digest(path),
            }
        }
    files = {}
    for candidate in sorted(path.rglob("*")):
        if not candidate.is_file() or candidate.is_symlink():
            continue
        name = str(candidate.relative_to(root))
        files[name] = {
            "size": candidate.stat().st_size,
            "sha256": _file_digest(candidate),
        }
    return files


def snapshot(root):
    root = Path(root).resolve()
    preserved = {}
    for relative in PRESERVED_TREES:
        preserved.update(_relative_files(root, relative))
    preserved.update(_preserved_secret_files(root))
    master_key = root / "secrets" / "control-plane.key"
    return {
        "version": VERSION,
        "control": _control_snapshot(root / "state" / "control-plane.sqlite3"),
        "usage": _usage_snapshot(root / "state" / "usage.sqlite3"),
        "preserved_files": preserved,
        "logical_secrets": _logical_secrets(root),
        "encrypted_secret_count": _encrypted_secret_count(root),
        "master_key": (
            {"present": True, "sha256": _file_digest(master_key)}
            if master_key.is_file()
            else {"present": False}
        ),
    }


def _allowed_settings_migrations(old, new):
    old_rows = {
        str(row.get("key")): row.get("value_json")
        for row in (old or {}).get("rows", ())
    }
    new_rows = {
        str(row.get("key")): row.get("value_json")
        for row in (new or {}).get("rows", ())
    }
    added = set(new_rows) - set(old_rows)
    removed = set(old_rows) - set(new_rows)
    if not added.issubset(LEGACY_ENV_SETTING_KEYS) or not removed.issubset(
        RETIRED_SETTING_KEYS
    ):
        return False, False, False
    changed = {
        key
        for key in set(old_rows) & set(new_rows)
        if old_rows[key] != new_rows[key]
    }
    allowed = {
        "gateway.listen_address",
        "portal.session_ttl_seconds",
        "gateway.port",
        "gateway.internal_port",
        "delivery.release_metadata_image",
    }
    if not changed.issubset(allowed) or not (added or changed or removed):
        return False, False, False
    if any(not _valid_legacy_setting_value(key, new_rows[key]) for key in added):
        return False, False, False
    for key in changed:
        try:
            previous = json.loads(old_rows[key])
            current = json.loads(new_rows[key])
        except (TypeError, ValueError):
            return False, False, False
        if key == "gateway.listen_address":
            if previous not in ("0.0.0.0", "::") or current != "127.0.0.1":
                return False, False, False
        elif key == "portal.session_ttl_seconds":
            try:
                if int(previous) <= 43200 or int(current) != 43200:
                    return False, False, False
            except (TypeError, ValueError):
                return False, False, False
        elif key in ("gateway.port", "gateway.internal_port"):
            try:
                if not 1024 <= int(current) <= 65535:
                    return False, False, False
            except (TypeError, ValueError):
                return False, False, False
        elif key == "delivery.release_metadata_image":
            if not isinstance(current, str) or not current:
                return False, False, False
    security_changes = changed & {
        "gateway.listen_address",
        "portal.session_ttl_seconds",
    }
    return True, bool(security_changes), bool(removed)


def _allowed_account_proxy_mode_migration(old, new):
    old_rows = (old or {}).get("rows", ())
    new_rows = (new or {}).get("rows", ())
    if len(old_rows) != len(new_rows) or not old_rows:
        return False
    old_by_id = {str(row.get("id")): row for row in old_rows}
    new_by_id = {str(row.get("id")): row for row in new_rows}
    if set(old_by_id) != set(new_by_id):
        return False
    for account, previous in old_by_id.items():
        current = new_by_id[account]
        if set(current) - set(previous) != {"proxy_mode"}:
            return False
        if any(current.get(key) != value for key, value in previous.items()):
            return False
        if current.get("proxy_mode") != "inherit":
            return False
    return True


def _allowed_retired_account_state_migration(old, new):
    old_rows = (old or {}).get("rows", ())
    new_rows = (new or {}).get("rows", ())
    if len(old_rows) != len(new_rows) or not old_rows:
        return False
    old_by_id = {str(row.get("id")): row for row in old_rows}
    new_by_id = {str(row.get("id")): row for row in new_rows}
    if set(old_by_id) != set(new_by_id):
        return False
    for account, previous in old_by_id.items():
        current = new_by_id[account]
        removed = set(previous) - set(current)
        added = set(current) - set(previous)
        if removed != {"gost_port"} or not added.issubset({"proxy_mode"}):
            return False
        if any(
            current.get(key) != previous.get(key)
            for key in set(previous) & set(current)
        ):
            return False
        if "proxy_mode" in added and current.get("proxy_mode") != "inherit":
            return False
    return True


def _allowed_live_account_policy_change(old, new):
    old_rows = (old or {}).get("rows", ())
    new_rows = (new or {}).get("rows", ())
    if len(old_rows) != len(new_rows) or not old_rows:
        return False
    old_by_id = {str(row.get("id")): row for row in old_rows}
    new_by_id = {str(row.get("id")): row for row in new_rows}
    if set(old_by_id) != set(new_by_id):
        return False
    changed = False
    mutable = {"group_enabled", "default_group"}
    for account, previous in old_by_id.items():
        current = new_by_id[account]
        if set(previous) != set(current):
            return False
        if any(
            current.get(key) != value
            for key, value in previous.items()
            if key not in mutable
        ):
            return False
        for key in mutable:
            value = current.get(key)
            if value not in (0, 1, False, True):
                return False
            changed = changed or value != previous.get(key)
    enabled_defaults = [
        row
        for row in new_rows
        if bool(row.get("default_group")) and bool(row.get("group_enabled"))
    ]
    invalid_defaults = [
        row
        for row in new_rows
        if bool(row.get("default_group")) and not bool(row.get("group_enabled"))
    ]
    return changed and len(enabled_defaults) <= 1 and not invalid_defaults


def compare(before, after):
    errors = []
    changes = []
    if before.get("version") != VERSION or after.get("version") != VERSION:
        errors.append("unsupported manifest version")

    before_control = before.get("control", {})
    after_control = after.get("control", {})
    if before_control.get("present") != after_control.get("present"):
        errors.append("control-plane database presence changed")
    if after_control.get("present") and after_control.get("integrity") != ["ok"]:
        errors.append("control-plane database integrity check failed")
    security_settings_migrated = False
    retired_settings_migrated = False
    account_proxy_mode_migrated = False
    retired_account_state_migrated = False
    account_policy_changed = False
    for name in (
        "settings",
        "accounts",
        "key_records",
        "teams",
        "user_team_memberships",
        "tags",
        "user_tags",
    ):
        old = before_control.get("tables", {}).get(name)
        new = after_control.get("tables", {}).get(name)
        if old != new:
            if name == "settings":
                allowed, security_changed, retired_removed = (
                    _allowed_settings_migrations(old, new)
                )
                if allowed:
                    security_settings_migrated = security_changed
                    retired_settings_migrated = retired_removed
                else:
                    errors.append("control-plane table changed: {}".format(name))
            elif name == "accounts" and _allowed_account_proxy_mode_migration(old, new):
                account_proxy_mode_migrated = True
            elif name == "accounts" and _allowed_retired_account_state_migration(old, new):
                retired_account_state_migrated = True
            elif name == "accounts" and _allowed_live_account_policy_change(old, new):
                account_policy_changed = True
            else:
                errors.append("control-plane table changed: {}".format(name))
    old_routes = before_control.get("tables", {}).get("user_routes", {})
    new_routes = after_control.get("tables", {}).get("user_routes", {})
    user_routes_changed = old_routes != new_routes
    if (
        int(old_routes.get("count", 0)) != int(new_routes.get("count", 0))
        or old_routes.get("row_key_sha256", ())
        != new_routes.get("row_key_sha256", ())
    ):
        errors.append("control-plane route users changed: user_routes")
    valid_account_ids = {
        str(row.get("id"))
        for row in after_control.get("tables", {})
        .get("accounts", {})
        .get("rows", ())
        if row.get("id") is not None
    }
    invalid_route_accounts = sorted(
        set(new_routes.get("account_ids", ())) - valid_account_ids
    )
    if invalid_route_accounts:
        errors.append("control-plane routes reference unknown accounts")
    old_internal = before_control.get("tables", {}).get("internal_keys", {})
    new_internal = after_control.get("tables", {}).get("internal_keys", {})
    missing_internal = sorted(
        set(old_internal.get("row_sha256", ()))
        - set(new_internal.get("row_sha256", ()))
    )
    if (
        int(new_internal.get("count", 0)) < int(old_internal.get("count", 0))
        or missing_internal
    ):
        errors.append("control-plane table lost or changed rows: internal_keys")

    before_usage = before.get("usage", {})
    after_usage = after.get("usage", {})
    if before_usage.get("present") != after_usage.get("present"):
        errors.append("usage database presence changed")
    if after_usage.get("present"):
        if after_usage.get("integrity") != ["ok"]:
            errors.append("usage database integrity check failed")
        old_events = before_usage.get("usage_events", {})
        new_events = after_usage.get("usage_events", {})
        for field in ("count", "max_id", "total_tokens"):
            if int(new_events.get(field, 0)) < int(old_events.get(field, 0)):
                errors.append("usage event metric decreased: {}".format(field))

    old_files = before.get("preserved_files", {})
    new_files = after.get("preserved_files", {})
    missing = sorted(set(old_files) - set(new_files))
    if missing:
        errors.append("preserved runtime files disappeared: {}".format(", ".join(missing)))
    for name in sorted(set(old_files) & set(new_files)):
        if old_files[name] != new_files[name]:
            changes.append(name)

    old_secrets = before.get("logical_secrets", {})
    new_secrets = after.get("logical_secrets", {})
    retired_secrets_migrated = False
    for name, digest in old_secrets.items():
        if new_secrets.get(name) != digest:
            if name in RETIRED_SECRET_NAMES and name not in new_secrets:
                retired_secrets_migrated = True
            else:
                errors.append("logical secret changed or disappeared: {}".format(name))

    old_master = before.get("master_key", {"present": False})
    new_master = after.get("master_key", {"present": False})
    if old_master.get("present") and old_master != new_master:
        errors.append("control-plane encryption key changed or disappeared")
    if int(after.get("encrypted_secret_count", 0)) and not new_master.get("present"):
        errors.append("encrypted control-plane secrets exist without a master key")

    return {
        "ok": not errors,
        "errors": errors,
        "changed_preserved_files": changes,
        "control_counts": {
            name: after_control.get("tables", {}).get(name, {}).get("count", 0)
            for name in CONTROL_TABLES
        },
        "usage_events": after_usage.get("usage_events", {}),
        "security_settings_migrated": security_settings_migrated,
        "retired_settings_migrated": retired_settings_migrated,
        "account_proxy_mode_migrated": account_proxy_mode_migrated,
        "retired_account_state_migrated": retired_account_state_migrated,
        "account_policy_changed": account_policy_changed,
        "retired_secrets_migrated": retired_secrets_migrated,
        "user_routes_changed": user_routes_changed,
    }


def _write_json(path, payload):
    path = Path(path).resolve()
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(".{}.{}.tmp".format(path.name, os.getpid()))
    temporary.write_text(
        json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    os.chmod(temporary, 0o600)
    os.replace(temporary, path)
    os.chmod(path, 0o600)


def main(argv=None):
    parser = argparse.ArgumentParser(description="Production data continuity guard")
    subparsers = parser.add_subparsers(dest="command", required=True)
    snapshot_parser = subparsers.add_parser("snapshot")
    snapshot_parser.add_argument("root")
    snapshot_parser.add_argument("output")
    compare_parser = subparsers.add_parser("compare")
    compare_parser.add_argument("before")
    compare_parser.add_argument("after")
    args = parser.parse_args(argv)

    if args.command == "snapshot":
        payload = snapshot(args.root)
        _write_json(args.output, payload)
        print(
            json.dumps(
                {
                    "control_counts": {
                        name: payload.get("control", {})
                        .get("tables", {})
                        .get(name, {})
                        .get("count", 0)
                        for name in CONTROL_TABLES
                    },
                    "usage_events": payload.get("usage", {}).get("usage_events", {}),
                    "preserved_files": len(payload.get("preserved_files", {})),
                },
                ensure_ascii=False,
                sort_keys=True,
            )
        )
        return 0

    before = json.loads(Path(args.before).read_text(encoding="utf-8"))
    after = json.loads(Path(args.after).read_text(encoding="utf-8"))
    result = compare(before, after)
    print(json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True))
    return 0 if result["ok"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
