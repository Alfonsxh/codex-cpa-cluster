#!/usr/bin/env python3
"""Cross-version SQLite ownership leases for the transitional Python runtime."""

import json
import math
import os
import re
import secrets
import sqlite3
import stat
import threading
import time
import uuid
from contextlib import contextmanager
from pathlib import Path

import fcntl


LEASE_STATE_VERSION = 1
LEASE_STATE_PREFIX = "ownership_lease:"
RUNTIME_SCOPE = "runtime-writer"
MIN_TTL_SECONDS = 5
MAX_TTL_SECONDS = 5 * 60
SCOPE_PATTERN = re.compile(r"^[a-z0-9][a-z0-9._-]{0,63}$")


class OwnershipError(RuntimeError):
    """Base error for ownership acquisition and fencing failures."""


class LeaseHeldError(OwnershipError):
    pass


class LeaseMissingError(OwnershipError):
    pass


class LeaseLostError(OwnershipError):
    pass


class LeaseStateError(OwnershipError):
    pass


class LeaseStore:
    """Lease-only handle that never creates or migrates deployment state."""

    def __init__(self, root, now=None):
        self.root = Path(root).resolve()
        self.path = self.root / "state" / "control-plane.sqlite3"
        self.key_path = Path(
            os.environ.get(
                "CLIPROXY_SECRET_KEY_FILE",
                self.root / "secrets" / "control-plane.key",
            )
        ).resolve()
        self.lock_path = self.root / "state" / ".control-plane.lock"
        self.now = now or time.time
        self._require_regular_file(self.path, "control-plane database")
        self._require_regular_file(self.key_path, "control-plane encryption key")
        with self._connect() as connection:
            row = connection.execute(
                "SELECT COUNT(*) FROM sqlite_master "
                "WHERE type = 'table' AND name = 'runtime_state'"
            ).fetchone()
        if int(row[0]) != 1:
            raise OwnershipError(
                "existing control-plane database is missing required table runtime_state"
            )

    @staticmethod
    def _require_regular_file(path, description):
        try:
            information = path.lstat()
        except OSError as error:
            raise OwnershipError(
                "open existing {}: {}".format(description, error)
            ) from error
        if path.is_symlink() or not stat.S_ISREG(information.st_mode):
            raise OwnershipError(
                "open existing {}: {} is not a regular file".format(description, path)
            )

    def _connect(self):
        connection = sqlite3.connect(
            "file:{}?mode=rw".format(self.path),
            uri=True,
            timeout=30,
        )
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA busy_timeout = 30000")
        connection.execute("PRAGMA foreign_keys = ON")
        return connection

    @contextmanager
    def _transaction(self):
        descriptor = os.open(str(self.lock_path), os.O_CREAT | os.O_RDWR, 0o600)
        try:
            os.chmod(self.lock_path, 0o600)
            fcntl.flock(descriptor, fcntl.LOCK_EX)
            connection = self._connect()
            try:
                connection.execute("BEGIN IMMEDIATE")
                yield connection
                connection.commit()
            except Exception:
                connection.rollback()
                raise
            finally:
                connection.close()
        finally:
            fcntl.flock(descriptor, fcntl.LOCK_UN)
            os.close(descriptor)

    @staticmethod
    def _validate_input(scope, owner, ttl_seconds):
        scope = str(scope or "").strip()
        owner = str(owner or "").strip()
        if not SCOPE_PATTERN.fullmatch(scope):
            raise OwnershipError("ownership lease scope is invalid")
        if not 1 <= len(owner) <= 128 or any(ord(character) < 32 for character in owner):
            raise OwnershipError(
                "ownership lease owner must contain 1 to 128 non-control characters"
            )
        ttl_seconds = int(math.ceil(float(ttl_seconds)))
        if not MIN_TTL_SECONDS <= ttl_seconds <= MAX_TTL_SECONDS:
            raise OwnershipError(
                "ownership lease TTL must be between 5 and 300 seconds"
            )
        return scope, owner, ttl_seconds

    @staticmethod
    def _decode(scope, raw):
        try:
            lease = json.loads(raw)
        except (TypeError, json.JSONDecodeError) as error:
            raise LeaseStateError(
                "ownership lease state is invalid for {}".format(scope)
            ) from error
        required = {
            "version",
            "scope",
            "owner",
            "generation",
            "acquired_at",
            "renewed_at",
            "expires_at",
        }
        if not isinstance(lease, dict) or not required.issubset(lease):
            raise LeaseStateError(
                "ownership lease state is invalid for {}".format(scope)
            )
        valid = (
            lease.get("version") == LEASE_STATE_VERSION
            and lease.get("scope") == scope
            and isinstance(lease.get("owner"), str)
            and bool(lease.get("owner"))
            and isinstance(lease.get("generation"), int)
            and lease["generation"] >= 1
            and isinstance(lease.get("acquired_at"), int)
            and lease["acquired_at"] >= 1
            and isinstance(lease.get("renewed_at"), int)
            and lease["renewed_at"] >= lease["acquired_at"]
            and isinstance(lease.get("expires_at"), int)
            and lease["expires_at"] >= lease["renewed_at"]
            and isinstance(lease.get("token", ""), str)
        )
        if not valid:
            raise LeaseStateError(
                "ownership lease state is invalid for {}".format(scope)
            )
        return lease

    def _read(self, connection, scope):
        row = connection.execute(
            "SELECT payload_json FROM runtime_state WHERE name = ?",
            (LEASE_STATE_PREFIX + scope,),
        ).fetchone()
        return self._decode(scope, row["payload_json"]) if row else None

    @staticmethod
    def _write(connection, lease, updated_at):
        connection.execute(
            """
            INSERT INTO runtime_state(name, payload_json, updated_at) VALUES (?, ?, ?)
            ON CONFLICT(name) DO UPDATE SET
                payload_json = excluded.payload_json,
                updated_at = excluded.updated_at
            """,
            (
                LEASE_STATE_PREFIX + lease["scope"],
                json.dumps(lease, ensure_ascii=False, separators=(",", ":")),
                int(updated_at),
            ),
        )

    def read(self, scope):
        scope = str(scope or "").strip()
        if not SCOPE_PATTERN.fullmatch(scope):
            raise OwnershipError("ownership lease scope is invalid")
        with self._connect() as connection:
            lease = self._read(connection, scope)
        return dict(lease) if lease else None

    def take(self, scope, owner, ttl_seconds):
        scope, owner, ttl_seconds = self._validate_input(scope, owner, ttl_seconds)
        with self._transaction() as connection:
            current = self._read(connection, scope)
            now = int(self.now())
            if current and current["expires_at"] > now:
                raise LeaseHeldError(
                    "ownership lease is held: scope {} is owned by {} at generation {} until {}".format(
                        scope,
                        current["owner"],
                        current["generation"],
                        current["expires_at"],
                    )
                )
            lease = {
                "version": LEASE_STATE_VERSION,
                "scope": scope,
                "owner": owner,
                "generation": int(current["generation"] + 1) if current else 1,
                "token": secrets.token_hex(32),
                "acquired_at": now,
                "renewed_at": now,
                "expires_at": now + ttl_seconds,
            }
            self._write(connection, lease, now)
        return dict(lease)

    def join(self, scope, owner, ttl_seconds):
        scope, owner, ttl_seconds = self._validate_input(scope, owner, ttl_seconds)
        with self._transaction() as connection:
            current = self._read(connection, scope)
            now = int(self.now())
            if not current or current["expires_at"] <= now or not current.get("token"):
                raise LeaseMissingError(
                    "ownership lease is missing: active scope {} must be transferred explicitly".format(
                        scope
                    )
                )
            if current["owner"] != owner:
                raise LeaseHeldError(
                    "ownership lease is held: scope {} is owned by {} at generation {} until {}".format(
                        scope,
                        current["owner"],
                        current["generation"],
                        current["expires_at"],
                    )
                )
            current["renewed_at"] = now
            current["expires_at"] = now + ttl_seconds
            current.pop("released_at", None)
            self._write(connection, current, now)
        return dict(current)

    def renew(self, lease, ttl_seconds):
        scope, owner, ttl_seconds = self._validate_input(
            lease.get("scope"), lease.get("owner"), ttl_seconds
        )
        if not lease.get("token") or int(lease.get("generation") or 0) < 1:
            raise LeaseLostError("ownership lease was lost: token and generation are required")
        with self._transaction() as connection:
            current = self._read(connection, scope)
            now = int(self.now())
            matches = current and all(
                current.get(field) == lease.get(field)
                for field in ("owner", "generation", "token")
            )
            if not matches or current["expires_at"] <= now:
                raise LeaseLostError(
                    "ownership lease was lost: scope {} generation {}".format(
                        scope, lease["generation"]
                    )
                )
            current["renewed_at"] = now
            current["expires_at"] = now + ttl_seconds
            current.pop("released_at", None)
            self._write(connection, current, now)
        return dict(current)

    def release(self, lease):
        scope, owner, _ = self._validate_input(
            lease.get("scope"), lease.get("owner"), MIN_TTL_SECONDS
        )
        if not lease.get("token") or int(lease.get("generation") or 0) < 1:
            raise LeaseLostError("ownership lease was lost: token and generation are required")
        with self._transaction() as connection:
            current = self._read(connection, scope)
            matches = current and all(
                current.get(field) == lease.get(field)
                for field in ("owner", "generation", "token")
            )
            if not matches:
                raise LeaseLostError(
                    "ownership lease was lost: scope {} generation {}".format(
                        scope, lease["generation"]
                    )
                )
            now = int(self.now())
            current["token"] = ""
            current["renewed_at"] = now
            current["expires_at"] = now
            current["released_at"] = now
            self._write(connection, current, now)


