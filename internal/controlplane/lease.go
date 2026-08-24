package controlplane

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/jmoiron/sqlx"
)

const (
	leaseStateVersion = 1
	leaseStatePrefix  = "ownership_lease:"
	minLeaseTTL       = 5 * time.Second
	maxLeaseTTL       = 5 * time.Minute
)

var (
	ErrLeaseHeld         = errors.New("ownership lease is held")
	ErrLeaseMissing      = errors.New("ownership lease is missing")
	ErrLeaseLost         = errors.New("ownership lease was lost")
	ErrLeaseStateInvalid = errors.New("ownership lease state is invalid")

	leaseScopePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

// Lease is a fencing record stored in the existing runtime_state table. Token
// and generation must both match for renewal or release, so a stale process
// cannot revive ownership after another runtime has taken over.
type Lease struct {
	Version    int    `json:"version"`
	Scope      string `json:"scope"`
	Owner      string `json:"owner"`
	Generation int64  `json:"generation"`
	Token      string `json:"token,omitempty"`
	AcquiredAt int64  `json:"acquired_at"`
	RenewedAt  int64  `json:"renewed_at"`
	ExpiresAt  int64  `json:"expires_at"`
	ReleasedAt *int64 `json:"released_at,omitempty"`
}

type LeaseHeldError struct {
	Scope      string
	Owner      string
	Generation int64
	ExpiresAt  int64
}

// InstallWriteFence makes every subsequent control-plane write on this Store
// prove that the exact runtime and worker lease generations are still active.
// The proof runs inside the same BEGIN transaction and cross-process file lock
// as the business mutation, so a stale process cannot write after ownership is
// transferred even if its heartbeat has not observed the transfer yet.
func (store *Store) InstallWriteFence(runtimeLease Lease, workerLease Lease) error {
	leases := []Lease{runtimeLease, workerLease}
	for _, lease := range leases {
		if lease.Scope == "" || lease.Owner == "" || lease.Generation < 1 || lease.Token == "" {
			return errors.New("ownership write fence requires active lease tokens and generations")
		}
	}
	if runtimeLease.Scope == workerLease.Scope {
		return errors.New("ownership write fence requires distinct runtime and worker scopes")
	}
	store.writeFenceMu.Lock()
	defer store.writeFenceMu.Unlock()
	if len(store.writeFence) != 0 {
		return errors.New("ownership write fence is already installed")
	}
	store.writeFence = append([]Lease(nil), leases...)
	return nil
}

func (store *Store) validateWriteFence(ctx context.Context, transaction *sqlx.Tx) error {
	store.writeFenceMu.RLock()
	leases := append([]Lease(nil), store.writeFence...)
	store.writeFenceMu.RUnlock()
	if len(leases) == 0 {
		return nil
	}
	now := store.now().Unix()
	for _, expected := range leases {
		current, found, err := readLease(ctx, transaction, expected.Scope)
		if err != nil {
			return fmt.Errorf("validate ownership write fence %s: %w", expected.Scope, err)
		}
		if !found || current.ExpiresAt <= now || current.Token == "" ||
			current.Owner != expected.Owner || current.Generation != expected.Generation ||
			current.Token != expected.Token {
			return fmt.Errorf(
				"%w: control-plane write fence scope %s generation %d",
				ErrLeaseLost,
				expected.Scope,
				expected.Generation,
			)
		}
	}
	return nil
}

// WithWriteFence holds the control-plane cross-process lock while a mutation
// in another local store (for example usage.sqlite3) runs. The validation
// transaction is closed before the callback so read-only control-plane lookups
// do not deadlock the Store's single SQLite connection. Ownership transfer
// uses the retained file lock, so the external mutation either finishes under
// the old generation or is rejected before it starts under a new generation.
func (store *Store) WithWriteFence(ctx context.Context, operation func() error) error {
	if operation == nil {
		return errors.New("fenced write operation is required")
	}
	store.writeFenceMu.RLock()
	hasFence := len(store.writeFence) != 0
	store.writeFenceMu.RUnlock()
	if !hasFence {
		return errors.New("ownership write fence is not installed")
	}
	return store.exclusive(func() error {
		transaction, err := store.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return fmt.Errorf("begin ownership write-fence transaction: %w", err)
		}
		if err := store.validateWriteFence(ctx, transaction); err != nil {
			_ = transaction.Rollback()
			return err
		}
		if err := transaction.Rollback(); err != nil {
			return fmt.Errorf("close ownership write-fence transaction: %w", err)
		}
		return operation()
	})
}

