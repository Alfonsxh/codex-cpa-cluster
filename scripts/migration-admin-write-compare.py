#!/usr/bin/env python3
"""Compare reversible Admin writes on two explicitly isolated CPA copies.

The comparator accepts one management key only from stdin, never on the command
line or through the environment. Reports contain only HTTP status, error codes,
schema field names, value-free persistence counters/version deltas, and one-way
user digests; response values, cookies, CSRF tokens, API keys, account
identifiers, and email addresses are never emitted.

This is intentionally not an account/runtime lifecycle test. It exercises only
isolated user-catalog writes without creating, rebuilding, or deleting CPA
containers: team CRUD, exact team-membership restoration, exact quota-policy
restoration, and a disposable user's create/rotate/reset/revoke/delete
lifecycle. The reviewed Python-v1 inactive internal-Key residue is reported but
never removed by direct database writes.
"""

import argparse
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


MAX_RESPONSE_BYTES = 4 * 1024 * 1024
TEMPORARY_WEEKLY_TOKENS = 987_654_321
PRODUCTION_ROOTS = {
    Path("/home/AI/CLIProxyAPI"),
    Path("/opt/codex-cpa-cluster"),
}

# These are reviewed product/API-boundary changes. A write comparison accepts
# them only when the complete value-free step summary matches exactly. New
# fields, statuses, or error codes remain unexpected and fail the run.
WRITE_DECISIONS = {
    "login": {
        "reason": "v2 loads the account catalog through its fine-grained endpoint after login.",
        "v1": {
            "status": 201,
            "content_type": "application/json",
            "error_code": "",
            "schema": {
                "kind": "object",
                "top_keys": ["accounts", "authenticated", "csrf_token"],
            },
        },
        "v2": {
            "status": 201,
            "content_type": "application/json",
            "error_code": "",
            "schema": {"kind": "object", "top_keys": ["authenticated", "csrf_token"]},
        },
    },
    "team_readback": {
        "reason": "v2 removes the retired tag catalog from the team response.",
        "v1": {
            "status": 200,
            "content_type": "application/json",
            "error_code": "",
            "schema": {"kind": "object", "top_keys": ["tags", "teams"]},
        },
        "v2": {
            "status": 200,
            "content_type": "application/json",
            "error_code": "",
            "schema": {"kind": "object", "top_keys": ["teams"]},
        },
    },
    "team_delete_readback": {
        "reason": "v2 removes the retired tag catalog from the team response.",
        "v1": {
            "status": 200,
            "content_type": "application/json",
            "error_code": "",
            "schema": {"kind": "object", "top_keys": ["tags", "teams"]},
        },
        "v2": {
            "status": 200,
            "content_type": "application/json",
            "error_code": "",
            "schema": {"kind": "object", "top_keys": ["teams"]},
        },
    },
    "team_duplicate": {
        "reason": "v2 exposes a precise conflict status and code for a duplicate team name.",
        "v1": {
            "status": 400,
            "content_type": "application/json",
            "error_code": "invalid_request",
            "schema": {"kind": "object", "top_keys": ["error"]},
        },
        "v2": {
            "status": 409,
            "content_type": "application/json",
            "error_code": "team_name_conflict",
            "schema": {"kind": "object", "top_keys": ["error"]},
        },
    },
    "user_create": {
        "reason": "v2 groups the one-time API Key and initial password under the user lifecycle result.",
        "v1": {
            "status": 201,
            "content_type": "application/json",
            "error_code": "",
            "schema": {
                "kind": "object",
                "top_keys": ["initial_password", "keys", "message", "team", "team_id"],
            },
        },
        "v2": {
            "status": 201,
            "content_type": "application/json",
            "error_code": "",
            "schema": {"kind": "object", "top_keys": ["message", "user"]},
        },
    },
    "user_key_rotate": {
        "reason": "v2 returns the one-time replacement Key from the user-wide rotation transaction.",
        "v1": {
            "status": 200,
            "content_type": "application/json",
            "error_code": "",
            "schema": {"kind": "object", "top_keys": ["keys", "message"]},
        },
        "v2": {
            "status": 200,
            "content_type": "application/json",
            "error_code": "",
            "schema": {"kind": "object", "top_keys": ["key", "message"]},
        },
    },
    "user_password_reset": {
        "reason": "v2 groups the one-time password and forced-change state under the password result.",
        "v1": {
            "status": 200,
            "content_type": "application/json",
            "error_code": "",
            "schema": {
                "kind": "object",
                "top_keys": [
                    "initial_password",
                    "message",
                    "password_change_required",
                    "user",
                ],
            },
        },
        "v2": {
            "status": 200,
            "content_type": "application/json",
            "error_code": "",
            "schema": {"kind": "object", "top_keys": ["message", "password"]},
        },
    },
    "user_revoke": {
        "reason": "v2 returns the user-wide revocation and activated snapshot metadata as one result.",
        "v1": {
            "status": 200,
            "content_type": "application/json",
            "error_code": "",
            "schema": {"kind": "object", "top_keys": ["message", "revoked"]},
        },
        "v2": {
            "status": 200,
            "content_type": "application/json",
            "error_code": "",
            "schema": {"kind": "object", "top_keys": ["message", "revocation"]},
        },
    },
}

