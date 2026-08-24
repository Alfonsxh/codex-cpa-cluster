package runtimeops

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestGatewayDrainerWaitsForExactAccountAcrossGatewayReplicas(t *testing.T) {
	var firstCalls atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/__stats" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		inflight := 1
		if firstCalls.Add(1) >= 2 {
			inflight = 0
		}
		_, _ = fmt.Fprintf(writer, `[{"label":"alice:alpha","account":"alpha","inflight":%d}]`, inflight)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`[{"label":"bob:beta","account":"beta","inflight":8}]`))
	}))
	defer second.Close()
	drainer, err := NewGatewayDrainer(GatewayDrainerConfig{
		ProbeURLs: []string{first.URL, second.URL}, WaitTimeout: time.Second,
		PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewGatewayDrainer: %v", err)
	}
	if err := drainer.WaitAccountDrained(context.Background(), "alpha"); err != nil {
		t.Fatalf("WaitAccountDrained: %v", err)
	}
	if firstCalls.Load() < 2 {
		t.Fatalf("first Gateway calls = %d", firstCalls.Load())
	}
}

func TestGatewayDrainerFailsClosedWhenStatsRemainBusyOrUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	drainer, err := NewGatewayDrainer(GatewayDrainerConfig{
		ProbeURLs: []string{server.URL}, WaitTimeout: 25 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewGatewayDrainer: %v", err)
	}
	if err := drainer.WaitAccountDrained(context.Background(), "alpha"); err == nil {
		t.Fatal("WaitAccountDrained unexpectedly accepted unavailable stats")
	}
}

func TestGatewayDrainerFailsClosedWhenAnyReplicaIsUnavailable(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`[{"label":"alice:alpha","account":"alpha","inflight":0}]`))
	}))
	defer healthy.Close()
	unavailable := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer unavailable.Close()
	drainer, err := NewGatewayDrainer(GatewayDrainerConfig{
		ProbeURLs: []string{healthy.URL, unavailable.URL}, WaitTimeout: 25 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewGatewayDrainer: %v", err)
	}
	if err := drainer.WaitAccountDrained(context.Background(), "alpha"); err == nil {
		t.Fatal("WaitAccountDrained unexpectedly ignored an unavailable Gateway replica")
	}
}
