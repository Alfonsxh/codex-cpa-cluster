package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubReleaseCatalogReadsOnlyLatestReleaseTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.Header.Get("Accept") != "application/vnd.github+json" ||
			request.Header.Get("User-Agent") != "codex-cpa-admin" {
			t.Fatalf("unexpected GitHub Release request: method=%s headers=%v", request.Method, request.Header)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"tag_name":"v2.0.0-rc.30","target_commitish":"ignored"}`))
	}))
	t.Cleanup(server.Close)
	catalog := &GitHubReleaseCatalog{client: server.Client(), endpoint: server.URL}
	version, err := catalog.LatestRelease(context.Background())
	if err != nil || version != "v2.0.0-rc.30" {
		t.Fatalf("latest GitHub Release = %q, %v", version, err)
	}
}

func TestGitHubReleaseCatalogRejectsInvalidResponses(t *testing.T) {
	for name, testCase := range map[string]struct {
		status   int
		body     string
		expected string
	}{
		"http error":      {http.StatusNotFound, `{}`, "HTTP 404"},
		"invalid json":    {http.StatusOK, `{`, "decode latest GitHub Release"},
		"invalid version": {http.StatusOK, `{"tag_name":"main"}`, "not semantic versioning"},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(testCase.status)
				_, _ = writer.Write([]byte(testCase.body))
			}))
			t.Cleanup(server.Close)
			catalog := &GitHubReleaseCatalog{client: server.Client(), endpoint: server.URL}
			if _, err := catalog.LatestRelease(context.Background()); err == nil || !strings.Contains(err.Error(), testCase.expected) {
				t.Fatalf("LatestRelease error = %v, want %q", err, testCase.expected)
			}
		})
	}
}
