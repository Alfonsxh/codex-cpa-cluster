import importlib.util
import json
import unittest
from pathlib import Path
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "migration-data-plane-operational-compare.py"
SPEC = importlib.util.spec_from_file_location(
    "migration_data_plane_operational_compare", SCRIPT
)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class FakeClient:
    p95_by_surface = {"v1": 100, "v2": 200}

    def __init__(self, target, api_key, timeout):
        self.target = target
        self.api_key = api_key
        self.timeout = timeout

    def cancel_stream(self):
        return {
            "status": 200,
            "first_event_ms": 1,
            "inflight_before": 0,
            "inflight_during": 1,
            "inflight_after": 0,
            "fixture": {
                "active": 0,
                "started": 1,
                "completed": 0,
                "canceled": 1,
                "max_active": 1,
            },
            "passed": True,
        }

    def concurrent_probe(self, requests, workers, delay_ms):
        p95 = self.p95_by_surface[self.target.surface]
        return {
            "requests": requests,
            "workers": workers,
            "delay_ms": delay_ms,
            "status_counts": {"200": requests},
            "wall_ms": p95,
            "latency_ms": {"min": p95, "median": p95, "p95": p95, "max": p95},
            "fixture": {
                "active": 0,
                "started": requests,
                "completed": requests,
                "canceled": 0,
                "max_active": workers,
            },
            "inflight_after": 0,
            "passed": True,
        }

    def chunked_limit_probe(self, max_body_bytes):
        return {
            "status": 413,
            "bytes_sent": max_body_bytes + 1,
            "elapsed_ms": 1,
            "transport_error": "",
            "fixture_active_after": 0,
            "passed": True,
        }


class MigrationDataPlaneOperationalCompareTests(unittest.TestCase):
    def targets(self):
        return [
            MODULE.Target("v1-main", "v1", "http://127.0.0.1:19317", "http://127.0.0.1:19316"),
            MODULE.Target("go-v2", "v2", "http://127.0.0.1:18317", "http://127.0.0.1:18316"),
        ]

    def test_compatible_report_is_secret_free(self):
        FakeClient.p95_by_surface = {"v1": 100, "v2": 200}
        with mock.patch.object(MODULE, "Client", FakeClient):
            report = MODULE.run(
                self.targets(),
                "dedicated-secret-key-must-not-leak",
                10,
                16,
                8,
                100,
                1024,
            )
        self.assertTrue(report["compatible"])
        self.assertEqual(report["failures"], [])
        self.assertNotIn("dedicated-secret-key-must-not-leak", json.dumps(report))

    def test_detects_go_latency_regression(self):
        FakeClient.p95_by_surface = {"v1": 100, "v2": 400}
        with mock.patch.object(MODULE, "Client", FakeClient):
            report = MODULE.run(self.targets(), "dedicated-test-key", 10, 4, 2, 10, 1024)
        self.assertFalse(report["compatible"])
        self.assertEqual(report["failures"][0]["reason"], "go_v2_p95_regression")

    def test_target_rejects_public_or_duplicate_surfaces(self):
        with self.assertRaises(Exception):
            MODULE.parse_target("bad,v1,http://8.8.8.8,http://127.0.0.1:1")
        with self.assertRaises(ValueError):
            MODULE.run(
                [self.targets()[0], self.targets()[0]],
                "dedicated-test-key",
                10,
                4,
                2,
                10,
                1024,
            )


if __name__ == "__main__":
    unittest.main()