# Persistent-state differences are reviewed separately from HTTP response
# differences.  The Python v1 intentionally retains one inactive internal Key
# after deleting a user, while Go v2 removes that non-historical record.  Every
# other current-user projection must be gone on both surfaces.
PERSISTENCE_DECISIONS = {
    "temporary_user_cleanup": {
        "reason": (
            "Python v1 retains one inactive internal Key audit row; Go v2 "
            "removes it while both versions remove all active/current user state."
        ),
        "v1": {
            "key_records": 0,
            "active_key_records": 0,
            "internal_keys": 1,
            "inactive_internal_keys": 1,
            "other_internal_keys": 0,
            "user_routes": 0,
            "user_team_memberships": 0,
            "user_tags": 0,
            "portal_sessions": 0,
            "portal_credentials": 0,
            "user_quota_policies": 0,
            "key_identities": 0,
            "usage_events": 0,
            "user_quota_adjustments": 0,
            "user_weekly_usage": 0,
        },
        "v2": {
            "key_records": 0,
            "active_key_records": 0,
            "internal_keys": 0,
            "inactive_internal_keys": 0,
            "other_internal_keys": 0,
            "user_routes": 0,
            "user_team_memberships": 0,
            "user_tags": 0,
            "portal_sessions": 0,
            "portal_credentials": 0,
            "user_quota_policies": 0,
            "key_identities": 0,
            "usage_events": 0,
            "user_quota_adjustments": 0,
            "user_weekly_usage": 0,
        },
    }
}


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
    return Target(
        name=name,
        surface=surface,
        base_url=base_url.rstrip("/"),
        control_db=Path(control_db),
    )


def deployment_root(target):
    database = target.control_db.resolve()
    if database.name != "control-plane.sqlite3" or database.parent.name != "state":
        raise ValueError(
            "{} control database must be ROOT/state/control-plane.sqlite3".format(target.name)
        )
    return database.parent.parent


def validate_target(target):
    root = deployment_root(target)
    if root in PRODUCTION_ROOTS or root == Path("/"):
        raise ValueError("{} points at a live or broad deployment root".format(target.name))
    marker = root / ".v2-isolated-copy.json"
    if not marker.is_file():
        raise ValueError("{} isolated-copy marker is missing".format(target.name))
    if not target.control_db.is_file():
        raise ValueError("{} control database is missing".format(target.name))
    return root


def read_probe_user(database_path):
    database = sqlite3.connect("file:{}?mode=ro".format(database_path.resolve()), uri=True)
    try:
        row = database.execute(
            "SELECT lower(trim(user_email)) FROM key_records "
            "WHERE status = 'active' AND trim(user_email) <> '' "
            "ORDER BY sequence LIMIT 1"
        ).fetchone()
    finally:
        database.close()
    if not row or not row[0]:
        raise ValueError("comparison database has no active user")
    return str(row[0])


def json_schema(value):
    if not isinstance(value, dict):
        return {"kind": "array" if isinstance(value, list) else type(value).__name__}
    return {"kind": "object", "top_keys": sorted(value)}


def decode_json(response):
    try:
        return json.loads(response["body"])
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise ValueError("Admin response is not valid JSON") from error


def error_code(payload):
    if not isinstance(payload, dict):
        return ""
    error = payload.get("error")
    return str(error.get("code") or "") if isinstance(error, dict) else ""


class Client:
    def __init__(self, target, timeout):
        parsed = urllib.parse.urlsplit(target.base_url)
        self.target = target
        self.host = parsed.hostname
        self.port = parsed.port or 80
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
            raw = json.dumps(body, separators=(",", ":")).encode("utf-8")
            request_headers["Content-Type"] = "application/json"
        connection = http.client.HTTPConnection(self.host, self.port, timeout=self.timeout)
        connection.request(method, self.prefix + path, body=raw, headers=request_headers)
        response = connection.getresponse()
        payload = response.read(MAX_RESPONSE_BYTES + 1)
        if len(payload) > MAX_RESPONSE_BYTES:
            connection.close()
            raise ValueError("Admin response exceeded 4 MiB")
        result = {
            "status": response.status,
            "content_type": (response.getheader("Content-Type") or "").split(";", 1)[0],
            "set_cookie": response.getheader("Set-Cookie") or "",
            "body": payload,
        }
        connection.close()
        return result

    def login(self, management_key):
        response = self.request(
            "POST", "/admin/api/session", headers={"X-Management-Key": management_key}
        )
        payload = decode_json(response)
        if response["status"] != 201 or not response["set_cookie"]:
            raise ValueError("Admin login failed with status {}".format(response["status"]))
        cookies = SimpleCookie()
        cookies.load(response["set_cookie"])
        morsel = next(iter(cookies.values()))
        csrf = payload.get("csrf_token")
        if not isinstance(csrf, str) or not csrf:
            raise ValueError("Admin session lacks CSRF token")
        self.cookie = morsel.key + "=" + morsel.value
        self.csrf = csrf
        return response

    def authenticated(self, method, path, body=None):
        return self.request(
            method,
            path,
            headers={"Cookie": self.cookie, "X-CSRF-Token": self.csrf},
            body=body,
        )

    def logout(self):
        return self.authenticated("DELETE", "/admin/api/session")


