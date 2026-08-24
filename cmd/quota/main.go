package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/ownership"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/quota"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/scheduler"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const envPrefix = "CLIPROXY_QUOTA"

type appConfig struct {
	Root         string
	LogLevel     string
	Interval     time.Duration
	IntervalSet  bool
	MaxHealthAge time.Duration
	Once         bool
	Health       bool
	RuntimeOwner string
	LeaseTTL     time.Duration
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
		Use:           "cpa-quota",
		Short:         "Refresh official CPA account quotas",
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
				return fmt.Errorf("read quota worker config: %w", err)
			}
			return nil
		},
		RunE: func(command *cobra.Command, _ []string) error {
			interval := settings.GetDuration("interval")
			return run(appConfig{
				Root: settings.GetString("root"), LogLevel: settings.GetString("log-level"),
				Interval: interval, IntervalSet: command.Flags().Changed("interval") || interval != 0,
				MaxHealthAge: settings.GetDuration("max-health-age"),
				Once:         settings.GetBool("once"), Health: settings.GetBool("health"),
				RuntimeOwner: settings.GetString("runtime-owner"), LeaseTTL: settings.GetDuration("lease-ttl"),
			})
		},
	}
	flags := command.Flags()
	flags.String("config", "", "optional YAML, JSON, or TOML configuration file")
	flags.String("root", "/opt/codex-cpa-cluster", "existing CPA deployment root")
	flags.String("log-level", "info", "Zap log level")
	flags.Duration("interval", 0, "official quota polling interval override")
	flags.Duration("max-health-age", 3*time.Minute, "maximum healthy heartbeat age")
	flags.Bool("once", false, "refresh one complete quota snapshot and exit")
	flags.Bool("health", false, "read the quota heartbeat without mutating the target")
	flags.String("runtime-owner", "go-v2", "explicitly activated runtime ownership label")
	flags.Duration("lease-ttl", 30*time.Second, "runtime and worker ownership lease lifetime")
	if err := settings.BindPFlags(flags); err != nil {
		panic(err)
	}
	if err := settings.BindEnv("root", "CLIPROXY_QUOTA_ROOT", "CLIPROXY_ROOT"); err != nil {
		panic(err)
	}
	return command
}

func run(config appConfig) error {
	if err := config.validate(); err != nil {
		return err
	}
	if config.Health {
		return runHealth(config)
	}
	logger, err := newLogger(config.LogLevel)
	if err != nil {
		return err
	}
	defer func() { _ = logger.Sync() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	leaseConfig, err := ownership.WorkerConfig(config.RuntimeOwner, "quota", config.LeaseTTL)
	if err != nil {
		return err
	}
	return ownership.RunWithExistingStore(ctx, config.Root, controlplane.Options{}, leaseConfig, func(
		runContext context.Context,
		fenceContext context.Context,
		store *controlplane.Store,
	) error {
		settings, err := store.ReadSettings(runContext)
		if err != nil {
			return err
		}
		interval := quota.PollInterval(settings)
		if config.IntervalSet {
			interval = config.Interval
		}
		refresher := &quota.Refresher{Root: config.Root, Store: store}
		if config.Once {
			snapshot, err := refresher.RunOnce(runContext)
			if err != nil {
				return err
			}
			return printJSON(snapshot)
		}
		workerScheduler := scheduler.New(logger)
		runRound := func() {
			roundContext, cancel := context.WithTimeout(fenceContext, 2*time.Minute)
			defer cancel()
			snapshot, err := refresher.RunOnce(roundContext)
			if err != nil {
				_ = refresher.RecordError(fenceContext, err)
				if runContext.Err() == nil {
					logger.Error("official quota refresh failed", zap.Error(err))
				}
				return
			}
			unavailable := 0
			for _, account := range snapshot.Accounts {
				if account.Status != "ok" {
					unavailable++
				}
			}
			logger.Info("official quota refresh completed", zap.Int("accounts", len(snapshot.Accounts)), zap.Int("unavailable", unavailable))
		}
		if _, err := workerScheduler.AddFunc(scheduler.Every(interval), runRound); err != nil {
			return fmt.Errorf("schedule official quota refresh: %w", err)
		}
		runRound()
		if runContext.Err() != nil {
			return nil
		}
		logger.Info("Go v2 official quota worker started", zap.Duration("interval", interval))
		workerScheduler.Start()
		<-runContext.Done()
		waitForJobs := workerScheduler.Stop()
		<-waitForJobs.Done()
		logger.Info("Go v2 official quota worker stopped")
		return nil
	})
}

func runHealth(config appConfig) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reader, err := controlplane.OpenReadOnly(ctx, config.Root)
	if err != nil {
		return err
	}
	defer reader.Close()
	state, found, err := quota.ReadState(ctx, reader)
	if err != nil {
		return err
	}
	if err := printJSON(state); err != nil {
		return err
	}
	if !quota.Healthy(state, found, time.Now(), config.MaxHealthAge) {
		return errors.New("official quota worker is not healthy")
	}
	return nil
}

func (config appConfig) validate() error {
	if strings.TrimSpace(config.Root) == "" {
		return errors.New("quota worker root is required")
	}
	if config.Once && config.Health {
		return errors.New("--once and --health cannot be used together")
	}
	if config.MaxHealthAge < time.Second {
		return errors.New("maximum health age must be at least one second")
	}
	if config.IntervalSet && (config.Interval < 30*time.Second || config.Interval > time.Hour) {
		return errors.New("quota polling interval must be between 30 seconds and one hour")
	}
	return nil
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
