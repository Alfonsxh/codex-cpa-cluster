package collector

import (
	"context"
	"errors"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/usage"
)

// FencedQuotaPublisher proves the exact Collector lease generation before
// replacing either Gateway quota file. The inner publisher performs no
// control-plane writes, so holding the shared ownership lock cannot recurse.
type FencedQuotaPublisher struct {
	publisher QuotaPublisher
	fence     WriteFence
}

func NewFencedQuotaPublisher(
	publisher QuotaPublisher,
	fence WriteFence,
) (*FencedQuotaPublisher, error) {
	if publisher == nil || fence == nil {
		return nil, errors.New("fenced quota publisher requires a publisher and ownership fence")
	}
	return &FencedQuotaPublisher{publisher: publisher, fence: fence}, nil
}

func (publisher *FencedQuotaPublisher) PublishQuotaSnapshot(
	ctx context.Context,
	quotas map[string]usage.WeeklyQuota,
) (SnapshotResult, error) {
	return withFencedResult(ctx, publisher.fence, func() (SnapshotResult, error) {
		return publisher.publisher.PublishQuotaSnapshot(ctx, quotas)
	})
}

func (publisher *FencedQuotaPublisher) PublishQuotaHeartbeat(
	ctx context.Context,
	ok bool,
	errorText string,
	staleAfterSeconds int64,
	failOpenAfterSeconds int64,
) (HeartbeatPayload, error) {
	return withFencedResult(ctx, publisher.fence, func() (HeartbeatPayload, error) {
		return publisher.publisher.PublishQuotaHeartbeat(
			ctx,
			ok,
			errorText,
			staleAfterSeconds,
			failOpenAfterSeconds,
		)
	})
}

var _ QuotaPublisher = (*FencedQuotaPublisher)(nil)
