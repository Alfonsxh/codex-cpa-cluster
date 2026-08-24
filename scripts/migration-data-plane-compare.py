#!/usr/bin/env python3
"""Compare authenticated Codex data-plane behavior on isolated v1 and Go v2.

The dedicated Test API Key is accepted only from stdin and is first resolved
against two distinct isolated control databases.  The report contains status,
content type, field names, counts, timings, and one-way catalog/identity
digests; it never contains the Key, user/account identifiers, model names,
response IDs, output payloads, or SSE data.
"""

import argparse
import hashlib
import http.client
import ipaddress
import json
import os
import sqlite3
import sys
import time
import urllib.parse
from collections import Counter
from dataclasses import dataclass
from pathlib import Path


MAX_BODY_BYTES = 8 * 1024 * 1024
TEST_MODEL = "gpt-5.5"
TEST_INPUT = "Reply with exactly OK."
PRODUCTION_ROOTS = {
    Path("/home/AI/CLIProxyAPI"),
    Path("/opt/codex-cpa-cluster"),
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
    if not (root / ".v2-isolated-copy.json").is_file():
        raise ValueError("{} isolated-copy marker is missing".format(target.name))
    if not target.control_db.is_file():
        raise ValueError("{} control database is missing".format(target.name))
    return root


def key_identity(database_path, api_key):
    database = sqlite3.connect(
        "file:{}?mode=ro".format(database_path.resolve()), uri=True
    )
    try:
        rows = database.execute(
            "SELECT lower(trim(user_email)), account_id FROM key_records "
            "WHERE status = 'active' AND secret = ? ORDER BY sequence",
            (api_key,),
        ).fetchall()
    finally:
        database.close()
    users = {str(row[0] or "") for row in rows if str(row[0] or "")}
    accounts = {str(row[1] or "") for row in rows if str(row[1] or "")}
    if len(users) != 1 or not accounts:
        raise ValueError("dedicated Test Key does not resolve to one active isolated user")
    canonical = json.dumps(
        {"user": next(iter(users)), "accounts": sorted(accounts)},
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    return {
        "sha256": hashlib.sha256(canonical).hexdigest(),
        "account_count": len(accounts),
    }


def content_type(response):
    return (response.getheader("Content-Type") or "").split(";", 1)[0].lower()


def schema_summary(value, depth=0):
    if depth >= 4:
        return "..."
    if isinstance(value, dict):
        return {
            "kind": "object",
            "keys": sorted(value),
            "objects": {
                key: schema_summary(nested, depth + 1)
                for key, nested in sorted(value.items())
                if isinstance(nested, dict)
            },
            "arrays": {
                key: schema_summary(nested[0], depth + 1) if nested else "empty"
                for key, nested in sorted(value.items())
                if isinstance(nested, list)
            },
        }
    if isinstance(value, list):
        return {
            "kind": "array",
            "item": schema_summary(value[0], depth + 1) if value else "empty",
        }
    if value is None:
        return "null"
    if isinstance(value, bool):
        return "boolean"
    if isinstance(value, (int, float)):
        return "number"
    if isinstance(value, str):
        return "string"
    return type(value).__name__


def decode_json(body, name):
    try:
        return json.loads(body)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise ValueError("{} response is not valid JSON".format(name)) from error


def extract_output_text(payload):
    pieces = []
    output = payload.get("output") if isinstance(payload, dict) else None
    if not isinstance(output, list):
        return ""
    for item in output:
        if not isinstance(item, dict):
            continue
        content = item.get("content")
        if not isinstance(content, list):
            continue
        for part in content:
            if not isinstance(part, dict):
                continue
            if part.get("type") in {"output_text", "text"} and isinstance(
                part.get("text"), str
            ):
                pieces.append(part["text"])
    return "".join(pieces)


class Client:
    def __init__(self, target, api_key, timeout):
        parsed = urllib.parse.urlsplit(target.base_url)
        self.host = parsed.hostname
        self.port = parsed.port or 80
        self.host_header = parsed.netloc
        self.prefix = parsed.path.rstrip("/")
        self.api_key = api_key
        self.timeout = timeout

    def connect(self):
        return http.client.HTTPConnection(self.host, self.port, timeout=self.timeout)

    def headers(self, accept):
        return {
            "Host": self.host_header,
            "Authorization": "Bearer " + self.api_key,
            "Accept": accept,
            "User-Agent": "codex-cpa-migration-compare/1",
        }

    def json_request(self, method, path, body=None):
        headers = self.headers("application/json")
        raw = None
        if body is not None:
            raw = json.dumps(body, separators=(",", ":")).encode("utf-8")
            headers["Content-Type"] = "application/json"
        connection = self.connect()
        started = time.monotonic()
        connection.request(method, self.prefix + path, body=raw, headers=headers)
        response = connection.getresponse()
        payload = response.read(MAX_BODY_BYTES + 1)
        elapsed_ms = round((time.monotonic() - started) * 1000)
        result = {
            "status": response.status,
            "content_type": content_type(response),
            "body": payload,
            "bytes": len(payload),
            "elapsed_ms": elapsed_ms,
        }
        connection.close()
        if len(payload) > MAX_BODY_BYTES:
            raise ValueError("{} response exceeded 8 MiB".format(path))
        return result

    def stream_response(self):
        headers = self.headers("text/event-stream")
        headers["Content-Type"] = "application/json"
        request = json.dumps(
            {"model": TEST_MODEL, "input": TEST_INPUT, "stream": True},
            separators=(",", ":"),
        ).encode("utf-8")
        connection = self.connect()
        started = time.monotonic()
        connection.request(
            "POST", self.prefix + "/v1/responses", body=request, headers=headers
        )
        response = connection.getresponse()
        first_event_ms = None
        total_bytes = 0
        event_name = ""
        data_lines = []
        data_events = 0
        done_markers = 0
        event_types = []
        text_deltas = []
        completed = False
        error_events = 0

        def consume_event():
            nonlocal first_event_ms, data_events, done_markers, completed, error_events
            if not data_lines:
                return
            data = "\n".join(data_lines)
            data_events += 1
            if first_event_ms is None:
                first_event_ms = round((time.monotonic() - started) * 1000)
            if data.strip() == "[DONE]":
                done_markers += 1
                return
            try:
                payload = json.loads(data)
            except json.JSONDecodeError as error:
                raise ValueError("SSE data event is not valid JSON") from error
            payload_type = str(payload.get("type") or event_name or "message")
            event_types.append(payload_type)
            if payload_type == "response.output_text.delta" and isinstance(
                payload.get("delta"), str
            ):
                text_deltas.append(payload["delta"])
            if payload_type == "response.completed":
                completed = True
            if "error" in payload_type or payload_type in {
                "response.failed",
                "response.incomplete",
            }:
                error_events += 1

        try:
            while True:
                raw_line = response.readline()
                if not raw_line:
                    consume_event()
                    break
                total_bytes += len(raw_line)
                if total_bytes > MAX_BODY_BYTES:
                    raise ValueError("SSE response exceeded 8 MiB")
                line = raw_line.decode("utf-8", errors="strict").rstrip("\r\n")
                if not line:
                    consume_event()
                    event_name = ""
                    data_lines = []
                elif line.startswith("event:"):
                    event_name = line[6:].strip()
                elif line.startswith("data:"):
                    data_lines.append(line[5:].lstrip())
        finally:
            elapsed_ms = round((time.monotonic() - started) * 1000)
            status = response.status
            response_type = content_type(response)
            connection.close()
        counts = Counter(event_types)
        return {
            "status": status,
            "content_type": response_type,
            "bytes": total_bytes,
            "elapsed_ms": elapsed_ms,
            "first_event_ms": first_event_ms,
            "data_events": data_events,
            "done_markers": done_markers,
            "event_types": sorted(counts),
            "event_type_counts": {name: counts[name] for name in sorted(counts)},
            "completed": completed,
            "error_events": error_events,
            "text_exact": "".join(text_deltas).strip() == "OK",
        }


def probe_models(client):
    response = client.json_request("GET", "/v1/models")
    payload = decode_json(response["body"], "models")
    data = payload.get("data") if isinstance(payload, dict) else None
    identifiers = {
        str(item.get("id"))
        for item in data or []
        if isinstance(item, dict) and isinstance(item.get("id"), str)
    }
    catalog_digest = hashlib.sha256(
        json.dumps(sorted(identifiers), separators=(",", ":")).encode("utf-8")
    ).hexdigest()
    result = {
        key: response[key]
        for key in ("status", "content_type", "bytes", "elapsed_ms")
    }
    result.update(
        {
            "schema": schema_summary(payload),
            "model_count": len(data) if isinstance(data, list) else 0,
            "identified_model_count": len(identifiers),
            "catalog_sha256": catalog_digest,
        }
    )
    result["passed"] = (
        result["status"] == 200
        and result["content_type"] == "application/json"
        and result["model_count"] > 0
        and result["identified_model_count"] == result["model_count"]
    )
    return result


def probe_response(client):
    response = client.json_request(
        "POST",
        "/v1/responses",
        {"model": TEST_MODEL, "input": TEST_INPUT, "stream": False},
    )
    payload = decode_json(response["body"], "response")
    result = {
        key: response[key]
        for key in ("status", "content_type", "bytes", "elapsed_ms")
    }
    result.update(
        {
            "schema": schema_summary(payload),
            "response_completed": (
                isinstance(payload, dict) and payload.get("status") == "completed"
            ),
            "text_exact": extract_output_text(payload).strip() == "OK",
        }
    )
    result["passed"] = (
        result["status"] == 200
        and result["content_type"] == "application/json"
        and result["response_completed"]
        and result["text_exact"]
    )
    return result


def probe_stream(client):
    result = client.stream_response()
    result["passed"] = (
        result["status"] == 200
        and result["content_type"] == "text/event-stream"
        and result["data_events"] > 0
        and result["completed"]
        and result["error_events"] == 0
        and result["text_exact"]
        and result["first_event_ms"] is not None
    )
    return result


PROBES = (
    ("models", probe_models),
    ("responses", probe_response),
    ("responses_sse", probe_stream),
)


def run(targets, api_key, timeout):
    if len(targets) != 2 or {target.surface for target in targets} != {"v1", "v2"}:
        raise ValueError("data-plane comparison requires exactly one v1 and one v2 target")
    roots = [validate_target(target) for target in targets]
    databases = [target.control_db.resolve() for target in targets]
    if roots[0] == roots[1] or databases[0] == databases[1]:
        raise ValueError("v1 and v2 must use distinct isolated state copies")
    if os.path.samefile(databases[0], databases[1]):
        raise ValueError("v1 and v2 control databases must not be the same inode")
    identities = [key_identity(target.control_db, api_key) for target in targets]
    if identities[0] != identities[1]:
        raise ValueError("dedicated Test Key identity differs across isolated copies")

    report = {
        "version": 1,
        "test_identity": identities[0],
        "targets": {
            target.name: {
                "surface": target.surface,
                "base_url": target.base_url,
                "probes": {},
            }
            for target in targets
        },
        "unexpected_differences": [],
    }
    clients = {target.name: Client(target, api_key, timeout) for target in targets}
    for probe_name, probe in PROBES:
        for target in targets:
            try:
                result = probe(clients[target.name])
            except Exception as error:
                result = {"passed": False, "transport_error": type(error).__name__}
            report["targets"][target.name]["probes"][probe_name] = result

    by_surface = {
        target.surface: report["targets"][target.name] for target in targets
    }
    for probe_name, _ in PROBES:
        left = by_surface["v1"]["probes"][probe_name]
        right = by_surface["v2"]["probes"][probe_name]
        if not left.get("passed") or not right.get("passed"):
            report["unexpected_differences"].append(
                {"probe": probe_name, "reason": "target_probe_failed"}
            )
            continue
        if probe_name == "models":
            fields = ("schema", "model_count", "catalog_sha256")
        elif probe_name == "responses":
            fields = ("schema", "response_completed", "text_exact")
        else:
            fields = ("event_types", "completed", "text_exact")
        mismatched = [field for field in fields if left.get(field) != right.get(field)]
        if mismatched:
            report["unexpected_differences"].append(
                {
                    "probe": probe_name,
                    "reason": "semantic_mismatch",
                    "fields": mismatched,
                }
            )
    report["compatible"] = not report["unexpected_differences"]
    return report


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", action="append", type=parse_target, required=True)
    parser.add_argument("--timeout", type=float, default=120.0)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--api-key-stdin", action="store_true", required=True)
    parser.add_argument(
        "--confirm-test-data-request", action="store_true", required=True
    )
    parser.add_argument("--summary", action="store_true")
    args = parser.parse_args(argv)
    api_key = sys.stdin.readline().strip()
    if not api_key or len(api_key) > 4096:
        raise SystemExit("dedicated Test API Key was not provided on stdin")
    report = run(args.target, api_key, args.timeout)
    rendered = json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.write_text(rendered, encoding="utf-8")
    if args.summary:
        summary = {
            "version": report["version"],
            "compatible": report["compatible"],
            "unexpected_differences": report["unexpected_differences"],
            "targets": {
                name: {
                    "surface": target["surface"],
                    "probes": {
                        probe: {
                            key: value
                            for key, value in result.items()
                            if key
                            in {
                                "passed",
                                "status",
                                "content_type",
                                "model_count",
                                "response_completed",
                                "text_exact",
                                "completed",
                                "error_events",
                                "data_events",
                                "first_event_ms",
                                "elapsed_ms",
                                "transport_error",
                            }
                        }
                        for probe, result in target["probes"].items()
                    },
                }
                for name, target in report["targets"].items()
            },
        }
        print(json.dumps(summary, ensure_ascii=False, indent=2, sort_keys=True))
    else:
        print(rendered, end="")
    return 0 if report["compatible"] else 1


if __name__ == "__main__":
    sys.exit(main())
