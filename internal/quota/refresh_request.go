package quota

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

const (
	RefreshRequestStateName = "official_quota_refresh_request"
	refreshRequestVersion   = 1
	ForceRefreshMinimumAge  = 15 * time.Second
)

// RefreshRequestState is a secret-free single-flight handoff between the Admin
// process and the sole quota worker. A newer request may replace RequestID
// while an older refresh is running; the worker completes the captured ID and
// will then observe the newer request as still pending.
type RefreshRequestState struct {
	Version     int    `json:"version"`
	RequestID   string `json:"request_id"`
	RequestedAt int64  `json:"requested_at"`
	StartedID   string `json:"started_id"`
	StartedAt   int64  `json:"started_at"`
	CompletedID string `json:"completed_id"`
	CompletedAt int64  `json:"completed_at"`
	LastError   string `json:"last_error"`
}

type RefreshRequestStore interface {
	ReadRuntimeState(context.Context, string, any) (bool, error)
	PatchRuntimeState(context.Context, string, map[string]any) error
}

func ReadRefreshRequest(
	ctx context.Context,
	store interface {
		ReadRuntimeState(context.Context, string, any) (bool, error)
	},
) (RefreshRequestState, bool, error) {
	var raw json.RawMessage
	found, err := store.ReadRuntimeState(ctx, RefreshRequestStateName, &raw)
	if err != nil {
		return RefreshRequestState{}, false, fmt.Errorf("read official quota refresh request: %w", err)
	}
	if !found {
		return RefreshRequestState{}, false, nil
	}
	var state RefreshRequestState
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &state) != nil || state.Version != refreshRequestVersion {
		return RefreshRequestState{}, true, nil
	}
	return state, true, nil
}

func (state RefreshRequestState) Pending() bool {
	return state.Version == refreshRequestVersion && state.RequestID != "" && state.CompletedID != state.RequestID
}

// RequestRefresh records a new request only when there is no in-flight request
// and the persistent quota snapshot is old enough to match the v1 force-refresh
// throttle. Runtime-state writes are fenced by the owning control-plane Store.
func RequestRefresh(ctx context.Context, store RefreshRequestStore, now time.Time) (RefreshRequestState, bool, error) {
	request, _, err := ReadRefreshRequest(ctx, store)
	if err != nil {
		return RefreshRequestState{}, false, err
	}
	if request.Pending() {
		return request, false, nil
	}
	quotaState, quotaFound, err := ReadState(ctx, store)
	if err != nil {
		return RefreshRequestState{}, false, err
	}
	if quotaFound && quotaState.Snapshot.GeneratedAt > 0 {
		age := now.Unix() - quotaState.Snapshot.GeneratedAt
		if age >= 0 && age < int64(ForceRefreshMinimumAge/time.Second) {
			return request, false, nil
		}
	}
	requestID, err := newRefreshRequestID()
	if err != nil {
		return RefreshRequestState{}, false, err
	}
	request = RefreshRequestState{
		Version: refreshRequestVersion, RequestID: requestID, RequestedAt: now.Unix(),
		StartedID: request.StartedID, StartedAt: request.StartedAt,
		CompletedID: request.CompletedID, CompletedAt: request.CompletedAt,
	}
	if err := store.PatchRuntimeState(ctx, RefreshRequestStateName, map[string]any{
		"version":      refreshRequestVersion,
		"request_id":   request.RequestID,
		"requested_at": request.RequestedAt,
		"last_error":   "",
	}); err != nil {
		return RefreshRequestState{}, false, fmt.Errorf("request official quota refresh: %w", err)
	}
	return request, true, nil
}

func MarkRefreshStarted(ctx context.Context, store RefreshRequestStore, requestID string, now time.Time) error {
	if requestID == "" {
		return nil
	}
	if err := store.PatchRuntimeState(ctx, RefreshRequestStateName, map[string]any{
		"version":    refreshRequestVersion,
		"started_id": requestID,
		"started_at": now.Unix(),
		"last_error": "",
	}); err != nil {
		return fmt.Errorf("mark official quota refresh started: %w", err)
	}
	return nil
}

func MarkRefreshCompleted(
	ctx context.Context,
	store RefreshRequestStore,
	requestID string,
	now time.Time,
	runError error,
) error {
	if requestID == "" {
		return nil
	}
	if err := store.PatchRuntimeState(ctx, RefreshRequestStateName, map[string]any{
		"version":      refreshRequestVersion,
		"completed_id": requestID,
		"completed_at": now.Unix(),
		"last_error":   boundedError(runError, 500),
	}); err != nil {
		return fmt.Errorf("mark official quota refresh completed: %w", err)
	}
	return nil
}

func newRefreshRequestID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("create official quota refresh request id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
