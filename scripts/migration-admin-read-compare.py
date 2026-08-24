#!/usr/bin/env python3
"""Compare authenticated Admin reads and reject every write without CSRF.

The management key is accepted only from stdin, never an argument or
environment variable. Reports retain status, body size and schema field names
but never response values, cookies, CSRF tokens, account ids or user emails.
"""

import argparse
import http.client
import json
import sqlite3
import ssl
import sys
import urllib.parse
from dataclasses import dataclass
from http.cookies import SimpleCookie
from pathlib import Path


MAX_RESPONSE_BYTES = 16 * 1024 * 1024

# These are deliberate product/API-boundary differences, not broad endpoint
# exemptions.  The comparator calculates the exact schema delta and accepts it
# only when every added/removed field still matches this table.  A new field,
# a missing compatibility field, or a changed nested list item therefore fails
# the comparison instead of disappearing behind an endpoint-name allowlist.
SCHEMA_DECISIONS = {
    "session": {
        "reason": "账号目录从会话响应拆出，登录后按需请求 /admin/api/accounts。",
        "delta": {
            "top_keys": {"v1_only": ["accounts"], "v2_only": []},
            "object_fields": {"v1_only": ["accounts"], "v2_only": []},
        },
    },
    "accounts": {
        "reason": "账号目录只保留列表所需字段；额度、用量和运行状态由细粒度接口按需加载。",
        "delta": {
            "top_keys": {
                "v1_only": [
                    "collector",
                    "quota_cache_ttl_seconds",
                    "quota_cached",
                    "quota_generated_at",
                    "quota_refreshing",
                    "window",
                    "window_end_at",
                    "window_seconds",
                    "window_start_at",
                    "window_start_at_by_account",
                ],
                "v2_only": ["warnings"],
            },
            "object_fields": {"v1_only": ["collector"], "v2_only": []},
            "array_fields": {"v1_only": [], "v2_only": ["warnings"]},
            "array_item_keys": {
                "accounts": {
                    "v1_only": [
                        "active_user_emails_1h",
                        "associated_users",
                        "auth_files",
                        "auth_state",
                        "container_health",
                        "container_state",
                        "container_status",
                        "created_at",
                        "default_group",
                        "group_enabled",
                        "group_name",
                        "operational_status",
                        "proxy_display",
                        "proxy_source",
                        "quota",
                        "runtime",
                        "service",
                        "usage",
                        "usage_window_available",
                        "usage_window_start_at",
                    ],
                    "v2_only": ["account_state", "default", "enabled", "state_available"],
                }
            },
        },
    },
    "logs": {
        "reason": "保留 v1 的 exit_code；Go 有界日志读取额外公开明确的 truncated 状态。",
        "delta": {
            "top_keys": {"v1_only": [], "v2_only": ["truncated"]},
        },
    },
    "overview_usage": {
        "reason": "Go v2 不使用响应缓存，显式返回 cached=false。",
        "delta": {
            "top_keys": {"v1_only": [], "v2_only": ["cached"]},
        },
    },
    "teams": {
        "reason": "产品确认移除标签管理，团队目录不再返回 tags。",
        "delta": {
            "top_keys": {"v1_only": ["tags"], "v2_only": []},
            "array_fields": {"v1_only": ["tags"], "v2_only": []},
        },
    },
    "users": {
        "reason": "用户目录分页；账号、额度和用量详情在抽屉打开时按需请求，标签字段已移除。",
        "delta": {
            "top_keys": {
                "v1_only": [
                    "accounts",
                    "collector",
                    "window",
                    "window_end_at",
                    "window_seconds",
                    "window_start_at",
                ],
                "v2_only": ["pagination"],
            },
            "object_fields": {
                "v1_only": ["accounts", "collector"],
                "v2_only": ["pagination"],
            },
            "array_item_keys": {
                "users": {
                    "v1_only": ["accounts", "tags", "usage", "weekly_quota"],
                    "v2_only": ["active_accounts", "route_account_id"],
                }
            },
        },
    },
}

