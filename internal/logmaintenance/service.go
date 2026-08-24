package logmaintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/renameio/v2"
	"golang.org/x/sys/unix"
)

const (
	RuntimeStateName = "log_maintenance"
	megabyte         = int64(1024 * 1024)
	maximumBackups   = 100
)

var DefaultTargets = []string{
	"logs/gateway/access.tsv",
	"logs/gateway/admin-access.log",
	"logs/gateway/error.log",
	"logs/gateway/edge-error.log",
	"logs/admin/audit.jsonl",
}

type RuntimeStateStore interface {
	ReadRuntimeState(context.Context, string, any) (bool, error)
	WriteRuntimeState(context.Context, string, any) error
}

type WriteFence interface {
	WithWriteFence(context.Context, func() error) error
}

type Config struct {
	Root          string
	Store         RuntimeStateStore
	Fence         WriteFence
	MaxFileSizeMB int64
	Backups       int
	Targets       []string
	Now           func() time.Time
}

type State struct {
	HeartbeatAt   int64    `json:"heartbeat_at"`
	LastError     string   `json:"last_error"`
	Rotations     int64    `json:"rotations"`
	LastRotated   []string `json:"last_rotated"`
	MaxFileSizeMB int64    `json:"max_file_size_mb"`
	Backups       int      `json:"backups"`
}

type Service struct {
	root          string
	store         RuntimeStateStore
	fence         WriteFence
	maxFileSizeMB int64
	maxFileBytes  int64
	backups       int
	targets       []string
	now           func() time.Time
}

func New(config Config) (*Service, error) {
	if config.Store == nil {
		return nil, errors.New("log maintenance requires a runtime state store")
	}
	if config.Fence == nil {
		return nil, errors.New("log maintenance requires an ownership fence")
	}
	if strings.TrimSpace(config.Root) == "" {
		return nil, errors.New("log maintenance root is required")
	}
	root, err := filepath.Abs(config.Root)
	if err != nil {
		return nil, fmt.Errorf("resolve log maintenance root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve existing log maintenance root: %w", err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("inspect log maintenance root: %w", err)
	}
	if !rootInfo.IsDir() {
		return nil, errors.New("log maintenance root must be a directory")
	}
	if config.MaxFileSizeMB <= 0 || config.MaxFileSizeMB > math.MaxInt64/megabyte {
		return nil, errors.New("log maximum file size must be a positive number of MiB")
	}
	if config.Backups <= 0 || config.Backups > maximumBackups {
		return nil, fmt.Errorf("log backup count must be between 1 and %d", maximumBackups)
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	targets := config.Targets
	if len(targets) == 0 {
		targets = DefaultTargets
	}
	validated := make([]string, 0, len(targets))
	for _, target := range targets {
		cleaned, err := validateRelativeTarget(target)
		if err != nil {
			return nil, err
		}
		validated = append(validated, cleaned)
	}
	return &Service{
		root: root, store: config.Store, fence: config.Fence,
		maxFileSizeMB: config.MaxFileSizeMB,
		maxFileBytes:  config.MaxFileSizeMB * megabyte,
		backups:       config.Backups, targets: validated, now: config.Now,
	}, nil
}

func (service *Service) RunOnce(ctx context.Context) (State, error) {
	previous, _, err := ReadState(ctx, service.store)
	if err != nil {
		return State{}, err
	}
	rotated := make([]string, 0)
	rotationErrors := make([]string, 0)
	for _, relative := range service.targets {
		if err := ctx.Err(); err != nil {
			return State{}, err
		}
		path, exists, err := service.targetPath(relative)
		if err != nil {
			rotationErrors = append(rotationErrors, fmt.Sprintf("%s: %v", relative, err))
			continue
		}
		if !exists {
			continue
		}
		changed := false
		var rotateError error
		fenceError := service.fence.WithWriteFence(ctx, func() error {
			changed, rotateError = RotateFile(path, service.maxFileBytes, service.backups)
			return rotateError
		})
		if fenceError != nil && rotateError == nil {
			return State{}, fenceError
		}
		if rotateError != nil {
			rotationErrors = append(rotationErrors, fmt.Sprintf("%s: %v", relative, rotateError))
			continue
		}
		if changed {
			rotated = append(rotated, relative)
		}
	}
	state := State{
		HeartbeatAt: service.now().Unix(), LastError: truncateRunes(strings.Join(rotationErrors, "; "), 500),
		Rotations: previous.Rotations + int64(len(rotated)), LastRotated: rotated,
		MaxFileSizeMB: service.maxFileSizeMB, Backups: service.backups,
	}
	if err := service.store.WriteRuntimeState(ctx, RuntimeStateName, state); err != nil {
		return state, fmt.Errorf("write log maintenance runtime state: %w", err)
	}
	return state, nil
}

func ReadState(ctx context.Context, store interface {
	ReadRuntimeState(context.Context, string, any) (bool, error)
}) (State, bool, error) {
	var raw json.RawMessage
	found, err := store.ReadRuntimeState(ctx, RuntimeStateName, &raw)
	if err != nil {
		return State{}, false, fmt.Errorf("read log maintenance runtime state: %w", err)
	}
	if !found {
		return State{LastRotated: []string{}}, false, nil
	}
	var state State
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &state) != nil {
		return State{LastRotated: []string{}}, true, nil
	}
	if state.LastRotated == nil {
		state.LastRotated = []string{}
	}
	return state, true, nil
}

func Healthy(state State, found bool, now time.Time, maxAge time.Duration) bool {
	if !found || maxAge <= 0 || state.HeartbeatAt <= 0 || state.LastError != "" {
		return false
	}
	age := now.Unix() - state.HeartbeatAt
	return age >= 0 && age <= int64(maxAge/time.Second)
}

func (service *Service) targetPath(relative string) (string, bool, error) {
	parts := strings.Split(filepath.FromSlash(relative), string(filepath.Separator))
	current := service.root
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return current, false, nil
		}
		if err != nil {
			return current, false, fmt.Errorf("inspect log target: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return current, false, errors.New("refusing symlink in log target path")
		}
	}
	info, err := os.Lstat(current)
	if err != nil {
		return current, false, fmt.Errorf("inspect log target: %w", err)
	}
	if !info.Mode().IsRegular() {
		return current, false, errors.New("refusing non-regular log target")
	}
	return current, true, nil
}

