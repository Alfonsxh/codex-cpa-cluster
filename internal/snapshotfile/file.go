package snapshotfile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/google/renameio/v2"
)

// Write atomically replaces path only after the pending file already has the
// permissions and group required by readers. Applying ownership after rename
// creates a short window where an fsnotify reader can observe EACCES.
func Write(path string, payload []byte, permission os.FileMode, gid int) error {
	pending, err := newPending(path, permission, gid)
	if err != nil {
		return err
	}
	defer pending.Cleanup()
	written, err := pending.Write(payload)
	if err != nil {
		return fmt.Errorf("write pending snapshot: %w", err)
	}
	if written != len(payload) {
		return fmt.Errorf("write pending snapshot: %w", io.ErrShortWrite)
	}
	if err := pending.CloseAtomicallyReplace(); err != nil {
		return fmt.Errorf("replace snapshot atomically: %w", err)
	}
	return nil
}

func newPending(path string, permission os.FileMode, gid int) (*renameio.PendingFile, error) {
	directory := filepath.Dir(path)
	pending, err := renameio.NewPendingFile(
		path,
		renameio.WithTempDir(directory),
		renameio.WithStaticPermissions(permission),
	)
	if err != nil {
		return nil, fmt.Errorf("create pending snapshot: %w", err)
	}
	if err := ensureGroup(pending.File, gid); err != nil {
		_ = pending.Cleanup()
		return nil, err
	}
	return pending, nil
}

func ensureGroup(file *os.File, gid int) error {
	if gid < 0 {
		return nil
	}
	actual, err := fileGID(file)
	if err != nil {
		return err
	}
	if actual == gid {
		return nil
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("pending snapshot gid is %d, want %d", actual, gid)
	}
	if err := file.Chown(-1, gid); err != nil {
		return fmt.Errorf("assign pending snapshot group: %w", err)
	}
	actual, err = fileGID(file)
	if err != nil {
		return err
	}
	if actual != gid {
		return fmt.Errorf("pending snapshot gid is %d after chown, want %d", actual, gid)
	}
	return nil
}

func fileGID(file *os.File) (int, error) {
	information, err := file.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat pending snapshot: %w", err)
	}
	status, ok := information.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("unsupported pending snapshot stat type %T", information.Sys())
	}
	return int(status.Gid), nil
}
