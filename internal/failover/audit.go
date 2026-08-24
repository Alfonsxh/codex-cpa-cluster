package failover

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type FileAuditRecorder struct {
	Root  string
	Now   func() time.Time
	Fence WriteFence
	mu    sync.Mutex
}

func (recorder *FileAuditRecorder) Record(
	ctx context.Context,
	action string,
	target string,
	outcome string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if recorder == nil || strings.TrimSpace(recorder.Root) == "" || recorder.Fence == nil {
		return errors.New("audit root and ownership fence are required")
	}
	return recorder.Fence.WithWriteFence(ctx, func() error {
		return recorder.record(ctx, action, target, outcome)
	})
}

func (recorder *FileAuditRecorder) record(
	ctx context.Context,
	action string,
	target string,
	outcome string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := filepath.Abs(recorder.Root)
	if err != nil {
		return fmt.Errorf("resolve audit root: %w", err)
	}
	directory := filepath.Join(filepath.Clean(root), "logs", "admin")
	path := filepath.Join(directory, "audit.jsonl")
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create audit directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("audit path must not be a symbolic link")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect audit path: %w", err)
	}
	now := time.Now
	if recorder.Now != nil {
		now = recorder.Now
	}
	record := struct {
		Timestamp string `json:"timestamp"`
		Action    string `json:"action"`
		Target    string `json:"target"`
		Outcome   string `json:"outcome"`
	}{
		Timestamp: now().UTC().Format("2006-01-02T15:04:05Z"),
		Action:    strings.TrimSpace(action), Target: strings.TrimSpace(target),
		Outcome: strings.TrimSpace(outcome),
	}
	var payload bytes.Buffer
	encoder := json.NewEncoder(&payload)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record); err != nil {
		return fmt.Errorf("encode audit record: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure audit log: %w", err)
	}
	if _, err := file.Write(payload.Bytes()); err != nil {
		_ = file.Close()
		return fmt.Errorf("append audit record: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close audit log: %w", err)
	}
	return nil
}
