package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandReadsEnvironmentWithViper(t *testing.T) {
	t.Setenv("CLIPROXY_GATEWAY_PUBLIC_ADDRESS", "127.0.0.1:28317")
	t.Setenv("CLIPROXY_GATEWAY_INTERNAL_ADDRESS", "127.0.0.1:28317")
	command := newCommand()
	command.SetArgs([]string{"--access-log="})

	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "listen addresses must differ") {
		t.Fatalf("environment configuration error = %v", err)
	}
}

func TestCommandReadsYAMLConfigurationWithViper(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gateway.yaml")
	config := []byte("public-address: 127.0.0.1:28319\ninternal-address: 127.0.0.1:28319\n")
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatalf("write gateway config: %v", err)
	}
	command := newCommand()
	command.SetArgs([]string{"--config", configPath, "--access-log="})

	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "listen addresses must differ") {
		t.Fatalf("file configuration error = %v", err)
	}
}

func TestCommandFlagsOverrideConfiguration(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gateway.json")
	config := []byte(`{"public-address":"127.0.0.1:28317","internal-address":"127.0.0.1:28318"}`)
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatalf("write gateway config: %v", err)
	}
	command := newCommand()
	command.SetArgs([]string{
		"--config", configPath,
		"--public-address", "127.0.0.1:28318",
		"--access-log=",
	})

	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "listen addresses must differ") {
		t.Fatalf("flag precedence error = %v", err)
	}
}
