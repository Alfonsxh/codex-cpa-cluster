import importlib.util
import json
import ssl
import threading
import unittest
import urllib.error
import urllib.request
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


ROOT = Path(__file__).parents[1]
PREVIEW_PATH = ROOT / "scripts" / "admin-preview.py"


def load_preview_module():
    spec = importlib.util.spec_from_file_location("admin_preview", PREVIEW_PATH)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class PreviewTestSupport:
    @classmethod
    def setUpClass(cls):
        cls.preview = load_preview_module()

    def start_preview(self, mode, upstream="https://unused.invalid"):
        server = self.preview.PreviewServer(
            ("127.0.0.1", 0),
            mode=mode,
            root=ROOT,
            upstream=upstream,
            timeout=2,
            ssl_context=ssl.create_default_context(),
        )
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(thread.join, 2)
        self.addCleanup(server.shutdown)
        return server, "http://127.0.0.1:{}".format(server.server_port)

    @staticmethod
    def request(base, path, method="GET", body=None, headers=None):
        request_headers = dict(headers or {})
        raw_body = None
        if body is not None:
            raw_body = json.dumps(body).encode("utf-8")
            request_headers["Content-Type"] = "application/json"
        request = urllib.request.Request(
            base + path,
            data=raw_body,
            headers=request_headers,
            method=method,
        )
        try:
            with urllib.request.urlopen(request, timeout=3) as response:
                return response.status, dict(response.headers), response.read()
        except urllib.error.HTTPError as error:
            try:
                return error.code, dict(error.headers), error.read()
            finally:
                error.close()


class PreviewServerCase(PreviewTestSupport, unittest.TestCase):

    def test_mock_mode_serves_complete_core_get_surface_without_404(self):
        _, base = self.start_preview("mock")
        status, headers, raw = self.request(
            base,
            "/admin/api/session",
            method="POST",
            headers={"X-Management-Key": "preview"},
        )
        self.assertEqual(status, HTTPStatus.CREATED, raw.decode("utf-8", errors="replace"))
        cookie = headers["Set-Cookie"].split(";", 1)[0]
        paths = (
            "/admin/api/session",
            "/admin/api/overview",
            "/admin/api/overview/catalog",
            "/admin/api/overview/usage?window=today",
            "/admin/api/accounts?window=today",
            "/admin/api/accounts/usage-breakdown?account=cpa-main&window=today",
            "/admin/api/images/cliproxy",
            "/admin/api/users?view=summary&window=today&page=1&page_size=50",
            "/admin/api/users/detail?email=lin.chen%40example.com&window=today",
            "/admin/api/users/usage-breakdown?email=lin.chen%40example.com&window=today",
            "/admin/api/teams",
            "/admin/api/teams/usage?window=all",
            "/admin/api/teams/usage-breakdown?team_id=platform&window=today",
            "/admin/api/settings",
            "/admin/api/release",
            "/admin/api/operations/impact?action=stop&target=cpa-main",
        )
        for path in paths:
            with self.subTest(path=path):
                status, _, raw = self.request(base, path, headers={"Cookie": cookie})
                self.assertEqual(status, HTTPStatus.OK, raw.decode("utf-8", errors="replace"))
                self.assertIsInstance(json.loads(raw), dict)

    def test_mock_static_files_are_current_workspace_files_and_writes_are_local_only(self):
        _, base = self.start_preview("mock")
        status, headers, raw = self.request(base, "/admin/app.js")
        self.assertEqual(status, HTTPStatus.OK)
        self.assertEqual(raw, (ROOT / "admin" / "static" / "app.js").read_bytes())
        self.assertEqual(headers["X-CPA-Preview-Mode"], "mock")

        status, _, raw = self.request(base, "/admin/view-state-utils.js")
        self.assertEqual(status, HTTPStatus.OK)
        self.assertEqual(
            raw,
            (ROOT / "admin" / "static" / "view-state-utils.js").read_bytes(),
        )

        status, _, raw = self.request(
            base,
            "/admin/api/users",
            method="POST",
            body={"email": "must-not-exist@example.com"},
        )
        self.assertEqual(status, HTTPStatus.METHOD_NOT_ALLOWED)
        self.assertEqual(json.loads(raw)["error"]["code"], "read_only_preview")

    def test_cli_rejects_non_loopback_bind_and_non_https_upstream(self):
        with self.assertRaises(Exception):
            self.preview.loopback_host("0.0.0.0")
        with self.assertRaises(Exception):
            self.preview.validated_upstream("http://test.example.com")


