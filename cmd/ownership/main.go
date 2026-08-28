package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/ownership"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const envPrefix = "CLIPROXY_OWNERSHIP"

type commandConfig struct {
	Root string
	TTL  time.Duration
}

type leaseStatus struct {
	Found      bool   `json:"found"`
	Active     bool   `json:"active"`
	Scope      string `json:"scope"`
	Owner      string `json:"owner,omitempty"`
	Generation int64  `json:"generation,omitempty"`
	AcquiredAt int64  `json:"acquired_at,omitempty"`
	RenewedAt  int64  `json:"renewed_at,omitempty"`
	ExpiresAt  int64  `json:"expires_at,omitempty"`
	ReleasedAt *int64 `json:"released_at,omitempty"`
}

func main() {
	if err := newCommand(os.Stdout).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newCommand(output io.Writer) *cobra.Command {
	settings := viper.New()
	settings.SetEnvPrefix(envPrefix)
	settings.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	settings.AutomaticEnv()
	command := &cobra.Command{
		Use:           "cpa-ownership",
		Short:         "Inspect and explicitly transfer CPA runtime ownership",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(command *cobra.Command, _ []string) error {
			configFile, err := command.Flags().GetString("config")
			if err != nil || configFile == "" {
				return err
			}
			settings.SetConfigFile(configFile)
			if err := settings.ReadInConfig(); err != nil {
				return fmt.Errorf("read ownership config: %w", err)
			}
			return nil
		},
	}
	flags := command.PersistentFlags()
	flags.String("config", "", "optional YAML, JSON, or TOML configuration file")
	flags.String("root", "/opt/codex-cpa-cluster", "existing CPA deployment root")
	flags.Duration("ttl", 30*time.Second, "runtime ownership lease lifetime")
	if err := settings.BindPFlags(flags); err != nil {
		panic(err)
	}
	if err := settings.BindEnv("root", "CLIPROXY_OWNERSHIP_ROOT", "CLIPROXY_ROOT"); err != nil {
		panic(err)
	}

	command.AddCommand(newStatusCommand(settings, output))
	command.AddCommand(newActivateCommand(settings, output))
	command.AddCommand(newReleaseCommand(settings, output))
	return command
}

func newStatusCommand(settings *viper.Viper, output io.Writer) *cobra.Command {
	var field string
	command := &cobra.Command{
		Use:   "status [scope]",
		Short: "Read a lease without mutating the target",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			scope := ownership.RuntimeScope
			if len(arguments) == 1 {
				scope = arguments[0]
			}
			return runStatus(command.Context(), output, commandConfig{
				Root: settings.GetString("root"), TTL: settings.GetDuration("ttl"),
			}, scope, field)
		},
	}
	command.Flags().StringVar(
		&field,
		"field",
		"",
		"print one status field without JSON encoding",
	)
	return command
}

func newActivateCommand(settings *viper.Viper, output io.Writer) *cobra.Command {
	var owner string
	var expectedOwner string
	var expectedGeneration int64
	var confirmation string
	var allowEmpty bool
	var legacyBootstrapConfirmation string
	command := &cobra.Command{
		Use:   "activate",
		Short: "Activate runtime ownership after the previous runtime is stopped",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runActivate(command.Context(), output, commandConfig{
				Root: settings.GetString("root"), TTL: settings.GetDuration("ttl"),
			}, owner, expectedOwner, expectedGeneration, confirmation, allowEmpty, legacyBootstrapConfirmation)
		},
	}
	command.Flags().StringVar(&owner, "owner", "go-v2", "runtime owner label")
	command.Flags().StringVar(&expectedOwner, "expected-owner", "", "required previous owner when replacing an expired lease")
	command.Flags().Int64Var(&expectedGeneration, "expected-generation", 0, "required previous generation when replacing an expired lease")
	command.Flags().StringVar(&confirmation, "confirm-owner", "", "repeat the new owner label to confirm activation")
	command.Flags().BoolVar(&allowEmpty, "allow-empty-bootstrap", false, "allow first ownership record only for an isolated Test root")
	command.Flags().StringVar(
		&legacyBootstrapConfirmation,
		"confirm-legacy-bootstrap",
		"",
		"for a first live cutover, confirm as legacy-writers-stopped:<absolute-root>",
	)
	return command
}

