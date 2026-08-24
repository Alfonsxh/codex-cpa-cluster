package quota

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/sys/unix"
)

const maximumOAuthFileBytes = int64(2 * 1024 * 1024)

var (
	ErrOAuthMissing  = errors.New("Codex OAuth auth record is unavailable")
	accountIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
)

type OAuthLoader struct {
	Root string
}

type oauthCandidate struct {
	record  OAuthRecord
	modTime int64
	name    string
}

func (loader OAuthLoader) Load(account string) (OAuthRecord, error) {
	if !accountIDPattern.MatchString(account) {
		return OAuthRecord{}, ErrOAuthMissing
	}
	root, err := filepath.Abs(loader.Root)
	if err != nil {
		return OAuthRecord{}, ErrOAuthMissing
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return OAuthRecord{}, ErrOAuthMissing
	}
	authRoot := filepath.Join(root, "auth")
	accountDirectory := filepath.Join(authRoot, account)
	for _, directory := range []string{authRoot, accountDirectory} {
		info, err := os.Lstat(directory)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return OAuthRecord{}, ErrOAuthMissing
		}
	}
	entries, err := os.ReadDir(accountDirectory)
	if err != nil {
		return OAuthRecord{}, ErrOAuthMissing
	}
	candidates := make([]oauthCandidate, 0)
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(accountDirectory, entry.Name())
		raw, info, err := readRegularNoFollow(path, maximumOAuthFileBytes)
		if err != nil {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var payload struct {
			Type        string `json:"type"`
			Disabled    bool   `json:"disabled"`
			AccessToken string `json:"access_token"`
			AccountID   any    `json:"account_id"`
		}
		if err := decoder.Decode(&payload); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
			continue
		}
		accessToken := strings.TrimSpace(payload.AccessToken)
		if payload.Type != "codex" || payload.Disabled || accessToken == "" || containsControl(accessToken) {
			continue
		}
		candidates = append(candidates, oauthCandidate{
			record:  OAuthRecord{AccessToken: accessToken, AccountID: cleanHeaderValue(payload.AccountID)},
			modTime: info.ModTime().UnixNano(), name: entry.Name(),
		})
	}
	if len(candidates) == 0 {
		return OAuthRecord{}, ErrOAuthMissing
	}
	sort.Slice(candidates, func(left int, right int) bool {
		if candidates[left].modTime != candidates[right].modTime {
			return candidates[left].modTime > candidates[right].modTime
		}
		return candidates[left].name > candidates[right].name
	})
	return candidates[0].record, nil
}

func readRegularNoFollow(path string, maximum int64) ([]byte, os.FileInfo, error) {
	lstat, err := os.Lstat(path)
	if err != nil || lstat.Mode()&os.ModeSymlink != 0 || !lstat.Mode().IsRegular() || lstat.Size() > maximum {
		return nil, nil, errors.New("unsafe OAuth record")
	}
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, errors.New("open OAuth record")
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, nil, errors.New("open OAuth file descriptor")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(lstat, opened) {
		return nil, nil, errors.New("OAuth record changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(raw)) > maximum {
		return nil, nil, errors.New("read OAuth record")
	}
	return raw, opened, nil
}

func cleanHeaderValue(value any) string {
	var result string
	switch typed := value.(type) {
	case string:
		result = typed
	case json.Number:
		result = typed.String()
	case nil:
		return ""
	default:
		result = fmt.Sprint(typed)
	}
	result = strings.TrimSpace(result)
	if len(result) > 512 || containsControl(result) {
		return ""
	}
	return result
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
