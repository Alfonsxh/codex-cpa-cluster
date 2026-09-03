package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultGitHubLatestReleaseURL = "https://api.github.com/repos/Alfonsxh/codex-cpa-cluster/releases/latest"
	githubReleaseBodyLimit        = 1 << 20
	githubReleaseTimeout          = 15 * time.Second
)

type GitHubReleaseCatalog struct {
	client   *http.Client
	endpoint string
}

func NewGitHubReleaseCatalog(client *http.Client) *GitHubReleaseCatalog {
	if client == nil {
		client = &http.Client{Timeout: githubReleaseTimeout}
	}
	return &GitHubReleaseCatalog{client: client, endpoint: defaultGitHubLatestReleaseURL}
}

func (catalog *GitHubReleaseCatalog) LatestRelease(ctx context.Context) (string, error) {
	if catalog == nil || catalog.client == nil || strings.TrimSpace(catalog.endpoint) == "" {
		return "", fmt.Errorf("GitHub Release client is not configured")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, catalog.endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("create GitHub Release request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "codex-cpa-admin")

	response, err := catalog.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("query latest GitHub Release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, githubReleaseBodyLimit))
		return "", fmt.Errorf("query latest GitHub Release: HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, githubReleaseBodyLimit+1))
	if err != nil {
		return "", fmt.Errorf("read latest GitHub Release: %w", err)
	}
	if len(body) > githubReleaseBodyLimit {
		return "", fmt.Errorf("latest GitHub Release response exceeds %d bytes", githubReleaseBodyLimit)
	}
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode latest GitHub Release: %w", err)
	}
	version := strings.TrimSpace(payload.TagName)
	if !strings.HasPrefix(version, "v") || normalizedSemver(version) == "" {
		return "", fmt.Errorf("latest GitHub Release tag is not semantic versioning")
	}
	return version, nil
}
