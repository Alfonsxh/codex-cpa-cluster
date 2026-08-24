import hashlib
import json
import posixpath
import re
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
CONTRACT_PATH = ROOT / "testdata" / "gateway" / "contracts.json"


class GatewayContractFixtureTests(unittest.TestCase):
    def setUp(self):
        self.fixture = json.loads(CONTRACT_PATH.read_text(encoding="utf-8"))

    def test_fixture_paths_match_current_nginx_allowlist(self):
        nginx = (ROOT / "gateway" / "nginx.conf").read_text(encoding="utf-8")
        start = nginx.index("map $uri $public_api_route_allowed")
        end = nginx.index("\n    }", start)
        allowlist = nginx[start:end]
        patterns = (
            ("~^/v1(?:/|$) 1;", re.compile(r"^/v1(?:/|$)")),
            ("~^/v1beta(?:/|$) 1;", re.compile(r"^/v1beta(?:/|$)")),
            (
                "~^/backend-api/codex(?:/|$) 1;",
                re.compile(r"^/backend-api/codex(?:/|$)"),
            ),
            ("~^/api/provider(?:/|$) 1;", re.compile(r"^/api/provider(?:/|$)")),
            ("~^/v1internal:method$ 1;", re.compile(r"^/v1internal:method$")),
        )
        self.assertIn("default 0;", allowlist)
        for source, _ in patterns:
            self.assertIn(source, allowlist)
        for case in self.fixture["paths"]:
            with self.subTest(path=case["path"]):
                normalized = posixpath.normpath(case["path"])
                allowed = any(pattern.search(normalized) for _, pattern in patterns)
                self.assertEqual(allowed, case["allowed"])

    def test_fixture_external_key_digest_matches_auth_snapshot(self):
        records = self.fixture["auth_snapshot"]["records"]
        self.assertEqual(len(records), 1)
        digest = hashlib.sha256(self.fixture["external_key"].encode("utf-8")).hexdigest()
        self.assertEqual(records[0]["external_key_sha256"], digest)


if __name__ == "__main__":
    unittest.main()
