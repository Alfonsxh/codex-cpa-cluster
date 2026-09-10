package quota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
)

type CooldownRecoveryStore interface {
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadSecret(context.Context, string) (string, bool, error)
	WithWriteFence(context.Context, func() error) error
}

// CooldownRecovery synchronizes a confirmed new official quota period with
// CPA's old credential cooldown. It does not grant quota, consume reset credits,
// change routes, restart containers, or weaken the portal's runtime checks.
type CooldownRecovery struct {
	store CooldownRecoveryStore
	http  *http.Client
	now   func() time.Time
}

func NewCooldownRecovery(store CooldownRecoveryStore) *CooldownRecovery {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &CooldownRecovery{
		store: store, now: time.Now,
		http: &http.Client{
			Transport: transport, Timeout: 3 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

type cooldownAuth struct {
	Index          string    `json:"auth_index"`
	Provider       string    `json:"provider"`
	Status         string    `json:"status"`
	Message        string    `json:"status_message"`
	Unavailable    bool      `json:"unavailable"`
	Disabled       bool      `json:"disabled"`
	UpdatedAt      time.Time `json:"updated_at"`
	NextRetryAfter time.Time `json:"next_retry_after"`
	IDToken        struct {
		AccountID string `json:"chatgpt_account_id"`
	} `json:"id_token"`
}

// Reconcile is called only by the owned quota writer, after publishing a fresh
// snapshot. Recovery errors remain separate from successful official reads.
func (recovery *CooldownRecovery) Reconcile(ctx context.Context, snapshot Snapshot) (int, error) {
	if recovery.store == nil {
		return 0, errors.New("quota cooldown recovery requires a store")
	}
	// Native recovery must not delay the next official refresh indefinitely
	// when account containers are stopped or their management API is slow.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !freshRecoverySnapshot(snapshot, recovery.now()) {
		return 0, nil
	}
	accounts, err := recovery.store.ReadAccounts(ctx)
	if err != nil {
		return 0, err
	}
	enabled := make(map[string]bool, len(accounts))
	for _, account := range accounts {
		enabled[account.ID] = account.GroupEnabled
	}
	key, found, err := recovery.store.ReadSecret(ctx, "cpa_management_key")
	if err != nil {
		return 0, err
	}
	key = strings.TrimSpace(key)
	if !found || key == "" || strings.ContainsAny(key, "\r\n\x00") {
		return 0, errors.New("quota cooldown recovery management credential is unavailable")
	}
	recovered := 0
	var failures []error
	for _, account := range snapshot.Accounts {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		id, idError := controlplane.NormalizeAccountID(account.Account)
		if idError != nil || id != account.Account || !enabled[id] || account.oauthAccountID == "" || !account.recoveryAllowed ||
			!freshRecoverySnapshot(snapshot, recovery.now()) || recoveryWindow(account, recovery.now()) == nil {
			continue
		}
		auth, err := recovery.readAuth(ctx, id, key)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !recoverableCooldown(auth, account, recovery.now()) {
			continue
		}
		err = recovery.store.WithWriteFence(ctx, func() error {
			// Recheck catalog and CPA state under the same cross-process lock as
			// account lifecycle and ownership changes. GET handlers never do this.
			current, err := recovery.store.ReadAccounts(ctx)
			if err != nil {
				return err
			}
			stillEnabled := false
			for _, candidate := range current {
				if candidate.ID == id {
					stillEnabled = candidate.GroupEnabled
				}
			}
			if !stillEnabled || !freshRecoverySnapshot(snapshot, recovery.now()) {
				return nil
			}
			latest, err := recovery.readAuth(ctx, id, key)
			if err != nil {
				return err
			}
			if latest != auth || !freshRecoverySnapshot(snapshot, recovery.now()) || !recoverableCooldown(latest, account, recovery.now()) {
				return nil
			}
			body, _ := json.Marshal(map[string]string{"auth_index": auth.Index})
			var result struct {
				Status string `json:"status"`
				Index  string `json:"auth_index"`
			}
			if err := recovery.request(ctx, id, key, http.MethodPost, "/reset-quota", body, &result); err != nil {
				return err
			}
			if result.Status != "ok" || result.Index != auth.Index {
				return fmt.Errorf("CPA %s cooldown reset was not acknowledged", id)
			}
			after, err := recovery.readAuth(ctx, id, key)
			if err != nil {
				return err
			}
			if after.Index != auth.Index || after.Unavailable || after.Disabled || after.Status != "active" ||
				after.Message != "" || after.NextRetryAfter.After(recovery.now()) {
				return fmt.Errorf("CPA %s cooldown recovery is not confirmed", id)
			}
			recovered++
			return nil
		})
		if err != nil {
			failures = append(failures, err)
		}
	}
	return recovered, errors.Join(failures...)
}

func freshRecoverySnapshot(snapshot Snapshot, now time.Time) bool {
	age := now.Unix() - snapshot.GeneratedAt
	return !snapshot.Cached && !snapshot.Refreshing && snapshot.GeneratedAt > 0 && age >= 0 && age <= 120
}

// Native reset-quota clears every model cooldown for the credential. Check
// all upstream rate-limit flags, including short additional windows that are
// intentionally absent from the public weekly-quota representation.
func allRateLimitsAllowRecovery(payload map[string]any) bool {
	allowed := func(value any) bool {
		limit := object(value)
		return limit != nil && limit["allowed"] == true && limit["limit_reached"] == false
	}
	if !allowed(payload["rate_limit"]) {
		return false
	}
	if raw := payload["additional_rate_limits"]; raw != nil {
		additional, ok := raw.([]any)
		if !ok {
			return false
		}
		for _, item := range additional {
			if !allowed(object(item)["rate_limit"]) {
				return false
			}
		}
	}
	return true
}

func recoveryWindow(account AccountQuota, now time.Time) *WeeklyWindow {
	weekly := account.Weekly
	if account.Status != "ok" || account.Allowed == nil || !*account.Allowed ||
		account.LimitReached == nil || *account.LimitReached || weekly == nil ||
		!strings.HasPrefix(weekly.Key, "default:") || weekly.WindowSeconds != WeeklyWindowSeconds ||
		weekly.ResetAt == nil || *weekly.ResetAt <= now.Unix() ||
		*weekly.ResetAt-WeeklyWindowSeconds > now.Unix() {
		return nil
	}
	for _, window := range append([]WeeklyWindow{*weekly}, account.WeeklyWindows...) {
		if window.LimitReached || math.IsNaN(window.UsedPercent) || math.IsInf(window.UsedPercent, 0) ||
			window.UsedPercent < 0 || window.UsedPercent >= 100 || math.IsNaN(window.RemainingPercent) ||
			math.IsInf(window.RemainingPercent, 0) || window.RemainingPercent <= 0 || window.RemainingPercent > 100 {
			return nil
		}
	}
	return weekly
}

func recoverableCooldown(auth cooldownAuth, account AccountQuota, now time.Time) bool {
	weekly := recoveryWindow(account, now)
	if weekly == nil || account.oauthAccountID == "" || auth.IDToken.AccountID != account.oauthAccountID ||
		auth.Provider != "codex" || !auth.Unavailable || auth.Disabled || auth.Status != "error" ||
		auth.Index == "" || len(auth.Index) > 256 || strings.ContainsAny(auth.Index, "\r\n\x00") || auth.UpdatedAt.IsZero() {
		return false
	}
	var payload struct {
		Error struct {
			Type     string `json:"type"`
			Code     string `json:"code"`
			ResetsAt int64  `json:"resets_at"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(auth.Message), &payload) != nil ||
		(payload.Error.Type != "usage_limit_reached" && payload.Error.Code != "usage_limit_reached") {
		return false
	}
	// Positive remaining quota alone is insufficient: a short/model window may
	// still be exhausted. Require proof that the official regular weekly period
	// started AFTER this error and superseded its previously reported reset.
	return payload.Error.ResetsAt > 0 && payload.Error.ResetsAt < *weekly.ResetAt &&
		auth.UpdatedAt.Before(time.Unix(*weekly.ResetAt-WeeklyWindowSeconds, 0))
}

func (recovery *CooldownRecovery) readAuth(ctx context.Context, account, key string) (cooldownAuth, error) {
	var payload struct {
		Files []cooldownAuth `json:"files"`
	}
	if err := recovery.request(ctx, account, key, http.MethodGet, "/auth-files", nil, &payload); err != nil {
		return cooldownAuth{}, err
	}
	// The quota fetcher uses one OAuth identity per account. Ambiguous native
	// identity sets must never become targets for a blanket cooldown reset.
	if len(payload.Files) != 1 {
		return cooldownAuth{}, nil
	}
	return payload.Files[0], nil
}

func (recovery *CooldownRecovery) request(ctx context.Context, account, key, method, path string, body []byte, result any) error {
	request, err := http.NewRequestWithContext(ctx, method, "http://cliproxy-"+account+":8317/v0/management"+path, bytes.NewReader(body))
	if err != nil {
		return errors.New("cannot construct CPA cooldown request")
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response, err := recovery.http.Do(request)
	if err != nil {
		return fmt.Errorf("CPA %s cooldown request failed", account)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("CPA %s cooldown request returned HTTP %d", account, response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBodyBytes+1))
	if err != nil || len(raw) > maximumResponseBodyBytes || json.Unmarshal(raw, result) != nil {
		return fmt.Errorf("CPA %s cooldown response is invalid", account)
	}
	return nil
}
