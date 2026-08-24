package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/migrationcheck"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const envPrefix = "CLIPROXY_MIGRATION_COMPARE"

type appConfig struct {
	V1PublicURL      string
	V2PublicURL      string
	V1InternalURL    string
	V2InternalURL    string
	TestKeyFile      string
	StreamRequest    string
	Timeout          time.Duration
	ConfirmTestKey   bool
	AllowNonLoopback bool
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
		Use:           "cpa-migration-compare",
		Short:         "Compare isolated v1 and Go v2 CPA contracts with a dedicated test Key",
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRunE: func(command *cobra.Command, _ []string) error {
			configFile, err := command.Flags().GetString("config")
			if err != nil || configFile == "" {
				return err
			}
			settings.SetConfigFile(configFile)
			if err := settings.ReadInConfig(); err != nil {
				return fmt.Errorf("read migration comparison config: %w", err)
			}
			return nil
		},
		RunE: func(command *cobra.Command, _ []string) error {
			return run(command.Context(), output, appConfig{
				V1PublicURL: settings.GetString("v1-public-url"), V2PublicURL: settings.GetString("v2-public-url"),
				V1InternalURL: settings.GetString("v1-internal-url"), V2InternalURL: settings.GetString("v2-internal-url"),
				TestKeyFile: settings.GetString("test-key-file"), StreamRequest: settings.GetString("stream-request-file"),
				Timeout: settings.GetDuration("timeout"), ConfirmTestKey: settings.GetBool("confirm-dedicated-test-key"),
				AllowNonLoopback: settings.GetBool("allow-non-loopback-test-targets"),
			})
		},
	}
	flags := command.Flags()
	flags.String("config", "", "optional YAML, JSON, or TOML configuration file")
	flags.String("v1-public-url", "http://127.0.0.1:18317", "isolated v1 public origin")
	flags.String("v2-public-url", "http://127.0.0.1:28317", "isolated Go v2 public origin")
	flags.String("v1-internal-url", "", "optional isolated v1 internal origin")
	flags.String("v2-internal-url", "", "optional isolated Go v2 internal origin")
	flags.String("test-key-file", "", "0600 file containing one dedicated non-production API Key")
	flags.String("stream-request-file", "", "optional JSON request fixture with stream=true for /v1/responses")
	flags.Duration("timeout", 30*time.Second, "per-request and initial-stream timeout")
	flags.Bool("confirm-dedicated-test-key", false, "confirm that the Key and requests are isolated non-production test traffic")
	flags.Bool("allow-non-loopback-test-targets", false, "allow explicitly selected remote Test origins")
	if err := settings.BindPFlags(flags); err != nil {
		panic(err)
	}
	command.AddCommand(newStateCommand(output))
	return command
}

func newStateCommand(output io.Writer) *cobra.Command {
	settings := viper.New()
	settings.SetEnvPrefix(envPrefix + "_STATE")
	settings.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	settings.AutomaticEnv()
	command := &cobra.Command{
		Use:           "state",
		Short:         "Compare secret-free checkpoints from two isolated state copies",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRunE: func(command *cobra.Command, _ []string) error {
			configFile, err := command.Flags().GetString("config")
			if err != nil || configFile == "" {
				return err
			}
			settings.SetConfigFile(configFile)
			if err := settings.ReadInConfig(); err != nil {
				return fmt.Errorf("read state comparison config: %w", err)
			}
			return nil
		},
		RunE: func(command *cobra.Command, _ []string) error {
			return runStateComparison(
				command.Context(),
				output,
				settings.GetString("v1-root"),
				settings.GetString("v2-root"),
				settings.GetBool("confirm-isolated-state-copies"),
			)
		},
	}
	flags := command.Flags()
	flags.String("config", "", "optional YAML, JSON, or TOML configuration file")
	flags.String("v1-root", "", "isolated v1 state-copy root")
	flags.String("v2-root", "", "isolated Go v2 state-copy root")
	flags.Bool(
		"confirm-isolated-state-copies",
		false,
		"confirm both roots are disposable copies and not a live deployment",
	)
	if err := settings.BindPFlags(flags); err != nil {
		panic(err)
	}
	command.AddCommand(newStateSummaryCommand(output))
	return command
}

func newStateSummaryCommand(output io.Writer) *cobra.Command {
	var root string
	var confirmed bool
	command := &cobra.Command{
		Use:           "summarize",
		Short:         "Create a secret-free checkpoint for one isolated state copy",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(command *cobra.Command, _ []string) error {
			return runStateSummary(command.Context(), output, root, confirmed)
		},
	}
	command.Flags().StringVar(&root, "root", "", "isolated state-copy root")
	command.Flags().BoolVar(
		&confirmed,
		"confirm-isolated-state-copy",
		false,
		"confirm the root is a disposable copy and not a live deployment",
	)
	return command
}

func runStateSummary(
	ctx context.Context,
	output io.Writer,
	root string,
	confirmed bool,
) error {
	if !confirmed {
		return errors.New("refusing state summary without --confirm-isolated-state-copy")
	}
	summary, err := migrationcheck.SummarizeState(ctx, root)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(summary); err != nil {
		return fmt.Errorf("write state summary: %w", err)
	}
	return nil
}

func runStateComparison(
	ctx context.Context,
	output io.Writer,
	v1Root string,
	v2Root string,
	confirmed bool,
) error {
	if !confirmed {
		return errors.New("refusing state comparison without --confirm-isolated-state-copies")
	}
	comparison, err := migrationcheck.CompareStateRoots(ctx, v1Root, v2Root)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(comparison); err != nil {
		return fmt.Errorf("write state comparison report: %w", err)
	}
	if !comparison.Passed {
		return errors.New("v1/v2 durable state comparison failed")
	}
	return nil
}

func run(ctx context.Context, output io.Writer, config appConfig) error {
	if !config.ConfirmTestKey {
		return errors.New("refusing paired traffic without --confirm-dedicated-test-key")
	}
	testKey, err := migrationcheck.ReadPrivateTestKey(config.TestKeyFile)
	if err != nil {
		return err
	}
	var streamBody []byte
	if strings.TrimSpace(config.StreamRequest) != "" {
		streamBody, err = migrationcheck.ReadStreamFixture(config.StreamRequest)
		if err != nil {
			return err
		}
	}
	runner, err := migrationcheck.New(migrationcheck.Config{
		V1PublicURL: config.V1PublicURL, V2PublicURL: config.V2PublicURL,
		V1InternalURL: config.V1InternalURL, V2InternalURL: config.V2InternalURL,
		TestKey: testKey, Timeout: config.Timeout, StreamBody: streamBody,
		AllowNonLoopback: config.AllowNonLoopback,
	})
	if err != nil {
		return err
	}
	report := runner.Run(ctx)
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("write migration comparison report: %w", err)
	}
	if !report.Passed {
		return errors.New("v1/v2 migration comparison failed")
	}
	return nil
}
