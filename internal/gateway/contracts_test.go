package gateway

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

const fixtureExternalKey = "fixture-external-key"

type contractFixture struct {
	ExternalKey string `json:"external_key"`
	Paths       []struct {
		Path    string `json:"path"`
		Allowed bool   `json:"allowed"`
	} `json:"paths"`
	AuthSnapshot   json.RawMessage `json:"auth_snapshot"`
	QuotaSnapshot  json.RawMessage `json:"quota_snapshot"`
	QuotaHeartbeat json.RawMessage `json:"quota_heartbeat"`
}

func loadContractFixture(t *testing.T) contractFixture {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/gateway/contracts.json")
	if err != nil {
		t.Fatalf("read contract fixture: %v", err)
	}
	var fixture contractFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode contract fixture: %v", err)
	}
	if fixture.ExternalKey != fixtureExternalKey {
		t.Fatalf("fixture external key = %q, want %q", fixture.ExternalKey, fixtureExternalKey)
	}
	return fixture
}

func loadFixtureEngine(t *testing.T, loadedAt time.Time) *Engine {
	t.Helper()
	fixture := loadContractFixture(t)
	engine := NewEngine()
	if err := engine.LoadAuthSnapshot(bytes.NewReader(fixture.AuthSnapshot), loadedAt); err != nil {
		t.Fatalf("load auth snapshot: %v", err)
	}
	if err := engine.LoadQuotaSnapshot(bytes.NewReader(fixture.QuotaSnapshot), loadedAt); err != nil {
		t.Fatalf("load quota snapshot: %v", err)
	}
	if err := engine.LoadQuotaHeartbeat(bytes.NewReader(fixture.QuotaHeartbeat)); err != nil {
		t.Fatalf("load quota heartbeat: %v", err)
	}
	return engine
}

func TestPublicPathContract(t *testing.T) {
	fixture := loadContractFixture(t)
	for _, testCase := range fixture.Paths {
		t.Run(testCase.Path, func(t *testing.T) {
			if got := AllowedPublicPath(testCase.Path); got != testCase.Allowed {
				t.Fatalf("AllowedPublicPath(%q) = %v, want %v", testCase.Path, got, testCase.Allowed)
			}
		})
	}
}

func TestAuthorizeRejectsUnavailableAuthSnapshotBeforeHeader(t *testing.T) {
	decision := NewEngine().Authorize(time.Unix(1000, 0), "", true)
	assertDenied(t, decision, 503, "authentication_snapshot_unavailable")
	if decision.RetryAfterSeconds != 1 {
		t.Fatalf("RetryAfterSeconds = %d, want 1", decision.RetryAfterSeconds)
	}
}

func TestAuthorizePreservesBearerAndIdentityContract(t *testing.T) {
	now := time.Unix(1000, 0)
	engine := loadFixtureEngine(t, now)

	missing := engine.Authorize(now, "", true)
	assertDenied(t, missing, 401, "")

	invalid := engine.Authorize(now, "Bearer wrong-key", true)
	assertDenied(t, invalid, 401, "")

	decision := engine.Authorize(now, "bEaReR\t"+fixtureExternalKey+"  ", false)
	if !decision.Allowed || decision.Identity == nil {
		t.Fatalf("valid probe decision = %#v, want allowed identity", decision)
	}
	if decision.Identity.UserEmail != "fixture@example.com" ||
		decision.Identity.Account != "alpha" ||
		decision.Identity.Backend != "cliproxy-alpha:8317" ||
		decision.Identity.InternalKey != "fixture-internal-key" ||
		decision.Identity.Label != "fixture@example.com:alpha" {
		t.Fatalf("identity mismatch: %#v", decision.Identity)
	}
	if decision.UpstreamAuthorization() != "Bearer fixture-internal-key" {
		t.Fatalf("upstream authorization = %q", decision.UpstreamAuthorization())
	}
}

