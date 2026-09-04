package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/usage"
)

const runtimeReasoningMultiplierPrefix = "user_quota.reasoning_multiplier."

var runtimeReasoningEfforts = []string{
	"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra", "auto", "unknown",
}

type ControlCatalog interface {
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadKeyRecords(context.Context) ([]controlplane.KeyRecord, error)
	EnsureInternalKeys(context.Context, []string) (map[string]controlplane.InternalKey, error)
	ReadUserTeams(context.Context, []string) (map[string]controlplane.UserTeamClassification, error)
}

type RuntimeWriter interface {
	EventWriter
	SyncIdentities(context.Context, []usage.Identity) (int, error)
	SyncUserTeams(context.Context, map[string]usage.TeamIdentity) (int, error)
	EnsureUsageBreakdownStarted(context.Context) (int64, error)
	EnsureWeekTimezone(context.Context, string) (bool, error)
	ConfigurePersonalQuotaReset(context.Context, bool, bool) (usage.QuotaResetConfiguration, error)
	WeeklyQuotas(context.Context, []string, *int64) (map[string]usage.WeeklyQuota, error)
	UpdateCollectorStatus(context.Context, string) error
	RebuildWeeklyUsage(context.Context) (usage.RebuildResult, error)
}

type QuotaPublisher interface {
	PublishQuotaSnapshot(context.Context, map[string]usage.WeeklyQuota) (SnapshotResult, error)
	PublishQuotaHeartbeat(context.Context, bool, string, int64, int64) (HeartbeatPayload, error)
}

type QueueFactory func(account string, address string, password string, batchSize int) (BatchDrainer, error)

type RuntimeConfig struct {
	ManagementKey                string
	BatchSize                    int
	WeekTimezone                 string
	ResetPersonalWeeklyOnNewWeek bool
	DefaultWeeklyTokens          *int64
	ReasoningMultipliers         map[string]float64
	HeartbeatStaleAfterSeconds   int64
	QuotaFailOpenAfterSeconds    int64
}

type Runtime struct {
	Control      ControlCatalog
	Writer       RuntimeWriter
	Publisher    QuotaPublisher
	QueueFactory QueueFactory
	Config       RuntimeConfig
}

type RunResult struct {
	usage.IngestCounters
	Errors   []string       `json:"errors"`
	Snapshot SnapshotResult `json:"snapshot"`
}

type RebuildRuntimeResult struct {
	Rebuild  usage.RebuildResult `json:"rebuild"`
	Snapshot SnapshotResult      `json:"snapshot"`
}