func validateRelativeTarget(target string) (string, error) {
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(target))))
	if cleaned == "." || filepath.IsAbs(filepath.FromSlash(cleaned)) || cleaned == "logs" ||
		!strings.HasPrefix(cleaned, "logs/") || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("invalid log target %q", target)
	}
	return cleaned, nil
}

func RotateFile(path string, maxBytes int64, backups int) (bool, error) {
	if maxBytes <= 0 || backups <= 0 {
		return false, errors.New("log size and backup count must be positive")
	}
	lstat, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect log: %w", err)
	}
	if lstat.Mode()&os.ModeSymlink != 0 || !lstat.Mode().IsRegular() {
		return false, errors.New("refusing to rotate symlink or non-regular log")
	}
	if lstat.Size() <= maxBytes {
		return false, nil
	}

	descriptor, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return false, fmt.Errorf("open log without following symlinks: %w", err)
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = unix.Close(descriptor)
		return false, errors.New("open log file descriptor")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return false, fmt.Errorf("inspect opened log: %w", err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(lstat, opened) {
		return false, errors.New("log target changed while opening")
	}
	if opened.Size() <= maxBytes {
		return false, nil
	}

	backupPath := fmt.Sprintf("%s.1", path)
	pending, err := renameio.NewPendingFile(
		backupPath,
		renameio.WithTempDir(filepath.Dir(backupPath)),
		renameio.WithStaticPermissions(opened.Mode().Perm()),
	)
	if err != nil {
		return false, fmt.Errorf("create pending log backup: %w", err)
	}
	defer pending.Cleanup()
	if _, err := io.Copy(pending, file); err != nil {
		return false, fmt.Errorf("copy log backup: %w", err)
	}
	if err := os.Chtimes(pending.Name(), opened.ModTime(), opened.ModTime()); err != nil {
		return false, fmt.Errorf("preserve log backup timestamp: %w", err)
	}
	if err := validateBackups(path, backups); err != nil {
		return false, err
	}
	oldest := fmt.Sprintf("%s.%d", path, backups)
	if _, err := os.Lstat(oldest); err == nil {
		if err := os.Remove(oldest); err != nil {
			return false, fmt.Errorf("remove oldest log backup: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("inspect oldest log backup: %w", err)
	}
	for index := backups - 1; index > 0; index-- {
		source := fmt.Sprintf("%s.%d", path, index)
		destination := fmt.Sprintf("%s.%d", path, index+1)
		if _, err := os.Lstat(source); err == nil {
			if err := os.Rename(source, destination); err != nil {
				return false, fmt.Errorf("shift log backup %d: %w", index, err)
			}
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("inspect log backup %d: %w", index, err)
		}
	}
	if err := pending.CloseAtomicallyReplace(); err != nil {
		return false, fmt.Errorf("publish log backup: %w", err)
	}
	if err := file.Truncate(0); err != nil {
		return false, fmt.Errorf("truncate rotated log: %w", err)
	}
	if err := file.Sync(); err != nil {
		return false, fmt.Errorf("sync rotated log: %w", err)
	}
	return true, nil
}

func validateBackups(path string, backups int) error {
	for index := 1; index <= backups; index++ {
		candidate := fmt.Sprintf("%s.%d", path, index)
		info, err := os.Lstat(candidate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect log backup %d: %w", index, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("refusing symlink or non-regular log backup %d", index)
		}
	}
	return nil
}

func truncateRunes(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}
