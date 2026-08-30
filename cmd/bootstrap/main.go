package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/bootstrap"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	flags := flag.NewFlagSet("cpa-bootstrap", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	root := flags.String("root", "", "new deployment root")
	managementKeyOnly := flags.Bool("management-key-only", false, "print only the generated management key")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*root) == "" {
		return fmt.Errorf("usage: cpa-bootstrap --root /absolute/new-target")
	}
	result, err := bootstrap.Initialize(context.Background(), bootstrap.Config{Root: *root})
	if err != nil {
		return err
	}
	if *managementKeyOnly {
		_, err := fmt.Fprintln(os.Stdout, result.ManagementKey)
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{
		"management_key":           result.ManagementKey,
		"auth_snapshot_generation": result.AuthSnapshotGeneration,
		"quota_generation":         result.QuotaGeneration,
	})
}
