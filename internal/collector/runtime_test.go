package collector

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/usage"
)

func TestRuntimeCollectsEnabledAccountsAndPublishesQuota(t *testing.T) {
	teamID := "team-platform"
	control := &stubControlCatalog{
		accounts: []controlplane.Account{
			{ID: "alpha", GroupEnabled: true},
			{ID: "disabled", GroupEnabled: false},
		},
		records: []controlplane.KeyRecord{
			{Key: "external-key", Label: "alice:alpha", User: "alice@example.com", Account: "alpha", Status: "active"},
			{Key: "old-key", Label: "alice:old", User: "alice@example.com", Account: "alpha", Status: "rotated"},
		},
		internal: map[string]controlplane.InternalKey{
			"alice@example.com": {Key: "cpa_internal_test", Status: "active"},
		},
		teams: map[string]controlplane.UserTeamClassification{
			"alice@example.com": {TeamID: &teamID, TeamMembershipVersion: 3},
		},
	}
	limit := int64(1000)
	writer := &stubRuntimeWriter{quotas: map[string]usage.WeeklyQuota{
		"alice@example.com": {WeekStartAt: 100, WeekEndAt: 200, LimitTokens: &limit},
	}}
	publisher := &stubQuotaPublisher{}
	var queueAccounts []string
	runtime := &Runtime{
		Control: control, Writer: writer, Publisher: publisher,
		QueueFactory: func(account, address, password string, batchSize int) (BatchDrainer, error) {
			queueAccounts = append(queueAccounts, account)
			if address != "cliproxy-alpha:8317" || password != "management-secret" || batchSize != 50 {
				t.Fatalf("queue config = %s, %s, %d", address, password, batchSize)
			}
			return stubDrainer{batches: [][][]byte{{[]byte(`{"request_id":"one"}`)}}}, nil
		},
		Config: RuntimeConfig{
			ManagementKey: "management-secret", BatchSize: 50, WeekTimezone: "UTC",
			ResetPersonalWeeklyOnNewWeek: true, HeartbeatStaleAfterSeconds: 15,
			QuotaFailOpenAfterSeconds: 300,
		},
	}
	result, err := runtime.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Inserted != 1 || len(result.Errors) != 0 || result.Snapshot.Records != 1 {
		t.Fatalf("run result = %#v", result)
	}
	if strings.Join(queueAccounts, ",") != "alpha" {
		t.Fatalf("queue accounts = %#v", queueAccounts)
	}
	if len(writer.identities) != 3 {
		t.Fatalf("identities = %#v", writer.identities)
	}
	if writer.teams["alice@example.com"].TeamID != "team-platform" ||
		writer.teams["alice@example.com"].MembershipVersion != 3 {
		t.Fatalf("team identities = %#v", writer.teams)
	}
	if len(control.ensuredUsers) != 1 || control.ensuredUsers[0] != "alice@example.com" {
		t.Fatalf("active users = %#v", control.ensuredUsers)
	}
	if len(publisher.heartbeats) != 1 || !publisher.heartbeats[0].ok {
		t.Fatalf("heartbeats = %#v", publisher.heartbeats)
	}
}

func TestRuntimeRedactsManagementKeyAndPublishesFailedHeartbeat(t *testing.T) {
	control := &stubControlCatalog{
		accounts: []controlplane.Account{{ID: "alpha", GroupEnabled: true}},
		records:  []controlplane.KeyRecord{}, internal: map[string]controlplane.InternalKey{},
		teams: map[string]controlplane.UserTeamClassification{},
	}
	writer := &stubRuntimeWriter{quotas: map[string]usage.WeeklyQuota{}}
	publisher := &stubQuotaPublisher{}
	runtime := &Runtime{
		Control: control, Writer: writer, Publisher: publisher,
		QueueFactory: func(string, string, string, int) (BatchDrainer, error) {
			return nil, errors.New("server echoed management-secret")
		},
		Config: RuntimeConfig{
			ManagementKey: "management-secret", BatchSize: 1, WeekTimezone: "UTC",
			ResetPersonalWeeklyOnNewWeek: true, HeartbeatStaleAfterSeconds: 15,
			QuotaFailOpenAfterSeconds: 300,
		},
	}
	result, err := runtime.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(result.Errors) != 1 || strings.Contains(result.Errors[0], "management-secret") ||
		!strings.Contains(result.Errors[0], "[REDACTED]") {
		t.Fatalf("errors = %#v", result.Errors)
	}
	if len(publisher.heartbeats) != 1 || publisher.heartbeats[0].ok ||
		strings.Contains(publisher.heartbeats[0].errorText, "management-secret") {
		t.Fatalf("heartbeats = %#v", publisher.heartbeats)
	}
}

