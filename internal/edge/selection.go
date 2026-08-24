package edge

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"
	"golang.org/x/sys/unix"
)

type Slot string

const (
	Blue  Slot = "blue"
	Green Slot = "green"
)

var activeGatewayDirective = regexp.MustCompile(
	`^set\s+\$active_gateway_backend\s+gateway-(blue|green):8317;$`,
)

func ParseSelection(reader io.Reader) (Slot, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, 4*1024+1))
	if err != nil {
		return "", fmt.Errorf("read active Gateway selection: %w", err)
	}
	if len(raw) > 4*1024 {
		return "", errors.New("active Gateway selection exceeds 4096 bytes")
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	directives := make([]string, 0, 1)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		directives = append(directives, line)
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read active Gateway selection: %w", err)
	}
	if len(directives) != 1 {
		return "", errors.New("active Gateway selection must contain exactly one directive")
	}
	match := activeGatewayDirective.FindStringSubmatch(directives[0])
	if len(match) != 2 {
		return "", errors.New("active Gateway selection contains an unsafe directive")
	}
	return Slot(match[1]), nil
}

func ReadSelection(path string) (Slot, error) {
	absolutePath, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", fmt.Errorf("resolve active Gateway selection: %w", err)
	}
	descriptor, err := unix.Open(absolutePath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("open active Gateway selection: %w", err)
	}
	file := os.NewFile(uintptr(descriptor), absolutePath)
	if file == nil {
		_ = unix.Close(descriptor)
		return "", errors.New("open active Gateway selection returned an invalid descriptor")
	}
	defer file.Close()
	information, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect active Gateway selection: %w", err)
	}
	if !information.Mode().IsRegular() {
		return "", errors.New("active Gateway selection must be a regular non-symlink file")
	}
	slot, err := ParseSelection(file)
	if err != nil {
		return "", err
	}
	return slot, nil
}

type Selector struct {
	path            string
	refreshInterval time.Duration
	logger          *zap.Logger
	slot            atomic.Value
}

func NewSelector(path string, refreshInterval time.Duration, logger *zap.Logger) (*Selector, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("active Gateway selection path is required")
	}
	if refreshInterval <= 0 {
		return nil, errors.New("active Gateway refresh interval must be positive")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve active Gateway selection: %w", err)
	}
	selector := &Selector{path: absolutePath, refreshInterval: refreshInterval, logger: logger}
	slot, err := ReadSelection(absolutePath)
	if err != nil {
		return nil, err
	}
	selector.slot.Store(slot)
	return selector, nil
}

func (selector *Selector) Slot() Slot {
	value := selector.slot.Load()
	if value == nil {
		return ""
	}
	slot, _ := value.(Slot)
	return slot
}

func (selector *Selector) Refresh() (bool, error) {
	slot, err := ReadSelection(selector.path)
	if err != nil {
		return false, err
	}
	if selector.Slot() == slot {
		return false, nil
	}
	selector.slot.Store(slot)
	selector.logger.Info("Go v2 Edge activated Gateway slot", zap.String("slot", string(slot)))
	return true, nil
}

func (selector *Selector) Run(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create active Gateway selection watcher: %w", err)
	}
	defer watcher.Close()
	if err := watcher.Add(filepath.Dir(selector.path)); err != nil {
		return fmt.Errorf("watch active Gateway selection directory: %w", err)
	}
	refresh := func() {
		if _, err := selector.Refresh(); err != nil {
			selector.logger.Warn(
				"Go v2 Edge rejected active Gateway selection; keeping previous slot",
				zap.String("slot", string(selector.Slot())),
				zap.Error(err),
			)
		}
	}
	// Close the constructor-to-watcher race: an atomic replacement may land
	// after NewSelector reads the file but just before fsnotify is attached.
	refresh()
	ticker := time.NewTicker(selector.refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.Events:
			if !ok {
				return errors.New("active Gateway selection watcher closed")
			}
			if filepath.Clean(event.Name) == filepath.Clean(selector.path) {
				refresh()
			}
		case watchError, ok := <-watcher.Errors:
			if !ok {
				return errors.New("active Gateway selection watcher error channel closed")
			}
			selector.logger.Warn("active Gateway selection watch failed", zap.Error(watchError))
		case <-ticker.C:
			refresh()
		}
	}
}
