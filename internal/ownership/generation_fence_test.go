package ownership

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
)

func TestWriterLeaseGenerationTransferFencesStaleOwner(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Unix(1_000, 0)
	first, err := controlplane.Open(ctx, root, controlplane.Options{
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("open first owner store: %v", err)
	}
	runtimeFirst, err := first.TakeLease(ctx, RuntimeScope, "go-generation-1", 5*time.Second)
	if err != nil {
		t.Fatalf("take first runtime lease: %v", err)
	}
	workerFirst, err := first.TakeLease(ctx, "admin", "go-generation-1:admin", 5*time.Second)
	if err != nil {
		t.Fatalf("take first worker lease: %v", err)
	}
	if err := first.InstallWriteFence(runtimeFirst, workerFirst); err != nil {
		t.Fatalf("install first write fence: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first owner store: %v", err)
	}

	now = time.Unix(1_005, 0)
	second, err := controlplane.OpenExisting(ctx, root, controlplane.Options{
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("open second owner store: %v", err)
	}
	defer second.Close()
	runtimeSecond, err := second.TakeLease(ctx, RuntimeScope, "go-generation-2", 5*time.Second)
	if err != nil {
		t.Fatalf("transfer runtime lease: %v", err)
	}
	workerSecond, err := second.TakeLease(ctx, "admin", "go-generation-2:admin", 5*time.Second)
	if err != nil {
		t.Fatalf("transfer worker lease: %v", err)
	}
	if runtimeSecond.Generation != 2 || workerSecond.Generation != 2 {
		t.Fatalf(
			"second generations = runtime %d worker %d",
			runtimeSecond.Generation,
			workerSecond.Generation,
		)
	}
	if err := second.InstallWriteFence(runtimeSecond, workerSecond); err != nil {
		t.Fatalf("install second write fence: %v", err)
	}
	if err := second.WriteRuntimeState(ctx, "lease-rehearsal-business-state", map[string]any{
		"owner": "go-generation-2",
	}); err != nil {
		t.Fatalf("write with second generation: %v", err)
	}

	now = time.Unix(1_010, 0)
	third, err := controlplane.OpenExisting(ctx, root, controlplane.Options{
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("open third owner store: %v", err)
	}
	runtimeThird, err := third.TakeLease(ctx, RuntimeScope, "go-generation-3", 5*time.Second)
	if err != nil {
		third.Close()
		t.Fatalf("take third runtime lease: %v", err)
	}
	workerThird, err := third.TakeLease(ctx, "admin", "go-generation-3:admin", 5*time.Second)
	if err != nil {
		third.Close()
		t.Fatalf("take third worker lease: %v", err)
	}
	if runtimeThird.Generation != 3 || workerThird.Generation != 3 {
		third.Close()
		t.Fatalf(
			"third generations = runtime %d worker %d",
			runtimeThird.Generation,
			workerThird.Generation,
		)
	}
	if err := third.Close(); err != nil {
		t.Fatalf("close third owner store: %v", err)
	}

	err = second.WriteRuntimeState(ctx, "lease-rehearsal-business-state", map[string]any{
		"owner": "stale-go-generation-2",
	})
	if !errors.Is(err, controlplane.ErrLeaseLost) {
		t.Fatalf("stale second-generation write error = %v, want ErrLeaseLost", err)
	}
	var state map[string]any
	found, err := second.ReadRuntimeState(ctx, "lease-rehearsal-business-state", &state)
	if err != nil || !found || state["owner"] != "go-generation-2" {
		t.Fatalf("business state after transfer = (%v, %#v, %v)", found, state, err)
	}
}
