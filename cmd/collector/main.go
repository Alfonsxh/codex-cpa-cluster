package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/collector"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/ownership"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/usage"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const envPrefix = "CLIPROXY_COLLECTOR"

type appConfig struct {
	Root          string
	LogLevel      string
	Interval      time.Duration
	IntervalSet   bool
	BatchSize     int
	BatchSizeSet  bool
	Once          bool
	Health        bool
	RebuildWeekly bool
	RuntimeOwner  string
	LeaseTTL      time.Duration
}

func main() {
	if err := newCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newCommand() *cobra.Command {
	settings := viper.New()
	settings.SetEnvPrefix(envPrefix)
	settings.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	settings.AutomaticEnv()
	command := &cobra.Command{
		Use:           "cpa-collector",
		Short:         "Run the Go v2 CPA usage collector",
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRunE: func(command *cobra.Command, _ []string) error {
			configFile, err := command.Flags().GetString("config")
			if err != nil {
				return err
			}
			if configFile == "" {
				return nil
			}
			settings.SetConfigFile(configFile)
			if err := settings.ReadInConfig(); err != nil {
				return fmt.Errorf("read Collector config: %w", err)
			}
			return nil
		},
		RunE: func(command *cobra.Command, _ []string) error {
			interval := settings.GetDuration("interval")
			batchSize := settings.GetInt("batch-size")
			return run(appConfig{
				Root:          settings.GetString("root"),
				LogLevel:      settings.GetString("log-level"),
				Interval:      interval,
				IntervalSet:   command.Flags().Changed("interval") || interval != 0,
				BatchSize:     batchSize,
				BatchSizeSet:  command.Flags().Changed("batch-size") || batchSize != 0,
				Once:          settings.GetBool("once"),
				Health:        settings.GetBool("health"),
				RebuildWeekly: settings.GetBool("rebuild-weekly-usage"),
				RuntimeOwner:  settings.GetString("runtime-owner"),
				LeaseTTL:      settings.GetDuration("lease-ttl"),
			})
		},
	}
	flags := command.Flags()
	flags.String("config", "", "optional YAML, JSON, or TOML configuration file")
	flags.String("root", "/opt/codex-cpa-cluster", "existing CPA deployment root")
	flags.String("log-level", "info", "Zap log level")
	flags.Duration("interval", 0, "collector polling interval override")
	flags.Int("batch-size", 0, "usage queue batch size override")
	flags.Bool("once", false, "run one collection round and exit")
	flags.Bool("health", false, "check collector heartbeat and exit")
	flags.Bool("rebuild-weekly-usage", false, "rebuild materialized weekly usage and publish quota state")
	flags.String("runtime-owner", "go-v2", "explicitly activated runtime ownership label")
	flags.Duration("lease-ttl", 30*time.Second, "runtime and worker ownership lease lifetime")
	if err := settings.BindPFlags(flags); err != nil {
		panic(err)
	}
	if err := settings.BindEnv("root", "CLIPROXY_COLLECTOR_ROOT", "CLIPROXY_ROOT"); err != nil {
		panic(err)
	}
	return command
}

func run(config appConfig) error {
	if strings.TrimSpace(config.Root) == "" {
		return errors.New("collector root is required")
	}
	if config.IntervalSet && (config.Interval < 500*time.Millisecond || config.Interval > 60*time.Second) {
		return errors.New("collector interval must be between 500ms and 60s")
	}
	if config.BatchSizeSet && (config.BatchSize < 1 || config.BatchSize > 500) {
		return errors.New("collector batch size must be between 1 and 500")
	}
	logger, err := newLogger(config.LogLevel)
	if err != nil {
		return err
	}
	defer func() { _ = logger.Sync() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if config.Health {
		reader, err := usage.OpenReadOnly(config.Root, nil)
		if err != nil {
			return err
		}
		defer reader.Close()
		status, err := reader.Status(ctx)
		if err != nil {
			return err
		}
		if err := printJSON(status); err != nil {
			return err
		}
		if status.Status != "healthy" || status.HeartbeatAt == 0 {
			return errors.New("usage collector is not healthy")
		}
		return nil
	}
	leaseConfig, err := ownership.WorkerConfig(config.RuntimeOwner, "usage-collector", config.LeaseTTL)
	if err != nil {
		return err
	}
	return ownership.RunWithExistingStore(
		ctx,
		config.Root,
		controlplane.Options{},
		leaseConfig,
		func(runContext context.Context, _ context.Context, store *controlplane.Store) error {
			return runOwnedCollector(runContext, config, logger, store)
		},
	)
}

func runOwnedCollector(
	runContext context.Context,
	config appConfig,
	logger *zap.Logger,
	store *controlplane.Store,
) error {
	writer, err := usage.OpenWriterPath(filepath.Join(config.Root, usage.DatabaseRelativePath), nil)
	if err != nil {
		return err
	}
	defer writer.Close()
	fencedWriter, err := collector.NewFencedRuntimeWriter(writer, store)
	if err != nil {
		return err
	}
	fencedPublisher, err := collector.NewFencedQuotaPublisher(
		&collector.SnapshotPublisher{Root: config.Root},
		store,
	)
	if err != nil {
		return err
	}
	settings, err := store.ReadSettings(runContext)
	if err != nil {
		return err
	}
	runtimeConfig, interval, err := collector.RuntimeConfigFromSettings(settings)
	if err != nil {
		return err
	}
	if config.IntervalSet {
		interval = config.Interval
	}
	if config.BatchSizeSet {
		runtimeConfig.BatchSize = config.BatchSize
	}
	managementKey, found, err := store.ReadSecret(runContext, "cpa_management_key")
	if err != nil {
		return err
	}
	if !found || !validManagementKey(managementKey) {
		return errors.New("valid CPA management key is required")
	}
	runtimeConfig.ManagementKey = managementKey
	runtimeConfig.HeartbeatStaleAfterSeconds = max(15, int64(interval.Seconds()*3+1))
	runtime := &collector.Runtime{
		Control: store, Writer: fencedWriter,
		Publisher: fencedPublisher,
		Config:    runtimeConfig,
	}
	if config.RebuildWeekly {
		result, err := runtime.Rebuild(runContext)
		if err != nil {
			return err
		}
		return printJSON(result)
	}
	if config.Once {
		result, err := runtime.RunOnce(runContext)
		if err != nil {
			return err
		}
		if err := printJSON(result); err != nil {
			return err
		}
		if len(result.Errors) != 0 {
			return fmt.Errorf("collector round completed with %d error(s)", len(result.Errors))
		}
		return nil
	}
	logger.Info("Go v2 usage collector started", zap.Duration("interval", interval), zap.Int("batch_size", runtimeConfig.BatchSize))
	for {
		result, err := runtime.RunOnce(runContext)
		if err != nil {
			if runContext.Err() != nil {
				return nil
			}
			logger.Error("usage collector round failed", zap.Error(err))
			_ = fencedWriter.UpdateCollectorStatus(runContext, err.Error())
		} else if result.Received != 0 || len(result.Errors) != 0 {
			logger.Info(
				"usage collector round completed",
				zap.Int("received", result.Received), zap.Int("inserted", result.Inserted),
				zap.Int("duplicate", result.Duplicate), zap.Int("unmapped", result.Unmapped),
				zap.Int("ignored", result.Ignored), zap.Strings("errors", result.Errors),
			)
		}
		timer := time.NewTimer(interval)
		select {
		case <-runContext.Done():
			timer.Stop()
			logger.Info("Go v2 usage collector stopped")
			return nil
		case <-timer.C:
		}
	}
}

func validManagementKey(value string) bool {
	if len(value) < 12 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func newLogger(level string) (*zap.Logger, error) {
	var parsedLevel zapcore.Level
	if err := parsedLevel.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid log level %q: %w", level, err)
	}
	config := zap.NewProductionConfig()
	config.Level = zap.NewAtomicLevelAt(parsedLevel)
	return config.Build()
}
