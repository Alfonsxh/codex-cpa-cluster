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

	"github.com/Alfonsxh/codex-cpa-cluster/internal/edge"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const envPrefix = "CLIPROXY_EDGE"

type appConfig struct {
	PublicAddress       string
	InternalAddress     string
	ActiveGatewayFile   string
	RefreshInterval     time.Duration
	WebTarget           string
	BluePublicTarget    string
	BlueInternalTarget  string
	GreenPublicTarget   string
	GreenInternalTarget string
	MaxBodyBytes        int64
	LogLevel            string
	ShutdownTimeout     time.Duration
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
		Use:           "cpa-edge",
		Short:         "Run the Go v2 stable CPA Edge",
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRunE: func(command *cobra.Command, _ []string) error {
			configFile, err := command.Flags().GetString("config")
			if err != nil || configFile == "" {
				return err
			}
			settings.SetConfigFile(configFile)
			if err := settings.ReadInConfig(); err != nil {
				return fmt.Errorf("read Edge config: %w", err)
			}
			return nil
		},
		RunE: func(_ *cobra.Command, _ []string) error {
			return run(appConfig{
				PublicAddress: settings.GetString("public-address"), InternalAddress: settings.GetString("internal-address"),
				ActiveGatewayFile: settings.GetString("active-gateway-file"), RefreshInterval: settings.GetDuration("refresh-interval"),
				WebTarget:        settings.GetString("web-target"),
				BluePublicTarget: settings.GetString("blue-public-target"), BlueInternalTarget: settings.GetString("blue-internal-target"),
				GreenPublicTarget: settings.GetString("green-public-target"), GreenInternalTarget: settings.GetString("green-internal-target"),
				MaxBodyBytes: settings.GetInt64("max-body-bytes"), LogLevel: settings.GetString("log-level"),
				ShutdownTimeout: settings.GetDuration("shutdown-timeout"),
			})
		},
	}
	flags := command.Flags()
	flags.String("config", "", "optional YAML, JSON, or TOML configuration file")
	flags.String("public-address", ":8317", "stable public listener address")
	flags.String("internal-address", ":8319", "host-loopback internal listener address")
	flags.String("active-gateway-file", "/var/run/cliproxy-edge/active-gateway.conf", "existing validated active Gateway selection")
	flags.Duration("refresh-interval", 500*time.Millisecond, "active Gateway polling fallback interval")
	flags.String("web-target", "http://web:8080", "Web origin")
	flags.String("blue-public-target", "http://gateway-blue:8317", "blue Gateway public origin")
	flags.String("blue-internal-target", "http://gateway-blue:8319", "blue Gateway internal origin")
	flags.String("green-public-target", "http://gateway-green:8317", "green Gateway public origin")
	flags.String("green-internal-target", "http://gateway-green:8319", "green Gateway internal origin")
	flags.Int64("max-body-bytes", edge.DefaultMaxBodyBytes, "maximum public or internal request body")
	flags.String("log-level", "info", "Zap log level")
	flags.Duration("shutdown-timeout", 30*time.Second, "maximum graceful shutdown wait")
	if err := settings.BindPFlags(flags); err != nil {
		panic(err)
	}
	return command
}

func run(config appConfig) error {
	if strings.TrimSpace(config.PublicAddress) == "" || strings.TrimSpace(config.InternalAddress) == "" {
		return errors.New("public and internal Edge listen addresses are required")
	}
	if config.PublicAddress == config.InternalAddress {
		return errors.New("public and internal Edge listen addresses must differ")
	}
	if config.RefreshInterval <= 0 || config.MaxBodyBytes <= 0 || config.ShutdownTimeout <= 0 {
		return errors.New("Edge intervals, body limit, and shutdown timeout must be positive")
	}
	logger, err := newLogger(config.LogLevel)
	if err != nil {
		return err
	}
	defer func() { _ = logger.Sync() }()
	selector, err := edge.NewSelector(config.ActiveGatewayFile, config.RefreshInterval, logger)
	if err != nil {
		return err
	}
	edgeServer, err := edge.NewServer(edge.ServerConfig{
		Selector: selector, Logger: logger, MaxBodyBytes: config.MaxBodyBytes,
		WebTarget:        config.WebTarget,
		BluePublicTarget: config.BluePublicTarget, BlueInternalTarget: config.BlueInternalTarget,
		GreenPublicTarget: config.GreenPublicTarget, GreenInternalTarget: config.GreenInternalTarget,
	})
	if err != nil {
		return err
	}
	publicServer := newHTTPServer(config.PublicAddress, edgeServer.PublicHandler())
	internalServer := newHTTPServer(config.InternalAddress, edgeServer.InternalHandler())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errorChannel := make(chan error, 3)
	go func() { errorChannel <- selector.Run(ctx) }()
	go serve("public", publicServer, logger, errorChannel)
	go serve("internal", internalServer, logger, errorChannel)
	logger.Info("Go v2 Edge started", zap.String("slot", string(selector.Slot())))
	var runError error
	select {
	case <-ctx.Done():
	case runError = <-errorChannel:
		stop()
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
	defer cancel()
	shutdownError := errors.Join(publicServer.Shutdown(shutdownContext), internalServer.Shutdown(shutdownContext))
	if shutdownError != nil {
		_ = publicServer.Close()
		_ = internalServer.Close()
	}
	return errors.Join(runError, shutdownError)
}

func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: address, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second,
		ReadTimeout: 0, WriteTimeout: 0,
	}
}

func serve(name string, server *http.Server, logger *zap.Logger, errorsChannel chan<- error) {
	logger.Info("Go v2 Edge listener ready", zap.String("listener", name), zap.String("address", server.Addr))
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	if err != nil {
		err = fmt.Errorf("%s Edge listener: %w", name, err)
	}
	errorsChannel <- err
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
