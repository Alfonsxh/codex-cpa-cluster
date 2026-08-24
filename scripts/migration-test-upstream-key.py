#!/usr/bin/env python3
"""Prepare a permission-restricted internal Key for an isolated test upstream.

The dedicated external Test Key and both internal Keys are never printed.  The
two control databases must be distinct marked isolation copies and must resolve
the Test Key to the same user, account set, and active internal Key.
"""

import argparse
import hashlib
import json
import os
import sqlite3
import stat
import sys
from pathlib import Path


MAX_KEY_BYTES = 4096
PRODUCTION_ROOTS = {
    Path("/home/AI/CLIProxyAPI"),
    Path("/opt/codex-cpa-cluster"),
}


def deployment_root(database_path):
    database = Path(database_path).resolve()
    if database.name != "control-plane.sqlite3" or database.parent.name != "state":
        raise ValueError("control database must be ROOT/state/control-plane.sqlite3")
    return database.parent.parent


def validate_isolated_database(database_path):
    database = Path(database_path).resolve()
    root = deployment_root(database)
    if root in PRODUCTION_ROOTS or root == Path("/"):
        raise ValueError("control database points at a live or broad deployment root")
    if not (root / ".v2-isolated-copy.json").is_file():
        raise ValueError("isolated-copy marker is missing")
    if not database.is_file():
        raise ValueError("control database is missing")
    return database


def read_restricted_key(path):
    path = Path(path)
    metadata = path.stat()
    if not stat.S_ISREG(metadata.st_mode) or stat.S_IMODE(metadata.st_mode) & 0o077:
        raise ValueError("Test Key file must be a permission-restricted regular file")
    if metadata.st_size <= 0 or metadata.st_size > MAX_KEY_BYTES:
        raise ValueError("Test Key file size is invalid")
    key = path.read_text(encoding="utf-8").strip()
    if not key or len(key.encode("utf-8")) > MAX_KEY_BYTES or "\n" in key or "\r" in key:
        raise ValueError("Test Key file content is invalid")
    return key


def resolve_identity(database_path, external_key):
    database = sqlite3.connect(
        "file:{}?mode=ro".format(Path(database_path).resolve()), uri=True
    )
    try:
        rows = database.execute(
            "SELECT lower(trim(user_email)), account_id FROM key_records "
            "WHERE status = 'active' AND secret = ? ORDER BY account_id",
            (external_key,),
        ).fetchall()
        users = {str(row[0] or "") for row in rows if str(row[0] or "")}
        accounts = sorted({str(row[1] or "") for row in rows if str(row[1] or "")})
        if len(users) != 1 or not accounts:
            raise ValueError("Test Key does not resolve to one active isolated user")
        user = next(iter(users))
        internal_rows = database.execute(
            "SELECT secret FROM internal_keys "
            "WHERE lower(trim(user_email)) = ? AND status = 'active'",
            (user,),
        ).fetchall()
        internal_keys = {str(row[0] or "") for row in internal_rows if str(row[0] or "")}
        if len(internal_keys) != 1:
            raise ValueError("Test user does not resolve to one active internal Key")
    finally:
        database.close()
    canonical = json.dumps(
        {"user": user, "accounts": accounts},
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    return {
        "user": user,
        "accounts": accounts,
        "identity_sha256": hashlib.sha256(canonical).hexdigest(),
        "internal_key": next(iter(internal_keys)),
    }


def write_restricted(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if path.exists():
        raise ValueError("internal Key output already exists")
    temporary = path.with_name(".{}.{}.tmp".format(path.name, os.getpid()))
    descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            handle.write(value + "\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
    except Exception:
        try:
            temporary.unlink()
        except FileNotFoundError:
            pass
        raise


def prepare(v1_database, v2_database, external_key_file, output):
    v1_database = validate_isolated_database(v1_database)
    v2_database = validate_isolated_database(v2_database)
    if os.path.samefile(v1_database, v2_database):
        raise ValueError("v1 and v2 control databases must be distinct files")
    external_key = read_restricted_key(external_key_file)
    v1 = resolve_identity(v1_database, external_key)
    v2 = resolve_identity(v2_database, external_key)
    if v1["identity_sha256"] != v2["identity_sha256"]:
        raise ValueError("isolated Test Key identities differ")
    if v1["internal_key"] != v2["internal_key"]:
        raise ValueError("isolated Test user internal Keys differ")
    write_restricted(output, v1["internal_key"])
    return {
        "version": 1,
        "compatible": True,
        "identity_sha256": v1["identity_sha256"],
        "account_count": len(v1["accounts"]),
        "output_created": True,
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--v1-control-db", type=Path, required=True)
    parser.add_argument("--v2-control-db", type=Path, required=True)
    parser.add_argument("--external-key-file", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args(argv)
    report = prepare(
        args.v1_control_db,
        args.v2_control_db,
        args.external_key_file,
        args.output,
    )
    print(json.dumps(report, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
