#!/usr/bin/env python3
"""Compare the reversible Portal workflow on isolated v1 and Go v2 copies.

The management key and dedicated Test API Key are read as two stdin lines.  The
report never contains either credential, the generated password, cookies,
CSRF tokens, user email, account identifiers, or response values.  Every write
uses the supported HTTP API and the original route and reset-password state are
restored before the run exits.
"""

import argparse
import copy
import hashlib
import http.client
import ipaddress
import json
import os
import sqlite3
import sys
import urllib.parse
from dataclasses import dataclass
from http.cookies import SimpleCookie
from pathlib import Path


MAX_RESPONSE_BYTES = 16 * 1024 * 1024
PRODUCTION_ROOTS = {
    Path("/home/AI/CLIProxyAPI"),
    Path("/opt/codex-cpa-cluster"),
}


class ComparisonFailure(Exception):
    def __init__(self, step, reason):
        super().__init__(reason)
        self.step = step
        self.reason = reason


@dataclass(frozen=True)
class Target:
    name: str
    surface: str
    base_url: str
    control_db: Path


def parse_target(value):
    parts = value.split(",", 3)
    if len(parts) != 4:
        raise argparse.ArgumentTypeError(
            "target must be NAME,SURFACE,BASE_URL,CONTROL_DB"
        )
    name, surface, base_url, control_db = (part.strip() for part in parts)
    parsed = urllib.parse.urlsplit(base_url)
    if not name or surface not in {"v1", "v2"}:
        raise argparse.ArgumentTypeError("target surface must be v1 or v2")
    if parsed.scheme != "http" or not parsed.netloc or parsed.query or parsed.fragment:
        raise argparse.ArgumentTypeError("comparison target must be an absolute HTTP URL")
    host = parsed.hostname
    if host != "localhost":
        try:
            address = ipaddress.ip_address(host or "")
        except ValueError as error:
            raise argparse.ArgumentTypeError(
                "comparison target must use localhost or a private IP address"
            ) from error
        if not (address.is_loopback or address.is_private):
            raise argparse.ArgumentTypeError(
                "comparison target must use localhost or a private IP address"
            )
    return Target(name, surface, base_url.rstrip("/"), Path(control_db))


def deployment_root(target):
    database = target.control_db.resolve()
    if database.name != "control-plane.sqlite3" or database.parent.name != "state":
        raise ValueError(
            "{} control database must be ROOT/state/control-plane.sqlite3".format(
                target.name
            )
        )
    return database.parent.parent


def validate_target(target):
    root = deployment_root(target)
    if root in PRODUCTION_ROOTS or root == Path("/"):
        raise ValueError("{} points at a live or broad deployment root".format(target.name))
    required = (
        root / ".v2-isolated-copy.json",
        target.control_db,
        root / "state" / "usage.sqlite3",
        root / "secrets" / "control-plane.key",
    )
    if not all(path.is_file() for path in required):
        raise ValueError("{} isolated-copy prerequisites are incomplete".format(target.name))
    return root


def normalize_user(value):
    return str(value or "").strip().lower()


def read_probe_identity(database_path, test_key):
    database = sqlite3.connect(
        "file:{}?mode=ro".format(database_path.resolve()), uri=True
    )
    try:
        rows = database.execute(
            "SELECT lower(trim(user_email)), account_id "
            "FROM key_records WHERE status = 'active' AND secret = ? "
            "ORDER BY sequence",
            (test_key,),
        ).fetchall()
    finally:
        database.close()
    users = {normalize_user(row[0]) for row in rows if normalize_user(row[0])}
    accounts = {str(row[1]).strip() for row in rows if str(row[1]).strip()}
    if len(users) != 1 or not accounts:
        raise ValueError("dedicated Test Key does not resolve to one active isolated user")
    return next(iter(users)), test_key, accounts


