import importlib.machinery
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

try:
    from fixtures import seed_control_plane
except ImportError:
    from tests.fixtures import seed_control_plane


ROOT = Path(__file__).resolve().parents[1]
MODULE = importlib.machinery.SourceFileLoader(
    "v1_compare_admin",
    str(ROOT / "scripts" / "v1-compare-admin.py"),
).load_module()


def load_control_module():
    path = ROOT / "scripts" / "cliproxy.py"
    spec = importlib.util.spec_from_file_location("cliproxy_v1_compare_test", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class V1CompareAdminTests(unittest.TestCase):
    def test_normalizes_project_scoped_container_rows(self):
        rows = MODULE._normalize_container_rows(
            [
                {
                    "Names": ["/cliproxy-alpha"],
                    "State": "running",
                    "Status": "Up 2 hours (healthy)",
                    "Labels": {
                        "com.docker.compose.project": "cliproxy-multi",
                        "com.docker.compose.service": "cliproxy-alpha",
                    },
                },
                {
                    "Names": ["/oneoff"],
                    "State": "exited",
                    "Status": "Exited (0)",
                    "Labels": {
                        "com.docker.compose.project": "cliproxy-multi",
                    },
                },
            ],
            "cliproxy-multi",
        )
        self.assertEqual(
            rows,
            [
                {
                    "service": "cliproxy-alpha",
                    "name": "cliproxy-alpha",
                    "state": "running",
                    "status": "Up 2 hours (healthy)",
                    "health": "healthy",
                }
            ],
        )

    def test_rejects_any_container_outside_exact_project(self):
        with self.assertRaisesRegex(RuntimeError, "scope is wider"):
            MODULE._normalize_container_rows(
                [
                    {
                        "Labels": {
                            "com.docker.compose.project": "another-project",
                            "com.docker.compose.service": "cliproxy-alpha",
                        }
                    }
                ],
                "cliproxy-multi",
            )

    def test_compare_apply_renders_and_waits_for_its_gateway_without_docker(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / ".v2-isolated-copy.json").write_text("{}\n", encoding="utf-8")
            snapshot = root / "state" / "gateway" / "auth-snapshot.json"
            snapshot.parent.mkdir(parents=True)
            generation = "a" * 32

            def render():
                snapshot.write_text(
                    json.dumps(
                        {
                            "version": 1,
                            "generation": generation,
                            "records": [{"external_key_sha256": "b" * 64}],
                        }
                    ),
                    encoding="utf-8",
                )

            control = mock.Mock()
            control.root = root
            control.auth_snapshot_path = snapshot
            control.render.side_effect = render

            result = MODULE._apply_compare_changes(
                control,
                restart_containers=True,
                timeout=3,
            )

            self.assertEqual(result, {"generation": generation, "records": 1})
            control.render.assert_called_once_with()
            control.wait_for_gateway_snapshot.assert_called_once_with(
                "auth", generation, timeout=3
            )
            self.assertFalse(control.compose.called)

    def test_compare_apply_rejects_unmarked_or_invalid_snapshot(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            snapshot = root / "state" / "gateway" / "auth-snapshot.json"
            snapshot.parent.mkdir(parents=True)
            control = mock.Mock(root=root, auth_snapshot_path=snapshot)
            with self.assertRaisesRegex(RuntimeError, "isolated-copy marker"):
                MODULE._apply_compare_changes(control)

            (root / ".v2-isolated-copy.json").write_text("{}\n", encoding="utf-8")
            control.render.side_effect = lambda: snapshot.write_text(
                '{"generation":"not-a-generation","records":[]}\n',
                encoding="utf-8",
            )
            with self.assertRaisesRegex(RuntimeError, "generation is invalid"):
                MODULE._apply_compare_changes(control)

    def test_compare_compose_allows_only_policy_validation_without_docker(self):
        result = MODULE._validate_compare_compose(
            "config",
            "--quiet",
            capture=True,
        )

        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")
        for command in (
            ("ps", "--services"),
            ("up", "-d", "cliproxy-alpha"),
            ("exec", "-T", "gateway", "openresty", "-s", "reload"),
        ):
            with self.subTest(command=command):
                with self.assertRaisesRegex(RuntimeError, "not allowed"):
                    MODULE._validate_compare_compose(*command)

    def test_policy_activation_failure_restores_accounts_and_routes(self):
        control_module = load_control_module()
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            seed_control_plane(root)
            (root / ".v2-isolated-copy.json").write_text("{}\n", encoding="utf-8")
            control = control_module.ControlPlane(root)
            control.ensure_layout()
            (root / "secrets" / "cpa-management.key").write_text(
                "test-management-key\n",
                encoding="utf-8",
            )
            control.create_user(
                "alice@example.com",
                apply=False,
                initial_account="gamma",
            )
            original_accounts = control._read_account_records()
            original_routes = control._read_routes()
            control.compose = MODULE._validate_compare_compose
            control._reload_gateway_if_running = lambda: MODULE._reload_compare_gateway(
                control,
                timeout=2,
            )
            control.wait_for_gateway_snapshot = mock.Mock(
                side_effect=[RuntimeError("activation failed"), None]
            )

            with self.assertRaisesRegex(RuntimeError, "activation failed"):
                control.update_account_policy(
                    "gamma",
                    "gamma",
                    False,
                    fallback_account="beta",
                )

            self.assertEqual(control._read_account_records(), original_accounts)
            self.assertEqual(control._read_routes(), original_routes)
            self.assertEqual(control.wait_for_gateway_snapshot.call_count, 2)


if __name__ == "__main__":
    unittest.main()
