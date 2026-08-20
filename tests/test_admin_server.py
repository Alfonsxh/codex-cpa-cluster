import base64
import gzip
import http.cookiejar
import importlib.util
import json
import sys
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request
import uuid
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest import mock

try:
    from fixtures import TEST_INITIAL_PORTAL_PASSWORD, seed_control_plane
except ImportError:
    from tests.fixtures import TEST_INITIAL_PORTAL_PASSWORD, seed_control_plane


ROOT = Path(__file__).parents[1]
SERVER_PATH = ROOT / "admin" / "server.py"
CONTROL_PATH = ROOT / "scripts" / "cliproxy.py"


def load_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class AdminServerTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.control_module = load_module("cliproxy_test_admin", CONTROL_PATH)
        cls.server_module = load_module("cliproxy_admin_server", SERVER_PATH)

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        seed_control_plane(self.root)
        (self.root / "secrets").mkdir(parents=True, exist_ok=True)
        self.key_file = self.root / "secrets" / "cpa-management.key"
        self.key_file.write_text("test-management-key\n", encoding="utf-8")
        self.control = self.control_module.ControlPlane(self.root)
        self.control.ensure_layout()
        self.control.apply_changes = mock.Mock()
        self.app = self.server_module.AdminApplication(
            root=self.root,
            key_file=self.key_file,
            control=self.control,
        )
        self.server = self.server_module.AdminHTTPServer(("127.0.0.1", 0), self.app)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.base = "http://127.0.0.1:{}".format(self.server.server_port)

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)
        self.tmp.cleanup()

    def request(self, path, method="GET", body=None, authenticated=True, extra_headers=None):
        headers = dict(extra_headers or {})
        if authenticated:
            headers["X-Management-Key"] = "test-management-key"
        data = None
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(self.base + path, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(request, timeout=5) as response:
                return response.status, dict(response.headers), response.read()
        except urllib.error.HTTPError as error:
            return error.code, dict(error.headers), error.read()

    def test_static_page_is_public_but_api_requires_management_key(self):
        status, headers, body = self.request("/admin/", authenticated=False)
        self.assertEqual(status, 200)
        self.assertIn(b"Codex CPA Cluster", body)
        self.assertIn(b'pattern="[a-z][a-z0-9\\-]{1,31}"', body)
        self.assertIn("Content-Security-Policy", headers)

        status, _, body = self.request("/admin/api/session", authenticated=False)
        self.assertEqual(status, 401)
        self.assertEqual(json.loads(body)["error"]["code"], "unauthorized")

    def test_admin_browser_session_supports_http_cookie_and_csrf_without_persisting_key(self):
        status, headers, raw = self.request(
            "/admin/api/session",
            method="POST",
            authenticated=True,
        )
        self.assertEqual(status, 201)
        payload = json.loads(raw)
        self.assertTrue(payload["authenticated"])
        self.assertTrue(payload["csrf_token"])
        self.assertIn("HttpOnly", headers["Set-Cookie"])
        self.assertNotIn("Secure", headers["Set-Cookie"])
        self.assertIn("SameSite=Strict", headers["Set-Cookie"])
        cookie = headers["Set-Cookie"].split(";", 1)[0]

        status, _, raw = self.request(
            "/admin/api/session",
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(raw)["csrf_token"], payload["csrf_token"])

        status, _, raw = self.request(
            "/admin/api/users",
            method="POST",
            body={"email": "csrf@example.com"},
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 403)
        self.assertEqual(json.loads(raw)["error"]["code"], "csrf_required")

        status, _, _ = self.request(
            "/admin/api/users",
            method="POST",
            body={"email": "csrf@example.com"},
            authenticated=False,
            extra_headers={
                "Cookie": cookie,
                "X-CSRF-Token": payload["csrf_token"],
            },
        )
        self.assertEqual(status, 201)

        status, headers, _ = self.request(
            "/admin/api/session",
            method="DELETE",
            authenticated=False,
            extra_headers={
                "Cookie": cookie,
                "X-CSRF-Token": payload["csrf_token"],
            },
        )
        self.assertEqual(status, 200)
        self.assertIn("Max-Age=0", headers["Set-Cookie"])

    def test_session_cookies_remain_secure_behind_https_proxy(self):
        status, headers, _ = self.request(
            "/admin/api/session",
            method="POST",
            authenticated=True,
            extra_headers={"X-Forwarded-Proto": "https"},
        )
        self.assertEqual(status, 201)
        self.assertIn("Secure", headers["Set-Cookie"])
        self.assertIn("SameSite=Strict", headers["Set-Cookie"])
        self.assertEqual(headers["Strict-Transport-Security"], "max-age=0")

        self.control.create_user("secure-cookie@example.com", apply=False)
        self.app.usage_store.set_portal_credential(
            "secure-cookie@example.com",
            self.server_module.hash_portal_password("secure-password"),
            must_change=False,
        )
        status, headers, _ = self.request(
            "/usage/session",
            method="POST",
            body={
                "email": "secure-cookie@example.com",
                "password": "secure-password",
            },
            authenticated=False,
            extra_headers={"X-Forwarded-Proto": "https"},
        )
        self.assertEqual(status, 201)
        self.assertIn("Secure", headers["Set-Cookie"])
        self.assertIn("SameSite=Lax", headers["Set-Cookie"])
        self.assertEqual(headers["Strict-Transport-Security"], "max-age=0")

    def test_http_browser_reuses_admin_session_cookie_after_login(self):
        cookies = http.cookiejar.CookieJar()
        browser = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(cookies)
        )
        login = urllib.request.Request(
            self.base + "/admin/api/session",
            data=b"",
            method="POST",
            headers={"X-Management-Key": "test-management-key"},
        )
        with browser.open(login, timeout=5) as response:
            self.assertEqual(response.status, 201)
            self.assertNotIn("Strict-Transport-Security", response.headers)
        stored = list(cookies)
        self.assertEqual(len(stored), 1)
        self.assertFalse(stored[0].secure)

        with browser.open(self.base + "/admin/api/session", timeout=5) as response:
            self.assertEqual(response.status, 200)
            self.assertTrue(json.load(response)["authenticated"])

    def test_portal_login_rate_limit_blocks_ip_and_account(self):
        self.control.create_user("limited@example.com", apply=False)
        self.app.usage_store.set_portal_credential(
            "limited@example.com",
            self.server_module.hash_portal_password("correct-secret"),
            must_change=False,
        )
        for _ in range(4):
            status, _, _ = self.request(
                "/usage/session",
                method="POST",
                body={"email": "limited@example.com", "password": "wrong-secret"},
                authenticated=False,
            )
            self.assertEqual(status, 401)

        status, headers, raw = self.request(
            "/usage/session",
            method="POST",
            body={"email": "limited@example.com", "password": "wrong-secret"},
            authenticated=False,
        )
        self.assertEqual(status, 429)
        self.assertEqual(json.loads(raw)["error"]["code"], "rate_limited")
        self.assertGreaterEqual(int(headers["Retry-After"]), 1)

    def test_management_key_validation_is_rate_limited(self):
        for _ in range(5):
            status, _, _ = self.request(
                "/admin/api/session",
                authenticated=False,
                extra_headers={"X-Management-Key": "wrong-management-key"},
            )
            self.assertEqual(status, 401)

        status, headers, raw = self.request(
            "/admin/api/session",
            authenticated=False,
            extra_headers={"X-Management-Key": "wrong-management-key"},
        )
        self.assertEqual(status, 429)
        self.assertEqual(json.loads(raw)["error"]["code"], "rate_limited")
        self.assertGreaterEqual(int(headers["Retry-After"]), 1)

    def test_password_reset_clears_account_and_related_ip_login_lockout(self):
        self.control.create_user("limited@example.com", apply=False)
        self.app.usage_store.set_portal_credential(
            "limited@example.com",
            self.server_module.hash_portal_password("old-secret-123"),
            must_change=False,
        )
        for _ in range(5):
            status, _, _ = self.request(
                "/usage/session",
                method="POST",
                body={"email": "limited@example.com", "password": "wrong-secret"},
                authenticated=False,
            )
        self.assertEqual(status, 429)

        status, _, raw = self.request(
            "/admin/api/users/reset-password",
            method="POST",
            body={"email": "limited@example.com"},
        )
        self.assertEqual(status, 200)
        initial_password = json.loads(raw)["initial_password"]

        status, _, raw = self.request(
            "/usage/session",
            method="POST",
            body={"email": "limited@example.com", "password": initial_password},
            authenticated=False,
        )
        self.assertEqual(status, 201)
        self.assertTrue(json.loads(raw)["password_change_required"])

    def test_large_json_responses_use_fast_gzip_when_requested(self):
        self.control.create_user("alice@example.com", apply=False)
        with mock.patch.object(self.server_module, "JSON_GZIP_MIN_BYTES", 1):
            status, headers, raw = self.request(
                "/admin/api/users",
                extra_headers={"Accept-Encoding": "gzip"},
            )

        self.assertEqual(status, 200)
        self.assertEqual(headers["Content-Encoding"], "gzip")
        self.assertEqual(headers["Vary"], "Accept-Encoding")
        payload = json.loads(gzip.decompress(raw))
        self.assertEqual(payload["users"][0]["email"], "alice@example.com")

    def test_admin_monitor_utils_is_a_public_static_asset(self):
        status, headers, body = self.request(
            "/admin/monitor-utils.js", authenticated=False
        )

        self.assertEqual(status, 200)
        self.assertTrue(headers["Content-Type"].startswith("text/javascript"))
        self.assertIn(b"sortTooltipSeries", body)

        status, headers, body = self.request(
            "/admin/view-state-utils.js", authenticated=False
        )
        self.assertEqual(status, 200)
        self.assertTrue(headers["Content-Type"].startswith("text/javascript"))
        self.assertIn(b"mutationAffectedViews", body)

    def test_public_branding_config_and_authenticated_logo_lifecycle(self):
        status, _, raw = self.request("/site-config.json", authenticated=False)
        self.assertEqual(status, 200)
        public_configuration = json.loads(raw)
        self.assertEqual(public_configuration["product_name"], "Codex CPA Cluster")
        self.assertEqual(public_configuration["public_base_url"], "http://cpa.example.com")
        self.assertFalse(public_configuration["logo"]["custom"])
        self.assertNotIn("allowed_email_domains", public_configuration)
        self.assertNotIn("key_prefix", public_configuration)

        status, _, raw = self.request(
            "/admin/api/native-accounts",
            authenticated=False,
        )
        self.assertEqual(status, 401)
        status, _, raw = self.request("/admin/api/native-accounts")
        self.assertEqual(status, 200)
        native_accounts = json.loads(raw)["accounts"]
        self.assertEqual(native_accounts[0]["id"], "alpha")
        self.assertIn("management_url", native_accounts[0])
        self.assertNotIn("port", native_accounts[0])
        status, _, raw = self.request(
            "/admin/api/native-accounts",
            extra_headers={"Host": "cpa.example.com"},
        )
        self.assertEqual(status, 200)
        public_host_account = json.loads(raw)["accounts"][0]
        self.assertNotIn("management_url", public_host_account)
        self.assertNotIn("port", public_host_account)

        svg = b'<svg xmlns="http://www.w3.org/2000/svg"><circle cx="5" cy="5" r="5"/></svg>'
        status, _, raw = self.request(
            "/admin/api/settings/logo",
            method="POST",
            body={
                "filename": "custom.svg",
                "content_type": "image/svg+xml",
                "data_base64": base64.b64encode(svg).decode("ascii"),
                "confirm": "save",
            },
        )
        self.assertEqual(status, 200)
        self.assertTrue(json.loads(raw)["logo"]["custom"])

        status, headers, raw = self.request("/branding/logo", authenticated=False)
        self.assertEqual(status, 200)
        self.assertEqual(headers["Content-Type"], "image/svg+xml")
        self.assertEqual(raw, svg)

        status, _, raw = self.request(
            "/admin/api/settings/logo",
            method="DELETE",
            body={"confirm": "reset"},
        )
        self.assertEqual(status, 200)
        self.assertFalse(json.loads(raw)["logo"]["custom"])

    def test_reasoning_effort_colors_are_configurable_without_relaxing_csp(self):
        status, headers, body = self.request(
            "/admin/reasoning-effort-colors.css",
            authenticated=False,
        )
        self.assertEqual(status, 200)
        self.assertTrue(headers["Content-Type"].startswith("text/css"))
        stylesheet = body.decode("utf-8")
        self.assertIn("--account-model-effort-xhigh: #5965c7;", stylesheet)
        self.assertIn("--account-model-effort-high: #2f73d9;", stylesheet)
        self.assertNotIn("user_quota", stylesheet)

        status, _, raw = self.request(
            "/admin/api/settings/configuration",
            method="POST",
            body={
                "values": {
                    "admin.account_usage.reasoning_effort_color.xhigh": "#F4E61A"
                },
                "confirm": "save",
            },
        )
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(raw)["applied"], ["live"])

        status, _, body = self.request(
            "/admin/reasoning-effort-colors.css?v=changed",
            authenticated=False,
        )
        self.assertEqual(status, 200)
        stylesheet = body.decode("utf-8")
        self.assertIn("--account-model-effort-xhigh: #f4e61a;", stylesheet)
        self.assertIn("--account-model-effort-xhigh-text: #171d2b;", stylesheet)

        status, _, raw = self.request(
            "/admin/api/settings/configuration",
            method="POST",
            body={
                "values": {
                    "admin.account_usage.reasoning_effort_color.high":
                        "#ffffff; background:url(https://example.com)"
                },
                "confirm": "save",
            },
        )
        self.assertEqual(status, 400)
        self.assertIn("#RRGGBB", raw.decode("utf-8"))

    def test_create_user_returns_secrets_once_and_list_masks_them(self):
        status, _, raw = self.request(
            "/admin/api/users",
            method="POST",
            body={"email": "alice@example.com"},
        )
        self.assertEqual(status, 201)
        created = json.loads(raw)
        self.assertEqual(len(created["keys"]), 1)
        self.assertEqual(created["initial_password"], TEST_INITIAL_PORTAL_PASSWORD)
        self.assertIsNone(created["team_id"])
        self.assertIsNone(created["team"])
        self.assertNotEqual(created["initial_password"], "123456")
        secrets = [item["key"] for item in created["keys"]]
        self.assertEqual(secrets[0].rsplit("_", 1)[0], "cpa_alice")
        self.assertTrue(
            all(uuid.UUID(secret.rsplit("_", 1)[1]).version == 4 for secret in secrets)
        )

        status, _, raw = self.request("/admin/api/users")
        self.assertEqual(status, 200)
        text = raw.decode("utf-8")
        self.assertNotIn(secrets[0], text)
        payload = json.loads(text)
        self.assertEqual(payload["users"][0]["active_keys"], 1)
        self.assertEqual(
            [item["status"] for item in payload["users"][0]["accounts"]],
            ["active", "active", "active", "active"],
        )
        self.assertEqual(
            {item["key"]["preview"].split("_••••", 1)[0] for item in payload["users"][0]["accounts"]},
            {"cpa_alice"},
        )

    def test_create_user_accepts_optional_team_and_rejects_unknown_team_first(self):
        team = self.control.store.create_team("Platform", "Core platform")

        status, _, raw = self.request(
            "/admin/api/users",
            method="POST",
            body={"email": "alice@example.com", "team_id": team["id"]},
        )

        self.assertEqual(status, 201)
        created = json.loads(raw)
        self.assertEqual(created["team_id"], team["id"])
        self.assertEqual(
            created["team"],
            {
                "id": team["id"],
                "name": "Platform",
                "description": "Core platform",
            },
        )
        classification = self.control.store.read_user_classifications(
            ["alice@example.com"]
        )["alice@example.com"]
        self.assertEqual(classification["team_id"], team["id"])
        self.assertEqual(classification["team"]["name"], "Platform")

        status, _, raw = self.request(
            "/admin/api/users",
            method="POST",
            body={"email": "bob@example.com", "team_id": "team_missing"},
        )

        self.assertEqual(status, 400)
        self.assertIn("团队不存在", raw.decode("utf-8"))
        self.assertNotIn(
            "bob@example.com",
            {item["email"] for item in self.control.store.read_user_summaries()},
        )

    def test_initial_password_requires_encrypted_configuration_and_never_reads_back(self):
        self.control.store.delete_secret("portal_initial_password")

        status, _, raw = self.request(
            "/admin/api/users",
            method="POST",
            body={"email": "alice@example.com"},
        )
        self.assertEqual(status, 503)
        self.assertEqual(json.loads(raw)["error"]["code"], "initial_password_unavailable")

        status, _, raw = self.request("/admin/api/settings")
        self.assertEqual(status, 200)
        settings = json.loads(raw)
        self.assertFalse(settings["initial_password_configured"])
        self.assertNotIn("initial_password", settings)

        configured_password = "configured-fixture-password"
        status, _, raw = self.request(
            "/admin/api/settings/initial-password",
            method="POST",
            body={
                "initial_password": configured_password,
                "confirmation": configured_password,
            },
        )
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(raw), {
            "message": "用户初始密码已安全保存；已有用户密码不会自动变化",
            "configured": True,
        })
        self.assertEqual(
            self.control.store.read_secret("portal_initial_password"),
            configured_password,
        )
        self.assertNotIn(
            configured_password.encode("utf-8"),
            (self.root / "state" / "control-plane.sqlite3").read_bytes(),
        )

        status, _, raw = self.request(
            "/admin/api/users",
            method="POST",
            body={"email": "alice@example.com"},
        )
        self.assertEqual(status, 201)
        self.assertEqual(json.loads(raw)["initial_password"], configured_password)

    def test_user_summary_api_paginates_and_loads_details_lazily(self):
        target_secret = ""
        for index in range(55):
            records = self.control.create_user(
                "user{:03d}@example.com".format(index),
                apply=False,
            )
            if index == 25:
                target_secret = records[0]["key"]

        status, _, raw = self.request(
            "/admin/api/users?view=summary&page=2&page_size=25"
            "&sort=email&direction=asc&window=86400"
        )

        self.assertEqual(status, 200)
        payload = json.loads(raw)
        self.assertEqual(
            payload["pagination"],
            {"page": 2, "page_size": 25, "total": 55, "total_pages": 3},
        )
        self.assertEqual(len(payload["users"]), 25)
        self.assertEqual(payload["users"][0]["email"], "user025@example.com")
        self.assertNotIn("accounts", payload["users"][0])
        self.assertEqual(payload["users"][0]["account_count"], 4)
        self.assertEqual(payload["users"][0]["active_accounts"], 4)

        status, _, raw = self.request(
            "/admin/api/users/detail?email=user025%40example.com&window=86400"
        )

        self.assertEqual(status, 200)
        detail = json.loads(raw)["user"]
        self.assertEqual(detail["email"], "user025@example.com")
        self.assertEqual(len(detail["accounts"]), 4)
        self.assertNotIn(target_secret, raw.decode("utf-8"))

        status, _, raw = self.request(
            "/admin/api/users?view=summary&page=1&page_size=25"
            "&q=user054&sort=tokens&direction=desc&window=86400"
        )
        self.assertEqual(status, 200)
        filtered = json.loads(raw)
        self.assertEqual(filtered["pagination"]["total"], 1)
        self.assertEqual(filtered["users"][0]["email"], "user054@example.com")

    def test_admin_team_tag_assignment_filters_and_current_team_usage(self):
        alice_records = self.control.create_user("alice@example.com", apply=False)
        self.control.create_user("bob@example.com", apply=False)
        status, _, raw = self.request(
            "/admin/api/teams",
            method="POST",
            body={"name": "Platform", "description": "Platform owners"},
        )
        self.assertEqual(status, 201)
        platform = json.loads(raw)["team"]
        status, _, raw = self.request(
            "/admin/api/teams",
            method="POST",
            body={"name": "Sales"},
        )
        self.assertEqual(status, 201)
        sales = json.loads(raw)["team"]
        status, _, raw = self.request(
            "/admin/api/tags",
            method="POST",
            body={"name": "Pilot", "color": "#336699"},
        )
        self.assertEqual(status, 201)
        pilot = json.loads(raw)["tag"]

        status, _, _ = self.request(
            "/admin/api/users/team",
            method="PUT",
            body={"email": "alice@example.com", "team_id": platform["id"]},
        )
        self.assertEqual(status, 200)
        status, _, _ = self.request(
            "/admin/api/users/tags",
            method="PUT",
            body={"email": "alice@example.com", "tag_ids": [pilot["id"]]},
        )
        self.assertEqual(status, 200)

        status, _, raw = self.request(
            "/admin/api/users?view=summary&window=all&team_id={}&tag_id={}".format(
                platform["id"], pilot["id"]
            )
        )
        self.assertEqual(status, 200)
        filtered = json.loads(raw)
        self.assertEqual(filtered["pagination"]["total"], 1)
        self.assertEqual(filtered["users"][0]["team"]["name"], "Platform")
        self.assertEqual(filtered["users"][0]["tags"][0]["name"], "Pilot")

        now = int(time.time())
        self.app.usage_store.sync_identities(self.control._read_registry(), now=now)
        self.app.usage_store.sync_user_teams(
            self.control.store.read_user_classifications(
                ["alice@example.com", "bob@example.com"]
            )
        )
        self.app.usage_store.ingest_events(
            alice_records[0]["account"],
            [{
                "timestamp": now - 20,
                "model": "gpt-5.6-sol",
                "alias": "gpt-5.6-sol",
                "api_key": alice_records[0]["key"],
                "request_id": "platform-event",
                "failed": False,
                "tokens": {"total_tokens": 100},
            }],
            now=now,
        )
        status, _, _ = self.request(
            "/admin/api/users/team",
            method="PUT",
            body={"email": "alice@example.com", "team_id": sales["id"]},
        )
        self.assertEqual(status, 200)
        self.app.usage_store.ingest_events(
            alice_records[0]["account"],
            [{
                "timestamp": now - 10,
                "model": "gpt-5.6-sol",
                "alias": "gpt-5.6-sol",
                "api_key": alice_records[0]["key"],
                "request_id": "sales-event",
                "failed": False,
                "tokens": {"total_tokens": 200},
            }],
            now=now,
        )

        status, _, raw = self.request("/admin/api/teams/usage?window=all")
        self.assertEqual(status, 200)
        team_payload = json.loads(raw)
        self.assertEqual(team_payload["attribution"], "current_membership")
        team_usage = {item["id"]: item for item in team_payload["teams"]}
        self.assertEqual(team_usage[platform["id"]]["usage"]["total_tokens"], 0)
        self.assertEqual(team_usage[sales["id"]]["usage"]["total_tokens"], 300)
        status, _, raw = self.request(
            "/admin/api/teams/usage-breakdown?window=all&team_id={}".format(
                sales["id"]
            )
        )
        self.assertEqual(status, 200)
        breakdown = json.loads(raw)
        self.assertEqual(breakdown["definition"], "team_model_reasoning_effort_tokens")
        self.assertEqual(breakdown["attribution"], "current_membership")
        self.assertEqual(breakdown["users"][0]["user"], "alice@example.com")
        self.assertEqual(breakdown["combinations"][0]["model"], "gpt-5.6-sol")
        self.assertEqual(breakdown["combinations"][0]["reasoning_effort"], "unknown")
        self.assertEqual(breakdown["totals"]["total_tokens"], 300)
        status, _, _ = self.request(
            "/admin/api/teams/usage", authenticated=False
        )
        self.assertEqual(status, 401)

        status, _, raw = self.request(
            "/admin/api/users/team/batch",
            method="POST",
            body={"users": ["bob@example.com"], "team_id": platform["id"]},
        )
        self.assertEqual(status, 200)
        self.assertEqual(len(json.loads(raw)["assignments"]), 1)
        status, _, raw = self.request(
            "/admin/api/teams?id={}".format(platform["id"]),
            method="DELETE",
        )
        self.assertEqual(status, 400)
        self.assertIn("不能删除", raw.decode("utf-8"))

    def test_organization_bulk_filters_conflicts_and_tag_membership_updates(self):
        alice_records = self.control.create_user("alice@example.com", apply=False)
        self.control.create_user("bob@example.com", apply=False)
        status, _, raw = self.request(
            "/admin/api/teams", method="POST", body={"name": "Prototype"}
        )
        self.assertEqual(status, 201)
        prototype = json.loads(raw)["team"]
        status, _, raw = self.request(
            "/admin/api/teams", method="POST", body={"name": "Existing"}
        )
        self.assertEqual(status, 201)
        existing = json.loads(raw)["team"]
        status, _, raw = self.request(
            "/admin/api/tags",
            method="POST",
            body={"name": "Pilot", "color": "#336699"},
        )
        self.assertEqual(status, 201)
        pilot = json.loads(raw)["tag"]

        now = int(time.time())
        self.app.usage_store.sync_identities(self.control._read_registry(), now=now)
        self.app.usage_store.ingest_events(
            alice_records[0]["account"],
            [{
                "timestamp": now - 10,
                "model": "gpt-5.6-sol",
                "api_key": alice_records[0]["key"],
                "request_id": "organization-used-user",
                "failed": False,
                "tokens": {"total_tokens": 123},
            }],
            now=now,
        )
        status, _, raw = self.request(
            "/admin/api/users?view=summary&window=all&team_id=unassigned"
            "&usage_state=used&page_size=25"
        )
        self.assertEqual(status, 200)
        used = json.loads(raw)
        self.assertEqual(used["pagination"]["total"], 1)
        self.assertEqual(used["users"][0]["email"], "alice@example.com")

        status, _, _ = self.request(
            "/admin/api/users/team/batch",
            method="POST",
            body={
                "users": ["alice@example.com"],
                "team_id": prototype["id"],
                "expected_team_id": None,
            },
        )
        self.assertEqual(status, 200)
        status, _, _ = self.request(
            "/admin/api/users/team",
            method="PUT",
            body={"email": "bob@example.com", "team_id": existing["id"]},
        )
        self.assertEqual(status, 200)
        status, _, raw = self.request(
            "/admin/api/users/team/batch",
            method="POST",
            body={
                "users": ["bob@example.com"],
                "team_id": prototype["id"],
                "expected_team_id": None,
            },
        )
        self.assertEqual(status, 409)
        self.assertEqual(json.loads(raw)["error"]["code"], "team_membership_conflict")

        status, _, _ = self.request(
            "/admin/api/users/tags/batch",
            method="POST",
            body={
                "users": ["alice@example.com", "bob@example.com"],
                "tag_id": pilot["id"],
                "assigned": True,
            },
        )
        self.assertEqual(status, 200)
        status, _, raw = self.request(
            "/admin/api/users?view=summary&window=all&tag_id={}"
            "&tag_membership=tagged&page_size=25".format(pilot["id"])
        )
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(raw)["pagination"]["total"], 2)
        status, _, _ = self.request(
            "/admin/api/users/tags/batch",
            method="POST",
            body={
                "users": ["bob@example.com"],
                "tag_id": pilot["id"],
                "assigned": False,
            },
        )
        self.assertEqual(status, 200)
        status, _, raw = self.request(
            "/admin/api/users?view=summary&window=all&tag_id={}"
            "&tag_membership=untagged&page_size=25".format(pilot["id"])
        )
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(raw)["pagination"]["total"], 1)
        self.assertEqual(json.loads(raw)["users"][0]["email"], "bob@example.com")

    def test_user_summary_relative_window_cache_is_stable_across_seconds(self):
        self.control.create_user("alice@example.com", apply=False)
        original = self.app.usage_store.usage_summaries_for_users
        self.app.usage_store.usage_summaries_for_users = mock.Mock(
            wraps=original
        )

        with mock.patch.object(
            self.server_module,
            "utc_timestamp",
            side_effect=[1_700_000_000, 1_700_000_002],
        ):
            first = self.app.user_management_page(86400)
            second = self.app.user_management_page(86400)

        self.assertEqual(first["users"], second["users"])
        self.assertFalse(first["summary_cached"])
        self.assertTrue(second["summary_cached"])
        self.assertEqual(second["summary_generated_at"], 1_700_000_000)
        self.app.usage_store.usage_summaries_for_users.assert_called_once_with(
            window_seconds=86400,
            now=1_700_000_000,
            start_at=None,
            end_at=None,
        )

    def test_user_summary_manual_refresh_bypasses_both_cache_layers(self):
        self.control.create_user("alice@example.com", apply=False)
        original = self.app.usage_store.usage_summaries_for_users
        self.app.usage_store.usage_summaries_for_users = mock.Mock(
            wraps=original
        )

        with mock.patch.object(
            self.server_module,
            "utc_timestamp",
            return_value=1_700_000_000,
        ):
            first = self.app.user_management_page(86400)
            cached = self.app.user_management_page(86400)
            refreshed = self.app.user_management_page(
                86400,
                force_refresh=True,
            )

        self.assertFalse(first["summary_cached"])
        self.assertTrue(cached["summary_cached"])
        self.assertFalse(refreshed["summary_cached"])
        self.assertEqual(
            self.app.usage_store.usage_summaries_for_users.call_count,
            2,
        )

    def test_user_summary_today_window_uses_midnight_and_does_not_share_all_cache(self):
        self.control.create_user("alice@example.com", apply=False)
        original = self.app.usage_store.usage_summaries_for_users
        self.app.usage_store.usage_summaries_for_users = mock.Mock(
            wraps=original
        )
        now = 1_700_000_000
        today_start = self.app._usage_window_context("today", now=now)[
            "window_start_at"
        ]

        with mock.patch.object(
            self.server_module,
            "utc_timestamp",
            side_effect=[now, now],
        ):
            today = self.app.user_management_page("today")
            all_history = self.app.user_management_page(None)

        self.assertFalse(today["summary_cached"])
        self.assertFalse(all_history["summary_cached"])
        self.assertEqual(
            self.app.usage_store.usage_summaries_for_users.call_args_list,
            [
                mock.call(
                    window_seconds=None,
                    now=now,
                    start_at=today_start,
                    end_at=None,
                ),
                mock.call(
                    window_seconds=None,
                    now=now,
                    start_at=None,
                    end_at=None,
                ),
            ],
        )

    def test_user_summary_quota_sort_uses_weighted_weekly_tokens(self):
        alice_records = self.control.create_user("alice@example.com", apply=False)
        bob_records = self.control.create_user("bob@example.com", apply=False)
        now = int(time.time())
        self.app.usage_store.sync_identities(self.control._read_registry(), now=now)
        for record, request_id, tokens in (
            (alice_records[0], "quota-alice", 900),
            (bob_records[0], "quota-bob", 300),
        ):
            self.app.usage_store.ingest_events(
                record["account"],
                [{
                    "timestamp": now - 30,
                    "latency_ms": 100,
                    "provider": "openai",
                    "model": "gpt-5.6-sol",
                    "endpoint": "POST /v1/responses",
                    "api_key": record["key"],
                    "request_id": request_id,
                    "failed": False,
                    "tokens": {
                        "input_tokens": tokens,
                        "output_tokens": 0,
                        "reasoning_tokens": 0,
                        "cached_tokens": 0,
                        "total_tokens": tokens,
                    },
                }],
                now=now,
            )

        payload = self.app.user_management_page(3600, sort="quota", direction="desc")

        self.assertEqual(
            [item["email"] for item in payload["users"][:2]],
            ["alice@example.com", "bob@example.com"],
        )
        self.assertGreater(
            payload["users"][0]["weekly_quota"]["used_tokens"],
            payload["users"][1]["weekly_quota"]["used_tokens"],
        )

    def test_user_management_returns_user_and_account_usage_without_raw_keys(self):
        records = self.control.create_user("alice@example.com", apply=False)
        now = int(time.time())
        self.app.usage_store.sync_identities(self.control._read_registry(), now=now)
        self.app.usage_store.ingest_events(
            records[0]["account"],
            [
                {
                    "timestamp": now - 30,
                    "latency_ms": 200,
                    "provider": "openai",
                    "model": "gpt-5.6-sol",
                    "endpoint": "POST /v1/responses",
                    "api_key": records[0]["key"],
                    "request_id": "request-1",
                    "failed": False,
                    "tokens": {
                        "input_tokens": 120,
                        "output_tokens": 30,
                        "reasoning_tokens": 10,
                        "cached_tokens": 40,
                        "total_tokens": 150,
                    },
                }
            ],
            now=now,
        )
        self.app.usage_store.update_collector_status(now=now)

        status, _, raw = self.request("/admin/api/users?window=3600")

        self.assertEqual(status, 200)
        payload = json.loads(raw)
        self.assertEqual(payload["window_seconds"], 3600)
        self.assertEqual(payload["collector"]["status"], "healthy")
        user = payload["users"][0]
        self.assertEqual(user["usage"]["request_count"], 1)
        self.assertEqual(user["usage"]["total_tokens"], 150)
        self.assertEqual(user["accounts"][0]["usage"]["input_tokens"], 120)
        self.assertEqual(user["accounts"][0]["usage"]["cached_tokens"], 40)
        self.assertNotIn(records[0]["key"], raw.decode("utf-8"))

    def test_user_management_accepts_all_window_and_rejects_unknown_window(self):
        self.control.create_user("alice@example.com", apply=False)

        status, _, raw = self.request("/admin/api/users?window=all")
        self.assertEqual(status, 200)
        self.assertIsNone(json.loads(raw)["window_seconds"])

        status, _, raw = self.request("/admin/api/users?window=300")
        self.assertEqual(status, 400)
        self.assertIn("统计范围无效", raw.decode("utf-8"))

    def test_account_and_user_management_support_custom_time_ranges(self):
        records = self.control.create_user("alice@example.com", apply=False)
        account = records[0]["account"]
        now = int(time.time())
        start_at = now - 300
        end_at = now - 100
        self.app.usage_store.sync_identities(self.control._read_registry(), now=now)
        self.app.usage_store.ensure_usage_breakdown_started(now=start_at - 60)
        self.app.usage_store.ingest_events(
            account,
            [
                {
                    "timestamp": start_at - 1,
                    "model": "gpt-5.6-sol",
                    "alias": "gpt-5.6-sol",
                    "api_key": records[0]["key"],
                    "request_id": "before-custom-range",
                    "failed": False,
                    "tokens": {"total_tokens": 10},
                },
                {
                    "timestamp": start_at,
                    "model": "gpt-5.6-sol",
                    "alias": "gpt-5.6-sol",
                    "api_key": records[0]["key"],
                    "request_id": "at-custom-start",
                    "failed": False,
                    "tokens": {"total_tokens": 20},
                },
                {
                    "timestamp": end_at - 1,
                    "model": "gpt-5.6-sol",
                    "alias": "gpt-5.6-sol",
                    "api_key": records[0]["key"],
                    "request_id": "before-custom-end",
                    "failed": False,
                    "tokens": {"total_tokens": 30},
                },
                {
                    "timestamp": end_at,
                    "model": "gpt-5.6-sol",
                    "alias": "gpt-5.6-sol",
                    "api_key": records[0]["key"],
                    "request_id": "at-custom-end",
                    "failed": False,
                    "tokens": {"total_tokens": 40},
                },
            ],
            now=now,
        )
        custom_query = f"window=custom&start_at={start_at}&end_at={end_at}"

        status, _, raw = self.request(f"/admin/api/users?{custom_query}")
        self.assertEqual(status, 200)
        users = json.loads(raw)
        self.assertEqual(users["window"], "custom")
        self.assertEqual(users["window_start_at"], start_at)
        self.assertEqual(users["window_end_at"], end_at)
        self.assertEqual(users["window_seconds"], end_at - start_at)
        self.assertEqual(users["users"][0]["usage"]["request_count"], 2)
        self.assertEqual(users["users"][0]["usage"]["total_tokens"], 50)

        self.app.usage_limits = mock.Mock(return_value={"accounts": []})
        self.app._compose_ps = mock.Mock(return_value=[])
        status, _, raw = self.request(f"/admin/api/accounts?{custom_query}")
        self.assertEqual(status, 200)
        accounts = json.loads(raw)
        target = next(item for item in accounts["accounts"] if item["id"] == account)
        self.assertEqual(accounts["window"], "custom")
        self.assertEqual(accounts["window_start_at"], start_at)
        self.assertEqual(accounts["window_end_at"], end_at)
        self.assertEqual(target["usage"]["request_count"], 2)
        self.assertEqual(target["usage"]["total_tokens"], 50)

        status, _, raw = self.request(
            "/admin/api/users/usage-breakdown"
            f"?email=alice%40example.com&{custom_query}"
        )
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(raw)["totals"]["request_count"], 2)

        status, _, raw = self.request(
            "/admin/api/accounts/usage-breakdown"
            f"?account={account}&{custom_query}"
        )
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(raw)["totals"]["request_count"], 2)

        status, _, raw = self.request("/admin/api/users?window=custom")
        self.assertEqual(status, 400)
        self.assertIn("自定义统计范围", raw.decode("utf-8"))

        status, _, raw = self.request(
            f"/admin/api/accounts?window=custom&start_at={end_at}&end_at={start_at}"
        )
        self.assertEqual(status, 400)
        self.assertIn("开始时间必须早于结束时间", raw.decode("utf-8"))

        status, _, raw = self.request(
            f"/admin/api/users?window=custom&start_at={start_at}&end_at={now + 120}"
        )
        self.assertEqual(status, 400)
        self.assertIn("结束时间不能晚于当前时间", raw.decode("utf-8"))

    def test_overview_usage_returns_account_and_user_token_series(self):
        records = self.control.create_user("alice@example.com", apply=False)
        now = int(time.time())
        self.app.usage_store.sync_identities(self.control._read_registry(), now=now)
        self.app.usage_store.ingest_events(
            records[0]["account"],
            [
                {
                    "timestamp": now - 30,
                    "api_key": records[0]["key"],
                    "request_id": "overview-usage-1",
                    "endpoint": "POST /v1/responses",
                    "failed": False,
                    "tokens": {"total_tokens": 321},
                }
            ],
            now=now,
        )
        self.app.usage_store.update_collector_status(now=now)

        status, _, raw = self.request(
            "/admin/api/overview/usage?window=3600"
            "&account={}&user=alice%40example.com".format(records[0]["account"])
        )

        self.assertEqual(status, 200)
        payload = json.loads(raw)
        self.assertEqual(payload["window_seconds"], 3600)
        self.assertEqual(payload["bucket_seconds"], 60)
        self.assertEqual(payload["selected_account"], records[0]["account"])
        self.assertEqual(payload["selected_user"], "alice@example.com")
        self.assertEqual(payload["collector"]["status"], "healthy")
        self.assertEqual(len(payload["accounts"]), 1)
        self.assertEqual(payload["accounts"][0]["total"], 321)
        self.assertEqual(payload["users"][0]["total"], 321)
        self.assertEqual(sum(payload["accounts"][0]["values"]), 321)
        self.assertNotIn(records[0]["key"], raw.decode("utf-8"))

        status, _, raw = self.request("/admin/api/overview/usage?window=300")
        self.assertEqual(status, 400)
        self.assertIn("趋势时间范围无效", raw.decode("utf-8"))

        status, _, raw = self.request(
            "/admin/api/overview/usage?window=3600&account=missing"
        )
        self.assertEqual(status, 404)
        self.assertEqual(json.loads(raw)["error"]["code"], "account_not_found")

        status, _, raw = self.request(
            "/admin/api/overview/usage?window=3600&user=missing%40example.com"
        )
        self.assertEqual(status, 404)
        self.assertEqual(json.loads(raw)["error"]["code"], "user_not_found")

    def test_overview_catalog_is_complete_lightweight_and_uses_canonical_status(self):
        self.control.create_user("alice@example.com", apply=False)
        self.control.create_user("bob@example.com", apply=False)
        self.control.revoke_user("bob@example.com", apply=False)
        accounts = self.control.accounts()
        now = int(time.time())
        self.app._compose_ps = mock.Mock(
            return_value=[
                {
                    "service": "cliproxy-{}".format(account),
                    "state": "running",
                    "status": "Up",
                    "health": "healthy",
                }
                for account in accounts
            ]
        )
        self.control.auth_status = mock.Mock(
            return_value={account: {"files": 1} for account in accounts}
        )
        self.app.usage_limits = mock.Mock(
            return_value={
                "generated_at": now,
                "accounts": [
                    {
                        "account": account,
                        "status": "ok",
                        "allowed": True,
                        "weekly": {
                            "limit_reached": False,
                            "remaining_percent": 75,
                        },
                    }
                    for account in accounts
                ],
            }
        )
        self.app._cached_cpa_management_snapshots = mock.Mock(
            return_value={
                account: {
                    "query_status": "ok",
                    "credential_status": "active",
                }
                for account in accounts
            }
        )
        self.app._gateway_error_activity = mock.Mock(return_value={})

        status, _, raw = self.request("/admin/api/overview/catalog")

        self.assertEqual(status, 200)
        payload = json.loads(raw)
        self.assertEqual(
            [item["id"] for item in payload["accounts"]],
            list(accounts),
        )
        self.assertTrue(
            all(
                set(item) == {"id", "operational_status"}
                for item in payload["accounts"]
            )
        )
        self.assertTrue(
            all(
                item["operational_status"]["code"] == "available"
                for item in payload["accounts"]
            )
        )
        self.assertEqual(
            payload["users"],
            [
                {"email": "alice@example.com", "status": "active"},
                {"email": "bob@example.com", "status": "inactive"},
            ],
        )
        self.assertTrue(
            all(set(item) == {"email", "status"} for item in payload["users"])
        )
        self.assertNotIn("quota", raw.decode("utf-8"))
        self.assertNotIn("weekly_quota", raw.decode("utf-8"))
        self.assertNotIn("key", raw.decode("utf-8"))

        status, _, _ = self.request(
            "/admin/api/overview/catalog", authenticated=False
        )
        self.assertEqual(status, 401)

    def test_overview_usage_supports_end_exclusive_custom_range(self):
        records = self.control.create_user("alice@example.com", apply=False)
        account = records[0]["account"]
        now = int(time.time())
        start_at = now - 300
        end_at = now - 60
        self.app.usage_store.sync_identities(self.control._read_registry(), now=now)
        self.app.usage_store.ingest_events(
            account,
            [
                {
                    "timestamp": start_at,
                    "api_key": records[0]["key"],
                    "request_id": "overview-custom-start",
                    "failed": False,
                    "tokens": {"total_tokens": 20},
                },
                {
                    "timestamp": end_at - 1,
                    "api_key": records[0]["key"],
                    "request_id": "overview-custom-before-end",
                    "failed": False,
                    "tokens": {"total_tokens": 30},
                },
                {
                    "timestamp": end_at,
                    "api_key": records[0]["key"],
                    "request_id": "overview-custom-at-end",
                    "failed": False,
                    "tokens": {"total_tokens": 40},
                },
            ],
            now=now,
        )

        status, _, raw = self.request(
            "/admin/api/overview/usage"
            f"?window=custom&start_at={start_at}&end_at={end_at}"
        )

        self.assertEqual(status, 200)
        payload = json.loads(raw)
        self.assertEqual(payload["window"], "custom")
        self.assertEqual(payload["window_start_at"], start_at)
        self.assertEqual(payload["generated_at"], end_at - 1)
        self.assertEqual(payload["window_seconds"], end_at - start_at)
        self.assertLessEqual(len(payload["buckets"]), 360)
        self.assertEqual(payload["accounts"][0]["total"], 50)
        self.assertEqual(payload["users"][0]["total"], 50)

        status, _, raw = self.request("/admin/api/overview/usage?window=custom")
        self.assertEqual(status, 400)
        self.assertIn("自定义统计范围", raw.decode("utf-8"))

    def test_overview_counts_active_users_without_building_user_matrix(self):
        self.control.create_user("alice@example.com", apply=False)
        self.app.users = mock.Mock(
            side_effect=AssertionError("overview must not build the user usage matrix")
        )
        self.app._compose_ps = mock.Mock(return_value=[])
        self.control.inflight_stats = mock.Mock(
            return_value={
                account: {"count": 0, "labels": [], "users": []}
                for account in self.control.accounts()
            }
        )

        payload = self.app.overview()

        self.assertEqual(payload["summary"]["users"], 1)
        self.app.users.assert_not_called()

    def test_overview_does_not_expose_inflight_command_failure(self):
        self.app._compose_ps = mock.Mock(return_value=[])
        self.control.inflight_stats = mock.Mock(
            side_effect=RuntimeError(
                "Command docker compose exec gateway wget /opt/private returned 8"
            )
        )

        payload = self.app.overview()

        self.assertEqual(payload["warnings"], ["实时请求统计暂不可用，请稍后刷新"])
        self.assertNotIn("docker", payload["warnings"][0])
        self.assertNotIn("/opt/", payload["warnings"][0])

    def test_overview_usage_since_reset_uses_each_account_quota_period(self):
        records = self.control.create_user("alice@example.com", apply=False)
        now = 200_000
        period_start = now - 3_600
        target_account = records[0]["account"]
        self.app.usage_store.sync_identities(self.control._read_registry(), now=now)
        self.app.usage_store.ingest_events(
            target_account,
            [
                {
                    "timestamp": now - 7_200,
                    "api_key": records[0]["key"],
                    "request_id": "overview-before-period",
                    "failed": False,
                    "tokens": {"total_tokens": 900},
                },
                {
                    "timestamp": now - 1_800,
                    "api_key": records[0]["key"],
                    "request_id": "overview-in-period",
                    "failed": False,
                    "tokens": {"total_tokens": 100},
                },
            ],
            now=now,
        )
        self.app.usage_limits = mock.Mock(
            return_value={
                "accounts": [
                    {
                        "account": account,
                        "weekly": (
                            {
                                "window_seconds": self.server_module.WEEKLY_WINDOW_SECONDS,
                                "reset_at": period_start + self.server_module.WEEKLY_WINDOW_SECONDS,
                            }
                            if account == target_account
                            else None
                        ),
                    }
                    for account in self.control.accounts()
                ]
            }
        )

        with mock.patch.object(self.server_module, "utc_timestamp", return_value=now):
            payload = self.app.overview_usage("since_reset")

        self.assertEqual(payload["window"], "since_reset")
        self.assertEqual(payload["window_seconds"], self.server_module.WEEKLY_WINDOW_SECONDS)
        self.assertEqual(payload["bucket_seconds"], 3_600)
        self.assertEqual(
            payload["window_start_at_by_account"][target_account],
            period_start,
        )
        self.assertEqual(payload["unavailable_accounts"], [
            account for account in self.control.accounts() if account != target_account
        ])
        self.assertEqual(len(payload["accounts"]), 1)
        self.assertEqual(payload["accounts"][0]["name"], target_account)
        self.assertEqual(payload["accounts"][0]["total"], 100)
        self.assertEqual(payload["users"][0]["total"], 100)

    def test_admin_manages_persistent_user_weekly_quota_policy(self):
        self.control.create_user("alice@example.com", apply=False)
        self.control.update_configuration({"user_quota.default_weekly_tokens": 1000})

        status, _, raw = self.request(
            "/admin/api/users/quota?email=alice%40example.com"
        )
        self.assertEqual(status, 200)
        inherited = json.loads(raw)["weekly_quota"]
        self.assertEqual(inherited["policy_mode"], "inherit")
        self.assertEqual(inherited["limit_tokens"], 1000)

        status, _, _ = self.request(
            "/admin/api/users/quota",
            method="PUT",
            body={"email": "alice@example.com", "mode": "custom", "weekly_tokens": 500},
            authenticated=False,
        )
        self.assertEqual(status, 401)

        status, _, raw = self.request(
            "/admin/api/users/quota",
            method="PUT",
            body={"email": "alice@example.com", "mode": "custom", "weekly_tokens": 500},
        )
        self.assertEqual(status, 200)
        custom = json.loads(raw)["weekly_quota"]
        self.assertEqual(custom["policy_mode"], "custom")
        self.assertEqual(custom["limit_tokens"], 500)
        self.assertTrue(custom["personal_policy_reset_enabled"])
        self.assertEqual(custom["policy_reset_at"], custom["week_end_at"])

        status, _, raw = self.request("/admin/api/users")
        self.assertEqual(status, 200)
        self.assertEqual(
            json.loads(raw)["users"][0]["weekly_quota"]["source"],
            "user_custom",
        )

        status, _, raw = self.request(
            "/admin/api/settings/configuration",
            method="POST",
            body={
                "values": {
                    "user_quota.reset_personal_weekly_on_new_week": False
                },
                "confirm": "save",
            },
        )
        self.assertEqual(status, 200)
        self.assertIn(
            "user_quota.reset_personal_weekly_on_new_week",
            json.loads(raw)["changed"],
        )
        status, _, raw = self.request(
            "/admin/api/users/quota?email=alice%40example.com"
        )
        self.assertEqual(status, 200)
        persistent = json.loads(raw)["weekly_quota"]
        self.assertFalse(persistent["personal_policy_reset_enabled"])
        self.assertIsNone(persistent["policy_reset_at"])
        self.assertEqual(persistent["policy_mode"], "custom")

        status, _, raw = self.request(
            "/admin/api/users/quota?email=alice%40example.com",
            method="DELETE",
        )
        self.assertEqual(status, 200)
        cleared = json.loads(raw)["weekly_quota"]
        self.assertEqual(cleared["policy_mode"], "inherit")
        self.assertEqual(cleared["limit_tokens"], 1000)

    def test_admin_adjusts_single_bulk_and_all_user_weekly_quota_without_deleting_usage(self):
        alice = self.control.create_user("alice@example.com", apply=False)
        self.control.create_user("bob@example.com", apply=False)
        self.control.update_configuration(
            {"user_quota.default_weekly_tokens": 1000}
        )
        now = int(time.time())
        self.app.usage_store.sync_identities(
            self.control._read_registry(),
            now=now,
        )
        self.app.usage_store.ingest_events(
            alice[0]["account"],
            [
                {
                    "timestamp": now - 30,
                    "provider": "openai",
                    "model": "gpt-5.6-sol",
                    "endpoint": "POST /v1/responses",
                    "api_key": alice[0]["key"],
                    "request_id": "quota-adjustment-1",
                    "reasoning_effort": "max",
                    "failed": False,
                    "tokens": {
                        "input_tokens": 120,
                        "output_tokens": 30,
                        "total_tokens": 150,
                    },
                }
            ],
            now=now,
        )

        status, _, _ = self.request(
            "/admin/api/users/quota-actions",
            method="POST",
            body={
                "action": "add_bonus",
                "scope": "selected",
                "users": ["alice@example.com"],
                "token_amount": 200,
                "reason": "临时项目扩容",
                "confirm": "add_bonus",
            },
            authenticated=False,
        )
        self.assertEqual(status, 401)

        status, _, raw = self.request(
            "/admin/api/users/quota-actions",
            method="POST",
            body={
                "action": "add_bonus",
                "scope": "selected",
                "users": ["alice@example.com"],
                "token_amount": 200,
                "reason": "临时项目扩容",
                "confirm": "add_bonus",
            },
        )
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(raw)["token_amount"], 200)

        status, _, raw = self.request(
            "/admin/api/users/quota?email=alice%40example.com"
        )
        self.assertEqual(status, 200)
        quota_payload = json.loads(raw)
        self.assertEqual(quota_payload["weekly_quota"]["limit_tokens"], 1200)
        self.assertEqual(quota_payload["weekly_quota"]["used_tokens"], 300)
        self.assertEqual(
            quota_payload["weekly_quota"]["weighted_used_tokens"],
            300,
        )
        self.assertEqual(quota_payload["weekly_quota"]["raw_used_tokens"], 150)
        self.assertEqual(
            quota_payload["weekly_quota"]["weighted_raw_used_tokens"],
            300,
        )
        self.assertEqual(
            quota_payload["weekly_quota"]["quota_unit"],
            "weighted_tokens",
        )
        self.assertEqual(quota_payload["adjustments"][0]["action"], "bonus")

        status, _, raw = self.request(
            "/admin/api/users/quota-actions",
            method="POST",
            body={
                "action": "reset_usage",
                "scope": "selected",
                "users": ["alice@example.com"],
                "reason": "异常流量补偿",
                "confirm": "reset_current_week_usage",
            },
        )
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(raw)["token_amount"], 300)

        status, _, raw = self.request(
            "/admin/api/users/quota?email=alice%40example.com"
        )
        quota_payload = json.loads(raw)
        self.assertEqual(quota_payload["weekly_quota"]["raw_used_tokens"], 150)
        self.assertEqual(
            quota_payload["weekly_quota"]["weighted_raw_used_tokens"],
            300,
        )
        self.assertEqual(quota_payload["weekly_quota"]["used_tokens"], 0)
        self.assertEqual(
            [item["action"] for item in quota_payload["adjustments"]],
            ["usage_reset", "bonus"],
        )

        self.app.usage_store.set_quota_policy(
            "alice@example.com",
            "custom",
            500,
        )
        self.app.usage_store.set_quota_policy(
            "bob@example.com",
            "unlimited",
        )
        status, _, raw = self.request(
            "/admin/api/users/quota-actions",
            method="POST",
            body={
                "action": "restore_default",
                "scope": "selected",
                "users": [
                    "alice@example.com",
                    "bob@example.com",
                ],
                "confirm": "restore_default",
            },
        )
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(raw)["changed_policies"], 2)
        quotas = self.app.usage_store.weekly_quotas(
            ["alice@example.com", "bob@example.com"],
            1000,
        )
        self.assertEqual(quotas["alice@example.com"]["policy_mode"], "inherit")
        self.assertEqual(quotas["bob@example.com"]["policy_mode"], "inherit")

        status, _, raw = self.request("/admin/api/settings")
        summary = json.loads(raw)["user_quota_operations"]
        self.assertEqual(summary["total_users"], 2)
        self.assertEqual(summary["users_with_usage"], 0)
        self.assertEqual(summary["total_used_tokens"], 0)
        self.assertEqual(summary["total_raw_used_tokens"], 150)
        self.assertEqual(summary["users_with_bonus"], 1)
        self.assertEqual(summary["users_with_usage_reset"], 1)

        status, _, raw = self.request(
            "/admin/api/users/quota-actions",
            method="POST",
            body={
                "action": "reset_usage",
                "scope": "all",
                "reason": "系统异常统一补偿",
                "confirm": "wrong",
            },
        )
        self.assertEqual(status, 400)
        self.assertEqual(
            json.loads(raw)["error"]["code"],
            "invalid_request",
        )
        self.app.usage_store.ingest_events(
            alice[0]["account"],
            [
                {
                    "timestamp": now + 1,
                    "provider": "openai",
                    "model": "gpt-5.6-sol",
                    "endpoint": "POST /v1/responses",
                    "api_key": alice[0]["key"],
                    "request_id": "quota-adjustment-2",
                    "failed": False,
                    "tokens": {"total_tokens": 25},
                }
            ],
            now=now + 1,
        )
        status, _, raw = self.request(
            "/admin/api/users/quota-actions",
            method="POST",
            body={
                "action": "reset_usage",
                "scope": "all",
                "reason": "系统异常统一补偿",
                "confirm": "reset_all_current_week_usage",
            },
        )
        self.assertEqual(status, 200)
        all_reset = json.loads(raw)
        self.assertEqual(all_reset["applied_users"], ["alice@example.com"])
        self.assertEqual(all_reset["skipped_users"], ["bob@example.com"])
        self.assertEqual(
            all_reset["quota_operations"]["total_used_tokens"],
            0,
        )

    def test_user_usage_breakdown_excludes_history_before_collection_start(self):
        records = self.control.create_user("alice@example.com", apply=False)
        now = int(time.time())
        start_at = now - 120
        self.app.usage_store.sync_identities(self.control._read_registry(), now=now)
        self.app.usage_store.ingest_events(
            records[0]["account"],
            [
                {
                    "timestamp": start_at - 1,
                    "model": "gpt-5.4",
                    "reasoning_effort": "medium",
                    "api_key": records[0]["key"],
                    "request_id": "old-model-request",
                    "failed": False,
                    "tokens": {"reasoning_tokens": 10, "total_tokens": 100},
                }
            ],
            now=now,
        )
        self.app.usage_store.ensure_usage_breakdown_started(now=start_at)
        self.app.usage_store.ingest_events(
            records[0]["account"],
            [
                {
                    "timestamp": start_at,
                    "model": "gpt-5.6-sol",
                    "alias": "gpt-5.6-sol",
                    "reasoning_effort": "xhigh",
                    "api_key": records[0]["key"],
                    "request_id": "new-sol-request",
                    "failed": False,
                    "tokens": {"reasoning_tokens": 30, "total_tokens": 300},
                },
                {
                    "timestamp": start_at + 1,
                    "model": "gpt-5.6-terra",
                    "alias": "gpt-5.6-terra",
                    "reasoning_effort": "high",
                    "api_key": records[0]["key"],
                    "request_id": "new-terra-request",
                    "failed": False,
                    "tokens": {"reasoning_tokens": 20, "total_tokens": 200},
                },
                {
                    "timestamp": start_at + 2,
                    "model": "gpt-5.6-sol",
                    "reasoning_effort": "xhigh",
                    "api_key": records[0]["key"],
                    "request_id": "failed-sol-request",
                    "failed": True,
                    "tokens": {"reasoning_tokens": 0, "total_tokens": 0},
                },
            ],
            now=now,
        )

        status, _, raw = self.request(
            "/admin/api/users/usage-breakdown"
            "?email=alice%40example.com&window=3600"
        )

        self.assertEqual(status, 200)
        payload = json.loads(raw)
        self.assertEqual(payload["definition"], "successful_model_requests")
        self.assertEqual(payload["collection_started_at"], start_at)
        self.assertEqual(payload["totals"]["success_count"], 2)
        self.assertEqual(payload["totals"]["failed_count"], 1)
        self.assertEqual(payload["totals"]["known_effort_count"], 2)
        self.assertEqual(
            [(item["model"], item["success_count"]) for item in payload["models"]],
            [("gpt-5.6-sol", 1), ("gpt-5.6-terra", 1)],
        )
        self.assertNotIn("gpt-5.4", raw.decode("utf-8"))
        self.assertNotIn(records[0]["key"], raw.decode("utf-8"))

        status, _, raw = self.request(
            "/admin/api/users/usage-breakdown"
            "?email=alice%40example.com&window=3600&account=missing"
        )
        self.assertEqual(status, 404)
        self.assertEqual(json.loads(raw)["error"]["code"], "account_not_found")

    def test_account_usage_breakdown_is_raw_only_and_uses_account_period(self):
        records = self.control.create_user("alice@example.com", apply=False)
        account = records[0]["account"]
        now = 200_000
        period_start = now - 3_600
        self.app.usage_store.sync_identities(self.control._read_registry(), now=now)
        self.app.usage_store.ensure_usage_breakdown_started(now=period_start - 60)
        self.app.usage_store.ingest_events(
            account,
            [
                {
                    "timestamp": period_start - 1,
                    "model": "gpt-5.6-sol",
                    "alias": "gpt-5.6-sol",
                    "reasoning_effort": "xhigh",
                    "api_key": records[0]["key"],
                    "request_id": "before-account-period-breakdown",
                    "failed": False,
                    "tokens": {"total_tokens": 900},
                },
                {
                    "timestamp": period_start,
                    "model": "gpt-5.6-sol",
                    "alias": "gpt-5.6-sol",
                    "reasoning_effort": "xhigh",
                    "api_key": records[0]["key"],
                    "request_id": "inside-account-period-breakdown",
                    "failed": False,
                    "tokens": {
                        "input_tokens": 100,
                        "output_tokens": 30,
                        "reasoning_tokens": 20,
                        "cached_tokens": 40,
                        "total_tokens": 130,
                    },
                },
            ],
            now=now,
        )
        self.app.usage_limits = mock.Mock(
            return_value={
                "accounts": [
                    {
                        "account": account,
                        "weekly": {
                            "window_seconds": self.server_module.WEEKLY_WINDOW_SECONDS,
                            "reset_at": period_start + self.server_module.WEEKLY_WINDOW_SECONDS,
                        },
                    }
                ]
            }
        )

        with mock.patch.object(self.server_module, "utc_timestamp", return_value=now):
            status, _, raw = self.request(
                "/admin/api/accounts/usage-breakdown"
                f"?account={account}&window=since_reset"
            )

        self.assertEqual(status, 200)
        payload = json.loads(raw)
        self.assertEqual(payload["definition"], "account_model_reasoning_effort_tokens")
        self.assertEqual(payload["account"], account)
        self.assertEqual(payload["window"], "since_reset")
        self.assertEqual(payload["window_start_at"], period_start)
        self.assertEqual(payload["totals"]["request_count"], 1)
        self.assertEqual(payload["totals"]["input_tokens"], 100)
        self.assertEqual(payload["totals"]["output_tokens"], 30)
        self.assertEqual(payload["totals"]["reasoning_tokens"], 20)
        self.assertEqual(payload["totals"]["cached_tokens"], 40)
        self.assertEqual(payload["totals"]["total_tokens"], 130)
        self.assertEqual(payload["combinations"][0]["model"], "gpt-5.6-sol")
        self.assertEqual(payload["combinations"][0]["reasoning_effort"], "xhigh")
        response_text = raw.decode("utf-8")
        self.assertNotIn(records[0]["key"], response_text)
        self.assertNotIn("alice@example.com", response_text)
        self.assertNotIn("weighted", response_text)
        self.assertNotIn("multiplier", response_text)
        self.app.usage_limits.assert_called_once_with()

        status, _, raw = self.request(
            "/admin/api/accounts/usage-breakdown?account=missing&window=3600"
        )
        self.assertEqual(status, 404)
        self.assertEqual(json.loads(raw)["error"]["code"], "account_not_found")

        status, _, raw = self.request(
            f"/admin/api/accounts/usage-breakdown?account={account}&window=3600",
            authenticated=False,
        )
        self.assertEqual(status, 401)
        self.assertEqual(json.loads(raw)["error"]["code"], "unauthorized")

        self.app.usage_limits = mock.Mock(
            return_value={"accounts": [{"account": account, "weekly": None}]}
        )
        with mock.patch.object(self.server_module, "utc_timestamp", return_value=now):
            status, _, raw = self.request(
                "/admin/api/accounts/usage-breakdown"
                f"?account={account}&window=since_reset"
            )
        self.assertEqual(status, 409)
        self.assertEqual(
            json.loads(raw)["error"]["code"],
            "usage_window_unavailable",
        )

    def test_today_window_uses_configured_timezone_across_all_usage_apis(self):
        self.control.update_configuration(
            {"user_quota.timezone": "Asia/Shanghai"}
        )
        self.app.usage_store.set_week_timezone("Asia/Shanghai")
        records = self.control.create_user("alice@example.com", apply=False)
        self.app.usage_store.set_portal_credential(
            "alice@example.com",
            "unused-by-today-window-test",
            must_change=False,
        )
        zone = timezone(timedelta(hours=8))
        now = int(datetime(2026, 7, 21, 12, 0, tzinfo=zone).timestamp())
        today_start = int(datetime(2026, 7, 21, 0, 0, tzinfo=zone).timestamp())
        self.app.usage_store.sync_identities(self.control._read_registry(), now=now)
        self.app.usage_store.ingest_events(
            records[0]["account"],
            [
                {
                    "timestamp": today_start - 1,
                    "api_key": records[0]["key"],
                    "request_id": "before-today",
                    "failed": False,
                    "tokens": {"total_tokens": 100},
                },
                {
                    "timestamp": today_start,
                    "api_key": records[0]["key"],
                    "request_id": "today-boundary",
                    "failed": False,
                    "tokens": {"total_tokens": 200},
                },
            ],
            now=now,
        )
        self.app._compose_ps = mock.Mock(return_value=[])
        self.app.usage_limits = mock.Mock(return_value={"accounts": []})
        self.control.auth_status = mock.Mock(return_value={})

        with mock.patch.object(
            self.server_module, "utc_timestamp", return_value=now
        ), mock.patch.object(
            self.server_module,
            "verify_portal_password",
            return_value=True,
        ):
            status, _, raw = self.request("/admin/api/users?window=today")
            self.assertEqual(status, 200)
            users = json.loads(raw)

            status, _, raw = self.request("/admin/api/accounts?window=today")
            self.assertEqual(status, 200)
            accounts = json.loads(raw)

            status, _, raw = self.request(
                "/admin/api/overview/usage?window=today"
            )
            self.assertEqual(status, 200)
            overview_usage = json.loads(raw)

            status, headers, _ = self.request(
                "/usage/session",
                method="POST",
                body={"email": "alice@example.com", "password": "portal-secret-123"},
                authenticated=False,
            )
            self.assertEqual(status, 201)
            cookie = headers["Set-Cookie"].split(";", 1)[0]
            status, _, raw = self.request(
                "/usage/me?window=today",
                authenticated=False,
                extra_headers={"Cookie": cookie},
            )
            self.assertEqual(status, 200)
            dashboard = json.loads(raw)

        for payload in (users, accounts, dashboard):
            self.assertEqual(payload["window"], "today")
            self.assertIsNone(payload["window_seconds"])
            self.assertEqual(payload["window_start_at"], today_start)
            self.assertEqual(payload["generated_at"], now)
        self.assertEqual(overview_usage["window"], "today")
        self.assertIsNone(overview_usage["window_seconds"])
        self.assertEqual(overview_usage["window_start_at"], today_start)
        self.assertIsNone(overview_usage["window_start_at_by_account"])
        self.assertEqual(overview_usage["generated_at"], now)
        self.assertEqual(overview_usage["bucket_seconds"], 15 * 60)
        overview_account = next(
            item
            for item in overview_usage["accounts"]
            if item["name"] == records[0]["account"]
        )
        self.assertEqual(overview_account["total"], 200)
        self.assertEqual(overview_usage["users"][0]["total"], 200)
        self.assertEqual(users["users"][0]["usage"]["total_tokens"], 200)
        account = next(
            item for item in accounts["accounts"] if item["id"] == records[0]["account"]
        )
        self.assertEqual(account["usage"]["total_tokens"], 200)

        self.app.usage_limits.reset_mock()
        status, _, _ = self.request("/admin/api/accounts?window=today&fresh=1")
        self.assertEqual(status, 200)
        status, _, _ = self.request(
            "/usage/me?window=today&fresh=1",
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 200)
        self.assertEqual(
            self.app.usage_limits.call_args_list,
            [mock.call(force_refresh=True), mock.call(force_refresh=True)],
        )
        group = next(
            item for item in dashboard["groups"] if item["id"] == records[0]["account"]
        )
        self.assertEqual(group["usage"]["total_tokens"], 200)

        next_midnight = today_start + 24 * 60 * 60
        with mock.patch.object(
            self.server_module,
            "utc_timestamp",
            return_value=next_midnight,
        ):
            midnight_overview = self.app.overview_usage("today")
        self.assertEqual(midnight_overview["window_start_at"], next_midnight)
        self.assertEqual(midnight_overview["buckets"], [next_midnight])

    def test_current_week_window_uses_user_quota_timezone_for_users_and_teams(self):
        self.control.update_configuration(
            {"user_quota.timezone": "Asia/Shanghai"}
        )
        self.app.usage_store.set_week_timezone("Asia/Shanghai")
        records = self.control.create_user("alice@example.com", apply=False)
        team = self.control.store.create_team("Platform")
        self.control.store.set_user_teams(["alice@example.com"], team["id"])
        zone = timezone(timedelta(hours=8))
        now = int(datetime(2026, 7, 22, 12, 0, tzinfo=zone).timestamp())
        week_start = int(datetime(2026, 7, 20, 0, 0, tzinfo=zone).timestamp())
        week_end = int(datetime(2026, 7, 27, 0, 0, tzinfo=zone).timestamp())
        self.app.usage_store.sync_identities(self.control._read_registry(), now=now)
        self.app.usage_store.sync_user_teams(
            self.control.store.read_user_classifications(["alice@example.com"])
        )
        self.app.usage_store.ensure_usage_breakdown_started(now=week_start - 60)
        self.app.usage_store.ingest_events(
            records[0]["account"],
            [
                {
                    "timestamp": week_start - 1,
                    "model": "gpt-5.6-sol",
                    "api_key": records[0]["key"],
                    "request_id": "before-current-week",
                    "failed": False,
                    "tokens": {"total_tokens": 100},
                },
                {
                    "timestamp": week_start,
                    "model": "gpt-5.6-sol",
                    "api_key": records[0]["key"],
                    "request_id": "inside-current-week",
                    "failed": False,
                    "tokens": {"total_tokens": 200},
                },
            ],
            now=now,
        )

        with mock.patch.object(
            self.server_module, "utc_timestamp", return_value=now
        ):
            status, _, raw = self.request(
                "/admin/api/teams/usage?window=current_week"
            )
            self.assertEqual(status, 200)
            teams = json.loads(raw)
            status, _, raw = self.request(
                "/admin/api/users?view=summary&window=current_week"
                f"&team_id={team['id']}"
            )
            self.assertEqual(status, 200)
            users = json.loads(raw)
            status, _, raw = self.request(
                "/admin/api/teams/usage-breakdown?window=current_week"
                f"&team_id={team['id']}"
            )
            self.assertEqual(status, 200)
            breakdown = json.loads(raw)

        for payload in (teams, users, breakdown):
            self.assertEqual(payload["window"], "current_week")
            self.assertIsNone(payload["window_seconds"])
            self.assertEqual(payload["window_start_at"], week_start)
            self.assertEqual(payload["window_end_at"], week_end)
            self.assertEqual(payload["window_timezone"], "Asia/Shanghai")
        team_usage = next(item for item in teams["teams"] if item["id"] == team["id"])
        self.assertEqual(team_usage["usage"]["total_tokens"], 200)
        self.assertEqual(users["users"][0]["usage"]["total_tokens"], 200)
        self.assertEqual(breakdown["totals"]["total_tokens"], 200)
        self.assertEqual(breakdown["series"]["end_at"], now)

    def test_legacy_key_preview_remains_compatible(self):
        record = {
            "label": "alice@example.com:gamma",
            "account": "gamma",
            "account_email": "gamma@accounts.example.com",
            "user": "alice@example.com",
            "status": "active",
            "key": "cpa_" + "a" * 64,
            "created_at": 123,
            "updated_at": 123,
        }

        payload = self.app.key_payload(record)

        self.assertEqual(payload["preview"], "cpa_••••aaaa")
        self.assertNotIn("key", payload)

    def test_self_service_key_rotation_requires_session_and_confirmation(self):
        records = self.control.create_user("alice@example.com", apply=False)
        old_key = records[0]["key"]
        self.app.usage_store.set_portal_credential(
            "alice@example.com",
            self.server_module.hash_portal_password("alice-secret-123"),
            must_change=False,
        )
        self.control.publish_auth_snapshot = mock.Mock(
            return_value={"generation": "b" * 32, "records": 1}
        )

        status, _, raw = self.request(
            "/usage/me/key/rotate",
            method="POST",
            body={"confirm": True},
            authenticated=False,
        )
        self.assertEqual(status, 401)
        self.assertEqual(json.loads(raw)["error"]["code"], "session_required")

        status, headers, _ = self.request(
            "/usage/session",
            method="POST",
            body={"email": "alice@example.com", "password": "alice-secret-123"},
            authenticated=False,
        )
        self.assertEqual(status, 201)
        cookie = headers["Set-Cookie"].split(";", 1)[0]

        status, _, raw = self.request(
            "/usage/me/key/rotate",
            method="POST",
            body={"confirm": False},
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 400)
        self.assertEqual(json.loads(raw)["error"]["code"], "confirmation_required")
        self.assertEqual({item["key"] for item in self.control.active_records()}, {old_key})
        self.control.publish_auth_snapshot.assert_not_called()

        status, _, raw = self.request(
            "/usage/me/key/rotate",
            method="POST",
            body={"confirm": True, "email": "bob@example.com"},
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 200)
        payload = json.loads(raw)
        self.assertEqual(payload["message"], "API Key 已刷新，旧 Key 已失效，请更新客户端配置")
        self.assertNotEqual(payload["api_key"], old_key)
        active = self.control.active_records()
        self.assertEqual(len(active), len(self.control.accounts()))
        self.assertEqual({item["user"] for item in active}, {"alice@example.com"})
        self.assertEqual({item["key"] for item in active}, {payload["api_key"]})
        self.assertTrue(
            all(
                item["status"] == "rotated"
                for item in self.control._read_registry()
                if item["key"] == old_key
            )
        )
        self.control.publish_auth_snapshot.assert_called_once_with(wait=False)

    def test_portal_record_lookup_only_reads_the_current_user(self):
        records = self.control.create_user("alice@example.com", apply=False)
        inactive = dict(records[0], status="rotated", label="z-inactive")
        unordered = [records[-1], inactive, *records[:-1]]

        with mock.patch.object(
            self.control.store,
            "read_key_records_for_users",
            return_value=unordered,
        ) as read_records, mock.patch.object(
            self.control,
            "active_records",
            side_effect=AssertionError("portal lookup must not scan every key"),
        ):
            active = self.app._active_records_for_portal_user("alice@example.com")

        read_records.assert_called_once_with(["alice@example.com"])
        self.assertEqual(len(active), len(records))
        self.assertEqual(
            [item["label"] for item in active],
            sorted(item["label"] for item in records),
        )

    def test_self_service_lifetime_cache_refreshes_expired_values_in_background(self):
        user = "alice@example.com"
        accounts = ("alpha", "beta")
        cache_key = (user, accounts)
        stale = {"request_count": 2, "total_tokens": 100}
        fresh = {"request_count": 3, "total_tokens": 250}
        self.app.self_service_lifetime_cache[cache_key] = (time.monotonic() - 1, stale)
        refresh_started = threading.Event()
        release_refresh = threading.Event()

        def slow_load(_user, _accounts):
            refresh_started.set()
            release_refresh.wait(timeout=2)
            return fresh

        with mock.patch.object(
            self.app,
            "_load_self_service_lifetime_usage",
            side_effect=slow_load,
        ) as load:
            started_at = time.monotonic()
            result = self.app._cached_self_service_lifetime_usage(user, accounts)
            elapsed = time.monotonic() - started_at

            self.assertEqual(result, stale)
            self.assertLess(elapsed, 0.5)
            self.assertTrue(refresh_started.wait(timeout=1))
            self.assertEqual(
                self.app._cached_self_service_lifetime_usage(user, accounts),
                stale,
            )
            load.assert_called_once_with(user, accounts)

            release_refresh.set()
            deadline = time.monotonic() + 2
            while cache_key in self.app.self_service_lifetime_refreshing:
                self.assertLess(time.monotonic(), deadline)
                time.sleep(0.01)

            self.assertEqual(
                self.app._cached_self_service_lifetime_usage(user, accounts),
                fresh,
            )
            load.assert_called_once_with(user, accounts)

    def test_portal_session_exposes_one_key_groups_and_supports_logout(self):
        alice = self.control.create_user("alice@example.com", apply=False)
        initial_password = "random-initial-secret"
        self.app.usage_store.set_portal_credential(
            "alice@example.com",
            self.server_module.hash_portal_password(initial_password),
            must_change=True,
        )
        self.control._reload_gateway_if_running = mock.Mock()
        self.control.publish_auth_snapshot = mock.Mock(
            return_value={"generation": "a" * 32, "records": 1}
        )
        weekly = {
            "status": "ok",
            "allowed": True,
            "limit_reached": False,
            "weekly": {"used_percent": 30, "remaining_percent": 70, "reset_at": 1784618552},
        }
        self.app.usage_limits = mock.Mock(
            return_value={"accounts": [{**weekly, "account": account} for account in self.control.accounts()]}
        )
        self.control.auth_status = mock.Mock(
            return_value={account: {"files": 1} for account in self.control.accounts()}
        )
        self.app._compose_ps = mock.Mock(
            return_value=[
                {"service": self.control.services()[account], "state": "running"}
                for account in self.control.accounts()
            ]
        )
        self.app._cached_cpa_management_snapshots = mock.Mock(
            return_value={
                account: {
                    "query_status": "ok",
                    "credential_status": "active",
                    "credential_unavailable": False,
                    "status_message": "",
                }
                for account in self.control.accounts()
            }
        )
        self.app._gateway_error_activity = mock.Mock(return_value={})
        self.app.usage_store.sync_identities(alice)
        self.app.usage_store.ingest_events(
            "alpha",
            [
                {
                    "timestamp": int(time.time()) - 2 * 24 * 60 * 60,
                    "endpoint": "POST /v1/responses",
                    "api_key": alice[0]["key"],
                    "request_id": "portal-lifetime-usage",
                    "tokens": {"total_tokens": 321},
                }
            ],
        )
        self.app.usage_store.ingest_events(
            "beta",
            [
                {
                    "timestamp": int(time.time()) - 60,
                    "endpoint": "POST /v1/responses",
                    "api_key": alice[1]["key"],
                    "request_id": "portal-today-usage",
                    "tokens": {"input_tokens": 80, "output_tokens": 20, "total_tokens": 100},
                }
            ],
        )

        status, _, raw = self.request("/usage/me", authenticated=False)
        self.assertEqual(status, 401)

        self.assertEqual(json.loads(raw)["error"]["code"], "session_required")
        status, _, raw = self.request("/usage/me/route", authenticated=False)
        self.assertEqual(status, 401)
        self.assertEqual(json.loads(raw)["error"]["code"], "session_required")

        status, headers, raw = self.request(
            "/usage/session",
            method="POST",
            body={"email": "Alice@Example.com", "password": initial_password},
            authenticated=False,
        )
        self.assertEqual(status, 201)
        self.assertEqual(json.loads(raw)["user"], "alice@example.com")
        self.assertTrue(json.loads(raw)["password_change_required"])
        credential = self.app.usage_store.portal_credential("alice@example.com")
        self.assertTrue(credential["password_hash"].startswith("scrypt$"))
        self.assertNotIn("123456", credential["password_hash"])
        cookie = headers["Set-Cookie"].split(";", 1)[0]
        self.assertIn("HttpOnly", headers["Set-Cookie"])
        self.assertNotIn("Secure", headers["Set-Cookie"])
        self.assertIn("SameSite=Lax", headers["Set-Cookie"])

        status, _, raw = self.request(
            "/usage/me?window=86400",
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 403)
        self.assertEqual(json.loads(raw)["error"]["code"], "password_change_required")

        status, _, raw = self.request(
            "/usage/me/password",
            method="PUT",
            body={"current_password": initial_password, "new_password": "123456"},
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 400)
        self.assertEqual(json.loads(raw)["error"]["code"], "weak_password")

        status, _, raw = self.request(
            "/usage/me/password",
            method="PUT",
            body={
                "current_password": "wrong-initial-secret",
                "new_password": TEST_INITIAL_PORTAL_PASSWORD,
            },
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 401)
        self.assertEqual(json.loads(raw)["error"]["code"], "invalid_current_password")

        status, _, raw = self.request(
            "/usage/me/password",
            method="PUT",
            body={"current_password": initial_password, "new_password": initial_password},
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 400)
        self.assertEqual(json.loads(raw)["error"]["code"], "weak_password")

        status, _, raw = self.request(
            "/usage/me/password",
            method="PUT",
            body={"current_password": initial_password, "new_password": TEST_INITIAL_PORTAL_PASSWORD},
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 400)
        self.assertEqual(json.loads(raw)["error"]["code"], "weak_password")

        credential = self.app.usage_store.portal_credential("alice@example.com")
        self.assertTrue(credential["must_change"])

        status, _, raw = self.request(
            "/usage/me?window=86400",
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 403)
        self.assertEqual(json.loads(raw)["error"]["code"], "password_change_required")

        status, _, raw = self.request(
            "/usage/me/password",
            method="PUT",
            body={
                "current_password": initial_password,
                "new_password": "new-secret-123",
            },
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 200)
        self.assertFalse(json.loads(raw)["password_change_required"])

        credential = self.app.usage_store.portal_credential("alice@example.com")
        self.assertFalse(credential["must_change"])

        status, _, raw = self.request(
            "/usage/me/password",
            method="PUT",
            body={
                "current_password": "new-secret-123",
                "new_password": TEST_INITIAL_PORTAL_PASSWORD,
            },
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 400)
        self.assertEqual(json.loads(raw)["error"]["code"], "weak_password")

        status, _, raw = self.request(
            "/usage/me/password",
            method="PUT",
            body={
                "current_password": "new-secret-123",
                "new_password": "new-secret-123",
            },
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 200)
        self.assertFalse(json.loads(raw)["password_change_required"])

        status, _, raw = self.request(
            "/usage/me?window=86400",
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 200)
        payload = json.loads(raw)
        self.assertEqual(payload["api_key"], alice[0]["key"])
        self.assertEqual(payload["lifetime_usage"]["total_tokens"], 421)
        self.assertEqual(payload["window_usage"]["request_count"], 1)
        self.assertEqual(payload["window_usage"]["total_tokens"], 100)
        self.assertEqual(payload["window_usage"]["weighted_tokens"], 100)
        self.assertEqual(payload["today_usage"], payload["window_usage"])
        self.assertEqual(sum(group["usage"]["total_tokens"] for group in payload["groups"]), 100)
        self.assertTrue(payload["weekly_quota"]["unlimited"])
        self.assertEqual(payload["weekly_quota"]["policy_mode"], "inherit")
        self.assertEqual(payload["current_group"], "alpha")
        self.assertEqual(
            payload["route_assignment"],
            {
                "status": "assigned",
                "account": "alpha",
                "active_users_1h": 0,
                "routed_users": 0,
            },
        )
        self.assertEqual(len(payload["groups"]), 4)
        self.assertEqual(
            [item["id"] for item in payload["groups"] if item["current"]],
            ["alpha"],
        )
        self.assertEqual(
            {item["operational_status"]["code"] for item in payload["groups"]},
            {"available"},
        )
        self.assertTrue(
            all(item["operational_status"]["selectable"] for item in payload["groups"])
        )
        self.assertEqual(self.control._read_routes()["alice@example.com"], "alpha")
        self.control.publish_auth_snapshot.assert_called_with(wait=True)

        with mock.patch.object(
            self.app,
            "_cached_self_service_lifetime_usage",
            side_effect=AssertionError("lightweight dashboard must skip lifetime usage"),
        ):
            status, _, raw = self.request(
                "/usage/me?window=86400&lifetime=0",
                authenticated=False,
                extra_headers={"Cookie": cookie},
            )
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(raw)["lifetime_usage"], {})

        status, route_headers, raw = self.request(
            "/usage/me/route",
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 200)
        self.assertEqual(route_headers["Cache-Control"], "no-store")
        route_payload = json.loads(raw)
        self.assertEqual(route_payload["current_group"], "alpha")
        self.assertIsInstance(route_payload["generated_at"], int)
        self.assertEqual(set(route_payload), {"current_group", "generated_at"})

        quota_payload = self.app.usage_limits.return_value
        self.app.usage_limits.side_effect = lambda **unused: (
            self.control._write_routes({"alice@example.com": "beta"}),
            quota_payload,
        )[1]
        status, _, raw = self.request(
            "/usage/me?window=86400",
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 200)
        switched_during_request = json.loads(raw)
        self.assertEqual(switched_during_request["current_group"], "beta")
        self.assertEqual(
            [item["id"] for item in switched_during_request["groups"] if item["current"]],
            ["beta"],
        )
        self.control._write_routes({})
        self.app.usage_limits.side_effect = None

        status, _, raw = self.request("/admin/api/accounts?window=86400")
        self.assertEqual(status, 200)
        admin_accounts = json.loads(raw)["accounts"]
        self.assertEqual(
            {
                item["id"]: item["operational_status"]["code"]
                for item in admin_accounts
            },
            {
                item["id"]: item["operational_status"]["code"]
                for item in payload["groups"]
            },
        )

        status, _, raw = self.request(
            "/usage/me/group",
            method="PUT",
            body={"group_id": "gamma"},
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(raw)["group_id"], "gamma")
        self.assertEqual(
            self.control.explicit_user_route("alice@example.com"),
            "gamma",
        )
        key_map = self.control.gateway_key_map_path.read_text(encoding="utf-8")
        self.assertIn("alice@example.com:gamma", key_map)

        status, _, raw = self.request(
            "/usage/me?window=86400",
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 200)
        payload = json.loads(raw)
        self.assertEqual(payload["current_group"], "gamma")
        self.assertEqual(sum(item["current"] for item in payload["groups"]), 1)
        status, _, raw = self.request(
            "/usage/me/route",
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(raw)["current_group"], "gamma")

        status, headers, _ = self.request(
            "/usage/session",
            method="DELETE",
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 200)
        self.assertIn("Max-Age=0", headers["Set-Cookie"])
        status, _, _ = self.request(
            "/usage/me", authenticated=False, extra_headers={"Cookie": cookie}
        )
        self.assertEqual(status, 401)

    def test_unbound_user_auto_assignment_uses_activity_then_route_count(self):
        self.control.create_user("new-user@example.com", apply=False)
        self.control._write_routes(
            {
                "routed-a@example.com": "beta",
                "routed-b@example.com": "beta",
                "routed-c@example.com": "gamma",
            }
        )
        groups = [
            {
                "account": account,
                "operational_status": {"selectable": account != "delta"},
            }
            for account in self.control.accounts()
        ]
        activity = {
            "alpha": {"active_users": 2},
            "beta": {"active_users": 1},
            "gamma": {"active_users": 1},
            "delta": {"active_users": 0},
        }

        with mock.patch.object(
            self.control,
            "set_user_routes",
            return_value={"moved_users": 1},
        ) as set_routes:
            account, assignment = self.app._auto_assign_self_service_route(
                "new-user@example.com",
                self.control.accounts(),
                groups,
                activity,
            )

        self.assertEqual(account, "gamma")
        self.assertEqual(assignment["active_users_1h"], 1)
        self.assertEqual(assignment["routed_users"], 1)
        set_routes.assert_called_once_with(
            {"new-user@example.com": "gamma"},
            wait_for_gateway=True,
        )

    def test_unbound_user_remains_unassigned_when_no_account_is_selectable(self):
        self.control.create_user("new-user@example.com", apply=False)
        groups = [
            {"account": account, "operational_status": {"selectable": False}}
            for account in self.control.accounts()
        ]

        with mock.patch.object(self.control, "set_user_routes") as set_routes:
            account, assignment = self.app._auto_assign_self_service_route(
                "new-user@example.com",
                self.control.accounts(),
                groups,
                {},
            )

        self.assertIsNone(account)
        self.assertEqual(assignment["status"], "unavailable")
        set_routes.assert_not_called()

    def test_legacy_email_query_is_removed_and_unknown_session_user_is_rejected(self):
        status, _, raw = self.request(
            "/my-keys/api",
            method="POST",
            body={"email": "alice@example.com"},
            authenticated=False,
        )
        self.assertEqual(status, 410)
        self.assertEqual(json.loads(raw)["error"]["code"], "email_lookup_removed")

        status, _, raw = self.request(
            "/usage/session",
            method="POST",
            body={"email": "missing@example.com", "password": "123456"},
            authenticated=False,
        )
        self.assertEqual(status, 401)
        self.assertEqual(json.loads(raw)["error"]["code"], "invalid_credentials")

        self.control.create_user("no-credential@example.com", apply=False)
        status, _, raw = self.request(
            "/usage/session",
            method="POST",
            body={"email": "no-credential@example.com", "password": "123456"},
            authenticated=False,
        )
        self.assertEqual(status, 401)
        self.assertEqual(json.loads(raw)["error"]["code"], "invalid_credentials")
        self.assertIsNone(
            self.app.usage_store.portal_credential("no-credential@example.com")
        )

    def test_startup_disables_legacy_shared_default_credentials(self):
        self.control.create_user("legacy@example.com", apply=False)
        self.app.usage_store.set_portal_credential(
            "legacy@example.com",
            self.server_module.hash_portal_password("123456"),
            must_change=True,
        )
        session = self.app.usage_store.create_session("legacy@example.com")

        restarted = self.server_module.AdminApplication(
            root=self.root,
            key_file=self.key_file,
            control=self.control,
        )

        self.assertIsNone(restarted.usage_store.portal_credential("legacy@example.com"))
        self.assertIsNone(restarted.usage_store.resolve_session(session["token"]))

    def test_admin_can_reset_portal_password_and_invalidate_existing_sessions(self):
        self.control.create_user("alice@example.com", apply=False)
        self.app.usage_store.set_portal_credential(
            "alice@example.com",
            self.server_module.hash_portal_password("old-secret-123"),
            must_change=False,
        )
        status, headers, raw = self.request(
            "/usage/session",
            method="POST",
            body={"email": "alice@example.com", "password": "old-secret-123"},
            authenticated=False,
        )
        self.assertEqual(status, 201)
        self.assertFalse(json.loads(raw)["password_change_required"])
        cookie = headers["Set-Cookie"].split(";", 1)[0]

        status, _, raw = self.request(
            "/admin/api/users/reset-password",
            method="POST",
            body={"email": "alice@example.com"},
        )
        self.assertEqual(status, 200)
        reset_payload = json.loads(raw)
        initial_password = reset_payload["initial_password"]
        self.assertEqual(initial_password, TEST_INITIAL_PORTAL_PASSWORD)
        self.assertNotIn("123456", reset_payload["message"])

        status, _, raw = self.request(
            "/usage/me",
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 401)
        self.assertEqual(json.loads(raw)["error"]["code"], "session_required")

        status, _, _ = self.request(
            "/usage/session",
            method="POST",
            body={"email": "alice@example.com", "password": "old-secret-123"},
            authenticated=False,
        )
        self.assertEqual(status, 401)
        status, _, raw = self.request(
            "/usage/session",
            method="POST",
            body={"email": "alice@example.com", "password": initial_password},
            authenticated=False,
        )
        self.assertEqual(status, 201)
        self.assertTrue(json.loads(raw)["password_change_required"])

    def test_self_service_usage_breakdown_is_scoped_to_session_user_and_account(self):
        records = self.control.create_user("alice@example.com", apply=False)
        now = int(time.time())
        account = records[0]["account"]
        self.app.usage_store.sync_identities(records, now=now - 120)
        self.app.usage_store.ensure_usage_breakdown_started(now=now - 120)
        self.app.usage_store.ingest_events(
            account,
            [
                {
                    "timestamp": now - 30,
                    "api_key": records[0]["key"],
                    "request_id": "portal-model-breakdown",
                    "model": "gpt-5.6-sol",
                    "alias": "gpt-5.6-sol",
                    "reasoning_effort": "xhigh",
                    "failed": False,
                    "tokens": {
                        "input_tokens": 100,
                        "output_tokens": 40,
                        "reasoning_tokens": 10,
                        "cached_tokens": 20,
                        "total_tokens": 140,
                    },
                }
            ],
            now=now,
        )
        self.app.usage_store.set_portal_credential(
            "alice@example.com",
            self.server_module.hash_portal_password("portal-secret-123"),
            must_change=False,
            now=now,
        )
        session = self.app.usage_store.create_session("alice@example.com", now=now)
        cookie = "{}={}".format(
            self.server_module.PORTAL_SESSION_COOKIE,
            session["token"],
        )

        status, _, raw = self.request(
            "/usage/me/usage-breakdown?account={}&window=3600".format(account),
            authenticated=False,
        )
        self.assertEqual(status, 401)
        self.assertEqual(json.loads(raw)["error"]["code"], "session_required")

        status, _, raw = self.request(
            "/usage/me/usage-breakdown?account={}&window=3600&email=bob%40example.com".format(account),
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 200)
        payload = json.loads(raw)
        self.assertNotIn("user", payload)
        self.assertEqual(payload["account"], account)
        self.assertEqual(
            payload["definition"],
            "user_account_model_reasoning_effort_tokens",
        )
        self.assertEqual(payload["totals"]["request_count"], 1)
        self.assertEqual(payload["totals"]["total_tokens"], 140)
        self.assertEqual(payload["totals"]["weighted_tokens"], 140)
        self.assertEqual(
            payload["combinations"][0]["reasoning_effort"],
            "xhigh",
        )

        status, _, raw = self.request(
            "/usage/me/usage-breakdown?window=3600",
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 400)
        self.assertEqual(json.loads(raw)["error"]["code"], "account_required")

        status, _, raw = self.request(
            "/usage/me/usage-breakdown?account=missing&window=3600",
            authenticated=False,
            extra_headers={"Cookie": cookie},
        )
        self.assertEqual(status, 404)
        self.assertEqual(json.loads(raw)["error"]["code"], "account_not_found")

    def test_operational_status_distinguishes_quota_from_invalid_credentials(self):
        quota = {
            "status": "ok",
            "allowed": True,
            "limit_reached": False,
            "weekly": {
                "remaining_percent": 70,
                "limit_reached": False,
            },
        }
        base = {
            "group_enabled": True,
            "container_state": "running",
            "auth_files": 1,
            "quota": quota,
        }

        quota_exhausted = self.app._account_operational_status(
            **base,
            runtime={
                "state": "unavailable",
                "status_message": json.dumps(
                    {"error": {"type": "usage_limit_reached"}}
                ),
            },
        )
        self.assertEqual(quota_exhausted["code"], "quota_exhausted")
        self.assertEqual(quota_exhausted["label"], "额度耗尽")
        self.assertFalse(quota_exhausted["selectable"])

        invalid_credential = self.app._account_operational_status(
            **base,
            runtime={
                "state": "unavailable",
                "status_message": json.dumps(
                    {"error": {"code": "refresh_token_invalidated"}}
                ),
            },
        )
        self.assertEqual(
            invalid_credential["code"],
            "credential_unavailable",
        )
        self.assertEqual(invalid_credential["label"], "凭据不可用")
        self.assertFalse(invalid_credential["selectable"])
        self.assertTrue(
            self.app._runtime_unavailable_due_to_invalid_credential(
                {"status_message": "unauthorized"}
            )
        )

        transient_cooldown = self.app._account_operational_status(
            **base,
            runtime={
                "state": "unavailable",
                "credential_status": "unavailable",
                "credential_disabled": False,
                "status_message": json.dumps(
                    {
                        "error": {
                            "code": "service_unavailable",
                            "type": "server_error",
                        }
                    }
                ),
            },
        )
        self.assertEqual(transient_cooldown["code"], "transient_cooldown")
        self.assertEqual(transient_cooldown["label"], "临时冷却")
        self.assertEqual(transient_cooldown["tone"], "warning")
        self.assertTrue(transient_cooldown["selectable"])

        unknown_unavailable = self.app._account_operational_status(
            **base,
            runtime={
                "state": "unavailable",
                "credential_status": "unavailable",
                "credential_disabled": False,
                "status_message": "unrecognized credential failure",
            },
        )
        self.assertEqual(unknown_unavailable["code"], "credential_unavailable")
        self.assertFalse(unknown_unavailable["selectable"])

        disabled_credential = self.app._account_operational_status(
            **base,
            runtime={
                "state": "unavailable",
                "credential_status": "unavailable",
                "credential_disabled": True,
                "status_message": "",
            },
        )
        self.assertEqual(disabled_credential["code"], "credential_unavailable")
        self.assertFalse(disabled_credential["selectable"])

        rate_limited = self.app._account_operational_status(
            **base,
            runtime={"state": "rate_limited", "status_message": ""},
        )
        self.assertEqual(rate_limited["code"], "rate_limited")
        self.assertEqual(rate_limited["tone"], "warning")
        self.assertTrue(rate_limited["selectable"])

        missing_native_credential = self.app._account_runtime_snapshot(
            {
                "query_status": "ok",
                "credential_status": "missing",
                "credential_unavailable": False,
            },
            {},
            int(time.time()),
        )
        self.assertEqual(missing_native_credential["state"], "unavailable")
        missing_status = self.app._account_operational_status(
            **base,
            runtime=missing_native_credential,
        )
        self.assertEqual(missing_status["code"], "credential_unavailable")
        self.assertFalse(missing_status["selectable"])

    def test_usage_limits_is_public_cached_and_never_returns_oauth_secrets(self):
        weekly = {
            "account": "alpha",
            "status": "ok",
            "plan_type": "pro",
            "allowed": True,
            "limit_reached": False,
            "weekly": {
                "used_percent": 38.0,
                "remaining_percent": 62.0,
                "reset_at": 1784618552,
                "reset_after_seconds": 600000,
                "window_seconds": 604800,
                "resettable": True,
            },
            "weekly_windows": [
                {
                    "key": "default:primary_window",
                    "label": "常规周限额",
                    "reset_at": 1784618552,
                    "window_seconds": 604800,
                    "resettable": True,
                }
            ],
            "reset_credits": {
                "available_count": 3,
                "applicable_available_count": 1,
                "credits": [
                    {
                        "id": "admin-only-credit-id",
                        "status": "available",
                        "expires_at": 1786579200,
                    }
                ],
            },
        }
        self.app._fetch_account_usage_limit = mock.Mock(side_effect=lambda account: {**weekly, "account": account})

        status, headers, raw = self.request("/usage/limits", authenticated=False)
        self.assertEqual(status, 200)
        self.assertEqual(headers["Cache-Control"], "no-store")
        payload = json.loads(raw)
        self.assertFalse(payload["cached"])
        self.assertTrue(payload["refreshing"])
        self.assertEqual(payload["cache_ttl_seconds"], 60)
        self.assertEqual(payload["accounts"], [])

        deadline = time.time() + 1
        while self.app.usage_limit_refreshing and time.time() < deadline:
            time.sleep(0.01)
        self.assertFalse(self.app.usage_limit_refreshing)

        status, _, second_raw = self.request("/usage/limits", authenticated=False)
        self.assertEqual(status, 200)
        second_payload = json.loads(second_raw)
        self.assertTrue(second_payload["cached"])
        self.assertEqual(len(second_payload["accounts"]), 4)
        self.assertTrue(
            all(
                item["weekly"]["window_seconds"] == 604800
                for item in second_payload["accounts"]
            )
        )
        self.assertEqual(self.app._fetch_account_usage_limit.call_count, 4)
        throttled = self.app.usage_limits(force_refresh=True)
        self.assertTrue(throttled["cached"])
        self.assertEqual(self.app._fetch_account_usage_limit.call_count, 4)
        self.app.usage_limit_cache["generated_at"] = int(time.time()) - 16
        refresh_started = threading.Event()
        release_refresh = threading.Event()

        def slow_refresh(account):
            refresh_started.set()
            release_refresh.wait(1)
            return {**weekly, "account": account}

        self.app._fetch_account_usage_limit.side_effect = slow_refresh
        refresh_call_started_at = time.monotonic()
        refreshed = self.app.usage_limits(force_refresh=True)
        refresh_call_elapsed = time.monotonic() - refresh_call_started_at
        self.assertTrue(refreshed["cached"])
        self.assertTrue(refreshed["refreshing"])
        self.assertLess(refresh_call_elapsed, 0.5)
        self.assertTrue(refresh_started.wait(1))
        release_refresh.set()
        deadline = time.time() + 1
        while self.app.usage_limit_refreshing and time.time() < deadline:
            time.sleep(0.01)
        self.assertFalse(self.app.usage_limit_refreshing)
        self.assertEqual(self.app._fetch_account_usage_limit.call_count, 8)
        self.assertFalse(self.app.usage_limit_cache["cached"])
        for secret_name in ("access_token", "refresh_token", "id_token", "proxy-url", "Bearer"):
            self.assertNotIn(secret_name, second_raw.decode("utf-8"))
        self.assertNotIn("reset_credits", second_raw.decode("utf-8"))
        self.assertNotIn("admin-only-credit-id", second_raw.decode("utf-8"))
        self.assertNotIn("resettable", second_raw.decode("utf-8"))

    def test_usage_limit_reads_account_oauth_and_normalizes_weekly_window(self):
        auth_dir = self.root / "auth" / "alpha"
        auth_dir.mkdir(parents=True, exist_ok=True)
        (auth_dir / "codex-test.json").write_text(
            json.dumps(
                {
                    "type": "codex",
                    "disabled": False,
                    "access_token": "secret-access-token",
                    "refresh_token": "secret-refresh-token",
                    "account_id": "account-123",
                }
            ),
            encoding="utf-8",
        )
        (self.root / "configs" / "alpha.yaml").write_text(
            'proxy-url: "http://192.0.2.10:16169"\n',
            encoding="utf-8",
        )
        official_payload = {
            "plan_type": "pro",
            "rate_limit": {
                "allowed": True,
                "limit_reached": False,
                "primary_window": {
                    "limit_window_seconds": 604800,
                    "used_percent": 38,
                    "reset_at": 1784618552,
                    "reset_after_seconds": 600000,
                },
            },
            "additional_rate_limits": [],
            "rate_limit_reset_credits": {"available_count": 3},
        }
        reset_credit_details = {
            "available_count": 2,
            "total_earned_count": 3,
            "credits": [
                {
                    "id": "credit-2",
                    "reset_type": "codex_rate_limits",
                    "status": "available",
                    "granted_at": "2026-07-14T00:00:00Z",
                    "expires_at": "2026-08-14T00:00:00Z",
                    "title": "Full reset",
                    "description": "Second reset",
                },
                {
                    "id": "credit-1",
                    "reset_type": "codex_rate_limits",
                    "status": "available",
                    "granted_at": "2026-07-13T00:00:00Z",
                    "expires_at": "2026-08-13T00:00:00Z",
                    "title": "Full reset",
                    "description": "First reset",
                },
            ],
        }
        self.app._request_official_usage = mock.Mock(return_value=official_payload)
        self.app._request_official_reset_credits = mock.Mock(
            return_value=reset_credit_details
        )

        result = self.app._fetch_account_usage_limit("alpha")

        self.assertEqual(result["status"], "ok")
        self.assertEqual(result["plan_type"], "pro")
        self.assertEqual(result["weekly"]["used_percent"], 38.0)
        self.assertEqual(result["weekly"]["remaining_percent"], 62.0)
        self.assertEqual(result["weekly"]["window_seconds"], 604800)
        self.assertEqual(result["reset_credits"]["available_count"], 2)
        self.assertEqual(result["reset_credits"]["total_earned_count"], 3)
        self.assertEqual(
            [credit["id"] for credit in result["reset_credits"]["credits"]],
            ["credit-1", "credit-2"],
        )
        self.assertEqual(result["reset_credits"]["credits"][0]["expires_at"], 1786579200)
        self.app._request_official_usage.assert_called_once_with(
            "secret-access-token",
            "account-123",
            "http://192.0.2.10:16169",
        )
        self.app._request_official_reset_credits.assert_called_once_with(
            "secret-access-token",
            "account-123",
            "http://192.0.2.10:16169",
        )
        serialized = json.dumps(result)
        self.assertNotIn("secret-access-token", serialized)
        self.assertNotIn("secret-refresh-token", serialized)
        self.assertNotIn("192.0.2.10", serialized)

    def test_usage_limit_background_refresh_cannot_restore_invalidated_cache(self):
        accounts = self.control.accounts()
        self.app.usage_limit_cache = {
            "generated_at": int(time.time()) - 20,
            "cache_ttl_seconds": 60,
            "cached": False,
            "accounts": [],
        }
        self.app.usage_limit_cache_fingerprint = self.app._usage_limit_source_fingerprint(accounts)
        self.app.usage_limit_cache_expires_at = time.monotonic() + 60
        refresh_started = threading.Event()
        release_refresh = threading.Event()
        refresh_finished = threading.Event()

        def slow_load(account_names, cache_seconds):
            refresh_started.set()
            release_refresh.wait(1)
            refresh_finished.set()
            return {
                "generated_at": int(time.time()),
                "cache_ttl_seconds": cache_seconds,
                "cached": False,
                "accounts": [],
            }

        with mock.patch.object(self.app, "_load_usage_limits", side_effect=slow_load):
            payload = self.app.usage_limits(force_refresh=True)
            self.assertTrue(payload["refreshing"])
            self.assertTrue(refresh_started.wait(1))
            self.app._invalidate_usage_limit_cache()
            release_refresh.set()
            self.assertTrue(refresh_finished.wait(1))

        self.assertFalse(self.app.usage_limit_refreshing)
        self.assertIsNone(self.app.usage_limit_cache)
        self.assertIsNone(self.app.usage_limit_cache_fingerprint)

    def test_usage_limit_source_change_returns_stale_cache_without_waiting(self):
        account_names = list(self.control.accounts())
        retained_account = account_names[0]
        self.app.usage_limit_cache = {
            "generated_at": int(time.time()) - 120,
            "cache_ttl_seconds": 60,
            "cached": False,
            "accounts": [
                {"account": retained_account, "status": "ok"},
                {"account": "removed-account", "status": "ok"},
            ],
        }
        self.app.usage_limit_cache_fingerprint = ("old-source",)
        self.app.usage_limit_cache_expires_at = 0
        refresh_started = threading.Event()
        release_refresh = threading.Event()

        def slow_load(requested_accounts, cache_seconds):
            refresh_started.set()
            self.assertTrue(release_refresh.wait(2))
            return {
                "generated_at": int(time.time()),
                "cache_ttl_seconds": cache_seconds,
                "cached": False,
                "accounts": [
                    {"account": account, "status": "ok"}
                    for account in requested_accounts
                ],
            }

        with mock.patch.object(self.app, "_load_usage_limits", side_effect=slow_load) as load:
            started_at = time.monotonic()
            first = self.app.usage_limits()
            elapsed = time.monotonic() - started_at
            self.assertLess(elapsed, 0.5)
            self.assertTrue(first["cached"])
            self.assertTrue(first["refreshing"])
            self.assertEqual(
                [item["account"] for item in first["accounts"]],
                [retained_account],
            )
            self.assertTrue(refresh_started.wait(1))

            second = self.app.usage_limits()
            self.assertEqual(second["accounts"], first["accounts"])
            self.assertEqual(load.call_count, 1)
            release_refresh.set()
            deadline = time.time() + 2
            while self.app.usage_limit_refreshing and time.time() < deadline:
                time.sleep(0.01)

        self.assertFalse(self.app.usage_limit_refreshing)
        self.assertEqual(
            [item["account"] for item in self.app.usage_limit_cache["accounts"]],
            account_names,
        )

    def test_synchronous_usage_limit_refresh_replaces_cache_for_manual_rebalance(self):
        fresh = {
            "generated_at": int(time.time()),
            "cache_ttl_seconds": 60,
            "cached": False,
            "accounts": [{"account": "alpha", "status": "ok"}],
        }
        self.app.usage_limit_cache = {"generated_at": 1, "accounts": []}
        previous_generation = self.app.usage_limit_cache_generation
        self.app._load_usage_limits = mock.Mock(return_value=fresh)

        result = self.app.refresh_usage_limits_sync()

        self.assertEqual(result["accounts"], fresh["accounts"])
        self.assertFalse(result["refreshing"])
        self.assertEqual(
            self.app.usage_limit_cache_generation,
            previous_generation + 1,
        )
        self.assertEqual(self.app.usage_limit_cache, fresh)
        self.assertFalse(self.app.usage_limit_refreshing)
        self.app._load_usage_limits.assert_called_once_with(
            list(self.control.accounts()),
            60,
        )

    def test_usage_limit_degrades_when_auth_or_official_schema_is_unavailable(self):
        self.assertEqual(self.app._fetch_account_usage_limit("alpha")["status"], "auth_missing")

        auth_dir = self.root / "auth" / "alpha"
        auth_dir.mkdir(parents=True, exist_ok=True)
        (auth_dir / "codex-test.json").write_text(
            json.dumps({"type": "codex", "access_token": "secret-access-token"}),
            encoding="utf-8",
        )
        self.app._request_official_usage = mock.Mock(return_value={"rate_limit": None})
        self.app._request_official_reset_credits = mock.Mock(
            side_effect=urllib.error.URLError("temporary")
        )
        result = self.app._fetch_account_usage_limit("alpha")
        self.assertEqual(result["status"], "weekly_unavailable")
        self.assertIsNone(result["weekly"])
        self.app._request_official_reset_credits.assert_not_called()

    def test_usage_limit_normalizes_all_weekly_reset_dates_and_only_marks_applicable_window(self):
        payload = {
            "plan_type": "pro",
            "rate_limit": {
                "allowed": False,
                "limit_reached": True,
                "primary_window": {
                    "limit_window_seconds": 604800,
                    "used_percent": 100,
                    "reset_at": 1784781238,
                    "reset_after_seconds": 500000,
                },
            },
            "additional_rate_limits": [
                {
                    "limit_name": "GPT-5.3-Codex-Spark",
                    "metered_feature": "codex_bengalfox",
                    "rate_limit": {
                        "allowed": True,
                        "limit_reached": False,
                        "primary_window": {
                            "limit_window_seconds": 604800,
                            "used_percent": 0,
                            "reset_at": 1784792761,
                            "reset_after_seconds": 510000,
                        },
                    },
                }
            ],
            "rate_limit_reached_type": {
                "type": "rate_limit_reached",
                "details": "default",
            },
            "rate_limit_reset_credits": {
                "available_count": 5,
                "applicable_available_count": 1,
            },
        }

        result = self.app._normalize_usage_limit_payload("gamma", payload)

        self.assertEqual(result["status"], "ok")
        self.assertEqual(result["reset_credits"]["available_count"], 5)
        self.assertEqual(result["reset_credits"]["applicable_available_count"], 1)
        self.assertEqual(
            [window["reset_at"] for window in result["weekly_windows"]],
            [1784781238, 1784792761],
        )
        self.assertEqual(
            [window["label"] for window in result["weekly_windows"]],
            ["常规周限额", "GPT-5.3-Codex-Spark"],
        )
        self.assertTrue(result["weekly_windows"][0]["resettable"])
        self.assertFalse(result["weekly_windows"][1]["resettable"])

    def test_exhausted_weekly_window_never_reports_phantom_remaining_quota(self):
        payload = {
            "plan_type": "pro",
            "rate_limit": {
                "allowed": False,
                "limit_reached": True,
                "primary_window": {
                    "limit_window_seconds": 604800,
                    "used_percent": 98,
                    "reset_at": 1784781238,
                    "reset_after_seconds": 300,
                },
            },
            "rate_limit_reached_type": {
                "type": "rate_limit_reached",
                "details": "default",
            },
        }

        result = self.app._normalize_usage_limit_payload("gamma", payload)

        self.assertTrue(result["weekly"]["limit_reached"])
        self.assertEqual(result["weekly"]["reported_used_percent"], 98.0)
        self.assertEqual(result["weekly"]["used_percent"], 100.0)
        self.assertEqual(result["weekly"]["remaining_percent"], 0.0)

    def test_reset_credit_details_filters_unusable_rows_and_reports_truncation(self):
        result = self.app._normalize_reset_credits(
            {"available_count": 5, "applicable_available_count": 2},
            {
                "available_count": 5,
                "total_earned_count": 8,
                "credits": [
                    {
                        "id": "later",
                        "status": "available",
                        "reset_type": "codex_rate_limits",
                        "granted_at": "2026-07-14T00:00:00Z",
                        "expires_at": "2026-08-14T00:00:00Z",
                        "title": "Full reset",
                    },
                    {
                        "id": "earlier",
                        "status": "available",
                        "reset_type": "codex_rate_limits",
                        "granted_at": "2026-07-13T00:00:00Z",
                        "expires_at": "2026-08-13T00:00:00Z",
                        "title": "Full reset",
                    },
                    {"id": "redeemed", "status": "redeemed"},
                    {
                        "id": "unsupported",
                        "status": "available",
                        "is_supported_by_plan": False,
                    },
                    {"id": "earlier", "status": "available"},
                ],
            },
        )

        self.assertEqual(result["available_count"], 5)
        self.assertEqual(result["applicable_available_count"], 2)
        self.assertEqual(result["total_earned_count"], 8)
        self.assertEqual(result["listed_count"], 2)
        self.assertTrue(result["details_truncated"])
        self.assertEqual([item["id"] for item in result["credits"]], ["earlier", "later"])

    def test_official_reset_credit_details_get_uses_codex_headers(self):
        response = mock.MagicMock()
        response.__enter__.return_value.read.return_value = b'{"available_count":2,"credits":[]}'
        opener = mock.Mock()
        opener.open.return_value = response
        self.app._official_opener = mock.Mock(return_value=opener)

        result = self.app._request_official_reset_credits(
            "secret-access-token",
            "account-123",
            "http://192.0.2.10:16169",
        )

        self.assertEqual(result["available_count"], 2)
        request = opener.open.call_args.args[0]
        self.assertEqual(
            request.full_url,
            self.server_module.USAGE_LIMIT_RESET_CREDITS_URL,
        )
        self.assertEqual(request.get_method(), "GET")
        headers = {key.lower(): value for key, value in request.header_items()}
        self.assertEqual(headers["authorization"], "Bearer secret-access-token")
        self.assertEqual(headers["chatgpt-account-id"], "account-123")
        self.assertEqual(headers["originator"], "Codex Desktop")
        self.assertEqual(opener.open.call_args.kwargs["timeout"], 20)

    def test_official_quota_reset_posts_selected_credit_and_uuid_with_codex_headers(self):
        response = mock.MagicMock()
        response.__enter__.return_value.read.return_value = b'{"windows_reset":1}'
        opener = mock.Mock()
        opener.open.return_value = response
        self.app._official_opener = mock.Mock(return_value=opener)

        result = self.app._request_official_quota_reset(
            "secret-access-token",
            "account-123",
            "http://192.0.2.10:16169",
            "credit-2",
        )

        self.assertEqual(result["windows_reset"], 1)
        request = opener.open.call_args.args[0]
        self.assertEqual(request.full_url, self.server_module.USAGE_LIMIT_RESET_URL)
        self.assertEqual(request.get_method(), "POST")
        payload = json.loads(request.data)
        self.assertEqual(uuid.UUID(payload["redeem_request_id"]).version, 4)
        self.assertEqual(payload["credit_id"], "credit-2")
        headers = {key.lower(): value for key, value in request.header_items()}
        self.assertEqual(headers["authorization"], "Bearer secret-access-token")
        self.assertEqual(headers["chatgpt-account-id"], "account-123")
        self.assertEqual(headers["originator"], "Codex Desktop")
        self.assertEqual(headers["content-type"], "application/json")
        self.assertEqual(opener.open.call_args.kwargs["timeout"], 20)

    def test_weekly_quota_reset_revalidates_target_consumes_credit_and_invalidates_cache(self):
        auth_dir = self.root / "auth" / "gamma"
        auth_dir.mkdir(parents=True, exist_ok=True)
        (auth_dir / "codex-test.json").write_text(
            json.dumps(
                {
                    "type": "codex",
                    "access_token": "secret-access-token",
                    "account_id": "account-123",
                }
            ),
            encoding="utf-8",
        )
        usage = {
            "rate_limit": {
                "allowed": False,
                "limit_reached": True,
                "primary_window": {
                    "limit_window_seconds": 604800,
                    "used_percent": 100,
                    "reset_at": 1784781238,
                },
            },
            "rate_limit_reached_type": {"details": "default"},
            "rate_limit_reset_credits": {
                "available_count": 2,
                "applicable_available_count": 1,
            },
        }
        reset_credit_details = {
            "available_count": 2,
            "credits": [
                {
                    "id": "credit-selected",
                    "reset_type": "codex_rate_limits",
                    "status": "available",
                    "granted_at": "2026-07-13T00:00:00Z",
                    "expires_at": "2026-08-13T00:00:00Z",
                    "title": "Full reset",
                },
                {
                    "id": "credit-later",
                    "reset_type": "codex_rate_limits",
                    "status": "available",
                    "granted_at": "2026-07-14T00:00:00Z",
                    "expires_at": "2026-08-14T00:00:00Z",
                    "title": "Full reset",
                },
            ],
        }
        self.app._account_proxy_url = mock.Mock(return_value="http://192.0.2.10:16169")
        self.app._request_official_usage = mock.Mock(return_value=usage)
        self.app._request_official_reset_credits = mock.Mock(
            return_value=reset_credit_details
        )
        self.app._request_official_quota_reset = mock.Mock(
            return_value={
                "code": "rate_limit_reset_credit_consumed",
                "windows_reset": 1,
                "credit": {
                    "id": "must-not-leak",
                    "reset_type": "default",
                    "status": "redeemed",
                    "redeemed_at": "2026-07-17T08:00:00Z",
                },
            }
        )
        self.app.usage_limit_cache = {"stale": True}
        self.app.usage_limit_cache_expires_at = 999999
        self.app.usage_limit_cache_fingerprint = (("stale", 1, 1),)

        result = self.app.reset_account_weekly_quota(
            {
                "account": "gamma",
                "credit_id": "credit-selected",
                "confirm": "gamma",
            }
        )

        self.assertEqual(result["windows_reset"], 1)
        self.assertEqual(result["windows"][0]["label"], "常规周限额")
        self.assertEqual(result["credit"]["title"], "Full reset")
        self.assertNotIn("must-not-leak", json.dumps(result))
        self.assertIsNone(self.app.usage_limit_cache)
        self.assertEqual(self.app.usage_limit_cache_expires_at, 0)
        self.assertIsNone(self.app.usage_limit_cache_fingerprint)
        self.app._request_official_usage.assert_called_once_with(
            "secret-access-token",
            "account-123",
            "http://192.0.2.10:16169",
        )
        self.app._request_official_reset_credits.assert_called_once_with(
            "secret-access-token",
            "account-123",
            "http://192.0.2.10:16169",
        )
        self.app._request_official_quota_reset.assert_called_once_with(
            "secret-access-token",
            "account-123",
            "http://192.0.2.10:16169",
            "credit-selected",
        )
        audit = (self.root / "logs" / "admin" / "audit.jsonl").read_text(encoding="utf-8")
        self.assertIn('"action":"account.quota.reset"', audit)

        self.app.usage_limit_cache = {"stale": True}
        self.app.usage_limit_cache_expires_at = 999999
        self.app.usage_limit_cache_fingerprint = (("stale", 1, 1),)
        self.app._request_official_quota_reset.side_effect = urllib.error.URLError("timeout")
        with self.assertRaises(self.server_module.APIError) as uncertain:
            self.app.reset_account_weekly_quota(
                {
                    "account": "gamma",
                    "credit_id": "credit-selected",
                    "confirm": "gamma",
                }
            )
        self.assertEqual(uncertain.exception.code, "quota_upstream_unavailable")
        self.assertIsNone(self.app.usage_limit_cache)

    def test_weekly_quota_reset_rejects_unavailable_window_and_stale_credit(self):
        auth_dir = self.root / "auth" / "gamma"
        auth_dir.mkdir(parents=True, exist_ok=True)
        (auth_dir / "codex-test.json").write_text(
            json.dumps({"type": "codex", "access_token": "secret-access-token"}),
            encoding="utf-8",
        )
        usage = {
            "rate_limit": {
                "limit_reached": True,
                "primary_window": {
                    "limit_window_seconds": 604800,
                    "used_percent": 100,
                    "reset_at": 1784781238,
                },
            },
            "rate_limit_reached_type": {"details": "default"},
            "rate_limit_reset_credits": {
                "available_count": 3,
                "applicable_available_count": 0,
            },
        }
        self.app._request_official_usage = mock.Mock(return_value=usage)
        reset_credit_details = {
            "available_count": 1,
            "credits": [
                {
                    "id": "credit-selected",
                    "status": "available",
                    "granted_at": "2026-07-13T00:00:00Z",
                    "expires_at": "2026-08-13T00:00:00Z",
                }
            ],
        }
        self.app._request_official_reset_credits = mock.Mock(
            return_value=reset_credit_details
        )
        self.app._request_official_quota_reset = mock.Mock()
        body = {
            "account": "gamma",
            "credit_id": "credit-selected",
            "confirm": "gamma",
        }

        with self.assertRaises(self.server_module.APIError) as unavailable:
            self.app.reset_account_weekly_quota(body)
        self.assertEqual(unavailable.exception.code, "quota_reset_unavailable")

        usage["rate_limit_reset_credits"]["applicable_available_count"] = 1
        reset_credit_details["credits"] = []
        with self.assertRaises(self.server_module.APIError) as stale:
            self.app.reset_account_weekly_quota(body)
        self.assertEqual(stale.exception.code, "quota_reset_credit_changed")
        self.app._request_official_quota_reset.assert_not_called()

    def test_weekly_quota_reset_endpoint_requires_admin_authentication(self):
        self.app.reset_account_weekly_quota = mock.Mock(
            return_value={"message": "周限额已重置", "windows_reset": 1}
        )
        body = {
            "account": "gamma",
            "credit_id": "credit-selected",
            "confirm": "gamma",
        }

        status, _, _ = self.request(
            "/admin/api/accounts/reset-quota",
            method="POST",
            body=body,
            authenticated=False,
        )
        self.assertEqual(status, 401)
        self.app.reset_account_weekly_quota.assert_not_called()

        status, _, raw = self.request(
            "/admin/api/accounts/reset-quota",
            method="POST",
            body=body,
        )
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(raw)["windows_reset"], 1)
        self.app.reset_account_weekly_quota.assert_called_once_with(body)

    def test_rotate_and_revoke_user_flow(self):
        self.request(
            "/admin/api/users",
            method="POST",
            body={"email": "alice@example.com"},
        )
        status, _, raw = self.request(
            "/admin/api/keys/rotate",
            method="POST",
            body={"label": "alice@example.com:alpha"},
        )
        self.assertEqual(status, 200)
        self.assertIn("key", json.loads(raw)["keys"][0])

        status, _, raw = self.request(
            "/admin/api/users/revoke",
            method="POST",
            body={"email": "alice@example.com"},
        )
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(raw)["revoked"], 1)
        self.assertEqual(self.control.active_records(), [])

    def test_create_user_cannot_be_narrowed_by_legacy_accounts_payload(self):
        status, _, raw = self.request(
            "/admin/api/users",
            method="POST",
            body={"email": "alice@example.com", "accounts": ["alpha"]},
        )
        self.assertEqual(status, 201)
        self.assertEqual(len(json.loads(raw)["keys"]), 1)
        self.assertEqual(len(self.control.active_records()), 4)

    def test_create_business_account_requires_admin_and_returns_assigned_port(self):
        record = {
            "id": "gamma-new2",
            "email": "gamma+new2@accounts.example.com",
            "port": 18323,
            "created_at": 123,
        }
        secret = "cpa_gamma_new2_" + "a" * 64
        self.control.add_account = mock.Mock(
            return_value={**record, "created_keys": 1, "keys": [{"key": secret}]}
        )

        status, _, _ = self.request(
            "/admin/api/accounts",
            method="POST",
            body={"id": record["id"], "email": record["email"]},
            authenticated=False,
        )
        self.assertEqual(status, 401)

        status, _, raw = self.request(
            "/admin/api/accounts",
            method="POST",
            body={
                "id": record["id"],
                "email": record["email"],
                "group_name": "ignored display name",
            },
        )
        self.assertEqual(status, 201)
        payload = json.loads(raw)
        self.assertEqual(payload["account"]["port"], 18323)
        self.assertNotIn("keys", payload)
        self.assertNotIn("keys", payload["account"])
        self.assertNotIn("created_keys", payload["account"])
        self.assertEqual(payload["created_keys"], 1)
        self.assertIn("后台关联 1 个已有用户的唯一 Key", payload["message"])
        self.assertNotIn(secret, raw.decode("utf-8"))
        self.control.add_account.assert_called_once_with(record["id"], record["email"])

    def test_account_lifecycle_endpoints_require_confirmation_and_forward_actions(self):
        self.control.update_account = mock.Mock(
            return_value={"id": "alpha", "email": "new@example.com", "port": 18319}
        )
        self.control.clear_account_auth = mock.Mock(
            return_value={"id": "alpha", "backup": "backups/accounts/clear"}
        )
        self.control.delete_account = mock.Mock(
            return_value={
                "id": "alpha",
                "removed_key_records": 2,
                "backup": "backups/accounts/deleted",
                "cleanup_warnings": [],
            }
        )

        status, _, raw = self.request(
            "/admin/api/accounts/update",
            method="POST",
            body={"id": "alpha", "new_id": "renamed-cpa", "email": "new@example.com"},
        )
        self.assertEqual(status, 400)
        self.assertIn("重命名确认", raw.decode("utf-8"))
        self.control.update_account.assert_not_called()

        status, _, _ = self.request(
            "/admin/api/accounts/update",
            method="POST",
            body={
                "id": "alpha",
                "new_id": "renamed-cpa",
                "email": "new@example.com",
                "confirm": "alpha",
            },
        )
        self.assertEqual(status, 200)
        self.control.update_account.assert_called_once_with(
            "alpha",
            "new@example.com",
            new_account_id="renamed-cpa",
        )

        status, _, raw = self.request(
            "/admin/api/accounts/clear-auth",
            method="POST",
            body={"id": "alpha", "confirm": "wrong"},
        )
        self.assertEqual(status, 400)
        self.assertIn("完全一致", raw.decode("utf-8"))

        status, _, _ = self.request(
            "/admin/api/accounts/clear-auth",
            method="POST",
            body={"id": "alpha", "confirm": "alpha"},
        )
        self.assertEqual(status, 200)
        self.control.clear_account_auth.assert_called_once_with("alpha")

        status, _, _ = self.request(
            "/admin/api/accounts/delete",
            method="POST",
            body={"id": "alpha", "confirm": "alpha", "revoke_keys": True},
        )
        self.assertEqual(status, 200)
        self.control.delete_account.assert_called_once_with(
            "alpha", revoke_keys=True, fallback_account=None
        )

    def test_account_management_returns_runtime_quota_and_usage_without_secrets(self):
        records = self.control.create_user("alice@example.com", apply=False)
        self.control.set_user_route("alice@example.com", "gamma", apply=False)
        now = int(time.time())
        self.app.usage_store.sync_identities(self.control._read_registry(), now=now)
        failed_key = "cpa_failed_activity_0123456789abcdef"
        historical_key = "cpa_historical_activity_0123456789abcdef"
        self.app.usage_store.sync_identities(
            [
                {
                    "key": failed_key,
                    "label": "bob@example.com:gamma",
                    "user": "bob@example.com",
                    "account": "gamma",
                },
                {
                    "key": historical_key,
                    "label": "carol@example.com:gamma",
                    "user": "carol@example.com",
                    "account": "gamma",
                },
            ],
            now=now,
        )
        self.app.usage_store.ingest_events(
            "gamma",
            [
                {
                    "timestamp": now - 10,
                    "api_key": records[0]["key"],
                    "request_id": "account-management-1",
                    "failed": False,
                    "tokens": {
                        "input_tokens": 80,
                        "output_tokens": 20,
                        "total_tokens": 100,
                    },
                },
                {
                    "timestamp": now - 20,
                    "api_key": failed_key,
                    "request_id": "account-management-failed",
                    "failed": True,
                },
                {
                    "timestamp": now - 7200,
                    "api_key": historical_key,
                    "request_id": "account-management-historical",
                    "failed": False,
                },
            ],
            now=now,
        )
        self.app._compose_ps = mock.Mock(
            return_value=[
                {
                    "service": "cliproxy-gamma",
                    "state": "running",
                    "status": "Up 1 hour",
                    "health": "",
                }
            ]
        )
        self.app.usage_limits = mock.Mock(
            return_value={
                "generated_at": now - 30,
                "cache_ttl_seconds": 60,
                "cached": True,
                "accounts": [
                    {
                        "account": "gamma",
                        "status": "ok",
                        "weekly": {
                            "used_percent": 25,
                            "remaining_percent": 75,
                        },
                        "reset_credits": {"available_count": 2},
                    }
                ]
            }
        )
        self.app._cached_cpa_management_snapshots = mock.Mock(
            return_value={
                "gamma": {
                    "query_status": "ok",
                    "credential_status": "active",
                    "credential_unavailable": False,
                    "error_log_files": 1,
                }
            }
        )
        self.app._gateway_error_activity = mock.Mock(
            return_value={
                "gamma": {
                    "window_seconds": 3600,
                    "requests": 10,
                    "error_count": 1,
                    "rate_429_count": 1,
                    "server_error_count": 0,
                    "affected_users": 1,
                    "last_error_at": now - 30,
                    "last_error_status": 429,
                    "error_rate_percent": 10.0,
                }
            }
        )
        self.control.auth_status = mock.Mock(
            return_value={"gamma": {"files": 1}}
        )

        status, _, raw = self.request("/admin/api/accounts?window=all")

        self.assertEqual(status, 200)
        payload = json.loads(raw)
        self.assertEqual(payload["quota_generated_at"], now - 30)
        self.assertTrue(payload["quota_cached"])
        self.assertEqual(payload["quota_cache_ttl_seconds"], 60)
        account = next(
            item for item in payload["accounts"] if item["id"] == "gamma"
        )
        self.assertEqual(account["group_name"], "gamma")
        self.assertEqual(account["container_state"], "running")
        self.assertEqual(account["routed_users"], 1)
        self.assertEqual(account["associated_users"], 1)
        self.assertEqual(account["active_users_1h"], 2)
        self.assertEqual(
            account["active_user_emails_1h"],
            ["alice@example.com", "bob@example.com"],
        )
        self.assertEqual(account["usage"]["active_users"], 3)
        self.assertEqual(account["usage"]["request_count"], 3)
        self.assertEqual(account["usage"]["failed_count"], 1)
        self.assertEqual(account["usage"]["total_tokens"], 100)
        self.assertEqual(account["quota"]["weekly"]["remaining_percent"], 75)
        self.assertEqual(account["quota"]["reset_credits"]["available_count"], 2)
        self.assertEqual(account["runtime"]["state"], "rate_limited")
        self.assertEqual(account["runtime"]["rate_429_count"], 1)
        self.assertEqual(account["runtime"]["error_log_files"], 1)
        self.assertEqual(account["operational_status"]["code"], "rate_limited")
        self.assertEqual(account["operational_status"]["tone"], "warning")
        self.assertTrue(account["operational_status"]["selectable"])
        self.assertNotIn(records[0]["key"], raw.decode("utf-8"))
        self.app.usage_limits.assert_called_once_with(force_refresh=False)

    def test_account_management_since_reset_uses_quota_period_start_per_account(self):
        records = self.control.create_user("alice@example.com", apply=False)
        now = 200_000
        period_start = now - 3_600
        self.app.usage_store.sync_identities(self.control._read_registry(), now=now)
        self.app.usage_store.ingest_events(
            records[0]["account"],
            [
                {
                    "timestamp": now - 7_200,
                    "api_key": records[0]["key"],
                    "request_id": "before-account-period",
                    "failed": False,
                    "tokens": {"total_tokens": 900},
                },
                {
                    "timestamp": now - 1_800,
                    "api_key": records[0]["key"],
                    "request_id": "inside-account-period",
                    "failed": False,
                    "tokens": {"total_tokens": 100},
                },
            ],
            now=now,
        )
        self.app._compose_ps = mock.Mock(return_value=[])
        self.app._cached_cpa_management_snapshots = mock.Mock(return_value={})
        self.app._gateway_error_activity = mock.Mock(return_value={})
        self.control.auth_status = mock.Mock(return_value={})
        self.app.usage_limits = mock.Mock(
            return_value={
                "generated_at": now,
                "accounts": [
                    {
                        "account": account,
                        "weekly": (
                            {
                                "window_seconds": self.server_module.WEEKLY_WINDOW_SECONDS,
                                "reset_at": period_start + self.server_module.WEEKLY_WINDOW_SECONDS,
                            }
                            if account == records[0]["account"]
                            else None
                        ),
                    }
                    for account in self.control.accounts()
                ],
            }
        )

        with mock.patch.object(self.server_module, "utc_timestamp", return_value=now):
            payload = self.app.account_management("since_reset")

        self.assertEqual(payload["window"], "since_reset")
        self.assertIsNone(payload["window_seconds"])
        self.assertEqual(
            payload["window_start_at_by_account"][records[0]["account"]],
            period_start,
        )
        account = next(
            item for item in payload["accounts"] if item["id"] == records[0]["account"]
        )
        self.assertTrue(account["usage_window_available"])
        self.assertEqual(account["usage_window_start_at"], period_start)
        self.assertEqual(account["usage"]["request_count"], 1)
        self.assertEqual(account["usage"]["total_tokens"], 100)
        unavailable = next(
            item for item in payload["accounts"] if item["id"] != records[0]["account"]
        )
        self.assertFalse(unavailable["usage_window_available"])
        self.assertEqual(unavailable["usage"]["request_count"], 0)

    def test_gateway_error_activity_tracks_429_users_and_latest_status(self):
        now = int(time.time())
        path = self.root / "logs/gateway/access.tsv"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(
            "\n".join(
                [
                    "{}\talice@example.com:alpha\talpha\t200\t1.0".format(now - 50),
                    "{}\talice@example.com:alpha\talpha\t429\t0.1".format(now - 40),
                    "{}\tbob@example.com:alpha\talpha\t500\t0.2".format(now - 30),
                    "{}\tbob@example.com:gamma\tgamma\t429\t0.1".format(now - 20),
                    "{}\talice@example.com:alpha\talpha\t429\t0.1".format(now - 4000),
                ]
            )
            + "\n",
            encoding="utf-8",
        )
        parser = self.control._parse_access_log_line
        self.control._parse_access_log_line = mock.Mock(wraps=parser)
        self.control.active_stats(300, now=now)
        parsed_lines = self.control._parse_access_log_line.call_count

        activity = self.app._gateway_error_activity(
            ["alpha", "gamma"],
            now=now,
        )

        self.assertEqual(activity["alpha"]["requests"], 3)
        self.assertEqual(activity["alpha"]["error_count"], 2)
        self.assertEqual(activity["alpha"]["rate_429_count"], 1)
        self.assertEqual(activity["alpha"]["server_error_count"], 1)
        self.assertEqual(activity["alpha"]["affected_users"], 2)
        self.assertEqual(activity["alpha"]["last_error_status"], 500)
        self.assertEqual(activity["gamma"]["rate_429_count"], 1)
        self.assertEqual(self.control._parse_access_log_line.call_count, parsed_lines)

    def test_cpa_management_snapshot_sanitizes_native_runtime_fields(self):
        self.app._request_cpa_management = mock.Mock(
            side_effect=lambda service, path: {
                "/auth-files": {
                    "files": [
                        {
                            "status": "active",
                            "status_message": "",
                            "unavailable": False,
                            "disabled": False,
                            "recent_requests": [
                                {"time": "10:00-10:10", "success": 8, "failed": 2}
                            ],
                            "id_token": "must-not-leak",
                        }
                    ]
                },
                "/request-error-logs": {"files": ["request-error-1.log"]},
            }[path]
        )

        snapshot = self.app._cpa_management_snapshot("cliproxy-alpha")

        self.assertEqual(snapshot["query_status"], "ok")
        self.assertEqual(snapshot["credential_status"], "active")
        self.assertFalse(snapshot["credential_disabled"])
        self.assertEqual(snapshot["native_success"], 8)
        self.assertEqual(snapshot["native_failed"], 2)
        self.assertEqual(snapshot["error_log_files"], 1)
        self.assertNotIn("id_token", snapshot)
        self.assertNotIn("must-not-leak", json.dumps(snapshot))

    def test_cpa_management_snapshot_refresh_returns_stale_without_waiting(self):
        account_services = {
            "alpha": "cliproxy-alpha",
            "beta": "cliproxy-beta-new",
        }
        self.app.cpa_snapshot_cache = {
            "alpha": {"query_status": "ok", "marker": "stale-alpha"},
            "beta": {"query_status": "ok", "marker": "stale-beta"},
        }
        self.app.cpa_snapshot_cache_fingerprint = (
            ("alpha", "cliproxy-alpha"),
            ("beta", "cliproxy-beta-old"),
        )
        self.app.cpa_snapshot_cache_expires_at = 0
        refresh_started = threading.Event()
        release_refresh = threading.Event()
        fresh = {
            account: {"query_status": "ok", "marker": "fresh-{}".format(account)}
            for account in account_services
        }

        def slow_load(requested_services):
            self.assertEqual(requested_services, account_services)
            refresh_started.set()
            self.assertTrue(release_refresh.wait(2))
            return fresh

        self.app._cpa_management_snapshots = mock.Mock(side_effect=slow_load)

        started_at = time.monotonic()
        first = self.app._cached_cpa_management_snapshots(account_services)
        elapsed = time.monotonic() - started_at
        self.assertLess(elapsed, 0.5)
        self.assertEqual(first, {"alpha": self.app.cpa_snapshot_cache["alpha"]})
        self.assertTrue(refresh_started.wait(1))

        second = self.app._cached_cpa_management_snapshots(account_services)
        self.assertEqual(second, first)
        self.assertEqual(self.app._cpa_management_snapshots.call_count, 1)
        release_refresh.set()
        deadline = time.time() + 2
        while self.app.cpa_snapshot_loading and time.time() < deadline:
            time.sleep(0.01)

        self.assertFalse(self.app.cpa_snapshot_loading)
        self.assertEqual(
            self.app._cached_cpa_management_snapshots(account_services),
            fresh,
        )
        self.assertEqual(self.app._cpa_management_snapshots.call_count, 1)

    def test_cliproxy_image_status_endpoint_returns_runtime_image_summary(self):
        self.control.cliproxy_image_status = mock.Mock(
            return_value={
                "target_image": "eceasy/cli-proxy-api:v7.2.93",
                "local_image": {
                    "available": True,
                    "id": "sha256:new",
                    "short_id": "new",
                    "created": "2026-07-21T05:18:17Z",
                    "repo_digests": [],
                },
                "accounts": [],
                "running_count": 4,
                "current_count": 3,
                "outdated_count": 1,
            }
        )

        status, _, raw = self.request("/admin/api/images/cliproxy")

        self.assertEqual(status, 200)
        payload = json.loads(raw)
        self.assertEqual(payload["target_image"], "eceasy/cli-proxy-api:v7.2.93")
        self.assertEqual(payload["outdated_count"], 1)
        self.assertFalse(payload["cached"])

        status, _, raw = self.request("/admin/api/images/cliproxy")
        self.assertEqual(status, 200)
        self.assertTrue(json.loads(raw)["cached"])
        self.control.cliproxy_image_status.assert_called_once_with()

    def test_public_usage_api_is_admin_owned_and_preserves_aggregate_contract(self):
        self.control.active_stats = mock.Mock(
            return_value={
                "alpha": {"count": 2, "requests": 5},
                "beta": {"count": 0, "requests": 0},
            }
        )
        self.control.inflight_stats = mock.Mock(
            return_value={
                "alpha": {"count": 1},
                "beta": {"count": 0},
            }
        )

        status, _, raw = self.request("/usage/api?window=300")

        self.assertEqual(status, 200)
        payload = json.loads(raw)
        self.assertEqual(payload["window_seconds"], 300)
        self.assertEqual(payload["totals"], {
            "inflight_keys": 1,
            "active_keys": 2,
            "requests": 5,
        })
        self.assertEqual(payload["accounts"][0]["account"], "alpha")
        self.assertNotIn("account_email", raw.decode("utf-8"))
        self.assertNotIn("users", raw.decode("utf-8"))

        status, _, _ = self.request("/usage/api?window=42")
        self.assertEqual(status, 400)

    def test_public_usage_cache_coalesces_cold_misses_and_blocks_stale_restore(self):
        load_started = threading.Event()
        release_load = threading.Event()
        stale = {"generated_at": 1, "window_seconds": 300}

        def slow_load(window):
            self.assertEqual(window, 300)
            load_started.set()
            self.assertTrue(release_load.wait(timeout=2))
            return stale

        self.app.public_gateway_usage = mock.Mock(side_effect=slow_load)
        with self.server_module.concurrent.futures.ThreadPoolExecutor(
            max_workers=8
        ) as executor:
            futures = [
                executor.submit(self.app.cached_public_gateway_usage, 300)
                for _ in range(8)
            ]
            self.assertTrue(load_started.wait(timeout=1))
            time.sleep(0.05)
            self.assertEqual(self.app.public_gateway_usage.call_count, 1)
            release_load.set()
            self.assertEqual([future.result() for future in futures], [stale] * 8)

        clock_first = {"generated_at": 10, "window_seconds": 3600}
        clock_second = {"generated_at": 11, "window_seconds": 3600}
        self.app.public_gateway_usage = mock.Mock(
            side_effect=[clock_first, clock_second]
        )
        self.assertEqual(
            self.app.cached_public_gateway_usage(3600, now=100),
            clock_first,
        )
        self.assertEqual(
            self.app.cached_public_gateway_usage(3600, now=109),
            clock_first,
        )
        self.assertEqual(self.app.public_gateway_usage.call_count, 1)
        self.assertEqual(
            self.app.cached_public_gateway_usage(3600, now=111),
            clock_first,
        )
        deadline = time.time() + 1
        while self.app.public_usage_cache.refreshing and time.time() < deadline:
            time.sleep(0.01)
        self.assertEqual(self.app.public_gateway_usage.call_count, 2)
        self.assertEqual(
            self.app.cached_public_gateway_usage(3600, now=111),
            clock_second,
        )

        refresh_started = threading.Event()
        release_refresh = threading.Event()
        refresh_finished = threading.Event()
        fresh = {"generated_at": 2, "window_seconds": 300}

        def slow_refresh(window):
            refresh_started.set()
            release_refresh.wait(timeout=2)
            refresh_finished.set()
            return fresh

        self.app.public_gateway_usage = mock.Mock(side_effect=slow_refresh)
        self.app.public_usage_cache.entries[300]["expires_at"] = 0
        started_at = time.monotonic()
        self.assertEqual(self.app.cached_public_gateway_usage(300), stale)
        self.assertLess(time.monotonic() - started_at, 0.5)
        self.assertTrue(refresh_started.wait(timeout=1))

        # Invalidation advances the generation. The old refresh may finish, but
        # it must not republish data calculated before the write.
        self.app.public_usage_cache.invalidate()
        release_refresh.set()
        self.assertTrue(refresh_finished.wait(timeout=1))
        self.assertEqual(self.app.public_usage_cache.entries, {})

    def test_bounded_swr_cache_evicts_lru_entries(self):
        cache = self.server_module.BoundedSWRCache(2)
        for key in ("a", "b", "c"):
            payload, state = cache.get(
                key,
                lambda key=key: {"key": key},
                60,
            )
            self.assertEqual(state, "miss")
            self.assertEqual(payload, {"key": key})
        self.assertEqual(list(cache.entries), ["b", "c"])

    def test_bounded_swr_cache_force_refresh_waits_for_one_fresh_value(self):
        cache = self.server_module.BoundedSWRCache(2)
        cached, state = cache.get("overview", lambda: {"generated_at": 1}, 60)
        self.assertEqual(state, "miss")
        self.assertEqual(cached["generated_at"], 1)

        refresh_started = threading.Event()
        release_refresh = threading.Event()

        def load_fresh():
            refresh_started.set()
            self.assertTrue(release_refresh.wait(timeout=2))
            return {"generated_at": 2}

        loader = mock.Mock(side_effect=load_fresh)
        with self.server_module.concurrent.futures.ThreadPoolExecutor(
            max_workers=2
        ) as executor:
            futures = [
                executor.submit(
                    cache.get,
                    "overview",
                    loader,
                    60,
                    force_refresh=True,
                    stale_while_revalidate=False,
                )
                for _ in range(2)
            ]
            self.assertTrue(refresh_started.wait(timeout=1))
            time.sleep(0.05)
            self.assertEqual(loader.call_count, 1)
            self.assertTrue(all(not future.done() for future in futures))
            release_refresh.set()
            results = [future.result(timeout=1) for future in futures]

        self.assertEqual(loader.call_count, 1)
        self.assertEqual(
            results,
            [({"generated_at": 2}, "refresh")] * 2,
        )

    def test_user_and_team_pages_share_one_cold_usage_aggregation(self):
        self.control.create_user("alice@example.com", apply=False)
        load_started = threading.Event()
        release_load = threading.Event()
        original = self.app.usage_store.usage_summaries_for_users

        def slow_load(**kwargs):
            load_started.set()
            self.assertTrue(release_load.wait(timeout=2))
            return original(**kwargs)

        self.app.usage_store.usage_summaries_for_users = mock.Mock(
            side_effect=slow_load
        )
        with self.server_module.concurrent.futures.ThreadPoolExecutor(
            max_workers=2
        ) as executor:
            users_future = executor.submit(self.app.user_management_page, None)
            self.assertTrue(load_started.wait(timeout=1))
            teams_future = executor.submit(self.app.team_usage_management, None)
            time.sleep(0.05)
            self.assertEqual(
                self.app.usage_store.usage_summaries_for_users.call_count,
                1,
            )
            release_load.set()
            self.assertEqual(users_future.result()["users"][0]["email"], "alice@example.com")
            self.assertIn("teams", teams_future.result())
        self.assertEqual(
            self.app.usage_store.usage_summaries_for_users.call_count,
            1,
        )

    def test_admin_read_cache_reports_server_timing_and_fresh_refresh(self):
        self.app._load_overview = mock.Mock(
            return_value={"generated_at": 1, "summary": {}}
        )
        status, headers, raw = self.request("/admin/api/overview")
        self.assertEqual(status, 200)
        self.assertIn('overview-miss', headers["Server-Timing"])
        self.assertEqual(json.loads(raw)["generated_at"], 1)

        status, headers, _ = self.request("/admin/api/overview")
        self.assertEqual(status, 200)
        self.assertIn('overview-hit', headers["Server-Timing"])
        self.app._load_overview.assert_called_once_with()

        refresh_started = threading.Event()
        release_refresh = threading.Event()

        def refresh():
            refresh_started.set()
            self.assertTrue(release_refresh.wait(timeout=2))
            return {"generated_at": 2, "summary": {}}

        self.app._load_overview = mock.Mock(side_effect=refresh)
        with self.server_module.concurrent.futures.ThreadPoolExecutor(
            max_workers=1
        ) as executor:
            future = executor.submit(
                self.request,
                "/admin/api/overview?fresh=1",
            )
            self.assertTrue(refresh_started.wait(timeout=1))
            self.assertFalse(future.done())
            release_refresh.set()
            status, headers, raw = future.result(timeout=1)
        self.assertEqual(status, 200)
        self.assertIn('overview-refresh', headers["Server-Timing"])
        self.assertEqual(json.loads(raw)["generated_at"], 2)
        self.assertEqual(self.app.overview()["generated_at"], 2)

    def test_admin_http_server_rejects_requests_beyond_worker_and_queue_limit(self):
        request_started = threading.Event()
        release_request = threading.Event()

        def slow_overview(force_refresh=False):
            request_started.set()
            self.assertTrue(release_request.wait(timeout=2))
            return {"generated_at": 1, "summary": {}}

        self.app.overview = slow_overview
        server = self.server_module.AdminHTTPServer(
            ("127.0.0.1", 0),
            self.app,
            max_workers=1,
            max_queue=1,
        )
        server_thread = threading.Thread(target=server.serve_forever, daemon=True)
        server_thread.start()
        base = "http://127.0.0.1:{}".format(server.server_port)

        def fetch():
            request = urllib.request.Request(
                base + "/admin/api/overview",
                headers={"X-Management-Key": "test-management-key"},
            )
            try:
                with urllib.request.urlopen(request, timeout=3) as response:
                    return response.status, dict(response.headers), response.read()
            except urllib.error.HTTPError as error:
                return error.code, dict(error.headers), error.read()

        first_result = []
        second_result = []
        first_thread = threading.Thread(target=lambda: first_result.append(fetch()))
        first_thread.start()
        second_thread = None
        close_thread = None
        close_finished = threading.Event()
        try:
            self.assertTrue(request_started.wait(timeout=1))
            second_thread = threading.Thread(
                target=lambda: second_result.append(fetch())
            )
            second_thread.start()
            deadline = time.time() + 1
            while server._request_slots._value and time.time() < deadline:
                time.sleep(0.01)
            self.assertEqual(server._request_slots._value, 0)
            status, headers, raw = fetch()
            self.assertEqual(status, 503)
            self.assertEqual(headers["Retry-After"], "1")
            self.assertEqual(json.loads(raw)["error"]["code"], "server_overloaded")
            server.shutdown()
            close_thread = threading.Thread(
                target=lambda: (
                    server.server_close(),
                    close_finished.set(),
                )
            )
            close_thread.start()
            time.sleep(0.05)
            self.assertFalse(close_finished.is_set())
        finally:
            release_request.set()
            first_thread.join(timeout=2)
            if second_thread is not None:
                second_thread.join(timeout=2)
            if close_thread is None:
                server.shutdown()
                server.server_close()
            else:
                close_thread.join(timeout=2)
                self.assertTrue(close_finished.is_set())
            server.server_close()
            server_thread.join(timeout=2)
        self.assertEqual(first_result[0][0], 200)
        self.assertEqual(second_result[0][0], 200)

    def test_admin_http_server_can_close_twice_without_serving_requests(self):
        server = self.server_module.AdminHTTPServer(
            ("127.0.0.1", 0),
            self.app,
            max_workers=1,
            max_queue=1,
        )
        server.server_close()
        server.server_close()
        self.assertTrue(server._close_complete.is_set())

    def test_compose_ps_cache_coalesces_queries_and_can_be_invalidated(self):
        load_started = threading.Event()
        release_load = threading.Event()
        rows = [{"service": "gateway", "state": "running"}]

        def load():
            load_started.set()
            self.assertTrue(release_load.wait(timeout=2))
            return rows

        self.app._load_compose_ps = mock.Mock(side_effect=load)
        results = []
        thread = threading.Thread(target=lambda: results.append(self.app._compose_ps()))
        thread.start()
        self.assertTrue(load_started.wait(timeout=2))
        fallback_started_at = time.monotonic()
        fallback = self.app._compose_ps()
        fallback_elapsed = time.monotonic() - fallback_started_at

        self.assertEqual(fallback, [])
        self.assertLess(fallback_elapsed, 0.5)
        release_load.set()
        thread.join(timeout=2)

        self.assertEqual(results, [rows])
        self.assertEqual(self.app._load_compose_ps.call_count, 1)

        self.app._invalidate_runtime_query_cache()
        self.app._compose_ps()
        self.assertEqual(self.app._load_compose_ps.call_count, 2)

    def test_image_operations_use_existing_job_manager_and_are_deduplicated(self):
        job = {
            "id": "image-job",
            "name": "拉取 CPA 镜像",
            "target": "all",
            "status": "queued",
            "created_at": int(time.time()),
            "started_at": None,
            "finished_at": None,
            "exit_code": None,
            "output": [],
        }
        self.app.jobs.start_or_reuse = mock.Mock(return_value=(job, False))

        payload = self.app.operation({"action": "image-pull", "target": "all"})

        self.assertEqual(payload["job"]["id"], "image-job")
        pull_call = self.app.jobs.start_or_reuse.call_args
        self.assertEqual(pull_call.args[0], "拉取 CPA 镜像")
        self.assertEqual(pull_call.args[1], "all")
        self.assertEqual(pull_call.args[2][0][-2:], ["image", "pull"])
        self.assertEqual(
            Path(pull_call.args[2][0][1]),
            ROOT / "scripts" / "cliproxy.py",
        )
        self.assertTrue(pull_call.kwargs["dedupe_key"].startswith("image-pull:all:"))

        self.app.jobs.start_or_reuse.reset_mock()
        update_job = {**job, "name": "更新 CPA 镜像", "target": "alpha"}
        self.app.jobs.start_or_reuse.return_value = (update_job, False)

        payload = self.app.operation(
            {"action": "image-update", "target": "alpha"}
        )

        self.assertEqual(payload["job"]["target"], "alpha")
        update_call = self.app.jobs.start_or_reuse.call_args
        self.assertEqual(update_call.args[2][0][-3:], ["image", "update", "alpha"])
        self.assertTrue(
            update_call.kwargs["dedupe_key"].startswith(
                "image-update:alpha:"
            )
        )

        records = self.control.store.read_accounts()
        for item in records:
            item["group_enabled"] = item["id"] != "alpha"
            item["default_group"] = item["id"] == "beta"
        self.control.store.write_accounts(records)
        self.app.jobs.start_or_reuse.reset_mock()

        with self.assertRaisesRegex(ValueError, "CPA 账号已停用"):
            self.app.operation({"action": "image-update", "target": "alpha"})

        self.app.jobs.start_or_reuse.assert_not_called()

    def test_account_policy_endpoint_requires_booleans_and_forwards_fallback(self):
        self.control.update_account_policy = mock.Mock(
            return_value={
                "id": "gamma",
                "group_name": "Plus Primary",
                "group_enabled": False,
                "default_group": False,
                "rerouted_users": 2,
            }
        )

        status, _, _ = self.request(
            "/admin/api/accounts/policy",
            method="POST",
            body={
                "id": "gamma",
                "group_name": "Plus Primary",
                "group_enabled": "no",
            },
        )
        self.assertEqual(status, 400)

        status, _, raw = self.request(
            "/admin/api/accounts/policy",
            method="POST",
            body={
                "id": "gamma",
                "group_name": "Plus Primary",
                "group_enabled": False,
                "default_group": False,
                "fallback_account": "beta",
            },
        )
        self.assertEqual(status, 200)
        self.assertIn("2 个用户", json.loads(raw)["message"])
        self.control.update_account_policy.assert_called_once_with(
            "gamma",
            "gamma",
            False,
            default_group=False,
            fallback_account="beta",
        )

    def test_manual_account_rebalance_requires_auth_confirmation_and_returns_distribution(self):
        self.app.account_failover.rebalance_account = mock.Mock(
            return_value={
                "account": "alpha",
                "moved_users": 3,
                "destinations": {"beta": 2, "gamma": 1},
                "quota_generated_at": 2_000_000_000,
                "snapshot_generation": "f" * 32,
            }
        )

        status, _, _ = self.request(
            "/admin/api/accounts/rebalance",
            method="POST",
            body={"id": "alpha", "confirm": "alpha"},
            authenticated=False,
        )
        self.assertEqual(status, 401)
        status, _, _ = self.request(
            "/admin/api/accounts/rebalance",
            method="POST",
            body={"id": "alpha", "confirm": "wrong"},
        )
        self.assertEqual(status, 400)
        self.app.account_failover.rebalance_account.assert_not_called()

        status, _, raw = self.request(
            "/admin/api/accounts/rebalance",
            method="POST",
            body={"id": "alpha", "confirm": "alpha"},
        )

        self.assertEqual(status, 200)
        payload = json.loads(raw)
        self.assertIn("已迁移 3 位用户", payload["message"])
        self.assertIn("beta 2 位", payload["message"])
        self.app.account_failover.rebalance_account.assert_called_once_with(
            self.app,
            "alpha",
        )

        self.app.account_failover.rebalance_account.reset_mock(
            side_effect=True,
        )
        self.app.account_failover.rebalance_account.side_effect = ValueError(
            "当前没有满足额度、OAuth 和运行状态条件的目标 CPA"
        )
        status, _, raw = self.request(
            "/admin/api/accounts/rebalance",
            method="POST",
            body={"id": "alpha", "confirm": "alpha"},
        )
        self.assertEqual(status, 409)
        self.assertEqual(
            json.loads(raw)["error"]["code"],
            "account_rebalance_unavailable",
        )

    def test_account_update_applies_metadata_and_policy_in_one_control_transaction(self):
        self.control.update_account = mock.Mock(
            return_value={
                "id": "gamma-renamed",
                "renamed_from": "gamma",
                "group_name": "Plus Primary",
                "group_enabled": False,
                "default_group": False,
                "rerouted_users": 3,
            }
        )

        status, _, raw = self.request(
            "/admin/api/accounts/update",
            method="POST",
            body={
                "id": "gamma",
                "new_id": "gamma-renamed",
                "confirm": "gamma",
                "email": "renamed@example.com",
                "group_name": "Plus Primary",
                "group_enabled": False,
                "default_group": False,
                "fallback_account": "beta",
            },
        )

        self.assertEqual(status, 200)
        self.assertIn("已重命名并更新", json.loads(raw)["message"])
        self.control.update_account.assert_called_once_with(
            "gamma",
            "renamed@example.com",
            new_account_id="gamma-renamed",
            group_enabled=False,
            default_group=False,
            fallback_account="beta",
        )

    def test_uuid_api_key_is_redacted_from_output_text(self):
        secret = "cpa_alice_12345678-1234-4abc-9def-1234567890ab"

        self.assertEqual(self.server_module.redact(secret), "key_[REDACTED]")

    def test_active_user_delete_requires_explicit_key_revoke(self):
        self.control.create_key("alice@example.com:alpha", apply=False)
        status, _, raw = self.request(
            "/admin/api/users/delete",
            method="POST",
            body={"email": "alice@example.com", "confirm": "alice@example.com"},
        )
        self.assertEqual(status, 400)
        self.assertIn("确认同时停用", raw.decode("utf-8"))

        status, _, raw = self.request(
            "/admin/api/users/delete",
            method="POST",
            body={
                "email": "alice@example.com",
                "confirm": "alice@example.com",
                "revoke_keys": True,
            },
        )
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(raw)["user"]["removed_records"], 4)
        self.assertEqual(json.loads(raw)["user"]["revoked_active_keys"], 1)
        self.assertEqual(self.control._read_registry(), [])

    def test_settings_reports_paths_without_secret_and_rotates_key_after_confirmation(self):
        legacy_secret = "cpa_" + "a" * 64
        namespaced_secret = "cpa_alpha_" + "b" * 64
        current_secret = "cpa_alpha_alice_" + "c" * 16
        self.app.audit("key.create", legacy_secret)
        self.app.audit("key.rotate", namespaced_secret)
        self.app.audit("key.rotate", current_secret)
        status, _, raw = self.request("/admin/api/settings")
        self.assertEqual(status, 200)
        text = raw.decode("utf-8")
        self.assertNotIn("test-management-key", text)
        self.assertNotIn(legacy_secret, text)
        self.assertNotIn(namespaced_secret, text)
        self.assertNotIn(current_secret, text)
        self.assertIn("key_[REDACTED]", text)
        payload = json.loads(text)
        self.assertTrue(payload["management_key_configured"])
        self.assertFalse(payload["notifications"]["webhook_configured"])
        self.assertEqual(payload["notifications"]["webhook_url"], "")
        self.assertEqual(payload["account_failover"]["mode"], "off")
        self.assertEqual(
            [item["path"] for item in payload["storage"]],
            [
                "state/control-plane.sqlite3",
                "state/usage.sqlite3",
                "secrets/control-plane.key",
                "logs/admin/audit.jsonl",
            ],
        )
        groups = payload["configuration"]["groups"]
        self.assertEqual(groups[0]["name"], "品牌与身份")
        self.assertEqual(groups[-1]["name"], "系统约束")
        fields = [field for group in groups for field in group["fields"]]
        strategy = next(group for group in groups if group["name"] == "推理强度策略")
        self.assertEqual(len(strategy["fields"]), 20)
        self.assertEqual(
            next(field for field in fields if field["key"] == "cpa.request_retry")["value"],
            2,
        )
        self.assertEqual(
            next(
                field
                for field in fields
                if field["key"] == "cpa.disable_image_generation"
            )["value"],
            "chat",
        )
        self.assertEqual(
            next(
                field
                for field in fields
                if field["key"] == "cpa.max_retry_interval"
            )["value"],
            12,
        )
        self.assertEqual(
            next(
                field
                for field in fields
                if field["key"] == "cpa.transient_error_cooldown_seconds"
            )["value"],
            10,
        )
        self.assertEqual(
            next(field for field in fields if field["key"] == "account_failover.mode")["value"],
            "active",
        )
        self.assertFalse(
            next(field for field in fields if field["key"] == "system.project_root")["editable"]
        )

        self.control.rotate_management_key = mock.Mock(return_value={"rotated": True, "services": 5})
        status, _, raw = self.request(
            "/admin/api/settings/management-key",
            method="POST",
            body={"new_key": "new-management-key!", "confirmation": "different-key!"},
        )
        self.assertEqual(status, 400)
        self.assertIn("不一致", raw.decode("utf-8"))

        status, _, raw = self.request(
            "/admin/api/settings/management-key",
            method="POST",
            body={"new_key": "new-management-key!", "confirmation": "new-management-key!"},
        )
        self.assertEqual(status, 200)
        self.assertNotIn("new-management-key!", raw.decode("utf-8"))
        self.control.rotate_management_key.assert_called_once_with("new-management-key!")

    def test_release_status_pulls_metadata_and_reports_only_newer_semver(self):
        labels = {
            "io.codex-cpa.component": "release",
            "org.opencontainers.image.version": "v1.2.0",
            "org.opencontainers.image.revision": "a" * 40,
        }
        self.control.store.write_runtime_state(
            "deployment",
            {
                "applied": {"version": "v1.1.0"},
                "pending": {"version": "v1.2.0"},
            },
        )
        self.control.update_configuration(
            {
                "delivery.release_metadata_image": (
                    "docker.io/example/codex-cpa-release:latest"
                )
            }
        )
        with mock.patch.object(
            self.server_module.subprocess,
            "run",
            side_effect=[
                mock.Mock(stdout=""),
                mock.Mock(stdout=json.dumps(labels)),
            ],
        ) as run:
            status, _, raw = self.request("/admin/api/release")

        self.assertEqual(status, 200)
        payload = json.loads(raw)
        self.assertTrue(payload["available"])
        self.assertEqual(payload["current_version"], "v1.1.0")
        self.assertEqual(payload["latest_version"], "v1.2.0")
        self.assertEqual(run.call_count, 2)
        self.assertEqual(
            run.call_args_list[0].args[0],
            ["docker", "pull", "docker.io/example/codex-cpa-release:latest"],
        )

    def test_release_status_disables_unconfigured_or_invalid_sources_without_docker(self):
        with mock.patch.object(self.server_module.subprocess, "run") as run:
            self.assertFalse(self.app.release_status()["configured"])
            run.assert_not_called()

        self.control.store.write_runtime_state(
            "deployment", {"version": "v1.0.0"}
        )
        settings = self.control.store.read_settings()
        settings["delivery.release_metadata_image"] = "invalid image; command"
        self.control.store.write_settings(settings)
        with mock.patch.object(self.server_module.subprocess, "run") as run:
            payload = self.app.release_status(force=True)
            self.assertEqual(payload["status"], "invalid_configuration")
            run.assert_not_called()

    def test_release_version_comparison_follows_numeric_prerelease_order(self):
        key = self.app._release_version_key
        self.assertLess(key("v1.2.0-rc.2"), key("v1.2.0-rc.10"))
        self.assertLess(key("v1.2.0-rc.10"), key("v1.2.0"))
        self.assertIsNone(key("v1.2.0-rc.01"))

    def test_notification_webhook_routes_show_configured_address_and_clear_disables_notifications(self):
        webhook = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test-placeholder"

        status, _, raw = self.request(
            "/admin/api/settings/configuration",
            method="POST",
            body={"values": {"notification.enabled": True}, "confirm": "save"},
        )
        self.assertEqual(status, 400)
        self.assertIn("必须先配置", raw.decode("utf-8"))

        status, _, raw = self.request(
            "/admin/api/settings/notification-webhook",
            method="POST",
            body={"webhook_url": webhook, "confirm": "save"},
        )
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(raw)["notifications"]["webhook_url"], webhook)
        self.assertFalse((self.root / "secrets" / "wecom-webhook.url").exists())
        self.assertEqual(self.control.store.read_secret("wecom_webhook"), webhook)

        status, _, raw = self.request(
            "/admin/api/settings/configuration",
            method="POST",
            body={"values": {"notification.enabled": True}, "confirm": "save"},
        )
        self.assertEqual(status, 200)
        self.assertTrue(self.control.configuration()["values"]["notification.enabled"])

        status, _, raw = self.request("/admin/api/settings")
        self.assertEqual(status, 200)
        notifications = json.loads(raw)["notifications"]
        self.assertTrue(notifications["webhook_configured"])
        self.assertEqual(notifications["webhook_url"], webhook)

        status, _, raw = self.request(
            "/admin/api/settings/notification-webhook/clear",
            method="POST",
            body={"confirm": "clear"},
        )
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(raw)["notifications"]["webhook_url"], "")
        self.assertFalse((self.root / "secrets" / "wecom-webhook.url").exists())
        self.assertFalse(self.control.configuration()["values"]["notification.enabled"])

    def test_notification_webhook_validation_and_manual_send_status(self):
        status, _, raw = self.request(
            "/admin/api/settings/notification-webhook",
            method="POST",
            body={
                "webhook_url": "https://example.com/send?key=should-not-leak",
                "confirm": "save",
            },
        )
        self.assertEqual(status, 400)
        self.assertNotIn("should-not-leak", raw.decode("utf-8"))

        status, _, raw = self.request(
            "/admin/api/notifications/send",
            method="POST",
            body={},
        )
        self.assertEqual(status, 400)
        self.assertIn("尚未配置企业微信 Webhook", raw.decode("utf-8"))

        webhook = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test-send-placeholder"
        self.app.notifications.set_webhook(webhook)
        self.app.account_management = mock.Mock(
            return_value={
                "accounts": [
                    {
                        "id": "alpha",
                        "usage": {"active_users": 2},
                        "quota": {
                            "status": "ok",
                            "reset_credits": {"available_count": 2},
                            "weekly_windows": [
                                {
                                    "key": "default:primary_window",
                                    "label": "常规周限额",
                                    "used_percent": 55,
                                    "reset_at": int(time.time()) + 3600,
                                }
                            ],
                        },
                    }
                ]
            }
        )
        self.app.notifications.send_content = mock.Mock(
            return_value={"errcode": 0, "errmsg": "ok"}
        )

        status, _, raw = self.request(
            "/admin/api/notifications/send",
            method="POST",
            body={},
        )
        self.assertEqual(status, 200)
        payload = json.loads(raw)
        self.assertEqual(payload["message"], "账号信息已发送到企业微信群")
        self.assertEqual(payload["format"], "markdown_v2")
        content = self.app.notifications.send_content.call_args.args[0]
        self.assertIn("# Codex CPA · 账号额度报告", content)
        self.assertIn(
            "| CPA账号 / 额度窗口 | 已用 | 1h用户 | 重置次数 | 下次刷新 |",
            content,
        )
        self.assertIn("55% | 2 | 2", content)
        self.assertIn(
            "应用地址：[http://cpa.example.com/usage/]"
            "(http://cpa.example.com/usage/)",
            content,
        )
        notification_state = self.app.notifications.read_state()
        self.assertIsNotNone(notification_state["last_success_at"])
        self.assertEqual(notification_state["last_error"], "")

        self.app.notifications.send_content.side_effect = RuntimeError(
            "企业微信消息发送失败：" + webhook
        )
        status, _, raw = self.request(
            "/admin/api/notifications/send",
            method="POST",
            body={},
        )
        self.assertEqual(status, 502)
        self.assertEqual(json.loads(raw)["error"]["code"], "notification_send_failed")
        self.assertNotIn("test-send-secret", raw.decode("utf-8"))
        self.assertIn("[REDACTED]", raw.decode("utf-8"))
        self.assertNotIn(
            "test-send-secret",
            self.app.notifications.read_state()["last_error"],
        )

        self.app.notifications.send_content.side_effect = None
        status, _, raw = self.request(
            "/admin/api/notifications/test",
            method="POST",
            body={},
        )
        self.assertEqual(status, 200)

    def test_webhook_url_is_redacted_from_generic_output(self):
        webhook = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test-redact"

        self.assertNotIn("redact-me", self.server_module.redact("failed " + webhook))
        self.assertIn("[REDACTED]", self.server_module.redact("failed " + webhook))

    def test_configuration_endpoint_applies_each_scope_and_persists_values(self):
        self.control.compose = mock.Mock()
        self.control.sync_environment_configuration = mock.Mock()
        self.control.apply_changes.reset_mock()

        status, _, raw = self.request(
            "/admin/api/settings/configuration",
            method="POST",
            body={
                "values": {
                    "cpa.debug": True,
                    "collector.batch_size": 50,
                    "usage.quota_cache_seconds": 120,
                    "gateway.port": 18315,
                },
                "confirm": "save",
            },
        )

        self.assertEqual(status, 200)
        payload = json.loads(raw)
        self.assertEqual(
            set(payload["changed"]),
            {
                "cpa.debug",
                "collector.batch_size",
                "usage.quota_cache_seconds",
                "gateway.port",
            },
        )
        self.assertTrue(payload["pending_deployment"])
        self.control.apply_changes.assert_called_once_with()
        self.control.compose.assert_called_once_with("restart", "usage-collector")
        self.control.sync_environment_configuration.assert_called_once()
        values = self.control.configuration()["values"]
        self.assertTrue(values["cpa.debug"])
        self.assertEqual(values["collector.batch_size"], 50)
        self.assertEqual(values["usage.quota_cache_seconds"], 120)
        self.assertEqual(values["gateway.port"], 18315)

    def test_configuration_endpoint_validates_confirmation_and_rolls_back_failed_apply(self):
        status, _, raw = self.request(
            "/admin/api/settings/configuration",
            method="POST",
            body={"values": {"cpa.debug": True}, "confirm": ""},
        )
        self.assertEqual(status, 400)
        self.assertIn("确认", raw.decode("utf-8"))

        self.control.apply_changes.side_effect = [RuntimeError("compose failed"), None]
        status, _, raw = self.request(
            "/admin/api/settings/configuration",
            method="POST",
            body={"values": {"cpa.debug": True}, "confirm": "save"},
        )

        self.assertEqual(status, 502)
        self.assertEqual(json.loads(raw)["error"]["code"], "configuration_apply_failed")
        self.assertFalse(self.control.configuration()["values"]["cpa.debug"])
        self.assertEqual(self.control.apply_changes.call_count, 2)

    def test_gateway_slots_are_not_mutable_from_admin_operations(self):
        status, _, raw = self.request(
            "/admin/api/operations",
            method="POST",
            body={"action": "stop", "target": "gateway-blue"},
        )

        self.assertEqual(status, 400)
        self.assertIn("未知操作目标", raw.decode("utf-8"))

    def test_operation_impact_reports_current_routed_users_for_stop_confirmation(self):
        self.control.create_user("alice@example.com", apply=False)
        self.control.set_user_route("alice@example.com", "gamma", apply=False)

        status, _, raw = self.request(
            "/admin/api/operations/impact?action=stop&target=gamma"
        )

        self.assertEqual(status, 200)
        self.assertEqual(
            json.loads(raw),
            {
                "action": "stop",
                "target": "gamma",
                "target_type": "account",
                "routed_users": 1,
            },
        )

        status, _, raw = self.request(
            "/admin/api/operations/impact?action=stop&target=usage-collector"
        )
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(raw)["target_type"], "service")
        self.assertIsNone(json.loads(raw)["routed_users"])

        status, _, raw = self.request(
            "/admin/api/operations/impact?action=restart&target=gamma"
        )
        self.assertEqual(status, 400)
        self.assertIn("只支持查询停止操作影响", raw.decode("utf-8"))

    def test_audit_write_does_not_wait_for_long_running_action_lock(self):
        completed = threading.Event()
        self.app.action_lock.acquire()
        try:
            thread = threading.Thread(
                target=lambda: (self.app.audit("operation.login", "alpha"), completed.set()),
                daemon=True,
            )
            thread.start()
            self.assertTrue(completed.wait(timeout=1))
            thread.join(timeout=1)
        finally:
            self.app.action_lock.release()

    def test_running_job_can_be_cancelled(self):
        job = self.app.jobs.start(
            "long task",
            "test",
            [[sys.executable, "-c", "import time; print('started', flush=True); time.sleep(10)"]],
        )
        deadline = time.time() + 2
        while time.time() < deadline:
            job = self.app.jobs.get(job["id"])
            if job["status"] == "running":
                break
            time.sleep(0.02)

        status, _, _ = self.request(
            "/admin/api/jobs/cancel",
            method="POST",
            body={"id": job["id"]},
        )
        self.assertEqual(status, 202)
        deadline = time.time() + 2
        while time.time() < deadline:
            job = self.app.jobs.get(job["id"])
            if job["status"] == "cancelled":
                break
            time.sleep(0.02)
        self.assertEqual(job["status"], "cancelled")

    def test_duplicate_oauth_requests_reuse_one_job_and_queued_cancel_is_immediate(self):
        self.app.action_lock.acquire()
        try:
            status, _, raw = self.request(
                "/admin/api/operations",
                method="POST",
                body={"action": "login", "target": "alpha"},
            )
            self.assertEqual(status, 202)
            first = json.loads(raw)
            self.assertFalse(first["reused"])

            status, _, raw = self.request(
                "/admin/api/operations",
                method="POST",
                body={"action": "login", "target": "alpha"},
            )
            self.assertEqual(status, 202)
            second = json.loads(raw)
            self.assertTrue(second["reused"])
            self.assertEqual(second["job"]["id"], first["job"]["id"])
            self.assertIn("直接打开", second["message"])
            self.assertEqual(len(self.app.jobs.recent(limit=10)), 1)

            cancelled = self.app.jobs.cancel(first["job"]["id"])
            self.assertEqual(cancelled["status"], "cancelled")
            self.assertEqual(cancelled["exit_code"], -15)
            self.assertIsNone(cancelled["started_at"])
            self.assertIsNotNone(cancelled["finished_at"])
        finally:
            self.app.action_lock.release()


if __name__ == "__main__":
    unittest.main()
