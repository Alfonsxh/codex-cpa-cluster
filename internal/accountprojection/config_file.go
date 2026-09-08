package accountprojection

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// CLIProxyAPI watches the config inode, not its parent directory. Replacing an
// existing file by rename removes that watch on Linux. Keep the inode and emit
// a write event; its debounced reload then reads the complete validated YAML.
// This is deliberately not a crash-atomic update. Gateway snapshots remain
// atomic, and user creation separately waits for upstream authentication.
func writeAccountConfig(path string, payload []byte, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return writeAtomic(path, payload, mode)
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: account config must be a regular file", ErrInvalidProjection)
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	var identity unix.Stat_t
	if err := unix.Fstat(fd, &identity); err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) || identity.Nlink != 1 {
		return fmt.Errorf("%w: account config identity changed or is hard-linked", ErrInvalidProjection)
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	previous, err := io.ReadAll(file)
	if err != nil || bytes.Equal(previous, payload) {
		return err
	}
	return updateConfigContents(file, previous, payload)
}

type configWriter interface {
	WriteAt([]byte, int64) (int, error)
	Truncate(int64) error
	Sync() error
}

func updateConfigContents(file configWriter, previous, payload []byte) error {
	if err := replaceConfigContents(file, payload, len(previous)); err != nil {
		return errors.Join(err, replaceConfigContents(file, previous, max(len(previous), len(payload))))
	}
	return nil
}

func replaceConfigContents(file configWriter, payload []byte, oldSize int) error {
	// Fill any obsolete suffix with YAML whitespace in the same write. A reader
	// between WriteAt and Truncate cannot see old trailing keys or an empty file.
	data := payload
	if len(payload) < oldSize {
		data = append(bytes.Clone(payload), bytes.Repeat([]byte{'\n'}, oldSize-len(payload))...)
	}
	n, err := file.WriteAt(data, 0)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	if err := file.Truncate(int64(len(payload))); err != nil {
		return err
	}
	return file.Sync()
}
