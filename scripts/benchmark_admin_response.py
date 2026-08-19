#!/usr/bin/env python3
"""Build a disposable local dataset and benchmark Admin summary responses."""

import argparse
import json
import os
import sqlite3
import statistics
import sys
import tempfile
import time
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
sys.path.insert(0, str(ROOT / "admin"))

import cliproxy  # noqa: E402
import server as admin_server  # noqa: E402
from control_plane_store import ControlPlaneStore  # noqa: E402
from usage_store import (  # noqa: E402
    UsageStore,
    WEEKLY_USAGE_BACKFILL_KEY,
    WEEKLY_USAGE_BACKFILL_VERSION,
    WEEKLY_USAGE_LAST_EVENT_ID_KEY,
    natural_week_bounds,
)


def account_id(index):
    return "cpa-{:03d}".format(index)


def user_email(index):
    return "user{:05d}@example.com".format(index)


def seed_control_plane(root, user_count, account_count, now):
    store = ControlPlaneStore(root)
    accounts = [
        {
            "id": account_id(index),
            "email": "account{:03d}@accounts.example.com".format(index),
            "port": 19000 + index,
            "created_at": now - account_count + index,
            "group_enabled": True,
            "default_group": index == 0,
        }
        for index in range(account_count)
    ]
    store.write_accounts(accounts)
    store.write_settings(
        {
            "identity.allowed_email_domains": ["example.com"],
            "identity.key_prefix": "cpa_",
            "branding.public_base_url": "http://127.0.0.1:18317",
        }
    )

    def key_records():
        for user_index in range(user_count):
            email = user_email(user_index)
            secret = "cpa_user{:05d}_00000000-0000-4000-8000-{:012d}".format(
                user_index,
                user_index,
            )
            for account_index, account in enumerate(accounts):
                yield {
                    "label": "cpa_user{:05d}".format(user_index),
                    "account": account["id"],
                    "account_email": account["email"],
                    "user": email,
                    "status": "active",
                    "key": secret,
                    "created_at": now - user_count + user_index,
                    "updated_at": now - user_count + user_index + account_index,
                }

    store.write_key_records(key_records())
    store.write_routes(
        {
            user_email(index): account_id(index % account_count)
            for index in range(user_count)
        }
    )
    return store


def usage_rows(event_count, user_count, account_count, now):
    for index in range(event_count):
        user_index = index % user_count
        account_index = (index * 17) % account_count
        occurred_at = now - (index % 86400)
        total_tokens = 100 + index % 1900
        failed = 1 if index % 97 == 0 else 0
        yield (
            "benchmark:{}".format(index),
            account_id(account_index),
            user_email(user_index),
            "cpa_user{:05d}".format(user_index),
            occurred_at,
            "request-{}".format(index),
            "openai",
            "gpt-5.6-sol",
            "gpt-5.6-sol",
            "high" if index % 3 == 0 else "medium",
            "POST /v1/responses",
            failed,
            100 + index % 500,
            total_tokens * 3 // 4,
            total_tokens // 4,
            total_tokens // 8,
            total_tokens // 5,
            total_tokens,
            1.0,
            total_tokens,
            "benchmark-v1",
        )


