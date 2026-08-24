package ownership

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
)

func TestCrossLanguageWriterLeaseTransfersV1ToV2AndBack(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	root := t.TempDir()
	staleLeaseFile := filepath.Join(t.TempDir(), "python-v1-leases.json")
	ctx := context.Background()
	seed, err := controlplane.Open(ctx, root, controlplane.Options{})
	if err != nil {
		t.Fatalf("seed isolated target: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close isolated target: %v", err)
	}

	runPythonLeaseStep(t, python, repository, root, staleLeaseFile, `
import json, os, pathlib, sys
sys.path.insert(0, str(pathlib.Path(sys.argv[1]) / "scripts"))
from ownership_lease import LeaseStore, RUNTIME_SCOPE

store = LeaseStore(sys.argv[2], now=lambda: 1000)
leases = {
    "runtime": store.take(RUNTIME_SCOPE, "python-v1", 5),
    "worker": store.take("admin", "python-v1:admin", 5),
}
descriptor = os.open(sys.argv[3], os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
    json.dump(leases, handle, separators=(",", ":"))
`)

	now := time.Unix(1005, 0)
	v2, err := controlplane.OpenExisting(ctx, root, controlplane.Options{
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("open Go v2 target: %v", err)
	}
	defer v2.Close()
	runtimeV2, err := v2.TakeLease(ctx, RuntimeScope, "go-v2", 5*time.Second)
	if err != nil {
		t.Fatalf("transfer runtime to Go v2: %v", err)
	}
	workerV2, err := v2.TakeLease(ctx, "admin", "go-v2:admin", 5*time.Second)
	if err != nil {
		t.Fatalf("transfer worker to Go v2: %v", err)
	}
	if runtimeV2.Generation != 2 || workerV2.Generation != 2 {
		t.Fatalf("Go v2 generations = runtime %d worker %d", runtimeV2.Generation, workerV2.Generation)
	}
	if err := v2.InstallWriteFence(runtimeV2, workerV2); err != nil {
		t.Fatalf("install Go v2 write fence: %v", err)
	}
	if err := v2.WriteRuntimeState(ctx, "lease-rehearsal-business-state", map[string]any{
		"owner": "go-v2",
	}); err != nil {
		t.Fatalf("write as Go v2 owner: %v", err)
	}

	now = time.Unix(1010, 0)
	runPythonLeaseStep(t, python, repository, root, staleLeaseFile, `
import json, pathlib, sys
sys.path.insert(0, str(pathlib.Path(sys.argv[1]) / "scripts"))
from ownership_lease import LeaseLostError, LeaseStore, RUNTIME_SCOPE

store = LeaseStore(sys.argv[2], now=lambda: 1010)
with open(sys.argv[3], "r", encoding="utf-8") as handle:
    stale = json.load(handle)
try:
    store.renew(stale["runtime"], 5)
except LeaseLostError:
    pass
else:
    raise SystemExit("stale Python v1 runtime lease unexpectedly renewed")

runtime = store.take(RUNTIME_SCOPE, "python-v1", 5)
worker = store.take("admin", "python-v1:admin-rollback", 5)
if runtime["generation"] != 3 or worker["generation"] != 3:
    raise SystemExit("Python v1 rollback did not advance both generations")
`)

	err = v2.WriteRuntimeState(ctx, "lease-rehearsal-business-state", map[string]any{
		"owner": "stale-go-v2",
	})
	if !errors.Is(err, controlplane.ErrLeaseLost) {
		t.Fatalf("stale Go v2 write error = %v, want ErrLeaseLost", err)
	}
	var state map[string]any
	found, err := v2.ReadRuntimeState(ctx, "lease-rehearsal-business-state", &state)
	if err != nil || !found || state["owner"] != "go-v2" {
		t.Fatalf("business state after rollback = (%v, %#v, %v)", found, state, err)
	}
	current, found, err := v2.ReadLease(ctx, RuntimeScope)
	if err != nil || !found || current.Owner != "python-v1" || current.Generation != 3 {
		t.Fatalf("rolled-back runtime lease = (%v, %#v, %v)", found, current, err)
	}
}

func runPythonLeaseStep(
	t *testing.T,
	python string,
	repository string,
	root string,
	leaseFile string,
	script string,
) {
	t.Helper()
	command := exec.Command(python, "-c", script, repository, root, leaseFile)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Python v1 lease step: %v\n%s", err, strings.TrimSpace(string(output)))
	}
}
