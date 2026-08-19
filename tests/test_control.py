import importlib.util
import base64
import json
import os
import subprocess
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

try:
    from fixtures import TEST_ACCOUNT_IDS, seed_control_plane
except ImportError:
    from tests.fixtures import TEST_ACCOUNT_IDS, seed_control_plane


MODULE_PATH = Path(__file__).parents[1] / "scripts" / "cliproxy.py"
ACCOUNT_IDS = TEST_ACCOUNT_IDS
CLIPROXY_IMAGE = "docker.m.daocloud.io/eceasy/cli-proxy-api:v7.1.23"


def load_module():
    spec = importlib.util.spec_from_file_location("cliproxy", MODULE_PATH)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class ControlTests(unittest.TestCase):
    def setUp(self):
        self.module = load_module()
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        seed_control_plane(self.root)
        self.app = self.module.ControlPlane(self.root)
        self.app.ensure_layout()
        (self.root / "secrets" / "cpa-management.key").write_text(
            "test-management-key\n", encoding="utf-8"
        )

    def tearDown(self):
        self.tmp.cleanup()

    def test_create_user_generates_one_named_key_linked_to_all_accounts(self):
        created = self.app.create_user("Alice@Example.com", apply=False)

        self.assertEqual([item["account"] for item in created], ACCOUNT_IDS)
        self.assertEqual(
            [item["label"] for item in created],
            ["alice@example.com:" + account for account in ACCOUNT_IDS],
        )
        self.assertEqual(len({item["key"] for item in created}), 1)
        for item in created:
            self.assertEqual(item["user"], "alice@example.com")
            self.assertRegex(
                item["key"],
                r"^cpa_alice_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$",
            )
        self.assertIsNone(self.app.explicit_user_route("alice@example.com"))
        self.assertNotIn("alice@example.com", self.app._read_routes())
        # 兼容旧调用方的计算型回退仍保留，但不会用于使用中心或网关映射。
        self.assertEqual(self.app.user_route("alice@example.com"), "alpha")

    def test_create_key_preserves_explicit_account_selection(self):
        self.app.create_key("alice@example.com:gamma", apply=False)

        self.assertEqual(
            self.app.explicit_user_route("alice@example.com"),
            "gamma",
        )

    def test_routed_user_counts_only_counts_persisted_routes(self):
        self.app.create_user("alice@example.com", apply=False, initial_account="alpha")
        self.app.create_user("bob@example.com", apply=False, initial_account="gamma")
        self.app.create_user("carol@example.com", apply=False)

        self.assertEqual(
            self.app.routed_user_counts(),
            {"alpha": 1, "beta": 0, "gamma": 1, "delta": 0},
        )

    def test_batch_user_routes_publish_one_snapshot_and_check_expected_sources(self):
        for user in ("alice@example.com", "bob@example.com"):
            self.app.create_user(user, apply=False, initial_account="alpha")
        with mock.patch.object(
            self.app,
            "publish_auth_snapshot",
            return_value={"generation": "a" * 32, "records": 2},
        ) as publish:
            result = self.app.set_user_routes(
                {
                    "alice@example.com": "beta",
                    "bob@example.com": "gamma",
                },
                expected_routes={
                    "alice@example.com": "alpha",
                    "bob@example.com": "alpha",
                },
                wait_for_gateway=True,
            )

        self.assertEqual(result["moved_users"], 2)
        self.assertEqual(result["destinations"], {"beta": 1, "gamma": 1})
        self.assertEqual(self.app.explicit_user_route("alice@example.com"), "beta")
        self.assertEqual(self.app.explicit_user_route("bob@example.com"), "gamma")
        publish.assert_called_once_with(wait=True)

        with self.assertRaisesRegex(ValueError, "路由已变化"):
            self.app.set_user_routes(
                {"alice@example.com": "gamma"},
                expected_routes={"alice@example.com": "alpha"},
                apply=False,
            )

    def test_batch_user_routes_restore_routes_when_snapshot_publish_fails(self):
        self.app.create_user(
            "alice@example.com",
            apply=False,
            initial_account="alpha",
        )
        with mock.patch.object(
            self.app,
            "publish_auth_snapshot",
            side_effect=[RuntimeError("snapshot failed"), {"generation": "b" * 32}],
        ):
            with self.assertRaisesRegex(RuntimeError, "snapshot failed"):
                self.app.set_user_routes(
                    {"alice@example.com": "beta"},
                    expected_routes={"alice@example.com": "alpha"},
                )

        self.assertEqual(self.app.explicit_user_route("alice@example.com"), "alpha")

    def test_key_username_namespace_normalizes_email_local_part(self):
        created = self.app.create_user("Alice.Smith+Dev@Example.com", apply=False)

        for item in created:
            self.assertIn("_alice_smith_dev_", item["key"])
            self.assertRegex(
                item["key"].rsplit("_", 1)[1],
                r"^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$",
            )

    def test_uuid_distinguishes_users_with_same_readable_namespace(self):
        dotted = self.app.create_user("alice.smith@example.com", apply=False)[0]["key"]
        dashed = self.app.create_user("alice-smith@example.com", apply=False)[0]["key"]

        self.assertTrue(dotted.startswith("cpa_alice_smith_"))
        self.assertTrue(dashed.startswith("cpa_alice_smith_"))
        self.assertNotEqual(dotted, dashed)

    def test_multiple_email_domains_and_new_key_prefix_are_configurable(self):
        self.app.update_configuration(
            {
                "identity.allowed_email_domains": ["example.com", "example.org"],
                "identity.key_prefix": "team_",
            }
        )

        first = self.app.create_user("alice@example.com", apply=False)[0]["key"]
        second = self.app.create_user("bob@example.org", apply=False)[0]["key"]

        self.assertTrue(first.startswith("team_alice_"))
        self.assertTrue(second.startswith("team_bob_"))
        with self.assertRaisesRegex(ValueError, "example.com.*example.org"):
            self.app.create_user("carol@outside.test", apply=False)

        self.app.update_configuration({"identity.key_prefix": "next_"})
        self.assertIn(first, {item["key"] for item in self.app.active_records()})
        rotated = self.app.rotate_key("alice@example.com:alpha", apply=False)
        self.assertTrue(rotated["key"].startswith("next_alice_"))

    def test_fresh_install_has_neutral_defaults_and_no_seed_accounts(self):
        with tempfile.TemporaryDirectory() as directory:
            app = self.module.ControlPlane(directory)
            app.ensure_layout()
            values = app.configuration()["values"]
            accounts = app.accounts()

        self.assertEqual(accounts, {})
        self.assertEqual(values["branding.product_name"], "Codex CPA Cluster")
        self.assertEqual(values["identity.allowed_email_domains"], [])
        self.assertEqual(values["identity.key_prefix"], "cpa_")

    def test_known_mutable_base_image_defaults_are_pinned(self):
        self.app.store.write_settings(
            {
                "runtime.admin_base_image": (
                    "docker:27.5.1-cli"
                ),
                "runtime.gateway_image": (
                    "openresty/openresty:alpine-fat"
                ),
            }
        )

        values = self.app.configuration()["values"]
        stored = self.app.store.read_settings()

        self.assertEqual(
            values["runtime.admin_base_image"],
            self.module.DEFAULT_ADMIN_BASE_IMAGE,
        )
        self.assertEqual(
            values["runtime.gateway_image"],
            self.module.DEFAULT_GATEWAY_BASE_IMAGE,
        )
        self.assertEqual(
            stored["runtime.admin_base_image"],
            self.module.DEFAULT_ADMIN_BASE_IMAGE,
        )
        self.assertEqual(
            stored["runtime.gateway_image"],
            self.module.DEFAULT_GATEWAY_BASE_IMAGE,
        )

        self.app.store.write_settings(
            {
                "runtime.admin_base_image": (
                    "registry.example.com/admin:v2@sha256:" + "a" * 64
                ),
                "runtime.gateway_image": (
                    "registry.example.com/gateway:v2@sha256:" + "b" * 64
                ),
            }
        )
        custom = self.app.configuration()["values"]
        self.assertEqual(
            custom["runtime.admin_base_image"],
            "registry.example.com/admin:v2@sha256:" + "a" * 64,
        )
        self.assertEqual(
            custom["runtime.gateway_image"],
            "registry.example.com/gateway:v2@sha256:" + "b" * 64,
        )

        self.app.store.write_settings({})
        (self.root / ".env").write_text(
            "ADMIN_BASE_IMAGE=docker:27.5.1-cli\n"
            "GATEWAY_IMAGE=docker.m.daocloud.io/openresty/"
            "openresty:1.31.1.1-2-alpine-fat\n",
            encoding="utf-8",
        )
        environment_values = self.app.configuration()["values"]
        self.assertEqual(
            environment_values["runtime.admin_base_image"],
            self.module.DEFAULT_ADMIN_BASE_IMAGE,
        )
        self.assertEqual(
            environment_values["runtime.gateway_image"],
            self.module.DEFAULT_GATEWAY_BASE_IMAGE,
        )

    def test_json_profile_is_validated_and_persisted_in_database(self):
        result = self.app.apply_configuration_profile(
            {
                "version": 1,
                "values": {
                    "branding.product_name": "Example CPA",
                    "identity.allowed_email_domains": ["example.com", "example.org"],
                    "identity.key_prefix": "team_",
                },
            }
        )

        self.assertEqual(result["values"]["branding.product_name"], "Example CPA")
        stored = self.app.store.read_settings()
        self.assertEqual(stored["identity.key_prefix"], "team_")
        self.assertEqual(
            stored["identity.allowed_email_domains"],
            ["example.com", "example.org"],
        )
        with self.assertRaisesRegex(ValueError, "不支持的配置项"):
            self.app.apply_configuration_profile(
                {
                    "version": 1,
                    "values": {
                        "branding.product_name": "Should Not Persist",
                        "private.organization": "example",
                    },
                }
            )
        self.assertEqual(
            self.app.configuration()["values"]["branding.product_name"],
            "Example CPA",
        )

        with self.assertRaisesRegex(ValueError, "查询参数或片段"):
            self.app.update_configuration(
                {"branding.public_base_url": "https://cpa.example.com/?source=test"}
            )

    def test_deployment_profile_is_imported_only_once(self):
        payload = {
            "version": 1,
            "values": {"branding.product_name": "Example CPA"},
        }

        first = self.app.import_configuration_profile_once(payload)
        second = self.app.import_configuration_profile_once(payload)

        self.assertTrue(first["imported"])
        self.assertFalse(second["imported"])
        with self.assertRaisesRegex(ValueError, "配置中心"):
            self.app.import_configuration_profile_once(
                {
                    "version": 1,
                    "values": {"branding.product_name": "Unexpected Override"},
                }
            )
        self.assertEqual(
            self.app.configuration()["values"]["branding.product_name"],
            "Example CPA",
        )

    def test_deployment_profile_can_be_registered_without_overwriting_existing_settings(self):
        self.app.update_configuration({"branding.product_name": "Configured in UI"})
        payload = {
            "version": 1,
            "values": {"branding.product_name": "Stale deployment profile"},
        }

        result = self.app.import_configuration_profile_once(
            payload,
            preserve_existing=True,
        )

        self.assertTrue(result["imported"])
        self.assertTrue(result["preserved_existing"])
        self.assertEqual(result["changed"], [])
        self.assertEqual(
            self.app.configuration()["values"]["branding.product_name"],
            "Configured in UI",
        )

    def test_json_profile_can_import_validated_logo_into_database(self):
        logo = (
            b'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20">'
            b'<circle cx="10" cy="10" r="8" fill="#246b4a"/></svg>'
        )
        result = self.app.apply_configuration_profile(
            {
                "version": 1,
                "values": {"branding.product_name": "Example CPA"},
                "branding": {
                    "logo": {
                        "filename": "example.svg",
                        "content_type": "image/svg+xml",
                        "data_base64": base64.b64encode(logo).decode("ascii"),
                    }
                },
            }
        )

        self.assertEqual(result["branding"]["logo"]["content_type"], "image/svg+xml")
        self.assertEqual(self.app.store.branding_asset("logo")["content"], logo)
        self.assertTrue(self.app.public_site_configuration()["logo"]["custom"])
        with self.assertRaisesRegex(ValueError, "编码无效"):
            self.app.apply_configuration_profile(
                {
                    "version": 1,
                    "values": {"branding.product_name": "Example CPA"},
                    "branding": {
                        "logo": {
                            "filename": "bad.svg",
                            "content_type": "image/svg+xml",
                            "data_base64": "%%%invalid%%%",
                        }
                    },
                }
            )

    def test_layout_does_not_generate_dashboard_credentials(self):
        self.assertFalse((self.root / "secrets" / "dashboard-admin.txt").exists())
        self.assertFalse((self.root / "gateway" / "dashboard.htpasswd").exists())
        self.app.ensure_layout()
        self.assertFalse((self.root / "secrets" / "dashboard-admin.txt").exists())
        self.assertFalse((self.root / "gateway" / "dashboard.htpasswd").exists())

    def test_cliproxy_image_status_compares_running_containers_with_local_target(self):
        target_id = "sha256:" + "a" * 64
        old_id = "sha256:" + "b" * 64

        self.app._docker_json = mock.Mock(
            return_value={
                "Id": target_id,
                "Created": "2026-07-21T05:18:17Z",
                "RepoDigests": [CLIPROXY_IMAGE.split(":", 1)[0] + "@sha256:new"],
            }
        )

        def docker_json_rows(*args, **kwargs):
            if args == ("container", "ls", "-a", "--format", "json"):
                return [
                    {"Names": "cliproxy-alpha"},
                    {"Names": "cliproxy-beta"},
                ]
            if args[:2] == ("container", "inspect"):
                return [
                    {
                        "Name": "/cliproxy-alpha",
                        "Image": old_id,
                        "Config": {"Image": CLIPROXY_IMAGE},
                        "State": {"Running": True, "Status": "running"},
                    },
                    {
                        "Name": "/cliproxy-beta",
                        "Image": target_id,
                        "Config": {"Image": CLIPROXY_IMAGE},
                        "State": {"Running": True, "Status": "running"},
                    },
                ]
            if args == ("image", "ls", "--format", "json"):
                return [
                    {
                        "Repository": "cliproxy-cpa-rollback",
                        "Tag": "alpha",
                    }
                ]
            return []

        self.app._docker_json_rows = mock.Mock(side_effect=docker_json_rows)
        self.app.store.write_runtime_state(
            "cliproxy_image",
            {
                "candidate": {
                    "version": "v7.2.111",
                    "image_id": target_id,
                    "resolved_ref": CLIPROXY_IMAGE.split(":", 1)[0] + ":v7.2.111@sha256:new",
                }
            },
        )

        status = self.app.cliproxy_image_status()

        self.assertTrue(status["local_image"]["available"])
        self.assertEqual(status["local_image"]["short_id"], "a" * 12)
        self.assertEqual(status["local_image"]["version"], "v7.2.111")
        self.assertEqual(status["running_count"], 2)
        self.assertEqual(status["current_count"], 1)
        self.assertEqual(status["outdated_count"], 1)
        accounts = {item["account"]: item for item in status["accounts"]}
        self.assertFalse(accounts["alpha"]["using_target"])
        self.assertTrue(accounts["beta"]["using_target"])
        self.assertEqual(accounts["alpha"]["image_short_id"], "b" * 12)
        self.assertTrue(accounts["alpha"]["rollback_available"])
        self.app._docker_json.assert_called_once_with(
            "image", "inspect", CLIPROXY_IMAGE
        )
        self.assertEqual(self.app._docker_json_rows.call_count, 3)

    def test_cliproxy_image_status_excludes_disabled_accounts_from_summary(self):
        records = self.app.store.read_accounts()
        for item in records:
            item["group_enabled"] = item["id"] == "alpha"
            item["default_group"] = item["id"] == "alpha"
        self.app.store.write_accounts(records)
        target_id = "sha256:" + "a" * 64
        old_id = "sha256:" + "b" * 64
        self.app._docker_json = mock.Mock(return_value={"Id": target_id})

        def docker_json_rows(*args, **kwargs):
            if args == ("container", "ls", "-a", "--format", "json"):
                return [
                    {"Names": "cliproxy-alpha"},
                    {"Names": "cliproxy-beta"},
                ]
            if args[:2] == ("container", "inspect"):
                return [
                    {
                        "Name": "/cliproxy-alpha",
                        "Image": old_id,
                        "State": {"Running": True, "Status": "running"},
                    },
                    {
                        "Name": "/cliproxy-beta",
                        "Image": target_id,
                        "State": {"Running": True, "Status": "running"},
                    },
                ]
            return []

        self.app._docker_json_rows = mock.Mock(side_effect=docker_json_rows)

        status = self.app.cliproxy_image_status()

        self.assertEqual(status["running_count"], 1)
        self.assertEqual(status["current_count"], 0)
        self.assertEqual(status["outdated_count"], 1)
        accounts = {item["account"]: item for item in status["accounts"]}
        self.assertTrue(accounts["alpha"]["enabled"])
        self.assertFalse(accounts["beta"]["enabled"])

    def test_cliproxy_image_status_ignores_version_cached_for_an_old_image_id(self):
        current_id = "sha256:" + "a" * 64
        self.app.store.write_runtime_state(
            "cliproxy_image",
            {
                "candidate": {
                    "version": "v7.2.100",
                    "image_id": "sha256:" + "b" * 64,
                    "resolved_ref": "registry.example.com/cpa:v7.2.100@sha256:" + "c" * 64,
                }
            },
        )
        self.app._docker_json = mock.Mock(return_value={"Id": current_id})
        self.app._docker_json_rows = mock.Mock(return_value=[])

        status = self.app.cliproxy_image_status()

        self.assertEqual(status["candidate"], {})
        self.assertEqual(status["local_image"]["version"], "")

    def test_cliproxy_image_status_keeps_versions_for_partially_updated_accounts(self):
        old_id = "sha256:" + "a" * 64
        new_id = "sha256:" + "b" * 64
        self.app.store.write_runtime_state(
            "cliproxy_image",
            {
                "applied": {
                    "version": "v7.2.100",
                    "image_id": old_id,
                    "resolved_ref": old_id,
                }
            },
        )
        self.app._commit_cliproxy_applied(
            {
                "version": "v7.2.111",
                "image_id": new_id,
                "resolved_ref": new_id,
            }
        )
        self.app._docker_json = mock.Mock(return_value={"Id": new_id})

        def docker_json_rows(*args, **kwargs):
            if args == ("container", "ls", "-a", "--format", "json"):
                return [
                    {"Names": "cliproxy-alpha"},
                    {"Names": "cliproxy-beta"},
                ]
            if args[:2] == ("container", "inspect"):
                return [
                    {
                        "Name": "/cliproxy-alpha",
                        "Image": new_id,
                        "State": {"Running": True, "Status": "running"},
                    },
                    {
                        "Name": "/cliproxy-beta",
                        "Image": old_id,
                        "State": {"Running": True, "Status": "running"},
                    },
                ]
            return []

        self.app._docker_json_rows = mock.Mock(side_effect=docker_json_rows)

        status = self.app.cliproxy_image_status()

        accounts = {item["account"]: item for item in status["accounts"]}
        self.assertEqual(accounts["alpha"]["version"], "v7.2.111")
        self.assertEqual(accounts["beta"]["version"], "v7.2.100")

    def test_pull_cliproxy_image_pulls_configured_reference_and_verifies_local_image(self):
        self.app._docker = mock.Mock()
        self.app._docker_json = mock.Mock(
            return_value={"Id": "sha256:" + "c" * 64}
        )
        self.app._resolve_cliproxy_image_identity = mock.Mock(
            return_value={
                "source_ref": CLIPROXY_IMAGE,
                "version": "v7.2.111",
                "commit": "abc123",
                "built_at": "2026-08-19T01:02:03Z",
                "image_id": "sha256:" + "c" * 64,
                "image_short_id": "c" * 12,
                "repo_digest": "eceasy/cli-proxy-api@sha256:" + "d" * 64,
                "repo_digests": [],
                "resolved_ref": "eceasy/cli-proxy-api:v7.2.111@sha256:" + "d" * 64,
            }
        )

        self.app.pull_cliproxy_image()

        self.app._docker.assert_called_once_with(
            "pull", CLIPROXY_IMAGE
        )
        self.app._docker_json.assert_called_once_with(
            "image", "inspect", CLIPROXY_IMAGE
        )
        candidate = self.app.store.read_runtime_state("cliproxy_image")["candidate"]
        self.assertEqual(candidate["version"], "v7.2.111")
        self.assertEqual(candidate["image_id"], "sha256:" + "c" * 64)

    def test_pull_cliproxy_image_does_not_replace_applied_compose_projection(self):
        old_id = "sha256:" + "a" * 64
        new_id = "sha256:" + "b" * 64
        self.app.store.write_runtime_state(
            "cliproxy_image",
            {
                "applied": {
                    "version": "v7.2.100",
                    "image_id": old_id,
                    "resolved_ref": old_id,
                }
            },
        )
        self.app.render_compose_environment()
        self.app._docker = mock.Mock()
        self.app._docker_json = mock.Mock(return_value={"Id": new_id})
        self.app._resolve_cliproxy_image_identity = mock.Mock(
            return_value={
                "source_ref": CLIPROXY_IMAGE,
                "version": "v7.2.111",
                "image_id": new_id,
                "image_short_id": "b" * 12,
                "repo_digest": "",
                "repo_digests": [],
                "resolved_ref": new_id,
            }
        )

        self.app.pull_cliproxy_image()

        projection = (self.root / "state" / "compose.env").read_text(
            encoding="utf-8"
        )
        self.assertIn("CLIPROXY_IMAGE=" + old_id, projection)
        self.assertNotIn("CLIPROXY_IMAGE=" + new_id, projection)

    def test_cliproxy_version_banner_is_parsed_without_network_or_privileges(self):
        image_id = "sha256:" + "d" * 64
        inspected = {
            "Id": image_id,
            "RepoDigests": ["registry.example.com/cpa@sha256:" + "e" * 64],
            "Config": {"Cmd": ["./CLIProxyAPI"]},
        }
        with mock.patch.object(
            self.module.subprocess,
            "run",
            return_value=mock.Mock(
                stdout="CLIProxyAPI Version: v7.2.111, Commit: abc123, BuiltAt: 2026-08-19T01:02:03Z\n",
                stderr="",
            ),
        ) as run:
            identity = self.app._resolve_cliproxy_image_identity(
                "registry.example.com/cpa:latest", image=inspected
            )

        self.assertEqual(identity["version"], "v7.2.111")
        self.assertEqual(
            identity["resolved_ref"],
            "registry.example.com/cpa:v7.2.111@sha256:" + "e" * 64,
        )
        command = run.call_args.args[0]
        self.assertIn("--network", command)
        self.assertIn("none", command)
        self.assertIn("--read-only", command)
        self.assertIn("no-new-privileges", command)
        self.assertEqual(command[-2:], ["./CLIProxyAPI", "-h"])

    def test_cliproxy_version_probe_passes_help_to_declared_entrypoint(self):
        with mock.patch.object(
            self.module.subprocess,
            "run",
            return_value=mock.Mock(stdout="", stderr=""),
        ) as run:
            self.app._resolve_cliproxy_image_identity(
                "registry.example.com/cpa:latest",
                image={
                    "Id": "sha256:" + "d" * 64,
                    "Config": {
                        "Entrypoint": ["/app/CLIProxyAPI"],
                        "Cmd": ["-config", "/app/config.yaml"],
                    },
                },
            )

        self.assertEqual(run.call_args.args[0][-1:], ["-h"])

    def test_cliproxy_image_identity_prefers_valid_label_and_sorts_semver_tags(self):
        image_id = "sha256:" + "d" * 64
        with mock.patch.object(
            self.module.subprocess,
            "run",
            return_value=mock.Mock(
                stdout="CLIProxyAPI Version: development, Commit: banner, BuiltAt: now\n",
                stderr="",
            ),
        ):
            labelled = self.app._resolve_cliproxy_image_identity(
                "registry.example.com/cpa:latest",
                image={
                    "Id": image_id,
                    "Config": {
                        "Labels": {"org.opencontainers.image.version": "v7.2.10"}
                    },
                    "RepoTags": ["registry.example.com/cpa:v7.2.9"],
                },
            )
            tagged = self.app._resolve_cliproxy_image_identity(
                "registry.example.com/cpa:latest",
                image={
                    "Id": image_id,
                    "RepoTags": [
                        "registry.example.com/cpa:v7.2.9",
                        "registry.example.com/cpa:v7.2.10",
                        "registry.example.com/cpa:latest",
                    ],
                },
            )

        self.assertEqual(labelled["version"], "v7.2.10")
        self.assertEqual(tagged["version"], "v7.2.10")

    def test_seed_cliproxy_applied_image_preserves_running_legacy_image_once(self):
        image_id = "sha256:" + "f" * 64
        identity = {
            "source_ref": CLIPROXY_IMAGE,
            "version": "v7.2.100",
            "commit": "abc123",
            "built_at": "2026-08-18T01:02:03Z",
            "image_id": image_id,
            "image_short_id": "f" * 12,
            "repo_digest": "",
            "repo_digests": [],
            "resolved_ref": image_id,
        }
        self.app._docker_json = mock.Mock(return_value={"Id": image_id})
        self.app._resolve_cliproxy_image_identity = mock.Mock(
            return_value=dict(identity)
        )

        applied = self.app.seed_cliproxy_applied_image(image_id)
        second = self.app.seed_cliproxy_applied_image("sha256:" + "e" * 64)

        self.assertEqual(applied["version"], "v7.2.100")
        self.assertIsNone(second)
        self.app._docker_json.assert_called_once_with("image", "inspect", image_id)
        state = self.app.store.read_runtime_state("cliproxy_image")
        self.assertEqual(state["applied"]["resolved_ref"], image_id)
        projection = (self.root / "state" / "compose.env").read_text(encoding="utf-8")
        self.assertIn("CLIPROXY_IMAGE=" + image_id, projection)

    def test_pull_cliproxy_image_rejects_concurrent_runtime_operation(self):
        competing = self.module.ControlPlane(self.root)
        competing._docker = mock.Mock()

        with self.app.runtime_operation_lock("测试操作"):
            with self.assertRaisesRegex(ValueError, "另一个发布或运行时操作"):
                competing.pull_cliproxy_image()

        competing._docker.assert_not_called()

    def test_update_cliproxy_image_recreates_one_running_account(self):
        target_id = "sha256:" + "d" * 64
        old_id = "sha256:" + "e" * 64

        def docker_json(*args):
            if args == ("image", "inspect", CLIPROXY_IMAGE):
                return {"Id": target_id}
            if args == ("container", "inspect", "cliproxy-alpha"):
                return {
                    "Image": old_id,
                    "State": {"Running": True, "Status": "running"},
                }
            return None

        self.app._docker_json = mock.Mock(side_effect=docker_json)
        self.app._docker = mock.Mock()
        self.app._compose_with_image = mock.Mock()
        self.app._probe_account_service = mock.Mock()

        self.app.update_cliproxy_image("alpha")

        self.app._docker.assert_called_once_with(
            "image", "tag", old_id, "cliproxy-cpa-rollback:alpha"
        )
        self.app._compose_with_image.assert_called_once_with(
            target_id,
            "up",
            "-d",
            "--no-deps",
            "--force-recreate",
            "cliproxy-alpha",
        )
        self.app._probe_account_service.assert_called_once_with("alpha")
        applied = self.app.store.read_runtime_state("cliproxy_image")["applied"]
        self.assertEqual(applied["image_id"], target_id)
        self.assertEqual(applied["resolved_ref"], target_id)
        self.assertIn("CLIPROXY_IMAGE=" + target_id, (self.root / "state" / "compose.env").read_text(encoding="utf-8"))

    def test_account_probe_uses_internal_key_after_gateway_key_translation(self):
        created = self.app.create_user("alice@example.com", apply=False)
        external_key = created[0]["key"]
        internal_key = self.app.sync_internal_keys()["alice@example.com"]["key"]

        class Response:
            status = 200

            def __enter__(self):
                return self

            def __exit__(self, *unused):
                return False

            @staticmethod
            def read():
                return b'{"data":[{"id":"gpt-5.3-codex"}]}'

        self.app._docker_json = mock.Mock(
            return_value={"State": {"Running": True}}
        )
        with mock.patch("urllib.request.urlopen", return_value=Response()) as urlopen:
            self.app._probe_account_service("alpha", timeout=1)

        request = urlopen.call_args.args[0]
        self.assertEqual(
            request.get_header("Authorization"),
            "Bearer " + internal_key,
        )
        self.assertNotIn(external_key, request.get_header("Authorization"))

    def test_update_cliproxy_image_restores_previous_image_on_failure(self):
        target_id = "sha256:" + "f" * 64
        old_id = "sha256:" + "1" * 64

        def docker_json(*args):
            if args == ("image", "inspect", CLIPROXY_IMAGE):
                return {"Id": target_id}
            if args == ("container", "inspect", "cliproxy-alpha"):
                return {
                    "Image": old_id,
                    "State": {"Running": True, "Status": "running"},
                }
            return None

        self.app._docker_json = mock.Mock(side_effect=docker_json)
        self.app._docker = mock.Mock()
        failure = subprocess.CalledProcessError(1, ["docker", "compose", "up"])
        self.app._compose_with_image = mock.Mock(side_effect=[failure, None])
        self.app._probe_account_service = mock.Mock()

        with self.assertRaises(subprocess.CalledProcessError):
            self.app.update_cliproxy_image("alpha")

        self.assertEqual(self.app._compose_with_image.call_count, 2)
        rollback_call = self.app._compose_with_image.call_args_list[1]
        self.assertEqual(rollback_call.args[0], "cliproxy-cpa-rollback:alpha")
        self.app._probe_account_service.assert_called_once_with("alpha")
        self.assertNotIn(
            "applied",
            self.app.store.read_runtime_state("cliproxy_image", {}),
        )

    def test_update_all_cliproxy_images_rolls_back_previously_updated_accounts(self):
        target_id = "sha256:" + "2" * 64
        old_images = {
            "cliproxy-alpha": "sha256:" + "3" * 64,
            "cliproxy-beta": "sha256:" + "4" * 64,
        }

        def docker_json(*args):
            if args == ("image", "inspect", CLIPROXY_IMAGE):
                return {"Id": target_id}
            if args[:2] == ("container", "inspect") and args[2] in old_images:
                return {
                    "Image": old_images[args[2]],
                    "State": {"Running": True, "Status": "running"},
                }
            return None

        self.app._docker_json = mock.Mock(side_effect=docker_json)
        self.app._docker = mock.Mock()
        failure = subprocess.CalledProcessError(1, ["docker", "compose", "up"])
        self.app._compose_with_image = mock.Mock(
            side_effect=[None, failure, None, None]
        )
        self.app._probe_account_service = mock.Mock()

        with self.assertRaises(subprocess.CalledProcessError):
            self.app.update_cliproxy_image("all")

        image_refs = [call.args[0] for call in self.app._compose_with_image.call_args_list]
        self.assertEqual(
            image_refs,
            [
                target_id,
                target_id,
                "cliproxy-cpa-rollback:beta",
                "cliproxy-cpa-rollback:alpha",
            ],
        )

    def test_update_cliproxy_image_rolls_back_when_compose_projection_commit_fails(self):
        target_id = "sha256:" + "5" * 64
        old_id = "sha256:" + "6" * 64

        def docker_json(*args):
            if args == ("image", "inspect", CLIPROXY_IMAGE):
                return {"Id": target_id}
            if args == ("container", "inspect", "cliproxy-alpha"):
                return {
                    "Image": old_id,
                    "State": {"Running": True, "Status": "running"},
                }
            return None

        self.app._docker_json = mock.Mock(side_effect=docker_json)
        self.app._docker = mock.Mock()
        self.app._compose_with_image = mock.Mock()
        self.app._probe_account_service = mock.Mock()
        self.app.render_compose_environment = mock.Mock(
            side_effect=OSError("compose env write failed")
        )

        with self.assertRaisesRegex(OSError, "compose env write failed"):
            self.app.update_cliproxy_image("alpha")

        self.assertEqual(self.app._compose_with_image.call_count, 2)
        self.assertEqual(
            self.app._compose_with_image.call_args_list[1].args[0],
            "cliproxy-cpa-rollback:alpha",
        )
        self.assertEqual(self.app._probe_account_service.call_count, 2)
        self.assertNotIn(
            "applied",
            self.app.store.read_runtime_state("cliproxy_image", {}),
        )

    def test_update_all_cliproxy_images_skips_disabled_accounts(self):
        records = self.app.store.read_accounts()
        for item in records:
            item["group_enabled"] = item["id"] == "alpha"
            item["default_group"] = item["id"] == "alpha"
        self.app.store.write_accounts(records)
        target_id = "sha256:" + "7" * 64
        old_id = "sha256:" + "8" * 64

        def docker_json(*args):
            if args == ("image", "inspect", CLIPROXY_IMAGE):
                return {"Id": target_id}
            if args == ("container", "inspect", "cliproxy-alpha"):
                return {
                    "Image": old_id,
                    "State": {"Running": True, "Status": "running"},
                }
            self.fail("不应检查停用账号：{}".format(args))

        self.app._docker_json = mock.Mock(side_effect=docker_json)
        self.app._docker = mock.Mock()
        self.app._compose_with_image = mock.Mock()
        self.app._probe_account_service = mock.Mock()

        with mock.patch("builtins.print") as printer:
            self.app.update_cliproxy_image("all")

        self.app._docker.assert_called_once_with(
            "image", "tag", old_id, "cliproxy-cpa-rollback:alpha"
        )
        self.app._compose_with_image.assert_called_once()
        self.app._probe_account_service.assert_called_once_with("alpha")
        for account in ("beta", "gamma", "delta"):
            printer.assert_any_call("跳过 {}：CPA 已停用".format(account), flush=True)

    def test_update_cliproxy_image_rejects_disabled_account(self):
        records = self.app.store.read_accounts()
        for item in records:
            item["group_enabled"] = item["id"] != "alpha"
            item["default_group"] = item["id"] == "beta"
        self.app.store.write_accounts(records)
        self.app._docker_json = mock.Mock()

        with self.assertRaisesRegex(ValueError, "CPA 账号已停用"):
            self.app.update_cliproxy_image("alpha")

        self.app._docker_json.assert_not_called()

    def test_update_cliproxy_image_does_not_start_stopped_account(self):
        self.app._docker_json = mock.Mock(
            side_effect=[
                {"Id": "sha256:" + "5" * 64},
                {
                    "Image": "sha256:" + "6" * 64,
                    "State": {"Running": False, "Status": "exited"},
                },
            ]
        )
        self.app._docker = mock.Mock()
        self.app._compose_with_image = mock.Mock()

        self.app.update_cliproxy_image("alpha")

        self.app._docker.assert_not_called()
        self.app._compose_with_image.assert_not_called()
        self.assertNotIn(
            "applied",
            self.app.store.read_runtime_state("cliproxy_image", {}),
        )

    def test_update_cliproxy_image_rejects_concurrent_runtime_operation(self):
        competing = self.module.ControlPlane(self.root)
        competing._docker_json = mock.Mock()

        with self.app.runtime_operation_lock("测试操作"):
            with self.assertRaisesRegex(ValueError, "另一个发布或运行时操作"):
                competing.update_cliproxy_image("alpha")

        competing._docker_json.assert_not_called()

    def test_sync_cliproxy_image_environment_updates_only_generated_projection(self):
        (self.root / ".env").write_text(
            "CUSTOM_VALUE=keep\n"
            "CLIPROXY_IMAGE=registry.example.com/cpa:old\n"
            "GATEWAY_PORT=19317\n",
            encoding="utf-8",
        )

        self.app.sync_cliproxy_image_environment("registry.example.com/cpa:new")

        bootstrap = (self.root / ".env").read_text(encoding="utf-8")
        projection = (self.root / "state" / "compose.env").read_text(encoding="utf-8")
        self.assertIn("CUSTOM_VALUE=keep", bootstrap)
        self.assertIn("registry.example.com/cpa:old", bootstrap)
        self.assertIn("CLIPROXY_IMAGE=registry.example.com/cpa:new", projection)
        self.assertNotIn("CUSTOM_VALUE=keep", projection)
        self.assertEqual((self.root / "state" / "compose.env").stat().st_mode & 0o777, 0o600)

    def test_create_user_rejects_non_company_email(self):
        with self.assertRaisesRegex(ValueError, "example.com"):
            self.app.create_user("alice@outside.test", apply=False)

    def test_create_user_is_atomic_when_identifier_exists(self):
        self.app.create_user("alice@example.com", apply=False)
        original = self.app._read_registry()

        with self.assertRaisesRegex(ValueError, "alpha"):
            self.app.create_user("alice@example.com", apply=False)

        self.assertEqual(self.app._read_registry(), original)

    def test_create_users_dedupes_skips_existing_and_avoids_container_restart(self):
        self.app.create_user("alice@example.com", apply=False)
        self.app.apply_changes = mock.Mock()

        result = self.app.create_users(
            [
                "alice@example.com",
                "Bob@Example.com",
                "bob@example.com",
                "not-an-email",
                "carol@example.com",
                "carol@example.com",
            ],
            apply=True,
            restart_containers=False,
        )

        self.assertEqual(result["requested"], 3)
        self.assertEqual(result["created"], 2)
        self.assertEqual(result["skipped_existing"], ["alice@example.com"])
        self.assertEqual(result["duplicates_in_input"], ["bob@example.com", "carol@example.com"])
        self.assertEqual(len(result["invalid"]), 1)
        self.assertEqual(
            {item["email"] for item in result["users"]},
            {"bob@example.com", "carol@example.com"},
        )
        self.assertTrue(all(item["account"] is None for item in result["users"]))
        routes = self.app._read_routes()
        self.assertNotIn("bob@example.com", routes)
        self.assertNotIn("carol@example.com", routes)
        active_users = {item["user"] for item in self.app.active_records()}
        self.assertEqual(
            active_users,
            {"alice@example.com", "bob@example.com", "carol@example.com"},
        )
        self.app.apply_changes.assert_called_once_with(restart_containers=False)

    def test_create_users_dry_run_does_not_write_registry(self):
        before = self.app._read_registry()
        result = self.app.create_users(
            ["alice@example.com", "alice@example.com"],
            dry_run=True,
        )
        self.assertEqual(result["created"], 1)
        self.assertTrue(result["dry_run"])
        self.assertEqual(self.app._read_registry(), before)

    def test_apply_changes_can_skip_container_recreate(self):
        self.app.render = mock.Mock()
        self.app.compose = mock.Mock()
        self.app._reload_gateway_if_running = mock.Mock()

        self.app.apply_changes(restart_containers=False)

        self.app.render.assert_called_once_with()
        self.app.compose.assert_called_once_with("config", "--quiet")
        self.app._reload_gateway_if_running.assert_called_once_with()

    def test_rotate_replaces_secret_and_revoke_removes_active_key(self):
        identifier = "alice@example.com:alpha"
        original = self.app.create_key(identifier, apply=False)["key"]
        rotated = self.app.rotate_key(identifier, apply=False)["key"]

        self.assertNotEqual(original, rotated)
        self.assertRegex(
            rotated,
            r"^cpa_alice_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$",
        )
        self.assertEqual(len(self.app.active_records()), 4)
        self.assertEqual({item["key"] for item in self.app.active_records()}, {rotated})
        self.assertFalse(self.app.auth_snapshot_path.exists())

        self.app.revoke_key(identifier, apply=False)
        self.assertEqual(self.app.active_records(), [])

    def test_rotate_key_rolls_registry_back_when_snapshot_publish_fails(self):
        identifier = "alice@example.com:alpha"
        self.app.create_key(identifier, apply=False)
        before = self.app._read_registry()
        self.app.publish_auth_snapshot = mock.Mock(
            side_effect=[
                RuntimeError("snapshot publish failed"),
                {"generation": "a" * 32, "records": 1},
            ]
        )

        with self.assertRaisesRegex(RuntimeError, "snapshot publish failed"):
            self.app.rotate_key(identifier)

        self.assertEqual(self.app._read_registry(), before)
        self.assertEqual(
            self.app.publish_auth_snapshot.call_args_list,
            [mock.call(wait=False), mock.call(wait=False)],
        )

    def test_rotate_legacy_keys_replaces_only_old_formats_in_one_batch(self):
        self.app.create_user("alice@example.com", apply=False)
        bob = self.app.create_user("bob@example.com", apply=False)
        current_bob_key = bob[0]["key"]
        records = self.app._read_registry()
        for item in records:
            if item["user"] == "alice@example.com":
                item["key"] = "cpa_alice_legacy012345"
        self.app._write_registry(records)
        routes_before = self.app._read_routes()

        result = self.app.rotate_legacy_keys(apply=False)

        self.assertEqual(
            result,
            {
                "users": 2,
                "rotated_users": 1,
                "unchanged_users": 1,
                "revoked_keys": 1,
                "issued_keys": 1,
                "dry_run": False,
            },
        )
        active = self.app.active_records()
        alice_keys = {item["key"] for item in active if item["user"] == "alice@example.com"}
        bob_keys = {item["key"] for item in active if item["user"] == "bob@example.com"}
        self.assertEqual(len(alice_keys), 1)
        self.assertNotIn("cpa_alice_legacy012345", alice_keys)
        self.assertRegex(
            next(iter(alice_keys)),
            r"^cpa_alice_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$",
        )
        self.assertEqual(bob_keys, {current_bob_key})
        self.assertEqual(self.app._read_routes(), routes_before)
        self.assertEqual(len([item for item in active if item["user"] == "alice@example.com"]), 4)
        self.assertEqual(len([item for item in active if item["user"] == "bob@example.com"]), 4)

    def test_rotate_legacy_keys_dry_run_and_failed_apply_do_not_change_registry(self):
        self.app.create_user("alice@example.com", apply=False)
        registry = self.app._read_registry()
        for item in registry:
            item["key"] = "legacy-alice"
        self.app._write_registry(registry)
        before = self.app._read_registry()

        dry_run = self.app.rotate_legacy_keys(dry_run=True)
        self.assertEqual(dry_run["rotated_users"], 1)
        self.assertEqual(self.app._read_registry(), before)

        with mock.patch.object(self.app, "apply_changes", side_effect=RuntimeError("apply failed")):
            with self.assertRaisesRegex(RuntimeError, "apply failed"):
                self.app.rotate_legacy_keys()
        self.assertEqual(self.app._read_registry(), before)

    def test_revoke_user_disables_all_active_account_keys_once(self):
        self.app.create_user("alice@example.com", apply=False)

        revoked = self.app.revoke_user("alice@example.com", apply=False)

        self.assertEqual(len(revoked), 4)
        self.assertEqual(self.app.active_records(), [])

    def test_failed_apply_rolls_registry_back(self):
        with mock.patch.object(self.app, "apply_changes", side_effect=RuntimeError("apply failed")):
            with self.assertRaisesRegex(RuntimeError, "apply failed"):
                self.app.create_key("alice@example.com:alpha")

        self.assertEqual(self.app.active_records(), [])

    def test_render_uses_internal_keys_and_auth_snapshot_routes_external_key(self):
        self.app.create_user("alice@example.com", apply=False)
        self.app.create_user("bob@example.com", apply=False)
        self.app.render()

        arch = (self.root / "configs" / "alpha.yaml").read_text()
        fusion = (self.root / "configs" / "beta.yaml").read_text()
        internal = self.app._read_internal_keys()

        self.assertEqual(arch.count("cpa_internal_"), 2)
        self.assertEqual(fusion.count("cpa_internal_"), 2)
        self.assertNotIn("cpa_alice_", arch)
        self.assertNotIn("cpa_bob_", fusion)
        self.assertIn(internal["alice@example.com"]["key"], arch)
        self.assertIn(internal["bob@example.com"]["key"], fusion)

        self.app.set_user_route("bob@example.com", "beta", apply=True)
        snapshot = json.loads(self.app.auth_snapshot_path.read_text(encoding="utf-8"))
        self.assertEqual(len(snapshot["records"]), 1)
        self.assertEqual(snapshot["records"][0]["user_email"], "bob@example.com")
        self.assertEqual(snapshot["records"][0]["account"], "beta")
        self.assertEqual(snapshot["records"][0]["backend"], "cliproxy-beta:8317")
        self.assertEqual(snapshot["records"][0]["internal_key"], internal["bob@example.com"]["key"])

    def test_render_uses_valid_empty_key_list(self):
        self.app.render()

        config = (self.root / "configs" / "alpha.yaml").read_text()

        self.assertIn("api-keys: []", config)
        self.assertIn("allow-remote: true", config)
        self.assertIn('secret-key: "test-management-key"', config)
        self.assertIn("disable-control-panel: false", config)
        self.assertIn("disable-auto-update-panel: true", config)
        self.assertIn("usage-statistics-enabled: true", config)
        self.assertIn('disable-image-generation: "chat"', config)
        self.assertIn("redis-usage-queue-retention-seconds: 3600", config)

    def test_configuration_center_defaults_are_versioned_and_private(self):
        configuration = self.app.configuration()

        self.assertEqual(configuration["version"], 1)
        self.assertEqual(configuration["values"]["cpa.request_retry"], 2)
        self.assertEqual(
            configuration["values"]["cpa.disable_image_generation"],
            "chat",
        )
        self.assertEqual(configuration["values"]["cpa.max_retry_interval"], 12)
        self.assertEqual(
            configuration["values"]["cpa.transient_error_cooldown_seconds"],
            10,
        )
        self.assertTrue(configuration["values"]["cpa.logging_to_file"])
        self.assertEqual(configuration["values"]["cpa.logs_max_total_size_mb"], 64)
        self.assertEqual(configuration["values"]["cpa.error_logs_max_files"], 10)
        self.assertEqual(configuration["values"]["collector.batch_size"], 100)
        self.assertEqual(
            configuration["values"]["runtime.cliproxy_image"],
            CLIPROXY_IMAGE,
        )
        self.assertEqual(
            configuration["values"]["runtime.admin_base_image"],
            self.module.DEFAULT_ADMIN_BASE_IMAGE,
        )
        self.assertEqual(
            configuration["values"]["runtime.gateway_image"],
            self.module.DEFAULT_GATEWAY_BASE_IMAGE,
        )
        self.assertFalse(configuration["values"]["notification.enabled"])
        self.assertEqual(configuration["values"]["notification.timezone"], "UTC")
        self.assertEqual(configuration["values"]["user_quota.timezone"], "UTC")

        self.assertEqual(
            configuration["values"]["notification.daily_times"],
            "09:00,14:00,18:00",
        )
        self.assertEqual(configuration["values"]["notification.weekly_threshold_percent"], 90.0)
        self.assertEqual(configuration["values"]["usage.quota_cache_seconds"], 60)
        self.assertEqual(configuration["values"]["account_failover.mode"], "active")
        self.assertEqual(configuration["values"]["account_failover.poll_seconds"], 60)
        self.assertEqual(configuration["values"]["account_failover.reserve_percent"], 5.0)
        self.assertEqual(configuration["values"]["account_failover.stale_after_seconds"], 120)
        self.assertIsNone(configuration["values"]["user_quota.default_weekly_tokens"])
        self.assertEqual(configuration["values"]["user_quota.fail_open_after_seconds"], 300)
        for effort, unused_label, expected in self.module.REASONING_EFFORT_MULTIPLIER_DEFAULTS:
            self.assertEqual(
                configuration["values"][
                    "user_quota.reasoning_multiplier.{}".format(effort)
                ],
                expected,
            )
        for effort, unused_label, expected in self.module.REASONING_EFFORT_COLOR_DEFAULTS:
            self.assertEqual(
                configuration["values"][
                    "admin.account_usage.reasoning_effort_color.{}".format(effort)
                ],
                expected,
            )
        self.assertEqual(self.app.store.path.stat().st_mode & 0o777, 0o600)
        self.assertFalse((self.root / "state/configuration.json").exists())

        self.app.update_configuration({"account_failover.mode": "observe"})
        self.assertEqual(
            self.app.configuration()["values"]["account_failover.mode"],
            "observe",
        )

    def test_configuration_removes_only_retired_gost_settings(self):
        stored = self.app.store.read_settings()
        self.app.store.write_settings(
            {
                **stored,
                "gost.enabled": True,
                "gost.remote_host": "relay.example.com",
                "runtime.gost_image": "example.invalid/gost:retired",
            }
        )

        configuration = self.app.configuration()

        for key in ("gost.enabled", "gost.remote_host", "runtime.gost_image"):
            self.assertNotIn(key, configuration["values"])
            self.assertNotIn(key, self.app.store.read_settings())

    def test_configuration_rejects_unrecognized_stored_settings(self):
        stored = self.app.store.read_settings()
        self.app.store.write_settings({**stored, "retired.relay_host": "unexpected"})

        with self.assertRaisesRegex(ValueError, "未知参数"):
            self.app.configuration()

    def test_quota_heartbeat_preserves_last_success_during_short_outage(self):
        healthy = self.app.publish_quota_heartbeat(
            ok=True,
            now=100,
            fail_open_after_seconds=300,
        )
        degraded = self.app.publish_quota_heartbeat(
            ok=False,
            error="collector unavailable",
            now=120,
            fail_open_after_seconds=300,
        )

        self.assertEqual(healthy["last_success_at"], 100)
        self.assertEqual(degraded["last_success_at"], 100)
        self.assertEqual(degraded["updated_at"], 120)
        self.assertFalse(degraded["ok"])
        self.assertEqual(degraded["fail_open_after_seconds"], 300)
        self.assertEqual(self.app.snapshot_dir.stat().st_mode & 0o777, 0o750)
        self.assertEqual(self.app.quota_heartbeat_path.stat().st_mode & 0o777, 0o640)
        if os.geteuid() == 0:
            self.assertEqual(self.app.quota_heartbeat_path.stat().st_gid, 65534)

    def test_quota_snapshot_publishes_weighted_gate_and_raw_audit_totals(self):
        result = self.app.publish_quota_snapshot(
            {
                "alice@example.com": {
                    "week_start_at": 100,
                    "week_end_at": 200,
                    "limit_tokens": 1_000,
                    "used_tokens": 240,
                    "raw_used_tokens": 120,
                    "weighted_raw_used_tokens": 240,
                }
            },
            generated_at=150,
        )

        self.assertTrue(result["changed"])
        payload = json.loads(
            self.app.quota_snapshot_path.read_text(encoding="utf-8")
        )
        record = payload["records"][0]
        self.assertEqual(record["used_tokens"], 240)
        self.assertEqual(record["raw_used_tokens"], 120)
        self.assertEqual(record["weighted_raw_used_tokens"], 240)
        self.assertEqual(record["quota_unit"], "weighted_tokens")

    def test_configuration_updates_rendered_cpa_and_future_account_port(self):
        result = self.app.update_configuration(
            {
                "cpa.proxy_url": "",
                "cpa.debug": True,
                "cpa.logging_to_file": True,
                "cpa.logs_max_total_size_mb": 96,
                "cpa.error_logs_max_files": 12,
                "cpa.request_retry": 4,
                "cpa.disable_image_generation": "true",
                "cpa.max_retry_interval": 15,
                "cpa.transient_error_cooldown_seconds": 8,
                "cpa.session_affinity": False,
                "cpa.session_affinity_ttl": "30m",
                "accounts.port_start": 19000,
                "accounts.port_end": 19010,
            }
        )
        self.app.render()
        created = self.app.add_account(
            "gamma-new2",
            "gamma+new2@accounts.example.com",
            apply=False,
        )

        self.assertIn("cpa.debug", result["changed"])
        config = (self.root / "configs" / "alpha.yaml").read_text(encoding="utf-8")
        self.assertIn("debug: true", config)
        self.assertIn("logging-to-file: true", config)
        self.assertIn("logs-max-total-size-mb: 96", config)
        self.assertIn("error-logs-max-files: 12", config)
        self.assertIn('proxy-url: "direct"', config)
        self.assertIn("request-retry: 4", config)
        self.assertIn("disable-image-generation: true", config)
        self.assertIn("max-retry-interval: 15", config)
        self.assertIn("transient-error-cooldown-seconds: 8", config)
        self.assertIn("session-affinity: false", config)
        self.assertIn('session-affinity-ttl: "30m"', config)
        self.assertEqual(created["port"], 19000)

    def test_configuration_rejects_unknown_invalid_and_colliding_values(self):
        with self.assertRaisesRegex(ValueError, "不支持"):
            self.app.update_configuration({"unknown.option": 1})
        with self.assertRaisesRegex(ValueError, "HTTP"):
            self.app.update_configuration({"cpa.proxy_url": "ftp://example.com"})
        proxy = self.app.update_configuration(
            {"cpa.proxy_url": "http://user:secret@example.com:8080"}
        )
        self.assertEqual(
            proxy["values"]["cpa.proxy_url"],
            "http://user:secret@example.com:8080",
        )
        self.assertEqual(
            self.app.redacted_configuration()["values"]["cpa.proxy_url"], "***"
        )
        with self.assertRaisesRegex(ValueError, "整数"):
            self.app.update_configuration({"cpa.request_retry": 2.5})
        with self.assertRaisesRegex(ValueError, "chat"):
            self.app.update_configuration(
                {"cpa.disable_image_generation": "unsupported"}
            )
        with self.assertRaisesRegex(ValueError, "1 至 300"):
            self.app.update_configuration({"cpa.max_retry_interval": 0})
        with self.assertRaisesRegex(ValueError, "1 至 300"):
            self.app.update_configuration(
                {"cpa.transient_error_cooldown_seconds": 301}
            )
        with self.assertRaisesRegex(ValueError, "起点"):
            self.app.update_configuration(
                {"accounts.port_start": 19010, "accounts.port_end": 19000}
            )
        with self.assertRaisesRegex(ValueError, "网关端口"):
            self.app.update_configuration(
                {
                    "accounts.port_start": 18000,
                    "accounts.port_end": 19000,
                }
            )
        with self.assertRaisesRegex(ValueError, "IANA"):
            self.app.update_configuration({"notification.timezone": "Mars/Olympus"})
        with self.assertRaisesRegex(ValueError, "HH:MM"):
            self.app.update_configuration({"notification.daily_times": "9点,14:00"})
        with self.assertRaisesRegex(ValueError, "无效时间"):
            self.app.update_configuration({"notification.daily_times": "24:00"})
        with self.assertRaisesRegex(ValueError, "1 至 100"):
            self.app.update_configuration({"notification.weekly_threshold_percent": 0})
        for key in ("runtime.admin_base_image", "runtime.gateway_image"):
            with self.subTest(image_key=key), self.assertRaisesRegex(
                ValueError, "name:tag@sha256:digest"
            ):
                self.app.update_configuration(
                    {key: "registry.example.com/component:v2"}
                )
        with self.assertRaisesRegex(ValueError, "整数"):
            self.app.update_configuration({"user_quota.default_weekly_tokens": 1.5})
        for value in (0, -1, float("inf"), float("nan"), 10.1):
            with self.subTest(reasoning_multiplier=value):
                with self.assertRaisesRegex(ValueError, "有限数字|0.1 至 10.0"):
                    self.app.update_configuration(
                        {"user_quota.reasoning_multiplier.max": value}
                    )
        for value in ("23784d", "#23784", "#23784zz", "red", ""):
            with self.subTest(reasoning_color=value):
                with self.assertRaisesRegex(ValueError, "#RRGGBB"):
                    self.app.update_configuration(
                        {"admin.account_usage.reasoning_effort_color.xhigh": value}
                    )
        with self.assertRaisesRegex(ValueError, "失效时间"):
            self.app.update_configuration(
                {
                    "account_failover.poll_seconds": 300,
                    "account_failover.stale_after_seconds": 120,
                }
            )

        result = self.app.update_configuration(
            {"user_quota.default_weekly_tokens": "1000000"}
        )
        self.assertEqual(result["values"]["user_quota.default_weekly_tokens"], 1000000)
        result = self.app.update_configuration(
            {
                "user_quota.reasoning_multiplier.max": "2.5",
                "user_quota.reasoning_multiplier.high": 1.25,
            }
        )
        self.assertEqual(
            result["values"]["user_quota.reasoning_multiplier.max"],
            2.5,
        )
        self.assertEqual(
            result["values"]["user_quota.reasoning_multiplier.high"],
            1.25,
        )
        result = self.app.update_configuration(
            {"admin.account_usage.reasoning_effort_color.xhigh": "#1A2B3C"}
        )
        self.assertEqual(
            result["values"]["admin.account_usage.reasoning_effort_color.xhigh"],
            "#1a2b3c",
        )
        result = self.app.update_configuration(
            {"user_quota.default_weekly_tokens": ""}
        )
        self.assertIsNone(result["values"]["user_quota.default_weekly_tokens"])

    def test_notification_times_are_normalized_sorted_and_deduplicated(self):
        result = self.app.update_configuration(
            {"notification.daily_times": "18:00，9:00 14:00,09:00"}
        )

        self.assertEqual(
            result["values"]["notification.daily_times"],
            "09:00,14:00,18:00",
        )

    def test_record_deployment_persists_release_state_and_regenerates_compose_env(self):
        result = self.module.main(
            [
                "--root",
                str(self.root),
                "record-deployment",
                "--version",
                "v1.2.3",
                "--commit",
                "abc123",
                "--pipeline",
                "manual-1",
                "--deployed-at",
                "2026-08-19T01:02:03Z",
                "--metadata-image",
                "registry.example.com/codex-cpa-release:latest",
                "--admin-image",
                "registry.example.com/codex-cpa-admin:v1.2.3",
                "--web-image",
                "registry.example.com/codex-cpa-web:v1.2.3",
                "--gateway-image",
                "registry.example.com/codex-cpa-gateway:v1.2.3",
                "--edge-image",
                "registry.example.com/codex-cpa-edge:v1.2.3",
                "--gateway-port",
                "19317",
                "--gateway-internal-port",
                "19316",
            ]
        )

        self.assertEqual(result, 0)
        restarted = self.module.ControlPlane(self.root)
        deployment_state = restarted.store.read_runtime_state("deployment")
        self.assertNotIn("pending", deployment_state)
        deployment = deployment_state["applied"]
        self.assertEqual(deployment["version"], "v1.2.3")
        self.assertEqual(deployment["admin_image"], "registry.example.com/codex-cpa-admin:v1.2.3")
        values = restarted.configuration()["values"]
        self.assertEqual(values["delivery.release_metadata_image"], "registry.example.com/codex-cpa-release:latest")
        self.assertEqual(values["gateway.port"], 19317)
        projection = (self.root / "state" / "compose.env").read_text(encoding="utf-8")
        self.assertNotIn("RELEASE_VERSION=", projection)
        self.assertIn("ADMIN_IMAGE=registry.example.com/codex-cpa-admin:v1.2.3", projection)
        self.assertIn("GATEWAY_PORT=19317", projection)

    def test_staged_deployment_drives_compose_without_changing_applied_release(self):
        self.app.store.write_runtime_state(
            "deployment",
            {"applied": {"version": "v1.1.0", "admin_image": "admin:old"}},
        )
        args = [
            "--root", str(self.root), "stage-deployment",
            "--version", "v1.2.0", "--commit", "abc123",
            "--pipeline", "manual-1", "--deployed-at", "2026-08-19T01:02:03Z",
            "--metadata-image", "registry.example.com/codex-cpa-release:latest",
            "--admin-image", "registry.example.com/codex-cpa-admin:v1.2.0",
            "--web-image", "registry.example.com/codex-cpa-web:v1.2.0",
            "--gateway-image", "registry.example.com/codex-cpa-gateway:v1.2.0",
            "--edge-image", "registry.example.com/codex-cpa-edge:v1.2.0",
            "--gateway-port", "18317", "--gateway-internal-port", "18316",
        ]

        self.assertEqual(self.module.main(args), 0)

        restarted = self.module.ControlPlane(self.root)
        state = restarted.deployment_runtime_state()
        self.assertEqual(state["applied"]["version"], "v1.1.0")
        self.assertEqual(state["pending"]["version"], "v1.2.0")
        projection = (self.root / "state" / "compose.env").read_text(encoding="utf-8")
        self.assertIn("ADMIN_IMAGE=registry.example.com/codex-cpa-admin:v1.2.0", projection)

        args[2] = "record-deployment"
        self.assertEqual(self.module.main(args), 0)
        state = self.module.ControlPlane(self.root).deployment_runtime_state()
        self.assertNotIn("pending", state)
        self.assertEqual(state["applied"]["version"], "v1.2.0")

    def test_legacy_env_is_imported_then_split_into_bootstrap_and_generated_projection(self):
        (self.root / ".env").write_text(
            "DEPLOY_ROOT={}\nCUSTOM_VALUE=keep\nGATEWAY_PORT=18315\n".format(
                self.root
            ),
            encoding="utf-8",
        )
        migration = self.app.migrate_legacy_environment()
        self.assertEqual(self.app.configuration()["values"]["gateway.port"], 18315)
        result = self.app.update_configuration(
            {
                "runtime.cliproxy_image": "registry.example.com/cpa:v2",
                "runtime.admin_base_image": (
                    "registry.example.com/admin-base:v2@sha256:" + "c" * 64
                ),
                "gateway.listen_address": "127.0.0.1",
            }
        )
        self.app.sync_environment_configuration(result["values"])

        bootstrap = (self.root / ".env").read_text(encoding="utf-8")
        projection = (self.root / "state" / "compose.env").read_text(encoding="utf-8")
        backup = Path(migration["backup_path"]).read_text(encoding="utf-8")
        self.assertIn("DEPLOY_ROOT={}".format(self.root), bootstrap)
        self.assertNotIn("CUSTOM_VALUE", bootstrap)
        self.assertNotIn("GATEWAY_PORT", bootstrap)
        self.assertIn("CUSTOM_VALUE=keep", backup)
        self.assertIn("CLIPROXY_IMAGE=registry.example.com/cpa:v2", projection)
        self.assertIn(
            "ADMIN_BASE_IMAGE=registry.example.com/admin-base:v2@sha256:" + "c" * 64,
            projection,
        )
        self.assertIn("GATEWAY_PORT=18315", projection)
        self.assertIn("GATEWAY_LISTEN_ADDRESS=127.0.0.1", projection)
        self.assertIn("MANAGEMENT_LISTEN_ADDRESS=127.0.0.1", projection)
        self.assertIn("MANAGEMENT_PORT=18318", projection)
        self.assertIn("BUSINESS_CPA_LISTEN_ADDRESS=127.0.0.1", projection)
        self.assertIn("GATEWAY_INTERNAL_PORT=18316", projection)
        self.assertEqual((self.root / ".env").stat().st_mode & 0o777, 0o600)
        self.assertEqual((self.root / "state" / "compose.env").stat().st_mode & 0o777, 0o600)
        migration_state = self.app.store.read_runtime_state(
            "legacy_environment_migration"
        )
        self.assertEqual(migration_state["unmapped_keys"], ["CUSTOM_VALUE"])

    def test_legacy_env_migration_canonicalizes_public_listeners(self):
        (self.root / ".env").write_text(
            "DEPLOY_ROOT={}\nGATEWAY_LISTEN_ADDRESS=0.0.0.0\n".format(
                self.root
            ),
            encoding="utf-8",
        )

        self.app.migrate_legacy_environment()

        self.assertEqual(
            self.app.configuration()["values"]["gateway.listen_address"],
            "127.0.0.1",
        )

    def test_legacy_env_migration_rejects_invalid_applied_release_identity(self):
        legacy = "DEPLOY_ROOT={}\nRELEASE_VERSION=not-a-version\n".format(
            self.root
        )
        (self.root / ".env").write_text(legacy, encoding="utf-8")

        with self.assertRaisesRegex(ValueError, "RELEASE_VERSION"):
            self.app.migrate_legacy_environment()

        self.assertEqual((self.root / ".env").read_text(encoding="utf-8"), legacy)

    def test_legacy_env_migration_ignores_stale_release_fields_already_in_sqlite(self):
        self.app.store.write_runtime_state(
            "deployment",
            {
                "applied": {
                    "version": "v1.2.3",
                    "admin_image": "registry.example.com/admin:v1.2.3",
                }
            },
        )
        (self.root / ".env").write_text(
            "DEPLOY_ROOT={}\nRELEASE_VERSION=stale-invalid-value\n"
            "ADMIN_IMAGE=invalid image; command\n".format(self.root),
            encoding="utf-8",
        )

        result = self.app.migrate_legacy_environment()

        self.assertEqual(result["deployment_fields"], [])
        self.assertEqual(result["unmapped_keys"], [])
        self.assertEqual(self.app.applied_deployment()["version"], "v1.2.3")

    def test_environment_backed_url_rejects_whitespace_and_invalid_port(self):
        for value in (
            "https://example.com/path\nINJECTED=true",
            "https://example.com:invalid/path",
        ):
            with self.subTest(value=value), self.assertRaises(ValueError):
                self.app.update_configuration({"cpa.proxy_url": value})

    def test_apply_changes_recreates_business_containers_for_atomic_configs(self):
        with mock.patch.object(self.app, "compose") as compose, mock.patch.object(
            self.app, "_reload_gateway_if_running"
        ) as reload_gateway:
            self.app.apply_changes()

        compose.assert_any_call("config", "--quiet")
        compose.assert_any_call(
            "up", "-d", "--force-recreate", *self.app.services().values()
        )
        reload_gateway.assert_called_once_with()

    def test_add_account_persists_and_renders_dynamic_runtime_files(self):
        created = self.app.add_account(
            "gamma-new2",
            "gamma+new2@accounts.example.com",
            group_name="ignored display name",
            apply=False,
        )
        self.app.render()

        self.assertEqual(created["port"], 18323)
        self.assertEqual(created["group_name"], "gamma-new2")
        self.assertEqual(created["created_keys"], 0)
        self.assertNotIn("keys", created)
        self.assertEqual(self.app.accounts()["gamma-new2"]["email"], "gamma+new2@accounts.example.com")
        self.assertTrue((self.root / "auth" / "gamma-new2").is_dir())
        self.assertTrue((self.root / "configs" / "gamma-new2.yaml").is_file())
        compose = (self.root / "compose.accounts.yml").read_text(encoding="utf-8")
        account_map = self.app.gateway_accounts_map_path.read_text(encoding="utf-8")
        backend_map = self.app.gateway_backends_map_path.read_text(encoding="utf-8")
        public = (self.root / "state" / "public" / "accounts.json").read_text(encoding="utf-8")
        self.assertIn("cliproxy-gamma-new2", compose)
        self.assertIn("18323:8317", compose)
        self.assertIn('max-size: "20m"', compose)
        self.assertIn('max-file: "3"', compose)
        self.assertIn("~:gamma-new2$ gamma-new2;", account_map)
        self.assertIn("gamma-new2 cliproxy-gamma-new2:8317;", backend_map)
        self.assertIn('"id": "gamma-new2"', public)
        self.assertNotIn("gamma+new2@accounts.example.com", public)
        self.assertNotIn('"email"', public)
        self.assertIn("${BUSINESS_CPA_LISTEN_ADDRESS:?state/compose.env missing}:18323:8317", compose)

    def test_proxy_resolution_prefers_account_then_default_and_supports_direct(self):
        self.app.update_configuration(
            {
                "cpa.proxy_url": "https://global-user:global-pass@proxy.example.com:8443",
                "cpa.proxy_enabled": True,
            }
        )
        self.app.update_account(
            "alpha",
            "alpha@accounts.example.com",
            proxy_mode="custom",
            proxy_url="socks5://account-user:account-pass@account-proxy.example.com:1080",
            apply=False,
        )
        self.app.update_account(
            "beta", "beta@accounts.example.com", proxy_mode="direct", apply=False
        )
        self.app.render()

        alpha = (self.root / "configs/alpha.yaml").read_text(encoding="utf-8")
        beta = (self.root / "configs/beta.yaml").read_text(encoding="utf-8")
        gamma = (self.root / "configs/gamma.yaml").read_text(encoding="utf-8")
        self.assertIn(
            'proxy-url: "socks5://account-user:account-pass@account-proxy.example.com:1080"',
            alpha,
        )
        self.assertIn('proxy-url: "direct"', beta)
        self.assertIn(
            'proxy-url: "https://global-user:global-pass@proxy.example.com:8443"',
            gamma,
        )
        database = self.app.store.path.read_bytes()
        self.assertNotIn(b"global-pass", database)
        self.assertNotIn(b"account-pass", database)

    def test_custom_proxy_requires_url_and_proxy_change_recreates_only_target_cpa(self):
        with self.assertRaisesRegex(ValueError, "必须配置 CPA 代理 URL"):
            self.app.update_account(
                "alpha", "alpha@accounts.example.com", proxy_mode="custom", apply=False
            )

        with mock.patch.object(self.app, "compose") as compose, mock.patch.object(
            self.app, "_reload_gateway_if_running"
        ):
            self.app.update_account(
                "alpha",
                "alpha@accounts.example.com",
                proxy_mode="custom",
                proxy_url="http://proxy.example.com:8080",
            )
        compose.assert_any_call(
            "up", "-d", "--no-deps", "--force-recreate", "cliproxy-alpha"
        )
        self.assertFalse(
            any("cliproxy-beta" in call.args for call in compose.call_args_list)
        )

    def test_default_proxy_apply_recreates_only_inheriting_accounts(self):
        self.app.update_account(
            "alpha", "alpha@accounts.example.com", proxy_mode="direct", apply=False
        )
        self.app.update_account(
            "beta",
            "beta@accounts.example.com",
            proxy_mode="custom",
            proxy_url="http://proxy.example.com:8080",
            apply=False,
        )
        with mock.patch.object(self.app, "compose") as compose, mock.patch.object(
            self.app, "_reload_gateway_if_running"
        ):
            self.app.apply_default_proxy_change()

        compose.assert_any_call(
            "up",
            "-d",
            "--no-deps",
            "--force-recreate",
            "cliproxy-gamma",
            "cliproxy-delta",
        )
        self.assertFalse(
            any(
                "cliproxy-alpha" in call.args or "cliproxy-beta" in call.args
                for call in compose.call_args_list
            )
        )

    def test_add_account_creates_keys_for_existing_users_without_returning_secrets(self):
        self.app.create_user("alice@example.com", apply=False)
        self.app.create_user("bob@example.com", apply=False)

        created = self.app.add_account(
            "gamma-new2", "gamma+new2@accounts.example.com", apply=False
        )

        self.assertEqual(created["created_keys"], 2)
        self.assertNotIn("keys", created)
        records = [
            item
            for item in self.app.active_records()
            if item["account"] == "gamma-new2"
        ]
        self.assertEqual(
            [item["label"] for item in records],
            [
                "alice@example.com:gamma-new2",
                "bob@example.com:gamma-new2",
            ],
        )
        self.assertEqual(records[0]["key"], self.app.user_active_key("alice@example.com"))
        self.assertEqual(records[1]["key"], self.app.user_active_key("bob@example.com"))
        self.assertFalse(self.app.issued_path.exists())

    def test_add_account_rejects_duplicate_email_and_reserved_identifier(self):
        with self.assertRaisesRegex(ValueError, "邮箱已存在"):
            self.app.add_account("another", "alpha@accounts.example.com", apply=False)
        with self.assertRaisesRegex(ValueError, "保留名称"):
            self.app.add_account("management", "new@example.com", apply=False)

    def test_add_account_failure_leaves_registry_and_audit_unchanged(self):
        self.app.create_user("alice@example.com", apply=False)
        original_keys = self.app._read_registry()
        with mock.patch.object(
            self.app,
            "compose",
            side_effect=RuntimeError("compose failed"),
        ):
            with self.assertRaisesRegex(RuntimeError, "compose failed"):
                self.app.add_account("gamma-new2", "gamma+new2@accounts.example.com")

        self.assertNotIn("gamma-new2", self.app.accounts())
        self.assertEqual(self.app._read_registry(), original_keys)
        self.assertFalse(self.app.issued_path.exists())
        self.assertFalse((self.root / "configs" / "gamma-new2.yaml").exists())
        self.assertFalse((self.root / "auth" / "gamma-new2").exists())
        self.assertFalse((self.root / "logs" / "gamma-new2").exists())
        public = (self.root / "state" / "public" / "accounts.json").read_text(encoding="utf-8")
        self.assertNotIn("gamma-new2", public)

    def test_update_account_changes_metadata_and_historical_key_records(self):
        self.app.create_key("alice@example.com:alpha", apply=False)

        updated = self.app.update_account("alpha", "renamed@example.com", apply=False)
        self.app.render()

        self.assertEqual(updated["email"], "renamed@example.com")
        self.assertEqual(self.app.accounts()["alpha"]["email"], "renamed@example.com")
        self.assertEqual(self.app._read_registry()[0]["account_email"], "renamed@example.com")
        self.assertIn("renamed@example.com", (self.root / "configs" / "alpha.yaml").read_text())

    def test_update_account_renames_runtime_identity_and_preserves_auth_and_keys(self):
        created = self.app.create_key("alice@example.com:alpha", apply=False)
        auth_file = self.root / "auth" / "alpha" / "oauth.json"
        auth_file.write_text('{"token":"secret"}', encoding="utf-8")
        self.app.render()

        updated = self.app.update_account(
            "alpha",
            "renamed@example.com",
            new_account_id="alpha-renamed",
            apply=False,
        )

        self.assertEqual(updated["id"], "alpha-renamed")
        self.assertEqual(updated["renamed_from"], "alpha")
        self.assertNotIn("alpha", self.app.accounts())
        self.assertEqual(self.app.accounts()["alpha-renamed"]["port"], 18319)
        self.assertFalse((self.root / "auth" / "alpha").exists())
        self.assertEqual(
            (self.root / "auth" / "alpha-renamed" / "oauth.json").read_text(),
            '{"token":"secret"}',
        )
        key = self.app._read_registry()[0]
        self.assertEqual(key["key"], created["key"])
        self.assertEqual(key["account"], "alpha-renamed")
        self.assertEqual(key["label"], "alice@example.com:alpha-renamed")
        self.assertEqual(key["account_email"], "renamed@example.com")
        self.assertTrue((self.root / "configs" / "alpha-renamed.yaml").is_file())
        self.assertFalse((self.root / "configs" / "alpha.yaml").exists())
        self.assertIn(
            "alpha-renamed cliproxy-alpha-renamed:8317;",
            self.app.gateway_backends_map_path.read_text(),
        )
        self.assertTrue((self.root / updated["backup"] / "auth" / "oauth.json").is_file())

    def test_update_account_rename_rejects_existing_id_and_rolls_back_runtime_failure(self):
        with self.assertRaisesRegex(ValueError, "已存在"):
            self.app.update_account(
                "alpha",
                "alpha@accounts.example.com",
                new_account_id="gamma",
                apply=False,
            )

        auth_file = self.root / "auth" / "alpha" / "oauth.json"
        auth_file.write_text("{}", encoding="utf-8")
        self.app.render()

        def compose_side_effect(*args, **kwargs):
            if args[:3] == ("up", "-d", "cliproxy-alpha-renamed"):
                raise RuntimeError("start failed")
            return mock.Mock(stdout="")

        with mock.patch.object(self.app, "compose", side_effect=compose_side_effect):
            with self.assertRaisesRegex(RuntimeError, "start failed"):
                self.app.update_account(
                    "alpha",
                    "renamed@example.com",
                    new_account_id="alpha-renamed",
                )

        self.assertIn("alpha", self.app.accounts())
        self.assertNotIn("alpha-renamed", self.app.accounts())
        self.assertTrue(auth_file.is_file())
        self.assertFalse((self.root / "auth" / "alpha-renamed").exists())
        self.assertTrue((self.root / "configs" / "alpha.yaml").is_file())
        self.assertFalse((self.root / "configs" / "alpha-renamed.yaml").exists())

    def test_update_account_rejects_duplicate_email_and_rolls_back_apply_failure(self):
        with self.assertRaisesRegex(ValueError, "邮箱已存在"):
            self.app.update_account("alpha", "beta@accounts.example.com", apply=False)
        with mock.patch.object(self.app, "apply_changes", side_effect=RuntimeError("apply failed")):
            with self.assertRaisesRegex(RuntimeError, "apply failed"):
                self.app.update_account("alpha", "new@example.com")
        self.assertEqual(self.app.accounts()["alpha"]["email"], "alpha@accounts.example.com")

    def test_account_policy_uses_account_id_sets_default_and_reroutes_before_disable(self):
        self.app.create_user("alice@example.com", apply=False)
        self.app.set_user_route("alice@example.com", "gamma", apply=False)

        result = self.app.update_account_policy(
            "gamma",
            "Plus Primary",
            False,
            fallback_account="beta",
            apply=False,
        )

        self.assertEqual(result["group_name"], "gamma")
        self.assertFalse(result["group_enabled"])
        self.assertEqual(result["rerouted_users"], 1)
        self.assertEqual(
            self.app.user_route("alice@example.com"),
            "beta",
        )
        enabled = self.app.update_account_policy(
            "gamma",
            "Plus Primary",
            True,
            default_group=True,
            apply=False,
        )
        self.assertTrue(enabled["default_group"])
        self.assertEqual(self.app.default_group(), "gamma")

    def test_account_policy_requires_fallback_for_current_routes(self):
        self.app.create_user("alice@example.com", apply=False)
        self.app.set_user_route("alice@example.com", "alpha", apply=False)

        with self.assertRaisesRegex(ValueError, "备用 CPA"):
            self.app.update_account_policy(
                "alpha",
                "Arch 组",
                False,
                apply=False,
            )

        result = self.app.update_account_policy(
            "alpha",
            "Arch 组",
            False,
            fallback_account="delta",
            apply=False,
        )
        self.assertEqual(result["rerouted_users"], 1)
        self.assertEqual(self.app.default_group(), "delta")

    def test_account_update_applies_metadata_policy_and_route_atomically(self):
        self.app.create_user("alice@example.com", apply=False)
        self.app.set_user_route("alice@example.com", "gamma", apply=False)

        result = self.app.update_account(
            "gamma",
            "gamma@accounts.example.com",
            group_name="Plus Primary",
            group_enabled=False,
            default_group=False,
            fallback_account="beta",
            apply=False,
        )

        self.assertEqual(result["group_name"], "gamma")
        self.assertFalse(result["group_enabled"])
        self.assertEqual(result["rerouted_users"], 1)
        self.assertEqual(self.app.user_route("alice@example.com"), "beta")

    def test_clear_account_auth_archives_files_and_restores_them_on_restart_failure(self):
        auth_file = self.root / "auth" / "alpha" / "oauth.json"
        auth_file.write_text('{"token":"secret"}', encoding="utf-8")

        with mock.patch.object(self.app, "compose", side_effect=RuntimeError("restart failed")):
            with self.assertRaisesRegex(RuntimeError, "restart failed"):
                self.app.clear_account_auth("alpha")

        self.assertTrue(auth_file.is_file())
        backups = list((self.root / "backups" / "accounts").iterdir())
        self.assertEqual(len(backups), 1)
        self.assertTrue((backups[0] / "auth" / "oauth.json").is_file())

    def test_clear_account_auth_removes_live_files_and_keeps_secure_backup(self):
        auth_file = self.root / "auth" / "alpha" / "oauth.json"
        auth_file.write_text("{}", encoding="utf-8")

        result = self.app.clear_account_auth("alpha", apply=False)

        self.assertFalse(auth_file.exists())
        backup = self.root / result["backup"]
        self.assertTrue((backup / "auth" / "oauth.json").is_file())
        self.assertEqual(backup.stat().st_mode & 0o777, 0o700)

    def test_delete_account_requires_explicit_key_revoke_and_removes_runtime_state(self):
        exclusive = self.app._new_record("alice@example.com:alpha")
        self.app._write_registry([exclusive])
        self.app.render()
        with self.assertRaisesRegex(ValueError, "有效 Key"):
            self.app.delete_account("alpha", apply=False)

        result = self.app.delete_account("alpha", revoke_keys=True, apply=False)

        self.assertNotIn("alpha", self.app.accounts())
        self.assertFalse(any(item["account"] == "alpha" for item in self.app._read_registry()))
        self.assertNotIn("cliproxy-alpha", self.app.accounts_compose_path.read_text())
        self.assertNotIn('"id": "alpha"', self.app.public_accounts_path.read_text())
        self.assertFalse((self.root / "auth" / "alpha").exists())
        self.assertTrue((self.root / result["backup"] / "keys.json").is_file())

    def test_delete_account_keeps_unified_user_key_without_revoke_confirmation(self):
        created = self.app.create_user("alice@example.com", apply=False)
        self.app.set_user_route("alice@example.com", "alpha", apply=False)
        key = created[0]["key"]

        result = self.app.delete_account(
            "alpha",
            fallback_account="delta",
            apply=False,
        )

        active = self.app.active_records()
        self.assertEqual(len(active), 3)
        self.assertEqual({item["key"] for item in active}, {key})
        self.assertNotIn("alpha", {item["account"] for item in active})
        self.assertEqual(result["replacement_account"], "delta")
        self.assertEqual(result["rerouted_users"], 1)
        self.assertEqual(self.app.default_group(), "delta")

    def test_delete_account_refuses_last_account(self):
        for account in ACCOUNT_IDS[:-1]:
            self.app.delete_account(account, revoke_keys=True, apply=False)
        with self.assertRaisesRegex(ValueError, "最后一个"):
            self.app.delete_account(ACCOUNT_IDS[-1], revoke_keys=True, apply=False)

    def test_delete_account_rolls_back_state_when_gateway_reload_fails(self):
        self.app.render()
        with mock.patch.object(self.app, "compose") as compose, mock.patch.object(
            self.app, "_reload_gateway_if_running", side_effect=RuntimeError("reload failed")
        ):
            with self.assertRaisesRegex(RuntimeError, "reload failed"):
                self.app.delete_account("alpha")

        self.assertIn("alpha", self.app.accounts())
        self.assertTrue((self.root / "auth" / "alpha").is_dir())
        self.assertTrue(any(call.args[:3] == ("up", "-d", "cliproxy-alpha") for call in compose.call_args_list))

    def test_delete_user_requires_explicit_revoke_and_removes_management_history(self):
        self.app.create_key("alice@example.com:alpha", apply=False)
        with self.assertRaisesRegex(ValueError, "确认同时停用"):
            self.app.delete_user("alice@example.com", apply=False)

        result = self.app.delete_user("alice@example.com", revoke_keys=True, apply=False)

        self.assertEqual(result["removed_records"], 4)
        self.assertEqual(result["revoked_active_keys"], 1)
        self.assertEqual(self.app._read_registry(), [])
        self.assertFalse(self.app.issued_path.exists())

    def test_migrate_legacy_account_keys_reuses_latest_routed_key(self):
        now = int(time.time())
        legacy = []
        for index, account in enumerate(ACCOUNT_IDS):
            record = self.app._new_record("alice@example.com:" + account)
            record["key"] = "legacy-key-{}".format(index)
            record["created_at"] = now + index
            record["updated_at"] = now + index
            legacy.append(record)
        self.app._write_registry(legacy)
        self.app.access_log.write_text(
            "{}.0\talice@example.com:gamma\tgamma\t200\t0.1\n".format(now + 10),
            encoding="utf-8",
        )

        result = self.app.migrate_single_user_keys(apply=False)

        self.assertEqual(result["migrated_users"], 1)
        self.assertEqual(self.app.user_route("alice@example.com"), "gamma")
        active = self.app.active_records()
        self.assertEqual(len(active), 4)
        self.assertEqual({item["key"] for item in active}, {"legacy-key-2"})
        self.assertEqual(
            len([item for item in self.app._read_registry() if item["status"] == "rotated"]),
            4,
        )

    def test_migrate_dry_run_does_not_change_registry_or_routes(self):
        records = []
        for index, account in enumerate(ACCOUNT_IDS):
            record = self.app._new_record("alice@example.com:" + account)
            record["key"] = "legacy-key-{}".format(index)
            records.append(record)
        self.app._write_registry(records)
        before_records = self.app._read_registry()
        before_routes = self.app._read_routes()

        result = self.app.migrate_single_user_keys(dry_run=True)

        self.assertTrue(result["dry_run"])
        self.assertEqual(result["migrated_users"], 1)
        self.assertEqual(self.app._read_registry(), before_records)
        self.assertEqual(self.app._read_routes(), before_routes)

    def test_delete_user_rolls_active_keys_back_when_apply_fails(self):
        self.app.create_user("alice@example.com", apply=False)
        original = self.app._read_registry()

        with mock.patch.object(self.app, "apply_changes", side_effect=RuntimeError("apply failed")):
            with self.assertRaisesRegex(RuntimeError, "apply failed"):
                self.app.delete_user("alice@example.com", revoke_keys=True)

        self.assertEqual(self.app._read_registry(), original)

    def test_rotate_management_key_updates_shared_configs_without_exposing_old_key(self):
        management_config = self.root / "management" / "config" / "config.yaml"
        management_config.parent.mkdir(parents=True)
        management_config.write_text(
            'remote-management:\n  secret-key: "old-hash"\nplugins:\n  enabled: true\n',
            encoding="utf-8",
        )

        result = self.app.rotate_management_key("new-management-key!", apply=False)

        self.assertTrue(result["rotated"])
        self.assertEqual(self.app.management_key(), "new-management-key!")
        self.assertFalse((self.root / "secrets" / "cpa-management.key").exists())
        self.assertIn('secret-key: "new-management-key!"', management_config.read_text())
        self.assertIn('secret-key: "new-management-key!"', (self.root / "configs" / "alpha.yaml").read_text())

    def test_rotate_management_key_rolls_back_when_runtime_restart_fails(self):
        with mock.patch.object(self.app, "compose", side_effect=RuntimeError("restart failed")):
            with self.assertRaisesRegex(RuntimeError, "restart failed"):
                self.app.rotate_management_key("new-management-key!")
        self.assertEqual(self.app.management_key(), "test-management-key")

    def test_active_stats_count_distinct_emails_per_named_account(self):
        now = time.time()
        log_path = self.root / "logs" / "gateway" / "access.tsv"
        log_path.parent.mkdir(parents=True, exist_ok=True)
        log_path.write_text(
            f"{now - 10:.3f}\talice@example.com:alpha\talpha\t200\t0.1\n"
            f"{now - 9:.3f}\talice@example.com:alpha\talpha\t200\t0.2\n"
            f"{now - 8:.3f}\tbob@example.com:alpha\talpha\t500\t0.3\n"
            f"{now - 400:.3f}\talice@example.com:beta\tbeta\t200\t0.1\n"
        )

        stats = self.app.active_stats(window_seconds=300, now=now)

        self.assertEqual(stats["alpha"]["count"], 2)
        self.assertEqual(stats["alpha"]["requests"], 3)
        self.assertEqual(
            stats["alpha"]["users"],
            ["alice@example.com", "bob@example.com"],
        )
        self.assertEqual(stats["beta"]["count"], 0)

    def test_inflight_stats_uses_gateway_internal_http_listener(self):
        class Response:
            def __enter__(self):
                return self

            def __exit__(self, *unused):
                return False

            @staticmethod
            def read(unused_limit=-1):
                return json.dumps(
                    [
                        {
                            "label": "alice@example.com:alpha",
                            "account": "alpha",
                            "inflight": 1,
                        }
                    ]
                ).encode("utf-8")

        with mock.patch.object(
            self.app, "gateway_internal_url", return_value="http://edge:8319"
        ), mock.patch(
            "urllib.request.urlopen", return_value=Response()
        ) as urlopen, mock.patch.object(self.app, "compose") as compose:
            stats = self.app.inflight_stats()

        request = urlopen.call_args.args[0]
        self.assertEqual(request.full_url, "http://edge:8319/__stats")
        self.assertEqual(request.get_header("Accept"), "application/json")
        self.assertEqual(
            urlopen.call_args.kwargs["timeout"],
            self.module.INFLIGHT_STATS_HTTP_TIMEOUT_SECONDS,
        )
        compose.assert_not_called()
        self.assertEqual(stats["alpha"]["count"], 1)
        self.assertEqual(stats["alpha"]["users"], ["alice@example.com"])
        self.assertEqual(stats["beta"]["count"], 0)

    def test_inflight_stats_falls_back_to_edge_exec_when_http_fails(self):
        response = subprocess.CompletedProcess(
            args=[],
            returncode=0,
            stdout=json.dumps(
                [
                    {
                        "label": "alice@example.com:alpha",
                        "account": "alpha",
                        "inflight": 1,
                    }
                ]
            ),
            stderr="",
        )

        with mock.patch(
            "urllib.request.urlopen",
            side_effect=self.module.urllib.error.URLError("unavailable"),
        ), mock.patch.object(self.app, "compose", return_value=response) as compose:
            stats = self.app.inflight_stats()

        compose.assert_called_once_with(
            "exec",
            "-T",
            "edge",
            "wget",
            "-qO-",
            "http://127.0.0.1:8319/__stats",
            capture=True,
        )
        self.assertEqual(stats["alpha"]["count"], 1)
        self.assertEqual(stats["alpha"]["users"], ["alice@example.com"])
        self.assertEqual(stats["beta"]["count"], 0)

    def test_inflight_stats_falls_back_when_http_payload_is_malformed(self):
        class Response:
            def __enter__(self):
                return self

            def __exit__(self, *unused):
                return False

            @staticmethod
            def read(unused_limit=-1):
                return b'{"unexpected":"object"}'

        fallback = subprocess.CompletedProcess(
            args=[], returncode=0, stdout="[]", stderr=""
        )
        with mock.patch(
            "urllib.request.urlopen", return_value=Response()
        ), mock.patch.object(
            self.app, "compose", return_value=fallback
        ) as compose:
            stats = self.app.inflight_stats()

        compose.assert_called_once()
        self.assertTrue(all(item["count"] == 0 for item in stats.values()))

    def test_recent_access_rows_reads_appends_and_resets_after_truncate(self):
        now = time.time()
        log_path = self.root / "logs" / "gateway" / "access.tsv"
        log_path.parent.mkdir(parents=True, exist_ok=True)
        log_path.write_text(
            f"{now - 10:.3f}\talice@example.com:alpha\talpha\t200\t0.1\n",
            encoding="utf-8",
        )
        parser = self.app._parse_access_log_line
        self.app._parse_access_log_line = mock.Mock(wraps=parser)

        first = self.app.active_stats(window_seconds=300, now=now)
        with log_path.open("a", encoding="utf-8") as handle:
            handle.write(
                f"{now - 5:.3f}\tbob@example.com:alpha\talpha\t500\t0.2\n"
            )
        second = self.app.active_stats(window_seconds=300, now=now)

        self.assertEqual(first["alpha"]["requests"], 1)
        self.assertEqual(second["alpha"]["requests"], 2)
        self.assertEqual(self.app._parse_access_log_line.call_count, 2)

        log_path.write_text(
            f"{now - 1:.3f}\tcarol@example.com:alpha\talpha\t429\t0.1\n",
            encoding="utf-8",
        )
        after_truncate = self.app.active_stats(window_seconds=300, now=now)

        self.assertEqual(after_truncate["alpha"]["requests"], 1)
        self.assertEqual(after_truncate["alpha"]["users"], ["carol@example.com"])
        self.assertEqual(self.app._parse_access_log_line.call_count, 3)

    def test_login_uses_named_service_and_device_flow(self):
        def complete_login(*unused, **unused_kwargs):
            (self.root / "auth" / "alpha" / "oauth.json").write_text("{}")

        with mock.patch.object(self.app, "compose", side_effect=complete_login) as compose:
            self.app.login("alpha")

        compose.assert_called_once_with(
            "run",
            "--rm",
            "--no-deps",
            "-T",
            "cliproxy-alpha",
            "./CLIProxyAPI",
            "-config",
            "/CLIProxyAPI/configs/alpha.yaml",
            "-codex-device-login",
            "-no-browser",
        )

    def test_login_marks_zero_exit_without_auth_change_as_failed(self):
        with mock.patch.object(self.app, "compose"):
            with self.assertRaisesRegex(ValueError, "未完成"):
                self.app.login("alpha")

    def test_all_target_keeps_admin_control_plane_out_of_bulk_operations(self):
        services = self.module.target_services(self.app, "all")

        self.assertNotIn("edge", services)
        self.assertNotIn("gateway-blue", services)
        self.assertNotIn("gateway-green", services)
        self.assertIn("web", services)
        self.assertIn("management", services)
        self.assertIn("usage-collector", services)
        self.assertIn("log-maintenance", services)
        self.assertNotIn("admin", services)

    def test_layout_bootstraps_fixed_edge_slot_include_once(self):
        slot = self.root / "state" / "edge" / "active-gateway.conf"
        self.assertEqual(
            slot.read_text(encoding="utf-8"),
            "set $active_gateway_backend gateway-blue:8317;\n",
        )
        slot.write_text(
            "set $active_gateway_backend gateway-green:8317;\n",
            encoding="utf-8",
        )
        self.app.ensure_layout()
        self.assertIn("gateway-green:8317", slot.read_text(encoding="utf-8"))

    def test_gateway_and_edge_lifecycle_are_release_owned(self):
        for target in ("gateway", "gateway-blue", "gateway-green", "edge"):
            with self.assertRaisesRegex(ValueError, "目标必须"):
                self.module.target_services(self.app, target)

    def test_auth_status_counts_named_account_files(self):
        (self.root / "auth" / "alpha" / "auth.json").write_text("{}")
        (self.root / "auth" / "alpha" / "ignored.txt").write_text("ignored")

        status = self.app.auth_status()

        self.assertEqual(status["alpha"]["files"], 1)
        self.assertEqual(status["beta"]["files"], 0)

    def test_verify_routing_checks_every_email_identifier_without_exposing_keys(self):
        self.app.create_user("alice@example.com", apply=False)

        class Response:
            status = 200

            def __enter__(self):
                return self

            def __exit__(self, *unused):
                return False

        with mock.patch("urllib.request.urlopen", return_value=Response()) as urlopen:
            result = self.app.verify_routing()

        self.assertEqual(
            [item["label"] for item in result],
            sorted("alice@example.com:" + account for account in ACCOUNT_IDS),
        )
        self.assertTrue(all(item["status"] == 200 for item in result))
        self.assertTrue(
            all(
                call.args[0].full_url.endswith("/__internal/probe/models")
                for call in urlopen.call_args_list
            )
        )

    def test_health_uses_internal_probe_endpoint(self):
        for index, account in enumerate(ACCOUNT_IDS):
            user = "health{}@example.com".format(index)
            self.app.create_user(user, apply=False)
            self.app.set_user_route(user, account, apply=False)

        class Response:
            status = 200

            def __enter__(self):
                return self

            def __exit__(self, *unused):
                return False

            @staticmethod
            def read():
                return b'{"data":[]}'

        with mock.patch("urllib.request.urlopen", return_value=Response()) as urlopen, mock.patch(
            "builtins.print"
        ):
            self.app.health()

        self.assertEqual(urlopen.call_count, len(ACCOUNT_IDS))
        self.assertTrue(
            all(
                call.args[0].full_url.endswith("/__internal/probe/models")
                for call in urlopen.call_args_list
            )
        )


if __name__ == "__main__":
    unittest.main()
