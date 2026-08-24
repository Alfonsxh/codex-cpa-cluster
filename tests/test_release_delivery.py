import hashlib
import importlib.machinery
import importlib.util
import json
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).parents[1]


class ReleaseDeliveryTests(unittest.TestCase):
    def test_local_agent_and_deployment_context_stays_out_of_git_and_images(self):
        gitignore = (ROOT / ".gitignore").read_text(encoding="utf-8")
        dockerignore = (ROOT / ".dockerignore").read_text(encoding="utf-8")
        package = (ROOT / "scripts" / "package-release.sh").read_text(
            encoding="utf-8"
        )

        self.assertIn("/.harness/", gitignore)
        self.assertIn("/AGENTS.md", gitignore)
        self.assertIn("\n.harness\n", "\n" + dockerignore)
        self.assertIn("\nAGENTS.md\n", "\n" + dockerignore)
        self.assertNotIn("  .harness \\\n", package)
        self.assertNotIn("  AGENTS.md \\\n", package)
        self.assertIn("COPYFILE_DISABLE=1 tar --no-xattrs", package)
        self.assertIn("LIBARCHIVE.xattr.com.apple.", package)
        self.assertIn("release archive contains Apple metadata", package)

    def test_ci_is_manual_and_uses_the_same_local_quality_gate(self):
        self.assertFalse((ROOT / ".gitlab-ci.yml").exists())
        pipeline = (ROOT / ".github" / "workflows" / "ci.yml").read_text(
            encoding="utf-8"
        )
        self.assertIn("on:\n  workflow_dispatch:\n", pipeline)
        self.assertNotIn("  push:\n", pipeline)
        self.assertNotIn("  pull_request:\n", pipeline)
        self.assertIn("  validate:\n", pipeline)
        self.assertIn("  package:\n    needs: validate", pipeline)
        self.assertIn("permissions:\n  contents: read", pipeline)
        self.assertNotIn("deploy-test", pipeline)
        self.assertNotIn("deploy-production", pipeline)
        self.assertNotIn("SSH_PRIVATE_KEY", pipeline)
        self.assertNotIn("DEPLOY_WEBHOOK", pipeline)
        self.assertNotIn("docker login", pipeline)
        self.assertNotIn("packages: write", pipeline)
        self.assertNotIn("make publish", pipeline)
        self.assertIn("sudo apt-get install -y --no-install-recommends lua5.4", pipeline)
        self.assertIn("run: make verify", pipeline)

        verifier = (ROOT / "scripts" / "verify.sh").read_text(encoding="utf-8")
        self.assertIn("python3 -m unittest discover", verifier)
        self.assertIn("tests/test_gateway_state.lua", verifier)
        self.assertIn("scripts/check-public-release.py", verifier)
        self.assertIn("config --quiet", verifier)
        self.assertIn("git -C \"$ROOT_DIR\" diff --check", verifier)

    def test_release_archive_has_no_apple_metadata(self):
        with tempfile.TemporaryDirectory() as directory:
            archive_path = Path(directory) / "release.tar.gz"
            subprocess.run(
                ["sh", str(ROOT / "scripts" / "package-release.sh"), str(archive_path)],
                cwd=ROOT,
                check=True,
                capture_output=True,
                text=True,
            )
            with tarfile.open(archive_path, "r:gz") as archive:
                members = archive.getmembers()
                member_names = {member.name for member in members}
            apple_double = [
                member.name
                for member in members
                if any(part.startswith("._") for part in Path(member.name).parts)
            ]
            apple_xattrs = [
                key
                for member in members
                for key in member.pax_headers
                if key.startswith("LIBARCHIVE.xattr.com.apple.")
            ]
            self.assertEqual(apple_double, [])
            self.assertEqual(apple_xattrs, [])
            self.assertTrue(
                {
                    "README.md",
                    "compose.env.example",
                    "CHANGELOG.md",
                    "CONTRIBUTING.md",
                    "SECURITY.md",
                    "codex-cpa",
                    "docker-compose.v2-test.yml",
                    "docker-compose.v2.yml",
                    "docker-compose.v1-compare.yml",
                    "v1-compare.env.example",
                    "scripts/v1-compare-admin.py",
                    "v2-compose.env.example",
                    "go.mod",
                    "go.sum",
                    "v2/Dockerfile",
                    "cmd/gateway/main.go",
                    "internal/gateway/server.go",
                    "frontend/package.json",
                    "tools/openapi/package.json",
                }.issubset(member_names)
            )
            self.assertFalse(
                any(
                    "node_modules" in Path(name).parts
                    or Path(name).parts[:2] == ("frontend", "dist")
                    for name in member_names
                )
            )

    def test_production_compose_has_no_build_context_and_dev_override_has_it(self):
        production = (ROOT / "docker-compose.yml").read_text(encoding="utf-8")
        development = (ROOT / "docker-compose.dev.yml").read_text(encoding="utf-8")

        self.assertNotIn("    build:", production)
        self.assertIn("    build:", development)
        for component in ("admin/Dockerfile", "web/Dockerfile", "gateway/Dockerfile", "edge/Dockerfile"):
            self.assertIn(component, development)

    def test_v2_test_compose_keeps_backends_internal_and_exposes_only_edge(self):
        compose = (ROOT / "docker-compose.v2-test.yml").read_text(
            encoding="utf-8"
        )

        self.assertIn("name: codex-cpa-v2-test", compose)
        self.assertIn("  v2-test:\n    driver: bridge\n    internal: true", compose)
        self.assertIn("  v2-ingress:\n    driver: bridge", compose)
        edge = compose[compose.index("  edge:\n") : compose.index("\nnetworks:\n")]
        self.assertIn('"127.0.0.1:${V2_TEST_PUBLIC_PORT:-28317}:8317"', edge)
        self.assertIn('"127.0.0.1:${V2_TEST_INTERNAL_PORT:-28319}:8319"', edge)
        self.assertIn("      - v2-test\n      - v2-ingress", edge)
        for service in (
            "cliproxy-alpha",
            "gateway-blue",
            "gateway-green",
            "web",
        ):
            start = compose.index(f"  {service}:\n")
            end = compose.find("\n  ", start + 3)
            block = compose[start:] if end == -1 else compose[start:end]
            self.assertNotIn("v2-ingress", block)

    def test_v1_compare_compose_is_isolated_and_has_no_writers(self):
        compose = (ROOT / "docker-compose.v1-compare.yml").read_text(
            encoding="utf-8"
        )
        self.assertIn("V1_COMPARE_DEPLOY_ROOT", compose)
        self.assertIn("V1_COMPARE_DOCKER_READ_PROXY_IMAGE", compose)
        self.assertIn("V1_COMPARE_HOST_DOCKER_SOCKET_PATH", compose)
        self.assertIn("V1_COMPARE_LIVE_COMPOSE_PROJECT", compose)
        self.assertIn("v1-docker-read-socket:/var/run/cpa-docker-read", compose)
        self.assertNotIn("target: /var/run/docker.sock", compose)
        self.assertIn('CLIPROXY_V1_COMPARE_MODE: "1"', compose)
        self.assertIn(
            "./scripts/v1-compare-admin.py:/opt/cpa-v1-compare/v1-compare-admin.py:ro",
            compose,
        )
        self.assertIn("  control:\n    driver: bridge\n    internal: true", compose)
        self.assertIn("  ingress:\n    driver: bridge", compose)
        self.assertIn("  upstream:\n    external: true", compose)
        for service in ("docker-read-proxy", "admin", "web", "gateway-blue", "gateway-green", "edge"):
            self.assertIn(f"  {service}:\n", compose)
        for forbidden in (
            "usage-collector:",
            "log-maintenance:",
            "management:",
            'profiles: ["writers"]',
            'profiles: ["external-effects"]',
        ):
            self.assertNotIn(forbidden, compose)
        edge = compose[compose.index("  edge:\n") : compose.index("\nnetworks:\n")]
        self.assertIn("      - control\n      - ingress", edge)
        self.assertNotIn("upstream", edge)

        deploy = (ROOT / "scripts" / "deploy-v1-compare-target.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn(".v2-isolated-copy.json", deploy)
        self.assertIn("refusing live or broad v1 comparison root", deploy)
        self.assertIn("V1_COMPARE_HOST_DOCKER_SOCKET_PATH", deploy)
        self.assertIn("V1_COMPARE_LIVE_COMPOSE_PROJECT", deploy)
        self.assertIn("V1_COMPARE_UPSTREAM_DEPLOY_ROOT", deploy)
        self.assertIn("V1_COMPARE_CONFIRM_UPSTREAM_DEPLOY_ROOT", deploy)
        self.assertIn('network_scope" = migration-disposable', deploy)
        self.assertIn("upstream container escapes the disposable root", deploy)
        self.assertIn('ENV_FILE="$ROOT_DIR/$ENV_FILE"', deploy)
        self.assertIn("io.codex-cpa.component-digest", deploy)
        self.assertIn("chgrp 65534", deploy)
        self.assertIn(
            '"$V1_COMPARE_DEPLOY_ROOT/state/gateway/auth-snapshot.json"', deploy
        )
        self.assertIn("ensure_comparison_network()", deploy)
        self.assertIn("remove_unexpected_comparison_networks()", deploy)
        self.assertIn("docker network connect \\", deploy)
        self.assertIn("docker network disconnect --force", deploy)
        self.assertIn(
            "refusing network repair outside exact v1 comparison service", deploy
        )
        self.assertIn('compose up -d --no-deps "$comparison_service"', deploy)
        self.assertIn(
            '"$comparison_service" "$control_network" "$V1_COMPARE_UPSTREAM_NETWORK"',
            deploy,
        )
        self.assertNotIn(
            "compose up -d --wait docker-read-proxy admin web gateway-blue gateway-green edge",
            deploy,
        )
        self.assertNotIn("docker build", deploy)
        self.assertNotIn("docker pull", deploy)

        route_probe = (
            ROOT / "scripts" / "migration-data-plane-route-compare.py"
        ).read_text(encoding="utf-8")
        self.assertIn('"PUT", "/usage/me/group"', route_probe)
        self.assertIn("route_restored", route_probe)
        self.assertIn("finally:", route_probe)
        self.assertNotIn("UPDATE user_routes", route_probe)

        example = (ROOT / "v1-compare.env.example").read_text(encoding="utf-8")
        self.assertIn("V1_COMPARE_UPSTREAM_DEPLOY_ROOT", example)
        self.assertIn("V1_COMPARE_CONFIRM_UPSTREAM_DEPLOY_ROOT", example)
        self.assertIn("migration-disposable", example)
        self.assertNotIn("V1_COMPARE_UPSTREAM_NETWORK=cliproxy-backend", example)
        self.assertNotIn("V1_COMPARE_LIVE_COMPOSE_PROJECT=cliproxy-multi", example)

        entrypoint = (ROOT / "scripts" / "v1-compare-admin.py").read_text(
            encoding="utf-8"
        )
        self.assertIn("CLIPROXY_V1_COMPARE_MODE", entrypoint)
        self.assertIn(".v2-isolated-copy.json", entrypoint)
        self.assertIn("stat.S_ISSOCK(socket_stat.st_mode)", entrypoint)
        self.assertIn('"POST", "/containers/create"', entrypoint)
        self.assertIn("permits mutations", entrypoint)
        self.assertNotIn("OwnershipGuard", entrypoint)
        self.assertNotIn("NotificationScheduler", entrypoint)
        self.assertNotIn("AccountFailoverScheduler", entrypoint)

    def test_v2_web_image_runs_the_go_web_binary(self):
        dockerfile = (ROOT / "v2" / "Dockerfile").read_text(encoding="utf-8")

        self.assertIn(
            "for command in admin collector docker-read-proxy edge failover gateway log-maintenance migration-compare notifications ownership quota test-upstream web",
            dockerfile,
        )
        self.assertIn("FROM go-runtime AS web", dockerfile)
        self.assertIn(
            "COPY --from=go-builder /out/cpa-web /usr/local/bin/cpa-web",
            dockerfile,
        )
        self.assertIn(
            "COPY --from=web-builder /src/frontend/dist/portal /srv/cpa-web/portal",
            dockerfile,
        )
        self.assertNotIn("COPY portal /srv/cpa-web/portal", dockerfile)
        self.assertIn('ENTRYPOINT ["/usr/local/bin/cpa-web"]', dockerfile)
        self.assertNotIn("WEB_RUNTIME_IMAGE", dockerfile)
        self.assertNotIn("web-nginx.conf", dockerfile)
        self.assertIn("migration-compare", dockerfile)
        self.assertIn("/out/cpa-migration-compare", dockerfile)
        self.assertIn("/out/cpa-docker-read-proxy", dockerfile)

    def test_v2_target_compose_separates_core_writers_and_external_effects(self):
        compose = (ROOT / "docker-compose.v2.yml").read_text(encoding="utf-8")
        self.assertIn("V2_DEPLOY_ROOT", compose)
        self.assertIn("V2_UPSTREAM_NETWORK", compose)
        self.assertIn("V2_DOCKER_SOCKET_PATH", compose)
        self.assertIn("V2_ADMIN_DOCKER_SOCKET_PATH", compose)
        self.assertIn("V2_ADMIN_DOCKER_HOST", compose)
        self.assertIn("--runtime-read-only=${V2_RUNTIME_READ_ONLY:-true}", compose)
        self.assertIn("v2-docker-read-socket:/var/run/cpa-docker-read", compose)
        self.assertIn("/var/run/docker-host.sock:ro", compose)
        self.assertNotIn("V2_DOCKER_SOCKET_PATH:-/var/run/docker.sock}:/var/run/docker.sock", compose)
        self.assertIn('profiles: ["writers"]', compose)
        self.assertIn('profiles: ["external-effects"]', compose)
        self.assertIn('group_add:\n      - "65534"', compose)
        self.assertIn('"${V2_PUBLIC_BIND_ADDRESS:-127.0.0.1}:${V2_PUBLIC_PORT:', compose)
        self.assertIn('"127.0.0.1:${V2_INTERNAL_PORT:', compose)
        self.assertNotIn("    build:", compose)
        for service in (
            "v2-admin",
            "v2-docker-read-proxy",
            "v2-usage-collector",
            "v2-quota",
            "v2-account-failover",
            "v2-notifications",
            "v2-log-maintenance",
            "v2-web",
            "v2-gateway-blue",
            "v2-gateway-green",
            "v2-edge",
        ):
            self.assertIn("  {}:\n".format(service), compose)

        deploy = (ROOT / "scripts" / "deploy-v2-target.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("release-manifest.json", deploy)
        self.assertIn("io.codex-cpa.component-digest", deploy)
        self.assertIn("require_active_owner", deploy)
        self.assertIn("V2_CONFIRM_LEGACY_WRITERS_STOPPED", deploy)
        self.assertIn('V2_OWNERSHIP_ACTIVATION_TTL:=2m', deploy)
        self.assertIn('V2_PUBLIC_BIND_ADDRESS:=127.0.0.1', deploy)
        self.assertIn('V2_PUBLIC_PROBE_HOST:=$V2_PUBLIC_BIND_ADDRESS', deploy)
        self.assertIn('--ttl "$V2_OWNERSHIP_ACTIVATION_TTL"', deploy)
        self.assertIn("verify-images)", deploy)
        self.assertIn("Go v2 target images verified", deploy)
        self.assertIn("up-core)", deploy)
        self.assertIn('compose up -d --no-deps "$service"', deploy)
        self.assertNotIn('compose up -d --wait --no-deps "$service"', deploy)
        self.assertIn("/site-config.json", deploy)
        self.assertIn("ensure_candidate_network()", deploy)
        self.assertIn("wait_candidate_container()", deploy)
        self.assertIn('wait_candidate_container "$container"', deploy)
        self.assertIn(
            'ensure_candidate_network "$container" "$project" "$service" "$control_network"',
            deploy,
        )
        self.assertIn(
            'ensure_candidate_network "$container" "$project" "$service" "$V2_UPSTREAM_NETWORK"',
            deploy,
        )
        self.assertIn(
            'ensure_candidate_network "$container" "$project" "$service" "$ingress_network"',
            deploy,
        )
        self.assertIn('"com.docker.compose.project"', deploy)
        self.assertIn('"com.docker.compose.service"', deploy)
        self.assertIn("docker network connect \\", deploy)
        self.assertIn('    --alias "$service"', deploy)
        self.assertIn("refusing network repair outside exact Go v2 service", deploy)
        self.assertIn("require_core_topology", deploy)
        self.assertIn("missing required network", deploy)
        self.assertIn("missing required port binding", deploy)
        up_core = deploy[deploy.index("  up-core)") : deploy.index("  up-writers)")]
        self.assertLess(
            up_core.index('compose up -d --no-deps "$service"'),
            up_core.index(
                'ensure_candidate_network "$container" "$project" "$service" "$control_network"'
            ),
        )
        self.assertLess(
            up_core.index(
                'ensure_candidate_network "$container" "$project" "$service" "$control_network"'
            ),
            up_core.index('wait_candidate_container "$container"'),
        )
        self.assertIn("up-writers)", deploy)
        self.assertIn("up-notifications)", deploy)
        self.assertNotIn("docker build", deploy)
        self.assertNotIn("nginx", deploy.lower())

    def test_release_cli_verifies_descriptor_and_checksums(self):
        loader = importlib.machinery.SourceFileLoader(
            "codex_cpa_release_cli", str(ROOT / "codex-cpa")
        )
        spec = importlib.util.spec_from_loader(loader.name, loader)
        module = importlib.util.module_from_spec(spec)
        loader.exec_module(module)

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            archive = root / "codex-cpa-cluster-v1.2.3.tar.gz"
            archive.write_bytes(b"archive")
            components = {}
            for component in ("admin", "web", "gateway", "edge"):
                digest = hashlib.sha256(component.encode("utf-8")).hexdigest()
                components[component] = {
                    "source_sha256": digest,
                    "image": "ghcr.io/example/codex-cpa-{}:sha256-{}".format(
                        component, digest
                    ),
                }
            for component in ("v2-control", "v2-web", "v2-gateway", "v2-edge"):
                digest = hashlib.sha256(component.encode("utf-8")).hexdigest()
                components[component] = {
                    "source_sha256": digest,
                    "image": "ghcr.io/example/codex-cpa-{}:sha256-{}".format(
                        component, digest
                    ),
                }
            descriptor = root / "release-v1.2.3.json"
            descriptor.write_text(
                json.dumps(
                    {
                        "schema_version": 1,
                        "release_version": "v1.2.3",
                        "revision": "a" * 40,
                        "archive": {"name": archive.name},
                        "components": components,
                        "metadata_image": "ghcr.io/example/codex-cpa-release:v1.2.3",
                    }
                ),
                encoding="utf-8",
            )
            checksums = root / "SHA256SUMS"
            checksums.write_text(
                "{}  {}\n{}  {}\n".format(
                    module.file_sha256(archive),
                    archive.name,
                    module.file_sha256(descriptor),
                    descriptor.name,
                ),
                encoding="utf-8",
            )

            module.verify_release_assets(archive, descriptor, checksums)
            release = module.validate_descriptor(
                descriptor, "v1.2.3", archive.name
            )
            self.assertEqual(release["image_prefix"], "ghcr.io/example")
            self.assertEqual(release["revision"], "a" * 40)

    def test_makefile_exposes_separate_publish_and_target_pull_deploy(self):
        makefile = (ROOT / "Makefile").read_text(encoding="utf-8")
        self.assertIn("verify:", makefile)
        self.assertIn("publish-harbor", makefile)
        self.assertIn("publish-dockerhub", makefile)
        self.assertIn("publish-ghcr", makefile)
        self.assertIn("publish-all", makefile)
        self.assertIn("RELEASE_IMAGE_PREFIX=", makefile)
        self.assertIn("sh scripts/deploy-release.sh", makefile)

        self.assertIn("V2_TEST_PROJECT ?= codex-cpa-v2-test", makefile)
        self.assertIn("V1_COMPARE_ENV ?= v1-compare.env", makefile)
        for target in (
            "v1-compare-config:",
            "v1-compare-verify-images:",
            "v1-compare-up:",
            "v1-compare-smoke:",
            "v1-compare-ps:",
            "v1-compare-down:",
        ):
            self.assertIn(target, makefile)
        for target in (
            "v2-test-config:",
            "v2-test-build:",
            "v2-test-up:",
            "v2-test-down:",
        ):
            start = makefile.index(target)
            end = makefile.find("\n\n", start)
            recipe = makefile[start:] if end == -1 else makefile[start:end]
            self.assertIn("docker compose -p $(V2_TEST_PROJECT)", recipe)
        smoke_start = makefile.index("v2-test-smoke:")
        smoke_end = makefile.find("\n\n", smoke_start)
        smoke_recipe = makefile[smoke_start:smoke_end]
        self.assertIn('V2_TEST_PROJECT="$(V2_TEST_PROJECT)"', smoke_recipe)

        smoke = (ROOT / "scripts" / "v2-test-smoke.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn('docker compose \\\n    -p "$PROJECT"', smoke)
        self.assertIn('docker port "$edge_id" 8317/tcp', smoke)
        self.assertIn('docker port "$edge_id" 8319/tcp', smoke)

        self.assertIn("v2-test-faults:", makefile)
        self.assertIn("v2-lease-rehearsal:", makefile)
        self.assertIn("v2-worker-lease-rehearsal:", makefile)
        self.assertIn("v2-worker-process-rehearsal:", makefile)
        faults = (ROOT / "scripts" / "v2-test-faults.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("codex-cpa-v2-fault-test", faults)
        self.assertIn("compose down --remove-orphans", faults)
        self.assertIn('V2_TEST_GATEWAY_FIXTURE_DIR=', faults)
        self.assertIn('V2_TEST_EDGE_FIXTURE_DIR=', faults)

        worker_process = (
            ROOT / "scripts" / "v2-worker-process-rehearsal.sh"
        ).read_text(encoding="utf-8")
        self.assertIn("confirm-isolated-state-copy", worker_process)
        self.assertIn("duplicate Admin process", worker_process)
        self.assertIn("premature-collector-restart", worker_process)
        self.assertIn("expected-generation 1", worker_process)

        local_release = (ROOT / "scripts" / "local-release.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn('ACTION=${1:-publish}', local_release)
        self.assertIn('make -C "$ROOT_DIR" verify', local_release)
        self.assertIn('scripts/release-images.sh" publish', local_release)
        self.assertIn('scripts/package-release.sh" "$ARCHIVE"', local_release)
        self.assertIn('"$INSTALLER#Installer and upgrader"', local_release)
        self.assertIn('gh release create "$VERSION"', local_release)
        self.assertIn('gh release edit "$VERSION"', local_release)
        self.assertIn("工作区存在未提交修改，拒绝发布", local_release)
        self.assertIn(
            'rev-parse --verify "$VERSION^{commit}"', local_release
        )
        self.assertLess(
            local_release.index('scripts/release-images.sh" publish'),
            local_release.index('git -C "$ROOT_DIR" push'),
        )
        self.assertLess(
            local_release.index('git -C "$ROOT_DIR" push'),
            local_release.index('gh release create "$VERSION"'),
        )

        publisher = (ROOT / "scripts" / "release-images.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("docker buildx build", publisher)
        self.assertIn("docker manifest inspect", publisher)
        self.assertIn("docker push", publisher)
        self.assertIn(
            'RELEASE_COMPONENTS="admin web gateway edge v2-control v2-web v2-gateway v2-edge"',
            publisher,
        )
        self.assertIn("build_component v2-control v2/Dockerfile control", publisher)
        self.assertIn("build_component v2-web v2/Dockerfile web", publisher)
        self.assertIn("build_component v2-gateway v2/Dockerfile gateway", publisher)
        self.assertIn("build_component v2-edge v2/Dockerfile edge", publisher)
        self.assertIn('rev-parse "$VERSION^{commit}"', publisher)
        self.assertIn("版本标签已存在且验证一致，跳过", publisher)
        self.assertIn("已存在的镜像与组件指纹不匹配", publisher)
        self.assertIn("已存在的发布元数据与版本或 revision 不匹配", publisher)
        self.assertNotIn('--label "org.opencontainers.image.version=$VERSION"', publisher)
        self.assertLess(
            publisher.index("codex-cpa-release:$VERSION"),
            publisher.index("codex-cpa-release:latest"),
        )

        deploy = (ROOT / "scripts" / "deploy-release.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("RELEASE_VERSION=${RELEASE_VERSION:?", deploy)
        self.assertIn("RELEASE_IMAGE_PREFIX=${RELEASE_IMAGE_PREFIX:?", deploy)
        self.assertIn('docker pull "$ADMIN_RUNTIME_IMAGE"', deploy)
        self.assertIn('docker pull "$WEB_RUNTIME_IMAGE"', deploy)
        self.assertNotIn("docker build \\", deploy)

        installer = (ROOT / "scripts" / "install-release.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("首次安装只允许使用空目录", installer)
        self.assertIn('run_cli store migrate-secrets', installer)
        self.assertIn('compose up -d --no-deps', installer)
        self.assertIn('gateway_release_probe.py', installer)
        self.assertNotIn(r'Labels \"io.codex-cpa.component\"', installer)

    def test_release_serializes_with_runtime_updates_and_uses_control_image(self):
        deploy = (ROOT / "scripts" / "deploy-release.sh").read_text(
            encoding="utf-8"
        )

        self.assertIn('RUNTIME_OPERATION_LOCK="$TARGET/state/runtime-operation.lock"', deploy)
        self.assertIn('flock -n 9', deploy)
        self.assertIn(
            'app._compose_with_image(image_id, "up", "-d", "--no-deps", service)',
            deploy,
        )
        self.assertIn('release_cli stage-deployment', deploy)
        self.assertIn('--preserve-cliproxy-image "$PRESERVED_CLIPROXY_IMAGE"', deploy)
        self.assertIn('--env-file "$TARGET/state/compose.env"', deploy)
        wait_start = deploy.index("wait_for_runtime_services() {")
        wait_end = deploy.index("\ncompose_network_name()", wait_start)
        wait_helper = deploy[wait_start:wait_end]
        self.assertIn('"--env-file", str(root / ".env")', wait_helper)
        self.assertIn('"--env-file", str(compose_environment)', wait_helper)
        self.assertIn('--metadata-image "$RELEASE_METADATA_IMAGE"', deploy)

    def test_admin_runtime_source_is_not_shadowed_by_deployment_root_mount(self):
        dockerfile = (ROOT / "admin" / "Dockerfile").read_text(encoding="utf-8")
        compose = (ROOT / "docker-compose.yml").read_text(encoding="utf-8")
        self.assertIn("CLIPROXY_APP_ROOT=/opt/codex-cpa-runtime", dockerfile)
        self.assertIn("/opt/codex-cpa-runtime/admin/server.py", compose)
        self.assertNotIn("CLIPROXY_RELEASE_VERSION", compose)
        self.assertNotIn("CLIPROXY_RELEASE_METADATA_IMAGE", compose)

    def test_env_and_sqlite_compose_projection_have_distinct_responsibilities(self):
        bootstrap = (ROOT / ".env.example").read_text(encoding="utf-8")
        projection = (ROOT / "compose.env.example").read_text(encoding="utf-8")
        self.assertEqual(
            {
                line.split("=", 1)[0]
                for line in bootstrap.splitlines()
                if line and not line.startswith("#")
            },
            {
                "DEPLOY_ROOT",
                "INSTANCE_NAME",
                "COMPOSE_PROJECT_NAME",
                "DOCKER_NETWORK_NAME",
            },
        )
        self.assertIn("CLIPROXY_IMAGE=", projection)
        self.assertIn("ADMIN_BASE_IMAGE=", projection)
        self.assertIn("GATEWAY_PORT=", projection)
        self.assertNotIn("RELEASE_VERSION=", projection)
        self.assertNotIn("RELEASE_METADATA_IMAGE=", projection)
        self.assertNotIn("GATEWAY_DRAIN_TIMEOUT_SECONDS=", projection)
        self.assertNotIn("CLIPROXY_IMAGE=", bootstrap)
        compose = (ROOT / "docker-compose.yml").read_text(encoding="utf-8")
        self.assertIn("${CLIPROXY_IMAGE:?state/compose.env missing", compose)
        self.assertIn("${ADMIN_IMAGE:?state/compose.env missing", compose)

    def test_only_release_metadata_image_carries_release_version_labels(self):
        for component in ("admin", "web", "gateway", "edge"):
            dockerfile = (ROOT / component / "Dockerfile").read_text(
                encoding="utf-8"
            )
            self.assertIn('org.opencontainers.image.version=""', dockerfile)
            self.assertIn('org.opencontainers.image.revision=""', dockerfile)
            self.assertIn(
                'org.opencontainers.image.source="https://github.com/Alfonsxh/codex-cpa-cluster"',
                dockerfile,
            )
        metadata = (ROOT / "release" / "Dockerfile").read_text(encoding="utf-8")
        self.assertIn(
            'org.opencontainers.image.version="${RELEASE_VERSION}"', metadata
        )
        self.assertIn(
            'org.opencontainers.image.revision="${RELEASE_REVISION}"', metadata
        )
        self.assertIn(
            'org.opencontainers.image.source="https://github.com/Alfonsxh/codex-cpa-cluster"',
            metadata,
        )

        v2 = (ROOT / "v2" / "Dockerfile").read_text(encoding="utf-8")
        self.assertEqual(v2.count('org.opencontainers.image.version=""'), 2)
        self.assertIn("go build -tags timetzdata", v2)
        self.assertIn("test -s /etc/ssl/certs/ca-certificates.crt", v2)
        self.assertNotIn("apk add", v2)
        for name in ("GO_BUILDER_IMAGE", "NODE_BUILDER_IMAGE", "RUNTIME_IMAGE"):
            line = next(line for line in v2.splitlines() if line.startswith(f"ARG {name}="))
            self.assertRegex(line, r"@sha256:[0-9a-f]{64}$")

    def test_public_release_scanner_rejects_private_ipv4_and_unapproved_domain(self):
        path = ROOT / "scripts" / "check-public-release.py"
        spec = importlib.util.spec_from_file_location("public_release_check", path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / ".git").mkdir()
            # The scanner gets its file list from Git. Test the two core predicates
            # directly so the test does not mutate global Git configuration.
            self.assertFalse(module.allowed_domain("internal.corp.example"))
            self.assertTrue(module.allowed_domain("cpa.example.com"))
            self.assertTrue(module.allowed_domain("github.com"))
            self.assertTrue(module.allowed_domain("ghcr.io"))
            self.assertTrue(module.allowed_domain("goproxy.cn"))
            self.assertTrue(module.allowed_domain("sum.golang.google.cn"))
            self.assertTrue(module.allowed_domain("registry.npmmirror.com"))
            self.assertTrue(module.allowed_domain("opencollective.com"))
            self.assertTrue(module.allowed_domain("dotenvx.com"))
            self.assertTrue(module.allowed_domain("paulmillr.com"))
            self.assertTrue(module.allowed_domain("tidelift.com"))
            address = module.ipaddress.ip_address(3232238100)
            self.assertIn(address, module.ipaddress.ip_network((3232235520, 16)))

    def test_public_release_scanner_rejects_local_agent_context(self):
        path = ROOT / "scripts" / "check-public-release.py"
        spec = importlib.util.spec_from_file_location("public_release_check", path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(["git", "init", "-q"], cwd=root, check=True)
            (root / ".harness").mkdir()
            (root / ".harness" / "deployment.md").write_text(
                "local deployment context\n",
                encoding="utf-8",
            )
            (root / "AGENTS.md").write_text("local agent context\n", encoding="utf-8")
            subprocess.run(
                ["git", "add", "-f", ".harness/deployment.md", "AGENTS.md"],
                cwd=root,
                check=True,
            )

            problems = module.scan(root)
            self.assertEqual(
                {item[0].as_posix() for item in problems},
                {".harness/deployment.md", "AGENTS.md"},
            )

    def test_public_release_scanner_rejects_organization_and_contributor_markers(self):
        path = ROOT / "scripts" / "check-public-release.py"
        spec = importlib.util.spec_from_file_location("public_release_check", path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(["git", "init", "-q"], cwd=root, check=True)
            markers = (
                "".join(("wo", "qu")),
                "".join(("q", "data")),
                "".join(("q", "arch")),
                "".join(("q", "fusion")),
                "".join(("chen", "hui", ".", "shang")),
                "".join(("Q", "AI-123")),
            )
            (root / "README.md").write_text("\n".join(markers), encoding="utf-8")
            marked_path = root / ("legacy-" + "".join(("w", "q")) + "-notes.md")
            marked_path.write_text("neutral content\n", encoding="utf-8")
            subprocess.run(["git", "add", "README.md", marked_path.name], cwd=root, check=True)

            problems = module.scan(root)
            self.assertEqual(
                {item[0].as_posix() for item in problems},
                {"README.md", marked_path.name},
            )


if __name__ == "__main__":
    unittest.main()
