#!/usr/bin/env python3
"""Probe every v1 or Go v2 HTTP operation without credentials.

The probe is deliberately non-mutating: protected operations must reject the
request before their handler sees the empty JSON body. Public operations have
an explicit status contract so a missing route cannot be mistaken for parity.
Response bodies are represented only by size, SHA-256 and JSON shape; secrets
or error text are never printed.
"""

import argparse
import concurrent.futures
import hashlib
import importlib.util
import json
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Tuple


ROOT = Path(__file__).resolve().parents[1]
OPENAPI_PATH = ROOT / "api" / "openapi.yaml"
ROUTE_MATRIX_PATH = ROOT / "scripts" / "migration-route-matrix.py"
HTTP_METHODS = {"get", "post", "put", "delete", "patch"}


@dataclass(frozen=True)
class Operation:
    method: str
    path: str
    security: Tuple[str, ...] = ()
    csrf: bool = False

    @property
    def key(self):
        return "{} {}".format(self.method, self.path)


@dataclass(frozen=True)
class Target:
    name: str
    surface: str
    base_url: str


def parse_openapi_operations(path=OPENAPI_PATH):
    """Read the small paths/security subset without adding a YAML dependency."""

    operations = []
    in_paths = False
    current_path = ""
    current_method = ""
    current_security = []
    current_csrf = False
    security_block = False

    def flush():
        nonlocal current_method, current_security, current_csrf, security_block
        if current_path and current_method:
            operations.append(
                Operation(
                    method=current_method.upper(),
                    path=re.sub(r"\{[^}]+\}", "probe", current_path),
                    security=tuple(current_security),
                    csrf=current_csrf,
                )
            )
        current_method = ""
        current_security = []
        current_csrf = False
        security_block = False

    for raw_line in path.read_text(encoding="utf-8").splitlines():
        if raw_line == "paths:":
            in_paths = True
            continue
        if not in_paths:
            continue
        if raw_line and not raw_line.startswith(" "):
            flush()
            break
        path_match = re.match(r"^  (/[^:]+):\s*$", raw_line)
        if path_match:
            flush()
            current_path = path_match.group(1)
            continue
        method_match = re.match(r"^    (get|post|put|delete|patch):\s*$", raw_line)
        if method_match:
            flush()
            current_method = method_match.group(1)
            continue
        if not current_method:
            continue
        if raw_line.strip() == '- $ref: "#/components/parameters/CsrfToken"':
            current_csrf = True
            continue
        security_match = re.match(r"^      security:\s*(\[\])?\s*$", raw_line)
        if security_match:
            current_security = []
            security_block = security_match.group(1) is None
            continue
        if security_block:
            scheme_match = re.match(r"^        - ([A-Za-z][A-Za-z0-9_]*):\s*\[\]\s*$", raw_line)
            if scheme_match:
                current_security.append(scheme_match.group(1))
                continue
            if raw_line.strip() and len(raw_line) - len(raw_line.lstrip()) <= 6:
                security_block = False
    else:
        flush()
    return operations


def load_route_matrix_module():
    spec = importlib.util.spec_from_file_location("migration_route_matrix", ROUTE_MATRIX_PATH)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def v1_operations():
    routes = load_route_matrix_module().extract_v1_routes()
    return [
        Operation(method=route.split(" ", 1)[0], path=route.split(" ", 1)[1])
        for route in sorted(routes, key=lambda item: (item.split(" ", 1)[1], item))
    ]


PUBLIC_EXPECTATIONS = {
    "GET /healthz": {200},
    "GET /site-config.json": {200},
    "POST /admin/api/session": {401},
    "GET /branding/logo": {200, 404},
    "GET /usage/api": {200},
    "GET /usage/limits": {200},
    "POST /usage/session": {400, 401},
    "DELETE /usage/session": {200},
    "POST /my-keys/api": {410},
}


def expected_statuses(surface, operation):
    if operation.key in PUBLIC_EXPECTATIONS:
        return PUBLIC_EXPECTATIONS[operation.key]
    if surface == "v2":
        if not operation.security:
            raise ValueError("anonymous v2 operation lacks an explicit expectation: {}".format(operation.key))
        return {401}
    if surface == "v1":
        if operation.path.startswith("/admin/api/") or operation.path.startswith("/usage/me"):
            return {401}
        raise ValueError("anonymous v1 operation lacks an explicit expectation: {}".format(operation.key))
    raise ValueError("unsupported surface: {}".format(surface))


