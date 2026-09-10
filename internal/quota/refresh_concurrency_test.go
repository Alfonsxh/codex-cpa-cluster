package quota

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
)

func TestSimultaneousSurfaceRefreshesJoinOneRequest(t *testing.T) {
	ctx := context.Background()
	store, err := controlplane.Open(ctx, t.TempDir(), controlplane.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	type result struct {
		state     RefreshRequestState
		requested bool
		err       error
	}
	results := make(chan result, 12)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 12 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			state, requested, err := RequestRefresh(ctx, store, time.Unix(100, 0))
			results <- result{state, requested, err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	created, requestID := 0, ""
	for result := range results {
		if result.err != nil || !result.state.Pending() {
			t.Fatalf("request=%+v", result)
		}
		if requestID == "" {
			requestID = result.state.RequestID
		}
		if result.state.RequestID != requestID {
			t.Fatal("simultaneous refresh created a second worker request")
		}
		if result.requested {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created %d requests", created)
	}
}