class FakeUpstreamHandler(BaseHTTPRequestHandler):
    requests = []

    def log_message(self, fmt, *args):
        return

    def _write(self, status, payload, headers=()):
        raw = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        for name, value in headers:
            self.send_header(name, value)
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        type(self).requests.append(
            {
                "method": "GET",
                "path": self.path,
                "management_key": self.headers.get("X-Management-Key"),
                "cookie": self.headers.get("Cookie"),
                "authorization": self.headers.get("Authorization"),
                "csrf": self.headers.get("X-CSRF-Token"),
            }
        )
        if self.path == "/admin/api/session":
            if self.headers.get("X-Management-Key") != "remote-secret":
                self._write(HTTPStatus.UNAUTHORIZED, {"error": {"code": "unauthorized", "message": "bad key"}})
                return
            self._write(
                HTTPStatus.OK,
                {"authenticated": True, "accounts": {"remote": {"email": "remote@example.com"}}},
                headers=(("Set-Cookie", "remote_cookie=must-not-forward"),),
            )
            return
        self._write(HTTPStatus.OK, {"path": self.path})

    def do_POST(self):
        self._write_request()

    def do_PUT(self):
        self._write_request()

    def do_PATCH(self):
        self._write_request()

    def do_DELETE(self):
        self._write_request()

    def _write_request(self):
        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length) if length else b""
        type(self).requests.append(
            {
                "method": self.command,
                "path": self.path,
                "management_key": self.headers.get("X-Management-Key"),
                "cookie": self.headers.get("Cookie"),
                "authorization": self.headers.get("Authorization"),
                "csrf": self.headers.get("X-CSRF-Token"),
                "content_type": self.headers.get("Content-Type"),
                "body": raw,
            }
        )
        self._write(HTTPStatus.OK, {"method": self.command, "path": self.path})


