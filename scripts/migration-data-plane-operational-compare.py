#!/usr/bin/env python3
"""Compare live cancellation, concurrency, latency and chunked-body behavior.

The suite is restricted to isolated private-IP targets backed by the disposable
test upstream.  The dedicated API Key is accepted only from stdin.  Evidence
contains timings, status counts and anonymous counters; it never contains a
credential, user/account identity, request/response body or fixture output.
"""

import argparse
import concurrent.futures
import http.client
import ipaddress
import json
import math
import select
import socket
import statistics
import sys
import time
import urllib.parse
from collections import Counter
from dataclasses import dataclass
from pathlib import Path


DEFAULT_MAX_BODY_BYTES = 100 * 1024 * 1024
MAX_RESPONSE_BYTES = 1024 * 1024
TEST_MODEL = "gpt-5.5"


@dataclass(frozen=True)
class Target:
    name: str
    surface: str
    public_url: str
    internal_url: str


def private_http_url(value, label):
    parsed = urllib.parse.urlsplit(value)
    if parsed.scheme != "http" or not parsed.hostname or parsed.query or parsed.fragment:
        raise argparse.ArgumentTypeError("{} must be an absolute HTTP URL".format(label))
    if parsed.hostname != "localhost":
        try:
            address = ipaddress.ip_address(parsed.hostname)
        except ValueError as error:
            raise argparse.ArgumentTypeError(
                "{} must use localhost or a private IP".format(label)
            ) from error
        if not (address.is_private or address.is_loopback):
            raise argparse.ArgumentTypeError(
                "{} must use localhost or a private IP".format(label)
            )
    return parsed


def parse_target(value):
    parts = value.split(",", 3)
    if len(parts) != 4:
        raise argparse.ArgumentTypeError(
            "target must be NAME,SURFACE,PUBLIC_URL,INTERNAL_URL"
        )
    name, surface, public_url, internal_url = (part.strip() for part in parts)
    if not name or surface not in {"v1", "v2"}:
        raise argparse.ArgumentTypeError("target surface must be v1 or v2")
    private_http_url(public_url, "public URL")
    private_http_url(internal_url, "internal URL")
    return Target(name, surface, public_url.rstrip("/"), internal_url.rstrip("/"))


def json_body(response, label):
    body = response.read(MAX_RESPONSE_BYTES + 1)
    if len(body) > MAX_RESPONSE_BYTES:
        raise ValueError("{} response exceeds 1 MiB".format(label))
    try:
        payload = json.loads(body)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise ValueError("{} response is invalid JSON".format(label)) from error
    if not isinstance(payload, dict):
        raise ValueError("{} response must be an object".format(label))
    return payload


