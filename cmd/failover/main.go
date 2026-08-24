package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/failover"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/ownership"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/scheduler"
	usagestore "github.com/Alfonsxh/codex-cpa-cluster/internal/usage"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const envPrefix = "CLIPROXY_FAILOVER"

type appConfig struct {
	Root                 string
	LogLevel             string
	SchedulerInterval    time.Duration
	MaxHealthAge         time.Duration
	ProbeTimeout         time.Duration
	ProbeConcurrency     int
	AccountAddressFormat string
	GatewayProbes        []string
	SnapshotTimeout      time.Duration
	Once                 bool
	Health               bool
	RuntimeOwner         string
	LeaseTTL             time.Duration
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
		Use:           "cpa-failover",
		Short:         "Run quota-driven CPA account failover",
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
				return fmt.Errorf("read failover worker config: %w", err)
			}
			return nil
		},
		RunE: func(_ *cobra.Command, _ []string) error {
			return run(appConfig{
				Root: settings.GetString("root"), LogLevel: settings.GetString("log-level"),
				SchedulerInterval:    settings.GetDuration("scheduler-interval"),
				MaxHealthAge:         settings.GetDuration("max-health-age"),
				ProbeTimeout:         settings.GetDuration("probe-timeout"),
				ProbeConcurrency:     settings.GetInt("probe-concurrency"),
				AccountAddressFormat: settings.GetString("account-address-format"),
				GatewayProbes:        settings.GetStringSlice("gateway-probe-url"),
				SnapshotTimeout:      settings.GetDuration("snapshot-timeout"),
				Once:                 settings.GetBool("once"), Health: settings.GetBool("health"),
				RuntimeOwner: settings.GetString("runtime-owner"), LeaseTTL: settings.GetDuration("lease-ttl"),
			})
		},
	}
	flags := command.Flags()
	flags.String("config", "", "optional YAML, JSON, or TOML configuration file")
	flags.String("root", "/opt/codex-cpa-cluster", "existing CPA deployment root")
	flags.String("log-level", "info", "Zap log level")
	flags.Duration("scheduler-interval", 5*time.Second, "controller due-check interval")
	flags.Duration("max-health-age", 3*time.Minute, "maximum healthy heartbeat age")
	flags.Duration("probe-timeout", time.Second, "per-account TCP probe timeout")
	flags.Int("probe-concurrency", 8, "maximum parallel account probes")
	flags.String("account-address-format", "cliproxy-%s:8317", "Docker-network account TCP address format")
	flags.StringSlice("gateway-probe-url", []string{"http://edge:8319"}, "Gateway internal URLs used to confirm auth snapshot activation")
	flags.Duration("snapshot-timeout", 8*time.Second, "maximum auth snapshot activation wait")
	flags.Bool("once", false, "force one complete failover check and exit")
	flags.Bool("health", false, "read the failover heartbeat without mutating the target")
	flags.String("runtime-owner", "go-v2", "explicitly activated runtime ownership label")
	flags.Duration("lease-ttl", 30*time.Second, "runtime and worker ownership lease lifetime")
	if err := settings.BindPFlags(flags); err != nil {
		panic(err)
	}
	if err := settings.BindEnv("root", "CLIPROXY_FAILOVER_ROOT", "CLIPROXY_ROOT"); err != nil {
		panic(err)
	}
	if err := settings.BindEnv("gateway-probe-url", "CLIPROXY_GATEWAY_INTERNAL_URL"); err != nil {
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
	leaseConfig, err := ownership.WorkerConfig(config.RuntimeOwner, "account-failover", config.LeaseTTL)
	if err != nil {
		return err
	}
	return ownership.RunWithExistingStore(ctx, config.Root, controlplane.Options{}, leaseConfig, func(
		runContext context.Context,
		fenceContext context.Context,
		store *controlplane.Store,
	) error {
		usage, err := usagestore.OpenReadOnly(config.Root, nil)
		if err != nil {
			return fmt.Errorf("open failover usage reader: %w", err)
		}
		defer usage.Close()
		address := func(account controlplane.Account) string {
			return fmt.Sprintf(config.AccountAddressFormat, account.ID)
		}
		controller := &failover.Controller{
			Store: store,
			Probe: failover.TCPRuntimeProbe{
				Address: address, DialTimeout: config.ProbeTimeout,
				MaxConcurrency: config.ProbeConcurrency,
			},
			Activity: usage,
			Snapshots: &failover.AuthSnapshotPublisher{
				Root: config.Root, Store: store, ProbeURLs: config.GatewayProbes,
				WaitTimeout: config.SnapshotTimeout, Fence: store,
			},
			Audit: &failover.FileAuditRecorder{Root: config.Root, Fence: store},
		}
		if config.Once {
			result, runError := controller.RunForced(runContext)
			if printError := printJSON(result); printError != nil {
				return errors.Join(runError, printError)
			}
			return runError
		}
		workerScheduler := scheduler.New(logger)
		runRound := func(force bool) {
			roundContext, cancel := context.WithTimeout(fenceContext, 2*time.Minute)
			defer cancel()
			var result failover.ControllerResult
			var runError error
			if force {
				result, runError = controller.RunForced(roundContext)
			} else {
				result, runError = controller.RunOnce(roundContext)
			}
			if runError != nil {
				if runContext.Err() == nil {
					logger.Error("account failover check failed", zap.Error(runError))
				}
				return
			}
			if result.Checked {
				logger.Info(
					"account failover check completed",
					zap.Int("moved_users", result.MovedUsers),
					zap.Bool("capacity_unavailable", result.CapacityUnavailable),
				)
			}
		}
		runRound(true)
		if runContext.Err() != nil {
			return nil
		}
		if _, err := workerScheduler.AddFunc(scheduler.Every(config.SchedulerInterval), func() { runRound(false) }); err != nil {
			return fmt.Errorf("schedule account failover: %w", err)
		}
		logger.Info("Go v2 account failover controller started", zap.Duration("scheduler_interval", config.SchedulerInterval))
		workerScheduler.Start()
		<-runContext.Done()
		waitForJobs := workerScheduler.Stop()
		<-waitForJobs.Done()
		logger.Info("Go v2 account failover controller stopped")
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
	state, found, err := failover.ReadRuntimeState(ctx, reader)
	if err != nil {
		return err
	}
	if err := printJSON(state); err != nil {
		return err
	}
	if !failover.HealthyRuntimeState(state, found, time.Now(), config.MaxHealthAge) {
		return errors.New("account failover controller is not healthy")
	}
	return nil
}

func (config appConfig) validate() error {
	if strings.TrimSpace(config.Root) == "" {
		return errors.New("failover worker root is required")
	}
	if config.Once && config.Health {
		return errors.New("--once and --health cannot be used together")
	}
	if config.SchedulerInterval < time.Second || config.SchedulerInterval > time.Minute {
		return errors.New("scheduler interval must be between one second and one minute")
	}
	if config.MaxHealthAge < time.Second {
		return errors.New("maximum health age must be at least one second")
	}
	if config.ProbeTimeout < 100*time.Millisecond || config.ProbeTimeout > 10*time.Second {
		return errors.New("account probe timeout must be between 100 milliseconds and 10 seconds")
	}
	if config.ProbeConcurrency < 1 || config.ProbeConcurrency > 32 {
		return errors.New("account probe concurrency must be between 1 and 32")
	}
	if strings.Count(config.AccountAddressFormat, "%s") != 1 || strings.Count(config.AccountAddressFormat, "%") != 1 {
		return errors.New("account address format must contain exactly one %s placeholder")
	}
	probeAddress := fmt.Sprintf(config.AccountAddressFormat, "probe")
	if host, port, err := net.SplitHostPort(probeAddress); err != nil || strings.TrimSpace(host) == "" || port == "" {
		return errors.New("account address format must produce a valid host:port")
	} else if parsedPort, err := strconv.Atoi(port); err != nil || parsedPort < 1 || parsedPort > 65535 {
		return errors.New("account address format must contain a valid TCP port")
	}
	if config.SnapshotTimeout < time.Second || config.SnapshotTimeout > time.Minute {
		return errors.New("auth snapshot timeout must be between one second and one minute")
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
