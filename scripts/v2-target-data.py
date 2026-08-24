#!/usr/bin/env python3
"""Prepare, migrate, and permission an existing target for a Go v2 cutover.

All reports are secret-free. SQLite files are copied through the online backup
API; raw API Keys, OAuth payloads, the control-plane master key, and Webhooks
are never printed.
"""

import argparse
import hashlib
import importlib
import json
import os
import shutil
import sqlite3
import stat
import sys
import time
from pathlib import Path


ISOLATED_MARKER = ".v2-isolated-copy.json"
RUNTIME_TREES = ("auth", "configs", "logs", "management", "secrets", "state")
RUNTIME_FILES = (".env", "compose.accounts.yml")
STATE_DATABASE_NAMES = {
    "control-plane.sqlite3",
    "control-plane.sqlite3-shm",
    "control-plane.sqlite3-wal",
    "usage.sqlite3",
    "usage.sqlite3-shm",
    "usage.sqlite3-wal",
}
STATE_VOLATILE_NAMES = {"compose-env.lock", "runtime-operation.lock"}
SNAPSHOT_GROUP_ID = 65534


def _absolute(value):
    return Path(value).expanduser().resolve()


def _required_target(root):
    required = (
        root / "state" / "control-plane.sqlite3",
        root / "state" / "usage.sqlite3",
        root / "secrets" / "control-plane.key",
    )
    missing = [str(path) for path in required if not path.is_file()]
    if missing:
        raise RuntimeError("existing CPA target is incomplete: {}".format(", ".join(missing)))


def _reject_symlinks(root):
    for tree_name in RUNTIME_TREES:
        tree = root / tree_name
        if not tree.exists():
            continue
        if tree.is_symlink():
            raise RuntimeError("runtime tree cannot be a symlink: {}".format(tree))
        for current, directories, files in os.walk(tree, followlinks=False):
            for name in directories + files:
                path = Path(current) / name
                if path.is_symlink():
                    raise RuntimeError("runtime copy refuses symlink: {}".format(path))


def _ignore_runtime_state(directory, names):
    if Path(directory).name != "state":
        return []
    return sorted((STATE_DATABASE_NAMES | STATE_VOLATILE_NAMES).intersection(names))


def _online_backup(source, destination):
    destination.parent.mkdir(parents=True, exist_ok=True)
    source_uri = "file:{}?mode=ro".format(source.as_posix())
    with sqlite3.connect(source_uri, uri=True, timeout=30) as source_db:
        source_db.execute("PRAGMA busy_timeout = 30000")
        with sqlite3.connect(str(destination), timeout=30) as target_db:
            source_db.backup(target_db)
    os.chmod(destination, 0o600)


def _integrity(path):
    uri = "file:{}?mode=ro".format(path.as_posix())
    with sqlite3.connect(uri, uri=True, timeout=30) as connection:
        connection.execute("PRAGMA busy_timeout = 30000")
        return [str(row[0]) for row in connection.execute("PRAGMA integrity_check")]


def _usage_summary(path):
    uri = "file:{}?mode=ro".format(path.as_posix())
    with sqlite3.connect(uri, uri=True, timeout=30) as connection:
        connection.execute("PRAGMA busy_timeout = 30000")
        version = int(connection.execute("PRAGMA user_version").fetchone()[0])
        tables = {
            str(row[0])
            for row in connection.execute(
                "SELECT name FROM sqlite_master WHERE type = 'table'"
            )
        }
        result = {"user_version": version, "integrity": _integrity(path)}
        for table in (
            "usage_events",
            "key_identities",
            "portal_credentials",
            "portal_sessions",
            "user_quota_policies",
            "user_quota_adjustments",
            "user_weekly_usage",
        ):
            result[table] = (
                int(connection.execute("SELECT COUNT(*) FROM " + table).fetchone()[0])
                if table in tables
                else 0
            )
        if "usage_events" in tables:
            row = connection.execute(
                "SELECT COALESCE(MAX(id), 0), COALESCE(SUM(total_tokens), 0) FROM usage_events"
            ).fetchone()
            result["usage_event_max_id"] = int(row[0])
            result["usage_event_total_tokens"] = int(row[1])
        return result


