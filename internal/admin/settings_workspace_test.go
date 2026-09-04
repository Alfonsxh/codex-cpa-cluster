package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
)

func TestSettingsWorkspaceReturnsRealBoundedRedactedRuntimeMetadata(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{
		filepath.Join(root, "state"), filepath.Join(root, "secrets"),
		filepath.Join(root, "logs", "admin"), filepath.Join(root, "backups", "accounts", "older"),
		filepath.Join(root, "backups", "accounts", "latest"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatalf("create workspace fixture %s: %v", directory, err)
		}
	}
	for _, file := range []string{"state/usage.sqlite3"} {
		path := filepath.Join(root, filepath.FromSlash(file))
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatalf("write workspace fixture %s: %v", file, err)
		}
	}
	older := time.Unix(1_000, 0)
	latest := time.Unix(2_000, 0)
	if err := os.Chtimes(filepath.Join(root, "backups", "accounts", "older"), older, older); err != nil {
		t.Fatalf("timestamp older backup: %v", err)
	}
	if err := os.Chtimes(filepath.Join(root, "backups", "accounts", "latest"), latest, latest); err != nil {
		t.Fatalf("timestamp latest backup: %v", err)
	}
	auditPath := filepath.Join(root, "logs", "admin", "audit.jsonl")
	var audit strings.Builder
	audit.WriteString("not-json\n")
	for index := 0; index < 22; index++ {
		record := map[string]any{
			"timestamp": int64(index + 1), "action": fmt.Sprintf("action.%02d", index),
			"target": fmt.Sprintf("cpa_secret-value-%02d", index), "outcome": "accepted",
		}
		if index == 21 {
			record["target"] = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test-plain-secret"
		}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal audit fixture: %v", err)
		}
		audit.Write(raw)
		audit.WriteByte('\n')
	}
	if err := os.WriteFile(auditPath, []byte(audit.String()), 0o600); err != nil {
		t.Fatalf("write audit fixture: %v", err)
	}

	store, err := controlplane.Open(context.Background(), root, controlplane.Options{})
	if err != nil {
		t.Fatalf("open workspace store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.WriteSecret(context.Background(), "cpa_management_key", "test-management-key"); err != nil {
		t.Fatalf("write management key: %v", err)
	}
	server, err := New(Config{Root: root, Store: store})
	if err != nil {
		t.Fatalf("new workspace server: %v", err)
	}
	t.Cleanup(server.Close)

	unauthorized := performAdminRequest(server, http.MethodGet, "/admin/api/settings/workspace", nil, nil, nil)
	assertAdminError(t, unauthorized, http.StatusUnauthorized, "session_missing")
	response := performAdminRequest(server, http.MethodGet, "/admin/api/settings/workspace", nil,
		map[string]string{"X-Management-Key": "test-management-key"}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("workspace status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload settingsWorkspaceResponse
	decodeAdminResponse(t, response, &payload)
	if payload.Backups.Count != 2 || payload.Backups.Latest != "backups/accounts/latest" {
		t.Fatalf("workspace backups = %#v", payload.Backups)
	}
	if len(payload.Storage) != 4 || !payload.Storage[0].Exists || payload.Storage[0].Mode != "600" ||
		!payload.Storage[3].Exists || payload.Storage[3].Path != "logs/admin/audit.jsonl" {
		t.Fatalf("workspace storage = %#v", payload.Storage)
	}
	if len(payload.RecentAudit) != settingsAuditLimit || payload.RecentAudit[0].Timestamp != 22 ||
		payload.RecentAudit[len(payload.RecentAudit)-1].Timestamp != 3 {
		t.Fatalf("workspace audit ordering/limit = %#v", payload.RecentAudit)
	}
	body := response.Body.String()
	if strings.Contains(body, "test-plain-secret") || !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("workspace audit was not redacted: %s", body)
	}
}

func TestSettingsWorkspaceFailsClosedWithoutConfiguredRoot(t *testing.T) {
	server, _ := newTestAdmin(t)
	response := performAdminRequest(server, http.MethodGet, "/admin/api/settings/workspace", nil,
		map[string]string{"X-Management-Key": "test-management-key"}, nil)
	assertAdminError(t, response, http.StatusServiceUnavailable, "settings_workspace_unavailable")
}
