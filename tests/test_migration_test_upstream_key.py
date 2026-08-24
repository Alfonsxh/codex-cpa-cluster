import importlib.util
import json
import os
import sqlite3
import stat
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "migration_test_upstream_key",
    ROOT / "scripts" / "migration-test-upstream-key.py",
)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class MigrationTestUpstreamKeyTests(unittest.TestCase):
    def create_root(self, parent, name, internal_key="fixture-internal-key"):
        root = parent / name
        (root / "state").mkdir(parents=True)
        (root / ".v2-isolated-copy.json").write_text(
            json.dumps({"version": 1}), encoding="utf-8"
        )
        database_path = root / "state" / "control-plane.sqlite3"
        database = sqlite3.connect(database_path)
        database.executescript(
            """
            CREATE TABLE key_records(
                sequence INTEGER PRIMARY KEY,
                account_id TEXT NOT NULL,
                user_email TEXT NOT NULL,
                status TEXT NOT NULL,
                secret TEXT NOT NULL
            );
            CREATE TABLE internal_keys(
                user_email TEXT PRIMARY KEY,
                secret TEXT NOT NULL,
                status TEXT NOT NULL
            );
            """
        )
        database.execute(
            "INSERT INTO key_records VALUES(1, 'alpha', 'fixture@example.com', 'active', 'external-test-key')"
        )
        database.execute(
            "INSERT INTO internal_keys VALUES('fixture@example.com', ?, 'active')",
            (internal_key,),
        )
        database.commit()
        database.close()
        return database_path

    def test_prepare_writes_only_restricted_matching_internal_key(self):
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory)
            v1 = self.create_root(parent, "v1")
            v2 = self.create_root(parent, "v2")
            external = parent / "external.key"
            external.write_text("external-test-key\n", encoding="utf-8")
            os.chmod(external, 0o600)
            output = parent / "fixture" / "internal.key"

            report = MODULE.prepare(v1, v2, external, output)

            self.assertTrue(report["compatible"])
            self.assertEqual(report["account_count"], 1)
            self.assertEqual(output.read_text(encoding="utf-8"), "fixture-internal-key\n")
            self.assertEqual(stat.S_IMODE(output.stat().st_mode), 0o600)
            self.assertNotIn("internal_key", report)

    def test_prepare_rejects_different_internal_keys(self):
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory)
            v1 = self.create_root(parent, "v1", "v1-internal")
            v2 = self.create_root(parent, "v2", "v2-internal")
            external = parent / "external.key"
            external.write_text("external-test-key\n", encoding="utf-8")
            os.chmod(external, 0o600)

            with self.assertRaisesRegex(ValueError, "internal Keys differ"):
                MODULE.prepare(v1, v2, external, parent / "internal.key")


if __name__ == "__main__":
    unittest.main()
