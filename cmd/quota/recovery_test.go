package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Alfonsxh/codex-cpa-pool/internal/quota"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestRecoveringRefresherKeepsOfficialFactsWhenNativeRecoveryFails(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	fetcher := &recoveryFetchFixture{snapshot: quota.Snapshot{GeneratedAt: 200}}
	recovery := &recoveryFixture{err: errors.New("native recovery unavailable")}
	refresher := recoveringRefresher{refreshRunner: fetcher, recovery: recovery, logger: zap.New(core)}
	snapshot, err := refresher.RunOnce(context.Background())
	if err != nil || snapshot.GeneratedAt != 200 || recovery.calls != 1 || logs.Len() != 1 {
		t.Fatalf("successful official fetch was lost: snapshot=%#v error=%v calls=%d logs=%d", snapshot, err, recovery.calls, logs.Len())
	}
	fetcher.err = errors.New("official refresh failed")
	if _, err := refresher.RunOnce(context.Background()); !errors.Is(err, fetcher.err) || recovery.calls != 1 {
		t.Fatalf("failed official fetch attempted recovery: error=%v calls=%d", err, recovery.calls)
	}
}

type recoveryFetchFixture struct {
	snapshot quota.Snapshot
	err      error
}

func (fixture *recoveryFetchFixture) RunOnce(context.Context) (quota.Snapshot, error) {
	return fixture.snapshot, fixture.err
}

func (*recoveryFetchFixture) RecordError(context.Context, error) error { return nil }

type recoveryFixture struct {
	calls int
	err   error
}

func (fixture *recoveryFixture) Reconcile(_ context.Context, snapshot quota.Snapshot) (int, error) {
	fixture.calls++
	return 0, fixture.err
}
