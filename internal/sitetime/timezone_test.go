package sitetime

import "testing"

func TestTimezoneResolutionAndMigration(t *testing.T) {
	for _, test := range []struct {
		name     string
		settings map[string]any
		want     string
	}{
		{"new installation", map[string]any{}, DefaultName},
		{"legacy quota", map[string]any{"user_quota.timezone": "America/New_York"}, "America/New_York"},
		{"legacy notification only", map[string]any{"notification.timezone": "UTC"}, "UTC"},
		{"preserve quota boundary on conflict", map[string]any{"user_quota.timezone": "Europe/London", "notification.timezone": "UTC"}, "Europe/London"},
		{"explicit system wins", map[string]any{SettingKey: "Asia/Tokyo", "user_quota.timezone": "bad zone", "notification.timezone": "UTC"}, "Asia/Tokyo"},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.settings["unrelated"] = "preserved"
			name, err := Name(test.settings)
			if err != nil || name != test.want {
				t.Fatalf("Name = %q, %v", name, err)
			}
			if err := Migrate(test.settings); err != nil {
				t.Fatal(err)
			}
			if test.settings["unrelated"] != "preserved" {
				t.Fatal("unrelated setting changed")
			}
			if _, found := test.settings["user_quota.timezone"]; found {
				t.Fatal("legacy quota setting survived")
			}
			if _, found := test.settings["notification.timezone"]; found {
				t.Fatal("legacy notification setting survived")
			}
			if name, err := Name(test.settings); err != nil || name != test.want {
				t.Fatalf("migrated Name = %q, %v", name, err)
			}
			if test.name == "new installation" && test.settings[SettingKey] != nil {
				t.Fatal("default was incorrectly marked as explicitly selected")
			}
		})
	}
}

func TestInvalidTimezoneNeverFallsBackToProcessClock(t *testing.T) {
	for _, value := range []any{"", "Local", "Unknown/Timezone", 8} {
		if _, err := Name(map[string]any{SettingKey: value, "user_quota.timezone": "UTC"}); err == nil {
			t.Fatalf("invalid system timezone accepted: %#v", value)
		}
	}
}
