import importlib.util
import json
import sqlite3
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


ROOT = Path(__file__).parents[1]
MODULE_PATH = ROOT / "scripts" / "migration-data-plane-compare.py"
SPEC = importlib.util.spec_from_file_location(
    "migration_data_plane_compare", MODULE_PATH
)
DATA_COMPARE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(DATA_COMPARE)


class DataPlaneHandler(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        return

    def authorized(self):
        if self.headers.get("Authorization") == "Bearer dedicated-test-key":
            return True
        self.send_response(401)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"error":{"type":"invalid_request_error"}}')
        return False

    def send_json(self, payload):
        body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if not self.authorized():
            return
        if self.path != "/v1/models":
            self.send_error(404)
            return
        self.send_json(
            {
                "object": "list",
                "data": [
                    {
                        "id": self.server.model_id,
                        "object": "model",
                        "owned_by": "fixture",
                    }
                ],
            }
        )

    def do_POST(self):
        if not self.authorized():
            return
        if self.path != "/v1/responses":
            self.send_error(404)
            return
        length = int(self.headers.get("Content-Length") or 0)
        request = json.loads(self.rfile.read(length))
        if not request.get("stream"):
            self.send_json(
                {
                    "id": "response-fixture",
                    "object": "response",
                    "status": "completed",
                    "output": [
                        {
                            "type": "message",
                            "content": [{"type": "output_text", "text": "OK"}],
                        }
                    ],
                }
            )
            return
        events = [
            {"type": "response.created"},
            {"type": "response.output_text.delta", "delta": "OK"},
        ]
        if self.server.complete_stream:
            events.append(
                {"type": "response.completed", "response": {"status": "completed"}}
            )
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        for event in events:
            self.wfile.write(
                b"data: "
                + json.dumps(event, separators=(",", ":")).encode("utf-8")
                + b"\n\n"
            )
            self.wfile.flush()


class MigrationDataPlaneCompareTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.servers = []

    def tearDown(self):
        for server, thread in self.servers:
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)
        self.temporary.cleanup()

    def start_server(self, model_id="gpt-fixture", complete_stream=True):
        server = ThreadingHTTPServer(("127.0.0.1", 0), DataPlaneHandler)
        server.model_id = model_id
        server.complete_stream = complete_stream
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.servers.append((server, thread))
        return server.server_address[1]

    def make_target(self, name, surface, port, user="test-user@example.com"):
        root = Path(self.temporary.name) / name
        (root / "state").mkdir(parents=True)
        (root / ".v2-isolated-copy.json").write_text("{}\n", encoding="utf-8")
        database_path = root / "state" / "control-plane.sqlite3"
        database = sqlite3.connect(database_path)
        try:
            database.execute(
                "CREATE TABLE key_records ("
                "sequence INTEGER PRIMARY KEY, user_email TEXT, account_id TEXT, "
                "status TEXT, secret TEXT)"
            )
            database.execute(
                "INSERT INTO key_records VALUES (1, ?, 'account-fixture', "
                "'active', 'dedicated-test-key')",
                (user,),
            )
            database.commit()
        finally:
            database.close()
        return DATA_COMPARE.Target(
            name=name,
            surface=surface,
            base_url="http://127.0.0.1:{}".format(port),
            control_db=database_path,
        )

    def test_authenticated_models_responses_and_sse_are_compatible(self):
        v1 = self.make_target("v1", "v1", self.start_server())
        v2 = self.make_target("v2", "v2", self.start_server())
        report = DATA_COMPARE.run([v1, v2], "dedicated-test-key", 3)
        self.assertTrue(report["compatible"])
        self.assertEqual(report["unexpected_differences"], [])
        for target in report["targets"].values():
            self.assertTrue(target["probes"]["models"]["passed"])
            self.assertTrue(target["probes"]["responses"]["passed"])
            self.assertTrue(target["probes"]["responses_sse"]["passed"])
            self.assertTrue(target["probes"]["responses_sse"]["completed"])
            self.assertTrue(target["probes"]["responses_sse"]["text_exact"])

    def test_model_catalog_digest_mismatch_fails_closed_without_model_values(self):
        v1 = self.make_target("v1", "v1", self.start_server("model-left"))
        v2 = self.make_target("v2", "v2", self.start_server("model-right"))
        report = DATA_COMPARE.run([v1, v2], "dedicated-test-key", 3)
        self.assertFalse(report["compatible"])
        self.assertEqual(report["unexpected_differences"][0]["probe"], "models")
        rendered = json.dumps(report, sort_keys=True)
        self.assertNotIn("model-left", rendered)
        self.assertNotIn("model-right", rendered)
        self.assertNotIn("dedicated-test-key", rendered)

    def test_incomplete_sse_fails_target_probe(self):
        v1 = self.make_target("v1", "v1", self.start_server())
        v2 = self.make_target(
            "v2", "v2", self.start_server(complete_stream=False)
        )
        report = DATA_COMPARE.run([v1, v2], "dedicated-test-key", 3)
        self.assertFalse(report["compatible"])
        self.assertFalse(
            report["targets"]["v2"]["probes"]["responses_sse"]["passed"]
        )

    def test_public_target_and_mismatched_test_identity_are_rejected(self):
        with self.assertRaises(Exception):
            DATA_COMPARE.parse_target(
                "public,v1,http://example.com,/tmp/state/control-plane.sqlite3"
            )
        v1 = self.make_target("v1", "v1", self.start_server())
        v2 = self.make_target(
            "v2", "v2", self.start_server(), user="different@example.com"
        )
        with self.assertRaisesRegex(ValueError, "identity differs"):
            DATA_COMPARE.run([v1, v2], "dedicated-test-key", 3)

    def test_shared_control_database_is_rejected_before_request(self):
        port = self.start_server()
        v1 = self.make_target("v1", "v1", port)
        v2 = DATA_COMPARE.Target(
            name="v2",
            surface="v2",
            base_url="http://127.0.0.1:{}".format(port),
            control_db=v1.control_db,
        )
        with self.assertRaisesRegex(ValueError, "distinct"):
            DATA_COMPARE.run([v1, v2], "dedicated-test-key", 3)


if __name__ == "__main__":
    unittest.main()
