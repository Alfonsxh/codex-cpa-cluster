#!/usr/bin/env python3
"""Verify internal routing and the real public auth/quota path during release."""

import argparse
import json
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path


SCRIPT_DIR = Path(__file__).resolve().parent
if str(SCRIPT_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPT_DIR))

from cliproxy import ControlPlane  # noqa: E402


BLOCKED_PUBLIC_PATHS = (
    "/gateway-release-unknown-path",
    "/v0/management/auth-files",
    "/management.html",
    "/codex/callback",
    "/healthz",
)


def read_json(path):
    try:
        payload = json.loads(Path(path).read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise RuntimeError("Gateway probe snapshot cannot be read: {}".format(path)) from error
    if not isinstance(payload, dict):
        raise RuntimeError("Gateway probe snapshot must be an object: {}".format(path))
    return payload


def expected_public_status(
    user,
    quota_snapshot,
    heartbeat,
    now=None,
    loader_success_at=None,
    fail_open_after=None,
):
    """Mirror request_gate's fail-open decision for one external user."""
    now = int(time.time()) if now is None else int(now)
    last_success_at = int(heartbeat.get("last_success_at") or 0)
    fail_open_after = max(
        30,
        int(
            fail_open_after
            if fail_open_after is not None
            else heartbeat.get("fail_open_after_seconds") or 300
        ),
    )
    if last_success_at <= 0 or now - last_success_at > fail_open_after:
        return 200
    if loader_success_at is not None and (
        int(loader_success_at) <= 0 or now - int(loader_success_at) > fail_open_after
    ):
        return 200
    records = quota_snapshot.get("records")
    if not isinstance(records, list):
        raise RuntimeError("Gateway quota snapshot records are invalid")
    quota = next(
        (
            record
            for record in records
            if isinstance(record, dict) and record.get("user_email") == user
        ),
        None,
    )
    if quota is None:
        return 200
    week_end = int(quota.get("week_end_at") or now)
    if now >= week_end:
        return 200
    limit = int(quota.get("limit_tokens", -1))
    used = int(quota.get("used_tokens", 0))
    return 429 if limit >= 0 and used >= limit else 200


def routed_records(app):
    accounts = {
        account: metadata
        for account, metadata in app.accounts().items()
        if metadata.get("group_enabled") is not False
    }
    active = app.active_records()
    by_user = {}
    for record in active:
        by_user.setdefault(record["user"], []).append(record)
    by_account = {}
    for user, records in by_user.items():
        if len({record["key"] for record in records}) == 1:
            route = app.explicit_user_route(user, accounts=accounts)
            record = next(
                (item for item in records if item["account"] == route),
                None,
            )
            if record:
                by_account.setdefault(route, record)
            continue
        for record in records:
            if record["account"] in accounts:
                by_account.setdefault(record["account"], record)
    if active and not by_account:
        raise RuntimeError("active external Keys exist but none has a routable account")
    return accounts, active, by_account


def request_json(opener, url, key, accepted_statuses):
    request = urllib.request.Request(
        url,
        headers={"Authorization": "Bearer " + key},
    )
    try:
        with opener.open(request, timeout=20) as response:
            return response.status, json.load(response), response.headers
    except urllib.error.HTTPError as error:
        if error.code not in accepted_statuses:
            raise
        try:
            payload = json.load(error)
        except (TypeError, ValueError) as decode_error:
            raise RuntimeError("Gateway public probe returned invalid JSON") from decode_error
        return error.code, payload, error.headers


def request_status(opener, url, key):
    request = urllib.request.Request(
        url,
        headers={"Authorization": "Bearer " + key},
    )
    try:
        with opener.open(request, timeout=20) as response:
            return response.status
    except urllib.error.HTTPError as error:
        return error.code


def verify_blocked_public_paths(opener, public_url, key, label):
    for path in BLOCKED_PUBLIC_PATHS:
        status = request_status(opener, public_url.rstrip("/") + path, key)
        if status != 404:
            raise RuntimeError(
                "{} blocked path {} returned {}, expected 404".format(
                    label, path, status
                )
            )
    print(
        "{} blocked public paths verified: {}".format(
            label, len(BLOCKED_PUBLIC_PATHS)
        ),
        flush=True,
    )


def consistent_runtime_quota(opener, internal_url, app, attempts=10):
    last = ("", "", 0, 0)
    for _ in range(attempts):
        with opener.open(
            internal_url.rstrip("/") + "/__internal/snapshots", timeout=5
        ) as response:
            runtime_status = json.load(response)
        quota_snapshot = read_json(app.quota_snapshot_path)
        heartbeat = read_json(app.quota_heartbeat_path)
        runtime_quota = runtime_status.get("quota", {})
        runtime_generation = str(runtime_quota.get("active_generation") or "")
        file_generation = str(quota_snapshot.get("generation") or "")
        runtime_last_success = int(runtime_quota.get("last_success_at") or 0)
        file_last_success = int(heartbeat.get("last_success_at") or 0)
        last = (
            runtime_generation,
            file_generation,
            runtime_last_success,
            file_last_success,
        )
        if (
            runtime_generation
            and runtime_generation == file_generation
            and runtime_last_success == file_last_success
        ):
            return quota_snapshot, heartbeat, runtime_quota
        time.sleep(0.2)
    raise RuntimeError(
        "Gateway runtime/file quota state differs: generation={}/{} heartbeat={}/{}".format(
            *last
        )
    )


def verify(root, public_url, internal_url, label="Gateway"):
    root = Path(root).resolve()
    app = ControlPlane(root)
    accounts, active, by_account = routed_records(app)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    routed_record = next(iter(by_account.values()), None)
    verify_blocked_public_paths(
        opener,
        public_url,
        routed_record["key"] if routed_record else "gateway-release-invalid-key",
        label,
    )
    quota_snapshot, heartbeat, runtime_quota = consistent_runtime_quota(
        opener, internal_url, app
    )
    verified = 0
    for account in accounts:
        record = by_account.get(account)
        if not record:
            print("{} has no external Key route; skipped".format(account), flush=True)
            continue

        internal_status, internal_payload, _ = request_json(
            opener,
            internal_url.rstrip("/") + "/__internal/probe/models",
            record["key"],
            {200},
        )
        internal_models = (
            internal_payload.get("data") if isinstance(internal_payload, dict) else None
        )
        if internal_status != 200 or not isinstance(internal_models, list):
            raise RuntimeError("{} internal route probe failed".format(account))

        expected = expected_public_status(
            record["user"],
            quota_snapshot,
            heartbeat,
            loader_success_at=runtime_quota.get("snapshot_loader_success_at"),
            fail_open_after=runtime_quota.get("fail_open_after"),
        )
        public_status, public_payload, public_headers = request_json(
            opener,
            public_url.rstrip("/") + "/v1/models",
            record["key"],
            {200, 429},
        )
        if public_status != expected:
            raise RuntimeError(
                "{} public auth/quota probe returned {}, expected {}".format(
                    account, public_status, expected
                )
            )
        if public_status == 200:
            models = public_payload.get("data") if isinstance(public_payload, dict) else None
            if not isinstance(models, list):
                raise RuntimeError("{} public model response is invalid".format(account))
        else:
            error = public_payload.get("error", {}) if isinstance(public_payload, dict) else {}
            if error.get("code") != "weekly_user_token_quota_exceeded":
                raise RuntimeError("{} public quota response is invalid".format(account))
            if not public_headers.get("Retry-After"):
                raise RuntimeError("{} public quota response lacks Retry-After".format(account))
        verified += 1
        print(
            "{} {} public path verified: status={} internal_models={}".format(
                label, account, public_status, len(internal_models)
            ),
            flush=True,
        )
    if active and verified == 0:
        raise RuntimeError("no authenticated Gateway route was verified")
    print("{} authenticated routes verified: {}".format(label, verified), flush=True)


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", required=True)
    parser.add_argument("--public-url", required=True)
    parser.add_argument("--internal-url", required=True)
    parser.add_argument("--label", default="Gateway")
    args = parser.parse_args(argv)
    verify(args.root, args.public_url, args.internal_url, label=args.label)


if __name__ == "__main__":
    main()
