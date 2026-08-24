import hashlib
import importlib.machinery
import json
import sqlite3
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MODULE = importlib.machinery.SourceFileLoader(
    "migration_portal_write_compare",
    str(ROOT / "scripts" / "migration-portal-write-compare.py"),
).load_module()


class MigrationPortalWriteCompareTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.management_key = "management-secret"
        self.test_key = "dedicated-test-secret"
        self.user = "private-user@example.com"
        self.targets = []
        self.servers = []
        for surface in ("v1", "v2"):
            target_root = self.root / surface
            (target_root / "state").mkdir(parents=True)
            (target_root / "secrets").mkdir()
            (target_root / ".v2-isolated-copy.json").write_text("{}", encoding="utf-8")
            (target_root / "state" / "usage.sqlite3").touch()
            (target_root / "secrets" / "control-plane.key").write_bytes(b"x" * 32)
            database = sqlite3.connect(target_root / "state" / "control-plane.sqlite3")
            database.execute(
                "CREATE TABLE key_records (sequence INTEGER, user_email TEXT, "
                "account_id TEXT, status TEXT, secret TEXT)"
            )
            database.execute(
                "CREATE TABLE user_routes (user_email TEXT, account_id TEXT)"
            )
            database.execute(
                "INSERT INTO user_routes VALUES (?, 'account-a')", (self.user,)
            )
            for sequence, account in enumerate(("account-a", "account-b"), 1):
                database.execute(
                    "INSERT INTO key_records VALUES (?, ?, ?, 'active', ?)",
                    (sequence, self.user, account, self.test_key),
                )
            database.commit()
            database.close()
            fixture = PortalFixture(surface, self.management_key, self.test_key, self.user)
            server = ThreadingHTTPServer(("127.0.0.1", 0), fixture.handler())
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            self.servers.append((server, fixture))
            self.targets.append(
                MODULE.Target(
                    surface,
                    surface,
                    "http://127.0.0.1:{}".format(server.server_port),
                    target_root / "state" / "control-plane.sqlite3",
                )
            )

    def tearDown(self):
        for server, _ in self.servers:
            server.shutdown()
            server.server_close()
        self.temporary.cleanup()

    def test_reversible_portal_workflow_is_compatible_and_sanitized(self):
        report = MODULE.run(self.targets, self.management_key, self.test_key, 2)
        self.assertTrue(report["compatible"], report)
        self.assertFalse(report["unexpected_differences"])
        for target in report["targets"].values():
            self.assertTrue(target["cleaned"])
            self.assertEqual(target["account_count"], 2)
            self.assertEqual(target["selectable_count"], 2)
            self.assertTrue(target["original_route_selectable"])
            self.assertEqual(target["account_status_counts"], {"unknown|true": 2})
            self.assertEqual(
                target["probe_user_sha256"],
                hashlib.sha256(self.user.encode()).hexdigest(),
            )
        for _, fixture in self.servers:
            self.assertEqual(fixture.route, "account-a")
            self.assertEqual(fixture.password, fixture.initial_password)
            self.assertFalse(fixture.sessions)
        self.assertEqual(
            report["account_operational_transitions"],
            {"unknown|true -> unknown|true": 2},
        )
        rendered = json.dumps(report)
        for secret in (
            self.management_key,
            self.test_key,
            self.user,
            "account-a",
            "account-b",
            "initial-password",
        ):
            self.assertNotIn(secret, rendered)

    def test_rejects_shared_database_inode(self):
        shared = self.targets[0].control_db
        second = MODULE.Target(
            "v2", "v2", self.targets[1].base_url, shared
        )
        with self.assertRaisesRegex(ValueError, "distinct isolated state copies"):
            MODULE.run([self.targets[0], second], self.management_key, self.test_key, 2)

    def test_failure_still_resets_password_and_logs_out(self):
        self.servers[1][1].fail_accounts = True
        report = MODULE.run(self.targets, self.management_key, self.test_key, 2)
        self.assertFalse(report["compatible"])
        self.assertTrue(report["unexpected_differences"])
        for _, fixture in self.servers:
            self.assertEqual(fixture.password, fixture.initial_password)
            self.assertEqual(fixture.route, "account-a")
            self.assertFalse(fixture.sessions)

    def test_route_is_not_changed_when_original_is_not_selectable(self):
        self.servers[1][1].original_selectable = False
        report = MODULE.run(self.targets, self.management_key, self.test_key, 2)
        self.assertFalse(report["compatible"])
        self.assertIsNotNone(report["dedicated_test_user_reads"])
        self.assertEqual(
            report["dedicated_test_user_reads"]["v1"]["failures"], []
        )
        self.assertEqual(
            report["dedicated_test_user_reads"]["v2"]["failures"], []
        )
        self.assertFalse(
            report["dedicated_test_user_reads"]["v2"][
                "original_route_selectable"
            ]
        )
        self.assertEqual(
            report["route_write_probe"]["v2"]["failures"],
            [{"step": "route_switch", "reason": "no_reversible_fallback_user"}],
        )
        for _, fixture in self.servers:
            self.assertEqual(fixture.route, "account-a")


