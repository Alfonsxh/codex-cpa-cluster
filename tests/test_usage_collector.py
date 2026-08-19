import importlib.util
import io
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

try:
    from fixtures import seed_control_plane
except ImportError:
    from tests.fixtures import seed_control_plane


ROOT = Path(__file__).parents[1]


def load_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class UsageCollectorTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.module = load_module(
            "cliproxy_usage_collector_test",
            ROOT / "admin" / "usage_collector.py",
        )

    def test_resp_parser_handles_arrays_bulk_strings_and_nil(self):
        connection = self.module.RESPConnection.__new__(self.module.RESPConnection)
        connection.stream = io.BytesIO(b"*3\r\n$3\r\none\r\n$3\r\ntwo\r\n$-1\r\n")

        self.assertEqual(connection._read_reply(), [b"one", b"two", None])

    def test_cli_defers_interval_and_batch_defaults_to_configuration_center(self):
        args = self.module.build_parser().parse_args([])

        self.assertIsNone(args.interval)
        self.assertIsNone(args.batch_size)

    def test_run_once_maps_all_account_keys_without_persisting_secrets(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            seed_control_plane(root)
            (root / "secrets").mkdir(parents=True, exist_ok=True)
            (root / "secrets" / "cpa-management.key").write_text(
                "test-management-key\n", encoding="utf-8"
            )
            control = self.module.cliproxy.ControlPlane(root)
            control.ensure_layout()
            records = control.create_user("alice@example.com", apply=False)
            team = control.store.create_team("Platform")
            control.store.set_user_teams(["alice@example.com"], team["id"])
            by_service = {
                "cliproxy-{}".format(record["account"]): record for record in records
            }
            collector = self.module.UsageCollector(root)
            event_timestamp = int(self.module.time.time())

            def drain(service, management_key):
                self.assertEqual(management_key, "test-management-key")
                record = by_service[service]
                yield [
                    {
                        "timestamp": event_timestamp,
                        "latency_ms": 200,
                        "provider": "openai",
                        "model": "gpt-5.6-sol",
                        "alias": "gpt-5.6-sol",
                        "reasoning_effort": "max",
                        "endpoint": "POST /v1/responses",
                        "auth_type": "apikey",
                        "api_key": record["key"],
                        "request_id": "request-{}".format(record["account"]),
                        "failed": False,
                        "tokens": {
                            "input_tokens": 10,
                            "output_tokens": 5,
                            "reasoning_tokens": 2,
                            "cached_tokens": 3,
                            "total_tokens": 15,
                        },
                    }
                ]

            with mock.patch.object(collector, "_drain", side_effect=drain):
                result = collector.run_once()

            self.assertEqual(result["inserted"], 4)
            self.assertEqual(result["errors"], [])
            usage = collector.store.usage_for_users(
                ["alice@example.com"],
                control.accounts().keys(),
                window_seconds=None,
            )["alice@example.com"]
            self.assertEqual(usage["request_count"], 4)
            self.assertEqual(usage["total_tokens"], 60)
            self.assertEqual(usage["weighted_tokens"], 120)
            team_usage = collector.store.usage_for_teams(
                [team["id"]],
                {"alice@example.com": team["id"]},
                window_seconds=None,
            )
            self.assertEqual(team_usage[team["id"]]["request_count"], 4)
            self.assertEqual(team_usage[team["id"]]["total_tokens"], 60)
            self.assertEqual(collector.store.status()["status"], "healthy")
            self.assertGreater(
                collector.store.status()["usage_breakdown_started_at"],
                0,
            )
            with collector.store._connection() as connection:
                efforts = {
                    row["reasoning_effort"]
                    for row in connection.execute(
                        "SELECT DISTINCT reasoning_effort FROM usage_events"
                    )
                }
            self.assertEqual(efforts, {"max"})
            quota_snapshot = json.loads(
                control.quota_snapshot_path.read_text(encoding="utf-8")
            )
            self.assertEqual(len(quota_snapshot["records"]), 1)
            self.assertEqual(quota_snapshot["records"][0]["used_tokens"], 120)
            self.assertEqual(
                quota_snapshot["records"][0]["raw_used_tokens"],
                60,
            )
            self.assertEqual(
                quota_snapshot["records"][0]["weighted_raw_used_tokens"],
                120,
            )
            self.assertEqual(
                quota_snapshot["records"][0]["quota_unit"],
                "weighted_tokens",
            )
            self.assertEqual(quota_snapshot["records"][0]["limit_tokens"], -1)
            heartbeat = json.loads(
                control.quota_heartbeat_path.read_text(encoding="utf-8")
            )
            self.assertTrue(heartbeat["ok"])
            self.assertGreater(heartbeat["last_success_at"], 0)
            self.assertEqual(heartbeat["fail_open_after_seconds"], 300)
            raw = (root / "state" / "usage.sqlite3").read_bytes()
            self.assertNotIn(records[0]["key"].encode("utf-8"), raw)

    def test_health_command_rejects_degraded_collector(self):
        collector = mock.Mock()
        collector.store.status.return_value = {
            "status": "degraded",
            "heartbeat_at": 1,
        }
        with mock.patch.object(self.module, "UsageCollector", return_value=collector):
            self.assertEqual(self.module.main(["--health"]), 1)

        collector.store.status.return_value = {
            "status": "healthy",
            "heartbeat_at": 1,
        }
        with mock.patch.object(self.module, "UsageCollector", return_value=collector):
            self.assertEqual(self.module.main(["--health"]), 0)

    def test_run_once_skips_disabled_account_services(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            seed_control_plane(root)
            (root / "secrets").mkdir(parents=True, exist_ok=True)
            (root / "secrets" / "cpa-management.key").write_text(
                "test-management-key\n", encoding="utf-8"
            )
            control = self.module.cliproxy.ControlPlane(root)
            control.ensure_layout()
            control.update_account_policy(
                "delta",
                "delta",
                False,
                apply=False,
            )
            collector = self.module.UsageCollector(root)

            with mock.patch.object(collector, "_drain", return_value=()) as drain:
                result = collector.run_once()

            self.assertEqual(result["errors"], [])
            self.assertEqual(
                [call.args[0] for call in drain.call_args_list],
                ["cliproxy-alpha", "cliproxy-beta", "cliproxy-gamma"],
            )


if __name__ == "__main__":
    unittest.main()