func (runtime *Runtime) RunOnce(ctx context.Context) (RunResult, error) {
	var result RunResult
	if err := runtime.validate(); err != nil {
		return result, err
	}
	records, err := runtime.Control.ReadKeyRecords(ctx)
	if err != nil {
		return result, fmt.Errorf("read collector key identities: %w", err)
	}
	activeUsers := activeUsers(records)
	internalKeys, err := runtime.Control.EnsureInternalKeys(ctx, activeUsers)
	if err != nil {
		return result, fmt.Errorf("synchronize collector internal identities: %w", err)
	}
	identities, identityUsers := collectorIdentities(records, internalKeys)
	if _, err := runtime.Writer.SyncIdentities(ctx, identities); err != nil {
		return result, err
	}
	classifications, err := runtime.Control.ReadUserTeams(ctx, identityUsers)
	if err != nil {
		return result, fmt.Errorf("read collector team identities: %w", err)
	}
	teams := make(map[string]usage.TeamIdentity, len(classifications))
	for user, classification := range classifications {
		teamID := ""
		if classification.TeamID != nil {
			teamID = *classification.TeamID
		}
		teams[user] = usage.TeamIdentity{
			TeamID: teamID, MembershipVersion: classification.TeamMembershipVersion,
		}
	}
	if _, err := runtime.Writer.SyncUserTeams(ctx, teams); err != nil {
		return result, err
	}
	if _, err := runtime.Writer.EnsureUsageBreakdownStarted(ctx); err != nil {
		return result, err
	}
	timezoneChanged, err := runtime.Writer.EnsureWeekTimezone(ctx, runtime.Config.WeekTimezone)
	if err != nil {
		return result, err
	}
	if _, err := runtime.Writer.ConfigurePersonalQuotaReset(
		ctx,
		runtime.Config.ResetPersonalWeeklyOnNewWeek,
		timezoneChanged,
	); err != nil {
		return result, err
	}

	accounts, err := runtime.Control.ReadAccounts(ctx)
	if err != nil {
		return result, fmt.Errorf("read collector accounts: %w", err)
	}
	service := &Service{Writer: runtime.Writer, Multipliers: runtime.Config.ReasoningMultipliers}
	factory := runtime.QueueFactory
	if factory == nil {
		factory = defaultQueueFactory
	}
	for _, account := range accounts {
		if !account.GroupEnabled {
			continue
		}
		address := net.JoinHostPort("cliproxy-"+account.ID, "8317")
		queue, err := factory(account.ID, address, runtime.Config.ManagementKey, runtime.Config.BatchSize)
		if err == nil {
			var counters usage.IngestCounters
			counters, err = service.DrainAccount(ctx, account.ID, queue)
			addCounters(&result.IngestCounters, counters)
		}
		if err != nil {
			result.Errors = append(result.Errors, safeRuntimeError(account.ID, err, runtime.Config.ManagementKey))
		}
	}
	quotas, err := runtime.Writer.WeeklyQuotas(ctx, activeUsers, runtime.Config.DefaultWeeklyTokens)
	if err != nil {
		result.Errors = append(result.Errors, safeRuntimeError("quota snapshot", err, runtime.Config.ManagementKey))
	} else {
		result.Snapshot, err = runtime.Publisher.PublishQuotaSnapshot(ctx, quotas)
		if err != nil {
			result.Errors = append(result.Errors, safeRuntimeError("quota snapshot", err, runtime.Config.ManagementKey))
		}
	}
	errorText := strings.Join(result.Errors, "; ")
	if err := runtime.Writer.UpdateCollectorStatus(ctx, errorText); err != nil {
		return result, err
	}
	_, heartbeatErr := runtime.Publisher.PublishQuotaHeartbeat(
		ctx,
		len(result.Errors) == 0,
		errorText,
		runtime.Config.HeartbeatStaleAfterSeconds,
		runtime.Config.QuotaFailOpenAfterSeconds,
	)
	if heartbeatErr != nil {
		result.Errors = append(
			result.Errors,
			safeRuntimeError("quota heartbeat", heartbeatErr, runtime.Config.ManagementKey),
		)
		if err := runtime.Writer.UpdateCollectorStatus(ctx, strings.Join(result.Errors, "; ")); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (runtime *Runtime) Rebuild(ctx context.Context) (RebuildRuntimeResult, error) {
	var result RebuildRuntimeResult
	if err := runtime.validate(); err != nil {
		return result, err
	}
	records, err := runtime.Control.ReadKeyRecords(ctx)
	if err != nil {
		return result, err
	}
	activeUsers := activeUsers(records)
	changed, err := runtime.Writer.EnsureWeekTimezone(ctx, runtime.Config.WeekTimezone)
	if err != nil {
		return result, err
	}
	result.Rebuild, err = runtime.Writer.RebuildWeeklyUsage(ctx)
	if err != nil {
		return result, err
	}
	if _, err := runtime.Writer.ConfigurePersonalQuotaReset(
		ctx,
		runtime.Config.ResetPersonalWeeklyOnNewWeek,
		changed,
	); err != nil {
		return result, err
	}
	quotas, err := runtime.Writer.WeeklyQuotas(ctx, activeUsers, runtime.Config.DefaultWeeklyTokens)
	if err != nil {
		return result, err
	}
	result.Snapshot, err = runtime.Publisher.PublishQuotaSnapshot(ctx, quotas)
	if err != nil {
		return result, err
	}
	if _, err := runtime.Publisher.PublishQuotaHeartbeat(
		ctx,
		true,
		"",
		runtime.Config.HeartbeatStaleAfterSeconds,
		runtime.Config.QuotaFailOpenAfterSeconds,
	); err != nil {
		return result, err
	}
	if err := runtime.Writer.UpdateCollectorStatus(ctx, ""); err != nil {
		return result, err
	}
	return result, nil
}

func (runtime *Runtime) validate() error {
	if runtime == nil || runtime.Control == nil || runtime.Writer == nil || runtime.Publisher == nil {
		return errors.New("usage collector runtime dependencies are required")
	}
	if strings.TrimSpace(runtime.Config.ManagementKey) == "" {
		return errors.New("usage collector management key is required")
	}
	if runtime.Config.BatchSize < 1 || runtime.Config.BatchSize > maxBatchSize {
		return fmt.Errorf("usage collector batch size must be between 1 and %d", maxBatchSize)
	}
	if strings.TrimSpace(runtime.Config.WeekTimezone) == "" {
		return errors.New("usage collector week timezone is required")
	}
	return nil
}

func defaultQueueFactory(_ string, address string, password string, batchSize int) (BatchDrainer, error) {
	return NewQueue(QueueConfig{Address: address, Password: password, BatchSize: batchSize})
}

func activeUsers(records []controlplane.KeyRecord) []string {
	seen := make(map[string]struct{})
	for _, record := range records {
		if record.Status != "active" {
			continue
		}
		user := strings.ToLower(strings.TrimSpace(record.User))
		if user != "" {
			seen[user] = struct{}{}
		}
	}
	users := make([]string, 0, len(seen))
	for user := range seen {
		users = append(users, user)
	}
	sort.Strings(users)
	return users
}

func collectorIdentities(
	records []controlplane.KeyRecord,
	internal map[string]controlplane.InternalKey,
) ([]usage.Identity, []string) {
	identities := make([]usage.Identity, 0, len(records)+len(internal))
	userSet := make(map[string]struct{})
	for _, record := range records {
		user := strings.ToLower(strings.TrimSpace(record.User))
		identities = append(identities, usage.Identity{
			Key: record.Key, Label: record.Label, UserEmail: user, Account: record.Account,
		})
		if user != "" {
			userSet[user] = struct{}{}
		}
	}
	internalUsers := make([]string, 0, len(internal))
	for user := range internal {
		internalUsers = append(internalUsers, user)
	}
	sort.Strings(internalUsers)
	for _, user := range internalUsers {
		record := internal[user]
		if record.Status != "active" {
			continue
		}
		user = strings.ToLower(strings.TrimSpace(user))
		identities = append(identities, usage.Identity{Key: record.Key, Label: user, UserEmail: user})
		if user != "" {
			userSet[user] = struct{}{}
		}
	}
	users := make([]string, 0, len(userSet))
	for user := range userSet {
		users = append(users, user)
	}
	sort.Strings(users)
	return identities, users
}

func safeRuntimeError(scope string, err error, secret string) string {
	message := err.Error()
	if secret != "" {
		message = strings.ReplaceAll(message, secret, "[REDACTED]")
	}
	return scope + ": " + message
}

func DefaultRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		BatchSize: 100, WeekTimezone: "Asia/Shanghai", ResetPersonalWeeklyOnNewWeek: true,
		HeartbeatStaleAfterSeconds: 15, QuotaFailOpenAfterSeconds: 300,
		ReasoningMultipliers: make(map[string]float64),
	}
}

