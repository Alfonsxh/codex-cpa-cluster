package collector

import (
	"context"
	"errors"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/usage"
)

type WriteFence interface {
	WithWriteFence(context.Context, func() error) error
}

// FencedRuntimeWriter applies the control-plane runtime/worker lease proof to
// every usage.sqlite3 mutation without holding the fence during network reads
// or read-only quota queries.
type FencedRuntimeWriter struct {
	writer RuntimeWriter
	fence  WriteFence
}

func NewFencedRuntimeWriter(writer RuntimeWriter, fence WriteFence) (*FencedRuntimeWriter, error) {
	if writer == nil || fence == nil {
		return nil, errors.New("fenced usage writer requires a writer and ownership fence")
	}
	return &FencedRuntimeWriter{writer: writer, fence: fence}, nil
}

func (writer *FencedRuntimeWriter) SyncIdentities(
	ctx context.Context,
	identities []usage.Identity,
) (int, error) {
	return withFencedResult(ctx, writer.fence, func() (int, error) {
		return writer.writer.SyncIdentities(ctx, identities)
	})
}

func (writer *FencedRuntimeWriter) SyncUserTeams(
	ctx context.Context,
	teams map[string]usage.TeamIdentity,
) (int, error) {
	return withFencedResult(ctx, writer.fence, func() (int, error) {
		return writer.writer.SyncUserTeams(ctx, teams)
	})
}

func (writer *FencedRuntimeWriter) EnsureUsageBreakdownStarted(ctx context.Context) (int64, error) {
	return withFencedResult(ctx, writer.fence, func() (int64, error) {
		return writer.writer.EnsureUsageBreakdownStarted(ctx)
	})
}

func (writer *FencedRuntimeWriter) IngestEvents(
	ctx context.Context,
	account string,
	events []usage.Event,
	multipliers map[string]float64,
) (usage.IngestCounters, error) {
	return withFencedResult(ctx, writer.fence, func() (usage.IngestCounters, error) {
		return writer.writer.IngestEvents(ctx, account, events, multipliers)
	})
}

func (writer *FencedRuntimeWriter) ConfigurePersonalQuotaReset(
	ctx context.Context,
	enabled bool,
	reschedule bool,
) (usage.QuotaResetConfiguration, error) {
	return withFencedResult(ctx, writer.fence, func() (usage.QuotaResetConfiguration, error) {
		return writer.writer.ConfigurePersonalQuotaReset(ctx, enabled, reschedule)
	})
}

func (writer *FencedRuntimeWriter) WeeklyQuotas(
	ctx context.Context,
	users []string,
	defaultLimit *int64,
) (map[string]usage.WeeklyQuota, error) {
	return writer.writer.WeeklyQuotas(ctx, users, defaultLimit)
}

func (writer *FencedRuntimeWriter) UpdateCollectorStatus(ctx context.Context, lastError string) error {
	return writer.fence.WithWriteFence(ctx, func() error {
		return writer.writer.UpdateCollectorStatus(ctx, lastError)
	})
}

func (writer *FencedRuntimeWriter) RebuildWeeklyUsage(
	ctx context.Context,
) (usage.RebuildResult, error) {
	return withFencedResult(ctx, writer.fence, func() (usage.RebuildResult, error) {
		return writer.writer.RebuildWeeklyUsage(ctx)
	})
}

func (writer *FencedRuntimeWriter) EnsureWeekTimezone(
	ctx context.Context,
	timezone string,
) (bool, error) {
	return withFencedResult(ctx, writer.fence, func() (bool, error) {
		return writer.writer.EnsureWeekTimezone(ctx, timezone)
	})
}

func withFencedResult[T any](
	ctx context.Context,
	fence WriteFence,
	operation func() (T, error),
) (T, error) {
	var result T
	err := fence.WithWriteFence(ctx, func() error {
		var err error
		result, err = operation()
		return err
	})
	return result, err
}

var _ RuntimeWriter = (*FencedRuntimeWriter)(nil)
