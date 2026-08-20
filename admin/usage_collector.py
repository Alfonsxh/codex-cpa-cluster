#!/usr/bin/env python3
"""Drain CLIProxyAPI RESP usage queues into the local SQLite usage store."""

import argparse
import json
import os
import signal
import socket
import sys
import threading
import time
from pathlib import Path


APPLICATION_ROOT = Path(
    os.environ.get("CLIPROXY_APP_ROOT", Path(__file__).resolve().parents[1])
).resolve()
PROJECT_ROOT = Path(os.environ.get("CLIPROXY_ROOT", APPLICATION_ROOT)).resolve()
sys.path.insert(0, str(APPLICATION_ROOT / "scripts"))
sys.path.insert(0, str(APPLICATION_ROOT / "admin"))
import cliproxy  # noqa: E402
from usage_store import UsageStore, reasoning_effort_multipliers  # noqa: E402


class RESPError(RuntimeError):
    pass


class RESPConnection:
    def __init__(self, host, port=8317, timeout=5):
        self.socket = socket.create_connection((host, int(port)), timeout=timeout)
        self.socket.settimeout(timeout)
        self.stream = self.socket.makefile("rb")

    def close(self):
        try:
            self.stream.close()
        finally:
            self.socket.close()

    def command(self, *parts):
        encoded = [str(part).encode("utf-8") for part in parts]
        payload = b"*" + str(len(encoded)).encode("ascii") + b"\r\n"
        for part in encoded:
            payload += b"$" + str(len(part)).encode("ascii") + b"\r\n" + part + b"\r\n"
        self.socket.sendall(payload)
        return self._read_reply()

    def _readline(self):
        line = self.stream.readline()
        if not line:
            raise RESPError("usage queue connection closed")
        if not line.endswith(b"\r\n"):
            raise RESPError("invalid usage queue response")
        return line[:-2]

    def _read_reply(self):
        prefix = self.stream.read(1)
        if not prefix:
            raise RESPError("usage queue connection closed")
        if prefix == b"+":
            return self._readline().decode("utf-8", errors="replace")
        if prefix == b"-":
            raise RESPError(self._readline().decode("utf-8", errors="replace"))
        if prefix == b":":
            return int(self._readline())
        if prefix == b"$":
            length = int(self._readline())
            if length < 0:
                return None
            payload = self.stream.read(length)
            if len(payload) != length or self.stream.read(2) != b"\r\n":
                raise RESPError("truncated usage queue response")
            return payload
        if prefix == b"*":
            length = int(self._readline())
            if length < 0:
                return None
            return [self._read_reply() for _ in range(length)]
        raise RESPError("unsupported usage queue response")


class UsageCollector:
    def __init__(
        self,
        root=PROJECT_ROOT,
        batch_size=100,
        heartbeat_stale_after_seconds=15,
    ):
        self.root = Path(root).resolve()
        self.control = cliproxy.ControlPlane(self.root)
        self.control.ensure_layout()
        self.key_path = Path(
            os.environ.get(
                "CLIPROXY_MANAGEMENT_KEY_FILE",
                self.root / "secrets" / "cpa-management.key",
            )
        )
        configuration = self.control.configuration()["values"]
        self.store = UsageStore(
            self.root / "state" / "usage.sqlite3",
            week_timezone=configuration["user_quota.timezone"],
            reset_personal_weekly_on_new_week=configuration[
                "user_quota.reset_personal_weekly_on_new_week"
            ],
        )
        self.batch_size = max(1, min(int(batch_size), 500))
        self.heartbeat_stale_after_seconds = max(5, int(heartbeat_stale_after_seconds))

    def _management_key(self):
        try:
            key = self.key_path.read_text(encoding="utf-8").strip()
        except FileNotFoundError:
            key = self.control.management_key()
        if not key:
            raise RuntimeError("management key is empty")
        return key

    def _drain(self, host, management_key):
        connection = RESPConnection(host)
        try:
            if connection.command("AUTH", management_key) != "OK":
                raise RESPError("usage queue authentication failed")
            while True:
                raw = connection.command("LPOP", "usage", self.batch_size)
                if raw is None or raw == []:
                    break
                items = raw if isinstance(raw, list) else [raw]
                payloads = []
                for item in items:
                    if not isinstance(item, (bytes, bytearray)):
                        continue
                    try:
                        payload = json.loads(bytes(item).decode("utf-8"))
                    except (UnicodeDecodeError, json.JSONDecodeError):
                        continue
                    if isinstance(payload, dict):
                        payloads.append(payload)
                if payloads:
                    yield payloads
                if len(items) < self.batch_size:
                    break
        finally:
            connection.close()

    def run_once(self):
        records = self.control._read_registry()
        accounts = self.control.accounts()
        active_users = sorted({
            item["user"] for item in records if item.get("status") == "active"
        })
        # Keep external identities during the rolling upgrade so queued events
        # emitted before CPA configs switch to internal keys remain attributable.
        identity_records = records + self.control.internal_identity_records()
        self.store.sync_identities(identity_records)
        identity_users = sorted(
            {
                str(item.get("user") or "").strip().lower()
                for item in identity_records
                if str(item.get("user") or "").strip()
            }
        )
        self.store.sync_user_teams(
            self.control.store.read_user_classifications(identity_users)
        )
        self.store.ensure_usage_breakdown_started()
        management_key = self._management_key()
        totals = {"received": 0, "inserted": 0, "duplicate": 0, "unmapped": 0, "ignored": 0}
        errors = []
        quota_configuration = {}
        multipliers = reasoning_effort_multipliers()
        try:
            quota_configuration = self.control.configuration()["values"]
            multipliers = reasoning_effort_multipliers(quota_configuration)
            self.store.configure_personal_quota_weekly_reset(
                quota_configuration[
                    "user_quota.reset_personal_weekly_on_new_week"
                ]
            )
        except Exception as error:
            errors.append(
                "quota configuration: {}: {}".format(type(error).__name__, error)
            )
        for account, service in self.control.services().items():
            metadata = accounts.get(account) or {}
            if metadata.get("group_enabled") is False:
                continue
            try:
                for payloads in self._drain(service, management_key):
                    counters = self.store.ingest_events(
                        account,
                        payloads,
                        reasoning_multipliers=multipliers,
                    )
                    for name in totals:
                        totals[name] += counters[name]
            except Exception as error:
                message = str(error).replace(management_key, "[REDACTED]")
                errors.append("{}: {}: {}".format(account, type(error).__name__, message))
        try:
            default_limit = quota_configuration["user_quota.default_weekly_tokens"]
            quotas = self.store.weekly_quotas(active_users, default_limit)
            self.control.publish_quota_snapshot(quotas)
        except Exception as error:
            errors.append("quota snapshot: {}: {}".format(type(error).__name__, error))
        error_text = "; ".join(errors)
        self.store.update_collector_status(error_text)
        try:
            self.control.publish_quota_heartbeat(
                ok=not errors,
                error=error_text,
                stale_after_seconds=self.heartbeat_stale_after_seconds,
                fail_open_after_seconds=quota_configuration.get(
                    "user_quota.fail_open_after_seconds", 300
                ),
            )
        except Exception as error:
            errors.append("quota heartbeat: {}: {}".format(type(error).__name__, error))
            self.store.update_collector_status("; ".join(errors))
        return {**totals, "errors": errors}


