package admin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/runtimeops"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/mod/semver"
)

const (
	releaseStatusCacheTTL        = 15 * time.Minute
	defaultReleaseMetadataImage  = "ghcr.io/alfonsxh/codex-cpa-release:latest"
	deploymentVersionMarker      = ".deploy-initialized"
	deploymentVersionMarkerLimit = 4 * 1024
)

var releaseMetadataImagePattern = regexp.MustCompile(
	`^[A-Za-z0-9.-]+(?::[0-9]+)?/[A-Za-z0-9._/-]+:[A-Za-z0-9._-]+$`,
)

var strictReleaseSemverPattern = regexp.MustCompile(
	`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`,
)

type ReleaseCatalog interface {
	PullReleaseMetadata(context.Context, string) (map[string]string, error)
}

type releaseStatusResponse struct {
	Configured     bool   `json:"configured"`
	CurrentVersion string `json:"current_version"`
	Available      bool   `json:"available"`
	Status         string `json:"status,omitempty"`
	LatestVersion  string `json:"latest_version,omitempty"`
	LatestRevision string `json:"latest_revision,omitempty"`
	CheckedAt      int64  `json:"checked_at,omitempty"`
}

type releaseConfigurationState struct {
	currentVersion     string
	metadataImage      string
	updateCheckEnabled bool
	metadataImageValid bool
}

type releaseMetadataStatus struct {
	Status         string
	LatestVersion  string
	LatestRevision string
	CheckedAt      int64
}

func (server *Server) readReleaseStatus(c *gin.Context) {
	payload, err := server.releaseStatus(c.Request.Context(), c.Query("fresh") == "1")
	if err != nil {
		server.internalError(c, "read release status", err)
		return
	}
	c.JSON(http.StatusOK, payload)
}

func (server *Server) releaseStatus(ctx context.Context, force bool) (releaseStatusResponse, error) {
	configuration, err := server.releaseConfiguration(ctx)
	if err != nil {
		return releaseStatusResponse{}, err
	}
	base := releaseStatusResponse{
		Configured:     configuration.updateCheckEnabled,
		CurrentVersion: configuration.currentVersion,
		Available:      false,
	}
	if !configuration.updateCheckEnabled {
		base.Status = "disabled"
		return base, nil
	}
	if !configuration.metadataImageValid || !releaseMetadataImagePattern.MatchString(configuration.metadataImage) {
		base.Status = "invalid_configuration"
		base.CheckedAt = server.now().Unix()
		return base, nil
	}
	if normalizedSemver(configuration.currentVersion) == "" {
		base.CurrentVersion = ""
		base.Status = "current_version_unavailable"
		base.CheckedAt = server.now().Unix()
		return base, nil
	}

	metadata := server.cachedReleaseMetadata(ctx, configuration.metadataImage, force)
	base.Status = metadata.Status
	base.LatestVersion = metadata.LatestVersion
	base.LatestRevision = metadata.LatestRevision
	base.CheckedAt = metadata.CheckedAt
	if metadata.Status == "ok" {
		base.Available = semver.Compare(
			normalizedSemver(metadata.LatestVersion),
			normalizedSemver(configuration.currentVersion),
		) > 0
	}
	return base, nil
}

func (server *Server) cachedReleaseMetadata(
	ctx context.Context,
	metadataImage string,
	force bool,
) releaseMetadataStatus {
	now := server.now()
	server.releaseStatusMu.Lock()
	defer server.releaseStatusMu.Unlock()
	if !force && server.releaseStatusCache != nil && server.releaseStatusCacheKey == metadataImage &&
		now.Before(server.releaseStatusCacheUntil) {
		return *server.releaseStatusCache
	}
	payload := releaseMetadataStatus{Status: "unavailable", CheckedAt: now.Unix()}
	if server.release != nil {
		labels, pullErr := server.release.PullReleaseMetadata(ctx, metadataImage)
		if pullErr == nil && labels["io.codex-cpa.component"] == "release" {
			latestVersion := strings.TrimSpace(labels["org.opencontainers.image.version"])
			if normalizedSemver(latestVersion) != "" {
				payload.Status = "ok"
				payload.LatestVersion = latestVersion
				payload.LatestRevision = boundedReleaseRevision(labels["org.opencontainers.image.revision"])
			} else {
				pullErr = fmt.Errorf("release metadata contains an invalid version")
			}
		} else if pullErr == nil {
			pullErr = fmt.Errorf("release metadata labels are invalid")
		}
		if pullErr != nil {
			server.logger.Warn("release metadata refresh unavailable", zap.String("error", runtimeops.Sanitize(pullErr.Error())))
		}
	}
	server.releaseStatusCache = &payload
	server.releaseStatusCacheKey = metadataImage
	server.releaseStatusCacheUntil = now.Add(releaseStatusCacheTTL)
	return payload
}