def read_user_probe_identity(database_path, raw_user):
    user = normalize_user(raw_user)
    database = sqlite3.connect(
        "file:{}?mode=ro".format(database_path.resolve()), uri=True
    )
    try:
        rows = database.execute(
            "SELECT account_id, secret FROM key_records "
            "WHERE status = 'active' AND lower(trim(user_email)) = ? "
            "ORDER BY sequence",
            (user,),
        ).fetchall()
    finally:
        database.close()
    keys = {str(row[1]) for row in rows if str(row[1])}
    accounts = {str(row[0]).strip() for row in rows if str(row[0]).strip()}
    if not user or len(keys) != 1 or not accounts:
        raise ValueError("isolated fallback user does not have one active unified Key")
    return user, next(iter(keys)), accounts


def read_fallback_candidates(database_path, allowed_accounts, excluded_user):
    database = sqlite3.connect(
        "file:{}?mode=ro".format(database_path.resolve()), uri=True
    )
    try:
        rows = database.execute(
            "SELECT lower(trim(r.user_email)), r.account_id, k.account_id, k.secret "
            "FROM user_routes AS r JOIN key_records AS k "
            "ON lower(trim(k.user_email)) = lower(trim(r.user_email)) "
            "WHERE k.status = 'active' ORDER BY lower(trim(r.user_email)), k.sequence"
        ).fetchall()
    finally:
        database.close()
    grouped = {}
    for raw_user, route, account, key in rows:
        user = normalize_user(raw_user)
        route = str(route or "").strip()
        if not user or user == excluded_user or route not in allowed_accounts:
            continue
        item = grouped.setdefault(user, {"route": route, "keys": set(), "accounts": set()})
        if item["route"] != route:
            item["route"] = ""
        item["keys"].add(str(key))
        item["accounts"].add(str(account).strip())
    return {
        user: {
            "route": item["route"],
            "key": next(iter(item["keys"])),
            "accounts": item["accounts"],
        }
        for user, item in grouped.items()
        if item["route"] and len(item["keys"]) == 1 and item["accounts"]
    }


def select_fallback_probe(targets, allowed_accounts, excluded_user):
    catalogs = [
        read_fallback_candidates(target.control_db, allowed_accounts, excluded_user)
        for target in targets
    ]
    common_users = set(catalogs[0]) & set(catalogs[1])
    compatible = []
    for user in common_users:
        left, right = catalogs[0][user], catalogs[1][user]
        if (
            left["route"] == right["route"]
            and left["key"] == right["key"]
            and left["accounts"] == right["accounts"]
        ):
            compatible.append(user)
    if not compatible:
        return None
    return min(compatible, key=digest)


def digest(value):
    return hashlib.sha256(str(value).encode("utf-8")).hexdigest()