func RuntimeConfigFromSettings(settings map[string]any) (RuntimeConfig, time.Duration, error) {
	config := DefaultRuntimeConfig()
	interval := 2 * time.Second
	if value, found, err := numericSetting(settings, "collector.interval_seconds"); err != nil {
		return config, 0, err
	} else if found {
		interval = time.Duration(value * float64(time.Second))
	}
	if value, found, err := integerSetting(settings, "collector.batch_size"); err != nil {
		return config, 0, err
	} else if found {
		config.BatchSize = int(value)
	}
	if value, found, err := stringSetting(settings, "user_quota.timezone"); err != nil {
		return config, 0, err
	} else if found {
		config.WeekTimezone = value
	}
	if value, found, err := booleanSetting(settings, "user_quota.reset_personal_weekly_on_new_week"); err != nil {
		return config, 0, err
	} else if found {
		config.ResetPersonalWeeklyOnNewWeek = value
	}
	if raw, found := settings["user_quota.default_weekly_tokens"]; found && raw != nil {
		value, ok := integerValue(raw)
		if !ok || value <= 0 {
			return config, 0, errors.New("user_quota.default_weekly_tokens must be a positive integer or null")
		}
		config.DefaultWeeklyTokens = &value
	}
	if value, found, err := integerSetting(settings, "user_quota.fail_open_after_seconds"); err != nil {
		return config, 0, err
	} else if found {
		config.QuotaFailOpenAfterSeconds = value
	}
	for _, effort := range runtimeReasoningEfforts {
		key := runtimeReasoningMultiplierPrefix + effort
		if value, found, err := numericSetting(settings, key); err != nil {
			return config, 0, err
		} else if found {
			if value < 0.1 || value > 10 {
				return config, 0, fmt.Errorf("%s must be between 0.1 and 10", key)
			}
			config.ReasoningMultipliers[key] = value
		}
	}
	if interval < 500*time.Millisecond || interval > 60*time.Second {
		return config, 0, errors.New("collector.interval_seconds must be between 0.5 and 60")
	}
	if config.BatchSize < 1 || config.BatchSize > maxBatchSize {
		return config, 0, fmt.Errorf("collector.batch_size must be between 1 and %d", maxBatchSize)
	}
	if _, err := time.LoadLocation(config.WeekTimezone); err != nil {
		return config, 0, fmt.Errorf("invalid user quota timezone %q: %w", config.WeekTimezone, err)
	}
	return config, interval, nil
}

