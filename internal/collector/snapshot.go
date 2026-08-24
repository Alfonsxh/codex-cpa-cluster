package collector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/gateway"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/usage"
	"github.com/google/renameio/v2"
	"github.com/google/uuid"
)

const (
	quotaSnapshotRelativePath  = "state/gateway/quota-snapshot.json"
	quotaHeartbeatRelativePath = "state/gateway/quota-heartbeat.json"
	defaultQuotaSnapshotGID    = 65534
)

type SnapshotPublisher struct {
	Root        string
	Now         func() time.Time
	SnapshotGID int
}

type SnapshotResult struct {
	Generation string `json:"generation"`
	Records    int    `json:"records"`
	Changed    bool   `json:"changed"`
}

type HeartbeatPayload struct {
	Version              int    `json:"version"`
	UpdatedAt            int64  `json:"updated_at"`
	OK                   bool   `json:"ok"`
	Error                string `json:"error"`
	StaleAfterSeconds    int64  `json:"stale_after_seconds"`
	LastSuccessAt        int64  `json:"last_success_at"`
	FailOpenAfterSeconds int64  `json:"fail_open_after_seconds"`
}

func (publisher *SnapshotPublisher) PublishQuotaSnapshot(
	ctx context.Context,
	quotas map[string]usage.WeeklyQuota,
) (SnapshotResult, error) {
	if err := ctx.Err(); err != nil {
		return SnapshotResult{}, err
	}
	path, err := publisher.path(quotaSnapshotRelativePath)
	if err != nil {
		return SnapshotResult{}, err
	}
	users := make([]string, 0, len(quotas))
	for user := range quotas {
		users = append(users, user)
	}
	sort.Strings(users)
	records := make([]gateway.QuotaRecord, 0, len(users))
	semanticRecords := make([]map[string]any, 0, len(users))
	for _, user := range users {
		quota := quotas[user]
		limit := int64(-1)
		if quota.LimitTokens != nil {
			limit = *quota.LimitTokens
		}
		record := gateway.QuotaRecord{
			UserEmail: user, WeekStartAt: quota.WeekStartAt, WeekEndAt: quota.WeekEndAt,
			LimitTokens: limit, UsedTokens: quota.UsedTokens,
			RawUsedTokens: quota.RawUsedTokens, WeightedRawUsedTokens: quota.WeightedRawUsedTokens,
			QuotaUnit: "weighted_tokens",
		}
		records = append(records, record)
		semanticRecords = append(semanticRecords, map[string]any{
			"user_email": user, "week_start_at": record.WeekStartAt, "week_end_at": record.WeekEndAt,
			"limit_tokens": record.LimitTokens, "used_tokens": record.UsedTokens,
			"raw_used_tokens":          record.RawUsedTokens,
			"weighted_raw_used_tokens": record.WeightedRawUsedTokens,
			"quota_unit":               record.QuotaUnit,
		})
	}
	semantic, err := json.Marshal(semanticRecords)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("encode quota snapshot digest input: %w", err)
	}
	digestBytes := sha256.Sum256(semantic)
	digest := hex.EncodeToString(digestBytes[:])
	previous := struct {
		Generation    string `json:"generation"`
		ContentSHA256 string `json:"content_sha256"`
	}{}
	if raw, readErr := os.ReadFile(path); readErr == nil {
		_ = json.Unmarshal(raw, &previous)
	}
	if previous.ContentSHA256 == digest && previous.Generation != "" {
		if err := publisher.secureExisting(path); err != nil {
			return SnapshotResult{}, err
		}
		return SnapshotResult{Generation: previous.Generation, Records: len(records), Changed: false}, nil
	}
	generation := strings.ReplaceAll(uuid.NewString(), "-", "")
	now := time.Now
	if publisher != nil && publisher.Now != nil {
		now = publisher.Now
	}
	payload := struct {
		Version       int                   `json:"version"`
		Generation    string                `json:"generation"`
		GeneratedAt   int64                 `json:"generated_at"`
		ContentSHA256 string                `json:"content_sha256"`
		Records       []gateway.QuotaRecord `json:"records"`
	}{
		Version: 1, Generation: generation, GeneratedAt: now().Unix(),
		ContentSHA256: digest, Records: records,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("encode quota snapshot: %w", err)
	}
	raw = append(raw, '\n')
	if _, err := gateway.ParseQuotaSnapshot(bytes.NewReader(raw)); err != nil {
		return SnapshotResult{}, fmt.Errorf("validate quota snapshot: %w", err)
	}
	if err := publisher.write(path, raw); err != nil {
		return SnapshotResult{}, err
	}
	return SnapshotResult{Generation: generation, Records: len(records), Changed: true}, nil
}

