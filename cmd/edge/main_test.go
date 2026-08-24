package main

import (
	"testing"
	"time"

	edgeapi "github.com/Alfonsxh/codex-cpa-cluster/internal/edge"
)

func TestCommandUsesExistingStableEdgeContractDefaults(t *testing.T) {
	command := newCommand()
	for name, expected := range map[string]string{
		"public-address":        ":8317",
		"internal-address":      ":8319",
		"active-gateway-file":   "/var/run/cliproxy-edge/active-gateway.conf",
		"web-target":            "http://web:8080",
		"blue-public-target":    "http://gateway-blue:8317",
		"blue-internal-target":  "http://gateway-blue:8319",
		"green-public-target":   "http://gateway-green:8317",
		"green-internal-target": "http://gateway-green:8319",
	} {
		flag := command.Flags().Lookup(name)
		if flag == nil || flag.DefValue != expected {
			t.Fatalf("flag %s default = %#v, want %q", name, flag, expected)
		}
	}
	if flag := command.Flags().Lookup("max-body-bytes"); flag == nil || flag.DefValue != "104857600" {
		t.Fatalf("max-body-bytes default = %#v, want %d", flag, edgeapi.DefaultMaxBodyBytes)
	}
}

func TestRunRejectsInvalidListenerAndLimitBeforeOpeningState(t *testing.T) {
	valid := appConfig{
		PublicAddress: "127.0.0.1:18317", InternalAddress: "127.0.0.1:18319",
		ActiveGatewayFile: "/missing/selection", RefreshInterval: time.Second,
		MaxBodyBytes: edgeapi.DefaultMaxBodyBytes, LogLevel: "info", ShutdownTimeout: time.Second,
	}
	for name, mutate := range map[string]func(*appConfig){
		"same listener": func(config *appConfig) { config.InternalAddress = config.PublicAddress },
		"refresh":       func(config *appConfig) { config.RefreshInterval = 0 },
		"body limit":    func(config *appConfig) { config.MaxBodyBytes = 0 },
		"shutdown":      func(config *appConfig) { config.ShutdownTimeout = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			config := valid
			mutate(&config)
			if err := run(config); err == nil {
				t.Fatal("invalid Edge configuration was accepted")
			}
		})
	}
}
