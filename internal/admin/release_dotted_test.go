package admin

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/mod/semver"
)

func TestDottedPrereleaseMarkerPreservesDisplayAndComparison(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, deploymentVersionMarker), []byte("version=v2.0.3.rc.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	version, present, err := readDeploymentVersionMarker(root)
	if err != nil || !present || version != "v2.0.3.rc.1" {
		t.Fatalf("marker=(%q,%v,%v)", version, present, err)
	}
	if normalizedSemver(version) != "v2.0.3-rc.1" || semver.Compare(normalizedSemver(version), "v2.0.3") >= 0 {
		t.Fatal("RC did not compare below the stable release")
	}
	for _, invalid := range []string{"v2.0.3.rc.01", "v02.0.3.rc.1", "v2.0.3.rc..1", "v2.0.3.4"} {
		if normalizedSemver(invalid) != "" {
			t.Fatalf("accepted invalid version %s", invalid)
		}
	}
}