func numericSetting(settings map[string]any, key string) (float64, bool, error) {
	raw, found := settings[key]
	if !found {
		return 0, false, nil
	}
	var result float64
	switch value := raw.(type) {
	case float64:
		result = value
	case int:
		result = float64(value)
	case int64:
		result = float64(value)
	case json.Number:
		parsed, err := value.Float64()
		if err != nil {
			return 0, false, fmt.Errorf("%s must be numeric", key)
		}
		result = parsed
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return 0, false, fmt.Errorf("%s must be numeric", key)
		}
		result = parsed
	default:
		return 0, false, fmt.Errorf("%s must be numeric", key)
	}
	if math.IsNaN(result) || math.IsInf(result, 0) {
		return 0, false, fmt.Errorf("%s must be finite", key)
	}
	return result, true, nil
}

func integerSetting(settings map[string]any, key string) (int64, bool, error) {
	raw, found := settings[key]
	if !found {
		return 0, false, nil
	}
	value, valid := integerValue(raw)
	if !valid {
		return 0, false, fmt.Errorf("%s must be an integer", key)
	}
	return value, true, nil
}

func integerValue(raw any) (int64, bool) {
	switch value := raw.(type) {
	case int:
		return int64(value), true
	case int64:
		return value, true
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || value > math.MaxInt64 || value < math.MinInt64 ||
			value != float64(int64(value)) {
			return 0, false
		}
		return int64(value), true
	case json.Number:
		parsed, err := value.Int64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func stringSetting(settings map[string]any, key string) (string, bool, error) {
	raw, found := settings[key]
	if !found {
		return "", false, nil
	}
	value, valid := raw.(string)
	if !valid {
		return "", false, fmt.Errorf("%s must be a string", key)
	}
	return strings.TrimSpace(value), true, nil
}

func booleanSetting(settings map[string]any, key string) (bool, bool, error) {
	raw, found := settings[key]
	if !found {
		return false, false, nil
	}
	value, valid := raw.(bool)
	if !valid {
		return false, false, fmt.Errorf("%s must be a boolean", key)
	}
	return value, true, nil
}