func TestAuthorizeAuthSnapshotFreshnessBoundary(t *testing.T) {
	loadedAt := time.Unix(1000, 0)
	engine := loadFixtureEngine(t, loadedAt)

	atBoundary := engine.Authorize(time.Unix(1005, 0), "Bearer "+fixtureExternalKey, false)
	if !atBoundary.Allowed {
		t.Fatalf("snapshot exactly five seconds old must remain available: %#v", atBoundary)
	}

	stale := engine.Authorize(time.Unix(1006, 0), "Bearer "+fixtureExternalKey, false)
	assertDenied(t, stale, 503, "authentication_snapshot_unavailable")
}

func TestAuthorizeWeeklyQuotaExceededContract(t *testing.T) {
	now := time.Unix(1000, 0)
	decision := loadFixtureEngine(t, now).Authorize(
		now,
		"Bearer "+fixtureExternalKey,
		true,
	)
	assertDenied(t, decision, 429, "weekly_user_token_quota_exceeded")
	if decision.RetryAfterSeconds != 1000 {
		t.Fatalf("RetryAfterSeconds = %d, want 1000", decision.RetryAfterSeconds)
	}
	want := &WeeklyQuota{
		UsedTokens:         1000,
		WeightedUsedTokens: 1000,
		RawUsedTokens:      600,
		LimitTokens:        1000,
		WeekEndAt:          2000,
		QuotaUnit:          "weighted_tokens",
	}
	if decision.Response == nil || decision.Response.UserWeeklyQuota == nil ||
		*decision.Response.UserWeeklyQuota != *want {
		t.Fatalf("weekly quota = %#v, want %#v", decision.Response, want)
	}
}

func TestAuthorizeQuotaFailuresRemainFailOpen(t *testing.T) {
	now := time.Unix(1000, 0)
	fixture := loadContractFixture(t)
	engine := NewEngine()
	if err := engine.LoadAuthSnapshot(bytes.NewReader(fixture.AuthSnapshot), now); err != nil {
		t.Fatalf("load auth snapshot: %v", err)
	}

	decision := engine.Authorize(now, "Bearer "+fixtureExternalKey, true)
	if !decision.Allowed || decision.Warning != "collector_last_success" {
		t.Fatalf("missing heartbeat decision = %#v, want fail-open collector warning", decision)
	}

	if err := engine.LoadQuotaSnapshot(bytes.NewReader(fixture.QuotaSnapshot), now); err != nil {
		t.Fatalf("load quota snapshot: %v", err)
	}
	if err := engine.LoadQuotaHeartbeat(bytes.NewReader(fixture.QuotaHeartbeat)); err != nil {
		t.Fatalf("load quota heartbeat: %v", err)
	}
	if err := engine.LoadAuthSnapshot(bytes.NewReader(fixture.AuthSnapshot), time.Unix(1301, 0)); err != nil {
		t.Fatalf("refresh auth snapshot: %v", err)
	}
	decision = engine.Authorize(time.Unix(1301, 0), "Bearer "+fixtureExternalKey, true)
	if !decision.Allowed || decision.Warning != "collector_last_success" {
		t.Fatalf("stale collector decision = %#v, want fail-open collector warning", decision)
	}
}

func TestInvalidSnapshotDoesNotReplaceLastGoodGeneration(t *testing.T) {
	now := time.Unix(1000, 0)
	fixture := loadContractFixture(t)
	engine := NewEngine()
	if err := engine.LoadAuthSnapshot(bytes.NewReader(fixture.AuthSnapshot), now); err != nil {
		t.Fatalf("load auth snapshot: %v", err)
	}

	var invalidSnapshot AuthSnapshot
	if err := json.Unmarshal(fixture.AuthSnapshot, &invalidSnapshot); err != nil {
		t.Fatalf("decode auth snapshot for invalid candidate: %v", err)
	}
	invalidSnapshot.Records[0].ExternalKeySHA256 = "invalid"
	invalid, err := json.Marshal(invalidSnapshot)
	if err != nil {
		t.Fatalf("encode invalid auth snapshot: %v", err)
	}
	if err := engine.LoadAuthSnapshot(bytes.NewReader(invalid), time.Unix(1001, 0)); err == nil {
		t.Fatal("invalid auth snapshot unexpectedly accepted")
	}

	decision := engine.Authorize(time.Unix(1001, 0), "Bearer "+fixtureExternalKey, false)
	if !decision.Allowed {
		t.Fatalf("last good generation was replaced: %#v", decision)
	}
}

