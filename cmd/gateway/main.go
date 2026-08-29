package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/gateway"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const envPrefix = "CLIPROXY_GATEWAY"

type appConfig struct {
	PublicAddress   string
	InternalAddress string
	SnapshotDir     string
	RefreshInterval time.Duration
	AccessLogPath   string
	LogLevel        string
	ShutdownTimeout time.Duration
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
		Use:           "cpa-gateway",
		Short:         "Run the Go CPA data-plane gateway",
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
				return fmt.Errorf("read gateway config: %w", err)
			}
			return nil
		},
		RunE: func(_ *cobra.Command, _ []string) error {
			return run(appConfig{
				PublicAddress:   settings.GetString("public-address"),
				InternalAddress: settings.GetString("internal-address"),
				SnapshotDir:     settings.GetString("snapshot-dir"),
				RefreshInterval: settings.GetDuration("refresh-interval"),
				AccessLogPath:   settings.GetString("access-log"),
				LogLevel:        settings.GetString("log-level"),
				ShutdownTimeout: settings.GetDuration("shutdown-timeout"),
			})
		},
	}

	flags := command.Flags()
	flags.String("config", "", "optional YAML, JSON, or TOML configuration file")
	flags.String("public-address", ":8317", "public API listen address")
	flags.String("internal-address", ":8319", "internal probe listen address")
	flags.String("snapshot-dir", "/var/run/cliproxy-snapshots", "gateway snapshot directory")
	flags.Duration("refresh-interval", gateway.DefaultSnapshotRefreshInterval, "snapshot polling fallback interval")
	flags.String("access-log", "/var/log/cliproxy/access.tsv", "privacy-bounded TSV access log path; empty disables it")
	flags.String("log-level", "info", "Zap log level")
	flags.Duration("shutdown-timeout", 30*time.Second, "maximum graceful shutdown wait")
	if err := settings.BindPFlags(flags); err != nil {
		panic(err)
	}
	return command
}

func run(config appConfig) error {
	if config.PublicAddress == "" || config.InternalAddress == "" {
		return errors.New("public and internal listen addresses are required")
	}
	if config.PublicAddress == config.InternalAddress {
		return errors.New("public and internal listen addresses must differ")
	}
	if config.RefreshInterval <= 0 {
		return errors.New("refresh interval must be positive")
	}
	if config.ShutdownTimeout <= 0 {
		return errors.New("shutdown timeout must be positive")
	}

	logger, err := newLogger(config.LogLevel)
	if err != nil {
		return err
	}
	defer func() { _ = logger.Sync() }()

	accessLog, err := openAccessLog(config.AccessLogPath)
	if err != nil {
		return err
	}
	if accessLog != nil {
		defer accessLog.Close()
	}

	engine := gateway.NewEngine()
	loader := gateway.NewSnapshotLoader(
		engine,
		gateway.SnapshotPathsForDirectory(config.SnapshotDir),
		config.RefreshInterval,
		logger,
	)
	loader.Refresh()
	httpGateway, err := gateway.NewHTTPGateway(gateway.HTTPGatewayConfig{
		Engine:    engine,
		Logger:    logger,
		AccessLog: accessLog,
	})
	if err != nil {
		return err
	}

	publicServer := newHTTPServer(config.PublicAddress, httpGateway.PublicHandler())
	internalServer := newHTTPServer(config.InternalAddress, httpGateway.InternalHandler())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errorChannel := make(chan error, 3)
	go func() {
		errorChannel <- loader.Run(ctx)
	}()
	go serve("public", publicServer, logger, errorChannel)
	go serve("internal", internalServer, logger, errorChannel)

	logger.Info(
		"Go gateway started",
		zap.String("public_address", config.PublicAddress),
		zap.String("internal_address", config.InternalAddress),
		zap.String("snapshot_dir", filepath.Clean(config.SnapshotDir)),
	)

	var runError error
	select {
	case <-ctx.Done():
	case runError = <-errorChannel:
		stop()
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
	defer cancel()
	shutdownError := errors.Join(
		publicServer.Shutdown(shutdownContext),
		internalServer.Shutdown(shutdownContext),
	)
	if shutdownError != nil {
		_ = publicServer.Close()
		_ = internalServer.Close()
	}
	logger.Info("Go gateway stopped")
	return errors.Join(runError, shutdownError)
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

func openAccessLog(path string) (*os.File, error) {
	if path == "" {
		return nil, nil
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return nil, fmt.Errorf("create access log directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open access log: %w", err)
	}
	return file, nil
}

func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// Streaming Codex responses can legitimately run for a long time. Do
		// not impose server-wide ReadTimeout or WriteTimeout deadlines.
		ReadTimeout:  0,
		WriteTimeout: 0,
	}
}

func serve(
	name string,
	server *http.Server,
	logger *zap.Logger,
	errorChannel chan<- error,
) {
	logger.Info("gateway listener ready", zap.String("listener", name), zap.String("address", server.Addr))
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	if err != nil {
		err = fmt.Errorf("%s gateway listener: %w", name, err)
	}
	errorChannel <- err
}
