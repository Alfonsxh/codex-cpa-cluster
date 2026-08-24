import importlib.util
import unittest
from pathlib import Path


ROOT = Path(__file__).parents[1]


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


ADMIN_COMPARE = load(
    "migration_admin_read_compare", ROOT / "scripts" / "migration-admin-read-compare.py"
)
HTTP_CONTRACT = load(
    "migration_http_contract_for_admin", ROOT / "scripts" / "migration-http-contract.py"
)
ROUTE_MATRIX = load(
    "migration_route_matrix_for_admin", ROOT / "scripts" / "migration-route-matrix.py"
)


class MigrationAdminReadCompareTests(unittest.TestCase):
    def test_v2_csrf_manifest_matches_openapi(self):
        expected = {
            operation.key
            for operation in HTTP_CONTRACT.parse_openapi_operations()
            if operation.csrf
        }
        self.assertEqual(set(ADMIN_COMPARE.V2_CSRF_OPERATIONS), expected)

    def test_v1_csrf_manifest_matches_every_admin_mutation(self):
        expected = {
            route
            for route in ROUTE_MATRIX.extract_v1_routes()
            if route.split(" ", 1)[1].startswith("/admin/api/")
            and route.split(" ", 1)[0] != "GET"
            and route != "POST /admin/api/session"
        }
        self.assertEqual(set(ADMIN_COMPARE.V1_CSRF_OPERATIONS), expected)

    def test_schema_summary_never_walks_dynamic_object_keys(self):
        summary = ADMIN_COMPARE.schema_summary(
            {
                "accounts": {"private-account-name": {"email": "secret@example.com"}},
                "users": [{"email": "secret@example.com", "count": 1}],
            }
        )

        self.assertEqual(summary["top_keys"], ["accounts", "users"])
        self.assertEqual(summary["object_fields"], ["accounts"])
        self.assertEqual(summary["array_item_keys"], {"users": ["count", "email"]})
        self.assertNotIn("private-account-name", str(summary))
        self.assertNotIn("secret@example.com", str(summary))

    def test_schema_delta_accepts_only_the_exact_documented_log_extension(self):
        v1 = ADMIN_COMPARE.schema_summary(
            {"target": "all", "output": "redacted", "exit_code": 0}
        )
        v2 = ADMIN_COMPARE.schema_summary(
            {"target": "all", "output": "redacted", "exit_code": 0, "truncated": False}
        )
        delta = ADMIN_COMPARE.schema_delta(v1, v2)

        self.assertEqual(delta, ADMIN_COMPARE.SCHEMA_DECISIONS["logs"]["delta"])
        v2["top_keys"].append("unknown")
        self.assertNotEqual(
            ADMIN_COMPARE.schema_delta(v1, v2),
            ADMIN_COMPARE.SCHEMA_DECISIONS["logs"]["delta"],
        )

    def test_schema_decisions_cover_only_reviewed_endpoint_differences(self):
        self.assertEqual(
            set(ADMIN_COMPARE.SCHEMA_DECISIONS),
            {"session", "accounts", "logs", "overview_usage", "teams", "users"},
        )


if __name__ == "__main__":
    unittest.main()