def summarized_step(response):
    payload = decode_json(response)
    return {
        "status": response["status"],
        "content_type": response["content_type"],
        "error_code": error_code(payload),
        "schema": json_schema(payload),
    }, payload


def require_status(name, response, expected):
    if response["status"] != expected:
        payload = decode_json(response)
        raise ValueError(
            "{} returned {}, expected {} ({})".format(
                name, response["status"], expected, error_code(payload) or "no error code"
            )
        )


def require_object(value, name):
    if not isinstance(value, dict):
        raise ValueError("{} is not an object".format(name))
    return value


def require_secret(value, name):
    if not isinstance(value, str) or not value.strip():
        raise ValueError("{} is missing its one-time secret".format(name))
    return value


def temporary_user_email(probe_user, suffix):
    local, separator, domain = probe_user.rpartition("@")
    if not separator or not local or not domain:
        raise ValueError("comparison probe user is not an email address")
    value = "migration-write-{}@{}".format(suffix, domain).lower()
    if len(value) > 254:
        raise ValueError("temporary comparison user would exceed 254 characters")
    return value


def quota_policy(payload):
    weekly = require_object(
        require_object(payload, "user quota response").get("weekly_quota"),
        "weekly quota",
    )
    mode = weekly.get("policy_mode")
    tokens = weekly.get("policy_tokens")
    if mode not in {"inherit", "unlimited", "custom"}:
        raise ValueError("user quota response has an unsupported policy mode")
    if mode == "custom":
        if isinstance(tokens, bool) or not isinstance(tokens, int) or tokens <= 0:
            raise ValueError("custom user quota response lacks positive policy tokens")
    elif tokens is not None:
        raise ValueError("non-custom user quota response unexpectedly has policy tokens")
    return {"mode": mode, "weekly_tokens": tokens}


def restore_quota_policy(client, email, policy):
    if policy["mode"] == "inherit":
        return client.authenticated(
            "DELETE",
            "/admin/api/users/quota?" + urllib.parse.urlencode({"email": email}),
        )
    return client.authenticated(
        "PUT",
        "/admin/api/users/quota",
        {
            "email": email,
            "mode": policy["mode"],
            "weekly_tokens": policy["weekly_tokens"],
        },
    )


def read_user(client, email):
    query = urllib.parse.urlencode(
        {
            "view": "summary",
            "page": 1,
            "page_size": 100,
            "q": email,
        }
    )
    response = client.authenticated("GET", "/admin/api/users?" + query)
    require_status("user readback", response, 200)
    payload = require_object(decode_json(response), "user readback")
    users = payload.get("users")
    if not isinstance(users, list):
        raise ValueError("user readback lacks a users array")
    matching = [
        item
        for item in users
        if isinstance(item, dict)
        and str(item.get("email") or "").strip().lower() == email.lower()
    ]
    if len(matching) > 1:
        raise ValueError("user readback returned duplicate exact users")
    return matching[0] if matching else None


def read_user_team(client, email):
    user = read_user(client, email)
    if user is None:
        raise ValueError("probe user is not visible through the Admin API")
    team_id = user.get("team_id")
    if team_id is not None and not isinstance(team_id, str):
        raise ValueError("probe user has an invalid team id")
    return team_id


def usage_database(target):
    path = deployment_root(target) / "state" / "usage.sqlite3"
    if not path.is_file():
        raise ValueError("{} usage database is missing".format(target.name))
    return path


def open_readonly_database(path):
    connection = sqlite3.connect(
        "file:{}?mode=ro".format(path.resolve()), uri=True, timeout=5.0
    )
    connection.execute("PRAGMA query_only = ON")
    return connection


