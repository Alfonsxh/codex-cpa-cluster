package accountstatus

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDisplayRefreshDoesNotBlockOrAuthorizeFromExpiredSnapshot(t *testing.T) {
	var now atomic.Int64
	now.Store(10000)
	var blocked atomic.Bool
	var probes atomic.Int64
	started, release := make(chan struct{}, 1), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	observer, err := New(Config{
		Root: t.TempDir(), Secrets: fixedSecretReader{value: "fixture"}, CacheTTL: time.Second,
		Now: func() time.Time { return time.Unix(now.Load(), 0) },
		Transport: displayTransportFunc(func(request *http.Request) (*http.Response, error) {
			payload := `{"files":[]}`
			if strings.HasSuffix(request.URL.Path, "/auth-files") {
				probes.Add(1)
				payload = `{"files":[{"status":"active"}]}`
				if blocked.Load() {
					select {
					case started <- struct{}{}:
					default:
					}
					select {
					case <-release:
					case <-request.Context().Done():
						return nil, request.Context().Err()
					}
					payload = `{"files":[{"status":"error","unavailable":true,"status_message":"invalid_grant"}]}`
				}
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: request}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	services := map[string]string{"alpha": "cliproxy-alpha"}
	assertState(t, observer.ObserveForDisplay(context.Background(), services)["alpha"], "", false, false)
	blocked.Store(true)
	now.Store(10001)
	// A display read must complete while the replacement probe remains blocked.
	displayed := make(chan map[string]State, 1)
	go func() { displayed <- observer.ObserveForDisplay(context.Background(), services) }()
	select {
	case states := <-displayed:
		assertState(t, states["alpha"], "", false, false)
	case <-time.After(time.Second):
		t.Fatal("display waited for a slow native probe")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("expired display snapshot did not refresh")
	}
	for range 10 {
		observer.ObserveForDisplay(context.Background(), services)
	}
	if probes.Load() != 2 {
		t.Fatalf("concurrent display reads fanned out into %d probes", probes.Load())
	}
	// A mutation read joins the refresh; cancellation must return unknown, not
	// reuse the healthy snapshot that was acceptable only for display.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assertState(t, observer.Observe(ctx, services)["alpha"], ReasonRuntimeUnknown, false, false)
	// The display allowance is bounded to one additional TTL, and cannot be
	// transferred to a changed account/service set.
	now.Store(10002)
	assertState(t, observer.ObserveForDisplay(ctx, services)["alpha"], ReasonRuntimeUnknown, false, false)
	changed := map[string]string{"beta": "cliproxy-beta"}
	assertState(t, observer.ObserveForDisplay(ctx, changed)["beta"], ReasonRuntimeUnknown, false, false)
	unblock()
	assertState(t, observer.Observe(context.Background(), services)["alpha"], ReasonCredentialUnavailable, true, false)
}

type displayTransportFunc func(*http.Request) (*http.Response, error)

func (transport displayTransportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}
