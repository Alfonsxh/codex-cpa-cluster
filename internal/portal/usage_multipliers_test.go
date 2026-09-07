package portal

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
)

func TestPortalUsageMultipliersFollowCurrentConfiguration(t *testing.T) {
	for _, path := range []string{
		"/usage/me/usage-trend?dimension=model_reasoning",
		"/usage/me/usage-breakdown?window=3600",
	} {
		t.Run(path, func(t *testing.T) {
			fixture := newPortalFixture(t)
			fixture.sessions.sessions["session"] = usage.PortalSession{User: "alice@example.com", ExpiresAt: 11_000}
			fixture.identity.settings["unrelated.secret"] = "never-expose-this-setting"
			for _, modelMultiplier := range []float64{4, 1.5, 1} {
				if modelMultiplier != 4 {
					fixture.identity.settings["user_quota.model_multiplier.gpt-6-astra"] = modelMultiplier
					fixture.identity.settings["user_quota.reasoning_multiplier.max"] = 2.5
				}
				response := fixture.request(http.MethodGet, path, "", "session")
				if response.Code != http.StatusOK {
					t.Fatalf("usage response = %d %s", response.Code, response.Body.String())
				}
				var body struct {
					Current usageDisplayMultipliers `json:"current_multipliers"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				wantMax := 2.0
				if modelMultiplier != 4 {
					wantMax = 2.5
				}
				if body.Current.Models["gpt-6-astra"] != modelMultiplier || body.Current.Models["unknown"] != 1 ||
					body.Current.ReasoningEfforts["max"] != wantMax || body.Current.ReasoningEfforts["xhigh"] != 1 ||
					body.Current.ReasoningEfforts["ultra"] != 3 {
					t.Fatalf("current display policy = %#v", body.Current)
				}
				if strings.Contains(response.Body.String(), "never-expose-this-setting") {
					t.Fatal("usage response exposed unrelated configuration")
				}
			}
		})
	}
}