def read_membership_state(target, email):
    """Return only value-free team/identity consistency counters."""

    user = str(email or "").strip().lower()
    if not user:
        raise ValueError("membership state user is required")
    control = open_readonly_database(target.control_db)
    usage = open_readonly_database(usage_database(target))
    try:
        memberships = control.execute(
            "SELECT team_id, membership_version "
            "FROM user_team_memberships WHERE lower(trim(user_email)) = ?",
            (user,),
        ).fetchall()
        if len(memberships) > 1:
            raise ValueError("control database contains duplicate team memberships")
        if memberships:
            control_team = str(memberships[0][0] or "")
            membership_version = memberships[0][1]
            membership_row = True
        else:
            control_team = ""
            membership_version = 0
            membership_row = False
        if (
            isinstance(membership_version, bool)
            or not isinstance(membership_version, int)
            or membership_version < 0
        ):
            raise ValueError("control membership version is invalid")

        identities = usage.execute(
            "SELECT team_id, team_membership_version "
            "FROM key_identities WHERE lower(trim(user_email)) = ?",
            (user,),
        ).fetchall()
        matching = 0
        for identity_team, identity_version in identities:
            if (
                isinstance(identity_version, bool)
                or not isinstance(identity_version, int)
                or identity_version < 0
            ):
                raise ValueError("usage identity membership version is invalid")
            if (
                str(identity_team or "") == control_team
                and identity_version == membership_version
            ):
                matching += 1
        return {
            "membership_row": membership_row,
            "team_assigned": bool(control_team),
            "membership_version": membership_version,
            "identity_rows": len(identities),
            "matching_identity_rows": matching,
            "all_identities_match": bool(identities) and matching == len(identities),
        }
    finally:
        control.close()
        usage.close()


def membership_transition(name, before, after):
    if before["identity_rows"] < 1 or not before["all_identities_match"]:
        raise ValueError("{} started with inconsistent usage identities".format(name))
    if after["identity_rows"] != before["identity_rows"]:
        raise ValueError("{} changed the number of usage identities".format(name))
    if not after["all_identities_match"]:
        raise ValueError("{} did not synchronize every usage identity".format(name))
    version_delta = after["membership_version"] - before["membership_version"]
    if version_delta != 1:
        raise ValueError("{} did not increment membership version exactly once".format(name))
    return {
        "membership_version_before": before["membership_version"],
        "membership_version_after": after["membership_version"],
        "membership_version_delta": version_delta,
        "identity_rows_before": before["identity_rows"],
        "identity_rows_after": after["identity_rows"],
        "matching_identity_rows_after": after["matching_identity_rows"],
        "all_identities_match": after["all_identities_match"],
    }


def temporary_user_cleanup_state(target, email):
    """Summarize exact current-user residue without returning identifiers."""

    user = str(email or "").strip().lower()
    if not user:
        raise ValueError("temporary cleanup user is required")
    control = open_readonly_database(target.control_db)
    usage = open_readonly_database(usage_database(target))
    try:
        def count(connection, table, suffix="", parameters=()):
            return connection.execute(
                "SELECT COUNT(*) FROM {} WHERE lower(trim(user_email)) = ?{}".format(
                    table, suffix
                ),
                (user,) + tuple(parameters),
            ).fetchone()[0]

        internal_keys = count(control, "internal_keys")
        inactive_internal_keys = count(
            control, "internal_keys", " AND status = ?", ("inactive",)
        )
        return {
            "key_records": count(control, "key_records"),
            "active_key_records": count(
                control, "key_records", " AND status = ?", ("active",)
            ),
            "internal_keys": internal_keys,
            "inactive_internal_keys": inactive_internal_keys,
            "other_internal_keys": internal_keys - inactive_internal_keys,
            "user_routes": count(control, "user_routes"),
            "user_team_memberships": count(control, "user_team_memberships"),
            "user_tags": count(control, "user_tags"),
            "portal_sessions": count(usage, "portal_sessions"),
            "portal_credentials": count(usage, "portal_credentials"),
            "user_quota_policies": count(usage, "user_quota_policies"),
            "key_identities": count(usage, "key_identities"),
            "usage_events": count(usage, "usage_events"),
            "user_quota_adjustments": count(usage, "user_quota_adjustments"),
            "user_weekly_usage": count(usage, "user_weekly_usage"),
        }
    finally:
        control.close()
        usage.close()


def require_temporary_user_cleanup(surface, state):
    expected = PERSISTENCE_DECISIONS["temporary_user_cleanup"].get(surface)
    if state != expected:
        mismatched = sorted(
            key for key in set(state) | set(expected or {})
            if state.get(key) != (expected or {}).get(key)
        )
        raise ValueError(
            "temporary user cleanup violated the reviewed {} contract ({})".format(
                surface, ", ".join(mismatched)
            )
        )
    return state


def find_team_id(client, names):
    response = client.authenticated("GET", "/admin/api/teams")
    require_status("team cleanup readback", response, 200)
    payload = require_object(decode_json(response), "team cleanup readback")
    teams = payload.get("teams")
    if not isinstance(teams, list):
        raise ValueError("team cleanup readback lacks a teams array")
    matches = [
        item.get("id")
        for item in teams
        if isinstance(item, dict)
        and item.get("name") in names
        and isinstance(item.get("id"), str)
    ]
    if len(matches) > 1:
        raise ValueError("multiple temporary teams matched during cleanup")
    return matches[0] if matches else ""