func TestRuntimeConfigUsesControlSettingsAndValidatesBounds(t *testing.T) {
	defaults := DefaultRuntimeConfig()
	if defaults.WeekTimezone != "Asia/Shanghai" {
		t.Fatalf("default runtime timezone = %q", defaults.WeekTimezone)
	}
	config, interval, err := RuntimeConfigFromSettings(map[string]any{
		"collector.interval_seconds":                   1.5,
		"collector.batch_size":                         float64(250),
		"user_quota.timezone":                          "Asia/Shanghai",
		"user_quota.reset_personal_weekly_on_new_week": false,
		"user_quota.default_weekly_tokens":             float64(900),
		"user_quota.fail_open_after_seconds":           float64(600),
		"user_quota.reasoning_multiplier.max":          2.5,
	})
	if err != nil {
		t.Fatalf("RuntimeConfigFromSettings: %v", err)
	}
	if interval != 1500*time.Millisecond || config.BatchSize != 250 ||
		config.WeekTimezone != "Asia/Shanghai" || config.ResetPersonalWeeklyOnNewWeek ||
		config.DefaultWeeklyTokens == nil || *config.DefaultWeeklyTokens != 900 ||
		config.ReasoningMultipliers["user_quota.reasoning_multiplier.max"] != 2.5 {
		t.Fatalf("runtime config = %#v, interval %s", config, interval)
	}
	if _, _, err := RuntimeConfigFromSettings(map[string]any{"collector.batch_size": float64(501)}); err == nil {
		t.Fatal("oversized batch setting was accepted")
	}
}

func TestFencedRuntimeWriterRejectsUsageMutationBeforeInnerWriter(t *testing.T) {
	sentinel := errors.New("lease generation lost")
	inner := &stubRuntimeWriter{quotas: map[string]usage.WeeklyQuota{"alice@example.com": {}}}
	fence := &stubWriteFence{err: sentinel}
	writer, err := NewFencedRuntimeWriter(inner, fence)
	if err != nil {
		t.Fatalf("NewFencedRuntimeWriter: %v", err)
	}
	if _, err := writer.SyncIdentities(context.Background(), []usage.Identity{{UserEmail: "alice@example.com"}}); !errors.Is(err, sentinel) {
		t.Fatalf("fenced SyncIdentities error = %v", err)
	}
	if len(inner.identities) != 0 || fence.calls != 1 {
		t.Fatalf("rejected fenced write = identities %#v fence calls %d", inner.identities, fence.calls)
	}
	if _, err := writer.WeeklyQuotas(context.Background(), []string{"alice@example.com"}, nil); err != nil {
		t.Fatalf("read-only WeeklyQuotas: %v", err)
	}
	if fence.calls != 1 {
		t.Fatalf("read-only query acquired write fence %d times", fence.calls)
	}
}

func TestFencedQuotaPublisherRejectsSnapshotAndHeartbeatBeforeInnerPublisher(t *testing.T) {
	sentinel := errors.New("lease generation lost")
	inner := &stubQuotaPublisher{}
	fence := &stubWriteFence{err: sentinel}
	publisher, err := NewFencedQuotaPublisher(inner, fence)
	if err != nil {
		t.Fatalf("NewFencedQuotaPublisher: %v", err)
	}
	if _, err := publisher.PublishQuotaSnapshot(context.Background(), map[string]usage.WeeklyQuota{}); !errors.Is(err, sentinel) {
		t.Fatalf("fenced quota snapshot error = %v", err)
	}
	if _, err := publisher.PublishQuotaHeartbeat(context.Background(), true, "", 15, 300); !errors.Is(err, sentinel) {
		t.Fatalf("fenced quota heartbeat error = %v", err)
	}
	if fence.calls != 2 || len(inner.heartbeats) != 0 {
		t.Fatalf("rejected quota writes = fence calls %d heartbeats %#v", fence.calls, inner.heartbeats)
	}
}

