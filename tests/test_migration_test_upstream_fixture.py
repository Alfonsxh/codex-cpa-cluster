import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


class MigrationTestUpstreamFixtureTests(unittest.TestCase):
    def test_fixture_uses_cpa_image_and_compose_visible_labels(self):
        source = (ROOT / "scripts" / "migration-test-upstream-fixture.sh").read_text(
            encoding="utf-8"
        )

        self.assertIn(
            "FIXTURE_IMAGE=${MIGRATION_FIXTURE_IMAGE:-eceasy/cli-proxy-api:latest}",
            source,
        )
        self.assertIn("--label com.docker.compose.oneoff=False", source)
        self.assertIn("--entrypoint /usr/local/bin/cpa-test-upstream", source)
        self.assertIn("docker network disconnect --force", source)
        self.assertIn('docker network disconnect bridge "$fixture_name"', source)
        self.assertIn("docker network connect --alias", source)
        self.assertNotIn("docker rename", source)
        self.assertNotIn("docker stop --time 30", source)
        run_block = source[source.index("    docker run -d \\") :]
        self.assertLess(
            run_block.index("--entrypoint /usr/local/bin/cpa-test-upstream"),
            run_block.index('"$FIXTURE_IMAGE"'),
        )
        self.assertIn('archive_file "$MANIFEST"', source)
        self.assertIn('archive_file "$FIXTURE_LIST"', source)
        self.assertIn('archive_file "$EVIDENCE"', source)

    def test_remote_data_plane_suite_always_restores_fixture(self):
        source = (
            ROOT / "scripts" / "migration-data-plane-suite-remote.sh"
        ).read_text(encoding="utf-8")

        self.assertIn("trap cleanup EXIT HUP INT TERM", source)
        self.assertIn('"$FIXTURE_SCRIPT" start', source)
        self.assertGreaterEqual(source.count('"$FIXTURE_SCRIPT" restore'), 2)
        self.assertIn("fixture_active=true", source)
        self.assertIn("fixture_active=false", source)
        self.assertIn("--confirm-test-data-request", source)
        self.assertIn('MIGRATION_ROUTE_OUTPUT="$ROUTE_OUTPUT"', source)
        self.assertIn("sed -n '1p' \"$TEST_KEY_FILE\" | python3", source)
        self.assertNotIn("/home/AI/CLIProxyAPI", source)

    def test_remote_fault_suite_is_isolated_and_restores_every_mutation(self):
        source = (ROOT / "scripts" / "migration-fault-suite-remote.sh").read_text(
            encoding="utf-8"
        )

        self.assertIn("production roots are forbidden", source)
        self.assertIn("trap cleanup EXIT HUP INT TERM", source)
        self.assertIn('"$FIXTURE_SCRIPT" start', source)
        self.assertGreaterEqual(source.count('"$FIXTURE_SCRIPT" restore'), 2)
        self.assertIn("restore_snapshots", source)
        self.assertIn('cp -p "$V1_AUTH_BACKUP" "$V1_AUTH_SNAPSHOT"', source)
        self.assertIn('cp -p "$V2_AUTH_BACKUP" "$V2_AUTH_SNAPSHOT"', source)
        self.assertIn("io.codex-cpa.migration-disposable", source)
        self.assertIn("run_compare upstream-unavailable", source)
        self.assertIn("run_compare auth-unavailable", source)
        self.assertNotIn("/home/AI/CLIProxyAPI/state", source)


if __name__ == "__main__":
    unittest.main()
