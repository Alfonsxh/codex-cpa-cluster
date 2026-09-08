package accountprojection

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

type failingConfigFile struct {
	*os.File
	stage  string
	failed bool
}

var errConfigIO = errors.New("injected config IO failure")

func (f *failingConfigFile) WriteAt(p []byte, offset int64) (int, error) {
	if f.stage == "write" && !f.failed {
		f.failed = true
		n, _ := f.File.WriteAt(p[:len(p)/2], offset)
		return n, errConfigIO
	}
	return f.File.WriteAt(p, offset)
}
func (f *failingConfigFile) Truncate(size int64) error {
	if f.stage == "truncate" && !f.failed {
		f.failed = true
		return errConfigIO
	}
	return f.File.Truncate(size)
}
func (f *failingConfigFile) Sync() error {
	if f.stage == "sync" && !f.failed {
		f.failed = true
		return errConfigIO
	}
	return f.File.Sync()
}

func TestAccountConfigRestoresPreviousContentAfterIOFailure(t *testing.T) {
	for _, stage := range []string{"write", "truncate", "sync"} {
		t.Run(stage, func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "config.yaml")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			before := []byte("api-keys:\n  - existing-user\n")
			if _, err := file.Write(before); err != nil {
				t.Fatal(err)
			}
			fault := &failingConfigFile{File: file, stage: stage}
			err = updateConfigContents(fault, before, []byte("api-keys:\n  - existing-user\n  - new-user\n"))
			if !errors.Is(err, errConfigIO) {
				t.Fatalf("failure not propagated: %v", err)
			}
			got, err := os.ReadFile(file.Name())
			if err != nil || !bytes.Equal(got, before) {
				t.Fatal("failed update did not restore the prior config")
			}
		})
	}
}

func TestRendererKeepsCPAFileWatchAcrossRepeatedConfigurationUpdates(t *testing.T) {
	root := t.TempDir()
	store := newProjectionStore(t, root)
	renderer := &Renderer{Root: root, Store: store}
	if _, err := renderer.Render(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "configs/alpha/config.yaml")
	original, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	if err := watcher.Add(path); err != nil {
		t.Fatal(err)
	}
	// Match CLIProxyAPI: register the file once, never re-register on rename.
	for _, retry := range []int{3, 4, 2} {
		if err := store.UpdateSettings(context.Background(), map[string]any{"cpa.request_retry": retry}); err != nil {
			t.Fatal(err)
		}
		if _, err := renderer.Render(context.Background()); err != nil {
			t.Fatal(err)
		}
		current, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(original, current) {
			t.Fatal("account config replacement invalidated the upstream file watch")
		}
		deadline := time.NewTimer(2 * time.Second)
		observed := false
		for !observed {
			select {
			case event := <-watcher.Events:
				if event.Op&(fsnotify.Rename|fsnotify.Remove) != 0 {
					t.Fatalf("upstream watch lost: %s", event)
				}
				observed = event.Has(fsnotify.Write)
			case err := <-watcher.Errors:
				t.Fatal(err)
			case <-deadline.C:
				t.Fatal("upstream did not observe a configuration write")
			}
		}
		deadline.Stop()
	}
}

func TestAccountConfigShorteningAndUnchangedRender(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeAccountConfig(path, []byte("api-keys:\n  - retained\n  - revoked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	payload := []byte("api-keys:\n  - retained\n")
	if err := writeAccountConfig(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	after, _ := os.Stat(path)
	if !bytes.Equal(got, payload) || !os.SameFile(before, after) {
		t.Fatal("shortened config retained stale keys or replaced its inode")
	}
	if err := writeAccountConfig(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := os.Stat(path)
	if !unchanged.ModTime().Equal(after.ModTime()) {
		t.Fatal("unchanged config was rewritten")
	}
}

func TestAccountConfigRejectsLinksWithoutChangingTheirTarget(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "protected")
			path := filepath.Join(root, "config.yaml")
			if err := os.WriteFile(target, []byte("protected"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := os.Link
			if kind == "symlink" {
				link = os.Symlink
			}
			if err := link(target, path); err != nil {
				t.Fatal(err)
			}
			if err := writeAccountConfig(path, []byte("api-keys: []\n"), 0o600); err == nil {
				t.Fatal("linked config accepted")
			}
			got, _ := os.ReadFile(target)
			if string(got) != "protected" {
				t.Fatal("linked target changed")
			}
		})
	}
}