class OwnershipGuard:
    """Hold one shared runtime lease and exclusive worker leases."""

    def __init__(
        self,
        root,
        worker_scopes,
        runtime_owner=None,
        ttl_seconds=None,
        on_lost=None,
        store=None,
    ):
        self.runtime_owner = str(
            runtime_owner or os.environ.get("CLIPROXY_RUNTIME_OWNER", "python-v1")
        ).strip()
        self.ttl_seconds = float(
            ttl_seconds or os.environ.get("CLIPROXY_OWNERSHIP_TTL_SECONDS", "30")
        )
        self.worker_scopes = tuple(dict.fromkeys(worker_scopes))
        if not self.worker_scopes:
            raise OwnershipError("at least one worker lease scope is required")
        self.store = store or LeaseStore(root)
        self.on_lost = on_lost
        self.runtime_lease = None
        self.worker_leases = []
        self.stopping = threading.Event()
        self.lost = threading.Event()
        self.error = None
        self.thread = None

    def start(self):
        if self.thread is not None:
            raise OwnershipError("ownership guard has already started")
        self.runtime_lease = self.store.join(
            RUNTIME_SCOPE, self.runtime_owner, self.ttl_seconds
        )
        try:
            for scope in self.worker_scopes:
                owner = "{}-{}:{}".format(self.runtime_owner, scope, uuid.uuid4())
                self.worker_leases.append(
                    self.store.take(scope, owner, self.ttl_seconds)
                )
        except Exception:
            self._release_workers()
            raise
        self.thread = threading.Thread(
            target=self._heartbeat,
            name="ownership-heartbeat",
            daemon=True,
        )
        self.thread.start()
        return self

    def _heartbeat(self):
        interval = max(1.0, self.ttl_seconds / 3.0)
        while not self.stopping.wait(interval):
            try:
                self.runtime_lease = self.store.renew(
                    self.runtime_lease, self.ttl_seconds
                )
                self.worker_leases = [
                    self.store.renew(lease, self.ttl_seconds)
                    for lease in self.worker_leases
                ]
            except Exception as error:
                self.error = error
                self.lost.set()
                self.stopping.set()
                if self.on_lost is not None:
                    self.on_lost(error)
                return

    def assert_owned(self):
        if self.lost.is_set():
            raise LeaseLostError("runtime ownership was lost: {}".format(self.error))

    def _release_workers(self):
        for lease in reversed(self.worker_leases):
            try:
                self.store.release(lease)
            except LeaseLostError:
                pass
        self.worker_leases = []

    def stop(self):
        self.stopping.set()
        if self.thread is not None and self.thread is not threading.current_thread():
            self.thread.join(timeout=max(1.0, self.ttl_seconds / 2.0))
        if not self.lost.is_set():
            self._release_workers()

    def __enter__(self):
        return self.start()

    def __exit__(self, unused_type, unused_value, unused_traceback):
        self.stop()
        return False


def exit_process_on_ownership_loss(error):
    """Fail closed so no in-flight Python v1 mutation survives fencing loss."""
    print(
        "runtime ownership lost; terminating Python v1 process: {}: {}".format(
            type(error).__name__, error
        ),
        file=os.sys.stderr,
        flush=True,
    )
    os._exit(70)