def seed_usage(root, event_count, user_count, account_count, now):
    path = Path(root) / "state" / "usage.sqlite3"
    UsageStore(path)
    connection = sqlite3.connect(str(path), timeout=30)
    try:
        connection.execute("PRAGMA synchronous = OFF")
        connection.executemany(
            """
            INSERT INTO usage_events(
                event_key, account, user_email, key_label, occurred_at,
                request_id, provider, model, alias, reasoning_effort,
                endpoint, failed, latency_ms, input_tokens, output_tokens,
                reasoning_tokens, cached_tokens, total_tokens, quota_multiplier,
                weighted_tokens, weight_policy_version
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            usage_rows(event_count, user_count, account_count, now),
        )
        week_start, week_end = natural_week_bounds(now, "UTC")
        connection.execute("DELETE FROM user_weekly_usage")
        connection.execute(
            """
            INSERT INTO user_weekly_usage(
                user_email, week_start_at, total_tokens, weighted_tokens,
                request_count, updated_at
            )
            SELECT user_email, ?, SUM(total_tokens), SUM(weighted_tokens),
                   COUNT(*), ?
              FROM usage_events
             WHERE occurred_at >= ? AND occurred_at < ?
             GROUP BY user_email
            """,
            (week_start, now, week_start, week_end),
        )
        last_event_id = connection.execute(
            "SELECT MAX(id) FROM usage_events"
        ).fetchone()[0]
        connection.executemany(
            "INSERT OR REPLACE INTO usage_meta(key, value) VALUES (?, ?)",
            (
                (WEEKLY_USAGE_BACKFILL_KEY, WEEKLY_USAGE_BACKFILL_VERSION),
                (WEEKLY_USAGE_LAST_EVENT_ID_KEY, str(int(last_event_id or 0))),
                ("collector_heartbeat_at", str(now)),
                ("collector_last_error", ""),
            ),
        )
        connection.commit()
    finally:
        connection.close()
    os.chmod(path, 0o600)


def elapsed_ms(callback):
    started = time.perf_counter()
    result = callback()
    return (time.perf_counter() - started) * 1000, result


def percentile(values, percent):
    ordered = sorted(values)
    if not ordered:
        return 0.0
    index = min(len(ordered) - 1, max(0, round((len(ordered) - 1) * percent)))
    return ordered[index]


def run_benchmark(root, samples):
    control = cliproxy.ControlPlane(root)
    app = admin_server.AdminApplication(root=root, control=control)
    cold_ms, payload = elapsed_ms(
        lambda: app.user_management_page(
            86400,
            page=1,
            page_size=50,
            sort="tokens",
            direction="desc",
        )
    )
    warm = []
    for _ in range(samples):
        duration, payload = elapsed_ms(
            lambda: app.user_management_page(
                86400,
                page=1,
                page_size=50,
                sort="tokens",
                direction="desc",
            )
        )
        warm.append(duration)
    email = payload["users"][0]["email"]
    detail_ms, detail = elapsed_ms(
        lambda: app.user_management_detail(email, 86400)
    )
    raw = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode(
        "utf-8"
    )
    return {
        "cold_summary_ms": round(cold_ms, 2),
        "warm_summary_median_ms": round(statistics.median(warm), 2),
        "warm_summary_p95_ms": round(percentile(warm, 0.95), 2),
        "detail_ms": round(detail_ms, 2),
        "summary_payload_bytes": len(raw),
        "returned_users": len(payload["users"]),
        "total_users": payload["pagination"]["total"],
        "detail_accounts": len(detail["user"]["accounts"]),
    }


def main():
    parser = argparse.ArgumentParser(
        description="在本地临时目录模拟 Admin 大数据量并验收响应耗时"
    )
    parser.add_argument("--users", type=int, default=2000)
    parser.add_argument("--accounts", type=int, default=120)
    parser.add_argument("--events", type=int, default=300000)
    parser.add_argument("--samples", type=int, default=10)
    parser.add_argument("--serve", action="store_true")
    parser.add_argument("--port", type=int, default=0)
    parser.add_argument("--max-warm-p95-ms", type=float, default=300)
    parser.add_argument("--max-detail-ms", type=float, default=500)
    parser.add_argument("--max-payload-bytes", type=int, default=200000)
    args = parser.parse_args()
    if args.users < 1 or args.accounts < 1 or args.events < 0 or args.samples < 1:
        parser.error("users/accounts/samples 必须为正数，events 不能为负数")

    now = int(time.time())
    with tempfile.TemporaryDirectory(prefix="cpa-admin-benchmark-") as temporary:
        root = Path(temporary)
        seed_started = time.perf_counter()
        seed_control_plane(root, args.users, args.accounts, now)
        seed_usage(root, args.events, args.users, args.accounts, now)
        seed_ms = (time.perf_counter() - seed_started) * 1000
        if args.serve:
            key_file = root / "secrets" / "cpa-management.key"
            key_file.write_text("local-benchmark-key\n", encoding="utf-8")
            os.chmod(key_file, 0o600)
            control = cliproxy.ControlPlane(root)
            app = admin_server.AdminApplication(
                root=root,
                key_file=key_file,
                control=control,
            )
            httpd = admin_server.AdminHTTPServer(("127.0.0.1", args.port), app)
            print(
                json.dumps(
                    {
                        "url": "http://127.0.0.1:{}/admin/".format(
                            httpd.server_port
                        ),
                        "management_key": "local-benchmark-key",
                        "users": args.users,
                        "accounts": args.accounts,
                        "events": args.events,
                        "temporary_data": str(root),
                    },
                    ensure_ascii=False,
                ),
                flush=True,
            )
            try:
                httpd.serve_forever()
            except KeyboardInterrupt:
                pass
            finally:
                httpd.server_close()
            return 0
        result = run_benchmark(root, args.samples)
    result.update(
        {
            "seed_ms": round(seed_ms, 2),
            "users": args.users,
            "accounts": args.accounts,
            "events": args.events,
            "temporary_data_removed": True,
        }
    )
    failures = []
    if result["warm_summary_p95_ms"] > args.max_warm_p95_ms:
        failures.append("warm_summary_p95_ms")
    if result["detail_ms"] > args.max_detail_ms:
        failures.append("detail_ms")
    if result["summary_payload_bytes"] > args.max_payload_bytes:
        failures.append("summary_payload_bytes")
    result["accepted"] = not failures
    result["failed_metrics"] = failures
    print(json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True))
    return 0 if not failures else 1


if __name__ == "__main__":
    raise SystemExit(main())