def create_credentials(payload, surface):
    payload = require_object(payload, "user create response")
    if surface == "v1":
        keys = payload.get("keys")
        if not isinstance(keys, list) or len(keys) != 1:
            raise ValueError("v1 user create response must expose one unified Key")
        key = require_object(keys[0], "v1 created Key")
        return {
            "api_key": require_secret(key.get("key"), "v1 user create response"),
            "initial_password": require_secret(
                payload.get("initial_password"), "v1 user create response"
            ),
            "team_id": payload.get("team_id"),
            "label": require_secret(key.get("label"), "v1 user create response label"),
        }
    user = require_object(payload.get("user"), "v2 user create response")
    return {
        "api_key": require_secret(user.get("api_key"), "v2 user create response"),
        "initial_password": require_secret(
            user.get("initial_password"), "v2 user create response"
        ),
        "team_id": user.get("team_id"),
        "label": "",
    }


def rotated_key(payload, surface):
    payload = require_object(payload, "user key rotation response")
    if surface == "v1":
        keys = payload.get("keys")
        if not isinstance(keys, list) or len(keys) != 1:
            raise ValueError("v1 Key rotation response must expose one unified Key")
        return require_secret(
            require_object(keys[0], "v1 rotated Key").get("key"),
            "v1 Key rotation response",
        )
    return require_secret(
        require_object(payload.get("key"), "v2 rotated Key").get("api_key"),
        "v2 Key rotation response",
    )


def reset_password(payload, surface):
    payload = require_object(payload, "user password reset response")
    result = payload if surface == "v1" else require_object(
        payload.get("password"), "v2 password reset result"
    )
    if result.get("password_change_required") is not True:
        raise ValueError("password reset did not require a first-login change")
    return require_secret(result.get("initial_password"), "password reset response")


def revoked_key_count(payload, surface):
    payload = require_object(payload, "user revoke response")
    value = payload.get("revoked") if surface == "v1" else require_object(
        payload.get("revocation"), "v2 revocation result"
    ).get("revoked_keys")
    if isinstance(value, bool) or not isinstance(value, int) or value < 1:
        raise ValueError("user revoke response did not revoke the temporary Key")
    return value