V1_CSRF_OPERATIONS = (
    "DELETE /admin/api/session",
    "DELETE /admin/api/settings/logo",
    "DELETE /admin/api/tags",
    "DELETE /admin/api/teams",
    "DELETE /admin/api/users/quota",
    "POST /admin/api/accounts",
    "POST /admin/api/accounts/clear-auth",
    "POST /admin/api/accounts/delete",
    "POST /admin/api/accounts/policy",
    "POST /admin/api/accounts/rebalance",
    "POST /admin/api/accounts/reset-quota",
    "POST /admin/api/accounts/update",
    "POST /admin/api/jobs/cancel",
    "POST /admin/api/keys/create",
    "POST /admin/api/keys/revoke",
    "POST /admin/api/keys/rotate",
    "POST /admin/api/operations",
    "POST /admin/api/settings/configuration",
    "POST /admin/api/settings/initial-password",
    "POST /admin/api/settings/logo",
    "POST /admin/api/settings/management-key",
    "POST /admin/api/settings/notification-webhook",
    "POST /admin/api/settings/notification-webhook/clear",
    "POST /admin/api/tags",
    "POST /admin/api/teams",
    "POST /admin/api/users",
    "POST /admin/api/users/delete",
    "POST /admin/api/users/quota-actions",
    "POST /admin/api/users/reset-password",
    "POST /admin/api/users/revoke",
    "POST /admin/api/users/tags/batch",
    "POST /admin/api/users/team/batch",
    "PUT /admin/api/tags",
    "PUT /admin/api/teams",
    "PUT /admin/api/users/quota",
    "PUT /admin/api/users/tags",
    "PUT /admin/api/users/team",
)

V2_CSRF_OPERATIONS = (
    "DELETE /admin/api/session",
    "POST /admin/api/accounts",
    "POST /admin/api/accounts/update",
    "POST /admin/api/accounts/clear-auth",
    "POST /admin/api/accounts/delete",
    "POST /admin/api/accounts/rebalance-all",
    "POST /admin/api/runtime/jobs",
    "POST /admin/api/runtime/jobs/probe/cancel",
    "POST /admin/api/users",
    "PUT /admin/api/users/quota",
    "DELETE /admin/api/users/quota",
    "POST /admin/api/keys/rotate",
    "POST /admin/api/users/revoke",
    "POST /admin/api/users/reset-password",
    "POST /admin/api/users/delete",
    "PUT /admin/api/users/team",
    "POST /admin/api/users/team/batch",
    "POST /admin/api/teams",
    "PUT /admin/api/teams",
    "DELETE /admin/api/teams",
    "PUT /admin/api/settings/general",
    "POST /admin/api/settings/initial-password",
    "PUT /admin/api/settings/notifications",
    "POST /admin/api/settings/notification-webhook",
    "POST /admin/api/settings/notification-webhook/clear",
    "POST /admin/api/notifications/send",
    "POST /admin/api/accounts/rebalance",
    "POST /admin/api/accounts/reset-quota",
    "POST /admin/api/users/quota-actions",
    "POST /admin/api/settings/logo",
    "DELETE /admin/api/settings/logo",
    "POST /admin/api/settings/configuration",
    "POST /admin/api/settings/management-key",
    "POST /admin/api/notifications/test",
    "POST /admin/api/jobs/cancel",
    "POST /admin/api/operations",
)


@dataclass(frozen=True)
class Target:
    name: str
    surface: str
    base_url: str


def parse_target(value):
    parts = value.split(",", 2)
    if len(parts) != 3:
        raise argparse.ArgumentTypeError("target must be NAME,SURFACE,BASE_URL")
    name, surface, base_url = (part.strip() for part in parts)
    parsed = urllib.parse.urlsplit(base_url)
    if not name or surface not in {"v1", "v2"}:
        raise argparse.ArgumentTypeError("target surface must be v1 or v2")
    if parsed.scheme not in {"http", "https"} or not parsed.netloc or parsed.query or parsed.fragment:
        raise argparse.ArgumentTypeError("target BASE_URL must be an absolute HTTP(S) URL")
    return Target(name, surface, base_url.rstrip("/"))


