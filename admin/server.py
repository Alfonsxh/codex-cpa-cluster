#!/usr/bin/env python3
"""Authenticated web control plane for the CLIProxyAPI multi-account stack."""

import argparse
import base64
import copy
import concurrent.futures
import gzip
import hashlib
import hmac
import ipaddress
import json
import math
import os
import re
import secrets
import signal
import subprocess
import sys
import threading
import time
import traceback
import urllib.error
import urllib.parse
import urllib.request
import uuid
from collections import OrderedDict
from datetime import datetime, timezone
from http import HTTPStatus
from http.cookies import SimpleCookie
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


APPLICATION_ROOT = Path(
    os.environ.get("CLIPROXY_APP_ROOT", Path(__file__).resolve().parents[1])
).resolve()
PROJECT_ROOT = Path(os.environ.get("CLIPROXY_ROOT", APPLICATION_ROOT)).resolve()
sys.path.insert(0, str(APPLICATION_ROOT / "scripts"))
sys.path.insert(0, str(APPLICATION_ROOT / "admin"))
import cliproxy  # noqa: E402
from account_failover import AccountFailoverScheduler, AccountFailoverService  # noqa: E402
from usage_store import MAX_WEEKLY_QUOTA_TOKENS, UsageStore  # noqa: E402
from wecom_notifications import (  # noqa: E402
    NotificationScheduler,
    WeComNotificationService,
    _timezone as configured_timezone,
    redact_webhook,
    usage_center_url,
)


MAX_BODY_BYTES = 64 * 1024
BRANDING_MAX_BODY_BYTES = 3 * 1024 * 1024
JSON_GZIP_MIN_BYTES = 32 * 1024
MAX_JOB_LINES = 600
MAX_JOB_COUNT = 60
USAGE_LIMIT_URL = "https://chatgpt.com/backend-api/wham/usage"
USAGE_LIMIT_RESET_CREDITS_URL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
USAGE_LIMIT_RESET_URL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"
USAGE_LIMIT_CACHE_SECONDS = 60
USAGE_LIMIT_FORCE_MIN_AGE_SECONDS = 15
USAGE_LIMIT_TIMEOUT_SECONDS = 20
WEEKLY_WINDOW_SECONDS = 7 * 24 * 60 * 60
CPA_MANAGEMENT_TIMEOUT_SECONDS = 3
CPA_MANAGEMENT_MAX_BODY_BYTES = 2 * 1024 * 1024
CPA_MANAGEMENT_MAX_WORKERS = 16
CPA_RUNTIME_ERROR_WINDOW_SECONDS = 60 * 60
CPA_ACTIVE_ERROR_SECONDS = 15 * 60
CPA_RUNTIME_SNAPSHOT_CACHE_SECONDS = 15
COMPOSE_PS_CACHE_SECONDS = 2
PUBLIC_USAGE_CACHE_SECONDS = 10
PUBLIC_USAGE_CACHE_MAX_ENTRIES = 8
ADMIN_OVERVIEW_CACHE_SECONDS = 3
ADMIN_OVERVIEW_CATALOG_CACHE_SECONDS = 5
ADMIN_OVERVIEW_USAGE_CACHE_SECONDS = 15
ADMIN_ACCOUNTS_CACHE_SECONDS = 5
ADMIN_READ_CACHE_MAX_ENTRIES = 32
USER_SUMMARY_CACHE_SECONDS = 15
USER_SUMMARY_CACHE_MAX_ENTRIES = 12
ADMIN_HTTP_MAX_WORKERS = 32
ADMIN_HTTP_MAX_QUEUE = 64
ADMIN_HTTP_REQUEST_TIMEOUT_SECONDS = 30
SELF_SERVICE_LIFETIME_CACHE_SECONDS = 5 * 60
SELF_SERVICE_LIFETIME_CACHE_MAX_ENTRIES = 512
RELEASE_STATUS_CACHE_SECONDS = 15 * 60
RELEASE_STATUS_TIMEOUT_SECONDS = 30
RELEASE_IMAGE_RE = re.compile(
    r"^[A-Za-z0-9.-]+(?::[0-9]+)?/[A-Za-z0-9._/-]+:[A-Za-z0-9._-]+$"
)
MANAGED_EXTRA_SERVICES = (
    "web", "management", "usage-collector", "log-maintenance",
)
LOGGABLE_EXTRA_SERVICES = (
    "edge",
    "web",
    "gateway-blue",
    "gateway-green",
    "management",
    "usage-collector",
    "log-maintenance",
    "admin",
)
DEFAULT_USER_USAGE_WINDOW_SECONDS = 24 * 60 * 60
USER_USAGE_WINDOWS = {60 * 60, 24 * 60 * 60, 7 * 24 * 60 * 60, 30 * 24 * 60 * 60}
TODAY_USER_USAGE_WINDOW = "today"
ACCOUNT_USAGE_SINCE_RESET = "since_reset"
CUSTOM_USAGE_WINDOW = "custom"
OVERVIEW_USAGE_BUCKETS = {
    TODAY_USER_USAGE_WINDOW: 15 * 60,
    60 * 60: 60,
    6 * 60 * 60: 5 * 60,
    24 * 60 * 60: 15 * 60,
    7 * 24 * 60 * 60: 60 * 60,
    30 * 24 * 60 * 60: 6 * 60 * 60,
}
OVERVIEW_USAGE_MAX_BUCKETS = 360
PORTAL_SESSION_COOKIE = "cpa_user_session"
PORTAL_SESSION_TTL_SECONDS = 12 * 60 * 60
ADMIN_SESSION_COOKIE = "cpa_admin_session"
ADMIN_SESSION_TTL_SECONDS = 8 * 60 * 60
LEGACY_DEFAULT_PORTAL_PASSWORD = "123456"
PORTAL_INITIAL_PASSWORD_SECRET = "portal_initial_password"
PORTAL_PASSWORD_MIN_LENGTH = 8
PORTAL_PASSWORD_MAX_LENGTH = 128
PORTAL_PASSWORD_ITERATIONS = 310_000
PORTAL_PASSWORD_SCRYPT_N = 16_384
PORTAL_PASSWORD_SCRYPT_R = 8
PORTAL_PASSWORD_SCRYPT_P = 1
PORTAL_PASSWORD_SCRYPT_MAXMEM = 64 * 1024 * 1024
TOKEN_PATTERNS = (
    (re.compile(r"\bBearer\s+\S+", re.IGNORECASE), "Bearer [REDACTED]"),
    (
        re.compile(
            r"\b[a-z][a-z0-9_]{1,31}_(?:[a-z0-9]+_)*(?:[0-9a-f]{16}|[0-9a-f]{64}|"
            r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\b",
            re.IGNORECASE,
        ),
        "key_[REDACTED]",
    ),
    (re.compile(r"\bcpa_(?:[a-z0-9]+_)*[0-9a-f]{64}\b", re.IGNORECASE), "cpa_[REDACTED]"),
    (
        re.compile(r'("(?:access_token|refresh_token|id_token|api_key)"\s*:\s*")[^"]+', re.IGNORECASE),
        r"\1[REDACTED]",
    ),
)


def redact(value):
    value = redact_webhook(value)
    for pattern, replacement in TOKEN_PATTERNS:
        value = pattern.sub(replacement, value)
    return value


def utc_timestamp():
    return int(time.time())


def parse_user_usage_window(value):
    raw = str(value or DEFAULT_USER_USAGE_WINDOW_SECONDS).strip().lower()
    if raw == "all":
        return None
    if raw == TODAY_USER_USAGE_WINDOW:
        return TODAY_USER_USAGE_WINDOW
    try:
        window = int(raw)
    except ValueError:
        raise ValueError("统计范围无效")
    if window not in USER_USAGE_WINDOWS:
        raise ValueError("统计范围无效")
    return window


def hash_portal_password(password, salt=None):
    salt = secrets.token_bytes(16) if salt is None else salt
    digest = hashlib.scrypt(
        str(password).encode("utf-8"),
        salt=salt,
        n=PORTAL_PASSWORD_SCRYPT_N,
        r=PORTAL_PASSWORD_SCRYPT_R,
        p=PORTAL_PASSWORD_SCRYPT_P,
        maxmem=PORTAL_PASSWORD_SCRYPT_MAXMEM,
        dklen=32,
    )
    return "scrypt${}${}${}${}${}".format(
        PORTAL_PASSWORD_SCRYPT_N,
        PORTAL_PASSWORD_SCRYPT_R,
        PORTAL_PASSWORD_SCRYPT_P,
        salt.hex(),
        digest.hex(),
    )


def verify_portal_password(password, encoded):
    try:
        parts = str(encoded).split("$")
        algorithm = parts[0]
        if algorithm == "scrypt" and len(parts) == 6:
            n, r, p = (int(value) for value in parts[1:4])
            if (n, r, p) != (
                PORTAL_PASSWORD_SCRYPT_N,
                PORTAL_PASSWORD_SCRYPT_R,
                PORTAL_PASSWORD_SCRYPT_P,
            ):
                return False
            salt_hex, expected_hex = parts[4:]
            digest = hashlib.scrypt(
                str(password).encode("utf-8"),
                salt=bytes.fromhex(salt_hex),
                n=n,
                r=r,
                p=p,
                maxmem=PORTAL_PASSWORD_SCRYPT_MAXMEM,
                dklen=32,
            )
        elif algorithm == "pbkdf2_sha256" and len(parts) == 4:
            iteration_count = int(parts[1])
            if not 100_000 <= iteration_count <= 1_000_000:
                return False
            digest = hashlib.pbkdf2_hmac(
                "sha256",
                str(password).encode("utf-8"),
                bytes.fromhex(parts[2]),
                iteration_count,
            )
            expected_hex = parts[3]
        else:
            return False
        return hmac.compare_digest(digest.hex(), expected_hex)
    except (TypeError, ValueError):
        return False


DUMMY_PORTAL_PASSWORD_HASH = hash_portal_password(
    "invalid-portal-user",
    salt=b"\x00" * 16,
)


def parse_account_usage_window(value):
    raw = str(value or DEFAULT_USER_USAGE_WINDOW_SECONDS).strip().lower()
    if raw == ACCOUNT_USAGE_SINCE_RESET:
        return ACCOUNT_USAGE_SINCE_RESET
    return parse_user_usage_window(raw)


def parse_overview_usage_window(value):
    raw = str(value or DEFAULT_USER_USAGE_WINDOW_SECONDS).strip().lower()
    if raw in (TODAY_USER_USAGE_WINDOW, ACCOUNT_USAGE_SINCE_RESET):
        return raw
    try:
        window = int(raw)
    except ValueError:
        raise ValueError("趋势时间范围无效")
    if window not in OVERVIEW_USAGE_BUCKETS:
        raise ValueError("趋势时间范围无效")
    return window


def overview_usage_bucket_seconds(window_seconds):
    """Choose a readable interval while keeping custom trends bounded."""
    window_seconds = max(1, int(window_seconds))
    for bucket_seconds in (60, 5 * 60, 15 * 60, 60 * 60, 6 * 60 * 60, 24 * 60 * 60):
        if math.ceil(window_seconds / bucket_seconds) + 1 <= OVERVIEW_USAGE_MAX_BUCKETS:
            return bucket_seconds
    return max(24 * 60 * 60, math.ceil(window_seconds / (OVERVIEW_USAGE_MAX_BUCKETS - 1)))


def validate_custom_usage_range(start_at, end_at, now=None):
    try:
        start_at = int(str(start_at).strip())
        end_at = int(str(end_at).strip())
    except (TypeError, ValueError):
        raise ValueError("自定义统计范围缺少有效的开始或结束时间")
    generated_at = utc_timestamp() if now is None else int(now)
    if start_at < 0 or end_at <= 0:
        raise ValueError("自定义统计范围无效")
    if start_at >= end_at:
        raise ValueError("自定义统计范围的开始时间必须早于结束时间")
    if end_at > generated_at + 60:
        raise ValueError("自定义统计范围的结束时间不能晚于当前时间")
    return start_at, end_at


def parse_admin_usage_range(query, window_parser):
    raw_window = query.get("window", [""])[0]
    if str(raw_window).strip().lower() != CUSTOM_USAGE_WINDOW:
        return {
            "window": window_parser(raw_window),
            "start_at": None,
            "end_at": None,
        }
    start_at, end_at = validate_custom_usage_range(
        query.get("start_at", [""])[0],
        query.get("end_at", [""])[0],
    )
    return {
        "window": CUSTOM_USAGE_WINDOW,
        "start_at": start_at,
        "end_at": end_at,
    }


def normalize_overview_filter_values(value, lower=False):
    """Normalize repeated or comma-separated Grafana-style variable values."""
    raw_values = value if isinstance(value, (list, tuple)) else [value]
    values = []
    for raw_value in raw_values:
        for part in str(raw_value or "").split(","):
            normalized = part.strip().lower() if lower else part.strip()
            if normalized and normalized not in values:
                values.append(normalized)
    return values


class APIError(Exception):
    def __init__(self, status, message, code="request_failed", headers=None):
        super().__init__(message)
        self.status = int(status)
        self.message = message
        self.code = code
        self.headers = list(headers or ())


class BoundedSWRCache:
    """Small in-process TTL cache with per-key single-flight refreshes.

    Entries stay available after expiry so read-heavy endpoints can return stale
    data while one daemon thread refreshes them. Generation tokens prevent a
    refresh that started before invalidation from restoring obsolete data.
    """

    def __init__(self, max_entries):
        self.max_entries = max(1, int(max_entries))
        self.condition = threading.Condition(threading.RLock())
        self.entries = OrderedDict()
        self.refreshing = {}
        self.generation = 0

    @staticmethod
    def _clone(value):
        return copy.deepcopy(value)

    def _publish(self, key, token, payload, ttl_seconds, now=None):
        cached_payload = self._clone(payload)
        with self.condition:
            if self.generation == token and self.refreshing.get(key) == token:
                self.entries[key] = {
                    "expires_at": (
                        time.monotonic() if now is None else float(now)
                    ) + float(ttl_seconds),
                    "payload": cached_payload,
                }
                self.entries.move_to_end(key)
                while len(self.entries) > self.max_entries:
                    self.entries.popitem(last=False)
            if self.refreshing.get(key) == token:
                self.refreshing.pop(key, None)
            self.condition.notify_all()

    def _refresh_in_background(self, key, token, loader, ttl_seconds, now):
        try:
            payload = loader()
        except Exception:
            # A stale value remains usable; the next request may retry refresh.
            with self.condition:
                if self.refreshing.get(key) == token:
                    self.refreshing.pop(key, None)
                self.condition.notify_all()
            return
        self._publish(key, token, payload, ttl_seconds, now=now)

    def get(
        self,
        key,
        loader,
        ttl_seconds,
        *,
        force_refresh=False,
        stale_while_revalidate=True,
        now=None,
    ):
        """Return ``(payload, state)`` where state is hit/miss/stale/refresh."""
        while True:
            refresh_thread = None
            return_cached = False
            with self.condition:
                cache_now = time.monotonic() if now is None else float(now)
                cached = self.entries.get(key)
                if cached is not None:
                    self.entries.move_to_end(key)
                is_fresh = bool(cached and cache_now < cached["expires_at"])
                if is_fresh and not force_refresh:
                    payload = cached["payload"]
                    state = "hit"
                    return_cached = True
                else:
                    token = self.generation
                    active_refresh = self.refreshing.get(key) == token
                    if cached is not None and stale_while_revalidate:
                        if not active_refresh:
                            self.refreshing[key] = token
                            refresh_thread = threading.Thread(
                                target=self._refresh_in_background,
                                args=(key, token, loader, ttl_seconds, now),
                                name="admin-cache-refresh",
                                daemon=True,
                            )
                        payload = cached["payload"]
                        state = "refresh" if force_refresh else "stale"
                        return_cached = True
                    elif active_refresh:
                        # A cold miss has no safe stale value, so wait for its
                        # one loader instead of duplicating the expensive query.
                        self.condition.wait()
                        continue
                    else:
                        self.refreshing[key] = token
                        payload = None
                        state = "miss"

            if refresh_thread is not None:
                refresh_thread.start()
            if return_cached:
                return self._clone(payload), state

            try:
                loaded = loader()
            except BaseException:
                with self.condition:
                    if self.refreshing.get(key) == token:
                        self.refreshing.pop(key, None)
                    self.condition.notify_all()
                raise
            self._publish(key, token, loaded, ttl_seconds, now=now)
            return self._clone(loaded), state

    def invalidate(self):
        with self.condition:
            self.generation += 1
            self.entries.clear()
            self.refreshing.clear()
            self.condition.notify_all()


class AuthenticationRateLimiter:
    """Small in-memory IP/account limiter for expensive secret verification."""

    def __init__(
        self,
        *,
        threshold=5,
        window_seconds=5 * 60,
        base_block_seconds=30,
        max_block_seconds=15 * 60,
    ):
        self.threshold = int(threshold)
        self.window_seconds = int(window_seconds)
        self.base_block_seconds = int(base_block_seconds)
        self.max_block_seconds = int(max_block_seconds)
        self.lock = threading.RLock()
        self.states = {}
        self.related = {}

    def retry_after(self, keys, now=None):
        now = time.time() if now is None else float(now)
        with self.lock:
            retry_at = max(
                (
                    float(self.states.get(str(key), {}).get("blocked_until", 0))
                    for key in keys
                ),
                default=0,
            )
        return max(0, int(retry_at - now + 0.999))

    def record_failure(self, keys, now=None):
        now = time.time() if now is None else float(now)
        blocked_until = 0
        with self.lock:
            if len(self.states) > 4096:
                cutoff = now - self.window_seconds - self.max_block_seconds
                self.states = {
                    key: state
                    for key, state in self.states.items()
                    if max(state["window_started"], state["blocked_until"]) >= cutoff
                }
                active_keys = set(self.states)
                self.related = {
                    key: {related for related in values if related in active_keys}
                    for key, values in self.related.items()
                    if key in active_keys
                }
            normalized_keys = tuple(dict.fromkeys(str(key) for key in keys))
            for key in normalized_keys:
                self.related.setdefault(key, set()).update(
                    related for related in normalized_keys if related != key
                )
                state = self.states.get(key)
                if not state or now - state["window_started"] > self.window_seconds:
                    state = {
                        "failures": 0,
                        "window_started": now,
                        "blocked_until": 0,
                    }
                state["failures"] += 1
                if state["failures"] >= self.threshold:
                    exponent = min(state["failures"] - self.threshold, 8)
                    block_seconds = min(
                        self.max_block_seconds,
                        self.base_block_seconds * (2 ** exponent),
                    )
                    state["blocked_until"] = max(
                        state["blocked_until"], now + block_seconds
                    )
                self.states[key] = state
                blocked_until = max(blocked_until, state["blocked_until"])
        return max(0, int(blocked_until - now + 0.999))

    def clear(self, keys, *, include_related=False):
        with self.lock:
            target_keys = {str(key) for key in keys}
            if include_related:
                for key in tuple(target_keys):
                    target_keys.update(self.related.get(key, ()))
            for key in target_keys:
                self.states.pop(key, None)
                self.related.pop(key, None)
            for related in self.related.values():
                related.difference_update(target_keys)


class AdminSessionStore:
    def __init__(self, ttl_seconds=ADMIN_SESSION_TTL_SECONDS):
        self.ttl_seconds = int(ttl_seconds)
        self.lock = threading.RLock()
        self.sessions = {}

    @staticmethod
    def _digest(value):
        return hashlib.sha256(str(value or "").encode("utf-8")).hexdigest()

    def create(self, now=None):
        now = utc_timestamp() if now is None else int(now)
        token = secrets.token_urlsafe(32)
        csrf_token = secrets.token_urlsafe(32)
        expires_at = now + self.ttl_seconds
        with self.lock:
            self._prune_locked(now)
            self.sessions[self._digest(token)] = {
                "csrf_hash": self._digest(csrf_token),
                "csrf_token": csrf_token,
                "expires_at": expires_at,
            }
        return {"token": token, "csrf_token": csrf_token, "expires_at": expires_at}

    def resolve(self, token, now=None):
        now = utc_timestamp() if now is None else int(now)
        digest = self._digest(token) if token else ""
        with self.lock:
            self._prune_locked(now)
            session = self.sessions.get(digest)
            return dict(session) if session else None

    def verify_csrf(self, session, token):
        provided = self._digest(token) if token else ""
        expected = str((session or {}).get("csrf_hash", ""))
        return bool(provided and expected and hmac.compare_digest(expected, provided))

    def revoke(self, token):
        digest = self._digest(token) if token else ""
        with self.lock:
            return self.sessions.pop(digest, None) is not None

    def revoke_all(self):
        with self.lock:
            count = len(self.sessions)
            self.sessions.clear()
        return count

    def _prune_locked(self, now):
        expired = [
            digest
            for digest, session in self.sessions.items()
            if int(session["expires_at"]) <= int(now)
        ]
        for digest in expired:
            self.sessions.pop(digest, None)


class JobManager:
    def __init__(self, root, action_lock=None):
        self.root = Path(root).resolve()
        self.action_lock = action_lock or threading.RLock()
        self.jobs = {}
        self.lock = threading.RLock()

    def _start(self, name, target, commands, dedupe_key=None):
        with self.lock:
            if dedupe_key:
                active = sorted(
                    (
                        job
                        for job in self.jobs.values()
                        if job.get("_dedupe_key") == dedupe_key
                        and job["status"] in ("queued", "running")
                    ),
                    key=lambda item: item["created_at"],
                    reverse=True,
                )
                if active:
                    return self._public(active[0]), True
            queued = sum(job["status"] in ("queued", "running", "cancelling") for job in self.jobs.values())
            if queued >= 10:
                raise APIError(HTTPStatus.CONFLICT, "已有过多任务正在等待，请稍后再试", "job_queue_full")
            job_id = uuid.uuid4().hex[:16]
            job = {
                "id": job_id,
                "name": name,
                "target": target,
                "status": "queued",
                "created_at": utc_timestamp(),
                "started_at": None,
                "finished_at": None,
                "exit_code": None,
                "output": [],
                "_cancel_requested": False,
                "_process": None,
                "_dedupe_key": dedupe_key,
            }
            self.jobs[job_id] = job
            self._trim_locked()
        thread = threading.Thread(
            target=self._run,
            args=(job_id, commands),
            name="admin-job-{}".format(job_id),
            daemon=True,
        )
        thread.start()
        return self.get(job_id), False

    def start(self, name, target, commands):
        job, _ = self._start(name, target, commands)
        return job

    def start_or_reuse(self, name, target, commands, dedupe_key):
        return self._start(name, target, commands, dedupe_key=dedupe_key)

    def _trim_locked(self):
        if len(self.jobs) <= MAX_JOB_COUNT:
            return
        removable = sorted(
            (job for job in self.jobs.values() if job["status"] not in ("queued", "running")),
            key=lambda item: item["created_at"],
        )
        for job in removable[: max(0, len(self.jobs) - MAX_JOB_COUNT)]:
            self.jobs.pop(job["id"], None)

    def _append(self, job_id, line):
        safe_line = redact(line.rstrip("\r\n"))
        with self.lock:
            job = self.jobs.get(job_id)
            if not job:
                return
            job["output"].append(safe_line)
            if len(job["output"]) > MAX_JOB_LINES:
                job["output"] = job["output"][-MAX_JOB_LINES:]

    def _run(self, job_id, commands):
        exit_code = 0
        try:
            with self.action_lock:
                with self.lock:
                    job = self.jobs[job_id]
                    if job.get("_cancel_requested"):
                        exit_code = -15
                        job["status"] = "cancelled"
                        job["exit_code"] = exit_code
                        job["finished_at"] = utc_timestamp()
                        return
                    job["status"] = "running"
                    job["started_at"] = utc_timestamp()
                for command in commands:
                    with self.lock:
                        if self.jobs[job_id].get("_cancel_requested"):
                            exit_code = -15
                            break
                    self._append(job_id, "$ {}".format(" ".join(command)))
                    process = subprocess.Popen(
                        command,
                        cwd=str(self.root),
                        stdout=subprocess.PIPE,
                        stderr=subprocess.STDOUT,
                        text=True,
                        bufsize=1,
                        start_new_session=True,
                    )
                    with self.lock:
                        self.jobs[job_id]["_process"] = process
                        cancel_after_spawn = self.jobs[job_id].get("_cancel_requested")
                    # Cancellation can arrive after the pre-spawn check but
                    # before _process is published. Recheck here so that race
                    # cannot leave a cancelled command running to completion.
                    if cancel_after_spawn and process.poll() is None:
                        try:
                            os.killpg(process.pid, signal.SIGTERM)
                        except ProcessLookupError:
                            pass
                    if process.stdout:
                        try:
                            for line in process.stdout:
                                self._append(job_id, line)
                        finally:
                            process.stdout.close()
                    exit_code = process.wait()
                    with self.lock:
                        self.jobs[job_id]["_process"] = None
                    if exit_code != 0:
                        break
        except Exception as error:
            exit_code = 1
            self._append(job_id, "任务执行失败：{}".format(redact(error)))
        finally:
            with self.lock:
                job = self.jobs.get(job_id)
                if job:
                    job["exit_code"] = exit_code
                    if job.get("_cancel_requested"):
                        job["status"] = "cancelled"
                    else:
                        job["status"] = "succeeded" if exit_code == 0 else "failed"
                    job["finished_at"] = utc_timestamp()

    @staticmethod
    def _public(job, include_output=True):
        payload = {key: value for key, value in job.items() if key != "output" and not key.startswith("_")}
        if include_output:
            payload["output"] = list(job["output"])
        return payload

    def get(self, job_id):
        with self.lock:
            job = self.jobs.get(job_id)
            if not job:
                raise APIError(HTTPStatus.NOT_FOUND, "任务不存在", "job_not_found")
            return self._public(job)

    def recent(self, limit=10, include_output=False):
        with self.lock:
            jobs = sorted(self.jobs.values(), key=lambda item: item["created_at"], reverse=True)
            return [self._public(job, include_output=include_output) for job in jobs[:limit]]

    def cancel(self, job_id):
        with self.lock:
            job = self.jobs.get(job_id)
            if not job:
                raise APIError(HTTPStatus.NOT_FOUND, "任务不存在", "job_not_found")
            if job["status"] not in ("queued", "running"):
                raise APIError(HTTPStatus.CONFLICT, "任务已经结束", "job_finished")
            job["_cancel_requested"] = True
            if job["status"] == "queued":
                job["status"] = "cancelled"
                job["exit_code"] = -15
                job["finished_at"] = utc_timestamp()
                process = None
            else:
                job["status"] = "cancelling"
                process = job.get("_process")
        if process and process.poll() is None:
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
        return self.get(job_id)