def build_parser():
    parser = argparse.ArgumentParser(description="CLIProxyAPI usage queue collector")
    parser.add_argument("--root", default=str(PROJECT_ROOT))
    parser.add_argument("--interval", type=float)
    parser.add_argument("--batch-size", type=int)
    parser.add_argument("--once", action="store_true")
    parser.add_argument("--health", action="store_true")
    parser.add_argument("--rebuild-weekly-usage", action="store_true")
    return parser


def main(argv=None):
    args = build_parser().parse_args(argv)
    control = cliproxy.ControlPlane(args.root)
    control.ensure_layout()
    configuration = control.configuration()["values"]
    interval = (
        args.interval
        if args.interval is not None
        else configuration["collector.interval_seconds"]
    )
    batch_size = (
        args.batch_size
        if args.batch_size is not None
        else configuration["collector.batch_size"]
    )
    collector = UsageCollector(
        args.root,
        batch_size=batch_size,
        heartbeat_stale_after_seconds=max(15, int(float(interval) * 3 + 1)),
    )
    if args.rebuild_weekly_usage:
        result = collector.store.rebuild_weekly_usage()
        active_users = sorted({
            item["user"]
            for item in collector.control.active_records()
        })
        quotas = collector.store.weekly_quotas(
            active_users,
            configuration["user_quota.default_weekly_tokens"],
        )
        collector.control.publish_quota_snapshot(quotas)
        collector.control.publish_quota_heartbeat(
            ok=True,
            stale_after_seconds=collector.heartbeat_stale_after_seconds,
            fail_open_after_seconds=configuration[
                "user_quota.fail_open_after_seconds"
            ],
        )
        print(json.dumps(result, ensure_ascii=False, separators=(",", ":")), flush=True)
        return 0
    if args.health:
        status = collector.store.status()
        return 0 if status["status"] == "healthy" and status["heartbeat_at"] else 1
    if args.once:
        result = collector.run_once()
        print(json.dumps(result, ensure_ascii=False, separators=(",", ":")), flush=True)
        return 0 if not result["errors"] else 1

    stopping = threading.Event()

    def stop(*unused):
        stopping.set()

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    print("CPA usage collector started", flush=True)
    while not stopping.is_set():
        try:
            result = collector.run_once()
            if result["received"] or result["errors"]:
                print(json.dumps(result, ensure_ascii=False, separators=(",", ":")), flush=True)
        except Exception as error:
            safe = str(error)
            print("usage collector failed: {}: {}".format(type(error).__name__, safe), file=sys.stderr, flush=True)
            try:
                collector.store.update_collector_status("{}: {}".format(type(error).__name__, safe))
            except Exception:
                pass
        stopping.wait(max(0.5, interval))
    return 0


if __name__ == "__main__":
    sys.exit(main())