class Client:
    def __init__(self, target, api_key, timeout):
        self.target = target
        self.api_key = api_key
        self.timeout = timeout
        self.public = urllib.parse.urlsplit(target.public_url)
        self.internal = urllib.parse.urlsplit(target.internal_url)

    @staticmethod
    def connection(parsed, timeout):
        return http.client.HTTPConnection(parsed.hostname, parsed.port or 80, timeout=timeout)

    def public_headers(self, accept="application/json"):
        return {
            "Accept": accept,
            "Authorization": "Bearer " + self.api_key,
            "Host": self.public.netloc,
            "User-Agent": "codex-cpa-migration-operational-compare/1",
        }

    def fixture_json(self, method, path, payload=None):
        headers = self.public_headers()
        body = None
        if payload is not None:
            body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
            headers["Content-Type"] = "application/json"
        connection = self.connection(self.public, self.timeout)
        try:
            connection.request(method, self.public.path.rstrip("/") + path, body, headers)
            response = connection.getresponse()
            status = response.status
            decoded = json_body(response, path)
            return status, decoded
        finally:
            connection.close()

    def reset_fixture(self):
        status, payload = self.fixture_json("POST", "/v1/fixture/reset")
        if status != 200 or any(int(payload.get(field, -1)) != 0 for field in (
            "active", "started", "completed", "canceled", "max_active"
        )):
            raise ValueError("fixture reset failed")

    def fixture_stats(self):
        status, payload = self.fixture_json("GET", "/v1/fixture/stats")
        if status != 200:
            raise ValueError("fixture stats failed")
        return {
            field: int(payload.get(field, -1))
            for field in ("active", "started", "completed", "canceled", "max_active")
        }

    def internal_inflight(self):
        connection = self.connection(self.internal, self.timeout)
        try:
            connection.request(
                "GET",
                self.internal.path.rstrip("/") + "/__stats",
                headers={
                    "Accept": "application/json",
                    "Host": self.internal.netloc,
                    "User-Agent": "codex-cpa-migration-operational-compare/1",
                },
            )
            response = connection.getresponse()
            body = response.read(MAX_RESPONSE_BYTES + 1)
            if response.status != 200 or len(body) > MAX_RESPONSE_BYTES:
                raise ValueError("internal inflight probe failed")
            payload = json.loads(body)
            if not isinstance(payload, list):
                raise ValueError("internal inflight response is invalid")
            return sum(
                max(0, int(item.get("inflight", 0)))
                for item in payload
                if isinstance(item, dict)
            )
        finally:
            connection.close()

    def cancel_stream(self):
        self.reset_fixture()
        before = self.internal_inflight()
        headers = self.public_headers("text/event-stream")
        headers["Content-Type"] = "application/json"
        body = json.dumps(
            {
                "model": TEST_MODEL,
                "input": "Cancellation propagation fixture.",
                "stream": True,
                "fixture_delay_ms": 10000,
            },
            separators=(",", ":"),
        ).encode("utf-8")
        connection = self.connection(self.public, self.timeout)
        started = time.monotonic()
        response_status = 0
        first_event_ms = None
        inflight_during = -1
        try:
            connection.request(
                "POST", self.public.path.rstrip("/") + "/v1/responses", body, headers
            )
            response = connection.getresponse()
            response_status = response.status
            while True:
                line = response.readline()
                if not line:
                    break
                if line.strip():
                    first_event_ms = round((time.monotonic() - started) * 1000)
                    break
            inflight_during = self.internal_inflight()
        finally:
            connection.close()

        deadline = time.monotonic() + 8
        stats = {}
        inflight_after = -1
        while time.monotonic() < deadline:
            stats = self.fixture_stats()
            inflight_after = self.internal_inflight()
            if stats["active"] == 0 and stats["canceled"] >= 1 and inflight_after == 0:
                break
            time.sleep(0.05)
        passed = (
            before == 0
            and response_status == 200
            and first_event_ms is not None
            and inflight_during >= 1
            and stats.get("active") == 0
            and stats.get("started") == 1
            and stats.get("canceled") == 1
            and inflight_after == 0
        )
        return {
            "status": response_status,
            "first_event_ms": first_event_ms,
            "inflight_before": before,
            "inflight_during": inflight_during,
            "inflight_after": inflight_after,
            "fixture": stats,
            "passed": passed,
        }

    def one_delayed_response(self, delay_ms):
        started = time.monotonic()
        status, payload = self.fixture_json(
            "POST",
            "/v1/responses",
            {
                "model": TEST_MODEL,
                "input": "Concurrency fixture.",
                "stream": False,
                "fixture_delay_ms": delay_ms,
            },
        )
        elapsed_ms = round((time.monotonic() - started) * 1000)
        exact = False
        if isinstance(payload.get("output"), list):
            pieces = []
            for item in payload["output"]:
                for part in item.get("content", []) if isinstance(item, dict) else []:
                    if isinstance(part, dict) and isinstance(part.get("text"), str):
                        pieces.append(part["text"])
            exact = "".join(pieces).strip() == "OK"
        return status, elapsed_ms, exact

    def concurrent_probe(self, requests, workers, delay_ms):
        self.reset_fixture()
        started = time.monotonic()
        with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as executor:
            futures = [executor.submit(self.one_delayed_response, delay_ms) for _ in range(requests)]
            results = [future.result(timeout=self.timeout + 5) for future in futures]
        wall_ms = round((time.monotonic() - started) * 1000)
        stats = self.fixture_stats()
        inflight_after = self.internal_inflight()
        statuses = Counter(status for status, _, _ in results)
        latencies = sorted(elapsed for _, elapsed, _ in results)
        p95_index = min(len(latencies) - 1, max(0, math.ceil(len(latencies) * 0.95) - 1))
        passed = (
            statuses == {200: requests}
            and all(exact for _, _, exact in results)
            and stats["active"] == 0
            and stats["started"] == requests
            and stats["completed"] == requests
            and stats["canceled"] == 0
            and stats["max_active"] >= min(2, requests)
            and inflight_after == 0
        )
        return {
            "requests": requests,
            "workers": workers,
            "delay_ms": delay_ms,
            "status_counts": {str(key): statuses[key] for key in sorted(statuses)},
            "wall_ms": wall_ms,
            "latency_ms": {
                "min": min(latencies),
                "median": round(statistics.median(latencies)),
                "p95": latencies[p95_index],
                "max": max(latencies),
            },
            "fixture": stats,
            "inflight_after": inflight_after,
            "passed": passed,
        }

    def chunked_limit_probe(self, max_body_bytes):
        self.reset_fixture()
        sock = socket.create_connection(
            (self.public.hostname, self.public.port or 80), timeout=self.timeout
        )
        sock.settimeout(self.timeout)
        path = self.public.path.rstrip("/") + "/v1/responses"
        request_head = (
            "POST {} HTTP/1.1\r\n"
            "Host: {}\r\n"
            "Authorization: Bearer {}\r\n"
            "Content-Type: application/octet-stream\r\n"
            "Transfer-Encoding: chunked\r\n"
            "X-Codex-CPA-Fixture-Drain-Body: 1\r\n"
            "Connection: close\r\n"
            "User-Agent: codex-cpa-migration-operational-compare/1\r\n\r\n"
        ).format(path, self.public.netloc, self.api_key).encode("utf-8")
        chunk = b"x" * (256 * 1024)
        bytes_sent = 0
        response_raw = bytearray()
        transport_error = ""
        started = time.monotonic()
        try:
            sock.sendall(request_head)
            while bytes_sent <= max_body_bytes:
                remaining = max_body_bytes + 1 - bytes_sent
                payload = chunk if remaining >= len(chunk) else chunk[:remaining]
                sock.sendall(("%x\r\n" % len(payload)).encode("ascii"))
                sock.sendall(payload)
                sock.sendall(b"\r\n")
                bytes_sent += len(payload)
                readable, _, _ = select.select([sock], [], [], 0)
                if readable:
                    break
            if bytes_sent > max_body_bytes:
                try:
                    sock.sendall(b"0\r\n\r\n")
                except OSError:
                    pass
            while b"\r\n\r\n" not in response_raw and len(response_raw) <= MAX_RESPONSE_BYTES:
                data = sock.recv(65536)
                if not data:
                    break
                response_raw.extend(data)
        except OSError as error:
            transport_error = type(error).__name__
            try:
                while b"\r\n\r\n" not in response_raw and len(response_raw) <= MAX_RESPONSE_BYTES:
                    data = sock.recv(65536)
                    if not data:
                        break
                    response_raw.extend(data)
            except OSError:
                pass
        finally:
            sock.close()
        elapsed_ms = round((time.monotonic() - started) * 1000)
        status = 0
        if response_raw:
            first_line = bytes(response_raw).split(b"\r\n", 1)[0]
            parts = first_line.split(b" ", 2)
            if len(parts) >= 2 and parts[1].isdigit():
                status = int(parts[1])
        deadline = time.monotonic() + 5
        stats = {}
        while time.monotonic() < deadline:
            stats = self.fixture_stats()
            if stats["active"] == 0:
                break
            time.sleep(0.05)
        return {
            "status": status,
            "bytes_sent": bytes_sent,
            "elapsed_ms": elapsed_ms,
            "transport_error": transport_error,
            "fixture_active_after": stats.get("active", -1),
            "passed": status == 413 and bytes_sent > max_body_bytes and stats.get("active") == 0,
        }


