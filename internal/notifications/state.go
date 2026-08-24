package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

func DefaultRuntimeState() RuntimeState {
	return RuntimeState{
		Version:      RuntimeStateVersion,
		Scheduled:    make(map[string]ScheduledRecord),
		QuotaAlerts:  make(map[string]AlertRecord),
		QuotaWindows: make(map[string]WindowRecord),
	}
}

func ReadRuntimeState(ctx context.Context, store interface {
	ReadRuntimeState(context.Context, string, any) (bool, error)
}) (RuntimeState, bool, error) {
	state := DefaultRuntimeState()
	var raw json.RawMessage
	found, err := store.ReadRuntimeState(ctx, RuntimeStateName, &raw)
	if err != nil {
		return state, false, fmt.Errorf("read notification runtime state: %w", err)
	}
	if !found {
		return state, false, nil
	}
	var fields map[string]json.RawMessage
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &fields) != nil {
		return state, true, nil
	}
	if !decodeField(fields["version"], &state.Version) || state.Version != RuntimeStateVersion {
		return DefaultRuntimeState(), true, nil
	}
	decodeRecordMap(fields["scheduled"], &state.Scheduled)
	decodeRecordMap(fields["quota_alerts"], &state.QuotaAlerts)
	decodeRecordMap(fields["quota_windows"], &state.QuotaWindows)
	decodeField(fields["heartbeat_at"], &state.HeartbeatAt)
	decodeField(fields["last_success_at"], &state.LastSuccessAt)
	decodeField(fields["last_error"], &state.LastError)
	decodeField(fields["next_schedule_at"], &state.NextScheduleAt)
	decodeField(fields["quota_checked_at"], &state.QuotaCheckedAt)
	return state, true, nil
}

func decodeField(raw json.RawMessage, destination any) bool {
	return len(raw) > 0 && json.Unmarshal(raw, destination) == nil
}

func decodeRecordMap[T any](raw json.RawMessage, destination *map[string]T) {
	var records map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &records) != nil {
		*destination = make(map[string]T)
		return
	}
	result := make(map[string]T, len(records))
	for key, value := range records {
		var record T
		if json.Unmarshal(value, &record) == nil {
			result[key] = record
		}
	}
	*destination = result
}

func HealthyRuntimeState(
	state RuntimeState,
	found bool,
	enabled bool,
	now time.Time,
	maxAge time.Duration,
) bool {
	if !found || state.Version != RuntimeStateVersion || state.HeartbeatAt == nil ||
		*state.HeartbeatAt <= 0 || maxAge <= 0 {
		return false
	}
	age := now.Unix() - *state.HeartbeatAt
	if age < 0 || age > int64(maxAge/time.Second) {
		return false
	}
	return !enabled || state.LastError == ""
}
