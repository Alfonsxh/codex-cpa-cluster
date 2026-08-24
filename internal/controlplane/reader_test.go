package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenReadOnlyRequiresExistingDatabaseWithoutCreatingState(t *testing.T) {
	root := t.TempDir()
	if _, err := OpenReadOnly(context.Background(), root); err == nil {
		t.Fatal("OpenReadOnly succeeded without an existing database")
	}
	if _, err := os.Stat(filepath.Join(root, "state")); !os.IsNotExist(err) {
		t.Fatalf("read-only open created state directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "secrets")); !os.IsNotExist(err) {
		t.Fatalf("read-only open created secrets directory: %v", err)
	}
}

func TestOpenReadOnlyReadsRuntimeState(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writer, err := Open(ctx, root, Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := writer.WriteRuntimeState(ctx, "health-test", map[string]any{"heartbeat_at": 123}); err != nil {
		t.Fatalf("WriteRuntimeState: %v", err)
	}
	settings, err := writer.ReadSettings(ctx)
	if err != nil {
		t.Fatalf("ReadSettings writer: %v", err)
	}
	settings["notification.enabled"] = true
	if err := writer.WriteSettings(ctx, settings); err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	reader, err := OpenReadOnly(ctx, root)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer reader.Close()
	var state struct {
		HeartbeatAt int64 `json:"heartbeat_at"`
	}
	found, err := reader.ReadRuntimeState(ctx, "health-test", &state)
	if err != nil {
		t.Fatalf("ReadRuntimeState: %v", err)
	}
	if !found || state.HeartbeatAt != 123 {
		t.Fatalf("runtime state = (%v, %#v)", found, state)
	}
	settings, err = reader.ReadSettings(ctx)
	if err != nil {
		t.Fatalf("ReadSettings reader: %v", err)
	}
	if enabled, _ := settings["notification.enabled"].(bool); !enabled {
		t.Fatalf("notification.enabled = %#v", settings["notification.enabled"])
	}
}
