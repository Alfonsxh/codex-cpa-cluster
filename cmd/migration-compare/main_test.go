package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunRequiresExplicitDedicatedTestKeyConfirmation(t *testing.T) {
	err := run(context.Background(), &bytes.Buffer{}, appConfig{})
	if err == nil || !strings.Contains(err.Error(), "confirm-dedicated-test-key") {
		t.Fatalf("run error = %v", err)
	}
}

func TestRunStateComparisonRequiresExplicitIsolatedCopyConfirmation(t *testing.T) {
	err := runStateComparison(context.Background(), &bytes.Buffer{}, "", "", false)
	if err == nil || !strings.Contains(err.Error(), "confirm-isolated-state-copies") {
		t.Fatalf("runStateComparison error = %v", err)
	}
}

func TestRunStateSummaryRequiresExplicitIsolatedCopyConfirmation(t *testing.T) {
	err := runStateSummary(context.Background(), &bytes.Buffer{}, "", false)
	if err == nil || !strings.Contains(err.Error(), "confirm-isolated-state-copy") {
		t.Fatalf("runStateSummary error = %v", err)
	}
}
