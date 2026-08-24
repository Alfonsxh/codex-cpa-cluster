package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	webapi "github.com/Alfonsxh/codex-cpa-cluster/internal/web"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const envPrefix = "CLIPROXY_WEB"

type appConfig struct {
	Address         string
	PortalRoot      string
	AdminRoot       string
	UsageRoot       string
	AdminTarget     string
	MaxBodyBytes    int64
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
		Use:           "cpa-web",
		Short:         "Run the Go v2 React static Web and Admin proxy",
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRunE: func(command *cobra.Command, _ []string) error {
			configFile, err := command.Flags().GetString("config")
			if err != nil || configFile == "" {
				return err
			}
			settings.SetConfigFile(configFile)
			if err := settings.ReadInConfig(); err != nil {
				return fmt.Errorf("read Web config: %w", err)
			}
			return nil
		},
		RunE: func(_ *cobra.Command, _ []string) error {
			return run(appConfig{
				Address: settings.GetString("address"), PortalRoot: settings.GetString("portal-root"),
				AdminRoot: settings.GetString("admin-root"), UsageRoot: settings.GetString("usage-root"),
				AdminTarget: settings.GetString("admin-target"), MaxBodyBytes: settings.GetInt64("max-body-bytes"),
				LogLevel: settings.GetString("log-level"), ShutdownTimeout: settings.GetDuration("shutdown-timeout"),
			})
		},
	}
	flags := command.Flags()
	flags.String("config", "", "optional YAML, JSON, or TOML configuration file")
	flags.String("address", ":8080", "Web listener address")
	flags.String("portal-root", "/srv/cpa-web/portal", "existing React Portal asset directory")
	flags.String("admin-root", "/srv/cpa-web/admin", "existing React Admin asset directory")
	flags.String("usage-root", "/srv/cpa-web/usage", "existing React Usage asset directory")
	flags.String("admin-target", "http://admin:8318", "Go Admin origin")
	flags.Int64("max-body-bytes", webapi.DefaultMaxBodyBytes, "maximum proxied request body")
	flags.String("log-level", "info", "Zap log level")
	flags.Duration("shutdown-timeout", 30*time.Second, "maximum graceful shutdown wait")
	if err := settings.BindPFlags(flags); err != nil {
		panic(err)
	}
	return command
}

func run(config appConfig) error {
	if strings.TrimSpace(config.Address) == "" {
		return errors.New("Web listen address is required")
	}
	if config.MaxBodyBytes <= 0 || config.ShutdownTimeout <= 0 {
		return errors.New("Web body limit and shutdown timeout must be positive")
	}
	logger, err := newLogger(config.LogLevel)
	if err != nil {
		return err
	}
	defer func() { _ = logger.Sync() }()
	webServer, err := webapi.NewServer(webapi.Config{
		PortalRoot: config.PortalRoot, AdminRoot: config.AdminRoot, UsageRoot: config.UsageRoot,
		AdminTarget: config.AdminTarget, MaxBodyBytes: config.MaxBodyBytes, Logger: logger,
	})
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr: config.Address, Handler: webServer.Handler(),
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second,
		ReadTimeout: 0, WriteTimeout: 0,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errorChannel := make(chan error, 1)
	go func() {
		logger.Info("Go v2 Web listener ready", zap.String("address", config.Address))
		err := httpServer.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errorChannel <- err
	}()
	var runError error
	select {
	case <-ctx.Done():
	case runError = <-errorChannel:
		stop()
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
	defer cancel()
	shutdownError := httpServer.Shutdown(shutdownContext)
	if shutdownError != nil {
		_ = httpServer.Close()
	}
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
