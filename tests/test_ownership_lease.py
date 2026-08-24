import json
import sqlite3
import tempfile
import threading
import unittest
from pathlib import Path

from scripts.control_plane_store import ControlPlaneStore
from scripts.ownership_lease import (
    LeaseHeldError,
    LeaseLostError,
    LeaseMissingError,
    LeaseStore,
    OwnershipError,
    OwnershipGuard,
    RUNTIME_SCOPE,
)


class MutableClock:
    def __init__(self, value):
        self.value = value

    def __call__(self):
        return self.value


class OwnershipLeaseTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        ControlPlaneStore(self.root)
        self.clock = MutableClock(1_000)
        self.store = LeaseStore(self.root, now=self.clock)

    def tearDown(self):
        self.temporary.cleanup()

    def test_take_join_renew_release_and_fence_stale_generation(self):
        runtime = self.store.take(RUNTIME_SCOPE, "python-v1", 30)
        self.assertEqual(runtime["generation"], 1)
        self.assertEqual(len(runtime["token"]), 64)

        self.clock.value += 5
        joined = self.store.join(RUNTIME_SCOPE, "python-v1", 30)
        self.assertEqual(joined["token"], runtime["token"])
        self.assertEqual(joined["expires_at"], 1_035)
        with self.assertRaises(LeaseHeldError):
            self.store.join(RUNTIME_SCOPE, "go-v2", 30)

        worker = self.store.take("usage-collector", "python-v1:first", 30)
        with self.assertRaises(LeaseHeldError):
            self.store.take("usage-collector", "python-v1:second", 30)
        self.store.release(worker)
        replacement = self.store.take(
            "usage-collector", "python-v1:second", 30
        )
        self.assertEqual(replacement["generation"], worker["generation"] + 1)
        with self.assertRaises(LeaseLostError):
            self.store.renew(worker, 30)

    def test_explicit_runtime_activation_is_required_before_guard_operation(self):
        guard = OwnershipGuard(
            self.root,
            ("admin", "quota"),
            runtime_owner="python-v1",
            ttl_seconds=30,
            store=self.store,
        )
        with self.assertRaises(LeaseMissingError):
            guard.start()

        self.store.take(RUNTIME_SCOPE, "python-v1", 30)
        with OwnershipGuard(
            self.root,
            ("admin", "quota"),
            runtime_owner="python-v1",
            ttl_seconds=30,
            store=self.store,
        ) as active:
            active.assert_owned()
            with self.assertRaises(LeaseHeldError):
                self.store.take("admin", "duplicate", 30)
        self.assertEqual(self.store.read("admin").get("token"), "")
        self.assertEqual(self.store.read("quota").get("token"), "")

    def test_reads_and_renews_go_compatible_payload_shape(self):
        payload = {
            "version": 1,
            "scope": RUNTIME_SCOPE,
            "owner": "python-v1",
            "generation": 7,
            "token": "go-compatible-token",
            "acquired_at": 900,
            "renewed_at": 990,
            "expires_at": 1_030,
        }
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                "INSERT INTO runtime_state(name, payload_json, updated_at) VALUES (?, ?, ?)",
                (
                    "ownership_lease:" + RUNTIME_SCOPE,
                    json.dumps(payload, separators=(",", ":")),
                    990,
                ),
            )
        joined = self.store.join(RUNTIME_SCOPE, "python-v1", 30)
        self.assertEqual(joined["generation"], 7)
        self.assertEqual(joined["token"], "go-compatible-token")
        self.assertEqual(joined["expires_at"], 1_030)

    def test_missing_target_is_not_initialized_as_a_side_effect(self):
        missing = self.root / "missing"
        with self.assertRaises(OwnershipError):
            LeaseStore(missing)
        self.assertFalse(missing.exists())

    def test_corrupt_state_fails_closed(self):
        with sqlite3.connect(self.root / "state" / "control-plane.sqlite3") as connection:
            connection.execute(
                "INSERT INTO runtime_state(name, payload_json, updated_at) VALUES (?, ?, ?)",
                ("ownership_lease:admin", '{"version":99}', 1_000),
            )
        with self.assertRaises(OwnershipError):
            self.store.take("admin", "python-v1", 30)


class HeartbeatFailureStore:
    def join(self, scope, owner, unused_ttl):
        return {
            "scope": scope,
            "owner": owner,
            "generation": 1,
            "token": "runtime",
        }

    def take(self, scope, owner, unused_ttl):
        return {
            "scope": scope,
            "owner": owner,
            "generation": 1,
            "token": "worker",
        }

    def renew(self, unused_lease, unused_ttl):
        raise LeaseLostError("test fence")

    def release(self, unused_lease):
        raise AssertionError("lost lease must not be released")


class OwnershipHeartbeatTests(unittest.TestCase):
    def test_heartbeat_failure_fences_process_callback(self):
        fenced = threading.Event()
        guard = OwnershipGuard(
            ".",
            ("admin",),
            runtime_owner="python-v1",
            ttl_seconds=5,
            store=HeartbeatFailureStore(),
            on_lost=lambda unused_error: fenced.set(),
        ).start()
        self.assertTrue(fenced.wait(3), "ownership heartbeat did not fence in time")
        with self.assertRaises(LeaseLostError):
            guard.assert_owned()
        guard.stop()


if __name__ == "__main__":
    unittest.main()