def read_comparison_identifiers(database_path):
    database = sqlite3.connect("file:{}?mode=ro".format(database_path), uri=True)
    try:
        account = database.execute(
            "SELECT id FROM accounts ORDER BY position, id LIMIT 1"
        ).fetchone()
        user = database.execute(
            "SELECT user_email FROM key_records WHERE status = 'active' ORDER BY sequence LIMIT 1"
        ).fetchone()
        team = database.execute("SELECT id FROM teams ORDER BY name, id LIMIT 1").fetchone()
    finally:
        database.close()
    if not account or not user:
        raise ValueError("comparison database lacks an account or active user")
    return account[0], user[0], team[0] if team else None


def read_endpoints(account, user, team):
    encode = urllib.parse.urlencode
    rows = [
        ("session", "/admin/api/session"),
        ("accounts", "/admin/api/accounts"),
        ("account_usage", "/admin/api/accounts/usage-breakdown?" + encode({"account": account, "window": "3600"})),
        ("image_status", "/admin/api/images/cliproxy"),
        ("jobs", "/admin/api/jobs?limit=30"),
        ("logs", "/admin/api/logs?target=all"),
        ("native_accounts", "/admin/api/native-accounts"),
        ("overview_usage", "/admin/api/overview/usage?window=3600"),
        ("release", "/admin/api/release?fresh=0"),
        ("teams", "/admin/api/teams"),
        ("team_usage", "/admin/api/teams/usage?window=3600"),
        ("users", "/admin/api/users?page=1&page_size=25"),
        ("user_quota", "/admin/api/users/quota?" + encode({"email": user})),
        ("user_usage", "/admin/api/users/usage-breakdown?" + encode({"email": user, "window": "3600"})),
        ("operation_impact", "/admin/api/operations/impact?" + encode({"action": "stop", "target": account})),
    ]
    if team:
        rows.append(
            ("team_breakdown", "/admin/api/teams/usage-breakdown?" + encode({"team_id": team, "window": "3600"}))
        )
    return rows


def schema_summary(value):
    """Describe stable field names without walking dynamic object keys."""

    if not isinstance(value, dict):
        return {"kind": "array" if isinstance(value, list) else type(value).__name__}
    arrays = {}
    objects = []
    for name, nested in value.items():
        if isinstance(nested, list):
            first = nested[0] if nested else None
            arrays[name] = sorted(first) if isinstance(first, dict) else []
        elif isinstance(nested, dict):
            objects.append(name)
    return {
        "kind": "object",
        "top_keys": sorted(value),
        "array_item_keys": {name: arrays[name] for name in sorted(arrays)},
        "object_fields": sorted(objects),
    }


def schema_delta(v1_schema, v2_schema):
    """Return the exact, value-free structural delta between two summaries."""

    result = {}
    if v1_schema.get("kind") != v2_schema.get("kind"):
        result["kind"] = {"v1": v1_schema.get("kind"), "v2": v2_schema.get("kind")}

    def add_set_delta(name, v1_values, v2_values):
        v1_values, v2_values = set(v1_values), set(v2_values)
        if v1_values != v2_values:
            result[name] = {
                "v1_only": sorted(v1_values - v2_values),
                "v2_only": sorted(v2_values - v1_values),
            }

    add_set_delta("top_keys", v1_schema.get("top_keys", []), v2_schema.get("top_keys", []))
    add_set_delta(
        "object_fields", v1_schema.get("object_fields", []), v2_schema.get("object_fields", [])
    )
    v1_arrays = v1_schema.get("array_item_keys", {})
    v2_arrays = v2_schema.get("array_item_keys", {})
    add_set_delta("array_fields", v1_arrays, v2_arrays)
    item_deltas = {}
    for name in sorted(set(v1_arrays) & set(v2_arrays)):
        v1_items, v2_items = set(v1_arrays[name]), set(v2_arrays[name])
        if v1_items != v2_items:
            item_deltas[name] = {
                "v1_only": sorted(v1_items - v2_items),
                "v2_only": sorted(v2_items - v1_items),
            }
    if item_deltas:
        result["array_item_keys"] = item_deltas
    return result


