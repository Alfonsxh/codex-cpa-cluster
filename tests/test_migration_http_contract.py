import importlib.util
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).parents[1]
MODULE_PATH = ROOT / "scripts" / "migration-http-contract.py"
SPEC = importlib.util.spec_from_file_location("migration_http_contract", MODULE_PATH)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class MigrationHTTPContractTests(unittest.TestCase):
    def test_openapi_operations_preserve_explicit_anonymous_logout(self):
        operations = {item.key: item for item in MODULE.parse_openapi_operations()}

        self.assertEqual(len(operations), 78)
        self.assertEqual(operations["DELETE /usage/session"].security, ())
        self.assertEqual(operations["GET /usage/session"].security, ("portalSession",))
        self.assertEqual(operations["GET /usage/me/key"].security, ("portalSession",))
        self.assertEqual(operations["GET /admin/api/accounts"].security, ("adminSession",))
        self.assertTrue(operations["POST /admin/api/accounts"].csrf)
        self.assertFalse(operations["POST /usage/me/key/rotate"].csrf)

    def test_v1_and_v2_every_operation_has_an_expectation(self):
        self.assertEqual(len(MODULE.v1_operations()), 72)
        for surface in ("v1", "v2"):
            for operation in MODULE.operations_for(surface):
                self.assertTrue(MODULE.expected_statuses(surface, operation))

    def test_parser_handles_security_blocks_and_path_parameters(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "openapi.yaml"
            path.write_text(
                """openapi: 3.0.3
paths:
  /public/{name}:
    delete:
      security: []
      responses: {}
  /private:
    get:
      security:
        - portalSession: []
      responses: {}
components: {}
""",
                encoding="utf-8",
            )

            operations = MODULE.parse_openapi_operations(path)

        self.assertEqual(
            operations,
            [
                MODULE.Operation("DELETE", "/public/probe", (), False),
                MODULE.Operation("GET", "/private", ("portalSession",), False),
            ],
        )

    def test_json_shape_never_retains_values(self):
        self.assertEqual(
            MODULE.json_shape({"secret": "cpa_live_value", "count": 3, "items": [{"ok": True}]}),
            {"count": "number", "items": [{"ok": "boolean"}], "secret": "string"},
        )


if __name__ == "__main__":
    unittest.main()
