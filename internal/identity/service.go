package identity

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/failover"
	"github.com/google/uuid"
)

var (
	ErrRotationConflict = errors.New("API key rotation conflict")
	ErrRotationUnsafe   = errors.New("API key rotation is unsafe")
	ErrInvalidUser      = errors.New("user email is outside the configured domains")
)

var keyPrefixPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,30}_$`)
var userEmailPattern = regexp.MustCompile(`^[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@([a-z0-9.-]+)$`)

type RotationStore interface {
	ReadSettings(context.Context) (map[string]any, error)
	ApplyUserKeyRotationExpected(context.Context, string, string, string) (controlplane.UserKeyRotation, error)
	RestoreUserKeyRotation(context.Context, controlplane.UserKeyRotation) error
}

type SnapshotPublisher interface {
	PublishAuthSnapshot(context.Context, bool) (failover.Snapshot, error)
}

type Service struct {
	Store     RotationStore
	Snapshots SnapshotPublisher
	Lock      sync.Locker
}

type RotationResult struct {
	APIKey             string `json:"api_key"`
	SnapshotGeneration string `json:"snapshot_generation"`
}

func (service *Service) RotateUserKey(
	ctx context.Context,
	user string,
	expectedKey string,
) (RotationResult, error) {
	if service == nil || service.Store == nil || service.Snapshots == nil {
		return RotationResult{}, errors.New("API key rotation dependencies are incomplete")
	}
	if service.Lock != nil {
		service.Lock.Lock()
		defer service.Lock.Unlock()
	}
	user = strings.ToLower(strings.TrimSpace(user))
	expectedKey = strings.TrimSpace(expectedKey)
	if user == "" || expectedKey == "" {
		return RotationResult{}, ErrRotationUnsafe
	}
	settings, err := service.Store.ReadSettings(ctx)
	if err != nil {
		return RotationResult{}, fmt.Errorf("read API key rotation settings: %w", err)
	}
	newKey, err := NewUserKey(settings, user)
	if err != nil {
		return RotationResult{}, ErrRotationUnsafe
	}
	rotation, err := service.Store.ApplyUserKeyRotationExpected(ctx, user, expectedKey, newKey)
	if err != nil {
		switch {
		case errors.Is(err, controlplane.ErrUserKeyRotationConflict):
			return RotationResult{}, errors.Join(ErrRotationConflict, err)
		case errors.Is(err, controlplane.ErrUserKeyRotationUnsafe),
			errors.Is(err, controlplane.ErrInvalidCatalogInput):
			return RotationResult{}, errors.Join(ErrRotationUnsafe, err)
		default:
			return RotationResult{}, fmt.Errorf("apply API key rotation: %w", err)
		}
	}
	snapshot, err := service.Snapshots.PublishAuthSnapshot(ctx, true)
	if err == nil {
		return RotationResult{APIKey: newKey, SnapshotGeneration: snapshot.Generation}, nil
	}
	rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	rollbackError := service.Store.RestoreUserKeyRotation(rollbackContext, rotation)
	var rollbackSnapshotError error
	if rollbackError == nil {
		_, rollbackSnapshotError = service.Snapshots.PublishAuthSnapshot(rollbackContext, true)
	}
	return RotationResult{}, errors.Join(
		fmt.Errorf("publish API key rotation snapshot: %w", err),
		wrapOptional("rollback API key rotation", rollbackError),
		wrapOptional("publish API key rollback snapshot", rollbackSnapshotError),
	)
}

// NormalizeUser applies the configured explicit domain allowlist. The result
// is suitable for storage and routing; display-name email parsing is
// intentionally unsupported.
func NormalizeUser(settings map[string]any, raw string) (string, error) {
	user := strings.ToLower(strings.TrimSpace(raw))
	match := userEmailPattern.FindStringSubmatch(user)
	if len(match) != 2 {
		return "", ErrInvalidUser
	}
	domains := make(map[string]struct{})
	switch values := settings["identity.allowed_email_domains"].(type) {
	case []string:
		for _, value := range values {
			domain := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "@")
			if domain != "" {
				domains[domain] = struct{}{}
			}
		}
	case []any:
		for _, rawValue := range values {
			value, ok := rawValue.(string)
			if !ok {
				return "", ErrInvalidUser
			}
			domain := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "@")
			if domain != "" {
				domains[domain] = struct{}{}
			}
		}
	}
	if _, allowed := domains[match[1]]; !allowed {
		return "", ErrInvalidUser
	}
	return user, nil
}

// NewUserKey generates the only externally visible secret format used for new
// users and rotations. The caller is responsible for returning it exactly
// once and never logging it.
func NewUserKey(settings map[string]any, user string) (string, error) {
	prefix := "cpa_"
	if value, ok := settings["identity.key_prefix"].(string); ok && strings.TrimSpace(value) != "" {
		prefix = strings.ToLower(strings.TrimSpace(value))
	}
	if !keyPrefixPattern.MatchString(prefix) {
		return "", ErrRotationUnsafe
	}
	return prefix + userNamespace(user) + "_" + uuid.NewString(), nil
}

func userNamespace(user string) string {
	local := strings.SplitN(strings.ToLower(strings.TrimSpace(user)), "@", 2)[0]
	var builder strings.Builder
	lastUnderscore := false
	for _, character := range local {
		valid := character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
		if valid {
			builder.WriteRune(character)
			lastUnderscore = false
			continue
		}
		if builder.Len() > 0 && !lastUnderscore {
			builder.WriteByte('_')
			lastUnderscore = true
		}
	}
	result := strings.Trim(builder.String(), "_")
	if result == "" {
		return "user"
	}
	return result
}

func wrapOptional(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
