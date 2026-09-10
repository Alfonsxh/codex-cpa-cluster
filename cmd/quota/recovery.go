package main

import (
	"context"

	"github.com/Alfonsxh/codex-cpa-pool/internal/quota"
	"go.uber.org/zap"
)

type cooldownReconciler interface {
	Reconcile(context.Context, quota.Snapshot) (int, error)
}

type recoveringRefresher struct {
	refreshRunner
	recovery cooldownReconciler
	logger   *zap.Logger
}

func (refresher recoveringRefresher) RunOnce(ctx context.Context) (quota.Snapshot, error) {
	snapshot, err := refresher.refreshRunner.RunOnce(ctx)
	if err != nil {
		return snapshot, err
	}
	count, recoveryError := refresher.recovery.Reconcile(ctx, snapshot)
	if recoveryError != nil {
		// Keep successful official quota facts available when an older CPA lacks
		// the recovery API or native state could not be safely reconciled.
		refresher.logger.Warn("CPA quota cooldown recovery incomplete", zap.Error(recoveryError))
	}
	if count > 0 {
		refresher.logger.Info("CPA quota cooldown recovered", zap.Int("accounts", count))
	}
	return snapshot, nil
}