func TestQuotaSnapshotNormalizesStableNumericContract(t *testing.T) {
	raw := `{
		"version":1,
		"generation":"dddddddddddddddddddddddddddddddd",
		"generated_at":100.9,
		"records":[{
			"user_email":"fixture@example.com",
			"week_start_at":100.9,
			"week_end_at":200.9,
			"limit_tokens":1000.9,
			"used_tokens":240.9,
			"raw_used_tokens":120.9,
			"weighted_raw_used_tokens":260.9
		}]
	}`
	snapshot, err := ParseQuotaSnapshot(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("ParseQuotaSnapshot: %v", err)
	}
	record := snapshot.Records[0]
	if record.WeekStartAt != 100 || record.WeekEndAt != 200 ||
		record.LimitTokens != 1000 || record.UsedTokens != 240 ||
		record.RawUsedTokens != 120 || record.WeightedRawUsedTokens != 260 {
		t.Fatalf("normalized quota record = %#v", record)
	}
}

func TestSnapshotOptionalNumbersFollowNumericFallbacks(t *testing.T) {
	authRaw := `{
		"version":1,
		"generation":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		"generated_at":"123.5",
		"records":[]
	}`
	auth, err := ParseAuthSnapshot(strings.NewReader(authRaw))
	if err != nil {
		t.Fatalf("ParseAuthSnapshot: %v", err)
	}
	if auth.GeneratedAt != 123.5 {
		t.Fatalf("auth generated_at = %v, want 123.5", auth.GeneratedAt)
	}

	quotaRaw := `{
		"version":1,
		"generation":"ffffffffffffffffffffffffffffffff",
		"generated_at":"invalid",
		"records":[{
			"user_email":"fixture@example.com",
			"week_start_at":100,
			"week_end_at":200,
			"limit_tokens":1000,
			"used_tokens":240,
			"raw_used_tokens":"120.9",
			"weighted_raw_used_tokens":{"invalid":true}
		}]
	}`
	quota, err := ParseQuotaSnapshot(strings.NewReader(quotaRaw))
	if err != nil {
		t.Fatalf("ParseQuotaSnapshot: %v", err)
	}
	if quota.GeneratedAt != 0 || quota.Records[0].RawUsedTokens != 120 ||
		quota.Records[0].WeightedRawUsedTokens != 240 {
		t.Fatalf("quota tonumber compatibility mismatch: %#v", quota)
	}
}

func TestSnapshotSizeLimit(t *testing.T) {
	oversized := strings.NewReader(strings.Repeat(" ", MaxSnapshotBytes+1))
	if _, err := ParseAuthSnapshot(oversized); err == nil {
		t.Fatal("oversized snapshot unexpectedly accepted")
	}
}

func TestFloorInt64RejectsPositiveOverflowBoundary(t *testing.T) {
	if _, err := floorInt64(float64(uint64(1) << 63)); err == nil {
		t.Fatal("positive int64 overflow boundary unexpectedly accepted")
	}
	value, err := floorInt64(-float64(uint64(1) << 63))
	if err != nil || value != -1<<63 {
		t.Fatalf("minimum int64 boundary = (%d, %v), want (%d, nil)", value, err, int64(-1<<63))
	}
}

func FuzzExtractBearer(f *testing.F) {
	for _, seed := range []string{
		"Bearer key",
		"bearer key ",
		"Basic key",
		"Bearer",
		"Bearer key extra",
		"",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, header string) {
		key, ok := ExtractBearer(header)
		if ok && (key == "" || strings.ContainsAny(key, " \t\r\n")) {
			t.Fatalf("accepted invalid bearer token %q from %q", key, header)
		}
	})
}

func assertDenied(t *testing.T, decision Decision, status int, code string) {
	t.Helper()
	if decision.Allowed || decision.Status != status || decision.Response == nil {
		t.Fatalf("decision = %#v, want denied status %d", decision, status)
	}
	if decision.Response.Error.Code != code {
		t.Fatalf("error code = %q, want %q", decision.Response.Error.Code, code)
	}
}