func (server *Server) releaseConfiguration(ctx context.Context) (releaseConfigurationState, error) {
	configuration := releaseConfigurationState{
		metadataImage:      defaultReleaseMetadataImage,
		updateCheckEnabled: true,
		metadataImageValid: true,
	}
	currentVersion, markerPresent, markerErr := readDeploymentVersionMarker(server.root)
	if markerErr != nil {
		server.logger.Warn(
			"deployment version marker unavailable",
			zap.String("error", runtimeops.Sanitize(markerErr.Error())),
		)
	} else if markerPresent {
		configuration.currentVersion = currentVersion
	}
	if !markerPresent {
		deployment := make(map[string]any)
		if _, err := server.store.ReadRuntimeState(ctx, "deployment", &deployment); err != nil {
			return releaseConfigurationState{}, fmt.Errorf("read applied deployment: %w", err)
		}
		applied := deployment
		if value, ok := deployment["applied"].(map[string]any); ok {
			applied = value
		} else if _, hasPending := deployment["pending"]; hasPending {
			applied = map[string]any{}
		}
		legacyVersion, _ := applied["version"].(string)
		if normalizedSemver(legacyVersion) != "" {
			configuration.currentVersion = strings.TrimSpace(legacyVersion)
		}
	}

	settings, err := server.store.ReadSettings(ctx)
	if err != nil {
		return releaseConfigurationState{}, fmt.Errorf("read release configuration: %w", err)
	}
	value, configured := settings["delivery.release_metadata_image"]
	if !configured {
		return configuration, nil
	}
	metadataImage, valid := value.(string)
	configuration.metadataImageValid = valid
	configuration.metadataImage = strings.TrimSpace(metadataImage)
	configuration.updateCheckEnabled = !valid || configuration.metadataImage != ""
	return configuration, nil
}

func readDeploymentVersionMarker(root string) (string, bool, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", false, nil
	}
	path := filepath.Join(root, deploymentVersionMarker)
	before, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", true, fmt.Errorf("inspect %s: %w", deploymentVersionMarker, err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return "", true, fmt.Errorf("%s must be a regular file", deploymentVersionMarker)
	}
	if before.Size() > deploymentVersionMarkerLimit {
		return "", true, fmt.Errorf("%s exceeds %d bytes", deploymentVersionMarker, deploymentVersionMarkerLimit)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", true, fmt.Errorf("open %s: %w", deploymentVersionMarker, err)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return "", true, fmt.Errorf("stat opened %s: %w", deploymentVersionMarker, err)
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return "", true, fmt.Errorf("%s changed while it was read", deploymentVersionMarker)
	}
	content, err := io.ReadAll(io.LimitReader(file, deploymentVersionMarkerLimit+1))
	if err != nil {
		return "", true, fmt.Errorf("read %s: %w", deploymentVersionMarker, err)
	}
	if len(content) > deploymentVersionMarkerLimit {
		return "", true, fmt.Errorf("%s exceeds %d bytes", deploymentVersionMarker, deploymentVersionMarkerLimit)
	}
	version := ""
	versionLines := 0
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "version=") {
			return "", true, fmt.Errorf("%s contains an unsupported entry", deploymentVersionMarker)
		}
		versionLines++
		version = strings.TrimSpace(strings.TrimPrefix(line, "version="))
	}
	if versionLines != 1 || normalizedSemver(version) == "" {
		return "", true, fmt.Errorf("%s must contain one valid semantic version", deploymentVersionMarker)
	}
	return version, true, nil
}

func normalizedSemver(value string) string {
	value = strings.TrimSpace(value)
	if !strictReleaseSemverPattern.MatchString(value) {
		return ""
	}
	if !strings.HasPrefix(value, "v") {
		value = "v" + value
	}
	if !semver.IsValid(value) {
		return ""
	}
	return value
}

func boundedReleaseRevision(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 64 {
		return value[:64]
	}
	return value
}
