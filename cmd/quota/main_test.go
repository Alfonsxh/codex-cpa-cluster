package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