func newReleaseCommand(settings *viper.Viper, output io.Writer) *cobra.Command {
	var scope string
	var expectedOwner string
	var confirmation string
	command := &cobra.Command{
		Use:   "release",
		Short: "Release one exact lease generation",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runRelease(command.Context(), output, commandConfig{
				Root: settings.GetString("root"), TTL: settings.GetDuration("ttl"),
			}, scope, expectedOwner, confirmation)
		},
	}
	command.Flags().StringVar(&scope, "scope", ownership.RuntimeScope, "lease scope")
	command.Flags().StringVar(&expectedOwner, "expected-owner", "", "exact current owner")
	command.Flags().StringVar(&confirmation, "confirm-release", "", "confirm as scope:generation")
	return command
}

func runStatus(
	ctx context.Context,
	output io.Writer,
	config commandConfig,
	scope string,
	field string,
) error {
	reader, err := controlplane.OpenReadOnly(ctx, config.Root)
	if err != nil {
		return err
	}
	defer reader.Close()
	lease, found, err := reader.ReadLease(ctx, strings.TrimSpace(scope))
	if err != nil {
		return err
	}
	status := redactedLeaseStatus(lease, found, time.Now())
	if strings.TrimSpace(field) != "" {
		return writeStatusField(output, status, field)
	}
	return writeJSON(output, status)
}

func writeStatusField(output io.Writer, status leaseStatus, field string) error {
	var value any
	switch strings.TrimSpace(field) {
	case "found":
		value = status.Found
	case "active":
		value = status.Active
	case "scope":
		value = status.Scope
	case "owner":
		value = status.Owner
	case "generation":
		value = status.Generation
	case "acquired_at":
		value = status.AcquiredAt
	case "renewed_at":
		value = status.RenewedAt
	case "expires_at":
		value = status.ExpiresAt
	case "released_at":
		if status.ReleasedAt == nil {
			value = ""
		} else {
			value = *status.ReleasedAt
		}
	default:
		return fmt.Errorf("unsupported status field: %s", field)
	}
	_, err := fmt.Fprintln(output, value)
	return err
}

func runActivate(
	ctx context.Context,
	output io.Writer,
	config commandConfig,
	owner string,
	expectedOwner string,
	expectedGeneration int64,
	confirmation string,
	allowEmpty bool,
	legacyBootstrapConfirmation string,
) error {
	owner = strings.TrimSpace(owner)
	expectedOwner = strings.TrimSpace(expectedOwner)
	if owner == "" || confirmation != owner {
		return errors.New("--confirm-owner must exactly match --owner")
	}
	if config.TTL < 5*time.Second || config.TTL > 5*time.Minute {
		return errors.New("ownership TTL must be between five seconds and five minutes")
	}
	if err := requireExistingTarget(config.Root); err != nil {
		return err
	}
	reader, err := controlplane.OpenReadOnly(ctx, config.Root)
	if err != nil {
		return err
	}
	current, found, readError := reader.ReadLease(ctx, ownership.RuntimeScope)
	closeError := reader.Close()
	if readError != nil {
		return readError
	}
	if closeError != nil {
		return closeError
	}
	if !found {
		cleanRoot, err := filepath.Abs(config.Root)
		if err != nil {
			return fmt.Errorf("resolve ownership root: %w", err)
		}
		expectedLegacyConfirmation := "legacy-writers-stopped:" + filepath.Clean(cleanRoot)
		if allowEmpty && legacyBootstrapConfirmation != "" {
			return errors.New("isolated and legacy ownership bootstrap confirmations are mutually exclusive")
		}
		if !allowEmpty && legacyBootstrapConfirmation != expectedLegacyConfirmation {
			return fmt.Errorf(
				"empty ownership bootstrap requires --allow-empty-bootstrap on an isolated Test root or --confirm-legacy-bootstrap %q after every legacy writer has stopped",
				expectedLegacyConfirmation,
			)
		}
		if expectedOwner != "" {
			return errors.New("--expected-owner must be empty when no prior ownership lease exists")
		}
		if expectedGeneration != 0 {
			return errors.New("--expected-generation must be zero when no prior ownership lease exists")
		}
	} else {
		if allowEmpty || legacyBootstrapConfirmation != "" {
			return errors.New("bootstrap confirmation flags require an empty ownership history")
		}
		if current.ExpiresAt > time.Now().Unix() {
			return &controlplane.LeaseHeldError{
				Scope: current.Scope, Owner: current.Owner,
				Generation: current.Generation, ExpiresAt: current.ExpiresAt,
			}
		}
		if expectedOwner == "" || expectedOwner != current.Owner {
			return errors.New("--expected-owner must exactly match the expired prior owner")
		}
		if expectedGeneration < 1 || expectedGeneration != current.Generation {
			return errors.New("--expected-generation must exactly match the expired prior generation")
		}
	}
	store, err := controlplane.OpenExisting(ctx, config.Root, controlplane.Options{})
	if err != nil {
		return err
	}
	defer store.Close()
	lease, err := store.TakeLease(ctx, ownership.RuntimeScope, owner, config.TTL)
	if err != nil {
		return err
	}
	return writeJSON(output, redactedLeaseStatus(lease, true, time.Now()))
}