class RemoteReadOnlyPreviewTests(PreviewTestSupport, unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.preview = load_preview_module()

    def setUp(self):
        FakeUpstreamHandler.requests = []
        self.upstream_server = ThreadingHTTPServer(("127.0.0.1", 0), FakeUpstreamHandler)
        self.upstream_thread = threading.Thread(
            target=self.upstream_server.serve_forever,
            daemon=True,
        )
        self.upstream_thread.start()
        self.addCleanup(self.upstream_server.server_close)
        self.addCleanup(self.upstream_thread.join, 2)
        self.addCleanup(self.upstream_server.shutdown)
        upstream = "http://127.0.0.1:{}".format(self.upstream_server.server_port)
        self.preview_server, self.base = self.start_preview("remote-read-only", upstream)

    def login(self):
        status, headers, raw = self.request(
            self.base,
            "/admin/api/session",
            method="POST",
            headers={"X-Management-Key": "remote-secret"},
        )
        self.assertEqual(status, HTTPStatus.CREATED, raw.decode("utf-8", errors="replace"))
        return headers["Set-Cookie"].split(";", 1)[0], headers["Set-Cookie"], json.loads(raw)

    def test_login_keeps_key_only_in_process_memory_and_uses_local_cookie(self):
        cookie, set_cookie, payload = self.login()
        self.assertTrue(payload["csrf_token"])
        self.assertNotIn("remote-secret", json.dumps(payload))
        self.assertIn("HttpOnly", set_cookie)
        self.assertIn("SameSite=Strict", set_cookie)
        self.assertNotIn("remote_cookie", set_cookie)
        sessions = list(self.preview_server.sessions._sessions.values())
        self.assertEqual(sessions[0]["management_key"], "remote-secret")
        self.assertEqual(FakeUpstreamHandler.requests[0]["management_key"], "remote-secret")

    def test_get_proxy_strips_side_effect_query_and_browser_credentials(self):
        cookie, _, _ = self.login()
        status, headers, raw = self.request(
            self.base,
            "/admin/api/accounts?window=today&fresh=1&account=cpa-main",
            headers={
                "Cookie": cookie,
                "Authorization": "Bearer browser-secret",
                "X-CSRF-Token": "browser-csrf",
            },
        )
        self.assertEqual(status, HTTPStatus.OK)
        self.assertNotIn("Set-Cookie", headers)
        self.assertEqual(json.loads(raw)["path"], "/admin/api/accounts?window=today&account=cpa-main")
        proxied = FakeUpstreamHandler.requests[-1]
        self.assertEqual(proxied["management_key"], "remote-secret")
        self.assertIsNone(proxied["cookie"])
        self.assertIsNone(proxied["authorization"])
        self.assertIsNone(proxied["csrf"])

    def test_business_writes_and_unknown_get_routes_never_reach_upstream(self):
        cookie, _, payload = self.login()
        request_count = len(FakeUpstreamHandler.requests)
        status, _, raw = self.request(
            self.base,
            "/admin/api/users",
            method="POST",
            body={"email": "blocked@example.com"},
            headers={"Cookie": cookie, "X-CSRF-Token": payload["csrf_token"]},
        )
        self.assertEqual(status, HTTPStatus.METHOD_NOT_ALLOWED)
        self.assertEqual(json.loads(raw)["error"]["code"], "read_only_preview")

        status, _, raw = self.request(
            self.base,
            "/admin/api/not-whitelisted",
            headers={"Cookie": cookie},
        )
        self.assertEqual(status, HTTPStatus.NOT_FOUND)
        self.assertEqual(json.loads(raw)["error"]["code"], "route_not_allowed")
        self.assertEqual(len(FakeUpstreamHandler.requests), request_count)

    def test_admin_static_assets_are_local_even_when_remote_mode_is_active(self):
        status, headers, raw = self.request(self.base, "/admin/app.css")
        self.assertEqual(status, HTTPStatus.OK)
        self.assertTrue(raw.startswith((ROOT / "admin" / "static" / "app.css").read_bytes()))
        self.assertEqual(headers["X-CPA-Preview-Mode"], "remote-read-only")
        self.assertEqual(FakeUpstreamHandler.requests, [])


class RemoteReadWritePreviewTests(PreviewTestSupport, unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.preview = load_preview_module()

    def setUp(self):
        FakeUpstreamHandler.requests = []
        self.upstream_server = ThreadingHTTPServer(("127.0.0.1", 0), FakeUpstreamHandler)
        self.upstream_thread = threading.Thread(
            target=self.upstream_server.serve_forever,
            daemon=True,
        )
        self.upstream_thread.start()
        self.addCleanup(self.upstream_server.server_close)
        self.addCleanup(self.upstream_thread.join, 2)
        self.addCleanup(self.upstream_server.shutdown)
        upstream = "http://127.0.0.1:{}".format(self.upstream_server.server_port)
        self.preview_server, self.base = self.start_preview("remote-read-write", upstream)

    def login(self):
        status, headers, raw = self.request(
            self.base,
            "/admin/api/session",
            method="POST",
            headers={"X-Management-Key": "remote-secret"},
        )
        self.assertEqual(status, HTTPStatus.CREATED, raw.decode("utf-8", errors="replace"))
        return headers["Set-Cookie"].split(";", 1)[0], json.loads(raw)

    def test_allowed_write_uses_process_key_and_drops_browser_credentials(self):
        cookie, session = self.login()
        body = {"name": "Writable Test Team"}
        status, _, raw = self.request(
            self.base,
            "/admin/api/teams",
            method="POST",
            body=body,
            headers={
                "Cookie": cookie,
                "Authorization": "Bearer browser-secret",
                "X-CSRF-Token": session["csrf_token"],
            },
        )
        self.assertEqual(status, HTTPStatus.OK, raw.decode("utf-8", errors="replace"))
        proxied = FakeUpstreamHandler.requests[-1]
        self.assertEqual(proxied["method"], "POST")
        self.assertEqual(proxied["path"], "/admin/api/teams")
        self.assertEqual(proxied["management_key"], "remote-secret")
        self.assertIsNone(proxied["cookie"])
        self.assertIsNone(proxied["authorization"])
        self.assertIsNone(proxied["csrf"])
        self.assertEqual(json.loads(proxied["body"]), body)

    def test_write_requires_local_session_csrf_and_allowlisted_route(self):
        cookie, session = self.login()
        request_count = len(FakeUpstreamHandler.requests)
        status, _, raw = self.request(
            self.base,
            "/admin/api/users/team",
            method="PUT",
            body={"email": "alice@example.com", "team_id": "platform"},
            headers={"Cookie": cookie},
        )
        self.assertEqual(status, HTTPStatus.FORBIDDEN)
        self.assertEqual(json.loads(raw)["error"]["code"], "csrf_required")
        self.assertEqual(len(FakeUpstreamHandler.requests), request_count)

        status, _, raw = self.request(
            self.base,
            "/admin/api/not-whitelisted",
            method="POST",
            body={},
            headers={"Cookie": cookie, "X-CSRF-Token": session["csrf_token"]},
        )
        self.assertEqual(status, HTTPStatus.NOT_FOUND)
        self.assertEqual(json.loads(raw)["error"]["code"], "route_not_allowed")
        self.assertEqual(len(FakeUpstreamHandler.requests), request_count)

    def test_write_mode_banner_and_health_make_mutation_capability_visible(self):
        status, headers, raw = self.request(self.base, "/admin/")
        self.assertEqual(status, HTTPStatus.OK)
        self.assertIn("测试环境数据 · 可读写", raw.decode("utf-8"))
        self.assertEqual(headers["X-CPA-Preview-Mode"], "remote-read-write")
        status, _, raw = self.request(self.base, "/healthz")
        self.assertEqual(status, HTTPStatus.OK)
        self.assertTrue(json.loads(raw)["write_enabled"])

    def test_cli_write_mode_requires_repeated_origin_confirmation(self):
        with self.assertRaises(SystemExit):
            self.preview.main([
                "--mode", "remote-read-write",
                "--upstream", "https://test.example.com",
            ])
        parser = self.preview.build_parser()
        args = parser.parse_args([
            "--mode", "remote-read-write",
            "--upstream", "https://test.example.com",
            "--confirm-write-upstream", "https://test.example.com",
        ])
        self.assertEqual(args.confirm_write_upstream, args.upstream)


if __name__ == "__main__":
    unittest.main()
