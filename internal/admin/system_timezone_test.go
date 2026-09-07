package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/collector"
	"github.com/Alfonsxh/codex-cpa-pool/internal/notifications"
	"github.com/gin-gonic/gin"
)

func TestSystemTimezoneUnifiesConsumersAndPreservesDSTBoundaries(t *testing.T) {
	server, store := newTestAdmin(t)
	server.configurationApplier = &recordingConfigurationApplier{}
	response := performAdminRequest(server, http.MethodPost, "/admin/api/settings/configuration", map[string]any{
		"confirm": "save", "values": map[string]any{"system.timezone": "America/New_York"},
	}, map[string]string{"X-Management-Key": "test-management-key"}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("save = %d %s", response.Code, response.Body.String())
	}
	settings, err := store.ReadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	collection, _, err := collector.RuntimeConfigFromSettings(settings)
	if err != nil || collection.WeekTimezone != "America/New_York" {
		t.Fatalf("collector = %#v, %v", collection, err)
	}
	notification, err := notifications.ParseConfig(settings)
	if err != nil || notification.TimezoneName != collection.WeekTimezone {
		t.Fatalf("notifications = %#v, %v", notification, err)
	}
	response = performAdminRequest(server, http.MethodGet, "/site-config.json", nil, nil, nil)
	var site publicSiteConfiguration
	decodeAdminResponse(t, response, &site)
	if site.Timezone != collection.WeekTimezone {
		t.Fatalf("browser timezone = %q", site.Timezone)
	}
	server.now = func() time.Time { return time.Date(2026, 3, 8, 18, 0, 0, 0, time.UTC) }
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/?window="+weekUsageWindow, nil)
	window, err := server.parseUsageWindow(c, false)
	if err != nil {
		t.Fatal(err)
	}
	start, _ := time.Parse(time.RFC3339, "2026-03-02T00:00:00-05:00")
	end, _ := time.Parse(time.RFC3339, "2026-03-09T00:00:00-04:00")
	if window.WindowStartAt == nil || window.WindowEndAt == nil || *window.WindowStartAt != start.Unix() || *window.WindowEndAt != end.Unix() {
		t.Fatalf("DST week must end at local Monday midnight (167 hours): %#v", window)
	}
}

func TestConfigurationMigratesLegacyTimezonesWithoutChangingQuotaBoundary(t *testing.T) {
	server, store := newTestAdmin(t)
	ctx := context.Background()
	if err := store.UpdateSettings(ctx, map[string]any{"user_quota.timezone": "Europe/London", "notification.timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}
	_, effective, _, _, err := server.currentConfiguration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := store.ReadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings["system.timezone"] != "Europe/London" || effective["system.timezone"] != "Europe/London" || settings["user_quota.timezone"] != nil || settings["notification.timezone"] != nil {
		t.Fatalf("migration = %#v", settings)
	}
}

func TestNotificationEndpointCannotOverrideSystemTimezone(t *testing.T) {
	server, store := newTestAdmin(t)
	ctx := context.Background()
	if err := store.UpdateSettings(ctx, map[string]any{"system.timezone": "Asia/Tokyo"}); err != nil {
		t.Fatal(err)
	}
	value := "UTC"
	var body notificationSettingsPayload
	body.Values.Timezone = &value
	if _, err := server.validatedNotificationChanges(ctx, body); err == nil {
		t.Fatal("independent notification timezone accepted")
	}
	value = "Asia/Tokyo"
	changes, err := server.validatedNotificationChanges(ctx, body)
	if err != nil || len(changes) != 0 {
		t.Fatalf("derived timezone should not write another setting: %#v, %v", changes, err)
	}
}