class Client:
    def __init__(self, target, timeout):
        parsed = urllib.parse.urlsplit(target.base_url)
        self.target = target
        self.scheme = parsed.scheme
        self.host = parsed.hostname
        self.port = parsed.port or (443 if parsed.scheme == "https" else 80)
        self.host_header = parsed.netloc
        self.prefix = parsed.path.rstrip("/")
        self.timeout = timeout
        self.cookie = ""
        self.csrf = ""

    def request(self, method, path, headers=None, body=None):
        request_headers = {"Host": self.host_header, "Accept": "application/json"}
        if headers:
            request_headers.update(headers)
        raw = None
        if body is not None:
            raw = json.dumps(body, separators=(",", ":")).encode()
            request_headers["Content-Type"] = "application/json"
        if self.scheme == "https":
            connection = http.client.HTTPSConnection(
                self.host, self.port, timeout=self.timeout, context=ssl.create_default_context()
            )
        else:
            connection = http.client.HTTPConnection(self.host, self.port, timeout=self.timeout)
        connection.request(method, self.prefix + path, body=raw, headers=request_headers)
        response = connection.getresponse()
        payload = response.read(MAX_RESPONSE_BYTES + 1)
        if len(payload) > MAX_RESPONSE_BYTES:
            connection.close()
            raise ValueError("response exceeded 16 MiB")
        result = {
            "status": response.status,
            "content_type": (response.getheader("Content-Type") or "").split(";", 1)[0],
            "bytes": len(payload),
            "set_cookie": response.getheader("Set-Cookie") or "",
            "body": payload,
        }
        connection.close()
        return result

    def login(self, management_key):
        response = self.request(
            "POST", "/admin/api/session", headers={"X-Management-Key": management_key}
        )
        if response["status"] != 201 or not response["set_cookie"]:
            raise ValueError("Admin login failed with status {}".format(response["status"]))
        cookies = SimpleCookie()
        cookies.load(response["set_cookie"])
        morsel = next(iter(cookies.values()))
        payload = json.loads(response["body"])
        csrf = payload.get("csrf_token")
        if not isinstance(csrf, str) or not csrf:
            raise ValueError("Admin session lacks CSRF token")
        self.cookie = morsel.key + "=" + morsel.value
        self.csrf = csrf
        return response["status"]

    def authenticated_read(self, path):
        response = self.request("GET", path, headers={"Cookie": self.cookie})
        try:
            payload = json.loads(response["body"])
            schema = schema_summary(payload)
        except (UnicodeDecodeError, json.JSONDecodeError):
            schema = {"kind": "invalid-json"}
        result = {key: response[key] for key in ("status", "content_type", "bytes")}
        result["schema"] = schema
        return result

    def csrf_probe(self, operation):
        method, path = operation.split(" ", 1)
        body = {} if method in {"POST", "PUT", "PATCH"} else None
        return self.request(method, path, headers={"Cookie": self.cookie}, body=body)["status"]

    def logout(self):
        return self.request(
            "DELETE",
            "/admin/api/session",
            headers={"Cookie": self.cookie, "X-CSRF-Token": self.csrf},
        )["status"]


