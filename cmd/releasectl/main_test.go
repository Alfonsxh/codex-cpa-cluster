package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChecksumWritesAtomicPortableLines(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first.txt")
	second := filepath.Join(root, "second.txt")
	output := filepath.Join(root, "SHA256SUMS")
	if err := os.WriteFile(first, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runChecksum([]string{"--output", output, first, second}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runChecksum: %v", err)
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "  first.txt") || !strings.HasSuffix(lines[1], "  second.txt") {
		t.Fatalf("checksum lines = %q", lines)
	}
}

func TestArchiveVerificationRejectsTraversal(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "unsafe.tar.gz")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(file)
	writer := tar.NewWriter(compressed)
	if err := writer.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o600, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := verifyArchive(archive); err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("verifyArchive error = %v", err)
	}
}

func TestArchiveVerificationRejectsRemovedRuntimePaths(t *testing.T) {
	for _, path := range []string{
		"scripts/worker.py",
		"gateway/request_gate.lua",
		"requirements-test.txt",
		"admin/index.html",
		"cmd/docker-read-" + "proxy/main.go",
		"internal/dockerread" + "proxy/server.go",
		"release/Docker" + "file",
		"testdata/v" + "2/fixture.json",
		"v" + "2/Dockerfile",
	} {
		t.Run(path, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "removed-runtime.tar.gz")
			file, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			compressed := gzip.NewWriter(file)
			writer := tar.NewWriter(compressed)
			if err := writer.WriteHeader(&tar.Header{Name: path, Mode: 0o600, Size: 1}); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := compressed.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if err := verifyArchive(archive); err == nil || !strings.Contains(err.Error(), "removed runtime path") {
				t.Fatalf("verifyArchive error = %v", err)
			}
		})
	}
}

func TestArchiveVerificationRejectsGeneratedArtifacts(t *testing.T) {
	for _, path := range []string{
		"frontend/dist/admin/index.html",
		"frontend/node_modules/package/index.js",
		"frontend/coverage/index.html",
		"frontend/playwright-report/index.html",
		"frontend/test-results/.last-run.json",
	} {
		t.Run(path, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "generated-artifact.tar.gz")
			file, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			compressed := gzip.NewWriter(file)
			writer := tar.NewWriter(compressed)
			if err := writer.WriteHeader(&tar.Header{Name: path, Mode: 0o600, Size: 1}); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := compressed.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if err := verifyArchive(archive); err == nil || !strings.Contains(err.Error(), "generated test or build artifact") {
				t.Fatalf("verifyArchive error = %v", err)
			}
		})
	}
}

func TestWebhookScannerAllowsOnlyExplicitFixtureKeys(t *testing.T) {
	if containsWebhookSecret("https://example.com/cgi-bin/webhook/send?key=test-placeholder") {
		t.Fatal("fixture webhook key was rejected")
	}
	secretURL := "https://example.com/cgi-bin/webhook/send?key=" + "real-looking-key"
	if !containsWebhookSecret(secretURL) {
		t.Fatal("non-fixture webhook key was accepted")
	}
}
