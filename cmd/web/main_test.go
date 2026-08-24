package main

import (
	"testing"
	"time"

	webapi "github.com/Alfonsxh/codex-cpa-cluster/internal/web"
)

func TestCommandUsesGoWebContractDefaults(t *testing.T) {
	command := newCommand()
	for name, expected := range map[string]string{
		"address":      ":8080",
		"portal-root":  "/srv/cpa-web/portal",
		"admin-root":   "/srv/cpa-web/admin",
		"usage-root":   "/srv/cpa-web/usage",
		"admin-target": "http://admin:8318",
	} {
		flag := command.Flags().Lookup(name)
		if flag == nil || flag.DefValue != expected {
			t.Fatalf("flag %s default = %#v, want %q", name, flag, expected)
		}
	}
	if flag := command.Flags().Lookup("max-body-bytes"); flag == nil || flag.DefValue != "3145728" {
		t.Fatalf("max-body-bytes default = %#v, want %d", flag, webapi.DefaultMaxBodyBytes)
	}
}

func TestRunRejectsInvalidConfigurationBeforeOpeningAssets(t *testing.T) {
	valid := appConfig{
		Address: "127.0.0.1:18080", PortalRoot: "/missing/portal", AdminRoot: "/missing/admin",
		UsageRoot: "/missing/usage", AdminTarget: "http://admin:8318",
		MaxBodyBytes: webapi.DefaultMaxBodyBytes, LogLevel: "info", ShutdownTimeout: time.Second,
	}
	for name, mutate := range map[string]func(*appConfig){
		"address":  func(config *appConfig) { config.Address = "" },
		"body":     func(config *appConfig) { config.MaxBodyBytes = 0 },
		"shutdown": func(config *appConfig) { config.ShutdownTimeout = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			config := valid
			mutate(&config)
			if err := run(config); err == nil {
				t.Fatal("invalid Web configuration was accepted")
			}
		})
	}
}
