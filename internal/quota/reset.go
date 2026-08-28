package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
)

var (
	ErrResetAccountNotFound = errors.New("quota reset account was not found")
	ErrResetCreditChanged   = errors.New("quota reset credit changed")
	ErrResetUnavailable     = errors.New("quota reset is unavailable")
)

type ResetStore interface {
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadSettings(context.Context) (map[string]any, error)
	ReadSecret(context.Context, string) (string, bool, error)
}

type ResetFence interface {
	WithWriteFence(context.Context, func() error) error
}

type ResetterConfig struct {
	Root   string
	Store  ResetStore
	Fence  ResetFence
	Client Client
}

type Resetter struct {
	root   string
	store  ResetStore
	fence  ResetFence
	client Client
}

type ResetWindow struct {
	Key             string `json:"key"`
	Label           string `json:"label"`
	PreviousResetAt *int64 `json:"previous_reset_at"`
}

type ResetCreditResult struct {
	Title      string `json:"title,omitempty"`
	ExpiresAt  *int64 `json:"expires_at,omitempty"`
	ResetType  string `json:"reset_type,omitempty"`
	Status     string `json:"status,omitempty"`
	RedeemedAt *int64 `json:"redeemed_at,omitempty"`
}

type ResetCreditOption struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	ExpiresAt *int64 `json:"expires_at,omitempty"`
}

type ResetInspection struct {
	Account          string              `json:"account"`
	AvailableCount   *int64              `json:"available_count"`
	DetailsTruncated bool                `json:"details_truncated"`
	Windows          []ResetWindow       `json:"windows"`
	Credits          []ResetCreditOption `json:"credits"`
}

type ResetResult struct {
	Account      string            `json:"account"`
	Windows      []ResetWindow     `json:"windows"`
	WindowsReset int64             `json:"windows_reset"`
	Code         string            `json:"code"`
	Credit       ResetCreditResult `json:"credit"`
}

type resetCredit struct {
	ID        string
	Title     string
	ExpiresAt *int64
}

// Inspect reads the current upstream weekly windows and selectable reset
// credits without consuming a credit. The Admin UI calls it only when an
// authenticated operator opens the reset dialog, keeping these details out of
// the initial account catalog and revalidating them again inside Reset.
func (resetter *Resetter) Inspect(ctx context.Context, rawAccountID string) (ResetInspection, error) {
	accountID := strings.ToLower(strings.TrimSpace(rawAccountID))
	if !accountIDPattern.MatchString(accountID) {
		return ResetInspection{}, fmt.Errorf("%w: invalid account", controlplane.ErrInvalidCatalogInput)
	}
	accounts, err := resetter.store.ReadAccounts(ctx)
	if err != nil {
		return ResetInspection{}, fmt.Errorf("read account before quota reset inspection: %w", err)
	}
	var account controlplane.Account
	found := false
	for _, item := range accounts {
		if item.ID == accountID {
			account = item
			found = true
			break
		}
	}
	if !found {
		return ResetInspection{}, ErrResetAccountNotFound
	}
	settings, err := resetter.store.ReadSettings(ctx)
	if err != nil {
		return ResetInspection{}, fmt.Errorf("read settings before quota reset inspection: %w", err)
	}
	auth, err := (OAuthLoader{Root: resetter.root}).Load(accountID)
	if err != nil {
		return ResetInspection{}, err
	}
	proxyURL, err := (ProxyResolver{Store: resetter.store, Settings: settings}).Resolve(ctx, account)
	if err != nil {
		return ResetInspection{}, err
	}
	usagePayload, err := resetter.client.Fetch(ctx, auth, proxyURL)
	if err != nil {
		return ResetInspection{}, err
	}
	creditPayload, err := resetter.client.FetchResetCredits(ctx, auth, proxyURL)
	if err != nil {
		return ResetInspection{}, err
	}
	inspection := ResetInspection{
		Account: accountID, AvailableCount: nonnegativeInt(creditPayload["available_count"]),
		Windows: make([]ResetWindow, 0), Credits: make([]ResetCreditOption, 0),
	}
	for _, window := range Normalize(accountID, usagePayload).WeeklyWindows {
		if window.Resettable {
			inspection.Windows = append(inspection.Windows, ResetWindow{
				Key: window.Key, Label: window.Label, PreviousResetAt: window.ResetAt,
			})
		}
	}
	for _, raw := range list(creditPayload["credits"]) {
		item := object(raw)
		id := strings.TrimSpace(stringValue(item["id"]))
		if id == "" || len(id) > 512 || containsControl(id) ||
			strings.TrimSpace(stringValue(item["status"])) != "available" || item["is_supported_by_plan"] == false {
			continue
		}
		inspection.Credits = append(inspection.Credits, ResetCreditOption{
			ID: id, Title: boundedResetText(item["title"], 120), ExpiresAt: resetTimestamp(item["expires_at"]),
		})
	}
	inspection.DetailsTruncated = inspection.AvailableCount != nil && *inspection.AvailableCount > int64(len(inspection.Credits))
	return inspection, nil
}

