package admin

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/notifications"
)

func TestNotificationStatusSeparatesSchedulerFromDelivery(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 6, 20, 0, 0, time.UTC) // 14:20 in the site timezone.
	for _, tc := range []struct {
		name      string
		heartbeat int64
		enabled   bool
		webhook   bool
		worker    string
		next      bool
	}{
		{"never started", 0, true, true, "not_started", false},
		{"manual success cannot revive scheduler", now.Add(-4 * time.Minute).Unix(), true, true, "heartbeat_lost", false},
		{"future heartbeat", now.Add(time.Second).Unix(), true, true, "heartbeat_lost", false},
		{"fresh despite historical delivery error", now.Unix(), true, true, "running", true},
		{"freshness boundary", now.Add(-3 * time.Minute).Unix(), true, true, "running", true},
		{"just expired", now.Add(-3*time.Minute - time.Second).Unix(), true, true, "heartbeat_lost", false},
		{"disabled but resident", now.Unix(), false, true, "running", false},
		{"unconfigured but resident", now.Unix(), true, false, "running", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, store := newTestAdmin(t)
			server.now = func() time.Time { return now }
			if err := store.UpdateSettings(ctx, map[string]any{
				"notification.enabled": tc.enabled, "notification.daily_times": "09:00,18:00", "system.timezone": "Asia/Shanghai",
			}); err != nil {
				t.Fatal(err)
			}
			if tc.webhook {
				if err := store.WriteSecret(ctx, "wecom_webhook", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test-notification-placeholder"); err != nil {
					t.Fatal(err)
				}
			}
			state := notifications.DefaultRuntimeState()
			if tc.heartbeat != 0 {
				state.HeartbeatAt = &tc.heartbeat
			}
			success, staleSchedule := now.Unix(), now.Add(-5*time.Hour).Unix()
			state.LastSuccessAt, state.NextScheduleAt = &success, &staleSchedule
			state.LastError = "previous send failed"
			if err := store.WriteRuntimeState(ctx, notifications.RuntimeStateName, state); err != nil {
				t.Fatal(err)
			}
			read := func() notificationStatus {
				t.Helper()
				response := performAdminRequest(server, http.MethodGet, "/admin/api/settings/notifications", nil, map[string]string{"X-Management-Key": "test-management-key"}, nil)
				if response.Code != http.StatusOK {
					t.Fatalf("status = %d: %s", response.Code, response.Body.String())
				}
				var payload notificationSettingsResponse
				decodeAdminResponse(t, response, &payload)
				return payload.Notifications
			}
			status := read()
			if status.WorkerStatus != tc.worker || (status.NextScheduleAt != nil) != tc.next ||
				status.LastSuccessAt == nil || *status.LastSuccessAt != success || status.LastError != state.LastError {
				t.Fatalf("status = %#v", status)
			}
			if tc.next {
				want := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC).Unix()
				if *status.NextScheduleAt != want {
					t.Fatalf("next = %d, want %d", *status.NextScheduleAt, want)
				}
				if err := store.UpdateSettings(ctx, map[string]any{"notification.daily_times": "15:00"}); err != nil {
					t.Fatal(err)
				}
				changed := read()
				if changed.NextScheduleAt == nil || *changed.NextScheduleAt != now.Add(40*time.Minute).Unix() {
					t.Fatalf("edited schedule not reflected: %#v", changed)
				}
			}
			unchanged, _, err := notifications.ReadRuntimeState(ctx, store)
			if err != nil || unchanged.NextScheduleAt == nil || *unchanged.NextScheduleAt != staleSchedule {
				t.Fatalf("status read mutated scheduler state: %#v, %v", unchanged, err)
			}
		})
	}
}
