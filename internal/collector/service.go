package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/usage"
)

type BatchDrainer interface {
	Drain(context.Context, func([][]byte) error) error
}

type EventWriter interface {
	IngestEvents(context.Context, string, []usage.Event, map[string]float64) (usage.IngestCounters, error)
}

type Service struct {
	Writer      EventWriter
	Multipliers map[string]float64
}

// DrainAccount decodes only JSON objects and commits each queue batch through
// the usage writer. Malformed queue entries are skipped for parity with the
// legacy collector and can never make a valid batch fail as a unit.
func (service *Service) DrainAccount(
	ctx context.Context,
	account string,
	queue BatchDrainer,
) (usage.IngestCounters, error) {
	var totals usage.IngestCounters
	if service == nil || service.Writer == nil {
		return totals, errors.New("usage collector writer is required")
	}
	if queue == nil {
		return totals, errors.New("usage collector queue is required")
	}
	err := queue.Drain(ctx, func(items [][]byte) error {
		events := make([]usage.Event, 0, len(items))
		for _, item := range items {
			decoder := json.NewDecoder(bytes.NewReader(item))
			decoder.UseNumber()
			var event usage.Event
			if err := decoder.Decode(&event); err != nil || event == nil {
				continue
			}
			var trailing any
			if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
				continue
			}
			events = append(events, event)
		}
		if len(events) == 0 {
			return nil
		}
		counters, err := service.Writer.IngestEvents(
			ctx,
			account,
			events,
			service.Multipliers,
		)
		if err != nil {
			return err
		}
		addCounters(&totals, counters)
		return nil
	})
	if err != nil {
		return totals, fmt.Errorf("collect usage for account %s: %w", account, err)
	}
	return totals, nil
}

func addCounters(target *usage.IngestCounters, value usage.IngestCounters) {
	target.Received += value.Received
	target.Inserted += value.Inserted
	target.Duplicate += value.Duplicate
	target.Unmapped += value.Unmapped
	target.MissingAPIKey += value.MissingAPIKey
	target.UnknownAPIKey += value.UnknownAPIKey
	target.Ignored += value.Ignored
}
