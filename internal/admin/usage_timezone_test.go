package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestUsageWindowsDefaultToBeijingMidnight(t *testing.T) {
	for _, test := range []struct {
		name, now, today, week string
	}{
		{"before UTC midnight", "2026-09-07T01:30:00+08:00", "2026-09-07T00:00:00+08:00", "2026-09-07T00:00:00+08:00"},
		{"after UTC midnight", "2026-09-05T14:00:00+08:00", "2026-09-05T00:00:00+08:00", "2026-08-31T00:00:00+08:00"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, _ := newTestAdmin(t)
			now, err := time.Parse(time.RFC3339, test.now)
			if err != nil {
				t.Fatal(err)
			}
			// The process clock may use UTC; business boundaries must not.
			server.now = func() time.Time { return now.UTC() }
			for window, want := range map[string]string{todayUsageWindow: test.today, weekUsageWindow: test.week} {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodGet, "/?window="+window, nil)
				result, err := server.parseUsageWindow(c, false)
				start, _ := time.Parse(time.RFC3339, want)
				if err != nil || result.WindowTimezone != "Asia/Shanghai" || result.WindowStartAt == nil || *result.WindowStartAt != start.Unix() {
					t.Fatalf("%s boundary = %#v, %v; want %s", window, result, err, want)
				}
				if window == weekUsageWindow && (result.WindowEndAt == nil || *result.WindowEndAt != start.AddDate(0, 0, 7).Unix()) {
					t.Fatalf("week reset = %#v; want next Monday midnight", result)
				}
			}
		})
	}
}

func TestUsageWindowsPreserveExplicitTimezone(t *testing.T) {
	server, store := newTestAdmin(t)
	if err := store.UpdateSettings(context.Background(), map[string]any{"user_quota.timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 5, 6, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return now }
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/?window=today", nil)
	result, err := server.parseUsageWindow(c, false)
	if err != nil || result.WindowTimezone != "UTC" || result.WindowStartAt == nil || *result.WindowStartAt != now.Add(-6*time.Hour).Unix() {
		t.Fatalf("explicit timezone = %#v, %v", result, err)
	}
}