def run_target(target, management_key, timeout, probe_suffix):
    probe_user = read_probe_user(target.control_db)
    temporary_user = temporary_user_email(probe_user, probe_suffix)
    client = Client(target, timeout)
    steps = {}
    persistence = {}
    created_team_id = ""
    team_created = False
    quota_dirty = False
    original_quota = None
    existing_team_dirty = False
    original_team_id = None
    temporary_user_created = False
    failure = None
    cleanup_errors = []
    probe_name = "Migration Write Probe {}".format(probe_suffix)
    updated_name = "Migration Write Verified {}".format(probe_suffix)
    try:
        response = client.login(management_key)
        steps["login"], _ = summarized_step(response)

        response = client.authenticated(
            "GET",
            "/admin/api/users/quota?" + urllib.parse.urlencode({"email": probe_user}),
        )
        require_status("original user quota", response, 200)
        steps["quota_original_read"], payload = summarized_step(response)
        original_quota = quota_policy(payload)

        temporary_quota = TEMPORARY_WEEKLY_TOKENS
        if (
            original_quota["mode"] == "custom"
            and original_quota["weekly_tokens"] == temporary_quota
        ):
            temporary_quota += 1
        response = client.authenticated(
            "PUT",
            "/admin/api/users/quota",
            {
                "email": probe_user,
                "mode": "custom",
                "weekly_tokens": temporary_quota,
            },
        )
        require_status("temporary user quota", response, 200)
        quota_dirty = True
        steps["quota_update"], payload = summarized_step(response)
        if quota_policy(payload) != {
            "mode": "custom",
            "weekly_tokens": temporary_quota,
        }:
            raise ValueError("temporary user quota response did not preserve the policy")

        response = client.authenticated(
            "GET",
            "/admin/api/users/quota?" + urllib.parse.urlencode({"email": probe_user}),
        )
        require_status("temporary user quota readback", response, 200)
        steps["quota_update_readback"], payload = summarized_step(response)
        if quota_policy(payload) != {
            "mode": "custom",
            "weekly_tokens": temporary_quota,
        }:
            raise ValueError("temporary user quota was not visible after update")

        response = restore_quota_policy(client, probe_user, original_quota)
        require_status("user quota restore", response, 200)
        steps["quota_restore"], payload = summarized_step(response)
        if quota_policy(payload) != original_quota:
            raise ValueError("user quota restore response did not match the original policy")
        response = client.authenticated(
            "GET",
            "/admin/api/users/quota?" + urllib.parse.urlencode({"email": probe_user}),
        )
        require_status("user quota restore readback", response, 200)
        steps["quota_restore_readback"], payload = summarized_step(response)
        if quota_policy(payload) != original_quota:
            raise ValueError("user quota restore was not visible after update")
        quota_dirty = False

        original_team_id = read_user_team(client, probe_user)

        response = client.authenticated(
            "POST",
            "/admin/api/teams",
            {"name": probe_name, "description": "isolated migration comparison"},
        )
        require_status("team create", response, 201)
        team_created = True
        steps["team_create"], payload = summarized_step(response)
        team = payload.get("team") if isinstance(payload, dict) else None
        if not isinstance(team, dict) or not isinstance(team.get("id"), str):
            raise ValueError("team create response lacks a team id")
        created_team_id = team["id"]
        if team.get("name") != probe_name or team.get("description") != "isolated migration comparison":
            raise ValueError("team create response did not preserve normalized fields")

        response = client.authenticated(
            "POST", "/admin/api/teams", {"name": probe_name.lower()}
        )
        steps["team_duplicate"], _ = summarized_step(response)

        response = client.authenticated(
            "PUT",
            "/admin/api/teams",
            {"id": created_team_id, "name": updated_name, "description": "verified"},
        )
        require_status("team update", response, 200)
        steps["team_update"], payload = summarized_step(response)
        updated = payload.get("team") if isinstance(payload, dict) else None
        if not isinstance(updated, dict) or updated.get("name") != updated_name:
            raise ValueError("team update response did not expose the updated team")

        response = client.authenticated("GET", "/admin/api/teams")
        require_status("team readback", response, 200)
        steps["team_readback"], payload = summarized_step(response)
        teams = payload.get("teams") if isinstance(payload, dict) else None
        if not isinstance(teams, list) or not any(
            isinstance(item, dict)
            and item.get("id") == created_team_id
            and item.get("name") == updated_name
            for item in teams
        ):
            raise ValueError("team readback did not contain the updated team")

        membership_before_assignment = read_membership_state(target, probe_user)
        response = client.authenticated(
            "PUT",
            "/admin/api/users/team",
            {
                "email": probe_user,
                "team_id": created_team_id,
                "expected_team_id": original_team_id,
            },
        )
        require_status("probe user team assignment", response, 200)
        existing_team_dirty = True
        steps["user_team_assign"], _ = summarized_step(response)
        if read_user_team(client, probe_user) != created_team_id:
            raise ValueError("probe user team assignment was not visible")
        membership_after_assignment = read_membership_state(target, probe_user)
        persistence["team_assignment"] = membership_transition(
            "probe user team assignment",
            membership_before_assignment,
            membership_after_assignment,
        )

        response = client.authenticated(
            "PUT",
            "/admin/api/users/team",
            {
                "email": probe_user,
                "team_id": original_team_id,
                "expected_team_id": created_team_id,
            },
        )
        require_status("probe user team restore", response, 200)
        steps["user_team_restore"], _ = summarized_step(response)
        if read_user_team(client, probe_user) != original_team_id:
            raise ValueError("probe user team restore was not visible")
        membership_after_restore = read_membership_state(target, probe_user)
        persistence["team_restore"] = membership_transition(
            "probe user team restore",
            membership_after_assignment,
            membership_after_restore,
        )
        existing_team_dirty = False

        response = client.authenticated(
            "PUT",
            "/admin/api/users/quota",
            {"email": probe_user, "mode": "custom", "weekly_tokens": 0},
        )
        require_status("invalid user quota", response, 400)
        steps["invalid_user_quota"], payload = summarized_step(response)
        if error_code(payload) != "invalid_request":
            raise ValueError("invalid user quota did not preserve invalid_request")

        response = client.authenticated(
            "POST",
            "/admin/api/users",
            {"email": temporary_user, "team_id": created_team_id},
        )
        require_status("temporary user create", response, 201)
        temporary_user_created = True
        steps["user_create"], payload = summarized_step(response)
        credentials = create_credentials(payload, target.surface)
        if credentials["team_id"] != created_team_id:
            raise ValueError("temporary user create response did not preserve its team")
        created_api_key = credentials["api_key"]
        if read_user_team(client, temporary_user) != created_team_id:
            raise ValueError("temporary user was not visible in its requested team")

        if target.surface == "v1":
            rotation_body = {"label": credentials["label"]}
        else:
            rotation_body = {"email": temporary_user, "confirm": "rotate"}
        response = client.authenticated(
            "POST", "/admin/api/keys/rotate", rotation_body
        )
        require_status("temporary user Key rotation", response, 200)
        steps["user_key_rotate"], payload = summarized_step(response)
        replacement_api_key = rotated_key(payload, target.surface)
        if replacement_api_key == created_api_key:
            raise ValueError("temporary user Key rotation returned the original Key")

        password_body = {"email": temporary_user}
        if target.surface == "v2":
            password_body["confirm"] = "reset"
        response = client.authenticated(
            "POST", "/admin/api/users/reset-password", password_body
        )
        require_status("temporary user password reset", response, 200)
        steps["user_password_reset"], payload = summarized_step(response)
        if reset_password(payload, target.surface) != credentials["initial_password"]:
            raise ValueError("password reset did not return the configured initial password")

        revoke_body = {"email": temporary_user}
        if target.surface == "v2":
            revoke_body["confirm"] = "revoke"
        response = client.authenticated(
            "POST", "/admin/api/users/revoke", revoke_body
        )
        require_status("temporary user revoke", response, 200)
        steps["user_revoke"], payload = summarized_step(response)
        revoked_key_count(payload, target.surface)
        revoked_user = read_user(client, temporary_user)
        if revoked_user is None or revoked_user.get("active_keys") != 0:
            raise ValueError("temporary user revoke was not visible in the user catalog")

        response = client.authenticated(
            "POST",
            "/admin/api/users/delete",
            {
                "email": temporary_user,
                "confirm": temporary_user,
                "revoke_keys": True,
            },
        )
        require_status("temporary user delete", response, 200)
        steps["user_delete"], payload = summarized_step(response)
        require_object(payload.get("user"), "temporary user delete result")
        if read_user(client, temporary_user) is not None:
            raise ValueError("temporary user is still visible after deletion")
        persistence["temporary_user_cleanup"] = require_temporary_user_cleanup(
            target.surface,
            temporary_user_cleanup_state(target, temporary_user),
        )
        temporary_user_created = False

        response = client.authenticated(
            "DELETE", "/admin/api/teams?" + urllib.parse.urlencode({"id": created_team_id})
        )
        require_status("team delete", response, 200)
        steps["team_delete"], payload = summarized_step(response)
        deleted = payload.get("team") if isinstance(payload, dict) else None
        if not isinstance(deleted, dict) or deleted.get("deleted") is not True:
            raise ValueError("team delete response lacks deleted=true")
        created_team_id = ""
        team_created = False

        response = client.authenticated("GET", "/admin/api/teams")
        require_status("team deletion readback", response, 200)
        steps["team_delete_readback"], payload = summarized_step(response)
        teams = payload.get("teams") if isinstance(payload, dict) else None
        if not isinstance(teams, list) or any(
            isinstance(item, dict) and item.get("name") in {probe_name, updated_name}
            for item in teams
        ):
            raise ValueError("deleted team is still visible")
    except Exception as error:
        failure = error
    finally:
        if client.cookie and client.csrf and temporary_user_created:
            try:
                if read_user(client, temporary_user) is not None:
                    response = client.authenticated(
                        "POST",
                        "/admin/api/users/delete",
                        {
                            "email": temporary_user,
                            "confirm": temporary_user,
                            "revoke_keys": True,
                        },
                    )
                    require_status("temporary user cleanup", response, 200)
                if read_user(client, temporary_user) is not None:
                    raise ValueError("temporary user cleanup did not remove the user")
                temporary_user_created = False
            except Exception:
                cleanup_errors.append("temporary user cleanup failed")
        if client.cookie and client.csrf and existing_team_dirty:
            try:
                if read_user_team(client, probe_user) != original_team_id:
                    response = client.authenticated(
                        "PUT",
                        "/admin/api/users/team",
                        {"email": probe_user, "team_id": original_team_id},
                    )
                    require_status("probe user team cleanup", response, 200)
                if read_user_team(client, probe_user) != original_team_id:
                    raise ValueError("probe user team cleanup did not restore the team")
                existing_team_dirty = False
            except Exception:
                cleanup_errors.append("probe user team cleanup failed")
        if client.cookie and client.csrf and quota_dirty and original_quota is not None:
            try:
                response = restore_quota_policy(client, probe_user, original_quota)
                require_status("user quota cleanup", response, 200)
                response = client.authenticated(
                    "GET",
                    "/admin/api/users/quota?"
                    + urllib.parse.urlencode({"email": probe_user}),
                )
                require_status("user quota cleanup readback", response, 200)
                if quota_policy(decode_json(response)) != original_quota:
                    raise ValueError("user quota cleanup did not restore the policy")
                quota_dirty = False
            except Exception:
                cleanup_errors.append("user quota cleanup failed")
        if client.cookie and client.csrf and team_created:
            try:
                cleanup_team_id = created_team_id or find_team_id(
                    client, {probe_name, updated_name}
                )
                if cleanup_team_id:
                    response = client.authenticated(
                        "DELETE",
                        "/admin/api/teams?"
                        + urllib.parse.urlencode({"id": cleanup_team_id}),
                    )
                    require_status("temporary team cleanup", response, 200)
                if find_team_id(client, {probe_name, updated_name}):
                    raise ValueError("temporary team cleanup did not remove the team")
                team_created = False
            except Exception:
                cleanup_errors.append("temporary team cleanup failed")
        if client.cookie and client.csrf:
            try:
                response = client.logout()
                require_status("Admin logout", response, 200)
                steps["logout"], _ = summarized_step(response)
            except Exception:
                cleanup_errors.append("Admin logout failed")

    if failure is not None:
        message = "write comparison failed: {}".format(failure)
        if cleanup_errors:
            message += "; " + "; ".join(cleanup_errors)
        raise ValueError(message) from failure
    if cleanup_errors:
        raise ValueError("; ".join(cleanup_errors))

    return {
        "surface": target.surface,
        "base_url": target.base_url,
        "probe_user_sha256": hashlib.sha256(probe_user.encode("utf-8")).hexdigest(),
        "temporary_user_sha256": hashlib.sha256(
            temporary_user.encode("utf-8")
        ).hexdigest(),
        "steps": steps,
        "persistence": persistence,
    }