func (publisher *SnapshotPublisher) PublishQuotaHeartbeat(
	ctx context.Context,
	ok bool,
	errorText string,
	staleAfterSeconds int64,
	failOpenAfterSeconds int64,
) (HeartbeatPayload, error) {
	if err := ctx.Err(); err != nil {
		return HeartbeatPayload{}, err
	}
	path, err := publisher.path(quotaHeartbeatRelativePath)
	if err != nil {
		return HeartbeatPayload{}, err
	}
	now := time.Now
	if publisher != nil && publisher.Now != nil {
		now = publisher.Now
	}
	updatedAt := now().Unix()
	lastSuccessAt := int64(0)
	if previousRaw, readErr := os.ReadFile(path); readErr == nil {
		previous := HeartbeatPayload{}
		if json.Unmarshal(previousRaw, &previous) == nil {
			lastSuccessAt = max(previous.LastSuccessAt, 0)
		}
	}
	if ok {
		lastSuccessAt = updatedAt
	}
	errorText = truncateText(errorText, 500)
	payload := HeartbeatPayload{
		Version: 1, UpdatedAt: updatedAt, OK: ok, Error: errorText,
		StaleAfterSeconds: max(staleAfterSeconds, 5), LastSuccessAt: lastSuccessAt,
		FailOpenAfterSeconds: max(failOpenAfterSeconds, 30),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return HeartbeatPayload{}, fmt.Errorf("encode quota heartbeat: %w", err)
	}
	raw = append(raw, '\n')
	if _, err := gateway.ParseQuotaHeartbeat(bytes.NewReader(raw)); err != nil {
		return HeartbeatPayload{}, fmt.Errorf("validate quota heartbeat: %w", err)
	}
	if err := publisher.write(path, raw); err != nil {
		return HeartbeatPayload{}, err
	}
	return payload, nil
}

func (publisher *SnapshotPublisher) path(relative string) (string, error) {
	if publisher == nil || strings.TrimSpace(publisher.Root) == "" {
		return "", errors.New("quota snapshot root is required")
	}
	root, err := filepath.Abs(publisher.Root)
	if err != nil {
		return "", fmt.Errorf("resolve quota snapshot root: %w", err)
	}
	return filepath.Join(root, relative), nil
}

func (publisher *SnapshotPublisher) write(path string, payload []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("create quota snapshot directory: %w", err)
	}
	if err := os.Chmod(directory, 0o750); err != nil {
		return fmt.Errorf("secure quota snapshot directory: %w", err)
	}
	if err := publisher.chown(directory); err != nil {
		return err
	}
	if err := renameio.WriteFile(path, payload, 0o640); err != nil {
		return fmt.Errorf("publish quota snapshot atomically: %w", err)
	}
	return publisher.secureExisting(path)
}

func (publisher *SnapshotPublisher) secureExisting(path string) error {
	if err := os.Chmod(path, 0o640); err != nil {
		return fmt.Errorf("secure quota snapshot: %w", err)
	}
	return publisher.chown(path)
}

func (publisher *SnapshotPublisher) chown(path string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	gid := publisher.SnapshotGID
	if gid <= 0 {
		gid = defaultQuotaSnapshotGID
	}
	if err := os.Chown(path, -1, gid); err != nil {
		return fmt.Errorf("assign quota snapshot group: %w", err)
	}
	return nil
}

func truncateText(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
