package accountlifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/accountconfig"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/google/renameio/v2"
	"github.com/google/uuid"
)

var ErrUnsafeAccountPath = errors.New("unsafe account lifecycle path")

type BackupData struct {
	Account controlplane.Account
	Keys    []controlplane.StoredKeyRecord
}

type FileTransition interface {
	BackupPath() string
	Commit() error
	Rollback() error
}

type Files interface {
	PrepareCreate(string, string) (FileTransition, error)
	PrepareUpdate(string, string, string, BackupData) (FileTransition, error)
	PrepareDelete(string, string, BackupData) (FileTransition, error)
	PrepareAuthClear(string, string, BackupData) (FileTransition, error)
	Recover(Operation, *controlplane.Account) error
}

type FileManager struct {
	Root string
	Now  func() time.Time
}

type fileTransition struct {
	backup     string
	commit     func() error
	rollback   func() error
	committed  bool
	rolledBack bool
}

func (transition *fileTransition) BackupPath() string { return transition.backup }

func (transition *fileTransition) Commit() error {
	if transition == nil || transition.committed {
		return nil
	}
	if transition.rolledBack {
		return errors.New("account file transition was already rolled back")
	}
	if transition.commit != nil {
		if err := transition.commit(); err != nil {
			return err
		}
	}
	transition.committed = true
	return nil
}

func (transition *fileTransition) Rollback() error {
	if transition == nil || transition.rolledBack {
		return nil
	}
	if transition.rollback != nil {
		if err := transition.rollback(); err != nil {
			return err
		}
	}
	transition.rolledBack = true
	return nil
}

func (manager *FileManager) PrepareCreate(operationID string, accountID string) (FileTransition, error) {
	if err := validateOperationID(operationID); err != nil {
		return nil, err
	}
	root, accountID, err := manager.validated(accountID)
	if err != nil {
		return nil, err
	}
	paths := accountPaths(root, accountID)
	for _, path := range []string{paths.config, paths.legacyConfig, paths.auth, paths.logs} {
		if exists(path) {
			return nil, fmt.Errorf("%w: account creation target already exists: %s", ErrUnsafeAccountPath, relativePath(root, path))
		}
	}
	for _, directory := range []string{paths.auth, paths.logs} {
		if err := secureDirectory(directory); err != nil {
			_ = removePath(paths.auth)
			_ = removePath(paths.logs)
			return nil, fmt.Errorf("create account runtime directory: %w", err)
		}
	}
	return &fileTransition{
		commit: func() error { return nil },
		rollback: func() error {
			return errors.Join(removePath(paths.config), removePath(paths.legacyConfig), removePath(paths.auth), removePath(paths.logs))
		},
	}, nil
}

func (manager *FileManager) PrepareUpdate(
	operationID string,
	oldAccountID string,
	newAccountID string,
	data BackupData,
) (FileTransition, error) {
	if err := validateOperationID(operationID); err != nil {
		return nil, err
	}
	root, oldAccountID, err := manager.validated(oldAccountID)
	if err != nil {
		return nil, err
	}
	_, newAccountID, err = manager.validated(newAccountID)
	if err != nil {
		return nil, err
	}
	if oldAccountID == newAccountID {
		return &fileTransition{commit: func() error { return nil }, rollback: func() error { return nil }}, nil
	}
	oldPaths := accountPaths(root, oldAccountID)
	newPaths := accountPaths(root, newAccountID)
	for _, target := range []string{newPaths.config, newPaths.legacyConfig, newPaths.auth, newPaths.logs} {
		if exists(target) {
			return nil, fmt.Errorf("%w: account rename target already exists: %s", ErrUnsafeAccountPath, relativePath(root, target))
		}
	}
	backup, err := manager.createBackup(operationID, root, oldAccountID, "renamed-to-"+newAccountID, data, true, true, true)
	if err != nil {
		return nil, err
	}
	moved := make([]pathMove, 0, 2)
	for _, pair := range []pathMove{
		{source: oldPaths.auth, destination: newPaths.auth},
		{source: oldPaths.logs, destination: newPaths.logs},
	} {
		if exists(pair.source) {
			if err := os.Rename(pair.source, pair.destination); err != nil {
				_ = rollbackMoves(moved)
				return nil, fmt.Errorf("rename account path %s: %w", relativePath(root, pair.source), err)
			}
			moved = append(moved, pair)
			continue
		}
		if err := secureDirectory(pair.destination); err != nil {
			_ = rollbackMoves(moved)
			return nil, fmt.Errorf("create renamed account path %s: %w", relativePath(root, pair.destination), err)
		}
		moved = append(moved, pathMove{destination: pair.destination, created: true})
	}
	return &fileTransition{
		backup: relativePath(root, backup),
		commit: func() error {
			return errors.Join(removePath(oldPaths.config), removePath(oldPaths.legacyConfig))
		},
		rollback: func() error {
			return errors.Join(removePath(newPaths.config), removePath(newPaths.legacyConfig), rollbackMoves(moved))
		},
	}, nil
}

