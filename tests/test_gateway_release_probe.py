import unittest
from unittest import mock

from scripts.gateway_release_probe import (
    BLOCKED_PUBLIC_PATHS,
    expected_public_status,
    routed_records,
    verify_blocked_public_paths,
)


class GatewayReleaseProbeTests(unittest.TestCase):
    def setUp(self):
        self.now = 1_800_000_000
        self.heartbeat = {
            "last_success_at": self.now,
            "fail_open_after_seconds": 300,
        }

    def snapshot(self, **updates):
        record = {
            "user_email": "alice@example.com",
            "week_end_at": self.now + 3600,
            "limit_tokens": 100,
            "used_tokens": 10,
        }
        record.update(updates)
        return {"records": [record]}

    def test_under_quota_requires_public_success(self):
        self.assertEqual(
            expected_public_status(
                "alice@example.com", self.snapshot(), self.heartbeat, self.now
            ),
            200,
        )

    def test_exhausted_quota_requires_public_429(self):
        self.assertEqual(
            expected_public_status(
                "alice@example.com",
                self.snapshot(used_tokens=100),
                self.heartbeat,
                self.now,
            ),
            429,
        )

    def test_missing_expired_or_stale_quota_follows_fail_open_contract(self):
        cases = (
            ({"records": []}, self.heartbeat),
            (self.snapshot(week_end_at=self.now), self.heartbeat),
            (
                self.snapshot(used_tokens=100),
                {"last_success_at": self.now - 301, "fail_open_after_seconds": 300},
            ),
        )
        for snapshot, heartbeat in cases:
            with self.subTest(snapshot=snapshot, heartbeat=heartbeat):
                self.assertEqual(
                    expected_public_status(
                        "alice@example.com", snapshot, heartbeat, self.now
                    ),
                    200,
                )

    def test_stale_runtime_loader_follows_fail_open_contract(self):
        self.assertEqual(
            expected_public_status(
                "alice@example.com",
                self.snapshot(used_tokens=100),
                self.heartbeat,
                self.now,
                loader_success_at=self.now - 301,
                fail_open_after=300,
            ),
            200,
        )

    @mock.patch("scripts.gateway_release_probe.request_status", return_value=404)
    def test_blocked_public_paths_require_404_with_the_supplied_key(self, request_status):
        opener = object()

        verify_blocked_public_paths(
            opener,
            "http://gateway:8317/",
            "valid-external-key",
            "inactive Gateway",
        )

        self.assertEqual(request_status.call_count, len(BLOCKED_PUBLIC_PATHS))
        for call, path in zip(request_status.call_args_list, BLOCKED_PUBLIC_PATHS):
            self.assertEqual(
                call.args,
                (opener, "http://gateway:8317" + path, "valid-external-key"),
            )

    @mock.patch(
        "scripts.gateway_release_probe.request_status",
        side_effect=(404, 401),
    )
    def test_blocked_public_path_probe_rejects_authentication_fallthrough(self, _):
        with self.assertRaisesRegex(
            RuntimeError,
            r"/v0/management/auth-files returned 401, expected 404",
        ):
            verify_blocked_public_paths(
                object(),
                "http://gateway:8317",
                "valid-external-key",
                "inactive Gateway",
            )

    def test_routed_records_excludes_disabled_accounts(self):
        class FakeApp:
            @staticmethod
            def accounts():
                return {
                    "alpha": {"group_enabled": True},
                    "beta": {"group_enabled": False},
                }

            @staticmethod
            def active_records():
                return [
                    {"user": "alice@example.com", "account": "alpha", "key": "key-a"},
                    {"user": "alice@example.com", "account": "beta", "key": "key-b"},
                ]

            @staticmethod
            def explicit_user_route(user, accounts=None):
                return "alpha"

        accounts, active, by_account = routed_records(FakeApp())

        self.assertEqual(set(accounts), {"alpha"})
        self.assertEqual(len(active), 2)
        self.assertEqual(set(by_account), {"alpha"})


if __name__ == "__main__":
    unittest.main()
