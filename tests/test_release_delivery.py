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
                }.issubset(member_names)
            )

    def test_production_compose_has_no_build_context_and_dev_override_has_it(self):
        production = (ROOT / "docker-compose.yml").read_text(encoding="utf-8")
        development = (ROOT / "docker-compose.dev.yml").read_text(encoding="utf-8")

        self.assertNotIn("    build:", production)
        self.assertIn("    build:", development)
        for component in ("admin/Dockerfile", "web/Dockerfile", "gateway/Dockerfile", "edge/Dockerfile"):
            self.assertIn(component, development)

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