def comparison_view(result):
    return {
        name: {
            "status": step["status"],
            "content_type": step["content_type"],
            "error_code": step["error_code"],
            "schema": step["schema"],
        }
        for name, step in sorted(result["steps"].items())
    }


def persistence_comparison_view(result):
    view = {}
    for name, state in sorted(result.get("persistence", {}).items()):
        if name in {"team_assignment", "team_restore"}:
            view[name] = {
                "membership_version_delta": state["membership_version_delta"],
                "identity_rows_before": state["identity_rows_before"],
                "identity_rows_after": state["identity_rows_after"],
                "matching_identity_rows_after": state[
                    "matching_identity_rows_after"
                ],
                "all_identities_match": state["all_identities_match"],
            }
        else:
            view[name] = dict(state)
    return view


def compare_views(views_by_surface):
    v1 = views_by_surface["v1"]
    v2 = views_by_surface["v2"]
    approved = []
    unexpected = []
    for name in sorted(set(v1) | set(v2)):
        if v1.get(name) == v2.get(name):
            continue
        decision = WRITE_DECISIONS.get(name)
        if decision and v1.get(name) == decision["v1"] and v2.get(name) == decision["v2"]:
            approved.append({"name": name, "reason": decision["reason"]})
        else:
            unexpected.append({"name": name, "v1": v1.get(name), "v2": v2.get(name)})
    return approved, unexpected