func (err *LeaseHeldError) Error() string {
	return fmt.Sprintf(
		"%s: scope %s is owned by %s at generation %d until %d",
		ErrLeaseHeld,
		err.Scope,
		err.Owner,
		err.Generation,
		err.ExpiresAt,
	)
}

func (err *LeaseHeldError) Unwrap() error {
	return ErrLeaseHeld
}

// TakeLease acquires an exclusive lease. It never joins an active lease, even
// when the owner label matches, because process-level worker leases must reject
// duplicate instances.
func (store *Store) TakeLease(
	ctx context.Context,
	scope string,
	owner string,
	ttl time.Duration,
) (Lease, error) {
	scope, owner, ttlSeconds, err := validateLeaseInput(scope, owner, ttl)
	if err != nil {
		return Lease{}, err
	}
	token, err := newLeaseToken()
	if err != nil {
		return Lease{}, err
	}
	var result Lease
	err = store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		current, found, err := readLease(ctx, transaction, scope)
		if err != nil {
			return err
		}
		now := store.now().Unix()
		if found && current.ExpiresAt > now {
			return heldLeaseError(current)
		}
		generation := int64(1)
		if found {
			generation = current.Generation + 1
		}
		result = Lease{
			Version: leaseStateVersion, Scope: scope, Owner: owner,
			Generation: generation, Token: token,
			AcquiredAt: now, RenewedAt: now, ExpiresAt: now + ttlSeconds,
		}
		return writeLease(ctx, transaction, result, now)
	})
	return result, err
}

// JoinLease joins and renews a runtime-wide lease only when the same owner is
// already active. Workers use this to prove that an explicit v1/v2 ownership
// transfer happened before they may mutate a shared Test or Production root.
// It intentionally refuses to bootstrap an empty or expired lease.
func (store *Store) JoinLease(
	ctx context.Context,
	scope string,
	owner string,
	ttl time.Duration,
) (Lease, error) {
	scope, owner, ttlSeconds, err := validateLeaseInput(scope, owner, ttl)
	if err != nil {
		return Lease{}, err
	}
	var result Lease
	err = store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		current, found, err := readLease(ctx, transaction, scope)
		if err != nil {
			return err
		}
		now := store.now().Unix()
		if !found || current.ExpiresAt <= now || current.Token == "" {
			return fmt.Errorf("%w: active scope %s must be transferred explicitly", ErrLeaseMissing, scope)
		}
		if current.Owner != owner {
			return heldLeaseError(current)
		}
		current.RenewedAt = now
		current.ExpiresAt = now + ttlSeconds
		current.ReleasedAt = nil
		if err := writeLease(ctx, transaction, current, now); err != nil {
			return err
		}
		result = current
		return nil
	})
	return result, err
}

func (store *Store) RenewLease(
	ctx context.Context,
	lease Lease,
	ttl time.Duration,
) (Lease, error) {
	scope, owner, ttlSeconds, err := validateLeaseInput(lease.Scope, lease.Owner, ttl)
	if err != nil {
		return Lease{}, err
	}
	if lease.Token == "" || lease.Generation < 1 {
		return Lease{}, fmt.Errorf("%w: token and generation are required", ErrLeaseLost)
	}
	var result Lease
	err = store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		current, found, err := readLease(ctx, transaction, scope)
		if err != nil {
			return err
		}
		now := store.now().Unix()
		if !found || current.ExpiresAt <= now || current.Token != lease.Token ||
			current.Generation != lease.Generation || current.Owner != owner {
			return fmt.Errorf("%w: scope %s generation %d", ErrLeaseLost, scope, lease.Generation)
		}
		current.RenewedAt = now
		current.ExpiresAt = now + ttlSeconds
		current.ReleasedAt = nil
		if err := writeLease(ctx, transaction, current, now); err != nil {
			return err
		}
		result = current
		return nil
	})
	return result, err
}

