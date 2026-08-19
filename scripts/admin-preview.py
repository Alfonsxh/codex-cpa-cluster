#!/usr/bin/env python3
"""Serve the local Admin frontend with mock data or a bounded remote API proxy."""

import argparse
import http.client
import ipaddress
import json
import secrets
import ssl
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from http import HTTPStatus
from http.cookies import SimpleCookie
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


REPOSITORY_ROOT = Path(__file__).resolve().parents[1]
DEFAULT_UPSTREAM = "https://unused.invalid"
SESSION_COOKIE = "cpa_admin_preview_session"
SESSION_TTL_SECONDS = 8 * 60 * 60
MAX_BODY_BYTES = 64 * 1024
MAX_WRITE_BODY_BYTES = 3 * 1024 * 1024
MAX_UPSTREAM_BODY_BYTES = 16 * 1024 * 1024

LOCAL_STATIC_FILES = {
    "/admin/": ("admin/static/index.html", "text/html; charset=utf-8"),
    "/admin/index.html": ("admin/static/index.html", "text/html; charset=utf-8"),
    "/admin/app.css": ("admin/static/app.css", "text/css; charset=utf-8"),
    "/admin/app.js": ("admin/static/app.js", "text/javascript; charset=utf-8"),
    "/admin/monitor-utils.js": ("admin/static/monitor-utils.js", "text/javascript; charset=utf-8"),
    "/admin/view-state-utils.js": ("admin/static/view-state-utils.js", "text/javascript; charset=utf-8"),
    "/portal/branding.js": ("portal/branding.js", "text/javascript; charset=utf-8"),
    "/portal/token-usage.js": ("portal/token-usage.js", "text/javascript; charset=utf-8"),
}
LOCAL_ASSET_NAMES = {
    "codex-cpa-cluster-favicon.svg",
    "codex-cpa-cluster-favicon-dark.svg",
    "codex-cpa-cluster-logo.svg",
    "codex-cpa-cluster-logo-dark.svg",
    "codex-cpa-cluster-mark.svg",
    "codex-cpa-cluster-mark-dark.svg",
}
PUBLIC_REMOTE_GET_PATHS = {
    "/site-config.json",
    "/branding/logo",
    "/admin/reasoning-effort-colors.css",
}
REMOTE_ADMIN_GET_PATHS = {
    "/admin/api/session",
    "/admin/api/overview",
    "/admin/api/overview/catalog",
    "/admin/api/overview/usage",
    "/admin/api/accounts",
    "/admin/api/accounts/usage-breakdown",
    "/admin/api/images/cliproxy",
    "/admin/api/users",
    "/admin/api/users/detail",
    "/admin/api/users/quota",
    "/admin/api/users/usage-breakdown",
    "/admin/api/teams",
    "/admin/api/tags",
    "/admin/api/teams/usage",
    "/admin/api/teams/usage-breakdown",
    "/admin/api/settings",
    "/admin/api/release",
    "/admin/api/native-accounts",
    "/admin/api/jobs",
    "/admin/api/logs",
    "/admin/api/operations/impact",
}
REMOTE_ADMIN_WRITE_PATHS = {
    "POST": {
        "/admin/api/users",
        "/admin/api/teams",
        "/admin/api/tags",
        "/admin/api/users/team/batch",
        "/admin/api/users/tags/batch",
        "/admin/api/accounts",
        "/admin/api/accounts/update",
        "/admin/api/accounts/reset-quota",
        "/admin/api/accounts/policy",
        "/admin/api/accounts/rebalance",
        "/admin/api/accounts/clear-auth",
        "/admin/api/accounts/delete",
        "/admin/api/users/revoke",
        "/admin/api/users/reset-password",
        "/admin/api/users/delete",
        "/admin/api/users/quota-actions",
        "/admin/api/keys/create",
        "/admin/api/keys/rotate",
        "/admin/api/keys/revoke",
        "/admin/api/operations",
        "/admin/api/jobs/cancel",
        "/admin/api/settings/management-key",
        "/admin/api/settings/initial-password",
        "/admin/api/settings/configuration",
        "/admin/api/settings/logo",
        "/admin/api/settings/notification-webhook",
        "/admin/api/settings/notification-webhook/clear",
        "/admin/api/notifications/send",
        "/admin/api/notifications/test",
    },
    "PUT": {
        "/admin/api/users/quota",
        "/admin/api/teams",
        "/admin/api/tags",
        "/admin/api/users/team",
        "/admin/api/users/tags",
    },
    "DELETE": {
        "/admin/api/users/quota",
        "/admin/api/settings/logo",
        "/admin/api/teams",
        "/admin/api/tags",
    },
}


def json_bytes(payload):
    return json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def error_payload(message, code):
    return {"error": {"code": code, "message": message}}


def empty_usage():
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


def usage_record(total_tokens, *, requests=0, failed=0, last_used_at=0, weighted=None):
    total = int(total_tokens)
    return {
        "request_count": int(requests),
        "success_count": max(0, int(requests) - int(failed)),
        "failed_count": int(failed),
        "input_tokens": round(total * 0.62),
        "output_tokens": round(total * 0.21),
        "reasoning_tokens": round(total * 0.11),
        "cached_tokens": round(total * 0.06),
        "total_tokens": total,
        "weighted_tokens": int(weighted if weighted is not None else round(total * 1.18)),
        "last_used_at": int(last_used_at),
    }


