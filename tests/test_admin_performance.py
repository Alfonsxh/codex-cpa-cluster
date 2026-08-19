import importlib.util
import tempfile
import time
import unittest
from pathlib import Path


ROOT = Path(__file__).parents[1]
BENCHMARK_PATH = ROOT / "scripts" / "benchmark_admin_response.py"


def load_benchmark_module():
    spec = importlib.util.spec_from_file_location(
        "benchmark_admin_response_test",
        BENCHMARK_PATH,
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class AdminPerformanceTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.benchmark = load_benchmark_module()

    def test_disposable_summary_fixture_stays_paginated_and_bounded(self):
        now = int(time.time())
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.benchmark.seed_control_plane(root, 120, 20, now)
            self.benchmark.seed_usage(root, 10000, 120, 20, now)
            result = self.benchmark.run_benchmark(root, samples=2)

        self.assertEqual(result["total_users"], 120)
        self.assertEqual(result["returned_users"], 50)
        self.assertEqual(result["detail_accounts"], 20)
        self.assertLess(result["summary_payload_bytes"], 200000)


if __name__ == "__main__":
    unittest.main()