type stubControlCatalog struct {
	accounts     []controlplane.Account
	records      []controlplane.KeyRecord
	internal     map[string]controlplane.InternalKey
	teams        map[string]controlplane.UserTeamClassification
	ensuredUsers []string
}

func (catalog *stubControlCatalog) ReadAccounts(context.Context) ([]controlplane.Account, error) {
	return catalog.accounts, nil
}

func (catalog *stubControlCatalog) ReadKeyRecords(context.Context) ([]controlplane.KeyRecord, error) {
	return catalog.records, nil
}

func (catalog *stubControlCatalog) EnsureInternalKeys(
	ctx context.Context,
	users []string,
) (map[string]controlplane.InternalKey, error) {
	catalog.ensuredUsers = append([]string(nil), users...)
	return catalog.internal, nil
}

func (catalog *stubControlCatalog) ReadUserTeams(
	context.Context,
	[]string,
) (map[string]controlplane.UserTeamClassification, error) {
	return catalog.teams, nil
}

type stubRuntimeWriter struct {
	identities []usage.Identity
	teams      map[string]usage.TeamIdentity
	quotas     map[string]usage.WeeklyQuota
	lastError  string
}

func (writer *stubRuntimeWriter) IngestEvents(
	context.Context,
	string,
	[]usage.Event,
	map[string]float64,
) (usage.IngestCounters, error) {
	return usage.IngestCounters{Received: 1, Inserted: 1}, nil
}

func (writer *stubRuntimeWriter) SyncIdentities(_ context.Context, values []usage.Identity) (int, error) {
	writer.identities = append([]usage.Identity(nil), values...)
	return len(values), nil
}

func (writer *stubRuntimeWriter) SyncUserTeams(_ context.Context, values map[string]usage.TeamIdentity) (int, error) {
	writer.teams = values
	return len(values), nil
}

func (writer *stubRuntimeWriter) EnsureUsageBreakdownStarted(context.Context) (int64, error) {
	return 100, nil
}

func (writer *stubRuntimeWriter) EnsureWeekTimezone(context.Context, string) (bool, error) {
	return false, nil
}

func (writer *stubRuntimeWriter) ConfigurePersonalQuotaReset(
	context.Context,
	bool,
	bool,
) (usage.QuotaResetConfiguration, error) {
	return usage.QuotaResetConfiguration{}, nil
}

func (writer *stubRuntimeWriter) WeeklyQuotas(
	context.Context,
	[]string,
	*int64,
) (map[string]usage.WeeklyQuota, error) {
	return writer.quotas, nil
}

func (writer *stubRuntimeWriter) UpdateCollectorStatus(_ context.Context, lastError string) error {
	writer.lastError = lastError
	return nil
}

func (writer *stubRuntimeWriter) RebuildWeeklyUsage(context.Context) (usage.RebuildResult, error) {
	return usage.RebuildResult{Backfilled: true}, nil
}

type stubQuotaPublisher struct {
	heartbeats []struct {
		ok        bool
		errorText string
	}
}

type stubWriteFence struct {
	calls int
	err   error
}

func (fence *stubWriteFence) WithWriteFence(_ context.Context, operation func() error) error {
	fence.calls++
	if fence.err != nil {
		return fence.err
	}
	return operation()
}

func (publisher *stubQuotaPublisher) PublishQuotaSnapshot(
	_ context.Context,
	quotas map[string]usage.WeeklyQuota,
) (SnapshotResult, error) {
	return SnapshotResult{Generation: "test", Records: len(quotas), Changed: true}, nil
}

func (publisher *stubQuotaPublisher) PublishQuotaHeartbeat(
	_ context.Context,
	ok bool,
	errorText string,
	_ int64,
	_ int64,
) (HeartbeatPayload, error) {
	publisher.heartbeats = append(publisher.heartbeats, struct {
		ok        bool
		errorText string
	}{ok: ok, errorText: errorText})
	return HeartbeatPayload{OK: ok, Error: errorText}, nil
}
