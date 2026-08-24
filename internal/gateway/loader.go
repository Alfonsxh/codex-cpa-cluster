package gateway

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"
)

const DefaultSnapshotRefreshInterval = 500 * time.Millisecond

type SnapshotPaths struct {
	Directory string
	Auth      string
	Quota     string
	Heartbeat string
}

func SnapshotPathsForDirectory(directory string) SnapshotPaths {
	directory = filepath.Clean(directory)
	return SnapshotPaths{
		Directory: directory,
		Auth:      filepath.Join(directory, "auth-snapshot.json"),
		Quota:     filepath.Join(directory, "quota-snapshot.json"),
		Heartbeat: filepath.Join(directory, "quota-heartbeat.json"),
	}
}

type SnapshotLoader struct {
	engine   *Engine
	paths    SnapshotPaths
	interval time.Duration
	logger   *zap.Logger
	now      func() time.Time

	refreshMu  sync.Mutex
	errorMu    sync.Mutex
	lastError  map[string]string
	watchReady chan struct{}
}

func NewSnapshotLoader(
	engine *Engine,
	paths SnapshotPaths,
	interval time.Duration,
	logger *zap.Logger,
) *SnapshotLoader {
	if interval <= 0 {
		interval = DefaultSnapshotRefreshInterval
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SnapshotLoader{
		engine:    engine,
		paths:     paths,
		interval:  interval,
		logger:    logger,
		now:       time.Now,
		lastError: make(map[string]string),
	}
}

// Refresh reloads every snapshot independently. A malformed or missing file
// never replaces the last valid in-memory state. Authentication freshness is
// advanced only after a successful parse, so prolonged failures naturally
// switch authorization to its fail-closed 503 behavior.
func (loader *SnapshotLoader) Refresh() {
	loader.refreshMu.Lock()
	defer loader.refreshMu.Unlock()

	now := loader.now()
	loader.load("auth", loader.paths.Auth, func(file *os.File) error {
		return loader.engine.LoadAuthSnapshot(file, now)
	})
	loader.load("quota", loader.paths.Quota, func(file *os.File) error {
		return loader.engine.LoadQuotaSnapshot(file, now)
	})
	loader.load("heartbeat", loader.paths.Heartbeat, func(file *os.File) error {
		return loader.engine.LoadQuotaHeartbeat(file)
	})
}

func (loader *SnapshotLoader) load(
	kind string,
	path string,
	load func(*os.File) error,
) {
	file, err := os.Open(path)
	if err == nil {
		err = load(file)
		closeError := file.Close()
		if err == nil && closeError != nil {
			err = closeError
		}
	}
	if err != nil {
		loader.recordError(kind, fmt.Errorf("load %s snapshot: %w", kind, err))
		return
	}
	loader.recordRecovery(kind)
}

func (loader *SnapshotLoader) recordError(kind string, err error) {
	message := err.Error()
	loader.errorMu.Lock()
	if loader.lastError[kind] == message {
		loader.errorMu.Unlock()
		return
	}
	loader.lastError[kind] = message
	loader.errorMu.Unlock()

	fields := []zap.Field{zap.String("snapshot", kind), zap.Error(err)}
	if kind == "auth" {
		loader.logger.Error("gateway snapshot refresh failed", fields...)
		return
	}
	loader.logger.Warn("gateway snapshot refresh failed; quota remains fail-open", fields...)
}

func (loader *SnapshotLoader) recordRecovery(kind string) {
	loader.errorMu.Lock()
	_, recovering := loader.lastError[kind]
	delete(loader.lastError, kind)
	loader.errorMu.Unlock()
	if recovering {
		loader.logger.Info("gateway snapshot refresh recovered", zap.String("snapshot", kind))
	}
}

// Run combines fsnotify for low-latency local updates with a periodic reload.
// The ticker is a correctness requirement: fsnotify documents that network and
// virtual filesystems may not deliver notifications, and repeated successful
// auth loads keep the five-second fail-closed freshness lease alive.
func (loader *SnapshotLoader) Run(ctx context.Context) error {
	if loader == nil || loader.engine == nil {
		return errors.New("snapshot loader requires an engine")
	}
	loader.Refresh()

	var events <-chan fsnotify.Event
	var watcherErrors <-chan error
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		loader.logger.Warn("gateway snapshot watcher unavailable; ticker fallback active", zap.Error(err))
	} else if err := watcher.Add(loader.paths.Directory); err != nil {
		loader.logger.Warn("gateway snapshot directory watch unavailable; ticker fallback active", zap.Error(err))
		_ = watcher.Close()
		watcher = nil
	} else {
		events = watcher.Events
		watcherErrors = watcher.Errors
		if loader.watchReady != nil {
			close(loader.watchReady)
		}
	}
	if watcher != nil {
		defer watcher.Close()
	}

	ticker := time.NewTicker(loader.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			loader.Refresh()
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if loader.relevantEvent(event) {
				loader.Refresh()
			}
		case watcherError, ok := <-watcherErrors:
			if !ok {
				watcherErrors = nil
				continue
			}
			loader.logger.Warn(
				"gateway snapshot watcher error; ticker fallback remains active",
				zap.Error(watcherError),
			)
		}
	}
}

func (loader *SnapshotLoader) relevantEvent(event fsnotify.Event) bool {
	if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
		return false
	}
	eventPath := filepath.Clean(event.Name)
	return eventPath == loader.paths.Auth ||
		eventPath == loader.paths.Quota ||
		eventPath == loader.paths.Heartbeat
}