def digest_set(values):
    canonical = json.dumps(sorted(values), separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(canonical).hexdigest()


def error_code(payload):
    if not isinstance(payload, dict):
        return ""
    error = payload.get("error")
    return str(error.get("code") or "") if isinstance(error, dict) else ""


def schema_summary(value):
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


def decode_json(response, step):
    try:
        return json.loads(response["body"])
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise ComparisonFailure(step, "invalid_json") from error


def observe(response, payload):
    return {
        "status": response["status"],
        "content_type": response["content_type"],
        "error_code": error_code(payload),
        "schema": schema_summary(payload),
    }


class HTTPClient:
    def __init__(self, target, timeout):
        parsed = urllib.parse.urlsplit(target.base_url)
        self.host = parsed.hostname
        self.port = parsed.port or 80
        self.host_header = parsed.netloc
        self.prefix = parsed.path.rstrip("/")
        self.timeout = timeout

    def request(self, method, path, headers=None, body=None):
        request_headers = {"Host": self.host_header, "Accept": "application/json"}
        if headers:
            request_headers.update(headers)
        raw = None
        if body is not None:
            raw = json.dumps(body, separators=(",", ":")).encode("utf-8")
            request_headers["Content-Type"] = "application/json"
        connection = http.client.HTTPConnection(self.host, self.port, timeout=self.timeout)
        connection.request(method, self.prefix + path, body=raw, headers=request_headers)
        response = connection.getresponse()
        payload = response.read(MAX_RESPONSE_BYTES + 1)
        result = {
            "status": response.status,
            "content_type": (response.getheader("Content-Type") or "").split(";", 1)[0],
            "set_cookie": response.getheader("Set-Cookie") or "",
            "body": payload,
        }
        connection.close()
        if len(payload) > MAX_RESPONSE_BYTES:
            raise ComparisonFailure("response", "response_too_large")
        return result


def cookie_value(header, expected_name, step):
    cookies = SimpleCookie()
    try:
        cookies.load(header)
    except Exception as error:
        raise ComparisonFailure(step, "invalid_cookie") from error
    morsel = cookies.get(expected_name)
    if morsel is None or not morsel.value:
        raise ComparisonFailure(step, "missing_cookie")
    return expected_name + "=" + morsel.value


class TargetRun:
    def __init__(
        self,
        target,
        management_key,
        test_key,
        timeout,
        probe_user=None,
        probe_kind="dedicated_test_key_user",
    ):
        self.target = target
        self.management_key = management_key
        self.client = HTTPClient(target, timeout)
        if probe_user is None:
            self.user, self.expected_key, self.key_accounts = read_probe_identity(
                target.control_db, test_key
            )
        else:
            self.user, self.expected_key, self.key_accounts = read_user_probe_identity(
                target.control_db, probe_user
            )
        self.probe_kind = probe_kind
        self.admin_cookie = ""
        self.admin_csrf = ""
        self.portal_cookie = ""
        self.initial_password = ""
        self.generated_password = "Migration-{}-9a".format(
            hashlib.sha256(os.urandom(32)).hexdigest()[:20]
        )
        self.original_route = ""
        self.catalog = set()
        self.selectable = set()
        self.operational = {}
        self.route_changed = False
        self.prepared = False
        self.cleaned = False
        self.steps = {}
        self.failures = []

    def record(self, step, response):
        payload = decode_json(response, step)
        self.steps[step] = observe(response, payload)
        return payload

    def require_status(self, step, response, expected):
        payload = self.record(step, response)
        if response["status"] != expected:
            raise ComparisonFailure(step, "unexpected_status")
        return payload

    def admin_request(self, method, path, body=None):
        headers = {}
        if self.admin_cookie:
            headers["Cookie"] = self.admin_cookie
            headers["X-CSRF-Token"] = self.admin_csrf
        else:
            headers["X-Management-Key"] = self.management_key
        return self.client.request(method, path, headers=headers, body=body)

    def portal_request(self, method, path, body=None):
        headers = {"Cookie": self.portal_cookie} if self.portal_cookie else {}
        return self.client.request(method, path, headers=headers, body=body)

    def admin_login(self):
        step = "admin_login"
        response = self.client.request(
            "POST",
            "/admin/api/session",
            headers={"X-Management-Key": self.management_key},
        )
        payload = self.require_status(step, response, 201)
        csrf = payload.get("csrf_token") if isinstance(payload, dict) else None
        if not isinstance(csrf, str) or not csrf:
            raise ComparisonFailure(step, "missing_csrf")
        self.admin_cookie = cookie_value(response["set_cookie"], "cpa_admin_session", step)
        self.admin_csrf = csrf

    def reset_password(self, step):
        response = self.admin_request(
            "POST",
            "/admin/api/users/reset-password",
            {"email": self.user, "confirm": "reset"},
        )
        payload = self.require_status(step, response, 200)
        if self.target.surface == "v1":
            password = payload.get("initial_password") if isinstance(payload, dict) else None
            changed = payload.get("password_change_required") if isinstance(payload, dict) else None
        else:
            nested = payload.get("password") if isinstance(payload, dict) else None
            password = nested.get("initial_password") if isinstance(nested, dict) else None
            changed = nested.get("password_change_required") if isinstance(nested, dict) else None
        if not isinstance(password, str) or not password or len(password) > 128 or changed is not True:
            raise ComparisonFailure(step, "invalid_one_time_password_response")
        self.initial_password = password

    def portal_login(self):
        step = "portal_login"
        response = self.client.request(
            "POST",
            "/usage/session",
            body={"email": self.user, "password": self.initial_password},
        )
        payload = self.require_status(step, response, 201)
        if normalize_user(payload.get("user") if isinstance(payload, dict) else "") != self.user:
            raise ComparisonFailure(step, "unexpected_user")
        if payload.get("password_change_required") is not True:
            raise ComparisonFailure(step, "password_change_not_required")
        self.portal_cookie = cookie_value(response["set_cookie"], "cpa_user_session", step)

    def change_password(self):
        step = "password_change"
        response = self.portal_request(
            "PUT",
            "/usage/me/password",
            {
                "current_password": self.initial_password,
                "new_password": self.generated_password,
            },
        )
        payload = self.require_status(step, response, 200)
        if payload.get("password_change_required") is not False:
            raise ComparisonFailure(step, "password_change_not_confirmed")

    @staticmethod
    def normalize_catalog(payload, surface):
        if surface == "v1":
            rows = payload.get("groups") if isinstance(payload, dict) else None
            route = payload.get("current_group") if isinstance(payload, dict) else None
            api_key = payload.get("api_key") if isinstance(payload, dict) else None
        else:
            rows = payload.get("accounts") if isinstance(payload, dict) else None
            route = payload.get("current_group") if isinstance(payload, dict) else None
            api_key = None
        if not isinstance(rows, list) or not isinstance(route, str) or not route:
            raise ComparisonFailure("accounts_read", "invalid_account_catalog")
        catalog = set()
        selectable = set()
        operational = {}
        for row in rows:
            if not isinstance(row, dict):
                raise ComparisonFailure("accounts_read", "invalid_account_row")
            account = str(row.get("id") or row.get("account") or "").strip()
            if not account:
                raise ComparisonFailure("accounts_read", "missing_account_id")
            catalog.add(account)
            allowed = row.get("selectable")
            if allowed is None and isinstance(row.get("operational_status"), dict):
                allowed = row["operational_status"].get("selectable")
            if allowed is True:
                selectable.add(account)
            status = row.get("operational_status")
            if not isinstance(status, dict) and isinstance(row.get("status"), dict):
                status = row["status"]
            code = status.get("code") if isinstance(status, dict) else row.get("status")
            operational[account] = {
                "code": str(code or "unknown"),
                "selectable": allowed is True,
            }
        return route, api_key, catalog, selectable, operational

    def read_portal_state(self):
        if self.target.surface == "v1":
            response = self.portal_request("GET", "/usage/me?window=3600&lifetime=0")
            payload = self.require_status("portal_session", response, 200)
            self.steps["profile_read"] = self.steps["portal_session"]
            self.steps["accounts_read"] = self.steps["portal_session"]
            route, api_key, catalog, selectable, operational = self.normalize_catalog(
                payload, "v1"
            )
            if api_key != self.expected_key:
                raise ComparisonFailure("profile_read", "test_key_mismatch")
        else:
            session_response = self.portal_request("GET", "/usage/session")
            session_payload = self.require_status("portal_session", session_response, 200)
            if session_payload.get("authenticated") is not True or session_payload.get(
                "password_change_required"
            ) is not False:
                raise ComparisonFailure("portal_session", "invalid_session_state")

            profile_response = self.portal_request("GET", "/usage/me/profile")
            profile = self.require_status("profile_read", profile_response, 200)
            if profile.get("api_key") != self.expected_key:
                raise ComparisonFailure("profile_read", "test_key_mismatch")

            accounts_response = self.portal_request("GET", "/usage/me/accounts?window=3600")
            accounts = self.require_status("accounts_read", accounts_response, 200)
            route, _, catalog, selectable, operational = self.normalize_catalog(
                accounts, "v2"
            )
            if profile.get("current_group") != route:
                raise ComparisonFailure("profile_read", "route_mismatch")

        route_response = self.portal_request("GET", "/usage/me/route")
        route_payload = self.require_status("route_read", route_response, 200)
        if route_payload.get("current_group") != route:
            raise ComparisonFailure("route_read", "route_mismatch")
        if route not in catalog or not self.key_accounts.issubset(catalog):
            raise ComparisonFailure("accounts_read", "incomplete_test_key_catalog")

        breakdown_response = self.portal_request(
            "GET",
            "/usage/me/usage-breakdown?"
            + urllib.parse.urlencode({"account": route, "window": "3600"}),
        )
        self.require_status("usage_breakdown", breakdown_response, 200)
        self.original_route = route
        self.catalog = catalog
        self.selectable = selectable
        self.operational = operational

    def prepare(self):
        self.admin_login()
        self.reset_password("password_reset_before")
        self.portal_login()
        self.change_password()
        self.read_portal_state()
        self.prepared = True

    def switch_route(self, target_route):
        if target_route == self.original_route:
            raise ComparisonFailure("route_switch", "probe_route_matches_original")
        response = self.portal_request(
            "PUT", "/usage/me/group", {"group_id": target_route}
        )
        self.require_status("route_switch", response, 200)
        self.route_changed = True
        readback = self.portal_request("GET", "/usage/me/route")
        payload = self.require_status("route_switch_readback", readback, 200)
        if payload.get("current_group") != target_route:
            raise ComparisonFailure("route_switch_readback", "route_not_changed")

        restore = self.portal_request(
            "PUT", "/usage/me/group", {"group_id": self.original_route}
        )
        self.require_status("route_restore", restore, 200)
        readback = self.portal_request("GET", "/usage/me/route")
        payload = self.require_status("route_restore_readback", readback, 200)
        if payload.get("current_group") != self.original_route:
            raise ComparisonFailure("route_restore_readback", "route_not_restored")
        self.route_changed = False

    def cleanup(self):
        cleanup_failed = False
        if self.route_changed and self.portal_cookie and self.original_route:
            try:
                response = self.portal_request(
                    "PUT", "/usage/me/group", {"group_id": self.original_route}
                )
                self.require_status("emergency_route_restore", response, 200)
                self.route_changed = False
            except Exception:
                cleanup_failed = True
                self.failures.append(
                    {"step": "emergency_route_restore", "reason": "cleanup_failed"}
                )
        if self.admin_cookie:
            try:
                self.reset_password("password_reset_after")
            except Exception:
                cleanup_failed = True
                self.failures.append(
                    {"step": "password_reset_after", "reason": "cleanup_failed"}
                )
        if self.portal_cookie:
            try:
                response = self.portal_request("GET", "/usage/me/route")
                self.require_status("reset_session_revoked", response, 401)
                response = self.portal_request("DELETE", "/usage/session")
                self.require_status("portal_logout", response, 200)
            except Exception:
                cleanup_failed = True
                self.failures.append(
                    {"step": "portal_logout", "reason": "cleanup_failed"}
                )
        if self.admin_cookie:
            try:
                response = self.admin_request("DELETE", "/admin/api/session")
                self.require_status("admin_logout", response, 200)
            except Exception:
                cleanup_failed = True
                self.failures.append(
                    {"step": "admin_logout", "reason": "cleanup_failed"}
                )
        self.generated_password = ""
        self.initial_password = ""
        self.management_key = ""
        self.expected_key = ""
        self.admin_cookie = ""
        self.admin_csrf = ""
        self.portal_cookie = ""
        self.cleaned = not cleanup_failed and not self.route_changed

    def report(self):
        status_counts = {}
        for status in self.operational.values():
            key = "{}|{}".format(status["code"], str(status["selectable"]).lower())
            status_counts[key] = status_counts.get(key, 0) + 1
        operational_rows = [
            (account, status["code"], status["selectable"])
            for account, status in sorted(self.operational.items())
        ]
        return {
            "surface": self.target.surface,
            "probe_kind": self.probe_kind,
            "base_url": self.target.base_url,
            "probe_user_sha256": digest(self.user),
            "account_catalog_sha256": digest_set(self.catalog) if self.catalog else "",
            "account_count": len(self.catalog),
            "selectable_count": len(self.selectable),
            "original_route_sha256": digest(self.original_route) if self.original_route else "",
            "original_route_selectable": bool(
                self.original_route and self.original_route in self.selectable
            ),
            "account_status_counts": status_counts,
            "account_operational_sha256": digest(
                json.dumps(operational_rows, separators=(",", ":"))
            )
            if operational_rows
            else "",
            "cleaned": self.cleaned,
            "steps": self.steps,
            "failures": self.failures,
        }


def validate_pair(targets):
    if len(targets) != 2 or {target.surface for target in targets} != {"v1", "v2"}:
        raise ValueError("Portal comparison requires exactly one v1 and one v2 target")
    roots = [validate_target(target) for target in targets]
    databases = [target.control_db.resolve() for target in targets]
    if roots[0] == roots[1] or databases[0] == databases[1]:
        raise ValueError("v1 and v2 must use distinct isolated state copies")
    if os.path.samefile(databases[0], databases[1]):
        raise ValueError("v1 and v2 control databases must not be the same inode")


def run(targets, management_key, test_key, timeout):
    validate_pair(targets)
    initial_runs = [TargetRun(target, management_key, test_key, timeout) for target in targets]

    def prepare_all(items):
        for target_run in items:
            try:
                target_run.prepare()
            except ComparisonFailure as error:
                target_run.failures.append({"step": error.step, "reason": error.reason})
            except Exception:
                target_run.failures.append(
                    {"step": "prepare", "reason": "unexpected_exception"}
                )

    prepare_all(initial_runs)
    route_runs = initial_runs
    initial_reports = None
    fallback_used = False

    if all(target_run.prepared for target_run in initial_runs):
        initial_common = set.intersection(
            *(target_run.selectable for target_run in initial_runs)
        )
        if any(
            target_run.original_route not in target_run.selectable
            for target_run in initial_runs
        ):
            for target_run in initial_runs:
                target_run.cleanup()
            initial_reports = {
                target_run.target.name: copy.deepcopy(target_run.report())
                for target_run in initial_runs
            }
            fallback_user = select_fallback_probe(
                targets, initial_common, initial_runs[0].user
            )
            if fallback_user:
                fallback_used = True
                route_runs = [
                    TargetRun(
                        target,
                        management_key,
                        test_key,
                        timeout,
                        probe_user=fallback_user,
                        probe_kind="isolated_existing_reversible_user",
                    )
                    for target in targets
                ]
                prepare_all(route_runs)
            else:
                route_runs = []

    if route_runs and all(target_run.prepared for target_run in route_runs):
        common = set.intersection(*(target_run.selectable for target_run in route_runs))
        originals = {target_run.original_route for target_run in route_runs}
        candidates = sorted(common - originals)
        unrestorable = [
            target_run
            for target_run in route_runs
            if target_run.original_route not in target_run.selectable
        ]
        if unrestorable:
            for target_run in route_runs:
                target_run.failures.append(
                    {"step": "route_switch", "reason": "original_route_not_restorable"}
                )
        elif candidates:
            selected = candidates[0]
            for target_run in route_runs:
                try:
                    target_run.switch_route(selected)
                except ComparisonFailure as error:
                    target_run.failures.append(
                        {"step": error.step, "reason": error.reason}
                    )
                except Exception:
                    target_run.failures.append(
                        {"step": "route_switch", "reason": "unexpected_exception"}
                    )
        else:
            for target_run in route_runs:
                target_run.failures.append(
                    {"step": "route_switch", "reason": "no_common_safe_alternate"}
                )
    elif not route_runs:
        for target_run in initial_runs:
            target_run.failures.append(
                {"step": "route_switch", "reason": "no_reversible_fallback_user"}
            )

    for target_run in route_runs:
        if not target_run.cleaned:
            target_run.cleanup()

    reports_source = route_runs or initial_runs
    reports = {target_run.target.name: target_run.report() for target_run in reports_source}
    unexpected = []

    def compare_report_pair(pair, workflow):
        pair_by_surface = {report["surface"]: report for report in pair.values()}
        for surface, report in sorted(pair_by_surface.items()):
            if report["failures"] or not report["cleaned"]:
                unexpected.append(
                    {
                        "workflow": workflow,
                        "surface": surface,
                        "failures": report["failures"],
                        "cleaned": report["cleaned"],
                    }
                )
        if len(pair_by_surface) != 2:
            return
        for field in (
            "probe_user_sha256",
            "account_catalog_sha256",
            "account_count",
            "selectable_count",
            "original_route_sha256",
            "original_route_selectable",
            "account_status_counts",
            "account_operational_sha256",
        ):
            if pair_by_surface["v1"][field] != pair_by_surface["v2"][field]:
                unexpected.append(
                    {"workflow": workflow, "field": field, "reason": "logical_state_mismatch"}
                )

    if initial_reports is not None:
        compare_report_pair(initial_reports, "dedicated_test_user_reads")
    compare_report_pair(reports, "route_write_probe")

    runs_by_surface = {target_run.target.surface: target_run for target_run in reports_source}
    transitions = {}
    if len(runs_by_surface) == 2:
        v1_run = runs_by_surface["v1"]
        v2_run = runs_by_surface["v2"]
        for account in sorted(set(v1_run.operational) & set(v2_run.operational)):
            left = v1_run.operational[account]
            right = v2_run.operational[account]
            key = "{}|{} -> {}|{}".format(
                left["code"],
                str(left["selectable"]).lower(),
                right["code"],
                str(right["selectable"]).lower(),
            )
            transitions[key] = transitions.get(key, 0) + 1

    return {
        "version": 1,
        "targets": reports,
        "dedicated_test_user_reads": initial_reports,
        "route_write_probe": reports,
        "fallback_route_probe_used": fallback_used,
        "account_operational_transitions": transitions,
        "compatible": not unexpected,
        "mapped_endpoints": [
            {
                "logical": "portal session and profile",
                "v1": "/usage/me",
                "v2": "/usage/session + /usage/me/profile",
            },
            {
                "logical": "portal account catalog",
                "v1": "/usage/me",
                "v2": "/usage/me/accounts",
            },
        ],
        "unexpected_differences": unexpected,
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", action="append", type=parse_target, required=True)
    parser.add_argument("--timeout", type=float, default=20.0)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--credentials-stdin", action="store_true", required=True)
    parser.add_argument("--confirm-isolated-write-test", action="store_true", required=True)
    args = parser.parse_args(argv)
    management_key = sys.stdin.readline().strip()
    test_key = sys.stdin.readline().strip()
    if not management_key or not test_key:
        raise SystemExit("management key and dedicated Test Key require two stdin lines")
    if len(management_key) > 4096 or len(test_key) > 4096:
        raise SystemExit("stdin credential length is invalid")
    report = run(args.target, management_key, test_key, args.timeout)
    rendered = json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.write_text(rendered, encoding="utf-8")
    print(rendered, end="")
    return 0 if report["compatible"] else 1


if __name__ == "__main__":
    sys.exit(main())