func NewResetter(config ResetterConfig) (*Resetter, error) {
	if strings.TrimSpace(config.Root) == "" || config.Store == nil || config.Fence == nil {
		return nil, errors.New("quota resetter requires a root, store, and ownership fence")
	}
	return &Resetter{
		root: strings.TrimSpace(config.Root), store: config.Store, fence: config.Fence, client: config.Client,
	}, nil
}

func (resetter *Resetter) Reset(
	ctx context.Context,
	accountID string,
	creditID string,
) (ResetResult, error) {
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	creditID = strings.TrimSpace(creditID)
	if !accountIDPattern.MatchString(accountID) || creditID == "" || len(creditID) > 512 || containsControl(creditID) {
		return ResetResult{}, fmt.Errorf("%w: invalid account or credit", controlplane.ErrInvalidCatalogInput)
	}
	accounts, err := resetter.store.ReadAccounts(ctx)
	if err != nil {
		return ResetResult{}, fmt.Errorf("read account before quota reset: %w", err)
	}
	var account controlplane.Account
	found := false
	for _, item := range accounts {
		if item.ID == accountID {
			account = item
			found = true
			break
		}
	}
	if !found {
		return ResetResult{}, ErrResetAccountNotFound
	}
	settings, err := resetter.store.ReadSettings(ctx)
	if err != nil {
		return ResetResult{}, fmt.Errorf("read settings before quota reset: %w", err)
	}
	auth, err := (OAuthLoader{Root: resetter.root}).Load(accountID)
	if err != nil {
		return ResetResult{}, err
	}
	proxyURL, err := (ProxyResolver{Store: resetter.store, Settings: settings}).Resolve(ctx, account)
	if err != nil {
		return ResetResult{}, err
	}

	var result ResetResult
	err = resetter.fence.WithWriteFence(ctx, func() error {
		usagePayload, requestError := resetter.client.Fetch(ctx, auth, proxyURL)
		if requestError != nil {
			return requestError
		}
		creditPayload, requestError := resetter.client.FetchResetCredits(ctx, auth, proxyURL)
		if requestError != nil {
			return requestError
		}
		selected, selectedFound := selectResetCredit(creditPayload, creditID)
		if !selectedFound {
			return ErrResetCreditChanged
		}
		quota := Normalize(accountID, usagePayload)
		eligibleWindows := make([]WeeklyWindow, 0)
		for _, window := range quota.WeeklyWindows {
			if window.Resettable {
				eligibleWindows = append(eligibleWindows, window)
			}
		}
		applicable := nonnegativeInt(object(usagePayload["rate_limit_reset_credits"])["applicable_available_count"])
		if applicable == nil || *applicable == 0 || len(eligibleWindows) == 0 {
			return ErrResetUnavailable
		}
		consumePayload, requestError := resetter.client.ConsumeResetCredit(ctx, auth, proxyURL, creditID)
		if requestError != nil {
			return requestError
		}
		result = buildResetResult(accountID, selected, eligibleWindows, consumePayload)
		return nil
	})
	return result, err
}

func selectResetCredit(payload map[string]any, creditID string) (resetCredit, bool) {
	for _, raw := range list(payload["credits"]) {
		item := object(raw)
		if strings.TrimSpace(stringValue(item["id"])) != creditID ||
			strings.TrimSpace(stringValue(item["status"])) != "available" || item["is_supported_by_plan"] == false {
			continue
		}
		return resetCredit{
			ID: creditID, Title: boundedResetText(item["title"], 120), ExpiresAt: resetTimestamp(item["expires_at"]),
		}, true
	}
	return resetCredit{}, false
}

func buildResetResult(
	account string,
	selected resetCredit,
	windows []WeeklyWindow,
	payload map[string]any,
) ResetResult {
	result := ResetResult{
		Account: account, Windows: make([]ResetWindow, 0, len(windows)),
		Code:   boundedResetText(payload["code"], 100),
		Credit: ResetCreditResult{Title: selected.Title, ExpiresAt: selected.ExpiresAt},
	}
	if value := nonnegativeInt(payload["windows_reset"]); value != nil {
		result.WindowsReset = *value
	}
	for _, window := range windows {
		result.Windows = append(result.Windows, ResetWindow{
			Key: window.Key, Label: window.Label, PreviousResetAt: window.ResetAt,
		})
	}
	credit := object(payload["credit"])
	result.Credit.ResetType = boundedResetText(credit["reset_type"], 64)
	result.Credit.Status = boundedResetText(credit["status"], 32)
	result.Credit.RedeemedAt = resetTimestamp(credit["redeemed_at"])
	return result
}

func boundedResetText(value any, maximum int) string {
	result := strings.TrimSpace(stringValue(value))
	if len(result) > maximum {
		result = result[:maximum]
	}
	return result
}

func resetTimestamp(value any) *int64 {
	if value == nil {
		return nil
	}
	var result int64
	switch typed := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseInt(typed.String(), 10, 64)
		if err != nil {
			return nil
		}
		result = parsed
	case int64:
		result = typed
	case int:
		result = int64(typed)
	case float64:
		result = int64(typed)
	case string:
		if parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64); err == nil {
			result = parsed
		} else if parsedTime, err := time.Parse(time.RFC3339, strings.TrimSpace(typed)); err == nil {
			result = parsedTime.Unix()
		} else {
			return nil
		}
	default:
		return nil
	}
	if result < 0 {
		result = 0
	}
	return &result
}