def weekly_quota(now, used_tokens, limit_tokens=20_000_000):
    used_tokens = int(used_tokens)
    limit_tokens = int(limit_tokens)
    return {
        "period": "weekly",
        "policy_mode": "inherit",
        "policy_tokens": None,
        "default_limit_tokens": limit_tokens,
        "base_limit_tokens": limit_tokens,
        "limit_tokens": limit_tokens,
        "unlimited": False,
        "used_tokens": used_tokens,
        "weighted_used_tokens": used_tokens,
        "raw_used_tokens": round(used_tokens / 1.18),
        "remaining_tokens": max(0, limit_tokens - used_tokens),
        "used_percent": round(used_tokens * 100 / limit_tokens, 2),
        "limit_reached": used_tokens >= limit_tokens,
        "bonus_tokens": 0,
        "usage_reset_tokens": 0,
        "adjustment_count": 0,
        "week_start_at": now - 2 * 86400,
        "week_end_at": now + 5 * 86400,
    }


def model_breakdown(now, factor=1.0, weighted=False):
    rows = [
        ("gpt-5.6-sol", "high", 38, 5_480_000),
        ("gpt-5.6-sol", "medium", 31, 3_820_000),
        ("gpt-5.6-terra", "medium", 24, 2_460_000),
        ("gpt-5.5", "low", 17, 1_180_000),
    ]
    combinations = []
    for model, effort, requests, tokens in rows:
        usage = usage_record(round(tokens * factor), requests=round(requests * factor), last_used_at=now - 300)
        if not weighted:
            usage.pop("weighted_tokens")
        combinations.append({"model": model, "reasoning_effort": effort, **usage})
    totals = empty_usage()
    models = {}
    for row in combinations:
        model = models.setdefault(row["model"], {"model": row["model"], **empty_usage()})
        for field in empty_usage():
            value = int(row.get(field, 0))
            if field == "last_used_at":
                totals[field] = max(totals[field], value)
                model[field] = max(model[field], value)
            else:
                totals[field] += value
                model[field] += value
    if not weighted:
        totals.pop("weighted_tokens")
        for model in models.values():
            model.pop("weighted_tokens")
    return {
        "collection_started_at": now - 45 * 86400,
        "effective_start_at": now - 86400,
        "totals": totals,
        "models": sorted(models.values(), key=lambda item: -item["total_tokens"]),
        "combinations": combinations,
    }