def _control_key_digest(path):
    uri = "file:{}?mode=ro".format(path.as_posix())
    digest = hashlib.sha256()
    count = 0
    with sqlite3.connect(uri, uri=True, timeout=30) as connection:
        connection.execute("PRAGMA busy_timeout = 30000")
        for row in connection.execute(
            "SELECT label, account_id, account_email, user_email, status, secret "
            "FROM key_records ORDER BY sequence"
        ):
            digest.update(json.dumps(row, ensure_ascii=False, separators=(",", ":")).encode("utf-8"))
            digest.update(b"\n")
            count += 1
    return {"records": count, "sha256": digest.hexdigest()}


def _write_json(path, payload):
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
        json.dump(payload, handle, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
        handle.write("\n")


def snapshot_runtime(source_root, target_root, confirmation):
    source = _absolute(source_root)
    target = _absolute(target_root)
    if confirmation != str(source):
        raise RuntimeError("--confirm-source-root must exactly match the absolute source root")
    if source == target or source in target.parents or target in source.parents:
        raise RuntimeError("source and target runtime roots must be separate sibling trees")
    _required_target(source)
    _reject_symlinks(source)
    if target.exists():
        raise RuntimeError("refusing to overwrite existing snapshot target: {}".format(target))
    target.mkdir(mode=0o700, parents=True)
    try:
        for name in RUNTIME_TREES:
            source_path = source / name
            if not source_path.exists():
                continue
            shutil.copytree(
                source_path,
                target / name,
                copy_function=shutil.copy2,
                ignore=_ignore_runtime_state,
            )
        for name in RUNTIME_FILES:
            source_path = source / name
            if source_path.is_file():
                shutil.copy2(source_path, target / name)
        target_state = target / "state"
        _online_backup(
            source / "state" / "control-plane.sqlite3",
            target_state / "control-plane.sqlite3",
        )
        _online_backup(
            source / "state" / "usage.sqlite3",
            target_state / "usage.sqlite3",
        )
        marker = {
            "version": 1,
            "created_at": int(time.time()),
            "source_root_sha256": hashlib.sha256(str(source).encode("utf-8")).hexdigest(),
        }
        _write_json(target / ISOLATED_MARKER, marker)
        _required_target(target)
    except BaseException:
        shutil.rmtree(target, ignore_errors=True)
        raise
    return {
        "target": str(target),
        "control_integrity": _integrity(target / "state" / "control-plane.sqlite3"),
        "usage": _usage_summary(target / "state" / "usage.sqlite3"),
        "keys": _control_key_digest(target / "state" / "control-plane.sqlite3"),
    }


def _read_control_setting(root, key, default):
    path = root / "state" / "control-plane.sqlite3"
    uri = "file:{}?mode=ro".format(path.as_posix())
    with sqlite3.connect(uri, uri=True, timeout=30) as connection:
        row = connection.execute(
            "SELECT value_json FROM settings WHERE key = ?",
            (key,),
        ).fetchone()
    if row is None:
        return default
    try:
        return json.loads(row[0])
    except (TypeError, ValueError):
        raise RuntimeError("control setting is not valid JSON: {}".format(key))


def _confirm_mutable_root(root, isolated, live_confirmation):
    marker = root / ISOLATED_MARKER
    if isolated:
        if live_confirmation:
            raise RuntimeError("isolated-copy and live-cutover confirmations are mutually exclusive")
        if not marker.is_file():
            raise RuntimeError("isolated migration requires the snapshot marker")
        return "isolated-copy"
    expected = "live-cutover:" + str(root)
    if live_confirmation != expected:
        raise RuntimeError("live mutation requires --confirm-live-cutover {}".format(expected))
    return "live-cutover"


def migrate_usage(root_value, module_root_value, backup_dir_value, isolated, live_confirmation):
    root = _absolute(root_value)
    module_root = _absolute(module_root_value)
    backup_dir = _absolute(backup_dir_value)
    _required_target(root)
    mode = _confirm_mutable_root(root, isolated, live_confirmation)
    if not (module_root / "admin" / "usage_store.py").is_file():
        raise RuntimeError("usage migration module is missing from {}".format(module_root))
    before = _usage_summary(root / "state" / "usage.sqlite3")
    keys_before = _control_key_digest(root / "state" / "control-plane.sqlite3")
    backup_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    backup = backup_dir / "usage-pre-v10-{}.sqlite3".format(int(time.time()))
    if backup.exists():
        raise RuntimeError("usage migration backup already exists: {}".format(backup))
    _online_backup(root / "state" / "usage.sqlite3", backup)

    sys.path.insert(0, str(module_root))
    try:
        usage_module = importlib.import_module("admin.usage_store")
        timezone_name = _read_control_setting(root, "user_quota.timezone", "Asia/Shanghai")
        reset_weekly = bool(
            _read_control_setting(
                root,
                "user_quota.reset_personal_weekly_on_new_week",
                True,
            )
        )
        usage_module.UsageStore(
            root / "state" / "usage.sqlite3",
            week_timezone=timezone_name,
            reset_personal_weekly_on_new_week=reset_weekly,
        )
    finally:
        sys.path.pop(0)

    after = _usage_summary(root / "state" / "usage.sqlite3")
    keys_after = _control_key_digest(root / "state" / "control-plane.sqlite3")
    if after["user_version"] != int(usage_module.SCHEMA_VERSION):
        raise RuntimeError("usage schema migration did not reach the required version")
    if before["usage_events"] != after["usage_events"]:
        raise RuntimeError("usage event count changed during schema migration")
    if before.get("usage_event_total_tokens") != after.get("usage_event_total_tokens"):
        raise RuntimeError("historical usage Token total changed during schema migration")
    if keys_before != keys_after:
        raise RuntimeError("API Key bytes changed during usage schema migration")
    return {
        "mode": mode,
        "root": str(root),
        "backup": str(backup),
        "before": before,
        "after": after,
        "keys": keys_after,
    }


def prepare_permissions(root_value, isolated, live_confirmation):
    root = _absolute(root_value)
    _required_target(root)
    mode = _confirm_mutable_root(root, isolated, live_confirmation)
    gateway_state = root / "state" / "gateway"
    edge_state = root / "state" / "edge"
    gateway_logs = root / "logs" / "gateway"
    gateway_state.mkdir(mode=0o750, parents=True, exist_ok=True)
    edge_state.mkdir(mode=0o755, parents=True, exist_ok=True)
    gateway_logs.mkdir(mode=0o770, parents=True, exist_ok=True)
    os.chmod(gateway_state, 0o750)
    os.chmod(edge_state, 0o755)
    os.chmod(gateway_logs, 0o770)

    paths = [gateway_state, gateway_logs]
    for name in ("auth-snapshot.json", "quota-snapshot.json", "quota-heartbeat.json"):
        path = gateway_state / name
        if path.is_file():
            os.chmod(path, 0o640)
            paths.append(path)
    active_slot = edge_state / "active-gateway.conf"
    if active_slot.is_file():
        os.chmod(active_slot, 0o644)
    access_log = gateway_logs / "access.tsv"
    if access_log.is_file():
        os.chmod(access_log, 0o660)
        paths.append(access_log)
    if os.geteuid() == 0:
        for path in paths:
            os.chown(path, -1, SNAPSHOT_GROUP_ID)
    return {
        "mode": mode,
        "root": str(root),
        "gateway_group": SNAPSHOT_GROUP_ID,
        "gateway_paths": len(paths),
    }


def build_parser():
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)

    snapshot = subparsers.add_parser("snapshot", help="create one isolated online runtime copy")
    snapshot.add_argument("--source-root", required=True)
    snapshot.add_argument("--target-root", required=True)
    snapshot.add_argument("--confirm-source-root", required=True)

    migrate = subparsers.add_parser("migrate-usage", help="migrate usage schema with an online backup")
    migrate.add_argument("--root", required=True)
    migrate.add_argument("--module-root", required=True)
    migrate.add_argument("--backup-dir", required=True)
    migrate.add_argument("--confirm-isolated-copy", action="store_true")
    migrate.add_argument("--confirm-live-cutover", default="")

    permissions = subparsers.add_parser("prepare-permissions", help="grant only Go Gateway runtime access")
    permissions.add_argument("--root", required=True)
    permissions.add_argument("--confirm-isolated-copy", action="store_true")
    permissions.add_argument("--confirm-live-cutover", default="")
    return parser


def main(argv=None):
    args = build_parser().parse_args(argv)
    if args.command == "snapshot":
        result = snapshot_runtime(args.source_root, args.target_root, args.confirm_source_root)
    elif args.command == "migrate-usage":
        result = migrate_usage(
            args.root,
            args.module_root,
            args.backup_dir,
            args.confirm_isolated_copy,
            args.confirm_live_cutover,
        )
    else:
        result = prepare_permissions(
            args.root,
            args.confirm_isolated_copy,
            args.confirm_live_cutover,
        )
    print(json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
