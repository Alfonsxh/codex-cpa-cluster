package admin

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/runtimeops"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/mod/semver"
)

const releaseStatusCacheTTL = 15 * time.Minute

var releaseMetadataImagePattern = regexp.MustCompile(
	`^[A-Za-z0-9.-]+(?::[0-9]+)?/[A-Za-z0-9._/-]+:[A-Za-z0-9._-]+$`,
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

func (server *Server) readReleaseStatus(c *gin.Context) {
	payload, err := server.releaseStatus(c.Request.Context(), c.Query("fresh") == "1")
	if err != nil {
		server.internalError(c, "read release status", err)
		return
	}
	c.JSON(http.StatusOK, payload)
}

func (server *Server) releaseStatus(ctx context.Context, force bool) (releaseStatusResponse, error) {
	currentVersion, metadataImage, err := server.releaseConfiguration(ctx)
	if err != nil {
		return releaseStatusResponse{}, err
	}
	if currentVersion == "" || metadataImage == "" {
		return releaseStatusResponse{Configured: false, CurrentVersion: currentVersion, Available: false}, nil
	}
	if !releaseMetadataImagePattern.MatchString(metadataImage) {
		return releaseStatusResponse{
			Configured: true, CurrentVersion: currentVersion, Available: false,
			Status: "invalid_configuration",
		}, nil
	}
	cacheKey := currentVersion + "\x00" + metadataImage
	now := server.now()
	server.releaseStatusMu.Lock()
	defer server.releaseStatusMu.Unlock()
	if !force && server.releaseStatusCache != nil && server.releaseStatusCacheKey == cacheKey &&
		now.Before(server.releaseStatusCacheUntil) {
		return *server.releaseStatusCache, nil
	}
	payload := releaseStatusResponse{
		Configured: true, CurrentVersion: currentVersion, Available: false,
		Status: "unavailable", CheckedAt: now.Unix(),
	}
	if server.release != nil {
		labels, pullErr := server.release.PullReleaseMetadata(ctx, metadataImage)
		if pullErr == nil && labels["io.codex-cpa.component"] == "release" {
			latestVersion := strings.TrimSpace(labels["org.opencontainers.image.version"])
			currentSemver := normalizedSemver(currentVersion)
			latestSemver := normalizedSemver(latestVersion)
			if currentSemver != "" && latestSemver != "" {
				payload.Status = "ok"
				payload.LatestVersion = latestVersion
				payload.LatestRevision = boundedReleaseRevision(labels["org.opencontainers.image.revision"])
				payload.Available = semver.Compare(latestSemver, currentSemver) > 0
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
	server.releaseStatusCacheKey = cacheKey
	server.releaseStatusCacheUntil = now.Add(releaseStatusCacheTTL)
	return payload, nil
}

func (server *Server) releaseConfiguration(ctx context.Context) (string, string, error) {
	deployment := make(map[string]any)
	if _, err := server.store.ReadRuntimeState(ctx, "deployment", &deployment); err != nil {
		return "", "", fmt.Errorf("read applied deployment: %w", err)
	}
	applied := deployment
	if value, ok := deployment["applied"].(map[string]any); ok {
		applied = value
	} else if _, hasPending := deployment["pending"]; hasPending {
		applied = map[string]any{}
	}
	currentVersion, _ := applied["version"].(string)
	settings, err := server.store.ReadSettings(ctx)
	if err != nil {
		return "", "", fmt.Errorf("read release configuration: %w", err)
	}
	metadataImage, _ := settings["delivery.release_metadata_image"].(string)
	return strings.TrimSpace(currentVersion), strings.TrimSpace(metadataImage), nil
}

func normalizedSemver(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
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
