package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/quota"
)

func TestAppConfigValidation(t *testing.T) {
	valid := appConfig{Root: t.TempDir(), MaxHealthAge: 3 * time.Minute}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	tests := []appConfig{
		{},
		{Root: valid.Root, Once: true, Health: true, MaxHealthAge: time.Minute},
		{Root: valid.Root, IntervalSet: true, Interval: time.Second, MaxHealthAge: time.Minute},
		{Root: valid.Root, IntervalSet: true, Interval: 2 * time.Hour, MaxHealthAge: time.Minute},
	}
	for index, config := range tests {
		if err := config.validate(); err == nil {
			t.Fatalf("invalid config %d passed validation: %#v", index, config)
		}
	}
}

func TestHealthProbeDoesNotInitializeMissingTarget(t *testing.T) {
	root := t.TempDir()
	err := run(appConfig{Root: root, Health: true, MaxHealthAge: 3 * time.Minute})
	if err == nil {
		t.Fatal("health probe succeeded without an existing database")
	}
	for _, name := range []string{"state", "secrets"} {
		if _, statErr := os.Stat(filepath.Join(root, name)); !os.IsNotExist(statErr) {
			t.Fatalf("health probe created %s: %v", name, statErr)
		}
	}
}

func TestCommandDefaultsToConfigurationCenterPolling(t *testing.T) {
	command := newCommand()
	if flag := command.Flags().Lookup("interval"); flag == nil || flag.DefValue != "0s" {
		t.Fatalf("interval flag = %#v", flag)
	}
	if flag := command.Flags().Lookup("max-health-age"); flag == nil || flag.DefValue != "3m0s" {
		t.Fatalf("max-health-age flag = %#v", flag)
	}
}

func TestRefreshRoundConsumesAdminRequestAndCompletesRealQuotaRefresh(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := controlplane.Open(ctx, root, controlplane.Options{})
	if err != nil {
		t.Fatalf("open control-plane store: %v", err)
	}
	defer store.Close()
	if err := store.WriteAccounts(ctx, []controlplane.Account{{
		ID: "alpha", Email: "alpha@example.com", Port: 18319,
		ProxyMode: "direct", GroupEnabled: true,
	}}); err != nil {
		t.Fatalf("write account: %v", err)
	}
	if err := store.WriteSettings(ctx, map[string]any{
		"usage.quota_cache_seconds": 60, "usage.upstream_timeout_seconds": 5,
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	authDirectory := filepath.Join(root, "auth", "alpha")
	if err := os.MkdirAll(authDirectory, 0o700); err != nil {
		t.Fatalf("create OAuth directory: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(authDirectory, "codex.json"),
		[]byte(`{"type":"codex","access_token":"test-access-token","account_id":"official-alpha"}`),
		0o600,
	); err != nil {
		t.Fatalf("write OAuth fixture: %v", err)
	}

	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upstreamCalls.Add(1)
		if request.Header.Get("Authorization") != "Bearer test-access-token" ||
			request.Header.Get("ChatGPT-Account-Id") != "official-alpha" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
          "rate_limit":{"allowed":true,"limit_reached":false,
          "primary_window":{"limit_window_seconds":604800,"used_percent":25,"reset_at":2000}}
        }`))
	}))
	defer upstream.Close()

	request, requested, err := quota.RequestRefresh(ctx, store, time.Unix(100, 0))
	if err != nil || !requested || !request.Pending() {
		t.Fatalf("Admin refresh request = (%#v, %v, %v)", request, requested, err)
	}
	refresher := &quota.Refresher{
		Root: root, Store: store, Endpoint: upstream.URL,
		Now: func() time.Time { return time.Unix(200, 0) }, MaxConcurrency: 1,
	}
	nowValues := []time.Time{time.Unix(101, 0), time.Unix(201, 0)}
	nowIndex := 0
	snapshot, err := runRefreshRound(ctx, store, refresher, func() time.Time {
		value := nowValues[nowIndex]
		nowIndex++
		return value
	})
	if err != nil {
		t.Fatalf("run requested refresh round: %v", err)
	}
	if upstreamCalls.Load() != 1 || len(snapshot.Accounts) != 1 ||
		snapshot.Accounts[0].Account != "alpha" || snapshot.Accounts[0].Status != "ok" ||
		snapshot.GeneratedAt != 200 {
		t.Fatalf("refreshed snapshot = %#v, upstream calls = %d", snapshot, upstreamCalls.Load())
	}
	completed, found, err := quota.ReadRefreshRequest(ctx, store)
	if err != nil || !found || completed.Pending() || completed.RequestID != request.RequestID ||
		completed.StartedID != request.RequestID || completed.StartedAt != 101 ||
		completed.CompletedID != request.RequestID || completed.CompletedAt != 201 || completed.LastError != "" {
		t.Fatalf("completed request = (%#v, %v, %v)", completed, found, err)
	}
	state, found, err := quota.ReadState(ctx, store)
	if err != nil || !found || state.LastSuccessAt != 200 || state.Snapshot.GeneratedAt != 200 {
		t.Fatalf("persisted quota state = (%#v, %v, %v)", state, found, err)
	}
}