def compare_persistence_views(views_by_surface):
    v1 = views_by_surface["v1"]
    v2 = views_by_surface["v2"]
    approved = []
    unexpected = []
    for name in sorted(set(v1) | set(v2)):
        if v1.get(name) == v2.get(name):
            continue
        decision = PERSISTENCE_DECISIONS.get(name)
        if (
            decision
            and v1.get(name) == decision["v1"]
            and v2.get(name) == decision["v2"]
        ):
            approved.append(
                {"name": "persistence." + name, "reason": decision["reason"]}
            )
        else:
            unexpected.append(
                {
                    "name": "persistence." + name,
                    "v1": v1.get(name),
                    "v2": v2.get(name),
                }
            )
    return approved, unexpected


def run(targets, management_key, timeout):
    if len(targets) != 2 or {target.surface for target in targets} != {"v1", "v2"}:
        raise ValueError("write comparison requires exactly one v1 and one v2 target")
    roots = [validate_target(target) for target in targets]
    databases = [target.control_db.resolve() for target in targets]
    if roots[0] == roots[1] or databases[0] == databases[1]:
        raise ValueError("v1 and v2 must use distinct isolated state copies")
    if os.path.samefile(databases[0], databases[1]):
        raise ValueError("v1 and v2 control databases must not be the same inode")
    usage_databases = [usage_database(target).resolve() for target in targets]
    if usage_databases[0] == usage_databases[1] or os.path.samefile(
        usage_databases[0], usage_databases[1]
    ):
        raise ValueError("v1 and v2 usage databases must not be the same inode")
    suffix = hashlib.sha256(os.urandom(32)).hexdigest()[:12]
    results = {
        target.name: run_target(target, management_key, timeout, suffix)
        for target in targets
    }
    views_by_surface = {
        result["surface"]: comparison_view(result) for result in results.values()
    }
    persistence_by_surface = {
        result["surface"]: persistence_comparison_view(result)
        for result in results.values()
    }
    approved, unexpected = compare_views(views_by_surface)
    persistence_approved, persistence_unexpected = compare_persistence_views(
        persistence_by_surface
    )
    approved.extend(persistence_approved)
    unexpected.extend(persistence_unexpected)
    return {
        "version": 2,
        "targets": results,
        "identical": not approved and not unexpected,
        "compatible": not unexpected,
        "approved_differences": approved,
        "unexpected_differences": unexpected,
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", action="append", type=parse_target, required=True)
    parser.add_argument("--timeout", type=float, default=10.0)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--management-key-stdin", action="store_true", required=True)
    parser.add_argument("--confirm-isolated-write-test", action="store_true", required=True)
    args = parser.parse_args(argv)
    management_key = sys.stdin.readline().strip()
    if not management_key:
        raise SystemExit("management key was not provided on stdin")
    report = run(args.target, management_key, args.timeout)
    rendered = json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.write_text(rendered, encoding="utf-8")
    print(rendered, end="")
    return 0 if report["compatible"] else 1


if __name__ == "__main__":
    sys.exit(main())