def run(targets, management_key, database_path, timeout):
    account, user, team = read_comparison_identifiers(database_path)
    endpoints = read_endpoints(account, user, team)
    report = {
        "version": 2,
        "targets": {},
        "reads": [],
        "failures": [],
        "schema_differences": [],
        "approved_schema_differences": [],
        "unexpected_schema_differences": [],
    }
    clients = {}
    for target in targets:
        client = Client(target, timeout)
        clients[target.name] = client
        login_status = client.login(management_key)
        csrf_operations = V1_CSRF_OPERATIONS if target.surface == "v1" else V2_CSRF_OPERATIONS
        csrf_results = [
            {"operation": operation, "status": client.csrf_probe(operation)}
            for operation in csrf_operations
        ]
        for row in csrf_results:
            if row["status"] != 403:
                report["failures"].append({"target": target.name, "kind": "csrf", **row})
        report["targets"][target.name] = {
            "surface": target.surface,
            "base_url": target.base_url,
            "login_status": login_status,
            "csrf_passed": sum(row["status"] == 403 for row in csrf_results),
            "csrf_total": len(csrf_results),
        }

    for name, path in endpoints:
        row = {"name": name, "targets": {}}
        for target in targets:
            result = clients[target.name].authenticated_read(path)
            row["targets"][target.name] = result
            if result["status"] != 200 or result["schema"].get("kind") != "object":
                report["failures"].append(
                    {"target": target.name, "kind": "read", "name": name, **result}
                )
        schemas = [row["targets"][target.name]["schema"] for target in targets]
        row["schema_equal"] = all(schema == schemas[0] for schema in schemas[1:])
        row["schema_compatible"] = row["schema_equal"]
        if not row["schema_equal"]:
            report["schema_differences"].append(name)
            v1_targets = [target for target in targets if target.surface == "v1"]
            v2_targets = [target for target in targets if target.surface == "v2"]
            decision = SCHEMA_DECISIONS.get(name)
            if len(v1_targets) == 1 and len(v2_targets) == 1:
                delta = schema_delta(
                    row["targets"][v1_targets[0].name]["schema"],
                    row["targets"][v2_targets[0].name]["schema"],
                )
                row["schema_delta"] = delta
                if decision and delta == decision["delta"]:
                    row["schema_compatible"] = True
                    row["schema_decision"] = decision["reason"]
                    report["approved_schema_differences"].append(name)
            if not row["schema_compatible"]:
                report["unexpected_schema_differences"].append(name)
                report["failures"].append(
                    {
                        "kind": "schema_difference",
                        "name": name,
                        "delta": row.get("schema_delta", {"comparison": "requires one v1 and one v2 target"}),
                    }
                )
        report["reads"].append(row)

    for target in targets:
        logout_status = clients[target.name].logout()
        report["targets"][target.name]["logout_status"] = logout_status
        if logout_status != 200:
            report["failures"].append(
                {"target": target.name, "kind": "logout", "status": logout_status}
            )
    return report


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", action="append", required=True, type=parse_target)
    parser.add_argument("--control-db", required=True, type=Path)
    parser.add_argument("--management-key-stdin", action="store_true", required=True)
    parser.add_argument("--timeout", type=float, default=10.0)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--summary", action="store_true")
    args = parser.parse_args(argv)
    management_key = sys.stdin.readline().strip()
    if not management_key:
        raise SystemExit("management key was not provided on stdin")
    report = run(args.target, management_key, args.control_db, args.timeout)
    full = json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.write_text(full, encoding="utf-8")
    if args.summary:
        summary = {
            "version": report["version"],
            "targets": report["targets"],
            "read_total": len(report["reads"]),
            "read_passed": len(report["reads"])
            - sum(failure["kind"] == "read" for failure in report["failures"]),
            "schema_differences": report["schema_differences"],
            "approved_schema_differences": report["approved_schema_differences"],
            "unexpected_schema_differences": report["unexpected_schema_differences"],
            "failures": report["failures"],
        }
        print(json.dumps(summary, ensure_ascii=False, indent=2, sort_keys=True))
    else:
        print(full, end="")
    return 1 if report["failures"] else 0


if __name__ == "__main__":
    sys.exit(main())
