package migrationcheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const MaximumTestKeyBytes = int64(4096)
const MaximumStreamFixtureBytes = int64(1024 * 1024)

func ReadPrivateTestKey(path string) (string, error) {
	raw, information, err := readRegularFile(path, MaximumTestKeyBytes)
	if err != nil {
		return "", fmt.Errorf("read dedicated test Key: %w", err)
	}
	if information.Mode().Perm()&0o077 != 0 {
		return "", errors.New("dedicated test Key file permissions must not grant group or other access")
	}
	value := strings.TrimSpace(string(raw))
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return "", errors.New("dedicated test Key file contains an invalid Key")
	}
	return value, nil
}

func ReadStreamFixture(path string) ([]byte, error) {
	raw, _, err := readRegularFile(path, MaximumStreamFixtureBytes)
	if err != nil {
		return nil, fmt.Errorf("read stream request fixture: %w", err)
	}
	if !jsonObject(raw) {
		return nil, errors.New("stream request fixture must be a JSON object with stream=true")
	}
	return raw, nil
}

func readRegularFile(path string, maximum int64) ([]byte, os.FileInfo, error) {
	absolutePath, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil || strings.TrimSpace(path) == "" {
		return nil, nil, errors.New("file path is required")
	}
	descriptor, err := unix.Open(absolutePath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(descriptor), absolutePath)
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, nil, errors.New("file descriptor is invalid")
	}
	defer file.Close()
	information, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !information.Mode().IsRegular() {
		return nil, nil, errors.New("file must be a regular non-symlink file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(raw)) > maximum {
		return nil, nil, errors.New("file exceeds the size limit")
	}
	return raw, information, nil
}

func jsonObject(raw []byte) bool {
	var payload map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &payload) != nil || payload == nil {
		return false
	}
	stream, ok := payload["stream"].(bool)
	return ok && stream
}
