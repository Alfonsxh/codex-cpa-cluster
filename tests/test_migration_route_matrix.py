import importlib.util
import unittest
from pathlib import Path


ROOT = Path(__file__).parents[1]
MODULE_PATH = ROOT / "scripts" / "migration-route-matrix.py"
SPEC = importlib.util.spec_from_file_location("migration_route_matrix", MODULE_PATH)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class MigrationRouteMatrixTests(unittest.TestCase):
    def test_every_v1_route_is_classified_and_alias_targets_exist(self):
        v1 = MODULE.extract_v1_routes()
        go = MODULE.extract_go_routes()
        rows = MODULE.build_rows(v1, go, MODULE.load_mapping())

        self.assertEqual({row["source"] for row in rows}, v1)
        self.assertTrue(all(row["status"] in {"exact", "mapped", "removed", "missing"} for row in rows))
        for row in rows:
            if row["status"] == "mapped":
                self.assertTrue(row["targets"])
                self.assertTrue(set(row["targets"]).issubset(go))

    def test_generated_document_is_current(self):
        rows = MODULE.build_rows(
            MODULE.extract_v1_routes(),
            MODULE.extract_go_routes(),
            MODULE.load_mapping(),
        )
        rendered, _undocumented = MODULE.render_markdown(
            rows, MODULE.extract_go_routes(), MODULE.extract_openapi_routes()
        )
        self.assertEqual(MODULE.DOC_PATH.read_text(encoding="utf-8"), rendered)


if __name__ == "__main__":
    unittest.main()
