#!/usr/bin/env python3
"""Compare secret-free data-plane error contracts on isolated v1 and Go v2.

The dedicated Test API Key is accepted only from stdin. Reports retain status,
content type, selected protocol headers, body size and the structured error
code; they never retain credentials, response messages or response bodies.
"""

import argparse
import http.client
import ipaddress
import json
import sys
import urllib.parse
from dataclasses import dataclass
from pathlib import Path


MAX_BODY_BYTES = 1024 * 1024
INVALID_KEY = "migration-intentionally-invalid-key"
UNKNOWN_PATH = "/__migration/unknown"
MODE_CONTRACTS = {
    "baseline": (200, ""),
    "quota-exceeded": (429, "weekly_user_token_quota_exceeded"),
    "upstream-unavailable": (502, "upstream_unavailable"),
    "auth-unavailable": (503, "authentication_snapshot_unavailable"),
}


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
    if parsed.scheme != "http" or not parsed.hostname or parsed.query or parsed.fragment:
        raise argparse.ArgumentTypeError("target must be an absolute HTTP URL")
    if parsed.hostname != "localhost":
        try:
            address = ipaddress.ip_address(parsed.hostname)
        except ValueError as error:
            raise argparse.ArgumentTypeError("target must use localhost or a private IP") from error
        if not (address.is_private or address.is_loopback):
            raise argparse.ArgumentTypeError("target must use localhost or a private IP")
    return Target(name=name, surface=surface, base_url=base_url.rstrip("/"))


class Client:
    def __init__(self, target, api_key, timeout):
        self.target = target
        self.api_key = api_key
        self.timeout = timeout
        self.parsed = urllib.parse.urlsplit(target.base_url)

    def request(self, path, authorization):
        connection = http.client.HTTPConnection(
            self.parsed.hostname,
            self.parsed.port or 80,
            timeout=self.timeout,
        )
        headers = {
            "Accept": "application/json",
            "Host": self.parsed.netloc,
            "User-Agent": "codex-cpa-migration-fault-compare/1",
        }
        if authorization:
            headers["Authorization"] = authorization
        prefix = self.parsed.path.rstrip("/")
        connection.request("GET", prefix + path, headers=headers)
        response = connection.getresponse()
        body = response.read(MAX_BODY_BYTES + 1)
        connection.close()
        if len(body) > MAX_BODY_BYTES:
            raise ValueError("fault response exceeds 1 MiB")
        content_type = (response.getheader("Content-Type") or "").split(";", 1)[0].lower()
        error_code = ""
        quota_fields = []
        quota_unit = ""
        quota_exceeded = False
        if content_type == "application/json" and body:
            try:
                payload = json.loads(body)
            except (UnicodeDecodeError, json.JSONDecodeError) as error:
                raise ValueError("fault response is invalid JSON") from error
            if isinstance(payload, dict) and isinstance(payload.get("error"), dict):
                value = payload["error"].get("code")
                if isinstance(value, str):
                    error_code = value
            quota = payload.get("user_weekly_quota") if isinstance(payload, dict) else None
            if isinstance(quota, dict):
                quota_fields = sorted(str(key) for key in quota)
                quota_unit = str(quota.get("quota_unit") or "")
                limit = quota.get("limit_tokens")
                used = quota.get("used_tokens")
                quota_exceeded = (
                    isinstance(limit, (int, float))
                    and not isinstance(limit, bool)
                    and isinstance(used, (int, float))
                    and not isinstance(used, bool)
                    and limit >= 0
                    and used >= limit
                )
        return {
            "status": response.status,
            "content_type": content_type,
            "error_code": error_code,
            "retry_after": response.getheader("Retry-After") or "",
            "quota_fields": quota_fields,
            "quota_unit": quota_unit,
            "quota_exceeded": quota_exceeded,
            "bytes": len(body),
        }

    def probes(self, mode):
        expected_status, expected_code = MODE_CONTRACTS[mode]
        if mode == "upstream-unavailable" and self.target.surface == "v1":
            expected_code = ""
        auth_status = 503 if mode == "auth-unavailable" else 401
        auth_code = expected_code if mode == "auth-unavailable" else ""
        definitions = (
            ("missing_key", "/v1/models", "", auth_status, auth_code),
            (
                "invalid_key",
                "/v1/models",
                "Bearer " + INVALID_KEY,
                auth_status,
                auth_code,
            ),
            ("unknown_path", UNKNOWN_PATH, "Bearer " + self.api_key, 404, ""),
            (
                "mode_contract",
                "/v1/models",
                "Bearer " + self.api_key,
                expected_status,
                expected_code,
            ),
        )
        results = {}
        for name, path, authorization, status, error_code in definitions:
            try:
                result = self.request(path, authorization)
            except Exception as error:
                legacy_timeout = (
                    self.target.surface == "v1"
                    and mode == "upstream-unavailable"
                    and name == "mode_contract"
                    and type(error).__name__ in {"timeout", "TimeoutError"}
                )
                results[name] = {
                    "transport_error": type(error).__name__,
                    "expected_status": status,
                    "expected_error_code": error_code,
                    "passed": legacy_timeout,
                }
                continue
            result["expected_status"] = status
            result["expected_error_code"] = error_code
            retry_valid = True
            quota_valid = True
            if status == 429:
                retry_valid = result["retry_after"].isdigit() and int(result["retry_after"]) > 0
                quota_valid = (
                    result["quota_exceeded"]
                    and result["quota_unit"] == "weighted_tokens"
                    and set(result["quota_fields"])
                    >= {"limit_tokens", "used_tokens", "week_end_at", "quota_unit"}
                )
            result["passed"] = (
                result["status"] == status
                and (not error_code or result["error_code"] == error_code)
                and retry_valid
                and quota_valid
            )
            results[name] = result
        return results