func (manager *FileManager) PrepareDelete(operationID string, accountID string, data BackupData) (FileTransition, error) {
	if err := validateOperationID(operationID); err != nil {
		return nil, err
	}
	root, accountID, err := manager.validated(accountID)
	if err != nil {
		return nil, err
	}
	paths := accountPaths(root, accountID)
	backup, err := manager.createBackup(operationID, root, accountID, "deleted", data, true, true, true)
	if err != nil {
		return nil, err
	}
	return &fileTransition{
		backup: relativePath(root, backup),
		commit: func() error {
			return errors.Join(removePath(paths.config), removePath(paths.legacyConfig), removePath(paths.auth), removePath(paths.logs))
		},
		rollback: func() error {
			return restoreBackup(backup, paths, true, true, true)
		},
	}, nil
}

func (manager *FileManager) PrepareAuthClear(operationID string, accountID string, data BackupData) (FileTransition, error) {
	if err := validateOperationID(operationID); err != nil {
		return nil, err
	}
	root, accountID, err := manager.validated(accountID)
	if err != nil {
		return nil, err
	}
	paths := accountPaths(root, accountID)
	backup, err := manager.createBackup(operationID, root, accountID, "oauth-clear", data, false, true, false)
	if err != nil {
		return nil, err
	}
	if err := clearDirectory(paths.auth); err != nil {
		return nil, fmt.Errorf("clear account OAuth directory: %w", err)
	}
	return &fileTransition{
		backup: relativePath(root, backup),
		commit: func() error { return nil },
		rollback: func() error {
			return restoreBackup(backup, paths, false, true, false)
		},
	}, nil
}

// Recover reconciles only paths owned by the interrupted operation. The
// authoritative SQLite account determines whether the operation committed
// forward or was compensated before the process stopped.
func (manager *FileManager) Recover(operation Operation, desired *controlplane.Account) error {
	if err := validateOperation(operation); err != nil {
		return err
	}
	root, accountID, err := manager.validated(operation.AccountID)
	if err != nil {
		return err
	}
	switch operation.Kind {
	case operationCreate:
		if desired == nil {
			return removeAccountPaths(accountPaths(root, accountID))
		}
		return ensureRuntimeDirectories(accountPaths(root, desired.ID))
	case operationUpdate:
		_, newAccountID, err := manager.validated(operation.NewAccountID)
		if err != nil {
			return err
		}
		if accountID == newAccountID {
			if desired == nil || desired.ID != accountID {
				return fmt.Errorf("%w: in-place update recovery has no canonical account", ErrUnsafeAccountPath)
			}
			return ensureRuntimeDirectories(accountPaths(root, accountID))
		}
		if desired == nil || (desired.ID != accountID && desired.ID != newAccountID) {
			return fmt.Errorf("%w: update recovery has no canonical account", ErrUnsafeAccountPath)
		}
		backup := manager.operationBackupPath(root, operation, "renamed-to-"+newAccountID)
		if desired.ID == newAccountID {
			if err := recoverMovedDirectory(root, accountPaths(root, accountID).auth, accountPaths(root, newAccountID).auth, filepath.Join(backup, "auth")); err != nil {
				return err
			}
			if err := recoverMovedDirectory(root, accountPaths(root, accountID).logs, accountPaths(root, newAccountID).logs, filepath.Join(backup, "logs")); err != nil {
				return err
			}
			return removeAccountConfigPaths(accountPaths(root, accountID))
		}
		if err := recoverMovedDirectory(root, accountPaths(root, newAccountID).auth, accountPaths(root, accountID).auth, filepath.Join(backup, "auth")); err != nil {
			return err
		}
		if err := recoverMovedDirectory(root, accountPaths(root, newAccountID).logs, accountPaths(root, accountID).logs, filepath.Join(backup, "logs")); err != nil {
			return err
		}
		return removeAccountConfigPaths(accountPaths(root, newAccountID))
	case operationDelete:
		backup := manager.operationBackupPath(root, operation, "deleted")
		paths := accountPaths(root, accountID)
		if desired == nil {
			return removeAccountPaths(paths)
		}
		return restoreMissingAccountPaths(backup, paths)
	case operationClearAuth:
		if desired == nil {
			return nil
		}
		backup := manager.operationBackupPath(root, operation, "oauth-clear")
		if !directory(backup) {
			// Backup creation failed before OAuth could be changed.
			return nil
		}
		return clearDirectory(accountPaths(root, accountID).auth)
	default:
		return fmt.Errorf("unknown account file recovery operation %q", operation.Kind)
	}
}