func runRelease(
	ctx context.Context,
	output io.Writer,
	config commandConfig,
	scope string,
	expectedOwner string,
	confirmation string,
) error {
	scope = strings.TrimSpace(scope)
	expectedOwner = strings.TrimSpace(expectedOwner)
	if expectedOwner == "" {
		return errors.New("--expected-owner is required")
	}
	if err := requireExistingTarget(config.Root); err != nil {
		return err
	}
	reader, err := controlplane.OpenReadOnly(ctx, config.Root)
	if err != nil {
		return err
	}
	lease, found, readError := reader.ReadLease(ctx, scope)
	closeError := reader.Close()
	if readError != nil {
		return readError
	}
	if closeError != nil {
		return closeError
	}
	if !found {
		return controlplane.ErrLeaseMissing
	}
	if lease.Owner != expectedOwner {
		return errors.New("--expected-owner does not match the current lease")
	}
	expectedConfirmation := fmt.Sprintf("%s:%d", scope, lease.Generation)
	if confirmation != expectedConfirmation {
		return fmt.Errorf("--confirm-release must equal %s", expectedConfirmation)
	}
	store, err := controlplane.OpenExisting(ctx, config.Root, controlplane.Options{})
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.ReleaseLease(ctx, lease); err != nil {
		return err
	}
	released, found, err := store.ReadLease(ctx, scope)
	if err != nil {
		return err
	}
	return writeJSON(output, redactedLeaseStatus(released, found, time.Now()))
}

func requireExistingTarget(root string) error {
	root = strings.TrimSpace(root)
	if root == "" {
		return errors.New("control-plane root is required")
	}
	for _, relative := range []string{
		"state/control-plane.sqlite3",
		"secrets/control-plane.key",
	} {
		path := filepath.Join(root, relative)
		information, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("existing target requires %s: %w", relative, err)
		}
		if information.Mode()&os.ModeSymlink != 0 || !information.Mode().IsRegular() {
			return fmt.Errorf("existing target %s must be a regular non-symlink file", relative)
		}
	}
	return nil
}

func redactedLeaseStatus(lease controlplane.Lease, found bool, now time.Time) leaseStatus {
	if !found {
		return leaseStatus{Found: false, Active: false, Scope: lease.Scope}
	}
	return leaseStatus{
		Found: true, Active: lease.Token != "" && lease.ExpiresAt > now.Unix(),
		Scope: lease.Scope, Owner: lease.Owner, Generation: lease.Generation,
		AcquiredAt: lease.AcquiredAt, RenewedAt: lease.RenewedAt,
		ExpiresAt: lease.ExpiresAt, ReleasedAt: lease.ReleasedAt,
	}
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