class AdminApplication:
    def __init__(self, root=PROJECT_ROOT, key_file=None, control=None, job_manager=None):
        self.root = Path(root).resolve()
        self.control = control or cliproxy.ControlPlane(self.root)
        self.control.ensure_layout()
        self.key_file = Path(
            key_file
            or os.environ.get("CLIPROXY_MANAGEMENT_KEY_FILE", self.root / "secrets" / "cpa-management.key")
        )
        self.static_dir = Path(__file__).resolve().parent / "static"
        self.action_lock = threading.RLock()
        self.audit_lock = threading.RLock()
        self.jobs = job_manager or JobManager(self.root, self.action_lock)
        self.audit_path = self.root / "logs" / "admin" / "audit.jsonl"
        self.audit_path.parent.mkdir(parents=True, exist_ok=True)
        self.usage_limit_lock = threading.RLock()
        self.usage_limit_cache = None
        self.usage_limit_cache_expires_at = 0
        self.usage_limit_cache_fingerprint = None
        self.usage_limit_cache_generation = 0
        self.usage_limit_refreshing = False
        self.compose_ps_condition = threading.Condition(threading.RLock())
        self.compose_ps_cache = None
        self.compose_ps_cache_expires_at = 0
        self.compose_ps_loading = False
        self.compose_ps_generation = 0
        self.cpa_snapshot_condition = threading.Condition(threading.RLock())
        self.cpa_snapshot_cache = None
        self.cpa_snapshot_cache_expires_at = 0
        self.cpa_snapshot_cache_fingerprint = None
        self.cpa_snapshot_loading = False
        self.cpa_snapshot_generation = 0
        self.user_summary_cache_lock = threading.RLock()
        self.user_summary_cache = BoundedSWRCache(USER_SUMMARY_CACHE_MAX_ENTRIES)
        self.user_management_cache = {}
        self.user_management_cache_generation = 0
        self.self_service_lifetime_lock = threading.RLock()
        self.self_service_lifetime_cache = {}
        self.self_service_lifetime_refreshing = set()
        self.image_status_lock = threading.RLock()
        self.image_status_cache = None
        self.image_status_cache_expires_at = 0
        self.image_status_generation = 0
        self.release_status_lock = threading.RLock()
        self.release_status_cache = None
        self.release_status_cache_expires_at = 0
        self.public_usage_cache = BoundedSWRCache(PUBLIC_USAGE_CACHE_MAX_ENTRIES)
        self.admin_overview_cache = BoundedSWRCache(1)
        self.admin_overview_catalog_cache = BoundedSWRCache(1)
        self.admin_overview_usage_cache = BoundedSWRCache(
            ADMIN_READ_CACHE_MAX_ENTRIES
        )
        self.admin_accounts_cache = BoundedSWRCache(ADMIN_READ_CACHE_MAX_ENTRIES)
        self.request_metrics = threading.local()
        self.portal_login_limiter = AuthenticationRateLimiter()
        self.management_login_limiter = AuthenticationRateLimiter(
            threshold=6,
            base_block_seconds=60,
        )
        self.admin_sessions = AdminSessionStore()
        configuration = self.control.configuration()["values"]
        self.usage_store = UsageStore(
            self.root / "state" / "usage.sqlite3",
            week_timezone=configuration["user_quota.timezone"],
        )
        self._disable_legacy_default_portal_credentials()
        self.notifications = WeComNotificationService(self.root, store=self.control.store)
        self.account_failover = AccountFailoverService(self.root, store=self.control.store)

    def _begin_request_metrics(self):
        self.request_metrics.cache = []

    def _record_cache_metric(self, name, state, duration_ms):
        metrics = getattr(self.request_metrics, "cache", None)
        if metrics is not None:
            metrics.append((str(name), str(state), max(0.0, float(duration_ms))))

    def _server_timing_header(self, total_duration_ms):
        parts = ["total;dur={:.1f}".format(max(0.0, float(total_duration_ms)))]
        for index, (name, state, duration_ms) in enumerate(
            getattr(self.request_metrics, "cache", ())[:4]
        ):
            # Names and states come exclusively from fixed server-side labels.
            parts.append(
                'cache{};dur={:.1f};desc="{}-{}"'.format(
                    index + 1,
                    duration_ms,
                    name,
                    state,
                )
            )
        return ", ".join(parts)

    def _cached_read(
        self,
        cache,
        name,
        key,
        ttl_seconds,
        loader,
        *,
        force_refresh=False,
        stale_while_revalidate=True,
        with_state=False,
        cache_now=None,
    ):
        started_at = time.perf_counter()
        payload, state = cache.get(
            key,
            loader,
            ttl_seconds,
            force_refresh=force_refresh,
            stale_while_revalidate=stale_while_revalidate,
            now=cache_now,
        )
        self._record_cache_metric(
            name,
            state,
            (time.perf_counter() - started_at) * 1000,
        )
        return (payload, state) if with_state else payload

    @staticmethod
    def _release_version_key(value):
        match = re.fullmatch(
            r"v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?",
            str(value or "").strip(),
        )
        if match is None:
            return None
        prerelease = match.group(4)
        prerelease_key = ()
        if prerelease is not None:
            parts = prerelease.split(".")
            if any(not part for part in parts):
                return None
            if any(part.isdigit() and len(part) > 1 and part.startswith("0") for part in parts):
                return None
            prerelease_key = tuple(
                (0, int(part)) if part.isdigit() else (1, part)
                for part in parts
            )
        return (
            int(match.group(1)),
            int(match.group(2)),
            int(match.group(3)),
            1 if prerelease is None else 0,
            prerelease_key,
        )

    def release_status(self, force=False):
        current_version = os.environ.get("CLIPROXY_RELEASE_VERSION", "").strip()
        metadata_image = os.environ.get(
            "CLIPROXY_RELEASE_METADATA_IMAGE", ""
        ).strip()
        if not current_version or not metadata_image:
            return {
                "configured": False,
                "current_version": current_version,
                "available": False,
            }
        if RELEASE_IMAGE_RE.fullmatch(metadata_image) is None:
            return {
                "configured": True,
                "current_version": current_version,
                "available": False,
                "status": "invalid_configuration",
            }
        now = time.time()
        with self.release_status_lock:
            if (
                not force
                and self.release_status_cache is not None
                and now < self.release_status_cache_expires_at
            ):
                return dict(self.release_status_cache)
            try:
                subprocess.run(
                    ["docker", "pull", metadata_image],
                    check=True,
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.PIPE,
                    text=True,
                    timeout=RELEASE_STATUS_TIMEOUT_SECONDS,
                )
                inspected = subprocess.run(
                    [
                        "docker",
                        "image",
                        "inspect",
                        "--format",
                        "{{json .Config.Labels}}",
                        metadata_image,
                    ],
                    check=True,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    text=True,
                    timeout=5,
                )
                labels = json.loads(inspected.stdout)
                if not isinstance(labels, dict) or labels.get(
                    "io.codex-cpa.component"
                ) != "release":
                    raise ValueError("release metadata labels are invalid")
                latest_version = str(
                    labels.get("org.opencontainers.image.version") or ""
                ).strip()
                current_key = self._release_version_key(current_version)
                latest_key = self._release_version_key(latest_version)
                if current_key is None or latest_key is None:
                    raise ValueError("release version is invalid")
                payload = {
                    "configured": True,
                    "status": "ok",
                    "current_version": current_version,
                    "latest_version": latest_version,
                    "latest_revision": str(
                        labels.get("org.opencontainers.image.revision") or ""
                    )[:64],
                    "available": latest_key > current_key,
                    "checked_at": int(now),
                }
            except (
                OSError,
                ValueError,
                json.JSONDecodeError,
                subprocess.SubprocessError,
            ):
                payload = {
                    "configured": True,
                    "status": "unavailable",
                    "current_version": current_version,
                    "available": False,
                    "checked_at": int(now),
                }
            self.release_status_cache = dict(payload)
            self.release_status_cache_expires_at = now + RELEASE_STATUS_CACHE_SECONDS
            return payload

    def _configuration_value(self, key, fallback):
        try:
            return self.control.configuration()["values"].get(key, fallback)
        except (OSError, ValueError, TypeError, json.JSONDecodeError):
            return fallback

    def _portal_session_ttl_seconds(self):
        return min(
            PORTAL_SESSION_TTL_SECONDS,
            int(
                self._configuration_value(
                    "portal.session_ttl_seconds",
                    PORTAL_SESSION_TTL_SECONDS,
                )
            ),
        )

    def _disable_legacy_default_portal_credentials(self):
        disabled = []
        for credential in self.usage_store.portal_credentials_requiring_change():
            if verify_portal_password(
                LEGACY_DEFAULT_PORTAL_PASSWORD,
                credential["password_hash"],
            ):
                self.usage_store.delete_portal_identity(credential["user"])
                disabled.append(credential["user"])
        if disabled:
            self.audit(
                "security.portal-default-credentials.disable",
                "{} users".format(len(disabled)),
            )
        return len(disabled)

    def _read_management_key(self):
        try:
            key = self.key_file.read_text(encoding="utf-8").strip()
        except FileNotFoundError:
            key = self.control.management_key()
        if not key:
            raise ValueError("CPA 管理密钥不可用")
        return key

    def _usage_limit_timeout_seconds(self):
        return int(
            self._configuration_value(
                "usage.upstream_timeout_seconds",
                USAGE_LIMIT_TIMEOUT_SECONDS,
            )
        )

    def _usage_window_context(
        self,
        usage_window,
        now=None,
        custom_start_at=None,
        custom_end_at=None,
    ):
        generated_at = utc_timestamp() if now is None else int(now)
        if usage_window == CUSTOM_USAGE_WINDOW:
            start_at, end_at = validate_custom_usage_range(
                custom_start_at,
                custom_end_at,
                now=generated_at,
            )
            return {
                "generated_at": generated_at,
                "window": CUSTOM_USAGE_WINDOW,
                "window_seconds": end_at - start_at,
                "window_start_at": start_at,
                "window_end_at": end_at,
            }
        if usage_window == TODAY_USER_USAGE_WINDOW:
            timezone_name = self._configuration_value(
                "user_quota.timezone",
                "UTC",
            )
            local_now = datetime.fromtimestamp(
                generated_at,
                configured_timezone(timezone_name),
            )
            start_at = int(
                local_now.replace(
                    hour=0,
                    minute=0,
                    second=0,
                    microsecond=0,
                ).timestamp()
            )
            return {
                "generated_at": generated_at,
                "window": TODAY_USER_USAGE_WINDOW,
                "window_seconds": None,
                "window_start_at": start_at,
            }
        if usage_window == ACCOUNT_USAGE_SINCE_RESET:
            return {
                "generated_at": generated_at,
                "window": ACCOUNT_USAGE_SINCE_RESET,
                "window_seconds": None,
                "window_start_at": None,
            }
        if usage_window is None:
            return {
                "generated_at": generated_at,
                "window": "all",
                "window_seconds": None,
                "window_start_at": None,
            }
        window_seconds = int(usage_window)
        return {
            "generated_at": generated_at,
            "window": window_seconds,
            "window_seconds": window_seconds,
            "window_start_at": generated_at - window_seconds,
        }

    def _usage_limit_cache_seconds(self):
        return int(
            self._configuration_value(
                "usage.quota_cache_seconds",
                USAGE_LIMIT_CACHE_SECONDS,
            )
        )

    def authenticate(self, provided):
        try:
            expected = self._read_management_key()
        except (OSError, ValueError):
            return False
        provided = (provided or "").strip()
        return bool(expected and provided and hmac.compare_digest(expected, provided))

    def authenticate_management_request(self, provided, client_identity):
        provided = (provided or "").strip()
        if not provided:
            return False
        keys = ("management-ip:{}".format(client_identity or "unknown"),)
        retry_after = self.management_login_limiter.retry_after(keys)
        if retry_after:
            raise APIError(
                HTTPStatus.TOO_MANY_REQUESTS,
                "管理入口尝试过于频繁，请稍后重试",
                "rate_limited",
                headers=[("Retry-After", str(retry_after))],
            )
        if not self.authenticate(provided):
            retry_after = self.management_login_limiter.record_failure(keys)
            self.audit("admin.session.create", client_identity or "unknown", outcome="rejected")
            if retry_after:
                raise APIError(
                    HTTPStatus.TOO_MANY_REQUESTS,
                    "管理入口尝试过于频繁，请稍后重试",
                    "rate_limited",
                    headers=[("Retry-After", str(retry_after))],
                )
            return False
        self.management_login_limiter.clear(keys)
        return True

    def create_admin_session(self, client_identity):
        session = self.admin_sessions.create()
        self.audit("admin.session.create", client_identity or "unknown")
        return session

    def audit(self, action, target, outcome="accepted"):
        record = {
            "timestamp": utc_timestamp(),
            "action": action,
            "target": target,
            "outcome": outcome,
        }
        # Runtime jobs can hold action_lock for minutes while an administrator
        # completes OAuth. Audit writes use their own lock so accepting a job
        # never waits for that long-running command to finish.
        with self.audit_lock:
            with self.audit_path.open("a", encoding="utf-8") as handle:
                handle.write(json.dumps(record, ensure_ascii=False, separators=(",", ":")) + "\n")
            os.chmod(self.audit_path, 0o600)

    @staticmethod
    def key_payload(record, reveal=False):
        key = record.get("key", "")
        readable_prefix = key.rsplit("_", 1)[0] if "_" in key else "cpa"
        payload = {
            "label": record["label"],
            "account": record["account"],
            "account_email": record["account_email"],
            "user": record["user"],
            "status": record["status"],
            "created_at": record["created_at"],
            "updated_at": record["updated_at"],
            "preview": "{}_••••{}".format(readable_prefix, key[-4:]) if key else "",
        }
        if reveal:
            payload["key"] = key
        return payload

    def _initial_portal_password(self, *, required=False):
        try:
            value = self.control.store.read_secret(PORTAL_INITIAL_PASSWORD_SECRET)
        except (OSError, ValueError):
            value = None
        if not isinstance(value, str) or not (
            PORTAL_PASSWORD_MIN_LENGTH <= len(value) <= PORTAL_PASSWORD_MAX_LENGTH
        ) or hmac.compare_digest(value, LEGACY_DEFAULT_PORTAL_PASSWORD):
            if required:
                raise APIError(
                    HTTPStatus.SERVICE_UNAVAILABLE,
                    "用户初始密码尚未在安全设置中配置",
                    "initial_password_unavailable",
                )
            return None
        return value

    def _portal_password(self, value, *, new=False):
        if not isinstance(value, str):
            raise APIError(HTTPStatus.BAD_REQUEST, "密码格式无效", "invalid_password")
        if not value or len(value) > PORTAL_PASSWORD_MAX_LENGTH:
            raise APIError(HTTPStatus.BAD_REQUEST, "密码格式无效", "invalid_password")
        if new and len(value) < PORTAL_PASSWORD_MIN_LENGTH:
            raise APIError(
                HTTPStatus.BAD_REQUEST,
                "新密码至少需要 {} 位".format(PORTAL_PASSWORD_MIN_LENGTH),
                "weak_password",
            )
        if new and hmac.compare_digest(value, LEGACY_DEFAULT_PORTAL_PASSWORD):
            raise APIError(
                HTTPStatus.BAD_REQUEST,
                "不能使用已停用的历史默认密码",
                "weak_password",
            )
        return value

    def _portal_credential(self, user):
        return self.usage_store.portal_credential(user)

    def _active_records_for_portal_user(self, user):
        return sorted(
            (
                item
                for item in self.control.store.read_key_records_for_users([user])
                if item["status"] == "active"
            ),
            key=lambda item: item["label"],
        )

    def create_portal_session(self, email, password, client_identity=""):
        password = self._portal_password(password)
        try:
            user = self.control._normalize_user(str(email or ""))
        except ValueError:
            user = ""
        identity_digest = hashlib.sha256(
            str(email or "").strip().lower().encode("utf-8")
        ).hexdigest()[:16]
        rate_keys = (
            "portal-ip:{}".format(client_identity or "unknown"),
            "portal-account:{}".format(user or identity_digest),
        )
        retry_after = self.portal_login_limiter.retry_after(rate_keys)
        if retry_after:
            raise APIError(
                HTTPStatus.TOO_MANY_REQUESTS,
                "登录尝试过于频繁，请稍后重试",
                "rate_limited",
                headers=[("Retry-After", str(retry_after))],
            )
        active = self._active_records_for_portal_user(user)
        credential = self._portal_credential(user) if active else None
        password_matches = verify_portal_password(
            password,
            credential["password_hash"] if credential else DUMMY_PORTAL_PASSWORD_HASH,
        )
        if not active or not password_matches:
            retry_after = self.portal_login_limiter.record_failure(rate_keys)
            if user:
                self.audit("self.session.create", user, outcome="rejected")
            if retry_after:
                raise APIError(
                    HTTPStatus.TOO_MANY_REQUESTS,
                    "登录尝试过于频繁，请稍后重试",
                    "rate_limited",
                    headers=[("Retry-After", str(retry_after))],
                )
            raise APIError(HTTPStatus.UNAUTHORIZED, "邮箱或密码错误", "invalid_credentials")
        self.portal_login_limiter.clear(rate_keys)
        session = self.usage_store.create_session(
            user,
            ttl_seconds=self._portal_session_ttl_seconds(),
        )
        self.audit("self.session.create", user)
        return {
            "user": user,
            "password_change_required": credential["must_change"],
            **session,
        }

    def portal_session(self, token):
        session = self.usage_store.resolve_session(token)
        if not session:
            raise APIError(HTTPStatus.UNAUTHORIZED, "用户会话已失效", "session_required")
        if not self._active_records_for_portal_user(session["user"]):
            self.usage_store.revoke_session(token)
            raise APIError(HTTPStatus.UNAUTHORIZED, "用户已停用或删除", "session_required")
        credential = self._portal_credential(session["user"])
        if not credential:
            self.usage_store.revoke_session(token)
            raise APIError(HTTPStatus.UNAUTHORIZED, "用户凭据未初始化或已失效", "session_required")
        return {**session, "password_change_required": credential["must_change"]}

    def change_portal_password(self, token, current_password, new_password):
        session = self.portal_session(token)
        current_password = self._portal_password(current_password)
        new_password = self._portal_password(new_password, new=True)
        with self.action_lock:
            credential = self._portal_credential(session["user"])
            if not verify_portal_password(current_password, credential["password_hash"]):
                raise APIError(
                    HTTPStatus.UNAUTHORIZED,
                    "当前密码错误",
                    "invalid_current_password",
                )
            configured_initial_password = self._initial_portal_password()
            reuses_required_initial_password = credential["must_change"] and hmac.compare_digest(
                new_password,
                current_password,
            )
            reuses_configured_initial_password = (
                configured_initial_password is not None
                and hmac.compare_digest(new_password, configured_initial_password)
            )
            if reuses_required_initial_password or reuses_configured_initial_password:
                raise APIError(
                    HTTPStatus.BAD_REQUEST,
                    "新密码不能与初始密码相同",
                    "weak_password",
                )
            self.usage_store.set_portal_credential(
                session["user"],
                hash_portal_password(new_password),
                must_change=False,
                keep_session_token=token,
            )
            self.audit("self.password.change", session["user"])
        return {"message": "密码已修改", "password_change_required": False}

    def revoke_portal_session(self, token):
        self.usage_store.revoke_session(token)
        return {"logged_out": True}

    def self_service_route(self, user):
        """Return only the acknowledged current route for lightweight UI polling."""
        with self.action_lock:
            accounts = self.control.accounts()
            current_group = self.control.explicit_user_route(
                user,
                accounts=accounts,
            )
        return {
            "current_group": current_group,
            "generated_at": utc_timestamp(),
        }

    def _auto_assign_self_service_route(self, user, accounts, groups, activity):
        """Persist the least-loaded selectable route for one currently unbound user."""
        with self.action_lock:
            current_accounts = self.control.accounts()
            current_group = self.control.explicit_user_route(
                user,
                accounts=current_accounts,
            )
            if current_group:
                return current_group, None

            group_by_account = {item["account"]: item for item in groups}
            routed_users = self.control.routed_user_counts(accounts=current_accounts)
            candidates = []
            for position, (account, metadata) in enumerate(current_accounts.items()):
                group = group_by_account.get(account)
                if (
                    not metadata["group_enabled"]
                    or not group
                    or not group.get("operational_status", {}).get("selectable", False)
                ):
                    continue
                recent_users = int(
                    (activity.get(account) or {}).get("active_users") or 0
                )
                candidates.append(
                    (
                        recent_users,
                        int(routed_users.get(account, 0)),
                        position,
                        account,
                    )
                )

            if not candidates:
                return None, {
                    "status": "unavailable",
                    "message": "当前没有可自动分配的 CPA，系统会在下次刷新时重试",
                }

            recent_users, route_count, unused_position, account = min(candidates)
            try:
                self.control.set_user_routes(
                    {user: account},
                    wait_for_gateway=True,
                )
            except (OSError, RuntimeError, ValueError) as error:
                self.audit(
                    "self.group.auto-assign",
                    "{} -> {}".format(user, account),
                    outcome="rejected",
                )
                raise APIError(
                    HTTPStatus.SERVICE_UNAVAILABLE,
                    "自动分配 CPA 失败，请稍后刷新重试",
                    "route_assignment_failed",
                ) from error
            self.audit("self.group.auto-assign", "{} -> {}".format(user, account))
            return account, {
                "status": "assigned",
                "account": account,
                "active_users_1h": recent_users,
                "routed_users": route_count,
            }

    def _load_self_service_lifetime_usage(self, user, accounts):
        payload = self.usage_store.usage_for_users(
            [user],
            accounts,
            window_seconds=None,
        )[user]
        return {
            key: value
            for key, value in payload.items()
            if key != "accounts"
        }

    def _refresh_self_service_lifetime_usage_in_background(self, key, user, accounts):
        payload = None
        try:
            payload = self._load_self_service_lifetime_usage(user, accounts)
        except Exception:
            payload = None
        finally:
            with self.self_service_lifetime_lock:
                if payload is not None:
                    self.self_service_lifetime_cache[key] = (
                        time.monotonic() + SELF_SERVICE_LIFETIME_CACHE_SECONDS,
                        dict(payload),
                    )
                self.self_service_lifetime_refreshing.discard(key)

    def _cached_self_service_lifetime_usage(self, user, accounts):
        accounts = tuple(accounts)
        key = (str(user), accounts)
        refresh_thread = None
        with self.self_service_lifetime_lock:
            now = time.monotonic()
            cached = self.self_service_lifetime_cache.get(key)
            if cached is not None:
                if now >= cached[0] and key not in self.self_service_lifetime_refreshing:
                    self.self_service_lifetime_refreshing.add(key)
                    refresh_thread = threading.Thread(
                        target=self._refresh_self_service_lifetime_usage_in_background,
                        args=(key, user, accounts),
                        name="user-lifetime-refresh",
                        daemon=True,
                    )
                payload = dict(cached[1])
            else:
                payload = None
        if refresh_thread is not None:
            refresh_thread.start()
        if payload is not None:
            return payload

        payload = self._load_self_service_lifetime_usage(user, accounts)
        with self.self_service_lifetime_lock:
            self.self_service_lifetime_cache[key] = (
                time.monotonic() + SELF_SERVICE_LIFETIME_CACHE_SECONDS,
                dict(payload),
            )
            if len(self.self_service_lifetime_cache) > SELF_SERVICE_LIFETIME_CACHE_MAX_ENTRIES:
                oldest = min(
                    self.self_service_lifetime_cache,
                    key=lambda item: self.self_service_lifetime_cache[item][0],
                )
                self.self_service_lifetime_cache.pop(oldest, None)
        return dict(payload)

    def self_service_dashboard(
        self,
        user,
        usage_window_seconds=DEFAULT_USER_USAGE_WINDOW_SECONDS,
        force_quota_refresh=False,
        include_lifetime_usage=True,
    ):
        usage_window = self._usage_window_context(usage_window_seconds)
        accounts = self.control.accounts()
        records = self._active_records_for_portal_user(user)
        if not records:
            raise APIError(HTTPStatus.NOT_FOUND, "用户尚未开通 API Key", "user_not_found")
        keys = {item["key"] for item in records}
        if len(keys) != 1:
            raise APIError(
                HTTPStatus.CONFLICT,
                "用户 Key 正在迁移，请稍后刷新",
                "single_key_migration_required",
            )
        key = next(iter(keys))
        usage = self.usage_store.usage_for_users(
            [user],
            accounts.keys(),
            window_seconds=usage_window["window_seconds"],
            now=usage_window["generated_at"],
            start_at=usage_window["window_start_at"],
        )[user]
        today_window = (
            usage_window
            if usage_window["window"] == TODAY_USER_USAGE_WINDOW
            else self._usage_window_context(
                TODAY_USER_USAGE_WINDOW,
                now=usage_window["generated_at"],
            )
        )
        today_usage = (
            usage
            if usage_window["window"] == TODAY_USER_USAGE_WINDOW
            else self.usage_store.usage_for_users(
                [user],
                accounts.keys(),
                window_seconds=None,
                now=today_window["generated_at"],
                start_at=today_window["window_start_at"],
            )[user]
        )
        lifetime_usage = (
            self._cached_self_service_lifetime_usage(user, accounts.keys())
            if include_lifetime_usage
            else {}
        )
        activity = self.usage_store.account_activity(
            accounts.keys(),
            window_seconds=3600,
            now=usage_window["generated_at"],
        )
        user_quota = self.usage_store.weekly_quotas(
            [user],
            self.control.configuration()["values"]["user_quota.default_weekly_tokens"],
            now=usage_window["generated_at"],
        )[user]
        limit_payload = self.usage_limits(force_refresh=force_quota_refresh)
        limits = {
            item["account"]: item for item in limit_payload.get("accounts", [])
        }
        auth = self.control.auth_status()
        services = {
            item["service"]: item for item in self._compose_ps()
        }
        account_services = self.control.services()
        running_account_services = {
            account: service
            for account, service in account_services.items()
            if services.get(service, {}).get("state") == "running"
        }
        native_runtime = self._cached_cpa_management_snapshots(
            running_account_services
        )
        gateway_activity = self._gateway_error_activity(
            accounts.keys(),
            now=usage_window["generated_at"],
        )
        groups = []
        for account, metadata in accounts.items():
            quota = limits.get(account, {})
            weekly = quota.get("weekly") if isinstance(quota.get("weekly"), dict) else None
            service = services.get(account_services[account], {})
            container_running = service.get("state") == "running"
            auth_files = auth.get(account, {}).get("files", 0)
            runtime = self._account_runtime_snapshot(
                native_runtime.get(account),
                gateway_activity.get(account),
                usage_window["generated_at"],
            )
            operational_status = self._account_operational_status(
                group_enabled=metadata["group_enabled"],
                container_state=service.get("state", "missing"),
                auth_files=auth_files,
                quota=quota,
                runtime=runtime,
            )
            groups.append(
                {
                    "id": account,
                    "name": account,
                    "account": account,
                    "current": False,
                    "enabled": metadata["group_enabled"],
                    "status": operational_status["code"],
                    "operational_status": operational_status,
                    "container_running": container_running,
                    "oauth_configured": auth_files > 0,
                    "weekly": weekly,
                    "active_users_1h": activity[account]["active_users"],
                    "account_requests_1h": activity[account]["request_count"],
                    "usage": usage["accounts"][account],
                }
            )
        # Route failover can finish while the heavier usage/runtime queries above
        # are running. Read or assign the route last under the same action lock so
        # the response never publishes a pre-switch or uncommitted route.
        current_group, route_assignment = self._auto_assign_self_service_route(
            user,
            accounts,
            groups,
            activity,
        )
        for group in groups:
            group["current"] = group["account"] == current_group
        self.audit("self.dashboard.view", user)
        return {
            **usage_window,
            "quota_generated_at": limit_payload.get("generated_at"),
            "quota_cached": bool(limit_payload.get("cached")),
            "quota_refreshing": bool(limit_payload.get("refreshing")),
            "quota_cache_ttl_seconds": limit_payload.get("cache_ttl_seconds"),
            "user": user,
            "api_key": key,
            "current_group": current_group,
            "route_assignment": route_assignment,
            "collector": self.usage_store.status(),
            "lifetime_usage": lifetime_usage,
            "window_usage": {
                key: value for key, value in usage.items() if key != "accounts"
            },
            "today_usage": {
                key: value for key, value in today_usage.items() if key != "accounts"
            },
            "weekly_quota": user_quota,
            "groups": groups,
        }

    def switch_self_service_group(self, user, group_id):
        group_id = str(group_id or "").strip().lower()
        current = self.self_service_dashboard(user, DEFAULT_USER_USAGE_WINDOW_SECONDS)
        target = next((item for item in current["groups"] if item["id"] == group_id), None)
        if not target:
            raise APIError(HTTPStatus.NOT_FOUND, "CPA 账号不存在", "group_not_found")
        if not target.get("operational_status", {}).get("selectable", False):
            raise APIError(HTTPStatus.CONFLICT, "该 CPA 账号当前不可用", "group_unavailable")
        with self.action_lock:
            result = self.control.set_user_route(user, group_id)
            self.audit("self.group.switch", "{} -> {}".format(user, group_id))
        return result

    def rotate_self_service_key(self, user, confirm=False):
        if confirm is not True:
            raise APIError(
                HTTPStatus.BAD_REQUEST,
                "请确认刷新后旧 API Key 将立即失效",
                "confirmation_required",
            )
        with self.action_lock:
            active = self._active_records_for_portal_user(user)
            if not active:
                raise APIError(
                    HTTPStatus.NOT_FOUND,
                    "用户尚未开通 API Key",
                    "user_not_found",
                )
            # rotate_key replaces the user's unified Key across every CPA. The
            # requested label only selects the representative record it returns.
            record = self.control.rotate_key(active[0]["label"])
            self.audit("self.key.rotate", user)
        return {
            "message": "API Key 已刷新，旧 Key 已失效，请更新客户端配置",
            "api_key": record["key"],
            "updated_at": record["updated_at"],
        }

    def self_service_usage_breakdown(
        self,
        user,
        account,
        usage_window_seconds=DEFAULT_USER_USAGE_WINDOW_SECONDS,
    ):
        account = str(account or "").strip().lower()
        if not account:
            raise APIError(
                HTTPStatus.BAD_REQUEST,
                "缺少 CPA 账号",
                "account_required",
            )
        payload = self.user_usage_breakdown(
            user,
            usage_window_seconds=usage_window_seconds,
            account=account,
        )
        payload.pop("user", None)
        payload["definition"] = "user_account_model_reasoning_effort_tokens"
        return payload

    def _usage_limit_source_fingerprint(self, accounts):
        fingerprint = [
            (
                "control-plane:accounts",
                json.dumps(
                    accounts,
                    ensure_ascii=False,
                    sort_keys=True,
                    separators=(",", ":"),
                ),
                None,
            )
        ]
        paths = []
        for account in accounts:
            paths.append(self.root / "configs" / "{}.yaml".format(account))
            paths.extend(sorted((self.root / "auth" / account).glob("*.json")))
        for path in paths:
            try:
                stat = path.stat()
                fingerprint.append((str(path), stat.st_mtime_ns, stat.st_size))
            except OSError:
                fingerprint.append((str(path), None, None))
        return tuple(fingerprint)

    def _codex_auth_record(self, account):
        auth_dir = self.root / "auth" / account
        candidates = []
        for path in auth_dir.glob("*.json"):
            try:
                payload = json.loads(path.read_text(encoding="utf-8"))
                stat = path.stat()
            except (OSError, json.JSONDecodeError):
                continue
            if payload.get("type") != "codex" or payload.get("disabled"):
                continue
            if not payload.get("access_token"):
                continue
            candidates.append((stat.st_mtime_ns, payload))
        if not candidates:
            raise FileNotFoundError("Codex OAuth auth record is unavailable")
        return max(candidates, key=lambda item: item[0])[1]

    def _account_proxy_url(self, account):
        path = self.root / "configs" / "{}.yaml".format(account)
        try:
            lines = path.read_text(encoding="utf-8").splitlines()
        except OSError:
            return None
        raw_value = next(
            (
                line.split(":", 1)[1].strip()
                for line in lines
                if line.strip().startswith("proxy-url:")
            ),
            "",
        )
        if not raw_value:
            return None
        try:
            value = json.loads(raw_value)
        except json.JSONDecodeError:
            value = raw_value.strip("'\"")
        if not value or str(value).lower() in ("direct", "none"):
            return None
        parsed = urllib.parse.urlsplit(str(value))
        if parsed.scheme not in ("http", "https", "socks5") or not parsed.hostname:
            raise ValueError("invalid proxy URL")
        return str(value)

    @staticmethod
    def _official_opener(proxy_url):
        handlers = []
        if proxy_url:
            handlers.append(urllib.request.ProxyHandler({"http": proxy_url, "https": proxy_url}))
        return urllib.request.build_opener(*handlers)

    @staticmethod
    def _official_headers(access_token, account_id, content_type=None):
        headers = {
            "Authorization": "Bearer {}".format(access_token),
            "Accept": "application/json",
            "User-Agent": "cpa-control/1.0",
            "OAI-Language": "zh-CN",
            "Originator": "Codex Desktop",
            "Sec-Fetch-Site": "none",
            "Sec-Fetch-Mode": "no-cors",
            "Sec-Fetch-Dest": "empty",
            "Priority": "u=4, i",
        }
        if account_id:
            headers["ChatGPT-Account-Id"] = str(account_id)
        if content_type:
            headers["Content-Type"] = content_type
        return headers

    def _request_official_usage(self, access_token, account_id, proxy_url):
        opener = self._official_opener(proxy_url)
        headers = self._official_headers(access_token, account_id)
        request = urllib.request.Request(USAGE_LIMIT_URL, headers=headers)
        with opener.open(request, timeout=self._usage_limit_timeout_seconds()) as response:
            return json.loads(response.read().decode("utf-8"))

    def _request_official_reset_credits(self, access_token, account_id, proxy_url):
        opener = self._official_opener(proxy_url)
        headers = self._official_headers(access_token, account_id)
        request = urllib.request.Request(USAGE_LIMIT_RESET_CREDITS_URL, headers=headers)
        with opener.open(request, timeout=self._usage_limit_timeout_seconds()) as response:
            return json.loads(response.read().decode("utf-8"))

    def _request_official_quota_reset(self, access_token, account_id, proxy_url, credit_id):
        opener = self._official_opener(proxy_url)
        headers = self._official_headers(access_token, account_id, "application/json")
        data = json.dumps(
            {
                "redeem_request_id": str(uuid.uuid4()),
                "credit_id": credit_id,
            },
            separators=(",", ":"),
        ).encode("utf-8")
        request = urllib.request.Request(
            USAGE_LIMIT_RESET_URL,
            data=data,
            headers=headers,
            method="POST",
        )
        with opener.open(request, timeout=self._usage_limit_timeout_seconds()) as response:
            return json.loads(response.read().decode("utf-8"))

    @staticmethod
    def _weekly_window(payload):
        rate_limits = [payload.get("rate_limit") or {}]
        rate_limits.extend(
            item.get("rate_limit") or {} for item in payload.get("additional_rate_limits") or []
        )
        for rate_limit in rate_limits:
            for name in ("primary_window", "secondary_window"):
                window = rate_limit.get(name) or {}
                if window.get("limit_window_seconds") == WEEKLY_WINDOW_SECONDS:
                    return window
        return None

    @staticmethod
    def _nonnegative_int(value):
        try:
            return max(0, int(value))
        except (TypeError, ValueError):
            return None

    @classmethod
    def _account_usage_window_start(cls, quota, now):
        weekly = (quota or {}).get("weekly") if isinstance(quota, dict) else None
        if not isinstance(weekly, dict):
            return None
        window_seconds = cls._nonnegative_int(weekly.get("window_seconds"))
        if not window_seconds:
            window_seconds = WEEKLY_WINDOW_SECONDS
        reset_at = cls._nonnegative_int(weekly.get("reset_at"))
        if reset_at:
            start_at = reset_at - window_seconds
        else:
            reset_after_seconds = cls._nonnegative_int(
                weekly.get("reset_after_seconds")
            )
            if reset_after_seconds is None:
                return None
            start_at = int(now) + reset_after_seconds - window_seconds
        if start_at < 0 or start_at > int(now):
            return None
        return start_at

    @staticmethod
    def _timestamp(value):
        if isinstance(value, bool) or value is None:
            return None
        if isinstance(value, (int, float)):
            return max(0, int(value))
        raw = str(value).strip()
        if not raw:
            return None
        try:
            return max(0, int(raw))
        except ValueError:
            pass
        try:
            parsed = datetime.fromisoformat(raw.replace("Z", "+00:00"))
        except ValueError:
            return None
        if parsed.tzinfo is None:
            parsed = parsed.replace(tzinfo=timezone.utc)
        return max(0, int(parsed.timestamp()))

    @staticmethod
    def _bounded_text(value, limit):
        text = str(value or "").strip()
        return text[:limit] if text else None

    @classmethod
    def _normalize_reset_credits(cls, usage_summary, details=None):
        usage_summary = usage_summary if isinstance(usage_summary, dict) else {}
        details = details if isinstance(details, dict) else None
        source = details or usage_summary
        available_count = cls._nonnegative_int(source.get("available_count"))
        if available_count is None:
            available_count = cls._nonnegative_int(usage_summary.get("available_count"))
        applicable_count = cls._nonnegative_int(
            usage_summary.get("applicable_available_count")
        )
        total_earned_count = cls._nonnegative_int(source.get("total_earned_count"))

        credits = None
        if details is not None and isinstance(details.get("credits"), list):
            credits = []
            seen_ids = set()
            for item in details["credits"]:
                if not isinstance(item, dict):
                    continue
                credit_id = cls._bounded_text(item.get("id"), 512)
                status = cls._bounded_text(item.get("status"), 32)
                if (
                    not credit_id
                    or credit_id in seen_ids
                    or status != "available"
                    or item.get("is_supported_by_plan") is False
                ):
                    continue
                seen_ids.add(credit_id)
                credits.append(
                    {
                        "id": credit_id,
                        "reset_type": cls._bounded_text(item.get("reset_type"), 64),
                        "status": status,
                        "granted_at": cls._timestamp(item.get("granted_at")),
                        "expires_at": cls._timestamp(item.get("expires_at")),
                        "title": cls._bounded_text(item.get("title"), 120),
                        "description": cls._bounded_text(item.get("description"), 500),
                    }
                )
            credits.sort(
                key=lambda item: (
                    item["expires_at"] is None,
                    item["expires_at"] or 0,
                    item["granted_at"] or 0,
                    item["id"],
                )
            )

        normalized = {
            key: value
            for key, value in (
                ("available_count", available_count),
                ("applicable_available_count", applicable_count),
                ("total_earned_count", total_earned_count),
                ("credits", credits),
            )
            if value is not None
        }
        if credits is not None:
            normalized["listed_count"] = len(credits)
            normalized["details_truncated"] = bool(
                available_count is not None and len(credits) < available_count
            )
        return normalized or None

    @classmethod
    def _weekly_windows(cls, payload):
        reset_credits = payload.get("rate_limit_reset_credits") or {}
        applicable_count = cls._nonnegative_int(
            reset_credits.get("applicable_available_count")
        )
        reached_type = payload.get("rate_limit_reached_type") or {}
        reached_details = (
            str(reached_type.get("details") or "").strip().lower()
            if isinstance(reached_type, dict)
            else ""
        )
        sources = [
            {
                "source_key": "default",
                "label": "常规周限额",
                "metered_feature": None,
                "rate_limit": payload.get("rate_limit") or {},
                "aliases": {"default"},
            }
        ]
        for index, item in enumerate(payload.get("additional_rate_limits") or []):
            if not isinstance(item, dict):
                continue
            limit_name = str(item.get("limit_name") or "").strip()
            metered_feature = str(item.get("metered_feature") or "").strip()
            source_id = metered_feature or limit_name or "additional-{}".format(index + 1)
            aliases = {
                value.lower()
                for value in (source_id, limit_name, metered_feature)
                if value
            }
            sources.append(
                {
                    "source_key": "additional:{}".format(source_id),
                    "label": limit_name or metered_feature or "附加周限额 {}".format(index + 1),
                    "metered_feature": metered_feature or None,
                    "rate_limit": item.get("rate_limit") or {},
                    "aliases": aliases,
                }
            )

        windows = []
        for source in sources:
            rate_limit = source["rate_limit"]
            if not isinstance(rate_limit, dict):
                continue
            limit_reached = rate_limit.get("limit_reached") is True
            for slot in ("primary_window", "secondary_window"):
                window = rate_limit.get(slot) or {}
                if not isinstance(window, dict):
                    continue
                window_seconds = cls._nonnegative_int(window.get("limit_window_seconds"))
                if window_seconds != WEEKLY_WINDOW_SECONDS:
                    continue
                try:
                    used = max(0.0, min(float(window.get("used_percent", 0)), 100.0))
                except (TypeError, ValueError):
                    used = 0.0
                reported_used = round(used, 2)
                effective_used = 100.0 if limit_reached else reported_used
                windows.append(
                    {
                        "key": "{}:{}".format(source["source_key"], slot),
                        "label": source["label"],
                        "metered_feature": source["metered_feature"],
                        "window_slot": slot,
                        "used_percent": effective_used,
                        "remaining_percent": round(100.0 - effective_used, 2),
                        "reported_used_percent": reported_used,
                        "reset_at": cls._nonnegative_int(window.get("reset_at")),
                        "reset_after_seconds": cls._nonnegative_int(
                            window.get("reset_after_seconds")
                        ),
                        "window_seconds": window_seconds,
                        "limit_reached": limit_reached,
                        "resettable": bool(
                            applicable_count
                            and limit_reached
                            and reached_details in source["aliases"]
                        ),
                    }
                )
        return windows

    @classmethod
    def _normalize_usage_limit_payload(cls, account, payload, reset_credit_details=None):
        base = {
            "account": account,
            "status": "unavailable",
            "plan_type": None,
            "allowed": None,
            "limit_reached": None,
            "weekly": None,
            "weekly_windows": [],
            "reset_credits": None,
        }
        rate_limit = payload.get("rate_limit") or {}
        reset_credits = payload.get("rate_limit_reset_credits") or {}
        normalized_credits = cls._normalize_reset_credits(
            reset_credits,
            reset_credit_details,
        )
        weekly_windows = cls._weekly_windows(payload)
        return {
            **base,
            "status": "ok" if weekly_windows else "weekly_unavailable",
            "plan_type": payload.get("plan_type"),
            "allowed": rate_limit.get("allowed"),
            "limit_reached": rate_limit.get("limit_reached"),
            "weekly": dict(weekly_windows[0]) if weekly_windows else None,
            "weekly_windows": weekly_windows,
            "reset_credits": normalized_credits,
        }

    def _fetch_account_usage_limit(self, account):
        base = {
            "account": account,
            "status": "unavailable",
            "plan_type": None,
            "allowed": None,
            "limit_reached": None,
            "weekly": None,
            "weekly_windows": [],
            "reset_credits": None,
        }
        try:
            auth = self._codex_auth_record(account)
        except FileNotFoundError:
            return {**base, "status": "auth_missing"}
        try:
            proxy_url = self._account_proxy_url(account)
            payload = self._request_official_usage(
                auth.get("access_token"),
                auth.get("account_id"),
                proxy_url,
            )
            if not isinstance(payload, dict):
                raise TypeError("official usage payload must be an object")
            reset_credit_details = None
            reset_credit_summary = payload.get("rate_limit_reset_credits") or {}
            available_credits = max(
                self._nonnegative_int(reset_credit_summary.get("available_count")) or 0,
                self._nonnegative_int(
                    reset_credit_summary.get("applicable_available_count")
                ) or 0,
            )
            if available_credits:
                try:
                    reset_credit_details = self._request_official_reset_credits(
                        auth.get("access_token"),
                        auth.get("account_id"),
                        proxy_url,
                    )
                    if not isinstance(reset_credit_details, dict):
                        reset_credit_details = None
                except (
                    urllib.error.HTTPError,
                    urllib.error.URLError,
                    OSError,
                    ValueError,
                    TypeError,
                    AttributeError,
                    UnicodeDecodeError,
                    json.JSONDecodeError,
                ):
                    # The usage summary remains useful when the optional detail
                    # endpoint is temporarily unavailable. The admin UI disables
                    # selection until concrete credit IDs can be re-fetched.
                    reset_credit_details = None
            return self._normalize_usage_limit_payload(
                account,
                payload,
                reset_credit_details,
            )
        except urllib.error.HTTPError as error:
            status = "auth_expired" if error.code in (HTTPStatus.UNAUTHORIZED, HTTPStatus.FORBIDDEN) else "unavailable"
            return {**base, "status": status}
        except (
            urllib.error.URLError,
            OSError,
            ValueError,
            TypeError,
            AttributeError,
            UnicodeDecodeError,
            json.JSONDecodeError,
        ):
            return base

    def _load_usage_limits(self, account_names, cache_seconds):
        if account_names:
            with concurrent.futures.ThreadPoolExecutor(
                max_workers=min(4, len(account_names)),
                thread_name_prefix="usage-limit",
            ) as executor:
                account_limits = list(executor.map(self._fetch_account_usage_limit, account_names))
        else:
            account_limits = []
        return {
            "generated_at": utc_timestamp(),
            "cache_ttl_seconds": cache_seconds,
            "cached": False,
            "accounts": account_limits,
        }

    def _refresh_usage_limits_in_background(
        self,
        account_names,
        fingerprint,
        cache_seconds,
        generation,
    ):
        payload = None
        try:
            payload = self._load_usage_limits(account_names, cache_seconds)
        except Exception:
            # Individual upstream failures are normalized per account. Keep the
            # stale cache if the refresh machinery itself fails unexpectedly.
            payload = None
        finally:
            with self.usage_limit_lock:
                if generation == self.usage_limit_cache_generation:
                    if payload is not None:
                        self.usage_limit_cache = payload
                        self.usage_limit_cache_fingerprint = fingerprint
                        self.usage_limit_cache_expires_at = time.monotonic() + cache_seconds
                    self.usage_limit_refreshing = False

    def usage_limits(self, force_refresh=False):
        accounts = self.control.accounts()
        account_names = list(accounts)
        fingerprint = self._usage_limit_source_fingerprint(accounts)
        now = time.monotonic()
        cache_seconds = self._usage_limit_cache_seconds()
        refresh_thread = None
        with self.usage_limit_lock:
            matching_cache = (
                isinstance(self.usage_limit_cache, dict)
                and self.usage_limit_cache_fingerprint == fingerprint
            )
            cached_age = (
                max(0, utc_timestamp() - int(self.usage_limit_cache.get("generated_at") or 0))
                if matching_cache
                else None
            )
            force_allowed = bool(
                force_refresh
                and cached_age is not None
                and cached_age >= USAGE_LIMIT_FORCE_MIN_AGE_SECONDS
            )
            refresh_requested = (
                not matching_cache
                or now >= self.usage_limit_cache_expires_at
                or force_allowed
            )
            if refresh_requested and not self.usage_limit_refreshing:
                self.usage_limit_refreshing = True
                refresh_thread = threading.Thread(
                    target=self._refresh_usage_limits_in_background,
                    args=(
                        account_names,
                        fingerprint,
                        cache_seconds,
                        self.usage_limit_cache_generation,
                    ),
                    name="usage-limit-refresh",
                    daemon=True,
                )
            if isinstance(self.usage_limit_cache, dict):
                cached_accounts = {
                    item.get("account"): item
                    for item in self.usage_limit_cache.get("accounts", [])
                    if isinstance(item, dict) and item.get("account") in accounts
                }
                payload = {
                    **self.usage_limit_cache,
                    "cached": True,
                    "refreshing": self.usage_limit_refreshing,
                    "accounts": [
                        cached_accounts[account]
                        for account in account_names
                        if account in cached_accounts
                    ],
                }
            else:
                payload = {
                    "generated_at": None,
                    "cache_ttl_seconds": cache_seconds,
                    "cached": False,
                    "refreshing": self.usage_limit_refreshing,
                    "accounts": [],
                }
        if refresh_thread is not None:
            refresh_thread.start()
        return payload

    def refresh_usage_limits_sync(self):
        """Fetch a fresh complete quota snapshot for a confirmed routing mutation."""
        accounts = self.control.accounts()
        account_names = list(accounts)
        fingerprint = self._usage_limit_source_fingerprint(accounts)
        cache_seconds = self._usage_limit_cache_seconds()
        with self.usage_limit_lock:
            self.usage_limit_cache_generation += 1
            generation = self.usage_limit_cache_generation
            self.usage_limit_refreshing = True
        try:
            payload = self._load_usage_limits(account_names, cache_seconds)
        except Exception:
            with self.usage_limit_lock:
                if generation == self.usage_limit_cache_generation:
                    self.usage_limit_refreshing = False
            raise
        with self.usage_limit_lock:
            if generation == self.usage_limit_cache_generation:
                self.usage_limit_cache = payload
                self.usage_limit_cache_fingerprint = fingerprint
                self.usage_limit_cache_expires_at = time.monotonic() + cache_seconds
                self.usage_limit_refreshing = False
        return {**payload, "refreshing": False}

    def _invalidate_usage_limit_cache(self):
        with self.usage_limit_lock:
            self.usage_limit_cache_generation += 1
            self.usage_limit_cache = None
            self.usage_limit_cache_expires_at = 0
            self.usage_limit_cache_fingerprint = None
            self.usage_limit_refreshing = False

    def public_usage_limits(self):
        payload = self.usage_limits()
        accounts = []
        for account in payload.get("accounts", []):
            public_account = {
                key: value for key, value in account.items() if key != "reset_credits"
            }
            if isinstance(public_account.get("weekly"), dict):
                public_account["weekly"] = {
                    key: value
                    for key, value in public_account["weekly"].items()
                    if key != "resettable"
                }
            public_account["weekly_windows"] = [
                {key: value for key, value in window.items() if key != "resettable"}
                for window in public_account.get("weekly_windows", [])
                if isinstance(window, dict)
            ]
            accounts.append(public_account)
        return {
            **payload,
            "accounts": accounts,
        }

    def users(
        self,
        usage_window_seconds=DEFAULT_USER_USAGE_WINDOW_SECONDS,
        usage_start_at=None,
        now=None,
        usage_end_at=None,
        user_emails=None,
    ):
        accounts = self.control.accounts()
        records = (
            self.control._read_registry()
            if user_emails is None
            else self.control.store.read_key_records_for_users(user_emails)
        )
        grouped = {}
        records_by_user = {}
        records_by_user_account = {}
        for record in records:
            user = record["user"]
            records_by_user.setdefault(user, []).append(record)
            records_by_user_account.setdefault((user, record["account"]), []).append(
                record
            )
            item = grouped.setdefault(
                user,
                {
                    "email": user,
                    "status": "inactive",
                    "active_keys": 0,
                    "total_records": 0,
                    "created_at": record["created_at"],
                    "updated_at": record["updated_at"],
                    "accounts": [],
                },
            )
            item["total_records"] += 1
            item["created_at"] = min(item["created_at"], record["created_at"])
            item["updated_at"] = max(item["updated_at"], record["updated_at"])

        usage = self.usage_store.usage_for_users(
            grouped.keys(),
            accounts.keys(),
            window_seconds=usage_window_seconds,
            now=now,
            start_at=usage_start_at,
            end_at=usage_end_at,
        )
        quota_now = int(time.time()) if now is None else int(now)
        quotas = self.usage_store.weekly_quotas(
            grouped.keys(),
            self.control.configuration()["values"]["user_quota.default_weekly_tokens"],
            now=quota_now,
        )
        classifications = self.control.store.read_user_classifications(grouped.keys())

        for user, item in grouped.items():
            user_records = records_by_user[user]
            active_user_records = [record for record in user_records if record["status"] == "active"]
            user_usage = usage[user]
            for account, metadata in accounts.items():
                account_records = records_by_user_account.get((user, account), [])
                active = [record for record in account_records if record["status"] == "active"]
                latest = max(account_records, key=lambda record: record["created_at"]) if account_records else None
                if active:
                    current = max(active, key=lambda record: record["created_at"])
                    status = "active"
                elif latest:
                    current = latest
                    status = latest["status"]
                else:
                    current = None
                    status = "missing"
                account_payload = {
                    "account": account,
                    "account_email": metadata["email"],
                    "status": status,
                    "history_count": len(account_records),
                    "key": self.key_payload(current) if current else None,
                    "usage": user_usage["accounts"][account],
                }
                item["accounts"].append(account_payload)
            item["active_keys"] = len({record["key"] for record in active_user_records})
            item["status"] = "active" if item["active_keys"] else "inactive"
            item["usage"] = {
                key: value for key, value in user_usage.items() if key != "accounts"
            }
            item["weekly_quota"] = quotas[user]
            item.update(classifications[user])
        return sorted(grouped.values(), key=lambda item: item["email"])

    def _cached_user_usage_summaries(
        self,
        usage_window_seconds,
        usage_start_at,
        usage_end_at,
        now,
    ):
        key = (
            usage_window_seconds,
            usage_start_at,
            usage_end_at,
        )
        return self._cached_read(
            self.user_summary_cache,
            "user-summary",
            key,
            USER_SUMMARY_CACHE_SECONDS,
            lambda: self.usage_store.usage_summaries_for_users(
                window_seconds=usage_window_seconds,
                now=now,
                start_at=usage_start_at,
                end_at=usage_end_at,
            ),
            # The outer user-page cache must not extend an expired aggregate.
            # Concurrent cold/expired requests therefore wait for one loader.
            stale_while_revalidate=False,
        )

    @staticmethod
    def _paginate(items, page, page_size):
        page_size = int(page_size or 50)
        if page_size not in (25, 50, 100):
            raise APIError(
                HTTPStatus.BAD_REQUEST,
                "每页数量只支持 25、50 或 100",
                "invalid_page_size",
            )
        page = max(1, int(page or 1))
        total = len(items)
        total_pages = max(1, (total + page_size - 1) // page_size)
        page = min(page, total_pages)
        start = (page - 1) * page_size
        return items[start : start + page_size], {
            "page": page,
            "page_size": page_size,
            "total": total,
            "total_pages": total_pages,
        }

    def user_management_page(
        self,
        usage_window_seconds=DEFAULT_USER_USAGE_WINDOW_SECONDS,
        custom_start_at=None,
        custom_end_at=None,
        page=1,
        page_size=50,
        search="",
        sort="tokens",
        direction="desc",
        team_id="",
        tag_id="",
        usage_state="all",
        tag_membership="",
    ):
        usage_window = self._usage_window_context(
            usage_window_seconds,
            custom_start_at=custom_start_at,
            custom_end_at=custom_end_at,
        )
        custom_window = usage_window["window"] == CUSTOM_USAGE_WINDOW
        bounded_start_window = (
            custom_window
            or usage_window["window"] == TODAY_USER_USAGE_WINDOW
        )
        query_start_at = (
            usage_window["window_start_at"] if bounded_start_window else None
        )
        query_end_at = usage_window["window_end_at"] if custom_window else None
        cache_key = (
            usage_window["window_seconds"],
            query_start_at,
            query_end_at,
        )
        monotonic_now = time.monotonic()
        with self.user_summary_cache_lock:
            cache_generation = self.user_management_cache_generation
            cached = self.user_management_cache.get(cache_key)
            cache_hit = bool(cached and monotonic_now < cached[0])
            items = (
                [dict(item) for item in cached[1]]
                if cache_hit
                else None
            )
            summary_generated_at = int(cached[2]) if cache_hit else None
        if items is None:
            summaries = self.control.store.read_user_summaries()
            users = [item["email"] for item in summaries]
            usage = self._cached_user_usage_summaries(
                usage_window["window_seconds"],
                query_start_at,
                query_end_at,
                usage_window["generated_at"],
            )
            default_limit = self.control.configuration()["values"][
                "user_quota.default_weekly_tokens"
            ]
            quotas = self.usage_store.weekly_quotas(
                users,
                default_limit,
                now=usage_window["generated_at"],
            )
            account_count = len(self.control.accounts())
            empty_usage = {
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
            items = []
            for summary in summaries:
                email = summary["email"]
                active_keys = int(summary["active_keys"])
                items.append(
                    {
                        **summary,
                        "status": "active" if active_keys else "inactive",
                        "account_count": account_count,
                        "usage": dict(usage.get(email, empty_usage)),
                        "weekly_quota": quotas[email],
                    }
                )
            with self.user_summary_cache_lock:
                if cache_generation == self.user_management_cache_generation:
                    self.user_management_cache[cache_key] = (
                        monotonic_now + USER_SUMMARY_CACHE_SECONDS,
                        [dict(item) for item in items],
                        usage_window["generated_at"],
                    )
                summary_generated_at = usage_window["generated_at"]
                if (
                    cache_generation == self.user_management_cache_generation
                    and len(self.user_management_cache) > 8
                ):
                    oldest = min(
                        self.user_management_cache,
                        key=lambda item: self.user_management_cache[item][0],
                    )
                    self.user_management_cache.pop(oldest, None)
        classifications = self.control.store.read_user_classifications(
            item["email"] for item in items
        )
        for item in items:
            item.update(classifications[item["email"]])
        normalized_team_id = str(team_id or "").strip()
        if normalized_team_id:
            if normalized_team_id == "unassigned":
                items = [item for item in items if not item["team_id"]]
            else:
                items = [item for item in items if item["team_id"] == normalized_team_id]
        normalized_tag_id = str(tag_id or "").strip()
        if normalized_tag_id:
            normalized_tag_membership = str(tag_membership or "tagged").strip().lower()
            if normalized_tag_membership not in ("tagged", "untagged"):
                raise APIError(
                    HTTPStatus.BAD_REQUEST,
                    "标签成员范围无效",
                    "invalid_tag_membership",
                )
            items = [
                item for item in items
                if (
                    normalized_tag_id in {tag["id"] for tag in item["tags"]}
                ) == (normalized_tag_membership == "tagged")
            ]
        normalized_usage_state = str(usage_state or "all").strip().lower()
        if normalized_usage_state not in ("all", "used", "unused"):
            raise APIError(
                HTTPStatus.BAD_REQUEST,
                "Token 状态无效",
                "invalid_usage_state",
            )
        if normalized_usage_state != "all":
            items = [
                item for item in items
                if (int(item["usage"]["total_tokens"] or 0) > 0)
                == (normalized_usage_state == "used")
            ]
        normalized_search = str(search or "").strip().lower()
        if normalized_search:
            items = [
                item for item in items
                if normalized_search in item["email"].lower()
                or normalized_search in str((item.get("team") or {}).get("name") or "").lower()
                or any(normalized_search in tag["name"].lower() for tag in item["tags"])
            ]
        sort = str(sort or "tokens").strip().lower()
        direction = str(direction or "desc").strip().lower()
        if direction not in ("asc", "desc"):
            raise APIError(
                HTTPStatus.BAD_REQUEST,
                "排序方向无效",
                "invalid_sort_direction",
            )
        sort_values = {
            "email": lambda item: item["email"],
            "requests": lambda item: item["usage"]["request_count"],
            "tokens": lambda item: item["usage"]["weighted_tokens"],
            "last_used": lambda item: item["usage"]["last_used_at"] or None,
            "quota": lambda item: item["weekly_quota"].get("used_tokens"),
        }
        if sort not in sort_values:
            raise APIError(
                HTTPStatus.BAD_REQUEST,
                "排序字段无效",
                "invalid_sort_field",
            )
        value_for = sort_values[sort]
        items.sort(key=lambda item: item["email"])
        available = [item for item in items if value_for(item) is not None]
        unavailable = [item for item in items if value_for(item) is None]
        available.sort(key=value_for, reverse=direction == "desc")
        items = available + unavailable
        page_items, pagination = self._paginate(items, page, page_size)
        return {
            **usage_window,
            "users": page_items,
            "accounts": self.control.accounts(),
            "teams": self.control.store.list_teams(),
            "tags": self.control.store.list_tags(),
            "collector": self.usage_store.status(),
            "pagination": pagination,
            "summary_cached": cache_hit,
            "summary_generated_at": summary_generated_at,
        }

    def user_management_detail(
        self,
        email,
        usage_window_seconds=DEFAULT_USER_USAGE_WINDOW_SECONDS,
        custom_start_at=None,
        custom_end_at=None,
    ):
        user = self.control._normalize_user(str(email or ""))
        usage_window = self._usage_window_context(
            usage_window_seconds,
            custom_start_at=custom_start_at,
            custom_end_at=custom_end_at,
        )
        users = self.users(
            usage_window["window_seconds"],
            usage_start_at=usage_window["window_start_at"],
            now=usage_window["generated_at"],
            usage_end_at=(
                usage_window["window_end_at"]
                if usage_window["window"] == CUSTOM_USAGE_WINDOW
                else None
            ),
            user_emails=[user],
        )
        if not users:
            raise APIError(HTTPStatus.NOT_FOUND, "用户不存在", "user_not_found")
        return {**usage_window, "user": users[0]}

    def user_management(
        self,
        usage_window_seconds=DEFAULT_USER_USAGE_WINDOW_SECONDS,
        custom_start_at=None,
        custom_end_at=None,
    ):
        usage_window = self._usage_window_context(
            usage_window_seconds,
            custom_start_at=custom_start_at,
            custom_end_at=custom_end_at,
        )
        return {
            **usage_window,
            "users": self.users(
                usage_window["window_seconds"],
                usage_start_at=usage_window["window_start_at"],
                now=usage_window["generated_at"],
                usage_end_at=(
                    usage_window["window_end_at"]
                    if usage_window["window"] == CUSTOM_USAGE_WINDOW
                    else None
                ),
            ),
            "accounts": self.control.accounts(),
            "collector": self.usage_store.status(),
        }

    def organization_catalog(self):
        return {
            "teams": self.control.store.list_teams(),
            "tags": self.control.store.list_tags(),
        }

    def create_team(self, body):
        with self.action_lock:
            team = self.control.store.create_team(
                body.get("name"),
                body.get("description", ""),
            )
            self.audit("team.create", team["id"])
        return {"message": "团队已创建", "team": team}

    def update_team(self, body):
        with self.action_lock:
            team = self.control.store.update_team(
                body.get("id"),
                body.get("name"),
                body.get("description", ""),
            )
            self.audit("team.update", team["id"])
        return {"message": "团队已更新", "team": team}

    def delete_team(self, team_id):
        with self.action_lock:
            team = self.control.store.delete_team(team_id)
            self.audit("team.delete", team["id"])
        return {"message": "团队已删除", "team": team}

    def create_tag(self, body):
        with self.action_lock:
            tag = self.control.store.create_tag(
                body.get("name"),
                body.get("color", "#6374d8"),
            )
            self.audit("tag.create", tag["id"])
        return {"message": "标签已创建", "tag": tag}

    def update_tag(self, body):
        with self.action_lock:
            tag = self.control.store.update_tag(
                body.get("id"),
                body.get("name"),
                body.get("color", "#6374d8"),
            )
            self.audit("tag.update", tag["id"])
        return {"message": "标签已更新", "tag": tag}

    def delete_tag(self, tag_id):
        with self.action_lock:
            tag = self.control.store.delete_tag(tag_id)
            self.audit("tag.delete", tag["id"])
        return {"message": "标签已删除", "tag": tag}

    def _known_users(self):
        return {record["user"] for record in self.control._read_registry()}

    def update_user_team(self, body, batch=False):
        requested = body.get("users") if batch else [body.get("email")]
        if not isinstance(requested, list):
            raise ValueError("用户列表必须为数组")
        users = sorted(
            {
                self.control._normalize_user(str(item or ""))
                for item in requested
                if str(item or "").strip()
            }
        )
        if not users:
            raise ValueError("请选择用户")
        if len(users) > 500:
            raise ValueError("单次团队分配不能超过 500 位用户")
        missing = [user for user in users if user not in self._known_users()]
        if missing:
            raise APIError(
                HTTPStatus.NOT_FOUND,
                "用户不存在：{}".format("、".join(missing[:3])),
                "user_not_found",
            )
        team_id = str(body.get("team_id") or "").strip() or None
        expected_team_id = body.get("expected_team_id")
        if (
            "expected_team_id" in body
            and expected_team_id is not None
            and not isinstance(expected_team_id, str)
        ):
            raise ValueError("预期团队无效")
        if "expected_team_id" in body:
            expected_team_id = str(expected_team_id or "").strip() or None
        with self.action_lock:
            if "expected_team_id" in body:
                current = self.control.store.read_user_classifications(users)
                conflicts = [
                    user for user in users
                    if current[user]["team_id"] != expected_team_id
                ]
                if conflicts:
                    raise APIError(
                        HTTPStatus.CONFLICT,
                        "有 {} 位用户的团队归属已变化，请刷新后重试：{}".format(
                            len(conflicts), "、".join(conflicts[:3])
                        ),
                        "team_membership_conflict",
                    )
            assignments = self.control.store.set_user_teams(users, team_id)
            classifications = self.control.store.read_user_classifications(users)
            self.usage_store.sync_user_teams(classifications)
            target = users[0] if len(users) == 1 else "selected:{}".format(len(users))
            self.audit("user.team.update", "{}:{}".format(target, team_id or "unassigned"))
        return {
            "message": "已更新 {} 位用户的团队归属；团队用量已按当前成员动态统计".format(len(users)),
            "assignments": assignments,
            "classifications": classifications,
        }

    def update_user_tags_batch(self, body):
        requested = body.get("users")
        if not isinstance(requested, list):
            raise ValueError("用户列表必须为数组")
        users = sorted(
            {
                self.control._normalize_user(str(item or ""))
                for item in requested
                if str(item or "").strip()
            }
        )
        if not users:
            raise ValueError("请选择用户")
        if len(users) > 500:
            raise ValueError("单次标签分配不能超过 500 位用户")
        missing = [user for user in users if user not in self._known_users()]
        if missing:
            raise APIError(
                HTTPStatus.NOT_FOUND,
                "用户不存在：{}".format("、".join(missing[:3])),
                "user_not_found",
            )
        assigned = body.get("assigned")
        if not isinstance(assigned, bool):
            raise ValueError("标签操作必须指定 assigned 布尔值")
        tag_id = str(body.get("tag_id") or "").strip()
        with self.action_lock:
            classifications = self.control.store.update_user_tag_memberships(
                users, tag_id, assigned
            )
            self.audit(
                "user.tags.{}".format("add" if assigned else "remove"),
                "selected:{}:{}".format(len(users), tag_id),
            )
        return {
            "message": "已为 {} 位用户{}标签".format(
                len(users), "添加" if assigned else "移除"
            ),
            "classifications": classifications,
        }

    def update_user_tags(self, body):
        user = self.control._normalize_user(str(body.get("email") or ""))
        if user not in self._known_users():
            raise APIError(HTTPStatus.NOT_FOUND, "用户不存在", "user_not_found")
        with self.action_lock:
            tags = self.control.store.set_user_tags(user, body.get("tag_ids"))
            self.audit("user.tags.update", "{}:{}".format(user, len(tags)))
        return {
            "message": "用户标签已更新",
            "user": user,
            "tags": tags,
        }

    def team_usage_management(
        self,
        usage_window_seconds=DEFAULT_USER_USAGE_WINDOW_SECONDS,
        custom_start_at=None,
        custom_end_at=None,
    ):
        usage_window = self._usage_window_context(
            usage_window_seconds,
            custom_start_at=custom_start_at,
            custom_end_at=custom_end_at,
        )
        teams = self.control.store.list_teams()
        known_users = sorted(self._known_users())
        classifications = self.control.store.read_user_classifications(known_users)
        current_team_by_user = {
            user: item["team_id"]
            for user, item in classifications.items()
        }
        custom_window = usage_window["window"] == CUSTOM_USAGE_WINDOW
        query_start_at = (
            usage_window["window_start_at"]
            if custom_window or usage_window["window"] == TODAY_USER_USAGE_WINDOW
            else None
        )
        query_end_at = usage_window.get("window_end_at") if custom_window else None
        # /users and /teams/usage are fetched together by the browser. Sharing
        # this per-user aggregate avoids two concurrent scans of usage_events.
        usage_by_user = self._cached_user_usage_summaries(
            usage_window["window_seconds"],
            query_start_at,
            query_end_at,
            usage_window["generated_at"],
        )
        usage = self.usage_store.usage_for_teams(
            [team["id"] for team in teams],
            current_team_by_user,
            window_seconds=usage_window["window_seconds"],
            now=usage_window["generated_at"],
            start_at=query_start_at,
            end_at=query_end_at,
            usage_by_user=usage_by_user,
        )
        unassigned_count = sum(
            1 for item in classifications.values() if not item["team_id"]
        )
        rows = [
            {
                **team,
                "current_user_count": team["user_count"],
                "usage": usage[team["id"]],
            }
            for team in teams
        ]
        rows.append(
            {
                "id": "unassigned",
                "name": "未分组",
                "description": "尚未分配团队的当前用户",
                "user_count": unassigned_count,
                "current_user_count": unassigned_count,
                "usage": usage["unassigned"],
            }
        )
        rows.sort(
            key=lambda item: (
                -int(item["usage"]["weighted_tokens"]),
                item["name"].lower(),
            )
        )
        return {
            **usage_window,
            "attribution": "current_membership",
            "teams": rows,
        }

    def team_usage_breakdown(
        self,
        team_id,
        usage_window_seconds=DEFAULT_USER_USAGE_WINDOW_SECONDS,
        custom_start_at=None,
        custom_end_at=None,
    ):
        normalized_team_id = str(team_id or "").strip()
        valid = {team["id"] for team in self.control.store.list_teams()}
        if normalized_team_id != "unassigned" and normalized_team_id not in valid:
            raise APIError(HTTPStatus.NOT_FOUND, "团队不存在", "team_not_found")
        usage_window = self._usage_window_context(
            usage_window_seconds,
            custom_start_at=custom_start_at,
            custom_end_at=custom_end_at,
        )
        known_users = sorted(self._known_users())
        classifications = self.control.store.read_user_classifications(known_users)
        current_users = [
            user
            for user, item in classifications.items()
            if (
                (not item["team_id"] and normalized_team_id == "unassigned")
                or item["team_id"] == normalized_team_id
            )
        ]
        return {
            "definition": "team_model_reasoning_effort_tokens",
            **usage_window,
            **self.usage_store.team_usage_breakdown(
                normalized_team_id,
                current_users,
                window_seconds=usage_window["window_seconds"],
                now=usage_window["generated_at"],
                start_at=usage_window["window_start_at"],
                end_at=(
                    usage_window["window_end_at"]
                    if usage_window["window"] == CUSTOM_USAGE_WINDOW
                    else None
                ),
            ),
        }

    def user_quota(self, email):
        user = self.control._normalize_user(str(email or ""))
        known_users = {record["user"] for record in self.control._read_registry()}
        if user not in known_users:
            raise APIError(HTTPStatus.NOT_FOUND, "用户不存在", "user_not_found")
        default_limit = self.control.configuration()["values"][
            "user_quota.default_weekly_tokens"
        ]
        return {
            "user": user,
            "weekly_quota": self.usage_store.weekly_quotas(
                [user], default_limit
            )[user],
            "adjustments": self.usage_store.quota_adjustment_history(user),
        }

    def update_user_quota(self, body):
        user = self.control._normalize_user(str(body.get("email") or ""))
        known_users = {record["user"] for record in self.control._read_registry()}
        if user not in known_users:
            raise APIError(HTTPStatus.NOT_FOUND, "用户不存在", "user_not_found")
        mode = str(body.get("mode") or "").strip().lower()
        with self.action_lock:
            self.usage_store.set_quota_policy(
                user,
                mode,
                body.get("weekly_tokens"),
                created_by="admin",
            )
            self.audit("user.quota.update", "{}:{}".format(user, mode))
        payload = self.user_quota(user)
        payload["message"] = "用户周额度策略已保存，将在下次采集后生效"
        return payload

    def clear_user_quota(self, email):
        user = self.control._normalize_user(str(email or ""))
        known_users = {record["user"] for record in self.control._read_registry()}
        if user not in known_users:
            raise APIError(HTTPStatus.NOT_FOUND, "用户不存在", "user_not_found")
        with self.action_lock:
            self.usage_store.clear_quota_policy(user)
            self.audit("user.quota.inherit", user)
        payload = self.user_quota(user)
        payload["message"] = "已恢复继承组织默认周额度，将在下次采集后生效"
        return payload

    def user_quota_action_summary(self, now=None):
        now = int(time.time()) if now is None else int(now)
        users = sorted(
            {record["user"] for record in self.control._read_registry()}
        )
        if not users:
            return {
                "total_users": 0,
                "users_with_usage": 0,
                "total_used_tokens": 0,
                "total_raw_used_tokens": 0,
                "users_with_personal_policy": 0,
                "users_with_bonus": 0,
                "users_with_usage_reset": 0,
                "week_start_at": None,
                "week_end_at": None,
            }
        quotas = self.usage_store.weekly_quotas(
            users,
            self.control.configuration()["values"][
                "user_quota.default_weekly_tokens"
            ],
            now=now,
        )
        first = quotas[users[0]]
        return {
            "total_users": len(users),
            "users_with_usage": sum(
                1 for quota in quotas.values() if quota["used_tokens"] > 0
            ),
            "total_used_tokens": sum(
                int(quota["used_tokens"]) for quota in quotas.values()
            ),
            "total_raw_used_tokens": sum(
                int(quota["raw_used_tokens"]) for quota in quotas.values()
            ),
            "users_with_personal_policy": sum(
                1
                for quota in quotas.values()
                if quota["policy_mode"] != "inherit"
            ),
            "users_with_bonus": sum(
                1 for quota in quotas.values() if quota["bonus_tokens"] > 0
            ),
            "users_with_usage_reset": sum(
                1
                for quota in quotas.values()
                if quota["usage_reset_tokens"] > 0
            ),
            "week_start_at": first["week_start_at"],
            "week_end_at": first["week_end_at"],
        }

    def update_user_quota_actions(self, body):
        action = str(body.get("action") or "").strip().lower()
        if action not in ("restore_default", "add_bonus", "reset_usage"):
            raise ValueError("额度操作必须为恢复默认、追加额度或清零本周用量")
        scope = str(body.get("scope") or "selected").strip().lower()
        known_users = sorted(
            {record["user"] for record in self.control._read_registry()}
        )
        known_user_set = set(known_users)
        if scope == "all":
            if action != "reset_usage":
                raise ValueError("全员操作仅支持清零本周已用量")
            users = known_users
        elif scope == "selected":
            requested = body.get("users")
            if not isinstance(requested, list):
                raise ValueError("请选择用户")
            users = sorted(
                {
                    self.control._normalize_user(str(item or ""))
                    for item in requested
                    if str(item or "").strip()
                }
            )
        else:
            raise ValueError("额度操作范围无效")
        if not users:
            raise ValueError("请选择用户")
        if scope == "selected" and len(users) > 500:
            raise ValueError("单次额度操作不能超过 500 位用户")
        missing = [user for user in users if user not in known_user_set]
        if missing:
            raise APIError(
                HTTPStatus.NOT_FOUND,
                "用户不存在：{}".format("、".join(missing[:3])),
                "user_not_found",
            )

        expected_confirm = {
            "restore_default": "restore_default",
            "add_bonus": "add_bonus",
            "reset_usage": (
                "reset_all_current_week_usage"
                if scope == "all"
                else "reset_current_week_usage"
            ),
        }[action]
        if str(body.get("confirm") or "") != expected_confirm:
            raise ValueError("请确认额度操作")

        with self.action_lock:
            if action == "restore_default":
                changed = self.usage_store.clear_quota_policies(users)
                result = {
                    "action": action,
                    "applied_users": users,
                    "skipped_users": [],
                    "changed_policies": changed,
                }
                message = "已将 {} 位用户恢复为继承组织默认额度".format(
                    len(users)
                )
            elif action == "add_bonus":
                quotas = self.usage_store.weekly_quotas(
                    users,
                    self.control.configuration()["values"][
                        "user_quota.default_weekly_tokens"
                    ],
                )
                unlimited = [
                    user for user, quota in quotas.items() if quota["unlimited"]
                ]
                if unlimited:
                    raise ValueError(
                        "不限额用户无需追加额度：{}".format(
                            "、".join(unlimited[:3])
                        )
                    )
                token_amount = self.usage_store.normalize_quota_adjustment_tokens(
                    body.get("token_amount")
                )
                oversized = [
                    user
                    for user, quota in quotas.items()
                    if int(quota["limit_tokens"]) + token_amount
                    > MAX_WEEKLY_QUOTA_TOKENS
                ]
                if oversized:
                    raise ValueError(
                        "追加后周额度不能超过 {:,} Token：{}".format(
                            MAX_WEEKLY_QUOTA_TOKENS,
                            "、".join(oversized[:3]),
                        )
                    )
                result = self.usage_store.add_quota_bonus(
                    users,
                    token_amount,
                    body.get("reason"),
                    created_by="admin",
                )
                message = (
                    "已为 {} 位用户追加本周额度；将在下次采集后生效".format(
                        len(users)
                    )
                )
            else:
                result = self.usage_store.reset_weekly_usage(
                    users,
                    body.get("reason"),
                    created_by="admin",
                )
                applied_count = len(result["applied_users"])
                skipped_count = len(result["skipped_users"])
                message = (
                    "已清零 {} 位用户的本周已用量；将在下次采集后生效".format(
                        applied_count
                    )
                )
                if skipped_count:
                    message += "；{} 位用户当前用量为 0，已跳过".format(
                        skipped_count
                    )
            target = (
                "all:{}".format(len(users))
                if scope == "all"
                else (
                    users[0]
                    if len(users) == 1
                    else "selected:{}".format(len(users))
                )
            )
            self.audit("user.quota.{}".format(action), target)

        return {
            **result,
            "message": message,
            "quota_operations": self.user_quota_action_summary(),
        }

    def user_usage_breakdown(
        self,
        email,
        usage_window_seconds=DEFAULT_USER_USAGE_WINDOW_SECONDS,
        account=None,
        custom_start_at=None,
        custom_end_at=None,
    ):
        user = self.control._normalize_user(str(email or ""))
        known_users = {record["user"] for record in self.control._read_registry()}
        if user not in known_users:
            raise APIError(HTTPStatus.NOT_FOUND, "用户不存在", "user_not_found")
        account = str(account or "").strip().lower()
        accounts = self.control.accounts()
        if account and account not in accounts:
            raise APIError(HTTPStatus.NOT_FOUND, "CPA 账号不存在", "account_not_found")
        usage_window = self._usage_window_context(
            usage_window_seconds,
            custom_start_at=custom_start_at,
            custom_end_at=custom_end_at,
        )
        breakdown = self.usage_store.usage_breakdown_for_user(
            user,
            window_seconds=usage_window["window_seconds"],
            now=usage_window["generated_at"],
            start_at=usage_window["window_start_at"],
            account=account or None,
            end_at=(
                usage_window["window_end_at"]
                if usage_window["window"] == CUSTOM_USAGE_WINDOW
                else None
            ),
        )
        return {
            **usage_window,
            "user": user,
            "account": account or None,
            "definition": "successful_model_requests",
            **breakdown,
        }

    def account_usage_breakdown(
        self,
        account,
        usage_window_seconds=DEFAULT_USER_USAGE_WINDOW_SECONDS,
        custom_start_at=None,
        custom_end_at=None,
    ):
        account = str(account or "").strip().lower()
        accounts = self.control.accounts()
        if account not in accounts:
            raise APIError(HTTPStatus.NOT_FOUND, "CPA 账号不存在", "account_not_found")

        usage_window = self._usage_window_context(
            usage_window_seconds,
            custom_start_at=custom_start_at,
            custom_end_at=custom_end_at,
        )
        if usage_window_seconds == ACCOUNT_USAGE_SINCE_RESET:
            limit_payload = self.usage_limits()
            quota = next(
                (
                    item
                    for item in limit_payload.get("accounts", [])
                    if item.get("account") == account
                ),
                None,
            )
            window_start_at = self._account_usage_window_start(
                quota,
                usage_window["generated_at"],
            )
            if window_start_at is None:
                raise APIError(
                    HTTPStatus.CONFLICT,
                    "未获得该 CPA 的额度周期边界，请刷新额度后重试",
                    "usage_window_unavailable",
                )
            usage_window["window_start_at"] = window_start_at

        breakdown = self.usage_store.usage_breakdown_for_account(
            account,
            window_seconds=usage_window["window_seconds"],
            now=usage_window["generated_at"],
            start_at=usage_window["window_start_at"],
            end_at=(
                usage_window["window_end_at"]
                if usage_window["window"] == CUSTOM_USAGE_WINDOW
                else None
            ),
        )
        return {
            **usage_window,
            "account": account,
            "definition": "account_model_reasoning_effort_tokens",
            **breakdown,
        }

    def _management_key(self):
        try:
            return self._read_management_key()
        except (OSError, ValueError) as error:
            raise RuntimeError("CPA management key is unavailable") from error

    def _request_cpa_management(self, service, path):
        request = urllib.request.Request(
            "http://{}:8317/v0/management{}".format(service, path),
            headers={
                "Authorization": "Bearer {}".format(self._management_key()),
                "Accept": "application/json",
            },
        )
        with urllib.request.urlopen(
            request,
            timeout=CPA_MANAGEMENT_TIMEOUT_SECONDS,
        ) as response:
            raw = response.read(CPA_MANAGEMENT_MAX_BODY_BYTES + 1)
        if len(raw) > CPA_MANAGEMENT_MAX_BODY_BYTES:
            raise ValueError("CPA management response is too large")
        payload = json.loads(raw.decode("utf-8"))
        if not isinstance(payload, dict):
            raise TypeError("CPA management response must be an object")
        return payload

    def _cpa_management_snapshot(self, service):
        snapshot = {
            "query_status": "unavailable",
            "credential_status": "unknown",
            "credential_count": 0,
            "credential_unavailable": False,
            "credential_unavailable_count": 0,
            "credential_disabled": False,
            "credential_disabled_count": 0,
            "status_message": "",
            "native_success": 0,
            "native_failed": 0,
            "error_log_files": 0,
            "error_log_status": "unavailable",
        }
        try:
            auth_payload = self._request_cpa_management(service, "/auth-files")
        except Exception as error:
            snapshot["query_error"] = redact(
                "{}: {}".format(type(error).__name__, error)
            )[:240]
            return snapshot

        files = auth_payload.get("files")
        credentials = [item for item in files or [] if isinstance(item, dict)]
        unavailable = [
            item
            for item in credentials
            if item.get("unavailable") is True or item.get("disabled") is True
        ]
        disabled = [item for item in credentials if item.get("disabled") is True]
        status_messages = [
            str(item.get("status_message") or "").strip()
            for item in credentials
            if str(item.get("status_message") or "").strip()
        ]
        recent = [
            request
            for item in credentials
            for request in item.get("recent_requests") or []
            if isinstance(request, dict)
        ]
        states = {
            str(item.get("status") or "").strip().lower()
            for item in credentials
        }
        if not credentials:
            credential_status = "missing"
        elif len(unavailable) == len(credentials):
            credential_status = "unavailable"
        elif unavailable or any(state not in ("", "active") for state in states):
            credential_status = "degraded"
        else:
            credential_status = "active"
        snapshot.update(
            {
                "query_status": "ok",
                "credential_status": credential_status,
                "credential_count": len(credentials),
                "credential_unavailable": credential_status == "unavailable",
                "credential_unavailable_count": len(unavailable),
                "credential_disabled": bool(credentials)
                and len(disabled) == len(credentials),
                "credential_disabled_count": len(disabled),
                "status_message": redact(status_messages[0])[:240]
                if status_messages
                else "",
                "native_success": sum(
                    max(0, int(item.get("success") or 0)) for item in recent
                ),
                "native_failed": sum(
                    max(0, int(item.get("failed") or 0)) for item in recent
                ),
            }
        )

        try:
            error_payload = self._request_cpa_management(
                service,
                "/request-error-logs",
            )
            error_files = error_payload.get("files")
            snapshot["error_log_files"] = len(error_files) if isinstance(error_files, list) else 0
            snapshot["error_log_status"] = "ok"
        except Exception as error:
            snapshot["error_log_error"] = redact(
                "{}: {}".format(type(error).__name__, error)
            )[:240]
        return snapshot

    def _cpa_management_snapshots(self, account_services):
        snapshots = {}
        if not account_services:
            return snapshots
        with concurrent.futures.ThreadPoolExecutor(
            max_workers=min(CPA_MANAGEMENT_MAX_WORKERS, len(account_services)),
            thread_name_prefix="cpa-runtime",
        ) as executor:
            futures = {
                account: executor.submit(self._cpa_management_snapshot, service)
                for account, service in account_services.items()
            }
            for account, future in futures.items():
                try:
                    snapshots[account] = future.result()
                except Exception as error:
                    snapshots[account] = {
                        "query_status": "unavailable",
                        "credential_status": "unknown",
                        "query_error": redact(
                            "{}: {}".format(type(error).__name__, error)
                        )[:240],
                    }
        return snapshots

    def _refresh_cpa_management_snapshots_in_background(
        self,
        account_services,
        fingerprint,
        generation,
    ):
        snapshots = None
        try:
            snapshots = self._cpa_management_snapshots(account_services)
        except Exception:
            snapshots = None
        finally:
            with self.cpa_snapshot_condition:
                if generation == self.cpa_snapshot_generation and snapshots is not None:
                    self.cpa_snapshot_cache = {
                        account: dict(snapshot)
                        for account, snapshot in snapshots.items()
                    }
                    self.cpa_snapshot_cache_fingerprint = fingerprint
                    self.cpa_snapshot_cache_expires_at = (
                        time.monotonic() + CPA_RUNTIME_SNAPSHOT_CACHE_SECONDS
                    )
                self.cpa_snapshot_loading = False
                self.cpa_snapshot_condition.notify_all()

    def _cached_cpa_management_snapshots(self, account_services):
        fingerprint = tuple(sorted(
            (str(account), str(service))
            for account, service in account_services.items()
        ))
        refresh_thread = None
        with self.cpa_snapshot_condition:
            now = time.monotonic()
            matching_cache = (
                self.cpa_snapshot_cache is not None
                and self.cpa_snapshot_cache_fingerprint == fingerprint
            )
            if matching_cache and now < self.cpa_snapshot_cache_expires_at:
                return {
                    account: dict(snapshot)
                    for account, snapshot in self.cpa_snapshot_cache.items()
                }
            if not self.cpa_snapshot_loading:
                self.cpa_snapshot_loading = True
                refresh_thread = threading.Thread(
                    target=self._refresh_cpa_management_snapshots_in_background,
                    args=(
                        dict(account_services),
                        fingerprint,
                        self.cpa_snapshot_generation,
                    ),
                    name="cpa-runtime-refresh",
                    daemon=True,
                )
            cached_services = dict(self.cpa_snapshot_cache_fingerprint or ())
            snapshots = {
                account: dict(self.cpa_snapshot_cache[account])
                for account, service in account_services.items()
                if (
                    isinstance(self.cpa_snapshot_cache, dict)
                    and cached_services.get(account) == service
                    and account in self.cpa_snapshot_cache
                )
            }
        if refresh_thread is not None:
            refresh_thread.start()
        return snapshots

    def _gateway_error_activity(self, accounts, now=None):
        now = int(time.time()) if now is None else int(now)
        accounts = list(accounts)
        activity = {
            account: {
                "window_seconds": CPA_RUNTIME_ERROR_WINDOW_SECONDS,
                "requests": 0,
                "error_count": 0,
                "rate_429_count": 0,
                "server_error_count": 0,
                "affected_users": 0,
                "last_error_at": 0,
                "last_error_status": 0,
            }
            for account in accounts
        }
        affected = {account: set() for account in accounts}
        try:
            rows = self.control.recent_access_rows(
                CPA_RUNTIME_ERROR_WINDOW_SECONDS,
                now=now,
            )
        except (OSError, ValueError):
            return activity
        for timestamp, label, account, status in rows:
            if status is None or account not in activity:
                continue
            item = activity[account]
            item["requests"] += 1
            if status not in (
                HTTPStatus.UNAUTHORIZED,
                HTTPStatus.FORBIDDEN,
                HTTPStatus.TOO_MANY_REQUESTS,
            ) and status < 500:
                continue
            item["error_count"] += 1
            if status == HTTPStatus.TOO_MANY_REQUESTS:
                item["rate_429_count"] += 1
            if status >= 500:
                item["server_error_count"] += 1
            if ":" in label:
                affected[account].add(label.rsplit(":", 1)[0])
            if timestamp >= item["last_error_at"]:
                item["last_error_at"] = int(timestamp)
                item["last_error_status"] = status
        for account, item in activity.items():
            item["affected_users"] = len(affected[account])
            item["error_rate_percent"] = (
                round(item["error_count"] * 100.0 / item["requests"], 2)
                if item["requests"]
                else 0.0
            )
        return activity

    @staticmethod
    def _account_runtime_snapshot(native, activity, now):
        native = dict(native or {})
        activity = dict(activity or {})
        last_error_at = int(activity.get("last_error_at") or 0)
        error_age = max(0, int(now) - last_error_at) if last_error_at else None
        if (
            native.get("credential_unavailable")
            or (
                native.get("query_status") == "ok"
                and native.get("credential_status") == "missing"
            )
        ):
            state = "unavailable"
        elif (
            activity.get("rate_429_count")
            and error_age is not None
            and error_age <= CPA_ACTIVE_ERROR_SECONDS
        ):
            state = "rate_limited"
        elif activity.get("error_count"):
            state = "degraded"
        elif native.get("credential_status") == "degraded":
            state = "degraded"
        elif native.get("credential_status") == "active":
            state = "healthy"
        else:
            state = "unknown"
        return {
            **native,
            **activity,
            "state": state,
            "error_age_seconds": error_age,
        }

    @staticmethod
    def _runtime_unavailable_due_to_quota(runtime):
        raw = str((runtime or {}).get("status_message") or "").strip()
        if not raw:
            return False
        try:
            payload = json.loads(raw)
        except (TypeError, ValueError, json.JSONDecodeError):
            return "usage_limit_reached" in raw.lower()
        error = payload.get("error") if isinstance(payload, dict) else None
        if not isinstance(error, dict):
            return False
        return any(
            str(error.get(field) or "").strip().lower() == "usage_limit_reached"
            for field in ("type", "code")
        )

    @staticmethod
    def _runtime_unavailable_due_to_invalid_credential(runtime):
        raw = str((runtime or {}).get("status_message") or "").strip().lower()
        if not raw:
            return False
        invalid_markers = (
            "invalid_grant",
            "refresh_token_invalidated",
            "refresh_token_expired",
            "invalid_refresh_token",
            "invalid_token",
            "invalid_api_key",
            "authentication_error",
            "authentication_required",
            "oauth_token_expired",
            "access_denied",
            "unauthorized",
            "forbidden",
        )
        if any(marker in raw for marker in invalid_markers):
            return True
        return "refresh token" in raw and any(
            marker in raw for marker in ("invalid", "expired", "revoked")
        )

    @staticmethod
    def _runtime_unavailable_due_to_transient_error(runtime):
        raw = str((runtime or {}).get("status_message") or "").strip().lower()
        if not raw:
            return False
        transient_markers = (
            "service_unavailable",
            "server_error",
            "internal_server_error",
            "bad_gateway",
            "gateway_timeout",
            "request_timeout",
            "upstream_error",
            "connection reset",
            "connection refused",
            "connection timed out",
        )
        if any(marker in raw for marker in transient_markers):
            return True
        return any(
            marker in raw
            for marker in (
                '"status":408',
                '"status": 408',
                '"status":500',
                '"status": 500',
                '"status":502',
                '"status": 502',
                '"status":503',
                '"status": 503',
                '"status":504',
                '"status": 504',
            )
        )

    @staticmethod
    def _operational_status(code, label, tone, reason, selectable):
        return {
            "code": code,
            "label": label,
            "tone": tone,
            "reason": reason,
            "selectable": bool(selectable),
        }

    @classmethod
    def _account_operational_status(
        cls,
        *,
        group_enabled,
        container_state,
        auth_files,
        quota,
        runtime,
    ):
        quota = dict(quota or {})
        runtime = dict(runtime or {})
        weekly = quota.get("weekly") if isinstance(quota.get("weekly"), dict) else None
        if not group_enabled:
            return cls._operational_status(
                "disabled", "已停用", "neutral", "账号已停用", False
            )
        if container_state != "running":
            return cls._operational_status(
                "stopped", "已停止", "danger", "CPA 容器未运行", False
            )
        if int(auth_files or 0) <= 0:
            return cls._operational_status(
                "auth_missing", "未授权", "danger", "OAuth 尚未授权", False
            )
        quota_exhausted = (
            quota.get("allowed") is False
            or quota.get("limit_reached") is True
            or bool(weekly and weekly.get("limit_reached") is True)
            or cls._runtime_unavailable_due_to_quota(runtime)
        )
        if quota_exhausted:
            return cls._operational_status(
                "quota_exhausted", "额度耗尽", "danger", "账号周额度已耗尽", False
            )
        if runtime.get("state") == "unavailable":
            if (
                runtime.get("credential_status") != "missing"
                and runtime.get("credential_disabled") is not True
                and not cls._runtime_unavailable_due_to_invalid_credential(runtime)
                and cls._runtime_unavailable_due_to_transient_error(runtime)
            ):
                return cls._operational_status(
                    "transient_cooldown",
                    "临时冷却",
                    "warning",
                    "上游请求临时失败，CPA 正在等待凭据冷却恢复",
                    True,
                )
            return cls._operational_status(
                "credential_unavailable",
                "凭据不可用",
                "danger",
                "OAuth 凭据已失效，需要重新授权",
                False,
            )
        if runtime.get("state") == "rate_limited":
            return cls._operational_status(
                "rate_limited",
                "限流中",
                "warning",
                "账号近期出现 429，仍可选择并稍后重试",
                True,
            )
        if runtime.get("state") == "degraded":
            return cls._operational_status(
                "degraded", "近期异常", "warning", "账号近期出现请求异常", True
            )
        if runtime.get("state") == "unknown":
            return cls._operational_status(
                "unknown", "状态未知", "neutral", "CPA 原生状态暂不可查询", True
            )
        if quota.get("status") != "ok" or weekly is None:
            return cls._operational_status(
                "quota_unknown", "额度未知", "neutral", "额度状态暂不可确认", True
            )
        try:
            remaining = float(weekly.get("remaining_percent"))
        except (TypeError, ValueError):
            remaining = None
        if remaining is not None and remaining <= 10:
            return cls._operational_status(
                "quota_warning",
                "注意额度",
                "warning",
                "周额度剩余不高于 10%",
                True,
            )
        return cls._operational_status(
            "available", "可用", "success", "容器、OAuth 与额度均正常", True
        )

    def _admin_usage_cache_scope(
        self,
        usage_window,
        custom_start_at=None,
        custom_end_at=None,
    ):
        if usage_window == TODAY_USER_USAGE_WINDOW:
            return self._usage_window_context(TODAY_USER_USAGE_WINDOW)[
                "window_start_at"
            ]
        if usage_window == CUSTOM_USAGE_WINDOW:
            return (custom_start_at, custom_end_at)
        return None

    def account_management(
        self,
        usage_window_seconds=DEFAULT_USER_USAGE_WINDOW_SECONDS,
        force_quota_refresh=False,
        custom_start_at=None,
        custom_end_at=None,
    ):
        key = (
            usage_window_seconds,
            custom_start_at,
            custom_end_at,
            self._admin_usage_cache_scope(
                usage_window_seconds,
                custom_start_at,
                custom_end_at,
            ),
        )
        payload, cache_state = self._cached_read(
            self.admin_accounts_cache,
            "accounts",
            key,
            ADMIN_ACCOUNTS_CACHE_SECONDS,
            lambda: self._load_account_management(
                usage_window_seconds,
                force_quota_refresh=force_quota_refresh,
                custom_start_at=custom_start_at,
                custom_end_at=custom_end_at,
            ),
            force_refresh=force_quota_refresh,
            with_state=True,
        )
        if force_quota_refresh and cache_state == "refresh":
            # Preserve the existing UI contract while the whole account view is
            # refreshed asynchronously from a stale snapshot.
            payload["quota_refreshing"] = True
        return payload

    def _load_account_management(
        self,
        usage_window_seconds=DEFAULT_USER_USAGE_WINDOW_SECONDS,
        force_quota_refresh=False,
        custom_start_at=None,
        custom_end_at=None,
    ):
        usage_window = self._usage_window_context(
            usage_window_seconds,
            custom_start_at=custom_start_at,
            custom_end_at=custom_end_at,
        )
        configured_accounts = self.control.accounts()
        account_services = self.control.services()
        active_records = self.control.active_records()
        active_users = sorted({item["user"] for item in active_records})
        active_records_by_account = {account: [] for account in configured_accounts}
        for record in active_records:
            if record["account"] in active_records_by_account:
                active_records_by_account[record["account"]].append(record)
        limit_payload = self.usage_limits(force_refresh=force_quota_refresh)
        limits = {
            item["account"]: item for item in limit_payload.get("accounts", [])
        }
        usage_window_start_at_by_account = None
        if usage_window_seconds == ACCOUNT_USAGE_SINCE_RESET:
            usage_window_start_at_by_account = {
                account: start_at
                for account in configured_accounts
                if (start_at := self._account_usage_window_start(
                    limits.get(account), usage_window["generated_at"]
                )) is not None
            }
            usage = self.usage_store.usage_for_accounts(
                configured_accounts.keys(),
                now=usage_window["generated_at"],
                start_at_by_account=usage_window_start_at_by_account,
            )
        else:
            usage = self.usage_store.usage_for_accounts(
                configured_accounts.keys(),
                window_seconds=usage_window["window_seconds"],
                now=usage_window["generated_at"],
                start_at=usage_window["window_start_at"],
                end_at=(
                    usage_window["window_end_at"]
                    if usage_window["window"] == CUSTOM_USAGE_WINDOW
                    else None
                ),
            )
        activity = self.usage_store.account_activity(
            configured_accounts.keys(),
            window_seconds=3600,
            now=usage_window["generated_at"],
            include_user_emails=True,
        )
        auth = self.control.auth_status()
        services = {item["service"]: item for item in self._compose_ps()}
        running_account_services = {
            account: service
            for account, service in account_services.items()
            if services.get(service, {}).get("state") == "running"
        }
        native_runtime = self._cached_cpa_management_snapshots(
            running_account_services
        )
        gateway_activity = self._gateway_error_activity(
            configured_accounts.keys(),
            now=usage_window["generated_at"],
        )
        routes = self.control.explicit_user_routes(
            active_users,
            accounts=configured_accounts,
        )
        accounts = []
        for account, metadata in configured_accounts.items():
            proxy = self.control.account_proxy_configuration(account, metadata)
            account_records = active_records_by_account[account]
            service = services.get(account_services[account], {})
            usage_window_start_at = (
                usage_window_start_at_by_account.get(account)
                if usage_window_start_at_by_account is not None
                else usage_window["window_start_at"]
            )
            auth_files = auth.get(account, {}).get("files", 0)
            runtime = self._account_runtime_snapshot(
                native_runtime.get(account),
                gateway_activity.get(account),
                usage_window["generated_at"],
            )
            operational_status = self._account_operational_status(
                group_enabled=metadata["group_enabled"],
                container_state=service.get("state", "missing"),
                auth_files=auth_files,
                quota=limits.get(account, {}),
                runtime=runtime,
            )
            accounts.append(
                {
                    "id": account,
                    "group_name": metadata["group_name"],
                    "email": metadata["email"],
                    "port": metadata["port"],
                    "proxy_mode": proxy["mode"],
                    "proxy_configured": proxy["configured"],
                    "proxy_source": proxy["source"],
                    "proxy_display": self.control.redact_proxy_url(proxy["effective_url"]),
                    "created_at": metadata["created_at"],
                    "group_enabled": metadata["group_enabled"],
                    "default_group": account == self.control.default_group(configured_accounts),
                    "service": account_services[account],
                    "container_state": service.get("state", "missing"),
                    "container_status": service.get("status", ""),
                    "container_health": service.get("health", ""),
                    "auth_files": auth_files,
                    "auth_state": (
                        "configured"
                        if auth_files
                        else "pending"
                    ),
                    "associated_users": len({item["user"] for item in account_records}),
                    "routed_users": sum(route == account for route in routes.values()),
                    "active_users_1h": activity[account]["active_users"],
                    "active_user_emails_1h": activity[account]["active_user_emails"],
                    "quota": limits.get(account, {}),
                    "usage": usage[account],
                    "usage_window_start_at": usage_window_start_at,
                    "usage_window_available": (
                        True
                        if usage_window_start_at_by_account is None
                        else usage_window_start_at is not None
                    ),
                    "runtime": runtime,
                    "operational_status": operational_status,
                }
            )
        return {
            **usage_window,
            "quota_generated_at": limit_payload.get("generated_at"),
            "quota_cached": bool(limit_payload.get("cached")),
            "quota_refreshing": bool(limit_payload.get("refreshing")),
            "quota_cache_ttl_seconds": limit_payload.get("cache_ttl_seconds"),
            "window_start_at_by_account": usage_window_start_at_by_account,
            "accounts": accounts,
            "collector": self.usage_store.status(),
        }

    def cliproxy_image_status(self):
        now = time.monotonic()
        with self.image_status_lock:
            if (
                self.image_status_cache is not None
                and now < self.image_status_cache_expires_at
            ):
                return {**self.image_status_cache, "cached": True}
            generation = self.image_status_generation
        payload = self.control.cliproxy_image_status()
        with self.image_status_lock:
            if generation == self.image_status_generation:
                self.image_status_cache = dict(payload)
                self.image_status_cache_expires_at = time.monotonic() + 30
        return {**payload, "cached": False}

    def _load_compose_ps(self):
        result = self.control.compose("ps", "-a", "--format", "json", check=False, capture=True)
        if result.returncode != 0:
            return []
        raw = (result.stdout or "").strip()
        if not raw:
            return []
        try:
            decoded = json.loads(raw)
            rows = decoded if isinstance(decoded, list) else [decoded]
        except json.JSONDecodeError:
            rows = []
            for line in raw.splitlines():
                try:
                    rows.append(json.loads(line))
                except json.JSONDecodeError:
                    continue
        output = []
        for row in rows:
            output.append(
                {
                    "service": row.get("Service", row.get("service", "")),
                    "name": row.get("Name", row.get("name", "")),
                    "state": str(row.get("State", row.get("state", "unknown"))).lower(),
                    "status": row.get("Status", row.get("status", "")),
                    "health": row.get("Health", row.get("health", "")),
                }
            )
        return output

    def _compose_ps(self):
        with self.compose_ps_condition:
            now = time.monotonic()
            if (
                self.compose_ps_cache is not None
                and now < self.compose_ps_cache_expires_at
            ):
                return [dict(item) for item in self.compose_ps_cache]
            if self.compose_ps_loading:
                return [dict(item) for item in self.compose_ps_cache or []]
            self.compose_ps_loading = True
            generation = self.compose_ps_generation

        try:
            output = self._load_compose_ps()
        except BaseException:
            with self.compose_ps_condition:
                self.compose_ps_loading = False
                self.compose_ps_condition.notify_all()
            raise

        with self.compose_ps_condition:
            if generation == self.compose_ps_generation:
                self.compose_ps_cache = [dict(item) for item in output]
                self.compose_ps_cache_expires_at = (
                    time.monotonic() + COMPOSE_PS_CACHE_SECONDS
                )
            self.compose_ps_loading = False
            self.compose_ps_condition.notify_all()
        return output

    def _invalidate_runtime_query_cache(self):
        with self.compose_ps_condition:
            self.compose_ps_generation += 1
            self.compose_ps_cache = None
            self.compose_ps_cache_expires_at = 0
        with self.cpa_snapshot_condition:
            self.cpa_snapshot_generation += 1
            self.cpa_snapshot_cache = None
            self.cpa_snapshot_cache_expires_at = 0
            self.cpa_snapshot_cache_fingerprint = None
        self.user_summary_cache.invalidate()
        with self.user_summary_cache_lock:
            self.user_management_cache_generation += 1
            self.user_management_cache.clear()
        self.public_usage_cache.invalidate()
        self.admin_overview_cache.invalidate()
        self.admin_overview_catalog_cache.invalidate()
        self.admin_overview_usage_cache.invalidate()
        self.admin_accounts_cache.invalidate()
        with self.image_status_lock:
            self.image_status_generation += 1
            self.image_status_cache = None
            self.image_status_cache_expires_at = 0

    def overview(self, force_refresh=False):
        return self._cached_read(
            self.admin_overview_cache,
            "overview",
            "overview",
            ADMIN_OVERVIEW_CACHE_SECONDS,
            self._load_overview,
            force_refresh=force_refresh,
        )

    def overview_catalog(self, force_refresh=False):
        return self._cached_read(
            self.admin_overview_catalog_cache,
            "overview-catalog",
            "overview-catalog",
            ADMIN_OVERVIEW_CATALOG_CACHE_SECONDS,
            self._load_overview_catalog,
            force_refresh=force_refresh,
        )

    def _load_overview_catalog(self):
        generated_at = utc_timestamp()
        configured_accounts = self.control.accounts()
        account_services = self.control.services()
        auth = self.control.auth_status()
        services = {item["service"]: item for item in self._compose_ps()}
        limit_payload = self.usage_limits(force_refresh=False)
        limits = {
            item["account"]: item for item in limit_payload.get("accounts", [])
        }
        running_account_services = {
            account: service
            for account, service in account_services.items()
            if services.get(service, {}).get("state") == "running"
        }
        native_runtime = self._cached_cpa_management_snapshots(
            running_account_services
        )
        gateway_activity = self._gateway_error_activity(
            configured_accounts.keys(),
            now=generated_at,
        )
        accounts = []
        for account, metadata in configured_accounts.items():
            service = services.get(account_services[account], {})
            auth_files = auth.get(account, {}).get("files", 0)
            runtime = self._account_runtime_snapshot(
                native_runtime.get(account),
                gateway_activity.get(account),
                generated_at,
            )
            accounts.append(
                {
                    "id": account,
                    "operational_status": self._account_operational_status(
                        group_enabled=metadata["group_enabled"],
                        container_state=service.get("state", "missing"),
                        auth_files=auth_files,
                        quota=limits.get(account, {}),
                        runtime=runtime,
                    ),
                }
            )
        users = [
            {
                "email": item["email"],
                "status": "active" if int(item["active_keys"]) > 0 else "inactive",
            }
            for item in self.control.store.read_user_summaries()
        ]
        return {
            "generated_at": generated_at,
            "accounts": accounts,
            "users": users,
        }

    def _load_overview(self):
        warnings = []
        configured_accounts = self.control.accounts()
        account_services = self.control.services()
        records = self.control.active_records()
        record_counts_by_account = {account: 0 for account in configured_accounts}
        for record in records:
            if record["account"] in record_counts_by_account:
                record_counts_by_account[record["account"]] += 1
        auth = self.control.auth_status()
        services = self._compose_ps()
        service_index = {item["service"]: item for item in services}
        try:
            active_stats = self.control.active_stats(300)
        except Exception as error:
            active_stats = {
                account: {"count": 0, "users": [], "labels": [], "account_email": metadata["email"]}
                for account, metadata in configured_accounts.items()
            }
            warnings.append("近 5 分钟统计读取失败：{}".format(redact(error)))
        try:
            inflight_stats = self.control.inflight_stats()
        except Exception:
            inflight_stats = {
                account: {"count": 0, "users": [], "labels": [], "account_email": metadata["email"]}
                for account, metadata in configured_accounts.items()
            }
            warnings.append("实时请求统计暂不可用，请稍后刷新")

        accounts = []
        for account, metadata in configured_accounts.items():
            service = service_index.get(account_services[account], {})
            accounts.append(
                {
                    "id": account,
                    "email": metadata["email"],
                    "service": account_services[account],
                    "port": metadata["port"],
                    "container_state": service.get("state", "missing"),
                    "container_status": service.get("status", ""),
                    "auth_files": auth[account]["files"],
                    "auth_state": "configured" if auth[account]["files"] else "pending",
                    "active_keys": record_counts_by_account[account],
                    "active_users": active_stats[account]["count"],
                    "requests_5m": active_stats[account].get("requests", 0),
                    "inflight_keys": inflight_stats[account]["count"],
                }
            )

        running = sum(item["state"] == "running" for item in services)
        return {
            "generated_at": utc_timestamp(),
            "summary": {
                "users": len({item["user"] for item in records}),
                "active_keys": len({item["key"] for item in records}),
                "authorized_accounts": sum(item["files"] > 0 for item in auth.values()),
                "running_services": running,
                "total_services": len(services),
                "requests_5m": sum(item["requests_5m"] for item in accounts),
                "business_accounts": len(configured_accounts),
            },
            "accounts": accounts,
            "services": services,
            "warnings": warnings,
            "recent_jobs": self.jobs.recent(limit=8),
        }

    def public_gateway_usage(self, window):
        stats = self.control.active_stats(window)
        try:
            inflight = self.control.inflight_stats()
        except Exception:
            inflight = {}
        totals = {"inflight_keys": 0, "active_keys": 0, "requests": 0}
        payload = []
        for account in self.control.accounts():
            item = stats.get(account, {})
            row = {
                "account": account,
                "inflight_keys": int(inflight.get(account, {}).get("count", 0)),
                "active_keys": int(item.get("count", 0)),
                "request_count": int(item.get("requests", 0)),
            }
            totals["inflight_keys"] += row["inflight_keys"]
            totals["active_keys"] += row["active_keys"]
            totals["requests"] += row["request_count"]
            payload.append(row)
        return {
            "generated_at": time.time(),
            "window_seconds": window,
            "truncated": False,
            "totals": totals,
            "accounts": payload,
        }

    def cached_public_gateway_usage(self, window, now=None):
        return self._cached_read(
            self.public_usage_cache,
            "public-usage",
            int(window),
            PUBLIC_USAGE_CACHE_SECONDS,
            lambda: self.public_gateway_usage(window),
            cache_now=now,
        )

    def overview_usage(
        self,
        window_seconds,
        account="",
        user="",
        user_limit=10,
        custom_start_at=None,
        custom_end_at=None,
        force_refresh=False,
    ):
        key = (
            window_seconds,
            tuple(normalize_overview_filter_values(account)),
            tuple(normalize_overview_filter_values(user, lower=True)),
            str(user_limit),
            custom_start_at,
            custom_end_at,
            self._admin_usage_cache_scope(
                window_seconds,
                custom_start_at,
                custom_end_at,
            ),
        )
        return self._cached_read(
            self.admin_overview_usage_cache,
            "overview-usage",
            key,
            ADMIN_OVERVIEW_USAGE_CACHE_SECONDS,
            lambda: self._load_overview_usage(
                window_seconds,
                account=account,
                user=user,
                user_limit=user_limit,
                custom_start_at=custom_start_at,
                custom_end_at=custom_end_at,
            ),
            force_refresh=force_refresh,
        )

    def _load_overview_usage(
        self,
        window_seconds,
        account="",
        user="",
        user_limit=10,
        custom_start_at=None,
        custom_end_at=None,
    ):
        configured_accounts = self.control.accounts()
        selected_accounts = normalize_overview_filter_values(account)
        unknown_accounts = [item for item in selected_accounts if item not in configured_accounts]
        if unknown_accounts:
            raise APIError(HTTPStatus.NOT_FOUND, "CPA 账号不存在", "account_not_found")

        known_users = sorted(
            {str(record["user"]).strip().lower() for record in self.control._read_registry() if record.get("user")}
        )
        selected_users = normalize_overview_filter_values(user, lower=True)
        if any(item not in known_users for item in selected_users):
            raise APIError(HTTPStatus.NOT_FOUND, "用户不存在", "user_not_found")

        unavailable_accounts = []
        window_start_at_by_account = None
        series_start_at_by_account = None
        today_start_at = None
        custom_window_seconds = None
        requested_window = window_seconds
        if window_seconds == CUSTOM_USAGE_WINDOW:
            custom_start_at, custom_end_at = validate_custom_usage_range(
                custom_start_at,
                custom_end_at,
            )
            generated_at = custom_end_at - 1
            custom_window_seconds = custom_end_at - custom_start_at
            window_seconds = max(1, generated_at - custom_start_at)
            series_start_at_by_account = {
                account_name: custom_start_at
                for account_name in configured_accounts
            }
        elif window_seconds == ACCOUNT_USAGE_SINCE_RESET:
            generated_at = utc_timestamp()
            limit_payload = self.usage_limits(force_refresh=False)
            limits = {
                item["account"]: item
                for item in limit_payload.get("accounts", [])
            }
            usage_scope_accounts = selected_accounts or list(configured_accounts)
            window_start_at_by_account = {
                account_name: start_at
                for account_name in configured_accounts
                if (start_at := self._account_usage_window_start(
                    limits.get(account_name), generated_at
                )) is not None
            }
            unavailable_accounts = [
                account_name
                for account_name in usage_scope_accounts
                if account_name not in window_start_at_by_account
            ]
            series_start_at_by_account = window_start_at_by_account
            window_seconds = WEEKLY_WINDOW_SECONDS
        elif window_seconds == TODAY_USER_USAGE_WINDOW:
            window_context = self._usage_window_context(
                TODAY_USER_USAGE_WINDOW,
            )
            generated_at = window_context["generated_at"]
            start_at = window_context["window_start_at"]
            today_start_at = start_at
            series_start_at_by_account = {
                account_name: start_at
                for account_name in configured_accounts
            }
            window_seconds = max(1, generated_at - start_at)
        else:
            generated_at = None
            window_seconds = int(window_seconds)
        if requested_window == CUSTOM_USAGE_WINDOW:
            bucket_seconds = overview_usage_bucket_seconds(custom_window_seconds)
        else:
            bucket_seconds = (
                OVERVIEW_USAGE_BUCKETS[TODAY_USER_USAGE_WINDOW]
                if requested_window == TODAY_USER_USAGE_WINDOW
                else OVERVIEW_USAGE_BUCKETS[window_seconds]
            )
        payload = self.usage_store.token_time_series(
            configured_accounts.keys(),
            known_users,
            window_seconds=window_seconds,
            bucket_seconds=bucket_seconds,
            now=generated_at,
            account=selected_accounts,
            user_email=selected_users,
            user_limit=user_limit,
            start_at_by_account=series_start_at_by_account,
        )
        return {
            **payload,
            "window": requested_window,
            "window_seconds": (
                None
                if requested_window == TODAY_USER_USAGE_WINDOW
                else custom_window_seconds
                if requested_window == CUSTOM_USAGE_WINDOW
                else payload["window_seconds"]
            ),
            "window_start_at": (
                custom_start_at
                if requested_window == CUSTOM_USAGE_WINDOW
                else today_start_at
                if requested_window == TODAY_USER_USAGE_WINDOW
                else payload["window_start_at"]
            ),
            "window_start_at_by_account": window_start_at_by_account,
            "unavailable_accounts": unavailable_accounts,
            "selected_account": selected_accounts[0] if len(selected_accounts) == 1 else None,
            "selected_user": selected_users[0] if len(selected_users) == 1 else None,
            "selected_accounts": selected_accounts,
            "selected_users": selected_users,
            "user_limit": max(1, min(int(user_limit or 10), 50)),
            "collector": self.usage_store.status(now=payload["generated_at"]),
        }

    def create_user(self, body):
        email = str(body.get("email", ""))
        raw_team_id = body.get("team_id")
        if raw_team_id is not None and not isinstance(raw_team_id, str):
            raise ValueError("团队无效")
        team_id = str(raw_team_id or "").strip() or None
        initial_password = self._initial_portal_password(required=True)
        with self.action_lock:
            team = None
            if team_id:
                team = next(
                    (
                        item
                        for item in self.control.store.list_teams()
                        if item["id"] == team_id
                    ),
                    None,
                )
                if team is None:
                    raise ValueError("团队不存在")
            records = self.control.create_user(email)
            user = records[0]["user"]
            if team_id:
                self.control.store.set_user_teams([user], team_id)
            self.usage_store.set_portal_credential(
                user,
                hash_portal_password(initial_password),
                must_change=True,
            )
            self.audit("user.create", email)
        return {
            "message": "用户已创建；API Key 仅显示本次，使用中心采用默认初始密码，首次登录必须修改",
            "keys": [self.key_payload(records[0], reveal=True)],
            "initial_password": initial_password,
            "team_id": team_id,
            "team": (
                {
                    "id": team["id"],
                    "name": team["name"],
                    "description": team["description"],
                }
                if team
                else None
            ),
        }

    def reset_user_password(self, body):
        user = self.control._normalize_user(str(body.get("email") or ""))
        known_users = {record["user"] for record in self.control._read_registry()}
        if user not in known_users:
            raise APIError(HTTPStatus.NOT_FOUND, "用户不存在", "user_not_found")
        initial_password = self._initial_portal_password(required=True)
        with self.action_lock:
            self.usage_store.set_portal_credential(
                user,
                hash_portal_password(initial_password),
                must_change=True,
            )
            self.portal_login_limiter.clear(
                ("portal-account:{}".format(user),),
                include_related=True,
            )
            self.audit("user.password.reset", user)
        return {
            "message": "用户密码已重置为默认初始密码；现有登录会话已失效，首次登录必须修改",
            "user": user,
            "password_change_required": True,
            "initial_password": initial_password,
        }

    def create_account(self, body):
        account_id = str(body.get("id", ""))
        email = str(body.get("email", ""))
        arguments = {}
        if "proxy_mode" in body:
            arguments["proxy_mode"] = body.get("proxy_mode")
        if "proxy_url" in body:
            arguments["proxy_url"] = body.get("proxy_url")
        with self.action_lock:
            result = self.control.add_account(account_id, email, **arguments)
            self.audit("account.create", result["id"])
        created_keys = int(result.get("created_keys", 0))
        account = {
            key: value for key, value in result.items() if key not in ("keys", "created_keys")
        }
        return {
            "message": "业务 CPA 已创建并启动；已在后台关联 {} 个已有用户的唯一 Key。请继续完成 OAuth 授权".format(
                created_keys
            ),
            "account": account,
            "created_keys": created_keys,
        }

    @staticmethod
    def _raise_quota_upstream_error(error, action):
        if isinstance(error, urllib.error.HTTPError):
            if error.code in (HTTPStatus.UNAUTHORIZED, HTTPStatus.FORBIDDEN):
                raise APIError(
                    HTTPStatus.CONFLICT,
                    "上游 OAuth 授权已失效，请重新完成 OAuth 后再重试",
                    "quota_auth_expired",
                )
            if error.code in (HTTPStatus.CONFLICT, HTTPStatus.UNPROCESSABLE_ENTITY):
                raise APIError(
                    HTTPStatus.CONFLICT,
                    "上游已拒绝本次{}，请刷新周限额后重试".format(action),
                    "quota_reset_rejected",
                )
        raise APIError(
            HTTPStatus.BAD_GATEWAY,
            "无法连接上游完成{}，请稍后重试".format(action),
            "quota_upstream_unavailable",
        )

    def reset_account_weekly_quota(self, body):
        account_id = self.control._normalize_account_id(body.get("account"))
        if account_id not in self.control.accounts():
            raise APIError(HTTPStatus.NOT_FOUND, "CPA 账号不存在", "account_not_found")
        if str(body.get("confirm", "")) != account_id:
            raise ValueError("确认内容必须与 CPA 标识完全一致")
        credit_id = str(body.get("credit_id", "")).strip()
        if not credit_id or len(credit_id) > 512:
            raise ValueError("请选择要使用的重置额度")

        with self.action_lock:
            try:
                auth = self._codex_auth_record(account_id)
            except FileNotFoundError:
                raise APIError(
                    HTTPStatus.CONFLICT,
                    "该 CPA 尚未完成 OAuth 授权",
                    "quota_auth_missing",
                )
            try:
                proxy_url = self._account_proxy_url(account_id)
                usage = self._request_official_usage(
                    auth.get("access_token"),
                    auth.get("account_id"),
                    proxy_url,
                )
                if not isinstance(usage, dict):
                    raise TypeError("official usage payload must be an object")
                reset_credit_details = self._request_official_reset_credits(
                    auth.get("access_token"),
                    auth.get("account_id"),
                    proxy_url,
                )
                if not isinstance(reset_credit_details, dict):
                    raise TypeError("official reset credits payload must be an object")
            except (
                urllib.error.HTTPError,
                urllib.error.URLError,
                OSError,
                ValueError,
                TypeError,
                UnicodeDecodeError,
                json.JSONDecodeError,
            ) as error:
                self._raise_quota_upstream_error(error, "刷新周限额与重置额度")

            current = self._normalize_usage_limit_payload(
                account_id,
                usage,
                reset_credit_details,
            )
            selected_credit = next(
                (
                    credit
                    for credit in (current.get("reset_credits") or {}).get("credits", [])
                    if credit.get("id") == credit_id
                ),
                None,
            )
            if not selected_credit:
                raise APIError(
                    HTTPStatus.CONFLICT,
                    "所选重置额度已使用、过期或不可用，请刷新列表后重新选择",
                    "quota_reset_credit_changed",
                )
            applicable_count = (
                current.get("reset_credits") or {}
            ).get("applicable_available_count")
            eligible_windows = [
                window
                for window in current.get("weekly_windows", [])
                if window.get("resettable")
            ]
            if not applicable_count or not eligible_windows:
                raise APIError(
                    HTTPStatus.CONFLICT,
                    "当前没有已耗尽且可重置的周限额，请等待额度耗尽或刷新列表",
                    "quota_reset_unavailable",
                )

            try:
                result = self._request_official_quota_reset(
                    auth.get("access_token"),
                    auth.get("account_id"),
                    proxy_url,
                    credit_id,
                )
                if not isinstance(result, dict):
                    raise TypeError("official reset payload must be an object")
            except (
                urllib.error.HTTPError,
                urllib.error.URLError,
                OSError,
                ValueError,
                TypeError,
                UnicodeDecodeError,
                json.JSONDecodeError,
            ) as error:
                # A timeout or invalid response can occur after upstream already
                # consumed the credit. Force the next display refresh to query
                # upstream instead of offering a stale reset action.
                self._invalidate_usage_limit_cache()
                self._raise_quota_upstream_error(error, "重置周限额")

            self._invalidate_usage_limit_cache()
            self.audit("account.quota.reset", account_id)

        windows_reset = self._nonnegative_int(result.get("windows_reset")) or 0
        credit = result.get("credit") if isinstance(result.get("credit"), dict) else {}
        return {
            "message": (
                "周限额已重置，共刷新 {} 个窗口".format(windows_reset)
                if windows_reset
                else "重置请求已处理，请刷新确认最新周限额"
            ),
            "account": account_id,
            "windows": [
                {
                    "key": window["key"],
                    "label": window["label"],
                    "previous_reset_at": window["reset_at"],
                }
                for window in eligible_windows
            ],
            "windows_reset": windows_reset,
            "code": str(result.get("code") or "")[:100],
            "credit": {
                "title": selected_credit.get("title"),
                "expires_at": selected_credit.get("expires_at"),
                **{
                    key: credit.get(key)
                    for key in ("reset_type", "status", "redeemed_at")
                    if credit.get(key) is not None
                },
            },
        }

    def update_account(self, body):
        account_id = str(body.get("id", ""))
        new_account_id = str(body.get("new_id", account_id))
        email = str(body.get("email", ""))
        has_policy = any(
            field in body
            for field in ("group_enabled", "default_group", "fallback_account")
        )
        if has_policy and (
            not isinstance(body.get("group_enabled"), bool)
            or not isinstance(body.get("default_group"), bool)
        ):
            raise ValueError("账号启用状态和默认状态必须为布尔值")
        if new_account_id.strip().lower() != account_id.strip().lower() and str(
            body.get("confirm", "")
        ).strip().lower() != account_id.strip().lower():
            raise ValueError("重命名确认内容必须与当前 CPA 标识完全一致")
        with self.action_lock:
            arguments = {"new_account_id": new_account_id}
            if "proxy_mode" in body:
                arguments["proxy_mode"] = body.get("proxy_mode")
            if "proxy_url" in body:
                arguments["proxy_url"] = body.get("proxy_url")
            if has_policy:
                arguments.update(
                    {
                        "group_enabled": body.get("group_enabled"),
                        "default_group": body.get("default_group"),
                        "fallback_account": body.get("fallback_account"),
                    }
                )
            record = self.control.update_account(account_id, email, **arguments)
            if record.get("renamed_from"):
                self.audit("account.rename", "{} -> {}".format(account_id, record["id"]))
            else:
                self.audit("account.update", record["id"])
        return {
            "message": (
                "CPA 已重命名并更新"
                if record.get("renamed_from")
                else "CPA 账号已更新"
            ),
            "account": record,
        }

    def update_account_policy(self, body):
        account_id = str(body.get("id", ""))
        group_enabled = body.get("group_enabled")
        default_group = body.get("default_group", False)
        if not isinstance(group_enabled, bool) or not isinstance(default_group, bool):
            raise ValueError("账号启用状态和默认状态必须为布尔值")
        with self.action_lock:
            record = self.control.update_account_policy(
                account_id,
                account_id,
                group_enabled,
                default_group=default_group,
                fallback_account=body.get("fallback_account"),
            )
            self.audit("account.policy.update", record["id"])
        message = "账号策略已更新"
        if record.get("rerouted_users"):
            message += "，{} 个用户已切换到备用 CPA".format(
                record["rerouted_users"]
            )
        return {"message": message, "account": record}

    def rebalance_account_users(self, body):
        account_id = self.control._normalize_account_id(str(body.get("id", "")))
        if account_id not in self.control.accounts():
            raise APIError(HTTPStatus.NOT_FOUND, "CPA 账号不存在", "account_not_found")
        if str(body.get("confirm", "")).strip().lower() != account_id:
            raise ValueError("确认内容必须与 CPA 标识完全一致")
        try:
            result = self.account_failover.rebalance_account(self, account_id)
        except ValueError as error:
            self.audit(
                "account.failover.manual_rebalance",
                account_id,
                outcome="rejected",
            )
            raise APIError(
                HTTPStatus.CONFLICT,
                str(error),
                "account_rebalance_unavailable",
            )
        distribution = "，".join(
            "{} {} 位".format(account, count)
            for account, count in result["destinations"].items()
        )
        return {
            "message": "已迁移 {} 位用户：{}".format(
                result["moved_users"],
                distribution,
            ),
            "rebalance": result,
        }

    def clear_account_auth(self, body):
        account_id = str(body.get("id", ""))
        if str(body.get("confirm", "")) != account_id:
            raise ValueError("确认内容必须与 CPA 标识完全一致")
        with self.action_lock:
            result = self.control.clear_account_auth(account_id)
            self.audit("account.oauth.clear", result["id"])
        return {
            "message": "OAuth 授权已清除，原文件已安全归档",
            "account": result,
        }

    def delete_account(self, body):
        account_id = str(body.get("id", ""))
        if str(body.get("confirm", "")) != account_id:
            raise ValueError("确认内容必须与 CPA 标识完全一致")
        revoke_keys = body.get("revoke_keys", False)
        if not isinstance(revoke_keys, bool):
            raise ValueError("revoke_keys 必须是布尔值")
        with self.action_lock:
            result = self.control.delete_account(
                account_id,
                revoke_keys=revoke_keys,
                fallback_account=body.get("fallback_account"),
            )
            self.audit("account.delete", result["id"])
        message = "业务 CPA 已删除，配置、授权和日志已归档"
        if result["cleanup_warnings"]:
            message += "；部分旧文件清理失败，请查看返回详情"
        return {"message": message, "account": result}

    def create_key(self, body):
        email = self.control._normalize_user(str(body.get("email", "")))
        account = str(body.get("account", "")).strip().lower()
        label = "{}:{}".format(email, account)
        with self.action_lock:
            record = self.control.create_key(label)
            self.audit("key.create", label)
        return {"message": "用户唯一 Key 已创建", "keys": [self.key_payload(record, reveal=True)]}

    def rotate_key(self, body):
        label = str(body.get("label", ""))
        with self.action_lock:
            record = self.control.rotate_key(label)
            self.audit("key.rotate", record["label"])
        return {"message": "用户唯一 Key 已轮换，新密钥只显示一次", "keys": [self.key_payload(record, reveal=True)]}

    def revoke_key(self, body):
        label = str(body.get("label", ""))
        with self.action_lock:
            record = self.control.revoke_key(label)
            self.audit("key.revoke", record["label"])
        return {"message": "用户唯一 Key 已停用", "label": record["label"]}

    def revoke_user(self, body):
        email = str(body.get("email", ""))
        with self.action_lock:
            records = self.control.revoke_user(email)
            self.audit("user.revoke", self.control._normalize_user(email))
        return {
            "message": "用户唯一 Key 已停用",
            "revoked": len({record["key"] for record in records}),
        }

    def delete_user(self, body):
        email = str(body.get("email", ""))
        if str(body.get("confirm", "")) != email:
            raise ValueError("确认内容必须与用户邮箱完全一致")
        revoke_keys = body.get("revoke_keys") is True
        with self.action_lock:
            result = self.control.delete_user(email, revoke_keys=revoke_keys)
            self.control.store.delete_user_classification(result["email"])
            self.usage_store.clear_quota_policy(result["email"])
            self.usage_store.delete_portal_identity(result["email"])
            self.portal_login_limiter.clear(
                ("portal-account:{}".format(result["email"]),),
                include_related=True,
            )
            self.audit("user.delete", result["email"])
        return {
            "message": "用户及其 Key 已删除；完整 Key 不保留额外明文审计副本",
            "user": result,
        }

    def settings(self):
        backup_root = self.root / "backups" / "accounts"
        backups = sorted(
            (path for path in backup_root.iterdir() if path.is_dir()),
            key=lambda path: path.stat().st_mtime,
            reverse=True,
        ) if backup_root.is_dir() else []
        storage = []
        for label, relative in (
            ("控制面数据库", "state/control-plane.sqlite3"),
            ("用户用量数据库", "state/usage.sqlite3"),
            ("控制面加密主密钥", "secrets/control-plane.key"),
            ("管理操作审计", "logs/admin/audit.jsonl"),
        ):
            path = self.root / relative
            storage.append(
                {
                    "label": label,
                    "path": relative,
                    "exists": path.is_file(),
                    "mode": "{:03o}".format(path.stat().st_mode & 0o777) if path.exists() else "—",
                }
            )
        try:
            management_key_configured = bool(self._read_management_key())
        except (OSError, ValueError):
            management_key_configured = False
        initial_password_configured = self._initial_portal_password() is not None
        recent_audit = []
        if self.audit_path.is_file():
            for line in self.audit_path.read_text(encoding="utf-8", errors="replace").splitlines()[-20:]:
                try:
                    item = json.loads(line)
                except json.JSONDecodeError:
                    continue
                recent_audit.append(
                    {
                        "timestamp": item.get("timestamp"),
                        "action": redact(item.get("action", "")),
                        "target": redact(item.get("target", "")),
                        "outcome": redact(item.get("outcome", "")),
                    }
                )
        recent_audit.reverse()
        configuration = self.control.configuration()
        grouped_configuration = []
        for definition in self.control.configuration_definitions():
            presentation = dict(definition)
            group = next(
                (
                    item
                    for item in grouped_configuration
                    if item["name"] == definition["group"]
                ),
                None,
            )
            if group is None:
                group = {"name": definition["group"], "fields": []}
                grouped_configuration.append(group)
            group["fields"].append(
                {
                    **presentation,
                    "value": (
                        ""
                        if definition["type"] == "proxy_url_secret"
                        else configuration["values"][definition["key"]]
                    ),
                    **(
                        {
                            "configured": bool(
                                configuration["values"][definition["key"]]
                            )
                        }
                        if definition["type"] == "proxy_url_secret"
                        else {}
                    ),
                    "editable": True,
                }
            )
        grouped_configuration.append(
            {
                "name": "系统约束",
                "fields": [
                    {
                        "key": "system.project_root",
                        "label": "运行目录",
                        "description": "由部署环境固定，不能通过管理页面迁移。",
                        "type": "string",
                        "value": str(self.root),
                        "editable": False,
                        "apply_mode": "readonly",
                    },
                    {
                        "key": "system.internal_ports",
                        "label": "容器内部端口",
                        "description": "网关与 CPA 为 8317，管理后端为 8318。",
                        "type": "string",
                        "value": "gateway/cpa 8317 · admin 8318",
                        "editable": False,
                        "apply_mode": "readonly",
                    },
                    {
                        "key": "system.admin_bind",
                        "label": "管理后端绑定",
                        "description": "由 CLIPROXY_ADMIN_HOST/PORT 或启动参数决定。",
                        "type": "string",
                        "value": "{}:{}".format(
                            os.environ.get("CLIPROXY_ADMIN_HOST", "0.0.0.0"),
                            os.environ.get("CLIPROXY_ADMIN_PORT", "8318"),
                        ),
                        "editable": False,
                        "apply_mode": "readonly",
                    },
                    {
                        "key": "system.internal_gateway_url",
                        "label": "管理后端网关地址",
                        "description": "CLIPROXY_GATEWAY_URL，仅用于容器内部健康和路由验证。",
                        "type": "string",
                        "value": os.environ.get(
                            "CLIPROXY_GATEWAY_URL",
                            "按 gateway.port 自动计算",
                        ),
                        "editable": False,
                        "apply_mode": "readonly",
                    },
                    {
                        "key": "system.weekly_window_seconds",
                        "label": "周限额窗口",
                        "description": "OpenAI 官方周额度语义，固定为 7 天。",
                        "type": "integer",
                        "value": WEEKLY_WINDOW_SECONDS,
                        "unit": "秒",
                        "editable": False,
                        "apply_mode": "readonly",
                    },
                    {
                        "key": "system.usage_windows",
                        "label": "管理统计范围",
                        "description": "前后端共同支持的统计筛选范围。",
                        "type": "string",
                        "value": "1 小时 · 今日 · 24 小时 · 7 天 · 30 天 · 全部",
                        "editable": False,
                        "apply_mode": "readonly",
                    },
                    {
                        "key": "system.security_limits",
                        "label": "安全上限",
                        "description": "请求正文 64 KiB，任务记录 60 条，单任务日志 600 行。",
                        "type": "string",
                        "value": "64 KiB · 60 jobs · 600 lines",
                        "editable": False,
                        "apply_mode": "readonly",
                    },
                    {
                        "key": "system.gateway_capacity",
                        "label": "网关容量约束",
                        "description": "OpenResty 连接数、请求体和长请求等待上限。",
                        "type": "string",
                        "value": "4096 connections · 100 MiB body · 3600s upstream",
                        "editable": False,
                        "apply_mode": "readonly",
                    },
                    {
                        "key": "system.gateway_shared_memory",
                        "label": "网关共享内存",
                        "description": "实时统计、使用情况、认证路由与用户额度快照的 OpenResty 共享字典。",
                        "type": "string",
                        "value": "stats 10 MiB · usage 2 MiB · auth 20 MiB · quota 20 MiB",
                        "editable": False,
                        "apply_mode": "readonly",
                    },
                ],
            }
        )
        return {
            "management_key_configured": management_key_configured,
            "initial_password_configured": initial_password_configured,
            "notifications": self.notifications.public_status(),
            "account_failover": self.account_failover.public_status(),
            "user_quota_operations": self.user_quota_action_summary(),
            "configuration": {
                "version": configuration["version"],
                "groups": grouped_configuration,
            },
            "storage": storage,
            "backups": {
                "count": len(backups),
                "latest": str(backups[0].relative_to(self.root)) if backups else "",
            },
            "recent_audit": recent_audit,
            "branding": self.control.public_site_configuration(),
        }

    def update_initial_portal_password(self, body):
        initial_password = body.get("initial_password")
        confirmation = body.get("confirmation")
        if not isinstance(initial_password, str) or not isinstance(confirmation, str):
            raise APIError(HTTPStatus.BAD_REQUEST, "密码格式无效", "invalid_password")
        if not hmac.compare_digest(initial_password, confirmation):
            raise APIError(HTTPStatus.BAD_REQUEST, "两次输入的初始密码不一致", "password_mismatch")
        if not PORTAL_PASSWORD_MIN_LENGTH <= len(initial_password) <= PORTAL_PASSWORD_MAX_LENGTH:
            raise APIError(
                HTTPStatus.BAD_REQUEST,
                "初始密码长度必须为 {} 到 {} 位".format(
                    PORTAL_PASSWORD_MIN_LENGTH,
                    PORTAL_PASSWORD_MAX_LENGTH,
                ),
                "invalid_password",
            )
        if hmac.compare_digest(initial_password, LEGACY_DEFAULT_PORTAL_PASSWORD):
            raise APIError(
                HTTPStatus.BAD_REQUEST,
                "不能使用已停用的历史默认密码",
                "weak_password",
            )
        with self.action_lock:
            self.control.store.write_secret(
                PORTAL_INITIAL_PASSWORD_SECRET,
                initial_password,
            )
            self.audit("settings.portal-initial-password.update", "configured")
        return {
            "message": "用户初始密码已安全保存；已有用户密码不会自动变化",
            "configured": True,
        }

    def native_accounts(self, *, include_management_urls=False):
        accounts = []
        listen_address = self.control.configuration()["values"]["accounts.listen_address"]
        for account, metadata in self.control.accounts().items():
            item = {
                "id": account,
                "group_enabled": metadata["group_enabled"],
            }
            if include_management_urls:
                host = "[{}]".format(listen_address) if ":" in listen_address else listen_address
                item["management_url"] = "http://{}:{}/management.html".format(
                    host,
                    metadata["port"],
                )
            accounts.append(item)
        return {"accounts": accounts}

    def update_branding_logo(self, body):
        if str(body.get("confirm", "")) != "save":
            raise ValueError("请确认保存 Logo")
        encoded = body.get("data_base64")
        if not isinstance(encoded, str) or not encoded:
            raise ValueError("请提供 Logo 文件")
        try:
            content = base64.b64decode(encoded, validate=True)
        except (ValueError, TypeError):
            raise ValueError("Logo 文件编码无效")
        with self.action_lock:
            asset = self.control.update_logo(
                body.get("filename"),
                body.get("content_type"),
                content,
            )
            self.audit("settings.branding.logo.update", asset["sha256"][:12])
        return {
            "message": "Logo 已更新",
            "logo": self.control.public_site_configuration()["logo"],
        }

    def clear_branding_logo(self, body):
        if str(body.get("confirm", "")) != "reset":
            raise ValueError("请确认恢复默认 Logo")
        with self.action_lock:
            existed = self.control.delete_logo()
            self.audit("settings.branding.logo.clear", "logo")
        return {
            "message": "已恢复默认 Logo" if existed else "当前已使用默认 Logo",
            "logo": self.control.public_site_configuration()["logo"],
        }

    def update_configuration(self, body):
        if str(body.get("confirm", "")) != "save":
            raise ValueError("请确认保存配置")
        changes = body.get("values")
        with self.action_lock:
            if isinstance(changes, dict):
                changes = dict(changes)
                if changes.get("cpa.proxy_url") == "":
                    changes.pop("cpa.proxy_url")
                if not changes:
                    return {
                        "message": "配置没有变化",
                        "changed": [],
                        "applied": [],
                        "pending_deployment": False,
                    }
                current = self.control.configuration()["values"]
                raw_enabled = changes.get("notification.enabled", current["notification.enabled"])
                enabled = self.control._normalize_configuration_value(
                    cliproxy.CONFIG_DEFINITION_BY_KEY["notification.enabled"],
                    raw_enabled,
                )
                if enabled and not self.notifications.webhook_configured():
                    raise ValueError("启用企业微信通知前必须先配置 Webhook")
            result = self.control.update_configuration(changes)
            changed = result["changed"]
            if not changed:
                return {
                    "message": "配置没有变化",
                    "changed": [],
                    "applied": [],
                    "pending_deployment": False,
                }
            modes = {
                cliproxy.CONFIG_DEFINITION_BY_KEY[key]["apply_mode"]
                for key in changed
            }
            try:
                if "deployment" in modes:
                    self.control.sync_environment_configuration(result["values"])
                if "accounts" in modes:
                    if set(changed) <= {"cpa.proxy_enabled", "cpa.proxy_url"}:
                        self.control.apply_default_proxy_change()
                    else:
                        self.control.apply_changes()
                if "user_quota.timezone" in changed:
                    self.usage_store.set_week_timezone(
                        result["values"]["user_quota.timezone"]
                    )
                if "collector" in modes:
                    self.control.compose("restart", "usage-collector")
            except Exception as error:
                try:
                    if "cpa.proxy_url" in changed:
                        previous_proxy = result["before"]["cpa.proxy_url"]
                        if previous_proxy:
                            self.control.store.write_secret(
                                cliproxy.DEFAULT_PROXY_SECRET, previous_proxy
                            )
                        else:
                            self.control.store.delete_secret(
                                cliproxy.DEFAULT_PROXY_SECRET
                            )
                    self.control.replace_configuration(result["stored_before"])
                    if "deployment" in modes:
                        self.control.sync_environment_configuration(result["before"])
                    if "accounts" in modes:
                        if set(changed) <= {"cpa.proxy_enabled", "cpa.proxy_url"}:
                            self.control.apply_default_proxy_change()
                        else:
                            self.control.apply_changes()
                    if "user_quota.timezone" in changed:
                        self.usage_store.set_week_timezone(
                            result["before"]["user_quota.timezone"]
                        )
                    if "collector" in modes:
                        self.control.compose("restart", "usage-collector")
                except Exception:
                    pass
                raise APIError(
                    HTTPStatus.BAD_GATEWAY,
                    "配置应用失败，已尝试恢复原配置：{}".format(redact(error)),
                    "configuration_apply_failed",
                )
            self._invalidate_usage_limit_cache()
            self.audit("settings.configuration.update", ",".join(changed))

        applied = sorted(modes - {"deployment", "future"})
        pending_deployment = "deployment" in modes
        message = "已保存 {} 项配置".format(len(changed))
        if pending_deployment:
            message += "；镜像参数可在账号管理中拉取更新，其他部署参数下次部署生效"
        return {
            "message": message,
            "changed": changed,
            "applied": applied,
            "pending_deployment": pending_deployment,
        }

    def reasoning_effort_color_stylesheet(self):
        try:
            values = self.control.configuration()["values"]
        except (OSError, ValueError, TypeError, json.JSONDecodeError):
            values = {}
        declarations = []
        for effort, unused_label, default in cliproxy.REASONING_EFFORT_COLOR_DEFAULTS:
            key = "admin.account_usage.reasoning_effort_color.{}".format(effort)
            color = str(values.get(key, default)).lower()
            red, green, blue = (
                int(color[index:index + 2], 16) / 255.0
                for index in (1, 3, 5)
            )
            channels = [
                value / 12.92
                if value <= 0.04045
                else ((value + 0.055) / 1.055) ** 2.4
                for value in (red, green, blue)
            ]
            luminance = (
                0.2126 * channels[0]
                + 0.7152 * channels[1]
                + 0.0722 * channels[2]
            )
            foreground = "#171d2b" if luminance > 0.179 else "#ffffff"
            declarations.extend(
                (
                    "  --account-model-effort-{}: {};".format(effort, color),
                    "  --account-model-effort-{}-text: {};".format(
                        effort, foreground
                    ),
                )
            )
        return ":root {\n" + "\n".join(declarations) + "\n}\n"

    def update_notification_webhook(self, body):
        if str(body.get("confirm", "")) != "save":
            raise ValueError("请确认保存企业微信 Webhook")
        with self.notifications.lock:
            status = self.notifications.set_webhook(body.get("webhook_url"))
            self.audit("settings.notification.webhook.update", "wecom")
        return {
            "message": "企业微信 Webhook 已保存",
            "notifications": {**self.notifications.public_status(), **status},
        }

    def clear_notification_webhook(self, body):
        if str(body.get("confirm", "")) != "clear":
            raise ValueError("请确认清除企业微信 Webhook")
        with self.action_lock:
            with self.notifications.lock:
                self.notifications.clear_webhook()
                configuration = self.control.configuration()["values"]
                if configuration.get("notification.enabled"):
                    self.control.update_configuration({"notification.enabled": False})
                self.audit("settings.notification.webhook.clear", "wecom")
        return {
            "message": "企业微信 Webhook 已清除，通知已关闭",
            "notifications": self.notifications.public_status(),
        }

    def send_notification(self):
        configuration = self.control.configuration()["values"]
        with self.notifications.lock:
            if not self.notifications.webhook_configured():
                raise ValueError("尚未配置企业微信 Webhook")
            state = self.notifications.read_state()
            snapshot = self.account_management(3600)
            try:
                content = self.notifications.build_markdown_v2(
                    snapshot,
                    title="{} · 账号额度报告".format(configuration["branding.short_name"]),
                    timezone_name=configuration["notification.timezone"],
                    threshold_percent=configuration["notification.weekly_threshold_percent"],
                    usage_center_url=usage_center_url(
                        configuration["branding.public_base_url"]
                    ),
                )
                result = self.notifications.send_content(content)
            except (RuntimeError, ValueError) as error:
                state["last_error"] = redact_webhook(error)
                self.notifications.write_state(state)
                raise APIError(
                    HTTPStatus.BAD_GATEWAY,
                    str(error),
                    "notification_send_failed",
                )
            state["last_success_at"] = utc_timestamp()
            state["last_error"] = ""
            self.notifications.write_state(state)
            self.audit("notification.manual_send", "wecom")
        return {
            "message": "账号信息已发送到企业微信群",
            "format": "markdown_v2",
            "result": result,
        }

    def test_notification(self):
        """Backward-compatible alias for older admin clients."""
        return self.send_notification()

    def rotate_management_key(self, body):
        new_key = str(body.get("new_key", ""))
        confirmation = str(body.get("confirmation", ""))
        if not hmac.compare_digest(new_key, confirmation):
            raise ValueError("两次输入的管理密钥不一致")
        with self.action_lock:
            result = self.control.rotate_management_key(new_key)
            self.admin_sessions.revoke_all()
            self.audit("settings.management-key.rotate", "control-plane")
        return {
            "message": "管理密钥已更新，请使用新密钥重新进入",
            "result": result,
        }

    def _services_for_target(self, target, logs=False):
        target = str(target or "all").strip().lower()
        account_services = self.control.services()
        if target == "all":
            services = self.control.runtime_services() + list(MANAGED_EXTRA_SERVICES)
            if logs:
                services.append("admin")
            return services
        if target in account_services:
            return self.control.runtime_services_for_account(target)
        allowed = LOGGABLE_EXTRA_SERVICES if logs else MANAGED_EXTRA_SERVICES
        if target in allowed:
            return [target]
        raise ValueError("未知操作目标：{}".format(target))

    def operation_impact(self, action, target):
        action = str(action or "").strip().lower()
        target = str(target or "").strip().lower()
        if action != "stop":
            raise ValueError("只支持查询停止操作影响")
        self._services_for_target(target)
        accounts = self.control.accounts()
        if target == "all":
            return {
                "action": action,
                "target": target,
                "target_type": "all",
                "routed_users": sum(
                    self.control.routed_user_counts(accounts=accounts).values()
                ),
            }
        if target in accounts:
            return {
                "action": action,
                "target": target,
                "target_type": "account",
                "routed_users": self.control.routed_user_counts(
                    accounts=accounts
                )[target],
            }
        return {
            "action": action,
            "target": target,
            "target_type": "service",
            "routed_users": None,
        }

    def operation(self, body):
        action = str(body.get("action", "")).strip().lower()
        target = str(body.get("target", "all")).strip().lower()
        cli = [
            sys.executable,
            str(APPLICATION_ROOT / "scripts" / "cliproxy.py"),
            "--root",
            str(self.root),
        ]
        compose = self.control.compose_command()
        if action == "login":
            if target not in self.control.accounts():
                raise ValueError("OAuth 授权必须选择一个 CPA 账号")
            commands = [cli + ["login", target]]
            name = "OAuth 授权"
        elif action in ("up", "stop", "restart"):
            services = self._services_for_target(target)
            if action == "up":
                commands = [cli + ["render"], compose + ["up", "-d"] + services]
                name = "启动服务"
            else:
                commands = [compose + [action] + services]
                name = "停止服务" if action == "stop" else "重启服务"
        elif action == "image-pull":
            commands = [cli + ["image", "pull"]]
            name = "拉取 CPA 镜像"
            target = "all"
        elif action == "image-update":
            accounts = self.control.accounts()
            if target != "all" and target not in accounts:
                raise ValueError("镜像更新必须选择 all 或有效 CPA 账号")
            if target != "all" and not accounts[target]["group_enabled"]:
                raise ValueError("CPA 账号已停用，不能更新镜像：{}".format(target))
            commands = [cli + ["image", "update", target]]
            name = "更新全部 CPA 镜像" if target == "all" else "更新 CPA 镜像"
        elif action == "render":
            commands = [cli + ["render"]]
            name = "渲染并校验配置"
            target = "all"
        elif action == "health":
            commands = [cli + ["health"]]
            name = "健康检查"
            target = "all"
        elif action == "verify-routing":
            commands = [cli + ["verify-routing"]]
            name = "路由验证"
            target = "all"
        else:
            raise ValueError("不支持的操作：{}".format(action))
        reused = False
        if action == "login":
            job, reused = self.jobs.start_or_reuse(
                name,
                target,
                commands,
                dedupe_key="oauth:{}".format(target),
            )
        elif action in ("image-pull", "image-update"):
            image_ref = self.control.configuration()["values"]["runtime.cliproxy_image"]
            job, reused = self.jobs.start_or_reuse(
                name,
                target,
                commands,
                dedupe_key="{}:{}:{}".format(action, target, image_ref),
            )
        else:
            job = self.jobs.start(name, target, commands)
        self.audit(
            "operation.{}".format(action),
            target,
            outcome="reused" if reused else "accepted",
        )
        return {
            "message": "已有相同任务，已直接打开" if reused else "任务已提交",
            "job": job,
            "reused": reused,
        }

    def logs(self, target):
        services = self._services_for_target(target, logs=True)
        command = self.control.compose_command() + ["logs", "--tail", "200"] + services
        result = subprocess.run(
            command,
            cwd=str(self.root),
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            timeout=30,
            check=False,
        )
        return {"target": target, "exit_code": result.returncode, "output": redact(result.stdout or "")}


class AdminRequestHandler(BaseHTTPRequestHandler):
    server_version = "CPAAdmin/1.0"

    @property
    def app(self):
        return self.server.app

    def log_message(self, fmt, *args):
        sys.stderr.write("[%s] %s\n" % (self.log_date_time_string(), fmt % args))

    def handle_one_request(self):
        self._request_started_at = time.perf_counter()
        self.app._begin_request_metrics()
        return super().handle_one_request()

    def _headers(self, status, content_type="application/json; charset=utf-8", length=0, headers=None):
        self.send_response(int(status))
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(length))
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("X-Frame-Options", "DENY")
        self.send_header("Referrer-Policy", "no-referrer")
        self.send_header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
        started_at = getattr(self, "_request_started_at", time.perf_counter())
        self.send_header(
            "Server-Timing",
            self.app._server_timing_header(
                (time.perf_counter() - started_at) * 1000
            ),
        )
        if self._request_uses_https():
            self.send_header("Strict-Transport-Security", "max-age=0")
        self.send_header(
            "Content-Security-Policy",
            "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; "
            "connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'",
        )
        for name, value in headers or ():
            self.send_header(name, value)
        self.end_headers()

    def _json(self, status, payload, headers=None):
        raw = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        response_headers = list(headers or ())
        if len(raw) >= JSON_GZIP_MIN_BYTES and self._accepts_gzip():
            compressed = gzip.compress(raw, compresslevel=1)
            if len(compressed) < len(raw):
                raw = compressed
                response_headers.extend(
                    (("Content-Encoding", "gzip"), ("Vary", "Accept-Encoding"))
                )
        self._headers(status, length=len(raw), headers=response_headers)
        self.wfile.write(raw)

    def _accepts_gzip(self):
        for item in self.headers.get("Accept-Encoding", "").split(","):
            parts = [part.strip() for part in item.split(";")]
            if not parts or parts[0].lower() not in ("gzip", "*"):
                continue
            quality = 1.0
            for parameter in parts[1:]:
                if parameter.lower().startswith("q="):
                    try:
                        quality = float(parameter.split("=", 1)[1])
                    except ValueError:
                        quality = 0.0
            return quality > 0
        return False

    def _error(self, status, message, code="request_failed", headers=None):
        self._json(
            status,
            {"error": {"code": code, "message": redact(message)}},
            headers=headers,
        )

    def _read_json(self, max_bytes=MAX_BODY_BYTES):
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            raise APIError(HTTPStatus.BAD_REQUEST, "Content-Length 无效", "invalid_body")
        if length <= 0 or length > int(max_bytes):
            raise APIError(HTTPStatus.BAD_REQUEST, "请求体为空或过大", "invalid_body")
        raw = self.rfile.read(length)
        try:
            body = json.loads(raw.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError):
            raise APIError(HTTPStatus.BAD_REQUEST, "请求体必须是 JSON", "invalid_json")
        if not isinstance(body, dict):
            raise APIError(HTTPStatus.BAD_REQUEST, "请求体必须是对象", "invalid_body")
        return body

    def _client_identity(self):
        peer = str(self.client_address[0] or "unknown")
        try:
            trusted_proxy = ipaddress.ip_address(peer).is_private or ipaddress.ip_address(peer).is_loopback
        except ValueError:
            trusted_proxy = False
        if trusted_proxy:
            for value in self.headers.get("X-Forwarded-For", "").split(","):
                candidate = value.strip()
                try:
                    return str(ipaddress.ip_address(candidate))
                except ValueError:
                    continue
        return peer

    def _request_host_is_loopback(self):
        host = str(self.headers.get("Host", "")).strip()
        if host.startswith("[") and "]" in host:
            hostname = host[1:host.index("]")]
        else:
            hostname = host.rsplit(":", 1)[0]
        if hostname.lower() == "localhost":
            return True
        try:
            return ipaddress.ip_address(hostname).is_loopback
        except ValueError:
            return False

    def _cookie_token(self, name):
        cookie = SimpleCookie()
        try:
            cookie.load(self.headers.get("Cookie", ""))
        except Exception:
            return ""
        morsel = cookie.get(name)
        return morsel.value if morsel else ""

    def _authentication_context(self):
        provided = self.headers.get("X-Management-Key", "")
        if provided:
            if self.app.authenticate_management_request(provided, self._client_identity()):
                return {"kind": "management_key"}
            return None
        session = self.app.admin_sessions.resolve(
            self._cookie_token(ADMIN_SESSION_COOKIE)
        )
        return {"kind": "session", "session": session} if session else None

    def _authenticated(self):
        try:
            return bool(self._authentication_context())
        except APIError:
            return False

    def _require_auth(self):
        context = self._authentication_context()
        if not context:
            raise APIError(HTTPStatus.UNAUTHORIZED, "管理密钥无效", "unauthorized")
        if context["kind"] == "session" and self.command not in ("GET", "HEAD"):
            if not self.app.admin_sessions.verify_csrf(
                context["session"],
                self.headers.get("X-CSRF-Token"),
            ):
                raise APIError(HTTPStatus.FORBIDDEN, "管理会话校验失败", "csrf_required")
        return context

    def _require_management_key(self):
        provided = self.headers.get("X-Management-Key", "")
        if not self.app.authenticate_management_request(
            provided,
            self._client_identity(),
        ):
            raise APIError(HTTPStatus.UNAUTHORIZED, "管理密钥无效", "unauthorized")

    def _portal_session_token(self):
        return self._cookie_token(PORTAL_SESSION_COOKIE)

    def _require_portal_user(self, allow_password_change=False):
        session = self.app.portal_session(self._portal_session_token())
        if session["password_change_required"] and not allow_password_change:
            raise APIError(
                HTTPStatus.FORBIDDEN,
                "首次登录请先修改初始密码",
                "password_change_required",
            )
        return session["user"]

    def _request_uses_https(self):
        forwarded = self.headers.get("X-Forwarded-Proto", "")
        scheme = forwarded.split(",", 1)[0].strip().lower()
        return scheme == "https"

    def _session_cookie(self, name, token, path, max_age, same_site):
        attributes = [
            "{}={}".format(name, token),
            "Path={}".format(path),
            "Max-Age={}".format(int(max_age)),
            "HttpOnly",
        ]
        if self._request_uses_https():
            attributes.append("Secure")
        attributes.append("SameSite={}".format(same_site))
        return "; ".join(attributes)

    def _portal_cookie(self, token, max_age):
        return self._session_cookie(
            PORTAL_SESSION_COOKIE,
            token,
            "/usage",
            max_age,
            "Lax",
        )

    def _admin_cookie(self, token, max_age):
        return self._session_cookie(
            ADMIN_SESSION_COOKIE,
            token,
            "/admin",
            max_age,
            "Strict",
        )

    def _serve_static(self, path):
        if path == "/admin/reasoning-effort-colors.css":
            raw = self.app.reasoning_effort_color_stylesheet().encode("utf-8")
            self._headers(
                HTTPStatus.OK,
                "text/css; charset=utf-8",
                len(raw),
            )
            self.wfile.write(raw)
            return True
        files = {
            "/admin/": ("index.html", "text/html; charset=utf-8"),
            "/admin/index.html": ("index.html", "text/html; charset=utf-8"),
            "/admin/app.css": ("app.css", "text/css; charset=utf-8"),
            "/admin/app.js": ("app.js", "text/javascript; charset=utf-8"),
            "/admin/monitor-utils.js": ("monitor-utils.js", "text/javascript; charset=utf-8"),
            "/admin/view-state-utils.js": ("view-state-utils.js", "text/javascript; charset=utf-8"),
        }
        item = files.get(path)
        source = self.app.static_dir / item[0] if item else None
        content_type = item[1] if item else None
        if not source or not source.is_file():
            return False
        raw = source.read_bytes()
        self._headers(HTTPStatus.OK, content_type, len(raw))
        self.wfile.write(raw)
        return True

    def do_GET(self):
        parsed = urllib.parse.urlsplit(self.path)
        path = parsed.path
        try:
            if path in ("/healthz", "/admin/healthz"):
                self._json(HTTPStatus.OK, {"status": "ok"})
                return
            if path == "/site-config.json":
                self._json(HTTPStatus.OK, self.app.control.public_site_configuration())
                return
            if path == "/branding/logo":
                asset = self.app.control.store.branding_asset("logo")
                if not asset:
                    raise APIError(HTTPStatus.NOT_FOUND, "未配置自定义 Logo", "not_found")
                self._headers(
                    HTTPStatus.OK,
                    asset["content_type"],
                    len(asset["content"]),
                    headers=[("ETag", '"{}"'.format(asset["sha256"]))],
                )
                self.wfile.write(asset["content"])
                return
            if path == "/usage/limits":
                self._json(HTTPStatus.OK, self.app.public_usage_limits())
                return
            if path == "/usage/api":
                raw_window = urllib.parse.parse_qs(parsed.query).get("window", ["300"])[0]
                try:
                    window = int(raw_window)
                except ValueError as error:
                    raise ValueError("统计范围无效") from error
                if window not in (300, 3600, 86400):
                    raise ValueError("统计范围无效")
                self._json(HTTPStatus.OK, self.app.cached_public_gateway_usage(window))
                return
            if path == "/usage/me/route":
                user = self._require_portal_user()
                self._json(HTTPStatus.OK, self.app.self_service_route(user))
                return
            if path == "/usage/me/usage-breakdown":
                user = self._require_portal_user()
                query = urllib.parse.parse_qs(parsed.query)
                window = parse_user_usage_window(query.get("window", [""])[0])
                self._json(
                    HTTPStatus.OK,
                    self.app.self_service_usage_breakdown(
                        user,
                        query.get("account", [""])[0],
                        window,
                    ),
                )
                return
            if path == "/usage/me":
                user = self._require_portal_user()
                query = urllib.parse.parse_qs(parsed.query)
                window = parse_user_usage_window(query.get("window", [""])[0])
                fresh = query.get("fresh", [""])[0] == "1"
                include_lifetime = query.get("lifetime", ["1"])[0] != "0"
                self._json(
                    HTTPStatus.OK,
                    self.app.self_service_dashboard(
                        user,
                        window,
                        force_quota_refresh=fresh,
                        include_lifetime_usage=include_lifetime,
                    ),
                )
                return
            if path == "/admin":
                self.send_response(HTTPStatus.PERMANENT_REDIRECT)
                self.send_header("Location", "/admin/")
                self.send_header("Content-Length", "0")
                self.end_headers()
                return
            if self._serve_static(path):
                return
            if not path.startswith("/admin/api/"):
                self._error(HTTPStatus.NOT_FOUND, "页面不存在", "not_found")
                return
            auth_context = self._require_auth()
            if path == "/admin/api/session":
                payload = {"authenticated": True, "accounts": self.app.control.accounts()}
                if auth_context["kind"] == "session":
                    payload["csrf_token"] = auth_context["session"]["csrf_token"]
            elif path == "/admin/api/overview":
                query = urllib.parse.parse_qs(parsed.query)
                payload = self.app.overview(
                    force_refresh=query.get("fresh", [""])[0] == "1"
                )
            elif path == "/admin/api/overview/catalog":
                query = urllib.parse.parse_qs(parsed.query)
                payload = self.app.overview_catalog(
                    force_refresh=query.get("fresh", [""])[0] == "1"
                )
            elif path == "/admin/api/overview/usage":
                query = urllib.parse.parse_qs(parsed.query)
                usage_range = parse_admin_usage_range(
                    query,
                    parse_overview_usage_window,
                )
                payload = self.app.overview_usage(
                    usage_range["window"],
                    account=query.get("account", []),
                    user=query.get("user", []),
                    user_limit=query.get("user_limit", ["10"])[0],
                    custom_start_at=usage_range["start_at"],
                    custom_end_at=usage_range["end_at"],
                    force_refresh=query.get("fresh", [""])[0] == "1",
                )
            elif path == "/admin/api/accounts":
                query = urllib.parse.parse_qs(parsed.query)
                usage_range = parse_admin_usage_range(
                    query,
                    parse_account_usage_window,
                )
                fresh = query.get("fresh", [""])[0] == "1"
                payload = self.app.account_management(
                    usage_range["window"],
                    force_quota_refresh=fresh,
                    custom_start_at=usage_range["start_at"],
                    custom_end_at=usage_range["end_at"],
                )
            elif path == "/admin/api/accounts/usage-breakdown":
                query = urllib.parse.parse_qs(parsed.query)
                usage_range = parse_admin_usage_range(
                    query,
                    parse_account_usage_window,
                )
                payload = self.app.account_usage_breakdown(
                    query.get("account", [""])[0],
                    usage_range["window"],
                    custom_start_at=usage_range["start_at"],
                    custom_end_at=usage_range["end_at"],
                )
            elif path == "/admin/api/images/cliproxy":
                payload = self.app.cliproxy_image_status()
            elif path == "/admin/api/users/usage-breakdown":
                query = urllib.parse.parse_qs(parsed.query)
                usage_range = parse_admin_usage_range(
                    query,
                    parse_user_usage_window,
                )
                payload = self.app.user_usage_breakdown(
                    query.get("email", [""])[0],
                    usage_range["window"],
                    account=query.get("account", [""])[0],
                    custom_start_at=usage_range["start_at"],
                    custom_end_at=usage_range["end_at"],
                )
            elif path == "/admin/api/users/detail":
                query = urllib.parse.parse_qs(parsed.query)
                usage_range = parse_admin_usage_range(
                    query,
                    parse_user_usage_window,
                )
                payload = self.app.user_management_detail(
                    query.get("email", [""])[0],
                    usage_range["window"],
                    custom_start_at=usage_range["start_at"],
                    custom_end_at=usage_range["end_at"],
                )
            elif path == "/admin/api/users/quota":
                query = urllib.parse.parse_qs(parsed.query)
                payload = self.app.user_quota(query.get("email", [""])[0])
            elif path == "/admin/api/teams/usage-breakdown":
                query = urllib.parse.parse_qs(parsed.query)
                usage_range = parse_admin_usage_range(
                    query,
                    parse_user_usage_window,
                )
                payload = self.app.team_usage_breakdown(
                    query.get("team_id", [""])[0],
                    usage_range["window"],
                    custom_start_at=usage_range["start_at"],
                    custom_end_at=usage_range["end_at"],
                )
            elif path == "/admin/api/teams/usage":
                query = urllib.parse.parse_qs(parsed.query)
                usage_range = parse_admin_usage_range(
                    query,
                    parse_user_usage_window,
                )
                payload = self.app.team_usage_management(
                    usage_range["window"],
                    custom_start_at=usage_range["start_at"],
                    custom_end_at=usage_range["end_at"],
                )
            elif path == "/admin/api/teams":
                payload = self.app.organization_catalog()
            elif path == "/admin/api/tags":
                payload = self.app.organization_catalog()
            elif path == "/admin/api/users":
                query = urllib.parse.parse_qs(parsed.query)
                usage_range = parse_admin_usage_range(
                    query,
                    parse_user_usage_window,
                )
                if query.get("view", [""])[0] == "summary":
                    payload = self.app.user_management_page(
                        usage_range["window"],
                        custom_start_at=usage_range["start_at"],
                        custom_end_at=usage_range["end_at"],
                        page=query.get("page", ["1"])[0],
                        page_size=query.get("page_size", ["50"])[0],
                        search=query.get("q", [""])[0],
                        sort=query.get("sort", ["tokens"])[0],
                        direction=query.get("direction", ["desc"])[0],
                        team_id=query.get("team_id", [""])[0],
                        tag_id=query.get("tag_id", [""])[0],
                        usage_state=query.get("usage_state", ["all"])[0],
                        tag_membership=query.get("tag_membership", [""])[0],
                    )
                else:
                    payload = self.app.user_management(
                        usage_range["window"],
                        custom_start_at=usage_range["start_at"],
                        custom_end_at=usage_range["end_at"],
                    )
            elif path == "/admin/api/settings":
                payload = self.app.settings()
            elif path == "/admin/api/release":
                query = urllib.parse.parse_qs(parsed.query)
                payload = self.app.release_status(
                    force=query.get("fresh", [""])[0] == "1"
                )
            elif path == "/admin/api/native-accounts":
                payload = self.app.native_accounts(
                    include_management_urls=self._request_host_is_loopback(),
                )
            elif path == "/admin/api/operations/impact":
                query = urllib.parse.parse_qs(parsed.query)
                payload = self.app.operation_impact(
                    query.get("action", [""])[0],
                    query.get("target", [""])[0],
                )
            elif path == "/admin/api/jobs":
                payload = {"jobs": self.app.jobs.recent(limit=30)}
            elif path.startswith("/admin/api/jobs/"):
                payload = {"job": self.app.jobs.get(path.rsplit("/", 1)[-1])}
            elif path == "/admin/api/logs":
                query = urllib.parse.parse_qs(parsed.query)
                payload = self.app.logs(query.get("target", ["all"])[0])
            else:
                raise APIError(HTTPStatus.NOT_FOUND, "接口不存在", "not_found")
            self._json(HTTPStatus.OK, payload)
        except APIError as error:
            self._error(error.status, error.message, error.code, error.headers)
        except (ValueError, json.JSONDecodeError) as error:
            self._error(HTTPStatus.BAD_REQUEST, error, "invalid_request")
        except Exception as error:
            traceback.print_exc()
            self._error(HTTPStatus.INTERNAL_SERVER_ERROR, error)

    def do_POST(self):
        parsed = urllib.parse.urlsplit(self.path)
        path = parsed.path
        try:
            if path == "/usage/session":
                body = self._read_json()
                payload = self.app.create_portal_session(
                    body.get("email"),
                    body.get("password"),
                    client_identity=self._client_identity(),
                )
                token = payload.pop("token")
                self._json(
                    HTTPStatus.CREATED,
                    payload,
                    headers=[
                        (
                            "Set-Cookie",
                            self._portal_cookie(
                                token,
                                self.app._portal_session_ttl_seconds(),
                            ),
                        )
                    ],
                )
                return
            if path == "/usage/me/key/rotate":
                user = self._require_portal_user()
                body = self._read_json()
                payload = self.app.rotate_self_service_key(
                    user,
                    confirm=body.get("confirm"),
                )
                self.app._invalidate_runtime_query_cache()
                self._json(HTTPStatus.OK, payload)
                return
            if path == "/admin/api/session":
                self._require_management_key()
                session = self.app.create_admin_session(self._client_identity())
                self._json(
                    HTTPStatus.CREATED,
                    {
                        "authenticated": True,
                        "accounts": self.app.control.accounts(),
                        "csrf_token": session["csrf_token"],
                    },
                    headers=[
                        (
                            "Set-Cookie",
                            self._admin_cookie(
                                session["token"],
                                ADMIN_SESSION_TTL_SECONDS,
                            ),
                        )
                    ],
                )
                return
            if path == "/my-keys/api":
                raise APIError(
                    HTTPStatus.GONE,
                    "邮箱查询已停用，请进入使用中心",
                    "email_lookup_removed",
                )
                return
            if not path.startswith("/admin/api/"):
                raise APIError(HTTPStatus.NOT_FOUND, "接口不存在", "not_found")
            self._require_auth()
            body = self._read_json(
                BRANDING_MAX_BODY_BYTES
                if path == "/admin/api/settings/logo"
                else MAX_BODY_BYTES
            )
            if path == "/admin/api/users":
                payload = self.app.create_user(body)
                status = HTTPStatus.CREATED
            elif path == "/admin/api/teams":
                payload = self.app.create_team(body)
                status = HTTPStatus.CREATED
            elif path == "/admin/api/tags":
                payload = self.app.create_tag(body)
                status = HTTPStatus.CREATED
            elif path == "/admin/api/users/team/batch":
                payload = self.app.update_user_team(body, batch=True)
                status = HTTPStatus.OK
            elif path == "/admin/api/users/tags/batch":
                payload = self.app.update_user_tags_batch(body)
                status = HTTPStatus.OK
            elif path == "/admin/api/accounts":
                payload = self.app.create_account(body)
                status = HTTPStatus.CREATED
            elif path == "/admin/api/accounts/update":
                payload = self.app.update_account(body)
                status = HTTPStatus.OK
            elif path == "/admin/api/accounts/reset-quota":
                payload = self.app.reset_account_weekly_quota(body)
                status = HTTPStatus.OK
            elif path == "/admin/api/accounts/policy":
                payload = self.app.update_account_policy(body)
                status = HTTPStatus.OK
            elif path == "/admin/api/accounts/rebalance":
                payload = self.app.rebalance_account_users(body)
                status = HTTPStatus.OK
            elif path == "/admin/api/accounts/clear-auth":
                payload = self.app.clear_account_auth(body)
                status = HTTPStatus.OK
            elif path == "/admin/api/accounts/delete":
                payload = self.app.delete_account(body)
                status = HTTPStatus.OK
            elif path == "/admin/api/users/revoke":
                payload = self.app.revoke_user(body)
                status = HTTPStatus.OK
            elif path == "/admin/api/users/reset-password":
                payload = self.app.reset_user_password(body)
                status = HTTPStatus.OK
            elif path == "/admin/api/users/delete":
                payload = self.app.delete_user(body)
                status = HTTPStatus.OK
            elif path == "/admin/api/users/quota-actions":
                payload = self.app.update_user_quota_actions(body)
                status = HTTPStatus.OK
            elif path == "/admin/api/keys/create":
                payload = self.app.create_key(body)
                status = HTTPStatus.CREATED
            elif path == "/admin/api/keys/rotate":
                payload = self.app.rotate_key(body)
                status = HTTPStatus.OK
            elif path == "/admin/api/keys/revoke":
                payload = self.app.revoke_key(body)
                status = HTTPStatus.OK
            elif path == "/admin/api/operations":
                payload = self.app.operation(body)
                status = HTTPStatus.ACCEPTED
            elif path == "/admin/api/jobs/cancel":
                payload = {
                    "message": "已请求取消任务",
                    "job": self.app.jobs.cancel(str(body.get("id", ""))),
                }
                status = HTTPStatus.ACCEPTED
            elif path == "/admin/api/settings/management-key":
                payload = self.app.rotate_management_key(body)
                status = HTTPStatus.OK
            elif path == "/admin/api/settings/initial-password":
                payload = self.app.update_initial_portal_password(body)
                status = HTTPStatus.OK
            elif path == "/admin/api/settings/configuration":
                payload = self.app.update_configuration(body)
                status = HTTPStatus.OK
            elif path == "/admin/api/settings/logo":
                payload = self.app.update_branding_logo(body)
                status = HTTPStatus.OK
            elif path == "/admin/api/settings/notification-webhook":
                payload = self.app.update_notification_webhook(body)
                status = HTTPStatus.OK
            elif path == "/admin/api/settings/notification-webhook/clear":
                payload = self.app.clear_notification_webhook(body)
                status = HTTPStatus.OK
            elif path in (
                "/admin/api/notifications/send",
                "/admin/api/notifications/test",
            ):
                payload = self.app.send_notification()
                status = HTTPStatus.OK
            else:
                raise APIError(HTTPStatus.NOT_FOUND, "接口不存在", "not_found")
            self.app._invalidate_runtime_query_cache()
            self._json(status, payload)
        except APIError as error:
            self._error(error.status, error.message, error.code, error.headers)
        except (ValueError, json.JSONDecodeError) as error:
            self._error(HTTPStatus.BAD_REQUEST, error, "invalid_request")
        except subprocess.CalledProcessError as error:
            self._error(HTTPStatus.INTERNAL_SERVER_ERROR, error, "operation_failed")
        except Exception as error:
            traceback.print_exc()
            self._error(HTTPStatus.INTERNAL_SERVER_ERROR, error)

    def do_PUT(self):
        parsed = urllib.parse.urlsplit(self.path)
        try:
            if parsed.path == "/usage/me/password":
                token = self._portal_session_token()
                self._require_portal_user(allow_password_change=True)
                body = self._read_json()
                payload = self.app.change_portal_password(
                    token,
                    body.get("current_password"),
                    body.get("new_password"),
                )
            elif parsed.path == "/usage/me/group":
                user = self._require_portal_user()
                body = self._read_json()
                payload = self.app.switch_self_service_group(user, body.get("group_id"))
                self.app._invalidate_runtime_query_cache()
            elif parsed.path == "/admin/api/users/quota":
                self._require_auth()
                payload = self.app.update_user_quota(self._read_json())
            elif parsed.path == "/admin/api/teams":
                self._require_auth()
                payload = self.app.update_team(self._read_json())
            elif parsed.path == "/admin/api/tags":
                self._require_auth()
                payload = self.app.update_tag(self._read_json())
            elif parsed.path == "/admin/api/users/team":
                self._require_auth()
                payload = self.app.update_user_team(self._read_json())
            elif parsed.path == "/admin/api/users/tags":
                self._require_auth()
                payload = self.app.update_user_tags(self._read_json())
            else:
                raise APIError(HTTPStatus.NOT_FOUND, "接口不存在", "not_found")
            if parsed.path.startswith("/admin/api/"):
                self.app._invalidate_runtime_query_cache()
            self._json(HTTPStatus.OK, payload)
        except APIError as error:
            self._error(error.status, error.message, error.code, error.headers)
        except (ValueError, json.JSONDecodeError) as error:
            self._error(HTTPStatus.BAD_REQUEST, error, "invalid_request")
        except subprocess.CalledProcessError as error:
            self._error(HTTPStatus.INTERNAL_SERVER_ERROR, error, "operation_failed")
        except Exception as error:
            traceback.print_exc()
            self._error(HTTPStatus.INTERNAL_SERVER_ERROR, error)

    def do_DELETE(self):
        parsed = urllib.parse.urlsplit(self.path)
        try:
            if parsed.path == "/usage/session":
                payload = self.app.revoke_portal_session(self._portal_session_token())
                self._json(
                    HTTPStatus.OK,
                    payload,
                    headers=[("Set-Cookie", self._portal_cookie("", 0))],
                )
                return
            if parsed.path == "/admin/api/session":
                self._require_auth()
                self.app.admin_sessions.revoke(
                    self._cookie_token(ADMIN_SESSION_COOKIE)
                )
                self._json(
                    HTTPStatus.OK,
                    {"logged_out": True},
                    headers=[("Set-Cookie", self._admin_cookie("", 0))],
                )
                return
            if parsed.path == "/admin/api/users/quota":
                self._require_auth()
                query = urllib.parse.parse_qs(parsed.query)
                payload = self.app.clear_user_quota(query.get("email", [""])[0])
                self.app._invalidate_runtime_query_cache()
                self._json(HTTPStatus.OK, payload)
                return
            if parsed.path == "/admin/api/settings/logo":
                self._require_auth()
                payload = self.app.clear_branding_logo(self._read_json())
                self.app._invalidate_runtime_query_cache()
                self._json(HTTPStatus.OK, payload)
                return
            if parsed.path == "/admin/api/teams":
                self._require_auth()
                query = urllib.parse.parse_qs(parsed.query)
                payload = self.app.delete_team(query.get("id", [""])[0])
                self.app._invalidate_runtime_query_cache()
                self._json(HTTPStatus.OK, payload)
                return
            if parsed.path == "/admin/api/tags":
                self._require_auth()
                query = urllib.parse.parse_qs(parsed.query)
                payload = self.app.delete_tag(query.get("id", [""])[0])
                self.app._invalidate_runtime_query_cache()
                self._json(HTTPStatus.OK, payload)
                return
            else:
                raise APIError(HTTPStatus.NOT_FOUND, "接口不存在", "not_found")
        except APIError as error:
            self._error(error.status, error.message, error.code, error.headers)
        except (ValueError, json.JSONDecodeError) as error:
            self._error(HTTPStatus.BAD_REQUEST, error, "invalid_request")
        except Exception as error:
            traceback.print_exc()
            self._error(HTTPStatus.INTERNAL_SERVER_ERROR, error)


class AdminHTTPServer(ThreadingHTTPServer):
    """HTTP server backed by a bounded executor instead of unbounded threads."""

    daemon_threads = True

    def __init__(self, address, app, max_workers=None, max_queue=None):
        self.app = app
        max_workers = self._positive_int(
            max_workers,
            os.environ.get("CLIPROXY_ADMIN_MAX_WORKERS", ADMIN_HTTP_MAX_WORKERS),
        )
        max_queue = self._non_negative_int(
            max_queue,
            os.environ.get("CLIPROXY_ADMIN_MAX_QUEUE", ADMIN_HTTP_MAX_QUEUE),
        )
        super().__init__(address, AdminRequestHandler)
        self._closing = False
        self._close_lock = threading.Lock()
        self._executor_closed = False
        self._close_complete = threading.Event()
        self._request_slots = threading.BoundedSemaphore(max_workers + max_queue)
        self._executor = concurrent.futures.ThreadPoolExecutor(
            max_workers=max_workers,
            thread_name_prefix="admin-http",
        )

    @staticmethod
    def _positive_int(value, default):
        try:
            return max(1, int(default if value is None else value))
        except (TypeError, ValueError):
            return ADMIN_HTTP_MAX_WORKERS

    @staticmethod
    def _non_negative_int(value, default):
        try:
            return max(0, int(default if value is None else value))
        except (TypeError, ValueError):
            return ADMIN_HTTP_MAX_QUEUE

    def _reject_overloaded(self, request):
        body = json.dumps(
            {
                "error": {
                    "code": "server_overloaded",
                    "message": "管理服务繁忙，请稍后重试",
                }
            },
            ensure_ascii=False,
            separators=(",", ":"),
        ).encode("utf-8")
        response = (
            "HTTP/1.1 503 Service Unavailable\r\n"
            "Content-Type: application/json; charset=utf-8\r\n"
            "Content-Length: {}\r\n"
            "Cache-Control: no-store\r\n"
            "Retry-After: 1\r\n"
            "Connection: close\r\n\r\n"
        ).format(len(body)).encode("ascii") + body
        try:
            request.sendall(response)
        except OSError:
            pass
        finally:
            self.shutdown_request(request)

    def process_request(self, request, client_address):
        if self._closing or not self._request_slots.acquire(blocking=False):
            self._reject_overloaded(request)
            return
        try:
            request.settimeout(ADMIN_HTTP_REQUEST_TIMEOUT_SECONDS)
            future = self._executor.submit(
                self.process_request_thread,
                request,
                client_address,
            )
        except BaseException:
            self._request_slots.release()
            self._reject_overloaded(request)
            return
        future.add_done_callback(lambda unused: self._request_slots.release())

    def server_close(self):
        with self._close_lock:
            self._closing = True
            close_executor = not self._executor_closed
            self._executor_closed = True
        if not close_executor:
            self._close_complete.wait()
            return
        try:
            super().server_close()
        finally:
            try:
                # Queued requests drain before shutdown returns; active sockets
                # have a finite timeout, so process exit cannot wait forever.
                self._executor.shutdown(wait=True, cancel_futures=False)
            finally:
                self._close_complete.set()


def build_parser():
    parser = argparse.ArgumentParser(description="CLIProxyAPI 综合管理服务")
    parser.add_argument("--host", default=os.environ.get("CLIPROXY_ADMIN_HOST", "0.0.0.0"))
    parser.add_argument("--port", type=int, default=int(os.environ.get("CLIPROXY_ADMIN_PORT", "8318")))
    parser.add_argument("--root", default=str(PROJECT_ROOT))
    return parser


def main(argv=None):
    args = build_parser().parse_args(argv)
    app = AdminApplication(args.root)
    notification_scheduler = NotificationScheduler(app)
    failover_scheduler = AccountFailoverScheduler(app)
    server = AdminHTTPServer((args.host, args.port), app)
    notification_scheduler.start()
    failover_scheduler.start()
    print("CPA admin server listening on {}:{}".format(args.host, args.port), flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        failover_scheduler.stop()
        notification_scheduler.stop()
        server.server_close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