func (store *Store) ReleaseLease(ctx context.Context, lease Lease) error {
	scope, owner, _, err := validateLeaseInput(lease.Scope, lease.Owner, minLeaseTTL)
	if err != nil {
		return err
	}
	if lease.Token == "" || lease.Generation < 1 {
		return fmt.Errorf("%w: token and generation are required", ErrLeaseLost)
	}
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		current, found, err := readLease(ctx, transaction, scope)
		if err != nil {
			return err
		}
		if !found || current.Token != lease.Token || current.Generation != lease.Generation ||
			current.Owner != owner {
			return fmt.Errorf("%w: scope %s generation %d", ErrLeaseLost, scope, lease.Generation)
		}
		now := store.now().Unix()
		current.Token = ""
		current.RenewedAt = now
		current.ExpiresAt = now
		current.ReleasedAt = &now
		return writeLease(ctx, transaction, current, now)
	})
}

func (store *Store) ReadLease(ctx context.Context, scope string) (Lease, bool, error) {
	if !leaseScopePattern.MatchString(strings.TrimSpace(scope)) {
		return Lease{}, false, errors.New("ownership lease scope is invalid")
	}
	return readLease(ctx, store.db, strings.TrimSpace(scope))
}

func (reader *Reader) ReadLease(ctx context.Context, scope string) (Lease, bool, error) {
	if !leaseScopePattern.MatchString(strings.TrimSpace(scope)) {
		return Lease{}, false, errors.New("ownership lease scope is invalid")
	}
	return readLease(ctx, reader.db, strings.TrimSpace(scope))
}

func readLease(
	ctx context.Context,
	database sqlx.QueryerContext,
	scope string,
) (Lease, bool, error) {
	var raw string
	err := sqlx.GetContext(
		ctx,
		database,
		&raw,
		"SELECT payload_json FROM runtime_state WHERE name = ?",
		leaseStatePrefix+scope,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, false, nil
	}
	if err != nil {
		return Lease{}, false, fmt.Errorf("read ownership lease %s: %w", scope, err)
	}
	var lease Lease
	if err := json.Unmarshal([]byte(raw), &lease); err != nil {
		return Lease{}, false, fmt.Errorf("%w: decode scope %s: %v", ErrLeaseStateInvalid, scope, err)
	}
	if lease.Version != leaseStateVersion || lease.Scope != scope || lease.Owner == "" ||
		lease.Generation < 1 || lease.AcquiredAt < 1 || lease.RenewedAt < lease.AcquiredAt ||
		lease.ExpiresAt < lease.RenewedAt {
		return Lease{}, false, fmt.Errorf("%w: malformed scope %s", ErrLeaseStateInvalid, scope)
	}
	return lease, true, nil
}

func writeLease(ctx context.Context, transaction *sqlx.Tx, lease Lease, updatedAt int64) error {
	raw, err := json.Marshal(lease)
	if err != nil {
		return fmt.Errorf("encode ownership lease %s: %w", lease.Scope, err)
	}
	if _, err := transaction.ExecContext(ctx, `
        INSERT INTO runtime_state(name, payload_json, updated_at) VALUES (?, ?, ?)
        ON CONFLICT(name) DO UPDATE SET
            payload_json = excluded.payload_json,
            updated_at = excluded.updated_at`,
		leaseStatePrefix+lease.Scope,
		string(raw),
		updatedAt,
	); err != nil {
		return fmt.Errorf("write ownership lease %s: %w", lease.Scope, err)
	}
	return nil
}

func validateLeaseInput(scope string, owner string, ttl time.Duration) (string, string, int64, error) {
	scope = strings.TrimSpace(scope)
	owner = strings.TrimSpace(owner)
	if !leaseScopePattern.MatchString(scope) {
		return "", "", 0, errors.New("ownership lease scope is invalid")
	}
	if owner == "" || len(owner) > 128 {
		return "", "", 0, errors.New("ownership lease owner must contain 1 to 128 characters")
	}
	for _, character := range owner {
		if unicode.IsControl(character) {
			return "", "", 0, errors.New("ownership lease owner contains a control character")
		}
	}
	if ttl < minLeaseTTL || ttl > maxLeaseTTL {
		return "", "", 0, fmt.Errorf(
			"ownership lease TTL must be between %s and %s",
			minLeaseTTL,
			maxLeaseTTL,
		)
	}
	ttlSeconds := int64((ttl + time.Second - 1) / time.Second)
	return scope, owner, ttlSeconds, nil
}

func newLeaseToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate ownership lease token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func heldLeaseError(lease Lease) error {
	return &LeaseHeldError{
		Scope: lease.Scope, Owner: lease.Owner,
		Generation: lease.Generation, ExpiresAt: lease.ExpiresAt,
	}
}
