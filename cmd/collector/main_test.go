package main

import "testing"

func TestValidManagementKey(t *testing.T) {
	for _, value := range []string{"short", "contains whitespace", "line\nbreak"} {
		if validManagementKey(value) {
			t.Fatalf("invalid management key %q was accepted", value)
		}
	}
	if !validManagementKey("valid-management-key") {
		t.Fatal("valid management key was rejected")
	}
}

func TestCollectorCommandExposesParityModes(t *testing.T) {
	command := newCommand()
	for _, name := range []string{"once", "health", "rebuild-weekly-usage", "interval", "batch-size"} {
		if command.Flags().Lookup(name) == nil {
			t.Fatalf("collector flag %q is missing", name)
		}
	}
}
