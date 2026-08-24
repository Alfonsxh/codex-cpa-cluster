package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/gateway"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/usage"
)

func TestSnapshotPublisherMatchesGatewayContractAndReusesGeneration(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	publisher := &SnapshotPublisher{Root: root, Now: func() time.Time { return now }, SnapshotGID: os.Getgid()}
	limit := int64(1000)
	quotas := map[string]usage.WeeklyQuota{
		"bob@example.com": {
			WeekStartAt: 1_799_625_600, WeekEndAt: 1_800_230_400,
			LimitTokens: nil, UsedTokens: 0, RawUsedTokens: 0, WeightedRawUsedTokens: 0,
		},
		"alice@example.com": {
			WeekStartAt: 1_799_625_600, WeekEndAt: 1_800_230_400,
			LimitTokens: &limit, UsedTokens: 240, RawUsedTokens: 120, WeightedRawUsedTokens: 240,
		},
	}
	first, err := publisher.PublishQuotaSnapshot(context.Background(), quotas)
	if err != nil {
		t.Fatalf("PublishQuotaSnapshot: %v", err)
	}
	if !first.Changed || first.Records != 2 || len(first.Generation) != 32 {
		t.Fatalf("first snapshot = %#v", first)
	}
	path := filepath.Join(root, quotaSnapshotRelativePath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read quota snapshot: %v", err)
	}
	parsed, err := gateway.ParseQuotaSnapshot(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ParseQuotaSnapshot: %v", err)
	}
	if parsed.Records[0].UserEmail != "alice@example.com" || parsed.Records[1].UserEmail != "bob@example.com" ||
		parsed.Records[1].LimitTokens != -1 {
		t.Fatalf("parsed records = %#v", parsed.Records)
	}
	var envelope struct {
		ContentSHA256 string `json:"content_sha256"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode quota envelope: %v", err)
	}
	if envelope.ContentSHA256 != "bf422dbf870cf543922a43a4294238d82f72fbf35bd2547e127712498328593b" {
		t.Fatalf("content digest = %s", envelope.ContentSHA256)
	}

	now = now.Add(time.Minute)
	second, err := publisher.PublishQuotaSnapshot(context.Background(), quotas)
	if err != nil {
		t.Fatalf("republish quota snapshot: %v", err)
	}
	if second.Changed || second.Generation != first.Generation {
		t.Fatalf("second snapshot = %#v", second)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat quota snapshot: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("quota snapshot mode = %o", info.Mode().Perm())
	}
}

func TestSnapshotPublisherPreservesLastSuccessOnFailure(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	publisher := &SnapshotPublisher{Root: root, Now: func() time.Time { return now }, SnapshotGID: os.Getgid()}
	first, err := publisher.PublishQuotaHeartbeat(context.Background(), true, "", 1, 1)
	if err != nil {
		t.Fatalf("publish healthy heartbeat: %v", err)
	}
	if first.LastSuccessAt != now.Unix() || first.StaleAfterSeconds != 5 || first.FailOpenAfterSeconds != 30 {
		t.Fatalf("healthy heartbeat = %#v", first)
	}
	now = now.Add(10 * time.Second)
	failureText := strings.Repeat("错误", 600)
	second, err := publisher.PublishQuotaHeartbeat(context.Background(), false, failureText, 15, 300)
	if err != nil {
		t.Fatalf("publish failed heartbeat: %v", err)
	}
	if second.OK || second.LastSuccessAt != first.LastSuccessAt || len([]rune(second.Error)) != 500 || !json.Valid(rawJSON(second.Error)) {
		t.Fatalf("failed heartbeat = %#v", second)
	}
	raw, err := os.ReadFile(filepath.Join(root, quotaHeartbeatRelativePath))
	if err != nil {
		t.Fatalf("read quota heartbeat: %v", err)
	}
	if _, err := gateway.ParseQuotaHeartbeat(bytes.NewReader(raw)); err != nil {
		t.Fatalf("ParseQuotaHeartbeat: %v", err)
	}
}

func rawJSON(value string) []byte {
	raw, _ := json.Marshal(value)
	return raw
}
