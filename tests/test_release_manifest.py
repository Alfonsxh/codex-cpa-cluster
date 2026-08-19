import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

from scripts import release_manifest

ROOT = Path(__file__).parents[1]


class ReleaseManifestTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        (self.root / ".dockerignore").write_text("state\n", encoding="utf-8")
        for directory in ("admin", "dashboard", "edge", "gateway", "portal", "scripts", "web"):
            (self.root / directory).mkdir()
            (self.root / directory / "source.txt").write_text(
                directory + "\n", encoding="utf-8"
            )
        (self.root / "admin/static").mkdir()
        (self.root / "admin/static/app.js").write_text(
            "admin frontend\n", encoding="utf-8"
        )
        for filename in (
            "Dockerfile", "gateway_state.lua", "nginx.conf", "request_gate.lua"
        ):
            (self.root / "gateway" / filename).write_text(
                filename + "\n", encoding="utf-8"
            )

    def tearDown(self):
        self.temporary.cleanup()

    def test_component_digests_only_change_for_owned_inputs(self):
        original = release_manifest.build_manifest(self.root)

        admin_source = self.root / "admin/source.txt"
        admin_source.write_text("admin changed\n", encoding="utf-8")
        admin_changed = release_manifest.build_manifest(self.root)

        self.assertNotEqual(
            original["components"]["admin"]["source_sha256"],
            admin_changed["components"]["admin"]["source_sha256"],
        )
        self.assertEqual(
            original["components"]["gateway"]["source_sha256"],
            admin_changed["components"]["gateway"]["source_sha256"],
        )

        portal_source = self.root / "portal/source.txt"
        portal_source.write_text("web portal changed\n", encoding="utf-8")
        portal_changed = release_manifest.build_manifest(self.root)
        self.assertEqual(
            admin_changed["components"]["admin"]["source_sha256"],
            portal_changed["components"]["admin"]["source_sha256"],
        )
        self.assertEqual(
            admin_changed["components"]["gateway"]["source_sha256"],
            portal_changed["components"]["gateway"]["source_sha256"],
        )
        self.assertNotEqual(
            admin_changed["components"]["web"]["source_sha256"],
            portal_changed["components"]["web"]["source_sha256"],
        )

    def test_admin_static_change_invalidates_admin_and_web_images(self):
        original = release_manifest.build_manifest(self.root)
        (self.root / "admin/static/app.js").write_text(
            "admin frontend changed\n", encoding="utf-8"
        )
        changed = release_manifest.build_manifest(self.root)

        for component in ("admin", "web"):
            self.assertNotEqual(
                original["components"][component]["source_sha256"],
                changed["components"][component]["source_sha256"],
            )
        for component in ("gateway", "edge"):
            self.assertEqual(
                original["components"][component]["source_sha256"],
                changed["components"][component]["source_sha256"],
            )

    def test_digest_ignores_mtime_but_tracks_file_mode(self):
        source = self.root / "gateway/request_gate.lua"
        original = release_manifest.build_manifest(self.root)
        os.utime(source, (1, 1))
        touched = release_manifest.build_manifest(self.root)
        self.assertEqual(original, touched)

        source.chmod(0o755)
        executable = release_manifest.build_manifest(self.root)
        self.assertNotEqual(
            touched["components"]["gateway"]["source_sha256"],
            executable["components"]["gateway"]["source_sha256"],
        )

    def test_verify_rejects_modified_release_input(self):
        manifest_path = self.root / "release-manifest.json"
        release_manifest.write_manifest(self.root, manifest_path)
        release_manifest.verify_manifest(self.root, manifest_path)

        (self.root / "gateway/request_gate.lua").write_text("tampered\n", encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "gateway"):
            release_manifest.verify_manifest(self.root, manifest_path)

    def test_manifest_is_versioned_and_contains_no_file_contents(self):
        manifest_path = self.root / "release-manifest.json"
        release_manifest.write_manifest(self.root, manifest_path)
        payload = json.loads(manifest_path.read_text(encoding="utf-8"))

        self.assertEqual(payload["version"], 3)
        self.assertEqual(set(payload["components"]), {"admin", "web", "gateway", "edge"})
        self.assertNotIn("admin changed", manifest_path.read_text(encoding="utf-8"))

    def test_gateway_apply_selection_is_fail_closed(self):
        deploy = (ROOT / "scripts/deploy-release.sh").read_text(encoding="utf-8")
        start = deploy.index("gateway_apply_required() {")
        end = deploy.index("\nGATEWAY_APPLY_REQUIRED=false", start)
        helper = deploy[start:end]
        assertions = """
gateway_apply_required gateway:same gateway:same false running && exit 11
gateway_apply_required gateway:old gateway:new false running || exit 12
gateway_apply_required gateway:same gateway:same true running || exit 13
gateway_apply_required gateway:same gateway:same false "" || exit 14
exit 0
"""
        subprocess.run(
            ["sh"],
            input=helper + "\n" + assertions,
            text=True,
            check=True,
        )

    def test_edge_apply_selection_is_fail_closed(self):
        deploy = (ROOT / "scripts/deploy-release.sh").read_text(encoding="utf-8")
        start = deploy.index("edge_apply_required() {")
        end = deploy.index("\nGATEWAY_APPLY_REQUIRED=false", start)
        helper = deploy[start:end]
        assertions = """
edge_apply_required edge:same edge:same false running && exit 11
edge_apply_required edge:old edge:new false running || exit 12
edge_apply_required edge:same edge:same true running || exit 13
edge_apply_required edge:same edge:same false "" || exit 14
exit 0
"""
        subprocess.run(
            ["sh"],
            input=helper + "\n" + assertions,
            text=True,
            check=True,
        )

    def test_every_runtime_boundary_has_an_independent_digest(self):
        original = release_manifest.build_manifest(self.root)
        for component, source in (
            ("admin", self.root / "scripts/source.txt"),
            ("web", self.root / "web/source.txt"),
            ("gateway", self.root / "gateway/request_gate.lua"),
            ("edge", self.root / "edge/source.txt"),
        ):
            previous = source.read_text(encoding="utf-8")
            source.write_text(previous + "changed\n", encoding="utf-8")
            changed = release_manifest.build_manifest(self.root)
            self.assertNotEqual(
                original["components"][component]["source_sha256"],
                changed["components"][component]["source_sha256"],
            )
            for other in set(original["components"]) - {component}:
                self.assertEqual(
                    original["components"][other]["source_sha256"],
                    changed["components"][other]["source_sha256"],
                )
            source.write_text(previous, encoding="utf-8")


if __name__ == "__main__":
    unittest.main()
