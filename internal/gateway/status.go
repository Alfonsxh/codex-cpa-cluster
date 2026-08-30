package gateway

import "time"

type SnapshotKindStatus struct {
	ActiveGeneration        string  `json:"active_generation"`
	PreviousGeneration      string  `json:"previous_generation"`
	GeneratedAt             float64 `json:"generated_at"`
	RecordCount             int     `json:"record_count"`
	SnapshotLoaderSuccessAt int64   `json:"snapshot_loader_success_at"`
}

type QuotaSnapshotStatus struct {
	SnapshotKindStatus
	HeartbeatAt         int64 `json:"heartbeat_at"`
	HeartbeatOK         bool  `json:"heartbeat_ok"`
	HeartbeatStaleAfter int64 `json:"heartbeat_stale_after"`
	LastSuccessAt       int64 `json:"last_success_at"`
	FailOpenAfter       int64 `json:"fail_open_after"`
}

type Status struct {
	Auth  SnapshotKindStatus  `json:"auth"`
	Quota QuotaSnapshotStatus `json:"quota"`
}

// AuthenticationReady reports whether a newly started Gateway has loaded a
// current, structurally valid API Key snapshot. An intentionally empty
// snapshot is ready for a new installation: requests still fail closed with
// 401 until the operator provisions users, but Admin/Web can start normally.
// The public liveness endpoint remains independent so a running slot can fail
// closed on stale state without being killed.
func (engine *Engine) AuthenticationReady(now time.Time) bool {
	auth := engine.auth.Load()
	if auth == nil || auth.generation == "" || auth.loadedAt <= 0 {
		return false
	}
	age := now.Unix() - auth.loadedAt
	return age >= 0 && age <= int64(AuthSnapshotMaxAge/time.Second)
}

// Status returns the stable operational fields of the internal snapshot
// endpoint. It intentionally contains no credentials or per-user records.
func (engine *Engine) Status() Status {
	result := Status{}
	if auth := engine.auth.Load(); auth != nil {
		result.Auth = SnapshotKindStatus{
			ActiveGeneration:        auth.generation,
			PreviousGeneration:      auth.previousGeneration,
			GeneratedAt:             auth.generatedAt,
			RecordCount:             auth.recordCount,
			SnapshotLoaderSuccessAt: auth.loadedAt,
		}
	}
	if quota := engine.quota.Load(); quota != nil {
		result.Quota.SnapshotKindStatus = SnapshotKindStatus{
			ActiveGeneration:        quota.generation,
			PreviousGeneration:      quota.previousGeneration,
			GeneratedAt:             quota.generatedAt,
			RecordCount:             quota.recordCount,
			SnapshotLoaderSuccessAt: quota.loadedAt,
		}
	}
	if heartbeat := engine.heartbeat.Load(); heartbeat != nil {
		result.Quota.HeartbeatAt = heartbeat.UpdatedAt
		result.Quota.HeartbeatOK = heartbeat.OK
		result.Quota.HeartbeatStaleAfter = heartbeat.StaleAfterSeconds
		result.Quota.LastSuccessAt = heartbeat.LastSuccessAt
		result.Quota.FailOpenAfter = heartbeat.FailOpenAfterSeconds
	}
	return result
}
