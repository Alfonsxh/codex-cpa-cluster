import importlib.util
import json
import unittest
from pathlib import Path


ROOT = Path(__file__).parents[1]
MODULE_PATH = ROOT / "scripts" / "migration-data-plane-route-compare.py"
SPEC = importlib.util.spec_from_file_location(
    "migration_data_plane_route_compare", MODULE_PATH
)
ROUTE_COMPARE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ROUTE_COMPARE)


class FakeTarget:
    def __init__(self, name, surface):
        self.name = name
        self.surface = surface
        self.base_url = "http://127.0.0.1:1"


class FakeRun:
    def __init__(self, name, surface, route="original-account"):
        self.target = FakeTarget(name, surface)
        self.portal_cookie = "cookie"
        self.original_route = route
        self.route_changed = False
        self.current_route = route
        self.steps = {}

    def portal_request(self, method, path, body=None):
        if method == "PUT":
            self.current_route = body["group_id"]
            return {"status": 200, "body": b"{}"}
        return {
            "status": 200,
            "body": json.dumps({"current_group": self.current_route}).encode(),
        }

    def require_status(self, _step, response, expected):
        self.asserted = response["status"] == expected
        return json.loads(response["body"])


class MigrationDataPlaneRouteCompareTests(unittest.TestCase):
    def test_set_route_uses_portal_api_and_tracks_restore(self):
        run = FakeRun("v1-main", "v1")
        ROUTE_COMPARE.set_route(run, "alternate-account", "switch")
        self.assertEqual(run.current_route, "alternate-account")
        self.assertTrue(run.route_changed)
        ROUTE_COMPARE.set_route(run, run.original_route, "restore")
        self.assertEqual(run.current_route, run.original_route)
        self.assertFalse(run.route_changed)

    def test_set_route_fails_closed_on_incorrect_readback(self):
        run = FakeRun("go-v2", "v2")

        def stale_request(method, _path, body=None):
            if method == "PUT":
                return {"status": 200, "body": b"{}"}
            return {
                "status": 200,
                "body": json.dumps({"current_group": "stale-account"}).encode(),
            }

        run.portal_request = stale_request
        with self.assertRaisesRegex(Exception, "route_not_changed"):
            ROUTE_COMPARE.set_route(run, "alternate-account", "switch")

    def test_source_has_no_direct_route_update_or_credential_output(self):
        source = MODULE_PATH.read_text(encoding="utf-8")
        self.assertIn('"PUT", "/usage/me/group"', source)
        self.assertIn("finally:", source)
        self.assertIn("route_restored", source)
        self.assertNotIn("UPDATE user_routes", source)
        self.assertNotIn("print(management_key", source)
        self.assertNotIn("print(test_key", source)

    def test_sanitized_report_exposes_only_aggregate_status_counts(self):
        run = FakeRun("go-v2", "v2")
        run.catalog = {"alpha", "beta", "gamma"}
        run.selectable = {"alpha", "beta"}
        run.operational = {
            "alpha": {"code": "unknown", "selectable": True},
            "beta": {"code": "unknown", "selectable": True},
            "gamma": {"code": "disabled", "selectable": False},
        }
        run.prepared = True
        run.cleaned = True
        run.failures = []
        run.user = "fixture@example.com"

        report = ROUTE_COMPARE.sanitized_target_run(run)

        self.assertEqual(
            report["account_status_counts"],
            {"unknown|true": 2, "disabled|false": 1},
        )
        self.assertNotIn("operational", report)


if __name__ == "__main__":
    unittest.main()
