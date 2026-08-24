import importlib.util
import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "migration-data-plane-fault-compare.py"
SPEC = importlib.util.spec_from_file_location("migration_data_plane_fault_compare", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class FaultHandler(BaseHTTPRequestHandler):
    mode = "baseline"
    legacy_upstream_html = False

    def do_GET(self):
        authorization = self.headers.get("Authorization", "")
        if self.path == MODULE.UNKNOWN_PATH:
            self.send_response(404)
            self.end_headers()
            return
        if self.mode == "auth-unavailable":
            status, code = MODULE.MODE_CONTRACTS[self.mode]
            self.json_error(status, code)
            return
        if authorization not in {"Bearer dedicated-test-key-123", "Bearer " + MODULE.INVALID_KEY}:
            self.json_error(401, "")
            return
        if authorization == "Bearer " + MODULE.INVALID_KEY:
            self.json_error(401, "")
            return
        status, code = MODULE.MODE_CONTRACTS[self.mode]
        if status == 200:
            body = json.dumps({"data": []}).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if status == 502 and self.legacy_upstream_html:
            body = b"<html><body>Bad Gateway</body></html>"
            self.send_response(502)
            self.send_header("Content-Type", "text/html")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self.json_error(status, code)

    def json_error(self, status, code):
        payload = {"error": {"code": code, "message": "redacted"}}
        if status == 429:
            payload["user_weekly_quota"] = {
                "used_tokens": 100,
                "weighted_used_tokens": 100,
                "raw_used_tokens": 100,
                "limit_tokens": 100,
                "week_end_at": 2000,
                "quota_unit": "weighted_tokens",
            }
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        if status == 503:
            self.send_header("Retry-After", "1")
        if status == 429:
            self.send_header("Retry-After", "1000")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, _format, *_args):
        return


class MigrationDataPlaneFaultCompareTests(unittest.TestCase):
    def setUp(self):
        self.servers = []
        self.threads = []

    def tearDown(self):
        for server in self.servers:
            server.shutdown()
            server.server_close()
        for thread in self.threads:
            thread.join(timeout=2)

    def target(self, name, surface, mode):
        handler = type(
            "ModeHandler",
            (FaultHandler,),
            {
                "mode": mode,
                "legacy_upstream_html": mode == "upstream-unavailable" and surface == "v1",
            },
        )
        server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.servers.append(server)
        self.threads.append(thread)
        return MODULE.Target(name, surface, "http://127.0.0.1:{}".format(server.server_port))

    def test_baseline_contract(self):
        report = MODULE.run(
            [self.target("v1", "v1", "baseline"), self.target("v2", "v2", "baseline")],
            "dedicated-test-key-123",
            "baseline",
            2,
        )
        self.assertTrue(report["compatible"])
        self.assertEqual(report["failures"], [])

    def test_auth_unavailable_contract(self):
        report = MODULE.run(
            [
                self.target("v1", "v1", "auth-unavailable"),
                self.target("v2", "v2", "auth-unavailable"),
            ],
            "dedicated-test-key-123",
            "auth-unavailable",
            2,
        )
        self.assertTrue(report["compatible"])
        self.assertEqual(
            report["targets"]["v2"]["probes"]["mode_contract"]["error_code"],
            "authentication_snapshot_unavailable",
        )

    def test_quota_exceeded_contract(self):
        report = MODULE.run(
            [
                self.target("v1", "v1", "quota-exceeded"),
                self.target("v2", "v2", "quota-exceeded"),
            ],
            "dedicated-test-key-123",
            "quota-exceeded",
            2,
        )
        self.assertTrue(report["compatible"])
        result = report["targets"]["v2"]["probes"]["mode_contract"]
        self.assertEqual(result["error_code"], "weekly_user_token_quota_exceeded")
        self.assertTrue(result["quota_exceeded"])
        self.assertEqual(result["quota_unit"], "weighted_tokens")

    def test_approves_structured_go_upstream_error_over_legacy_html(self):
        report = MODULE.run(
            [
                self.target("v1", "v1", "upstream-unavailable"),
                self.target("v2", "v2", "upstream-unavailable"),
            ],
            "dedicated-test-key-123",
            "upstream-unavailable",
            2,
        )
        self.assertTrue(report["compatible"])
        self.assertEqual(len(report["approved_differences"]), 1)
        self.assertEqual(
            report["targets"]["v1"]["probes"]["mode_contract"]["content_type"],
            "text/html",
        )

    def test_approves_bounded_go_error_when_legacy_upstream_times_out(self):
        original_request = MODULE.Client.request

        def request(client, path, authorization):
            if (
                client.target.surface == "v1"
                and path == "/v1/models"
                and authorization == "Bearer dedicated-test-key-123"
            ):
                raise TimeoutError("legacy upstream exceeded the comparison bound")
            return original_request(client, path, authorization)

        with mock.patch.object(MODULE.Client, "request", request):
            report = MODULE.run(
                [
                    self.target("v1", "v1", "upstream-unavailable"),
                    self.target("v2", "v2", "upstream-unavailable"),
                ],
                "dedicated-test-key-123",
                "upstream-unavailable",
                2,
            )

        self.assertTrue(report["compatible"])
        self.assertEqual(report["failures"], [])
        self.assertEqual(len(report["approved_differences"]), 1)
        self.assertEqual(
            report["targets"]["v1"]["probes"]["mode_contract"]["transport_error"],
            "TimeoutError",
        )
        self.assertEqual(
            report["targets"]["v2"]["probes"]["mode_contract"]["error_code"],
            "upstream_unavailable",
        )

    def test_detects_wrong_mode_status(self):
        report = MODULE.run(
            [self.target("v1", "v1", "baseline"), self.target("v2", "v2", "baseline")],
            "dedicated-test-key-123",
            "upstream-unavailable",
            2,
        )
        self.assertFalse(report["compatible"])
        self.assertEqual(len(report["failures"]), 2)


if __name__ == "__main__":
    unittest.main()
