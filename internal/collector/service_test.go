package collector

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/usage"
)

func TestServiceDecodesObjectsAndAggregatesBatches(t *testing.T) {
	writer := &stubEventWriter{results: []usage.IngestCounters{
		{Received: 1, Inserted: 1},
		{Received: 1, Duplicate: 1},
	}}
	service := &Service{Writer: writer, Multipliers: map[string]float64{"max": 2}}
	queue := stubDrainer{batches: [][][]byte{
		{[]byte(`{"request_id":"one"}`), []byte(`not-json`), []byte(`[]`), []byte(`{"request_id":"trailing"}{}`)},
		{[]byte(`{"request_id":"two"}`)},
	}}

	counters, err := service.DrainAccount(context.Background(), "alpha", queue)
	if err != nil {
		t.Fatalf("DrainAccount: %v", err)
	}
	if counters != (usage.IngestCounters{Received: 2, Inserted: 1, Duplicate: 1}) {
		t.Fatalf("counters = %#v", counters)
	}
	if len(writer.events) != 2 || len(writer.events[0]) != 1 || len(writer.events[1]) != 1 {
		t.Fatalf("writer events = %#v", writer.events)
	}
	if writer.accounts[0] != "alpha" || writer.multipliers[0]["max"] != 2 {
		t.Fatalf("writer call = %#v, %#v", writer.accounts, writer.multipliers)
	}
}

func TestServiceStopsWhenWriterFails(t *testing.T) {
	want := errors.New("database unavailable")
	service := &Service{Writer: &stubEventWriter{err: want}}
	queue := stubDrainer{batches: [][][]byte{{[]byte(`{"request_id":"one"}`)}}}
	_, err := service.DrainAccount(context.Background(), "alpha", queue)
	if !errors.Is(err, want) {
		t.Fatalf("DrainAccount error = %v", err)
	}
}

func TestServiceDrainsBacklogAcrossRestartWithDeterministicBatchReplay(t *testing.T) {
	const batchSize = 100
	batches := make([][][]byte, 0, 8)
	for batch := range 8 {
		items := make([][]byte, 0, batchSize)
		for item := range batchSize {
			requestID := fmt.Sprintf("request-%04d", batch*batchSize+item)
			items = append(items, []byte(fmt.Sprintf(`{"request_id":%q}`, requestID)))
		}
		batches = append(batches, items)
	}
	queue := &restartableDrainer{remaining: batches, stopAfter: 3}
	writer := &deduplicatingEventWriter{seen: make(map[string]struct{})}
	service := &Service{Writer: writer}

	first, err := service.DrainAccount(context.Background(), "alpha", queue)
	if !errors.Is(err, errCollectorRestart) {
		t.Fatalf("first drain error = %v", err)
	}
	if first.Inserted != 300 || len(queue.remaining) != 5 {
		t.Fatalf("first checkpoint = counters %#v remaining %d", first, len(queue.remaining))
	}

	// A restart may replay the last fully committed batch when an independent
	// Test queue fixture checkpoints conservatively. The schema-v10 event_key
	// uniqueness contract must make that replay deterministic.
	queue.remaining = append([][][]byte{batches[2]}, queue.remaining...)
	queue.stopAfter = 0
	second, err := service.DrainAccount(context.Background(), "alpha", queue)
	if err != nil {
		t.Fatalf("restart drain: %v", err)
	}
	if second.Inserted != 500 || second.Duplicate != 100 || len(queue.remaining) != 0 {
		t.Fatalf("restart checkpoint = counters %#v remaining %d", second, len(queue.remaining))
	}
	if len(writer.seen) != 800 {
		t.Fatalf("durable event set = %d, want 800", len(writer.seen))
	}
}

type stubDrainer struct {
	batches [][][]byte
	err     error
}

func (drainer stubDrainer) Drain(ctx context.Context, consume func([][]byte) error) error {
	for _, batch := range drainer.batches {
		if err := consume(batch); err != nil {
			return err
		}
	}
	return drainer.err
}

type stubEventWriter struct {
	results     []usage.IngestCounters
	err         error
	events      [][]usage.Event
	accounts    []string
	multipliers []map[string]float64
}

var errCollectorRestart = errors.New("simulated collector restart")

type restartableDrainer struct {
	remaining [][][]byte
	stopAfter int
}

func (drainer *restartableDrainer) Drain(
	_ context.Context,
	consume func([][]byte) error,
) error {
	drained := 0
	for len(drainer.remaining) > 0 {
		if drainer.stopAfter > 0 && drained == drainer.stopAfter {
			return errCollectorRestart
		}
		batch := drainer.remaining[0]
		drainer.remaining = drainer.remaining[1:]
		if err := consume(batch); err != nil {
			return err
		}
		drained++
	}
	return nil
}

type deduplicatingEventWriter struct {
	seen map[string]struct{}
}

func (writer *deduplicatingEventWriter) IngestEvents(
	_ context.Context,
	_ string,
	events []usage.Event,
	_ map[string]float64,
) (usage.IngestCounters, error) {
	result := usage.IngestCounters{Received: len(events)}
	for _, event := range events {
		requestID, _ := event["request_id"].(string)
		if _, found := writer.seen[requestID]; found {
			result.Duplicate++
			continue
		}
		writer.seen[requestID] = struct{}{}
		result.Inserted++
	}
	return result, nil
}

func (writer *stubEventWriter) IngestEvents(
	ctx context.Context,
	account string,
	events []usage.Event,
	multipliers map[string]float64,
) (usage.IngestCounters, error) {
	writer.accounts = append(writer.accounts, account)
	writer.events = append(writer.events, events)
	writer.multipliers = append(writer.multipliers, multipliers)
	if writer.err != nil {
		return usage.IngestCounters{}, writer.err
	}
	index := len(writer.events) - 1
	return writer.results[index], nil
}