class MockAdminData:
    """Deterministic, UI-shaped preview data with no persistent writes."""

    def __init__(self):
        self.now = int(time.time())
        self.teams = [
            {"id": "platform", "name": "平台研发", "description": "核心平台与基础设施", "user_count": 2, "created_at": self.now - 80 * 86400, "updated_at": self.now - 3600},
            {"id": "data", "name": "数据智能", "description": "数据产品与分析", "user_count": 1, "created_at": self.now - 55 * 86400, "updated_at": self.now - 7200},
            {"id": "design", "name": "产品设计", "description": "产品与体验设计", "user_count": 1, "created_at": self.now - 35 * 86400, "updated_at": self.now - 10800},
        ]
        self.tags = [
            {"id": "core", "name": "核心成员", "color": "#6374d8", "user_count": 2, "created_at": self.now - 70 * 86400, "updated_at": self.now - 3600},
            {"id": "pilot", "name": "试点用户", "color": "#8b72c8", "user_count": 2, "created_at": self.now - 30 * 86400, "updated_at": self.now - 7200},
            {"id": "external", "name": "外部协作", "color": "#5965c7", "user_count": 1, "created_at": self.now - 15 * 86400, "updated_at": self.now - 10800},
        ]
        self.users = [
            self._user("lin.chen@example.com", "platform", ("core",), 9_650_000, 128, 2),
            self._user("mei.zhao@example.com", "platform", ("pilot",), 6_280_000, 94, 1),
            self._user("kai.wang@example.com", "data", ("core", "pilot"), 4_920_000, 76, 0),
            self._user("rui.sun@example.com", "design", ("external",), 2_760_000, 49, 1),
            self._user("yan.liu@example.com", "", (), 880_000, 21, 0),
        ]
        self.accounts = [
            self._account("cpa-main", "main@example.com", 19001, 17_320_000, 211, 3, 48.2),
            self._account("cpa-lab", "lab@example.com", 19002, 7_860_000, 101, 1, 71.4),
            self._account("cpa-edge", "edge@example.com", 19003, 3_310_000, 56, 2, 22.8),
        ]

    def _team(self, team_id):
        return next((item for item in self.teams if item["id"] == team_id), None)

    def _tag(self, tag_id):
        return next(item for item in self.tags if item["id"] == tag_id)

    def _user(self, email, team_id, tag_ids, tokens, requests, failed):
        usage = usage_record(tokens, requests=requests, failed=failed, last_used_at=self.now - 480)
        return {
            "email": email,
            "status": "active",
            "active_keys": 1,
            "total_records": 3,
            "created_at": self.now - 60 * 86400,
            "updated_at": self.now - 600,
            "account_count": 3,
            "active_accounts": 3,
            "usage": usage,
            "weekly_quota": weekly_quota(self.now, round(usage["weighted_tokens"] * 1.4)),
            "team_id": team_id or None,
            "team": self._team(team_id) if team_id else None,
            "tags": [self._tag(tag_id) for tag_id in tag_ids],
        }

    def _account(self, account_id, email, port, tokens, requests, failed, used_percent):
        remaining = max(0.0, 100.0 - float(used_percent))
        active_user_count = min(len(self.users), max(1, requests // 30))
        active_user_emails = [
            user["email"] for user in self.users[:active_user_count]
        ]
        quota = {
            "status": "ok",
            "allowed": True,
            "limit_reached": False,
            "weekly": {"used_percent": used_percent, "remaining_percent": remaining, "reset_at": self.now + 4 * 86400, "resettable": True},
            "weekly_windows": [{"key": "primary", "label": "常规周限额", "used_percent": used_percent, "remaining_percent": remaining, "reset_at": self.now + 4 * 86400, "resettable": True}],
            "reset_credits": {"available_count": 2, "credits": [{"id": "preview-credit", "title": "Full reset", "expires_at": self.now + 30 * 86400}]},
        }
        return {
            "id": account_id,
            "group_name": account_id,
            "email": email,
            "port": port,
            "created_at": self.now - 90 * 86400,
            "group_enabled": True,
            "default_group": account_id == "cpa-main",
            "service": "cliproxy-{}".format(account_id),
            "container_state": "running",
            "container_status": "Up 8 days (healthy)",
            "container_health": "healthy",
            "auth_files": 1,
            "auth_state": "configured",
            "associated_users": len(self.users) if hasattr(self, "users") else 5,
            "routed_users": 2,
            "active_users_1h": active_user_count,
            "active_user_emails_1h": active_user_emails,
            "quota": quota,
            "usage": usage_record(tokens, requests=requests, failed=failed, last_used_at=self.now - 300),
            "usage_window_start_at": self.now - 86400,
            "usage_window_available": True,
            "runtime": {"state": "available", "error_count": failed, "rate_429_count": 0, "error_log_status": "ok", "error_log_files": 0},
            "operational_status": {"code": "available", "label": "可用", "tone": "success", "reason": "容器、OAuth 与额度均正常", "selectable": True},
        }

    @staticmethod
    def _collector(now):
        return {"status": "healthy", "heartbeat_at": now - 10, "generated_at": now}

    def site_config(self):
        return {
            "version": 1,
            "product_name": "Codex CPA Cluster",
            "short_name": "Codex CPA",
            "environment_label": "本地模拟预览",
            "public_base_url": "",
            "provider_name": "Codex CPA",
            "api_key_env": "CPA_API_KEY",
            "default_model": "gpt-5.6-sol",
            "logo": {"custom": False, "url": "/portal/assets/codex-cpa-cluster-logo.svg", "sha256": ""},
        }

    def overview(self):
        services = [
            {"service": "gateway", "name": "preview-gateway", "state": "running", "status": "Up (healthy)", "health": "healthy"},
            {"service": "admin", "name": "preview-admin", "state": "running", "status": "Up (healthy)", "health": "healthy"},
            {"service": "usage-collector", "name": "preview-collector", "state": "running", "status": "Up", "health": ""},
            *[{"service": account["service"], "name": "preview-{}".format(account["id"]), "state": "running", "status": account["container_status"], "health": "healthy"} for account in self.accounts],
        ]
        return {
            "generated_at": self.now,
            "summary": {"users": len(self.users), "active_keys": len(self.users), "authorized_accounts": len(self.accounts), "running_services": len(services), "total_services": len(services), "requests_5m": 74, "business_accounts": len(self.accounts)},
            "accounts": self.accounts,
            "services": services,
            "warnings": [],
            "recent_jobs": [],
        }

    def overview_catalog(self):
        return {
            "generated_at": self.now,
            "accounts": [
                {
                    "id": account["id"],
                    "operational_status": account["operational_status"],
                }
                for account in self.accounts
            ],
            "users": [
                {"email": user["email"], "status": user["status"]}
                for user in self.users
            ],
        }

    def operation_impact(self, query):
        target = query.get("target", [""])[0]
        account = next(
            (item for item in self.accounts if item["id"] == target),
            None,
        )
        return {
            "action": "stop",
            "target": target,
            "target_type": "account" if account else "service",
            "routed_users": account["routed_users"] if account else None,
        }

    def overview_usage(self, query):
        bucket_seconds = 900
        buckets = [self.now - bucket_seconds * index for index in reversed(range(25))]
        account_values = {
            "cpa-main": [260_000 + ((index * 83_000) % 540_000) for index in range(len(buckets))],
            "cpa-lab": [110_000 + ((index * 47_000) % 290_000) for index in range(len(buckets))],
            "cpa-edge": [45_000 + ((index * 29_000) % 150_000) for index in range(len(buckets))],
        }
        selected_accounts = query.get("account", [])
        selected_users = [item.lower() for item in query.get("user", [])]
        account_names = selected_accounts or list(account_values)

        def series(name, values):
            return {"name": name, "values": values, "current": values[-1], "average": round(sum(values) / len(values)), "maximum": max(values), "total": sum(values)}

        account_series = [series(name, account_values[name]) for name in account_names if name in account_values]
        user_series = []
        for index, user in enumerate(self.users):
            if selected_users and user["email"].lower() not in selected_users:
                continue
            values = [round((70_000 + ((point * (31_000 + index * 4_000)) % 220_000)) * (1 - index * 0.08)) for point in range(len(buckets))]
            user_series.append(series(user["email"], values))
        limit = max(1, min(int(query.get("user_limit", ["10"])[0]), 50))
        return {
            "generated_at": self.now,
            "window": query.get("window", ["today"])[0],
            "window_start_at": buckets[0],
            "window_seconds": self.now - buckets[0],
            "bucket_seconds": bucket_seconds,
            "buckets": buckets,
            "accounts": account_series,
            "users": sorted(user_series, key=lambda item: -item["total"])[:limit],
            "selected_accounts": selected_accounts,
            "selected_users": selected_users,
            "user_limit": limit,
            "unavailable_accounts": [],
            "collector": self._collector(self.now),
        }

    def account_page(self):
        return {
            "generated_at": self.now,
            "window": "today",
            "window_start_at": self.now - 86400,
            "window_end_at": self.now,
            "window_seconds": 86400,
            "quota_generated_at": self.now,
            "quota_cached": True,
            "quota_refreshing": False,
            "accounts": self.accounts,
            "collector": self._collector(self.now),
        }

    def user_page(self, query):
        users = list(self.users)
        search = query.get("q", [""])[0].lower()
        team_id = query.get("team_id", [""])[0]
        tag_id = query.get("tag_id", [""])[0]
        tag_membership = query.get("tag_membership", ["tagged"])[0]
        usage_state = query.get("usage_state", ["all"])[0]
        if search:
            users = [user for user in users if search in user["email"].lower() or search in str((user.get("team") or {}).get("name", "")).lower()]
        if team_id:
            users = [user for user in users if (not user["team_id"] if team_id == "unassigned" else user["team_id"] == team_id)]
        if tag_id:
            users = [user for user in users if ((tag_id in {tag["id"] for tag in user["tags"]}) == (tag_membership != "untagged"))]
        if usage_state == "used":
            users = [user for user in users if user["usage"]["total_tokens"] > 0]
        elif usage_state == "unused":
            users = [user for user in users if user["usage"]["total_tokens"] == 0]
        page_size = int(query.get("page_size", ["50"])[0])
        page = max(1, int(query.get("page", ["1"])[0]))
        total = len(users)
        total_pages = max(1, (total + page_size - 1) // page_size)
        page = min(page, total_pages)
        users = users[(page - 1) * page_size:page * page_size]
        return {
            "generated_at": self.now,
            "summary_generated_at": self.now,
            "summary_cached": True,
            "window": query.get("window", ["today"])[0],
            "window_start_at": self.now - 86400,
            "window_end_at": self.now,
            "window_seconds": 86400,
            "users": users,
            "accounts": {account["id"]: {"email": account["email"]} for account in self.accounts},
            "teams": self.teams,
            "tags": self.tags,
            "collector": self._collector(self.now),
            "pagination": {"page": page, "page_size": page_size, "total": total, "total_pages": total_pages},
        }

    def user_detail(self, email):
        summary = next((user for user in self.users if user["email"] == email), self.users[0])
        accounts = []
        for index, account in enumerate(self.accounts):
            accounts.append({
                "account": account["id"],
                "account_email": account["email"],
                "status": "active",
                "history_count": 1,
                "key": {"preview": "cpa_••••{}".format(index + 101), "status": "active"},
                "usage": usage_record(round(summary["usage"]["total_tokens"] * (0.52 - index * 0.13)), requests=max(1, 40 - index * 9), last_used_at=self.now - 600),
            })
        return {"generated_at": self.now, "window": "today", "window_start_at": self.now - 86400, "window_end_at": self.now, "window_seconds": 86400, "user": {**summary, "accounts": accounts}}

    def teams_usage(self):
        rows = []
        for index, team in enumerate(self.teams):
            rows.append({**team, "current_user_count": team["user_count"], "usage": {**usage_record(12_400_000 - index * 3_100_000, requests=140 - index * 30, last_used_at=self.now - 600), "active_users": team["user_count"]}})
        rows.append({"id": "unassigned", "name": "未分组", "description": "尚未分配团队的用户", "user_count": 1, "current_user_count": 1, "usage": {**usage_record(880_000, requests=21, last_used_at=self.now - 900), "active_users": 1}})
        return {"generated_at": self.now, "window": "all", "window_start_at": self.now - 30 * 86400, "window_end_at": self.now, "window_seconds": 30 * 86400, "teams": rows}

    def team_breakdown(self, team_id):
        details = model_breakdown(self.now, factor=1.1, weighted=True)
        team_users = [user for user in self.users if (user["team_id"] or "unassigned") == team_id]
        return {
            "definition": "team_model_reasoning_effort_tokens",
            "generated_at": self.now,
            "window": "today",
            "window_start_at": self.now - 86400,
            "window_end_at": self.now,
            "window_seconds": 86400,
            "team_id": team_id,
            "totals": details["totals"],
            "users": [{"user": user["email"], **user["usage"]} for user in team_users],
            "accounts": [{"account": account["id"], **account["usage"]} for account in self.accounts],
            "models": details["models"],
            "combinations": details["combinations"],
            "series": {"start_at": self.now - 86400, "end_at": self.now, "bucket_seconds": 3600, "buckets": [self.now - 3600 * index for index in reversed(range(25))], "values": [80_000 + ((index * 73_000) % 440_000) for index in range(25)]},
        }

    def settings(self):
        fields = [
            {"key": "branding.product_name", "label": "产品名称", "description": "公开界面展示名称。", "type": "string", "value": "Codex CPA Cluster", "default": "Codex CPA Cluster", "editable": True, "apply_mode": "live"},
            {"key": "user_quota.default_weekly_tokens", "label": "默认周额度", "description": "用户未配置个人策略时使用。", "type": "integer", "value": 20_000_000, "default": 20_000_000, "unit": "Token", "min": 0, "editable": True, "apply_mode": "quota"},
        ]
        return {
            "management_key_configured": True,
            "initial_password_configured": True,
            "notifications": {"webhook_configured": False, "webhook_url": "", "last_success_at": 0, "next_schedule_at": 0, "last_error": ""},
            "account_failover": {"enabled": True, "status": "healthy"},
            "user_quota_operations": {"total_users": len(self.users), "users_with_usage": len(self.users), "total_used_tokens": sum(user["weekly_quota"]["used_tokens"] for user in self.users), "total_raw_used_tokens": sum(user["weekly_quota"]["raw_used_tokens"] for user in self.users), "users_with_personal_policy": 0, "users_with_bonus": 0, "users_with_usage_reset": 0, "week_start_at": self.now - 2 * 86400, "week_end_at": self.now + 5 * 86400},
            "configuration": {"version": 1, "groups": [{"name": "品牌与身份", "fields": [fields[0]]}, {"name": "用户额度", "fields": [fields[1]]}]},
            "storage": [{"label": "控制面数据库", "path": "state/control-plane.sqlite3", "exists": True, "mode": "600"}, {"label": "控制面加密主密钥", "path": "secrets/control-plane.key", "exists": True, "mode": "600"}],
            "backups": {"count": 3, "latest": "backups/accounts/preview"},
            "recent_audit": [{"timestamp": self.now - 1800, "action": "preview.open", "target": "local-mock", "outcome": "accepted"}],
            "branding": self.site_config(),
        }

    def response(self, path, query):
        if path == "/site-config.json":
            return HTTPStatus.OK, self.site_config()
        if path == "/admin/api/session":
            return HTTPStatus.OK, {"authenticated": True, "accounts": {account["id"]: {"email": account["email"]} for account in self.accounts}}
        if path == "/admin/api/overview":
            return HTTPStatus.OK, self.overview()
        if path == "/admin/api/overview/catalog":
            return HTTPStatus.OK, self.overview_catalog()
        if path == "/admin/api/overview/usage":
            return HTTPStatus.OK, self.overview_usage(query)
        if path == "/admin/api/accounts":
            return HTTPStatus.OK, self.account_page()
        if path == "/admin/api/accounts/usage-breakdown":
            return HTTPStatus.OK, {"generated_at": self.now, "window": "today", "window_start_at": self.now - 86400, "window_end_at": self.now, "window_seconds": 86400, "account": query.get("account", [self.accounts[0]["id"]])[0], "definition": "account_model_reasoning_effort_tokens", **model_breakdown(self.now)}
        if path == "/admin/api/images/cliproxy":
            return HTTPStatus.OK, {"target_image": "docker.m.daocloud.io/eceasy/cli-proxy-api:v7.1.23", "local_image": {"available": True, "short_id": "preview123456"}, "running_count": len(self.accounts), "current_count": len(self.accounts), "outdated_count": 0, "accounts": [{"account": account["id"], "running": True, "using_target": True, "image_short_id": "preview123456"} for account in self.accounts]}
        if path == "/admin/api/users":
            return HTTPStatus.OK, self.user_page(query)
        if path == "/admin/api/users/detail":
            return HTTPStatus.OK, self.user_detail(query.get("email", [self.users[0]["email"]])[0])
        if path == "/admin/api/users/quota":
            user = next((item for item in self.users if item["email"] == query.get("email", [""])[0]), self.users[0])
            return HTTPStatus.OK, {"user": user["email"], "weekly_quota": user["weekly_quota"], "adjustments": []}
        if path == "/admin/api/users/usage-breakdown":
            return HTTPStatus.OK, {"generated_at": self.now, "window": "today", "window_start_at": self.now - 86400, "window_end_at": self.now, "window_seconds": 86400, "user": query.get("email", [self.users[0]["email"]])[0], "account": query.get("account", [None])[0], "definition": "successful_model_requests", **model_breakdown(self.now, weighted=True)}
        if path in ("/admin/api/teams", "/admin/api/tags"):
            return HTTPStatus.OK, {"teams": self.teams, "tags": self.tags}
        if path == "/admin/api/teams/usage":
            return HTTPStatus.OK, self.teams_usage()
        if path == "/admin/api/teams/usage-breakdown":
            return HTTPStatus.OK, self.team_breakdown(query.get("team_id", ["unassigned"])[0])
        if path == "/admin/api/settings":
            return HTTPStatus.OK, self.settings()
        if path == "/admin/api/release":
            return HTTPStatus.OK, {
                "configured": True,
                "status": "ok",
                "current_version": "v1.0.0",
                "latest_version": "v1.1.0",
                "latest_revision": "preview",
                "available": True,
                "checked_at": self.now,
            }
        if path == "/admin/api/native-accounts":
            return HTTPStatus.OK, {
                "accounts": [
                    {
                        "id": account["id"],
                        "name": account["id"],
                        "email": account["email"],
                        "status": account["operational_status"],
                    }
                    for account in self.accounts
                ]
            }
        if path == "/admin/api/operations/impact":
            return HTTPStatus.OK, self.operation_impact(query)
        if path == "/admin/api/jobs":
            return HTTPStatus.OK, {"jobs": []}
        if path.startswith("/admin/api/jobs/"):
            return HTTPStatus.NOT_FOUND, error_payload("模拟任务不存在", "not_found")
        if path == "/admin/api/logs":
            return HTTPStatus.OK, {"target": query.get("target", ["all"])[0], "content": "[preview] 本地模拟日志，不含远程运行数据。\n"}
        return HTTPStatus.NOT_FOUND, error_payload("模拟接口不存在", "not_found")


class SessionVault:
    def __init__(self):
        self._sessions = {}
        self._lock = threading.Lock()

    def create(self, management_key):
        token = secrets.token_urlsafe(32)
        csrf_token = secrets.token_urlsafe(24)
        expires_at = int(time.time()) + SESSION_TTL_SECONDS
        with self._lock:
            self._sessions[token] = {"management_key": management_key, "csrf_token": csrf_token, "expires_at": expires_at}
        return token, csrf_token

    def resolve(self, token):
        now = int(time.time())
        with self._lock:
            expired = [key for key, value in self._sessions.items() if value["expires_at"] <= now]
            for key in expired:
                self._sessions.pop(key, None)
            session = self._sessions.get(token)
            return dict(session) if session else None

    def revoke(self, token):
        with self._lock:
            return self._sessions.pop(token, None) is not None


class NoRedirectHandler(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, file_pointer, code, message, headers, new_url):
        return None


class PreviewServer(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True

    def __init__(self, address, *, mode, root, upstream, timeout, ssl_context=None):
        super().__init__(address, PreviewHandler)
        self.mode = mode
        self.root = Path(root).resolve()
        self.upstream = upstream.rstrip("/")
        self.timeout = float(timeout)
        self.sessions = SessionVault()
        self.mock = MockAdminData()
        handlers = [NoRedirectHandler()]
        if ssl_context is not None:
            handlers.insert(0, urllib.request.HTTPSHandler(context=ssl_context))
        self.opener = urllib.request.build_opener(*handlers)


class PreviewHandler(BaseHTTPRequestHandler):
    server_version = "CPAAdminPreview/1"

    def log_message(self, fmt, *args):
        print("[{}] {}".format(self.log_date_time_string(), fmt % args))

    def _send(self, status, raw=b"", content_type="application/json; charset=utf-8", headers=()):
        self.send_response(int(status))
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(raw)))
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("X-Frame-Options", "DENY")
        self.send_header("Referrer-Policy", "no-referrer")
        self.send_header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
        self.send_header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
        self.send_header("X-CPA-Preview-Mode", self.server.mode)
        for name, value in headers:
            self.send_header(name, value)
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(raw)

    def _json(self, status, payload, headers=()):
        self._send(status, json_bytes(payload), headers=headers)

    def _error(self, status, message, code):
        self._json(status, error_payload(message, code))

    def _cookie_token(self):
        cookie = SimpleCookie()
        try:
            cookie.load(self.headers.get("Cookie", ""))
        except Exception:
            return ""
        value = cookie.get(SESSION_COOKIE)
        return value.value if value else ""

    def _session(self):
        return self.server.sessions.resolve(self._cookie_token())

    @staticmethod
    def _session_cookie(token, max_age):
        return "{}={}; Path=/admin; Max-Age={}; HttpOnly; SameSite=Strict".format(SESSION_COOKIE, token, int(max_age))

    def _read_body(self):
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            raise ValueError("Content-Length 无效")
        if length <= 0 or length > MAX_BODY_BYTES:
            raise ValueError("请求体为空或过大")
        return self.rfile.read(length)

    def _read_json(self):
        try:
            payload = json.loads(self._read_body().decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError) as error:
            raise ValueError("请求体必须是 JSON") from error
        if not isinstance(payload, dict):
            raise ValueError("请求体必须是对象")
        return payload

    def _local_file(self, path):
        record = LOCAL_STATIC_FILES.get(path)
        if record:
            return self.server.root / record[0], record[1]
        if path.startswith("/portal/assets/"):
            name = path.removeprefix("/portal/assets/")
            if name in LOCAL_ASSET_NAMES:
                return self.server.root / "portal" / "assets" / name, "image/svg+xml"
        return None, None

    def _serve_local(self, path):
        source, content_type = self._local_file(path)
        if not source or not source.is_file():
            return False
        raw = source.read_bytes()
        if path == "/admin/app.css":
            raw += (
                b"\n.preview-environment-banner{position:fixed;z-index:9999;left:50%;top:8px;"
                b"transform:translateX(-50%);padding:6px 12px;border-radius:999px;"
                b"background:#20263d;color:#fff;font:600 12px/1.4 system-ui;"
                b"box-shadow:0 5px 18px #0003;pointer-events:none}"
                b".preview-environment-banner.preview-write-enabled{background:#9b2c2c;"
                b"box-shadow:0 5px 18px #621b1b66}"
            )
        if path in ("/admin/", "/admin/index.html"):
            labels = {
                "mock": ("本地模拟数据", ""),
                "remote-read-only": ("测试环境数据 · 只读", ""),
                "remote-read-write": ("测试环境数据 · 可读写", " preview-write-enabled"),
            }
            label, class_name = labels[self.server.mode]
            banner = '<div class="preview-environment-banner{}" role="status">{}</div>'.format(class_name, label)
            raw = raw.replace(b"<body>", ("<body>" + banner).encode("utf-8"), 1)
        self._send(HTTPStatus.OK, raw, content_type)
        return True

    @staticmethod
    def _allowed_remote_get(path):
        return path in PUBLIC_REMOTE_GET_PATHS or path in REMOTE_ADMIN_GET_PATHS or (
            path.startswith("/admin/api/jobs/") and path.count("/") == 4
        )

    @staticmethod
    def _allowed_remote_write(method, path):
        return path in REMOTE_ADMIN_WRITE_PATHS.get(str(method or "").upper(), set())

    @staticmethod
    def _sanitized_query(raw_query):
        pairs = urllib.parse.parse_qsl(raw_query, keep_blank_values=True)
        return urllib.parse.urlencode(
            [(name, value) for name, value in pairs if name.lower() != "fresh"],
            doseq=True,
        )

    def _upstream_request(self, method, path, query, management_key=None, body=None):
        url = "{}{}".format(self.server.upstream, path)
        safe_query = self._sanitized_query(query) if self.server.mode == "remote-read-only" else query
        if safe_query:
            url += "?" + safe_query
        headers = {"Accept": self.headers.get("Accept", "application/json"), "Accept-Encoding": "identity", "User-Agent": "CPA-Admin-Local-Preview/1"}
        if management_key:
            headers["X-Management-Key"] = management_key
        if body:
            headers["Content-Type"] = self.headers.get("Content-Type", "application/json")
        request = urllib.request.Request(url, data=body or None, headers=headers, method=method)
        try:
            response = self.server.opener.open(request, timeout=self.server.timeout)
        except urllib.error.HTTPError as error:
            response = error
        except (OSError, urllib.error.URLError, http.client.HTTPException) as error:
            self._error(HTTPStatus.BAD_GATEWAY, "测试环境代理暂不可用：{}".format(type(error).__name__), "upstream_unavailable")
            return None
        try:
            raw = response.read(MAX_UPSTREAM_BODY_BYTES + 1)
            if len(raw) > MAX_UPSTREAM_BODY_BYTES:
                self._error(HTTPStatus.BAD_GATEWAY, "测试环境响应过大", "upstream_response_too_large")
                return None
            content_type = response.headers.get_content_type()
            charset = response.headers.get_content_charset() or "utf-8"
            if content_type.startswith("text/") or content_type in ("application/json", "application/javascript"):
                content_type = "{}; charset={}".format(content_type, charset)
            return response.status, raw, content_type
        finally:
            response.close()

    def _upstream_get(self, path, query, management_key=None):
        return self._upstream_request("GET", path, query, management_key)

    def _proxy(self, method, path, query, management_key=None, body=None):
        result = self._upstream_request(method, path, query, management_key, body)
        if result:
            status, raw, content_type = result
            self._send(status, raw, content_type)
        return result

    def _proxy_write(self, parsed):
        if self.server.mode != "remote-read-write":
            self._error(HTTPStatus.METHOD_NOT_ALLOWED, "本地预览禁止写操作；请求未发送到测试环境", "read_only_preview")
            return
        if not self._allowed_remote_write(self.command, parsed.path):
            self._error(HTTPStatus.NOT_FOUND, "远程读写代理未允许该写接口", "route_not_allowed")
            return
        session = self._session()
        if not session:
            self._error(HTTPStatus.UNAUTHORIZED, "请先输入测试环境管理密钥", "unauthorized")
            return
        provided_csrf = str(self.headers.get("X-CSRF-Token", ""))
        if not provided_csrf or not secrets.compare_digest(provided_csrf, session["csrf_token"]):
            self._error(HTTPStatus.FORBIDDEN, "本地写入会话校验失败", "csrf_required")
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            self._error(HTTPStatus.BAD_REQUEST, "Content-Length 无效", "invalid_request")
            return
        if length < 0 or length > MAX_WRITE_BODY_BYTES:
            self._error(HTTPStatus.REQUEST_ENTITY_TOO_LARGE, "写请求体过大", "request_too_large")
            return
        body = self.rfile.read(length) if length else b""
        self._proxy(self.command, parsed.path, parsed.query, session["management_key"], body)

    def _create_remote_session(self, management_key):
        result = self._upstream_get("/admin/api/session", "", management_key)
        if not result or result[0] != HTTPStatus.OK:
            if result:
                status, raw, content_type = result
                self._send(status, raw, content_type)
            return
        try:
            upstream_payload = json.loads(result[1].decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError):
            self._error(HTTPStatus.BAD_GATEWAY, "测试环境会话响应无效", "invalid_upstream_response")
            return
        token, csrf_token = self.server.sessions.create(management_key)
        payload = {
            "authenticated": True,
            "accounts": upstream_payload.get("accounts", {}),
            "csrf_token": csrf_token,
        }
        self._json(
            HTTPStatus.CREATED,
            payload,
            headers=(("Set-Cookie", self._session_cookie(token, SESSION_TTL_SECONDS)),),
        )

    def do_HEAD(self):
        self.do_GET()

    def do_GET(self):
        parsed = urllib.parse.urlsplit(self.path)
        path = parsed.path
        if path == "/":
            self.send_response(HTTPStatus.TEMPORARY_REDIRECT)
            self.send_header("Location", "/admin/")
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        if path == "/admin":
            self.send_response(HTTPStatus.TEMPORARY_REDIRECT)
            self.send_header("Location", "/admin/")
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        if self._serve_local(path):
            return
        if path == "/healthz":
            self._json(HTTPStatus.OK, {"status": "ok", "mode": self.server.mode, "write_enabled": self.server.mode == "remote-read-write"})
            return
        if self.server.mode == "mock":
            if path == "/admin/reasoning-effort-colors.css":
                raw = b":root{--account-model-effort-none:#7d8490;--account-model-effort-low:#4b8ccf;--account-model-effort-medium:#7653a6;--account-model-effort-high:#2f73d9;--account-model-effort-xhigh:#5965c7;--account-model-effort-unknown:#687287}"
                self._send(HTTPStatus.OK, raw, "text/css; charset=utf-8")
                return
            if path == "/branding/logo":
                self._error(HTTPStatus.NOT_FOUND, "未配置自定义 Logo", "not_found")
                return
            if path.startswith("/admin/api/") and self._cookie_token() != "mock":
                self._error(HTTPStatus.UNAUTHORIZED, "请输入任意非空本地预览密钥", "unauthorized")
                return
            if path.startswith("/admin/api/") or path == "/site-config.json":
                query = urllib.parse.parse_qs(parsed.query, keep_blank_values=True)
                status, payload = self.server.mock.response(path, query)
                self._json(status, payload)
                return
            self._error(HTTPStatus.NOT_FOUND, "页面不存在", "not_found")
            return
        if not self._allowed_remote_get(path):
            self._error(HTTPStatus.NOT_FOUND, "只读代理未允许该 GET 路由", "route_not_allowed")
            return
        session = self._session()
        if path.startswith("/admin/api/") and not session:
            self._error(HTTPStatus.UNAUTHORIZED, "请先输入测试环境管理密钥", "unauthorized")
            return
        self._proxy("GET", path, parsed.query, session["management_key"] if session else None)

    def do_POST(self):
        parsed = urllib.parse.urlsplit(self.path)
        if self.server.mode in ("remote-read-only", "remote-read-write") and parsed.path == "/admin/api/session":
            management_key = str(self.headers.get("X-Management-Key", "")).strip()
            if not management_key:
                self._error(HTTPStatus.UNAUTHORIZED, "管理密钥不能为空", "unauthorized")
                return
            self._create_remote_session(management_key)
            return
        if self.server.mode == "mock" and parsed.path == "/admin/api/session":
            if not str(self.headers.get("X-Management-Key", "")).strip():
                self._error(HTTPStatus.UNAUTHORIZED, "请输入任意非空本地预览密钥", "unauthorized")
                return
            self._json(HTTPStatus.CREATED, {"authenticated": True, "accounts": {account["id"]: {"email": account["email"]} for account in self.server.mock.accounts}, "csrf_token": "local-mock-csrf"}, headers=(("Set-Cookie", self._session_cookie("mock", SESSION_TTL_SECONDS)),))
            return
        self._proxy_write(parsed)

    def do_DELETE(self):
        parsed = urllib.parse.urlsplit(self.path)
        if parsed.path == "/admin/api/session":
            self.server.sessions.revoke(self._cookie_token())
            self._json(HTTPStatus.OK, {"logged_out": True}, headers=(("Set-Cookie", self._session_cookie("", 0)),))
            return
        self._proxy_write(parsed)

    def do_PUT(self):
        self._proxy_write(urllib.parse.urlsplit(self.path))

    def do_PATCH(self):
        self._proxy_write(urllib.parse.urlsplit(self.path))


def validated_upstream(value):
    parsed = urllib.parse.urlsplit(str(value or ""))
    if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise argparse.ArgumentTypeError("--upstream 必须是无凭据、无查询参数的 HTTPS Origin")
    if parsed.path not in ("", "/"):
        raise argparse.ArgumentTypeError("--upstream 不能包含路径")
    return "{}://{}".format(parsed.scheme, parsed.netloc)


def loopback_host(value):
    try:
        address = ipaddress.ip_address(str(value or ""))
    except ValueError as error:
        raise argparse.ArgumentTypeError("--host 必须是回环 IP 地址") from error
    if not address.is_loopback:
        raise argparse.ArgumentTypeError("--host 仅允许回环 IP，避免管理密钥暴露到局域网")
    return str(address)


def build_parser():
    parser = argparse.ArgumentParser(description="本地 Admin 前端预览服务")
    parser.add_argument("--mode", choices=("mock", "remote-read-only", "remote-read-write"), default="mock")
    parser.add_argument("--host", type=loopback_host, default="127.0.0.1", help="仅允许本机回环 IP")
    parser.add_argument("--port", type=int, help="mock 默认 8876，remote-read-only 默认 8877，remote-read-write 默认 8878")
    parser.add_argument("--upstream", type=validated_upstream, default=DEFAULT_UPSTREAM)
    parser.add_argument(
        "--confirm-write-upstream",
        type=validated_upstream,
        help="remote-read-write 必填，且必须与 --upstream 完全相同",
    )
    parser.add_argument("--timeout", type=float, default=15.0)
    parser.add_argument("--insecure-upstream", action="store_true", help="仅临时诊断使用：关闭上游 TLS 验证")
    return parser


def main(argv=None):
    args = build_parser().parse_args(argv)
    default_ports = {"mock": 8876, "remote-read-only": 8877, "remote-read-write": 8878}
    if args.mode != "mock" and args.upstream == DEFAULT_UPSTREAM:
        raise SystemExit("远程预览模式必须显式提供 --upstream")
    if args.mode == "remote-read-write" and args.confirm_write_upstream != args.upstream:
        raise SystemExit("remote-read-write 必须用 --confirm-write-upstream 再次确认同一测试 Origin")
    if not 1 <= (args.port or default_ports[args.mode]) <= 65535:
        raise SystemExit("端口必须在 1 到 65535 之间")
    port = args.port or default_ports[args.mode]
    ssl_context = ssl.create_default_context()
    if args.insecure_upstream:
        ssl_context.check_hostname = False
        ssl_context.verify_mode = ssl.CERT_NONE
    server = PreviewServer(
        (args.host, port),
        mode=args.mode,
        root=REPOSITORY_ROOT,
        upstream=args.upstream,
        timeout=args.timeout,
        ssl_context=ssl_context,
    )
    print("Admin preview: http://{}:{}/admin/ [{}]".format(args.host, server.server_port, args.mode))
    if args.mode == "remote-read-only":
        print("Read-only upstream: {} (all business writes are blocked locally)".format(args.upstream))
    elif args.mode == "remote-read-write":
        print("READ-WRITE TEST upstream: {} (confirmed UI actions mutate Test)".format(args.upstream))
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