def run(targets, api_key, timeout, requests, workers, delay_ms, max_body_bytes):
    if len(targets) != 2 or {target.surface for target in targets} != {"v1", "v2"}:
        raise ValueError("operational comparison requires exactly one v1 and one v2 target")
    reports = {}
    failures = []
    for target in targets:
        client = Client(target, api_key, timeout)
        target_report = {"surface": target.surface}
        for name, operation in (
            ("cancellation", client.cancel_stream),
            (
                "concurrency",
                lambda client=client: client.concurrent_probe(requests, workers, delay_ms),
            ),
            (
                "chunked_413",
                lambda client=client: client.chunked_limit_probe(max_body_bytes),
            ),
        ):
            try:
                result = operation()
            except Exception as error:
                result = {"passed": False, "transport_error": type(error).__name__}
            target_report[name] = result
            if not result.get("passed"):
                failures.append({"target": target.name, "probe": name})
        reports[target.name] = target_report

    by_surface = {target.surface: reports[target.name] for target in targets}
    if not failures:
        v1_p95 = by_surface["v1"]["concurrency"]["latency_ms"]["p95"]
        v2_p95 = by_surface["v2"]["concurrency"]["latency_ms"]["p95"]
        latency_limit = max(v1_p95 * 3, v1_p95 + 250)
        if v2_p95 > latency_limit:
            failures.append(
                {
                    "probe": "concurrency",
                    "reason": "go_v2_p95_regression",
                    "v1_p95_ms": v1_p95,
                    "v2_p95_ms": v2_p95,
                    "limit_ms": latency_limit,
                }
            )

    return {
        "version": 1,
        "compatible": not failures,
        "parameters": {
            "requests": requests,
            "workers": workers,
            "delay_ms": delay_ms,
            "max_body_bytes": max_body_bytes,
        },
        "targets": reports,
        "failures": failures,
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", action="append", required=True, type=parse_target)
    parser.add_argument("--timeout", type=float, default=30)
    parser.add_argument("--requests", type=int, default=32)
    parser.add_argument("--workers", type=int, default=16)
    parser.add_argument("--delay-ms", type=int, default=200)
    parser.add_argument("--max-body-bytes", type=int, default=DEFAULT_MAX_BODY_BYTES)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--api-key-stdin", action="store_true", required=True)
    args = parser.parse_args(argv)
    if args.timeout <= 0 or args.requests <= 0 or args.workers <= 0:
        raise SystemExit("timeouts and concurrency parameters must be positive")
    if args.workers > args.requests or not (0 <= args.delay_ms <= 10000):
        raise SystemExit("invalid concurrency or fixture-delay parameters")
    if args.max_body_bytes <= 0:
        raise SystemExit("max body bytes must be positive")
    api_key = sys.stdin.readline().strip()
    if len(api_key) < 16:
        raise SystemExit("dedicated Test API Key is unavailable")
    report = run(
        args.target,
        api_key,
        args.timeout,
        args.requests,
        args.workers,
        args.delay_ms,
        args.max_body_bytes,
    )
    rendered = json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
    args.output.write_text(rendered, encoding="utf-8")
    print(rendered, end="")
    api_key = ""
    return 0 if report["compatible"] else 1


if __name__ == "__main__":
    sys.exit(main())
