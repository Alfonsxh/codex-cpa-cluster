package identity

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/failover"
)

func TestRotateUserKeyPublishesActivatedSnapshot(t *testing.T) {
	store := &rotationStoreFake{settings: map[string]any{"identity.key_prefix": "custom_"}}
	publisher := &rotationPublisherFake{}
	service := Service{Store: store, Snapshots: publisher}

	result, err := service.RotateUserKey(context.Background(), " Alice.Smith@Example.com ", "old-key")
	if err != nil {
		t.Fatalf("RotateUserKey: %v", err)
	}
	if store.user != "alice.smith@example.com" || store.expected != "old-key" {
		t.Fatalf("rotation arguments = (%q, %q)", store.user, store.expected)
	}
	if !strings.HasPrefix(result.APIKey, "custom_alice_smith_") || result.APIKey != store.replacement {
		t.Fatalf("generated API key = %q", result.APIKey)
	}
	if result.SnapshotGeneration != "generation-1" || publisher.calls != 1 || !publisher.waits[0] {
		t.Fatalf("rotation result/publisher = (%#v, %#v)", result, publisher)
	}
}

func TestRotateUserKeyMapsExpectedValueConflict(t *testing.T) {
	store := &rotationStoreFake{applyError: controlplane.ErrUserKeyRotationConflict}
	service := Service{Store: store, Snapshots: &rotationPublisherFake{}}

	_, err := service.RotateUserKey(context.Background(), "alice@example.com", "stale-key")
	if !errors.Is(err, ErrRotationConflict) || store.restored {
		t.Fatalf("rotation conflict = %v, restored=%v", err, store.restored)
	}
}

func TestRotateUserKeyExactlyRollsBackWhenSnapshotActivationFails(t *testing.T) {
	store := &rotationStoreFake{}
	publisher := &rotationPublisherFake{failFirst: true}
	service := Service{Store: store, Snapshots: publisher}

	_, err := service.RotateUserKey(context.Background(), "alice@example.com", "old-key")
	if err == nil || !strings.Contains(err.Error(), "publish API key rotation snapshot") {
		t.Fatalf("snapshot failure = %v", err)
	}
	if !store.restored || store.restoredRotation.OldKey != "old-key" ||
		store.restoredRotation.NewKey != store.replacement {
		t.Fatalf("restored rotation = %#v", store.restoredRotation)
	}
	if publisher.calls != 2 || !publisher.waits[0] || !publisher.waits[1] {
		t.Fatalf("rollback publisher = %#v", publisher)
	}
}

func TestRotateUserKeyRejectsUnsafePrefixBeforeWriting(t *testing.T) {
	store := &rotationStoreFake{settings: map[string]any{"identity.key_prefix": "unsafe-prefix"}}
	service := Service{Store: store, Snapshots: &rotationPublisherFake{}}

	_, err := service.RotateUserKey(context.Background(), "alice@example.com", "old-key")
	if !errors.Is(err, ErrRotationUnsafe) || store.user != "" {
		t.Fatalf("unsafe prefix result = %v, store=%#v", err, store)
	}
}

func TestNormalizeUserAndNewKeyUseConfiguredIdentityPolicy(t *testing.T) {
	settings := map[string]any{
		"identity.allowed_email_domains": []any{"example.com", "example.org"},
		"identity.key_prefix":            "custom_",
	}
	user, err := NormalizeUser(settings, " Alice.Smith@Example.com ")
	if err != nil || user != "alice.smith@example.com" {
		t.Fatalf("NormalizeUser = (%q, %v)", user, err)
	}
	key, err := NewUserKey(settings, user)
	if err != nil || !strings.HasPrefix(key, "custom_alice_smith_") {
		t.Fatalf("NewUserKey = (%q, %v)", key, err)
	}
	if _, err := NormalizeUser(settings, "alice@example.net"); !errors.Is(err, ErrInvalidUser) {
		t.Fatalf("outside domain error = %v", err)
	}
	if _, err := NormalizeUser(map[string]any{}, "alice@example.com"); !errors.Is(err, ErrInvalidUser) {
		t.Fatalf("missing domain policy error = %v", err)
	}
}

type rotationStoreFake struct {
	settings         map[string]any
	applyError       error
	user             string
	expected         string
	replacement      string
	restored         bool
	restoredRotation controlplane.UserKeyRotation
}

func (store *rotationStoreFake) ReadSettings(context.Context) (map[string]any, error) {
	return store.settings, nil
}

func (store *rotationStoreFake) ApplyUserKeyRotationExpected(
	_ context.Context,
	user string,
	expected string,
	replacement string,
) (controlplane.UserKeyRotation, error) {
	if store.applyError != nil {
		return controlplane.UserKeyRotation{}, store.applyError
	}
	store.user, store.expected, store.replacement = user, expected, replacement
	return controlplane.UserKeyRotation{
		User: user, OldKey: expected, NewKey: replacement,
		RotatedRows: []controlplane.RotatedKeyRow{{Sequence: 1, UpdatedAt: 10}},
		CreatedRows: []int{2},
	}, nil
}

func (store *rotationStoreFake) RestoreUserKeyRotation(
	_ context.Context,
	rotation controlplane.UserKeyRotation,
) error {
	store.restored = true
	store.restoredRotation = rotation
	return nil
}

type rotationPublisherFake struct {
	calls     int
	failFirst bool
	waits     []bool
}

func (publisher *rotationPublisherFake) PublishAuthSnapshot(
	_ context.Context,
	wait bool,
) (failover.Snapshot, error) {
	publisher.calls++
	publisher.waits = append(publisher.waits, wait)
	if publisher.failFirst && publisher.calls == 1 {
		return failover.Snapshot{}, errors.New("activation failed")
	}
	return failover.Snapshot{Generation: "generation-1"}, nil
}
