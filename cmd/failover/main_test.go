package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppConfigValidation(t *testing.T) {
	valid := appConfig{
		Root: t.TempDir(), SchedulerInterval: 5 * time.Second,
		MaxHealthAge: 3 * time.Minute, ProbeTimeout: time.Second,
		ProbeConcurrency: 8, AccountAddressFormat: "cliproxy-%s:8317",
		SnapshotTimeout: 8 * time.Second,
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	tests := []appConfig{
		{},
		{Root: valid.Root, Once: true, Health: true, SchedulerInterval: 5 * time.Second, MaxHealthAge: time.Minute, ProbeTimeout: time.Second, ProbeConcurrency: 8, AccountAddressFormat: "cliproxy-%s:8317", SnapshotTimeout: 8 * time.Second},
		{Root: valid.Root, SchedulerInterval: time.Millisecond, MaxHealthAge: time.Minute, ProbeTimeout: time.Second, ProbeConcurrency: 8, AccountAddressFormat: "cliproxy-%s:8317", SnapshotTimeout: 8 * time.Second},
		{Root: valid.Root, SchedulerInterval: 5 * time.Second, MaxHealthAge: time.Minute, ProbeTimeout: time.Second, ProbeConcurrency: 8, AccountAddressFormat: "cliproxy:8317", SnapshotTimeout: 8 * time.Second},
		{Root: valid.Root, SchedulerInterval: 5 * time.Second, MaxHealthAge: time.Minute, ProbeTimeout: time.Second, ProbeConcurrency: 0, AccountAddressFormat: "cliproxy-%s:8317", SnapshotTimeout: 8 * time.Second},
	}
	for index, config := range tests {
		if err := config.validate(); err == nil {
			t.Fatalf("invalid config %d passed validation: %#v", index, config)
		}
	}
}

func TestHealthProbeDoesNotInitializeMissingTarget(t *testing.T) {
	root := t.TempDir()
	err := run(appConfig{
		Root: root, Health: true, SchedulerInterval: 5 * time.Second,
		MaxHealthAge: 3 * time.Minute, ProbeTimeout: time.Second,
		ProbeConcurrency: 8, AccountAddressFormat: "cliproxy-%s:8317",
		SnapshotTimeout: 8 * time.Second,
	})
	if err == nil {
		t.Fatal("health probe succeeded without an existing database")
	}
	for _, name := range []string{"state", "secrets"} {
		if _, statErr := os.Stat(filepath.Join(root, name)); !os.IsNotExist(statErr) {
			t.Fatalf("health probe created %s: %v", name, statErr)
		}
	}
}

func TestCommandUsesFrameworkDefaults(t *testing.T) {
	command := newCommand()
	for name, expected := range map[string]string{
		"scheduler-interval": "5s", "probe-timeout": "1s", "probe-concurrency": "8",
		"account-address-format": "cliproxy-%s:8317", "snapshot-timeout": "8s",
	} {
		flag := command.Flags().Lookup(name)
		if flag == nil || flag.DefValue != expected {
			t.Fatalf("flag %s default = %#v, want %q", name, flag, expected)
		}
	}
}
