package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppConfigValidation(t *testing.T) {
	valid := appConfig{
		Root: t.TempDir(), LogLevel: "info", Interval: time.Minute,
		MaxFileSizeMB: 32, Backups: 2, MaxHealthAge: 5 * time.Minute,
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	tests := []appConfig{
		{},
		{Root: valid.Root, Once: true, Health: true, MaxHealthAge: time.Minute},
		{Root: valid.Root, Interval: time.Second, MaxFileSizeMB: 32, Backups: 2, MaxHealthAge: time.Minute},
		{Root: valid.Root, Interval: time.Minute, MaxFileSizeMB: 0, Backups: 2, MaxHealthAge: time.Minute},
		{Root: valid.Root, Interval: time.Minute, MaxFileSizeMB: 32, Backups: 0, MaxHealthAge: time.Minute},
		{Root: valid.Root, Interval: time.Minute, MaxFileSizeMB: 32, Backups: 101, MaxHealthAge: time.Minute},
	}
	for index, config := range tests {
		if err := config.validate(); err == nil {
			t.Fatalf("invalid config %d passed validation: %#v", index, config)
		}
	}
}

func TestHealthProbeDoesNotInitializeMissingTarget(t *testing.T) {
	root := t.TempDir()
	err := run(appConfig{Root: root, Health: true, MaxHealthAge: 5 * time.Minute})
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
		"interval": "1m0s", "max-file-size-mb": "32", "backups": "2", "max-health-age": "5m0s",
	} {
		flag := command.Flags().Lookup(name)
		if flag == nil || flag.DefValue != expected {
			t.Fatalf("flag %s default = %#v, want %q", name, flag, expected)
		}
	}
}
