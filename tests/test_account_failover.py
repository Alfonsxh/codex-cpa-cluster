import importlib.util
import json
import os
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

try:
    from fixtures import seed_control_plane
except ImportError:
    from tests.fixtures import seed_control_plane


ROOT = Path(__file__).parents[1]
ADMIN = ROOT / "admin"
SCRIPTS = ROOT / "scripts"
sys.path.insert(0, str(ADMIN))
sys.path.insert(0, str(SCRIPTS))


def load_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class AccountFailoverTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.failover_module = load_module(
            "account_failover_tests",
            ADMIN / "account_failover.py",
        )
        cls.control_module = load_module(
            "cliproxy_failover_tests",
            SCRIPTS / "cliproxy.py",
        )
        cls.server_module = load_module(
            "server_failover_tests",
            ADMIN / "server.py",
        )

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        seed_control_plane(self.root)
        (self.root / "secrets").mkdir(parents=True, exist_ok=True)
        self.key_path = self.root / "secrets" / "cpa-management.key"
        self.key_path.write_text("test-management-key\n", encoding="utf-8")
        self.control = self.control_module.ControlPlane(self.root)
        self.control.ensure_layout()
        self.app = self.server_module.AdminApplication(
            root=self.root,
            key_file=self.key_path,
            control=self.control,
        )
        self.now = int(time.time())

    def tearDown(self):
        self.tmp.cleanup()

    @staticmethod
    def quota(account, used, limit_reached=False, status="ok", allowed=True):
        return {
            "account": account,
            "status": status,
            "allowed": allowed,
            "limit_reached": limit_reached,
            "weekly": (
                {
                    "used_percent": 100.0 if limit_reached else float(used),
                    "remaining_percent": 0.0 if limit_reached else 100.0 - float(used),
                    "limit_reached": limit_reached,
                    "reset_at": 2_000_000_000,
                }
                if status == "ok"
                else None
            ),
        }

    def runtime(self):
        self.app._compose_ps = mock.Mock(
            return_value=[
                {"service": service, "state": "running"}
                for service in self.control.services().values()
            ]
        )
        self.control.auth_status = mock.Mock(
            return_value={account: {"files": 1} for account in self.control.accounts()}
        )

    def quota_payload(self, overrides=None):
        overrides = overrides or {}
        accounts = []
        for account in self.control.accounts():
            accounts.append(overrides.get(account, self.quota(account, 30)))
        return {
            "generated_at": self.now,
            "cached": False,
            "refreshing": False,
            "accounts": accounts,
        }

    def test_account_states_require_fresh_healthy_quota_and_reserve(self):
        accounts = self.control.accounts()
        service_names = self.control.services()
        services = {
            service: {"service": service, "state": "running"}
            for service in service_names.values()
        }
        auth = {account: {"files": 1} for account in accounts}
        auth["gamma"] = {"files": 0}
        payload = self.quota_payload(
            {
                "alpha": self.quota("alpha", 100, limit_reached=True),
                "beta": self.quota("beta", 10),
                "gamma": self.quota("gamma", 20),
                "delta": self.quota("delta", 95),
            }
        )

        states = self.failover_module.build_account_states(
            accounts,
            payload,
            services,
            service_names,
            auth,
            self.now,
            stale_after_seconds=120,
            reserve_percent=5,
        )

        self.assertTrue(states["alpha"]["exhausted"])
        self.assertTrue(states["beta"]["eligible"])
        self.assertEqual(states["gamma"]["reason"], "oauth_missing")
        self.assertEqual(states["delta"]["reason"], "reserve_reached")

        payload["generated_at"] = self.now - 121
        stale = self.failover_module.build_account_states(
            accounts,
            payload,
            services,
            service_names,
            auth,
            self.now,
            stale_after_seconds=120,
            reserve_percent=5,
        )
        self.assertFalse(any(item["eligible"] for item in stale.values()))
        self.assertFalse(stale["alpha"]["exhausted"])

        additional_only = self.quota_payload()
        additional_only["accounts"][0] = {
            "account": "alpha",
            "status": "ok",
            "allowed": True,
            "limit_reached": False,
            "weekly": {
                "key": "additional:model:primary_window",
                "used_percent": 100,
                "remaining_percent": 0,
                "limit_reached": True,
            },
            "weekly_windows": [],
        }
        additional_states = self.failover_module.build_account_states(
            accounts,
            additional_only,
            services,
            service_names,
            auth,
            self.now,
            stale_after_seconds=120,
            reserve_percent=5,
        )
        self.assertFalse(additional_states["alpha"]["exhausted"])
        self.assertEqual(additional_states["alpha"]["reason"], "quota_unavailable")

    def test_weighted_plan_is_deterministic_and_prefers_more_headroom(self):
        users = {"user{}@example.com".format(index) for index in range(12)}
        routes = {user: "source" for user in users}
        states = {
            "source": {"exhausted": True, "eligible": False, "headroom": 0},
            "low": {
                "exhausted": False,
                "eligible": True,
                "headroom": 85,
                "used_percent": 10,
            },
            "middle": {
                "exhausted": False,
                "eligible": True,
                "headroom": 55,
                "used_percent": 40,
            },
            "high": {
                "exhausted": False,
                "eligible": True,
                "headroom": 25,
                "used_percent": 70,
            },
        }

        plan = self.failover_module.plan_failover(routes, users, users, states)

        self.assertEqual(plan["planned_users"], 12)
        self.assertEqual(plan["destinations"], {"high": 2, "low": 6, "middle": 4})
        self.assertEqual(plan["unassigned_users"], 0)

    def test_retired_observe_runtime_state_fails_closed(self):
        self.control.store.write_runtime_state(
            "account_failover",
            {**self.app.account_failover._default_state(), "mode": "observe"},
        )

        state = self.app.account_failover.read_state()

        self.assertEqual(state["mode"], "off")

    def test_active_mode_moves_routes_once_and_persists_private_status(self):
        for user in ("alice@example.com", "bob@example.com"):
            self.control.create_user(user, apply=False, initial_account="alpha")
        self.control.update_configuration({"account_failover.mode": "active"})
        self.runtime()
        payload = self.quota_payload(
            {
                "alpha": self.quota("alpha", 100, limit_reached=True),
                "beta": self.quota("beta", 10),
                "gamma": self.quota("gamma", 50),
                "delta": self.quota("delta", 95),
            }
        )
        self.app.usage_limits = mock.Mock(return_value=payload)
        with mock.patch.object(
            self.control,
            "publish_auth_snapshot",
            return_value={"generation": "c" * 32, "records": 2},
        ) as publish:
            result = self.app.account_failover.run_once(self.app, now=self.now)

        self.assertEqual(result["moved_users"], 2)
        self.assertEqual(
            {
                self.control.explicit_user_route("alice@example.com"),
                self.control.explicit_user_route("bob@example.com"),
            },
            {"beta", "gamma"},
        )
        publish.assert_called_once_with(wait=True)
        state = self.control.store.read_runtime_state("account_failover")
        state_text = json.dumps(state, ensure_ascii=False)
        self.assertEqual(state["last_action"]["moved_users"], 2)
        self.assertEqual(state["last_action"]["snapshot_generation"], "c" * 32)
        self.assertNotIn("alice@example.com", state_text)
        self.assertNotIn("bob@example.com", state_text)
        audit = (self.root / "logs" / "admin" / "audit.jsonl").read_text(
            encoding="utf-8"
        )
        self.assertIn('"action":"account.failover.rebalance"', audit)
        self.assertNotIn("alice@example.com", audit)

    def test_manual_rebalance_uses_the_same_weights_while_automatic_mode_is_off(self):
        for user in (
            "alice@example.com",
            "bob@example.com",
            "carol@example.com",
        ):
            self.control.create_user(user, apply=False, initial_account="alpha")
        self.runtime()
        self.control.update_configuration({"account_failover.mode": "off"})
        payload = self.quota_payload(
            {
                "alpha": self.quota("alpha", 30),
                "beta": self.quota("beta", 10),
                "gamma": self.quota("gamma", 50),
                "delta": self.quota("delta", 95),
            }
        )
        self.app.refresh_usage_limits_sync = mock.Mock(return_value=payload)
        with mock.patch.object(
            self.control,
            "publish_auth_snapshot",
            return_value={"generation": "e" * 32, "records": 3},
        ) as publish:
            result = self.app.account_failover.rebalance_account(
                self.app,
                "alpha",
                now=self.now,
            )

        self.assertEqual(result["moved_users"], 3)
        self.assertEqual(result["destinations"], {"beta": 2, "gamma": 1})
        self.assertEqual(self.control.configuration()["values"]["account_failover.mode"], "off")
        self.assertNotIn("alpha", set(self.control._read_routes().values()))
        self.app.refresh_usage_limits_sync.assert_called_once_with()
        publish.assert_called_once_with(wait=True)
        state = self.app.account_failover.read_state()
        self.assertEqual(state["last_action"]["trigger"], "manual")
        audit = (self.root / "logs" / "admin" / "audit.jsonl").read_text(
            encoding="utf-8"
        )
        self.assertIn('"action":"account.failover.manual_rebalance"', audit)

    def test_all_accounts_exhausted_preserves_routes(self):
        self.control.create_user(
            "alice@example.com",
            apply=False,
            initial_account="alpha",
        )
        self.control.update_configuration({"account_failover.mode": "active"})
        self.runtime()
        payload = self.quota_payload(
            {
                account: self.quota(account, 100, limit_reached=True)
                for account in self.control.accounts()
            }
        )
        self.app.usage_limits = mock.Mock(return_value=payload)
        self.control.set_user_routes = mock.Mock()

        result = self.app.account_failover.run_once(self.app, now=self.now)

        self.assertEqual(result["plan"]["unassigned_users"], 1)
        self.control.set_user_routes.assert_not_called()
        self.assertEqual(
            self.control.explicit_user_route("alice@example.com"),
            "alpha",
        )
        audit = (self.root / "logs" / "admin" / "audit.jsonl").read_text(
            encoding="utf-8"
        )
        self.assertIn('"action":"account.failover.capacity_unavailable"', audit)


if __name__ == "__main__":
    unittest.main()