def parse_target(value):
    parts = value.split(",", 2)
    if len(parts) != 3:
        raise argparse.ArgumentTypeError("target must be NAME,SURFACE,BASE_URL")
    name, surface, base_url = (part.strip() for part in parts)
    if not name or surface not in {"v1", "v2"}:
        raise argparse.ArgumentTypeError("target surface must be v1 or v2")
    parsed = urllib.parse.urlsplit(base_url)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc or parsed.query or parsed.fragment:
        raise argparse.ArgumentTypeError("target BASE_URL must be an absolute HTTP(S) URL")
    return Target(name=name, surface=surface, base_url=base_url.rstrip("/"))


def json_shape(value, depth=0):
    if depth >= 5:
        return "..."
    if isinstance(value, dict):
        return {key: json_shape(value[key], depth + 1) for key in sorted(value)}
    if isinstance(value, list):
        return [json_shape(value[0], depth + 1)] if value else []
    if value is None:
        return "null"
    if isinstance(value, bool):
        return "boolean"
    if isinstance(value, (int, float)):
        return "number"
    if isinstance(value, str):
        return "string"
    return type(value).__name__


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, file_pointer, code, message, headers, new_url):
        return None


def probe(base_url, operation, timeout):
    url = base_url + operation.path
    data = None
    headers = {"Accept": "application/json"}
    if operation.method in {"POST", "PUT", "PATCH"}:
        data = b"{}"
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(url, data=data, headers=headers, method=operation.method)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    try:
        response = opener.open(request, timeout=timeout)
    except urllib.error.HTTPError as error:
        response = error
    body = response.read(1024 * 1024 + 1)
    if len(body) > 1024 * 1024:
        raise ValueError("response exceeds 1 MiB: {}".format(operation.key))
    content_type = response.headers.get_content_type()
    shape = None
    if content_type == "application/json" and body:
        try:
            shape = json_shape(json.loads(body))
        except json.JSONDecodeError:
            shape = "invalid-json"
    return {
        "status": int(response.status),
        "content_type": content_type,
        "bytes": len(body),
        "sha256": hashlib.sha256(body).hexdigest(),
        "json_shape": shape,
    }


def operations_for(surface):
    return parse_openapi_operations() if surface == "v2" else v1_operations()


def probe_operation(target, operation, timeout):
    expected = expected_statuses(target.surface, operation)
    try:
        result = probe(target.base_url, operation, timeout)
        actual = result["status"]
        return {
            "operation": operation.key,
            "expected": sorted(expected),
            "passed": actual in expected,
            **result,
        }
    except Exception as error:  # Keep the report complete after a transport error.
        return {
            "operation": operation.key,
            "expected": sorted(expected),
            "passed": False,
            "transport_error": type(error).__name__,
        }


def run(targets, timeout, workers=12):
    report = {"version": 1, "targets": {}, "failures": []}
    for target in targets:
        operations = operations_for(target.surface)
        rows = [None] * len(operations)
        with concurrent.futures.ThreadPoolExecutor(
            max_workers=min(max(1, workers), len(operations))
        ) as executor:
            futures = {
                executor.submit(probe_operation, target, operation, timeout): index
                for index, operation in enumerate(operations)
            }
            for future in concurrent.futures.as_completed(futures):
                rows[futures[future]] = future.result()
        for row in rows:
            if not row["passed"]:
                report["failures"].append({"target": target.name, **row})
        report["targets"][target.name] = {
            "surface": target.surface,
            "base_url": target.base_url,
            "passed": sum(row["passed"] for row in rows),
            "total": len(rows),
            "results": rows,
        }
    return report


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--target",
        action="append",
        required=True,
        type=parse_target,
        metavar="NAME,SURFACE,BASE_URL",
    )
    parser.add_argument("--timeout", type=float, default=5.0)
    parser.add_argument("--workers", type=int, default=12)
    parser.add_argument("--output", type=Path)
    parser.add_argument(
        "--summary",
        action="store_true",
        help="print only target totals and failures; --output still receives the full report",
    )
    args = parser.parse_args(argv)
    report = run(args.target, args.timeout, workers=args.workers)
    rendered = json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.write_text(rendered, encoding="utf-8")
    if args.summary:
        summary = {
            "version": report["version"],
            "targets": {
                name: {
                    key: value
                    for key, value in target.items()
                    if key in {"surface", "base_url", "passed", "total"}
                }
                for name, target in report["targets"].items()
            },
            "failures": report["failures"],
        }
        print(json.dumps(summary, ensure_ascii=False, indent=2, sort_keys=True))
    else:
        print(rendered, end="")
    return 1 if report["failures"] else 0


if __name__ == "__main__":
    sys.exit(main())
