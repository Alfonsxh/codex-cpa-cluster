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
	"github.com/Alfonsxh/codex-cpa-cluster/internal/logmaintenance"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/ownership"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/scheduler"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const envPrefix = "CLIPROXY_LOG_MAINTENANCE"

type appConfig struct {
	Root          string
	LogLevel      string
	Interval      time.Duration
	MaxFileSizeMB int64
	Backups       int
	MaxHealthAge  time.Duration
	Once          bool
	Health        bool
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
		Use:           "cpa-log-maintenance",
		Short:         "Bound CPA host logs with copy-truncate rotation",
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
				return fmt.Errorf("read log maintenance config: %w", err)
			}
			return nil
		},
		RunE: func(_ *cobra.Command, _ []string) error {
			return run(appConfig{
				Root: settings.GetString("root"), LogLevel: settings.GetString("log-level"),
				Interval: settings.GetDuration("interval"), MaxFileSizeMB: settings.GetInt64("max-file-size-mb"),
				Backups: settings.GetInt("backups"), MaxHealthAge: settings.GetDuration("max-health-age"),
				Once: settings.GetBool("once"), Health: settings.GetBool("health"),
				RuntimeOwner: settings.GetString("runtime-owner"), LeaseTTL: settings.GetDuration("lease-ttl"),
			})
		},
	}
	flags := command.Flags()
	flags.String("config", "", "optional YAML, JSON, or TOML configuration file")
	flags.String("root", "/opt/codex-cpa-cluster", "existing CPA deployment root")
	flags.String("log-level", "info", "Zap log level")
	flags.Duration("interval", time.Minute, "robfig/cron @every maintenance interval")
	flags.Int64("max-file-size-mb", 32, "maximum size of each active log in MiB")
	flags.Int("backups", 2, "number of copy-truncate backups to retain")
	flags.Duration("max-health-age", 5*time.Minute, "maximum healthy heartbeat age")
	flags.Bool("once", false, "run one maintenance round and exit")
	flags.Bool("health", false, "read the maintenance heartbeat without mutating the target")
	flags.String("runtime-owner", "codex-cpa", "explicitly activated runtime ownership label")
	flags.Duration("lease-ttl", 30*time.Second, "runtime and worker ownership lease lifetime")
	if err := settings.BindPFlags(flags); err != nil {
		panic(err)
	}
	if err := settings.BindEnv("root", "CLIPROXY_LOG_MAINTENANCE_ROOT", "CLIPROXY_ROOT"); err != nil {
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
	leaseConfig, err := ownership.WorkerConfig(config.RuntimeOwner, "log-maintenance", config.LeaseTTL)
	if err != nil {
		return err
	}
	return ownership.RunWithExistingStore(ctx, config.Root, controlplane.Options{}, leaseConfig, func(
		runContext context.Context,
		fenceContext context.Context,
		store *controlplane.Store,
	) error {
		service, err := logmaintenance.New(logmaintenance.Config{
			Root: config.Root, Store: store, Fence: store,
			MaxFileSizeMB: config.MaxFileSizeMB, Backups: config.Backups,
		})
		if err != nil {
			return err
		}
		if config.Once {
			state, err := service.RunOnce(runContext)
			if err != nil {
				return err
			}
			if err := printJSON(state); err != nil {
				return err
			}
			if state.LastError != "" {
				return errors.New("log maintenance round completed with rotation errors")
			}
			return nil
		}

		workerScheduler := scheduler.New(logger)
		runRound := func() {
			// An admitted copy-truncate round may finish during normal shutdown,
			// but the fence still cancels it if runtime ownership is lost.
			state, err := service.RunOnce(fenceContext)
			if err != nil {
				if runContext.Err() == nil {
					logger.Error("log maintenance round failed", zap.Error(err))
				}
				return
			}
			if state.LastError != "" {
				logger.Warn("log maintenance round completed with rotation errors", zap.String("error", state.LastError))
			} else if len(state.LastRotated) > 0 {
				logger.Info("log maintenance round completed", zap.Strings("rotated", state.LastRotated), zap.Int64("rotations", state.Rotations))
			}
		}
		if _, err := workerScheduler.AddFunc(scheduler.Every(config.Interval), runRound); err != nil {
			return fmt.Errorf("schedule log maintenance: %w", err)
		}
		runRound()
		if runContext.Err() != nil {
			return nil
		}
		logger.Info("Go log maintenance started", zap.Duration("interval", config.Interval))
		workerScheduler.Start()
		<-runContext.Done()
		waitForJobs := workerScheduler.Stop()
		<-waitForJobs.Done()
		logger.Info("Go log maintenance stopped")
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
	state, found, err := logmaintenance.ReadState(ctx, reader)
	if err != nil {
		return err
	}
	if err := printJSON(state); err != nil {
		return err
	}
	if !logmaintenance.Healthy(state, found, time.Now(), config.MaxHealthAge) {
		return errors.New("log maintenance is not healthy")
	}
	return nil
}

func (config appConfig) validate() error {
	if strings.TrimSpace(config.Root) == "" {
		return errors.New("log maintenance root is required")
	}
	if config.Once && config.Health {
		return errors.New("--once and --health cannot be used together")
	}
	if config.MaxHealthAge < time.Second {
		return errors.New("maximum health age must be at least one second")
	}
	if config.Health {
		return nil
	}
	if config.Interval < 5*time.Second {
		return errors.New("log maintenance interval must be at least five seconds")
	}
	if config.MaxFileSizeMB <= 0 {
		return errors.New("log maximum file size must be positive")
	}
	if config.Backups <= 0 || config.Backups > 100 {
		return errors.New("log backup count must be between 1 and 100")
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