class PortalFixture:
    def __init__(self, surface, management_key, test_key, user):
        self.surface = surface
        self.management_key = management_key
        self.test_key = test_key
        self.user = user
        self.initial_password = "initial-password"
        self.password = self.initial_password
        self.must_change = True
        self.route = "account-a"
        self.sessions = set()
        self.fail_accounts = False
        self.original_selectable = True

    def handler(self):
        fixture = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                return

            def body(self):
                length = int(self.headers.get("Content-Length", "0"))
                return json.loads(self.rfile.read(length) or b"{}")

            def reply(self, status, payload, cookie=""):
                raw = json.dumps(payload).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                if cookie:
                    self.send_header("Set-Cookie", cookie)
                self.end_headers()
                self.wfile.write(raw)

            def portal_authenticated(self):
                return "cpa_user_session=portal-session" in self.headers.get("Cookie", "") and "portal-session" in fixture.sessions

            def admin_authenticated(self):
                return (
                    self.headers.get("X-Management-Key") == fixture.management_key
                    or "cpa_admin_session=admin-session" in self.headers.get("Cookie", "")
                )

            def do_POST(self):
                if self.path == "/admin/api/session":
                    if self.headers.get("X-Management-Key") != fixture.management_key:
                        self.reply(401, {"error": {"code": "unauthorized"}})
                        return
                    payload = {"authenticated": True, "csrf_token": "csrf"}
                    if fixture.surface == "v1":
                        payload["accounts"] = []
                    self.reply(201, payload, "cpa_admin_session=admin-session; Path=/admin")
                    return
                if self.path == "/admin/api/users/reset-password":
                    if not self.admin_authenticated():
                        self.reply(401, {"error": {"code": "unauthorized"}})
                        return
                    body = self.body()
                    if body.get("email") != fixture.user or body.get("confirm") != "reset":
                        self.reply(400, {"error": {"code": "invalid_request"}})
                        return
                    fixture.password = fixture.initial_password
                    fixture.must_change = True
                    fixture.sessions.clear()
                    if fixture.surface == "v1":
                        payload = {
                            "user": fixture.user,
                            "initial_password": fixture.initial_password,
                            "password_change_required": True,
                        }
                    else:
                        payload = {
                            "password": {
                                "user": fixture.user,
                                "initial_password": fixture.initial_password,
                                "password_change_required": True,
                            }
                        }
                    self.reply(200, payload)
                    return
                if self.path == "/usage/session":
                    body = self.body()
                    if body.get("email") != fixture.user or body.get("password") != fixture.password:
                        self.reply(401, {"error": {"code": "invalid_credentials"}})
                        return
                    fixture.sessions.add("portal-session")
                    self.reply(
                        201,
                        {
                            "user": fixture.user,
                            "expires_at": 10000,
                            "password_change_required": fixture.must_change,
                        },
                        "cpa_user_session=portal-session; Path=/usage",
                    )
                    return
                self.reply(404, {"error": {"code": "not_found"}})

            def do_GET(self):
                if not self.portal_authenticated():
                    self.reply(401, {"error": {"code": "session_required"}})
                    return
                if fixture.must_change:
                    self.reply(403, {"error": {"code": "password_change_required"}})
                    return
                groups = [
                    {
                        "id": account,
                        "current": fixture.route == account,
                        "selectable": (
                            fixture.original_selectable
                            if account == "account-a"
                            else True
                        ),
                        "operational_status": {
                            "selectable": (
                                fixture.original_selectable
                                if account == "account-a"
                                else True
                            )
                        },
                    }
                    for account in ("account-a", "account-b")
                ]
                if self.path == "/usage/session" and fixture.surface == "v2":
                    self.reply(
                        200,
                        {
                            "authenticated": True,
                            "user": fixture.user,
                            "expires_at": 10000,
                            "password_change_required": False,
                        },
                    )
                elif self.path.startswith("/usage/me?") and fixture.surface == "v1":
                    self.reply(
                        200,
                        {
                            "user": fixture.user,
                            "api_key": fixture.test_key,
                            "current_group": fixture.route,
                            "groups": groups,
                        },
                    )
                elif self.path == "/usage/me/profile" and fixture.surface == "v2":
                    self.reply(
                        200,
                        {
                            "user": fixture.user,
                            "api_key": fixture.test_key,
                            "current_group": fixture.route,
                            "generated_at": 1,
                        },
                    )
                elif self.path.startswith("/usage/me/accounts?") and fixture.surface == "v2":
                    if fixture.fail_accounts:
                        self.reply(500, {"error": {"code": "internal_error"}})
                    else:
                        self.reply(
                            200,
                            {
                                "current_group": fixture.route,
                                "accounts": groups,
                                "generated_at": 1,
                            },
                        )
                elif self.path == "/usage/me/route":
                    self.reply(200, {"current_group": fixture.route, "generated_at": 1})
                elif self.path.startswith("/usage/me/usage-breakdown?"):
                    self.reply(200, {"account": fixture.route, "totals": {}, "models": []})
                else:
                    self.reply(404, {"error": {"code": "not_found"}})

            def do_PUT(self):
                if not self.portal_authenticated():
                    self.reply(401, {"error": {"code": "session_required"}})
                    return
                body = self.body()
                if self.path == "/usage/me/password":
                    if body.get("current_password") != fixture.password:
                        self.reply(401, {"error": {"code": "invalid_current_password"}})
                        return
                    fixture.password = body.get("new_password")
                    fixture.must_change = False
                    self.reply(200, {"message": "changed", "password_change_required": False})
                elif self.path == "/usage/me/group":
                    if body.get("group_id") not in {"account-a", "account-b"}:
                        self.reply(404, {"error": {"code": "account_not_found"}})
                        return
                    fixture.route = body["group_id"]
                    self.reply(200, {"current_group": fixture.route, "changed": True})
                else:
                    self.reply(404, {"error": {"code": "not_found"}})

            def do_DELETE(self):
                if self.path == "/usage/session":
                    fixture.sessions.clear()
                    self.reply(200, {"logged_out": True})
                elif self.path == "/admin/api/session":
                    self.reply(200, {"logged_out": True})
                else:
                    self.reply(404, {"error": {"code": "not_found"}})

        return Handler


if __name__ == "__main__":
    unittest.main()
