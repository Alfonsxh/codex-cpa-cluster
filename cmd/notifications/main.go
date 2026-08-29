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
	"github.com/Alfonsxh/codex-cpa-cluster/internal/notifications"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/ownership"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/scheduler"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/usage"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const envPrefix = "CLIPROXY_NOTIFICATIONS"

type appConfig struct {
	Root         string
	LogLevel     string
	Interval     time.Duration
	RoundTimeout time.Duration
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
		Use:           "cpa-notifications",
		Short:         "Send scheduled CPA quota reports and alerts to WeCom",
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
				return fmt.Errorf("read notification worker config: %w", err)
			}
			return nil
		},
		RunE: func(_ *cobra.Command, _ []string) error {
			return run(appConfig{
				Root: settings.GetString("root"), LogLevel: settings.GetString("log-level"),
				Interval: settings.GetDuration("interval"), RoundTimeout: settings.GetDuration("round-timeout"),
				MaxHealthAge: settings.GetDuration("max-health-age"), Once: settings.GetBool("once"),
				Health:       settings.GetBool("health"),
				RuntimeOwner: settings.GetString("runtime-owner"), LeaseTTL: settings.GetDuration("lease-ttl"),
			})
		},
	}
	flags := command.Flags()
	flags.String("config", "", "optional YAML, JSON, or TOML configuration file")
	flags.String("root", "/opt/codex-cpa-cluster", "existing CPA deployment root")
	flags.String("log-level", "info", "Zap log level")
	flags.Duration("interval", 30*time.Second, "robfig/cron @every notification evaluation interval")
	flags.Duration("round-timeout", 25*time.Second, "maximum duration of one notification round")
	flags.Duration("max-health-age", 3*time.Minute, "maximum healthy heartbeat age")
	flags.Bool("once", false, "evaluate one notification round and exit")
	flags.Bool("health", false, "read notification health without mutating the target")
	flags.String("runtime-owner", "codex-cpa", "explicitly activated runtime ownership label")
	flags.Duration("lease-ttl", 30*time.Second, "runtime and worker ownership lease lifetime")
	if err := settings.BindPFlags(flags); err != nil {
		panic(err)
	}
	if err := settings.BindEnv("root", "CLIPROXY_NOTIFICATIONS_ROOT", "CLIPROXY_ROOT"); err != nil {
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
	leaseConfig, err := ownership.WorkerConfig(config.RuntimeOwner, "notifications", config.LeaseTTL)
	if err != nil {
		return err
	}
	return ownership.RunWithExistingStore(ctx, config.Root, controlplane.Options{}, leaseConfig, func(
		runContext context.Context,
		fenceContext context.Context,
		store *controlplane.Store,
	) error {
		activity, err := usage.OpenReadOnly(config.Root, nil)
		if err != nil {
			return err
		}
		defer activity.Close()
		sender, err := notifications.NewFencedSender(
			&notifications.WebhookSender{Store: store, Timeout: 10 * time.Second},
			store,
		)
		if err != nil {
			return err
		}
		worker := &notifications.Worker{Store: store, Activity: activity, Sender: sender}
		if config.Once {
			result, err := worker.RunOnce(runContext)
			if err != nil {
				return err
			}
			return printJSON(result)
		}

		workerScheduler := scheduler.New(logger)
		runRound := func() {
			// Once cron admits a round, let it finish during ordinary shutdown,
			// while the fence context still cancels it immediately on lease loss.
			roundContext, cancel := context.WithTimeout(fenceContext, config.RoundTimeout)
			defer cancel()
			result, err := worker.RunOnce(roundContext)
			if err != nil {
				if runContext.Err() == nil {
					logger.Error("notification round failed", zap.Error(err))
				}
				return
			}
			if len(result.Sent) > 0 {
				logger.Info("notification round completed", zap.Strings("sent", result.Sent))
			}
		}
		if _, err := workerScheduler.AddFunc(scheduler.Every(config.Interval), runRound); err != nil {
			return fmt.Errorf("schedule notification worker: %w", err)
		}
		runRound()
		if runContext.Err() != nil {
			return nil
		}
		logger.Info("Go notification worker started", zap.Duration("interval", config.Interval))
		workerScheduler.Start()
		<-runContext.Done()
		waitForJobs := workerScheduler.Stop()
		<-waitForJobs.Done()
		logger.Info("Go notification worker stopped")
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
	settings, err := reader.ReadSettings(ctx)
	if err != nil {
		return err
	}
	notificationConfig, err := notifications.ParseConfig(settings)
	if err != nil {
		return err
	}
	secretStatuses, err := reader.SecretStatuses(ctx)
	if err != nil {
		return err
	}
	if notificationConfig.Enabled {
		webhook, configured := secretStatuses["wecom_webhook"]
		if !configured || strings.TrimSpace(webhook.SHA256) == "" {
			return errors.New("notification worker is enabled but the WeCom Webhook is not configured")
		}
	}
	state, found, err := notifications.ReadRuntimeState(ctx, reader)
	if err != nil {
		return err
	}
	if err := printJSON(state); err != nil {
		return err
	}
	if !notifications.HealthyRuntimeState(
		state, found, notificationConfig.Enabled, time.Now(), config.MaxHealthAge,
	) {
		return errors.New("notification worker is not healthy")
	}
	return nil
}

func (config appConfig) validate() error {
	if strings.TrimSpace(config.Root) == "" {
		return errors.New("notification worker root is required")
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
	if config.Interval < 5*time.Second || config.Interval > 5*time.Minute {
		return errors.New("notification interval must be between five seconds and five minutes")
	}
	if config.RoundTimeout < time.Second || config.RoundTimeout >= config.Interval {
		return errors.New("notification round timeout must be at least one second and shorter than the interval")
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
