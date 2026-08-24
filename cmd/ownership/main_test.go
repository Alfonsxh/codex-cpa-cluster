package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/ownership"
)

func TestOwnershipActivationStatusAndReleaseNeverPrintToken(t *testing.T) {
	root := t.TempDir()
	store, err := controlplane.Open(context.Background(), root, controlplane.Options{})
	if err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seed target: %v", err)
	}

	var activated bytes.Buffer
	err = runActivate(
		context.Background(),
		&activated,
		commandConfig{Root: root, TTL: 30 * time.Second},
		"go-v2",
		"",
		0,
		"go-v2",
		true,
		"",
	)
	if err != nil {
		t.Fatalf("runActivate: %v", err)
	}
	status := decodeLeaseStatus(t, activated.Bytes())
	if !status.Found || !status.Active || status.Owner != "go-v2" || status.Generation != 1 {
		t.Fatalf("activated status = %#v", status)
	}
	if strings.Contains(activated.String(), "token") {
		t.Fatalf("activation output contains token field: %s", activated.String())
	}

	var read bytes.Buffer
	if err := runStatus(
		context.Background(),
		&read,
		commandConfig{Root: root, TTL: 30 * time.Second},
		ownership.RuntimeScope,
	); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	readStatus := decodeLeaseStatus(t, read.Bytes())
	if readStatus.Generation != status.Generation || strings.Contains(read.String(), "token") {
		t.Fatalf("read status = %#v raw %s", readStatus, read.String())
	}

	var released bytes.Buffer
	if err := runRelease(
		context.Background(),
		&released,
		commandConfig{Root: root, TTL: 30 * time.Second},
		ownership.RuntimeScope,
		"go-v2",
		"runtime-writer:1",
	); err != nil {
		t.Fatalf("runRelease: %v", err)
	}
	releasedStatus := decodeLeaseStatus(t, released.Bytes())
	if releasedStatus.Active || releasedStatus.ReleasedAt == nil || strings.Contains(released.String(), "token") {
		t.Fatalf("released status = %#v raw %s", releasedStatus, released.String())
	}
}

func TestOwnershipActivationRequiresExplicitEmptyBootstrap(t *testing.T) {
	root := t.TempDir()
	store, err := controlplane.Open(context.Background(), root, controlplane.Options{})
	if err != nil {
		t.Fatalf("seed target: %v", err)
	}
	defer store.Close()
	err = runActivate(
		context.Background(),
		&bytes.Buffer{},
		commandConfig{Root: root, TTL: 30 * time.Second},
		"go-v2",
		"",
		0,
		"go-v2",
		false,
		"",
	)
	if err == nil || !strings.Contains(err.Error(), "allow-empty-bootstrap") {
		t.Fatalf("runActivate error = %v", err)
	}
}

func TestOwnershipActivationRequiresExactExpiredGeneration(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	seed, err := controlplane.Open(ctx, root, controlplane.Options{
		Now: func() time.Time { return time.Now().Add(-time.Minute) },
	})
	if err != nil {
		t.Fatalf("seed target: %v", err)
	}
	prior, err := seed.TakeLease(ctx, ownership.RuntimeScope, "python-v1", 5*time.Second)
	if err != nil {
		t.Fatalf("seed prior ownership: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed target: %v", err)
	}

	err = runActivate(
		ctx,
		&bytes.Buffer{},
		commandConfig{Root: root, TTL: 30 * time.Second},
		"go-v2",
		"python-v1",
		prior.Generation+1,
		"go-v2",
		false,
		"",
	)
	if err == nil || !strings.Contains(err.Error(), "expected-generation") {
		t.Fatalf("mismatched generation error = %v", err)
	}

	var activated bytes.Buffer
	if err := runActivate(
		ctx,
		&activated,
		commandConfig{Root: root, TTL: 30 * time.Second},
		"go-v2",
		"python-v1",
		prior.Generation,
		"go-v2",
		false,
		"",
	); err != nil {
		t.Fatalf("activate exact generation: %v", err)
	}
	status := decodeLeaseStatus(t, activated.Bytes())
	if status.Owner != "go-v2" || status.Generation != prior.Generation+1 {
		t.Fatalf("replacement status = %#v", status)
	}
}

func TestOwnershipActivationAllowsExplicitLegacyBootstrapOnlyForExactRoot(t *testing.T) {
	root := t.TempDir()
	store, err := controlplane.Open(context.Background(), root, controlplane.Options{})
	if err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seed target: %v", err)
	}

	err = runActivate(
		context.Background(),
		&bytes.Buffer{},
		commandConfig{Root: root, TTL: 30 * time.Second},
		"go-v2",
		"",
		0,
		"go-v2",
		false,
		"legacy-writers-stopped:/wrong/root",
	)
	if err == nil || !strings.Contains(err.Error(), "confirm-legacy-bootstrap") {
		t.Fatalf("wrong legacy confirmation error = %v", err)
	}

	var activated bytes.Buffer
	err = runActivate(
		context.Background(),
		&activated,
		commandConfig{Root: root, TTL: 30 * time.Second},
		"go-v2",
		"",
		0,
		"go-v2",
		false,
		"legacy-writers-stopped:"+root,
	)
	if err != nil {
		t.Fatalf("legacy bootstrap: %v", err)
	}
	status := decodeLeaseStatus(t, activated.Bytes())
	if status.Owner != "go-v2" || status.Generation != 1 {
		t.Fatalf("legacy bootstrap status = %#v", status)
	}
}

func decodeLeaseStatus(t *testing.T, raw []byte) leaseStatus {
	t.Helper()
	var status leaseStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatalf("decode lease status %q: %v", raw, err)
	}
	return status
}