func (manager *FileManager) operationBackupPath(root string, operation Operation, reason string) string {
	return filepath.Join(root, "backups", "accounts", operation.ID+"-"+operation.AccountID+"-"+reason)
}

func recoverMovedDirectory(root, source, destination, backup string) error {
	sourceExists := directory(source)
	destinationExists := directory(destination)
	if sourceExists && destinationExists {
		return fmt.Errorf(
			"%w: both recovery source and destination exist: %s, %s",
			ErrUnsafeAccountPath, relativePath(root, source), relativePath(root, destination),
		)
	}
	if destinationExists {
		return nil
	}
	if sourceExists {
		if err := os.Rename(source, destination); err != nil {
			return fmt.Errorf("recover account directory move: %w", err)
		}
		return nil
	}
	if directory(backup) {
		return copyDirectory(backup, destination)
	}
	return secureDirectory(destination)
}

func restoreMissingAccountPaths(backup string, paths accountFilePaths) error {
	errorsFound := make([]error, 0, 3)
	if !regularFile(paths.configFile) && regularFile(filepath.Join(backup, "config.yaml")) {
		errorsFound = append(errorsFound, copyRegularFile(filepath.Join(backup, "config.yaml"), paths.configFile, 0o600))
	}
	for _, item := range []struct{ source, destination string }{
		{filepath.Join(backup, "auth"), paths.auth},
		{filepath.Join(backup, "logs"), paths.logs},
	} {
		if directory(item.destination) {
			continue
		}
		if directory(item.source) {
			errorsFound = append(errorsFound, copyDirectory(item.source, item.destination))
		} else {
			errorsFound = append(errorsFound, secureDirectory(item.destination))
		}
	}
	return errors.Join(errorsFound...)
}

func ensureRuntimeDirectories(paths accountFilePaths) error {
	return errors.Join(secureDirectory(paths.auth), secureDirectory(paths.logs))
}

func removeAccountPaths(paths accountFilePaths) error {
	return errors.Join(removePath(paths.config), removePath(paths.legacyConfig), removePath(paths.auth), removePath(paths.logs))
}

func removeAccountConfigPaths(paths accountFilePaths) error {
	return errors.Join(removePath(paths.config), removePath(paths.legacyConfig))
}

func validateOperationID(operationID string) error {
	parsed, err := uuid.Parse(operationID)
	if err != nil || parsed.String() != strings.ToLower(strings.TrimSpace(operationID)) {
		return fmt.Errorf("invalid account lifecycle operation ID %q", operationID)
	}
	return nil
}

type accountFilePaths struct {
	config       string
	configFile   string
	legacyConfig string
	auth         string
	logs         string
}

func accountPaths(root, accountID string) accountFilePaths {
	return accountFilePaths{
		config:       accountconfig.Directory(root, accountID),
		configFile:   accountconfig.File(root, accountID),
		legacyConfig: accountconfig.LegacyFile(root, accountID),
		auth:         filepath.Join(root, "auth", accountID),
		logs:         filepath.Join(root, "logs", accountID),
	}
}