def run(targets, api_key, mode, timeout):
    if len(targets) != 2 or {target.surface for target in targets} != {"v1", "v2"}:
        raise ValueError("fault comparison requires exactly one v1 and one v2 target")
    target_reports = {}
    failures = []
    approved_differences = []
    for target in targets:
        try:
            probes = Client(target, api_key, timeout).probes(mode)
        except Exception as error:
            failures.append({"target": target.name, "reason": type(error).__name__})
            target_reports[target.name] = {
                "surface": target.surface,
                "transport_error": type(error).__name__,
            }
            continue
        target_reports[target.name] = {"surface": target.surface, "probes": probes}
        for name, result in probes.items():
            if not result["passed"]:
                failures.append(
                    {
                        "target": target.name,
                        "probe": name,
                        "status": result.get("status"),
                        "error_code": result.get("error_code", ""),
                        "transport_error": result.get("transport_error", ""),
                    }
                )
    if not failures:
        first, second = (target_reports[target.name]["probes"] for target in targets)
        for name in first:
            first_error = first[name].get("transport_error", "")
            second_error = second[name].get("transport_error", "")
            if first_error or second_error:
                by_surface = {
                    target.surface: target_reports[target.name]["probes"][name]
                    for target in targets
                }
                approved_timeout = (
                    mode == "upstream-unavailable"
                    and name == "mode_contract"
                    and by_surface["v1"].get("transport_error") in {"timeout", "TimeoutError"}
                    and by_surface["v2"].get("passed") is True
                )
                if not approved_timeout:
                    failures.append({"probe": name, "reason": "transport_mismatch"})
                continue
            if first[name]["status"] != second[name]["status"]:
                failures.append({"probe": name, "reason": "status_mismatch"})
            if (
                name == "mode_contract"
                and mode != "upstream-unavailable"
                and first[name]["error_code"] != second[name]["error_code"]
            ):
                failures.append({"probe": name, "reason": "error_code_mismatch"})
            if name == "mode_contract" and mode == "quota-exceeded":
                if first[name]["quota_fields"] != second[name]["quota_fields"]:
                    failures.append({"probe": name, "reason": "quota_shape_mismatch"})
                if first[name]["quota_unit"] != second[name]["quota_unit"]:
                    failures.append({"probe": name, "reason": "quota_unit_mismatch"})
        if mode == "upstream-unavailable":
            by_surface = {
                target.surface: target_reports[target.name]["probes"]["mode_contract"]
                for target in targets
            }
            if (
                by_surface["v1"].get("status") == 502
                and by_surface["v1"].get("error_code", "") == ""
                and by_surface["v2"]["error_code"] == "upstream_unavailable"
            ):
                approved_differences.append(
                    {
                        "probe": "mode_contract",
                        "reason": "Go v2 replaces the legacy Nginx 502 HTML body with a structured upstream_unavailable JSON error.",
                    }
                )
            elif (
                by_surface["v1"].get("transport_error") in {"timeout", "TimeoutError"}
                and by_surface["v2"].get("error_code") == "upstream_unavailable"
            ):
                approved_differences.append(
                    {
                        "probe": "mode_contract",
                        "reason": "The legacy v1 upstream connect path exceeded the 15-second comparison bound while Go v2 returned a bounded structured upstream_unavailable error.",
                    }
                )
    return {
        "version": 1,
        "mode": mode,
        "compatible": not failures,
        "targets": target_reports,
        "failures": failures,
        "approved_differences": approved_differences,
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", action="append", required=True, type=parse_target)
    parser.add_argument("--mode", choices=sorted(MODE_CONTRACTS), required=True)
    parser.add_argument("--timeout", type=float, default=10.0)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--api-key-stdin", action="store_true", required=True)
    args = parser.parse_args(argv)
    api_key = sys.stdin.readline().strip()
    if len(api_key) < 16:
        raise SystemExit("dedicated Test API Key is unavailable")
    report = run(args.target, api_key, args.mode, args.timeout)
    rendered = json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.write_text(rendered, encoding="utf-8")
    print(rendered, end="")
    api_key = ""
    return 0 if report["compatible"] else 1


if __name__ == "__main__":
    sys.exit(main())