func (manager *FileManager) createBackup(
	operationID string,
	root string,
	accountID string,
	reason string,
	data BackupData,
	includeConfig bool,
	includeAuth bool,
	includeLogs bool,
) (string, error) {
	backupRoot := filepath.Join(root, "backups", "accounts")
	if err := secureDirectory(backupRoot); err != nil {
		return "", fmt.Errorf("create account backup root: %w", err)
	}
	backup := filepath.Join(backupRoot, operationID+"-"+accountID+"-"+reason)
	if err := os.Mkdir(backup, 0o700); err != nil {
		return "", fmt.Errorf("create account backup: %w", err)
	}
	if err := os.Chmod(backup, 0o700); err != nil {
		return "", err
	}
	accountPayload, err := json.MarshalIndent(data.Account, "", "  ")
	if err != nil {
		return "", err
	}
	keyPayload, err := json.MarshalIndent(data.Keys, "", "  ")
	if err != nil {
		return "", err
	}
	if err := renameio.WriteFile(filepath.Join(backup, "account.json"), append(accountPayload, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("write account backup metadata: %w", err)
	}
	if err := renameio.WriteFile(filepath.Join(backup, "keys.json"), append(keyPayload, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("write account backup Key rows: %w", err)
	}
	paths := accountPaths(root, accountID)
	configSource := paths.configFile
	if !regularFile(configSource) {
		configSource = paths.legacyConfig
	}
	if includeConfig && regularFile(configSource) {
		if err := copyRegularFile(configSource, filepath.Join(backup, "config.yaml"), 0o600); err != nil {
			return "", err
		}
	}
	if includeAuth && directory(paths.auth) {
		if err := copyDirectory(paths.auth, filepath.Join(backup, "auth")); err != nil {
			return "", fmt.Errorf("backup account OAuth directory: %w", err)
		}
	}
	if includeLogs && directory(paths.logs) {
		if err := copyDirectory(paths.logs, filepath.Join(backup, "logs")); err != nil {
			return "", fmt.Errorf("backup account log directory: %w", err)
		}
	}
	return backup, nil
}

func restoreBackup(
	backup string,
	paths accountFilePaths,
	includeConfig bool,
	includeAuth bool,
	includeLogs bool,
) error {
	errorsFound := make([]error, 0, 3)
	if includeConfig {
		source := filepath.Join(backup, "config.yaml")
		if regularFile(source) {
			if err := copyRegularFile(source, paths.configFile, 0o600); err != nil {
				errorsFound = append(errorsFound, err)
			}
		}
	}
	for _, item := range []struct {
		enabled bool
		name    string
		target  string
	}{{includeAuth, "auth", paths.auth}, {includeLogs, "logs", paths.logs}} {
		if !item.enabled {
			continue
		}
		if err := removePath(item.target); err != nil {
			errorsFound = append(errorsFound, err)
			continue
		}
		source := filepath.Join(backup, item.name)
		if directory(source) {
			if err := copyDirectory(source, item.target); err != nil {
				errorsFound = append(errorsFound, err)
			}
		} else if err := secureDirectory(item.target); err != nil {
			errorsFound = append(errorsFound, err)
		}
	}
	return errors.Join(errorsFound...)
}

type pathMove struct {
	source      string
	destination string
	created     bool
}

func rollbackMoves(moves []pathMove) error {
	errorsFound := make([]error, 0)
	for index := len(moves) - 1; index >= 0; index-- {
		move := moves[index]
		if move.created {
			errorsFound = append(errorsFound, removePath(move.destination))
			continue
		}
		if !exists(move.destination) {
			continue
		}
		if exists(move.source) {
			errorsFound = append(errorsFound, fmt.Errorf("%w: rollback source already exists: %s", ErrUnsafeAccountPath, move.source))
			continue
		}
		if err := os.Rename(move.destination, move.source); err != nil {
			errorsFound = append(errorsFound, err)
		}
	}
	return errors.Join(errorsFound...)
}

func copyDirectory(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: backup source is not a real directory: %s", ErrUnsafeAccountPath, source)
	}
	if exists(destination) {
		return fmt.Errorf("%w: backup destination exists: %s", ErrUnsafeAccountPath, destination)
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkError error) error {
		if walkError != nil {
			return walkError
		}
		if path == source {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: invalid backup path %s", ErrUnsafeAccountPath, path)
		}
		target := filepath.Join(destination, relative)
		information, err := entry.Info()
		if err != nil {
			return err
		}
		if information.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symbolic links are not supported in account backups: %s", ErrUnsafeAccountPath, path)
		}
		if information.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !information.Mode().IsRegular() {
			return fmt.Errorf("%w: special file in account backup: %s", ErrUnsafeAccountPath, path)
		}
		return copyRegularFile(path, target, 0o600)
	})
}

func copyRegularFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	temporary, err := renameio.TempFile(filepath.Dir(destination), destination)
	if err != nil {
		return err
	}
	defer temporary.Cleanup()
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err := io.Copy(temporary, input); err != nil {
		return err
	}
	return temporary.CloseAtomicallyReplace()
}

func clearDirectory(path string) error {
	information, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return secureDirectory(path)
	}
	if err != nil {
		return err
	}
	if !information.IsDir() || information.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: expected a real directory: %s", ErrUnsafeAccountPath, path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := removePath(filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	return os.Chmod(path, 0o700)
}

func removePath(path string) error {
	information, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if information.IsDir() && information.Mode()&os.ModeSymlink == 0 {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

func secureDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	information, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !information.IsDir() || information.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: expected a real directory: %s", ErrUnsafeAccountPath, path)
	}
	return os.Chmod(path, 0o700)
}

func (manager *FileManager) validated(accountID string) (string, string, error) {
	if manager == nil || strings.TrimSpace(manager.Root) == "" {
		return "", "", errors.New("account file root is required")
	}
	root, err := filepath.Abs(manager.Root)
	if err != nil {
		return "", "", err
	}
	root = filepath.Clean(root)
	normalized, err := controlplane.NormalizeAccountID(accountID)
	if err != nil || normalized != accountID {
		return "", "", fmt.Errorf("%w: invalid canonical account ID %q", ErrUnsafeAccountPath, accountID)
	}
	return root, normalized, nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func directory(path string) bool {
	information, err := os.Lstat(path)
	return err == nil && information.IsDir() && information.Mode()&os.ModeSymlink == 0
}

func regularFile(path string) bool {
	information, err := os.Lstat(path)
	return err == nil && information.Mode().IsRegular()
}

func relativePath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(relative)
}
