package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/dockerreadproxy"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

const envPrefix = "CLIPROXY_DOCKER_READ_PROXY"

type appConfig struct {
	ListenSocket   string
	UpstreamSocket string
	ComposeProject string
	Shutdown       time.Duration
	Health         bool
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
		Use:           "cpa-docker-read-proxy",
		Short:         "Expose a project-scoped read-only Docker API socket",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			config := appConfig{
				ListenSocket: settings.GetString("listen-unix"), UpstreamSocket: settings.GetString("upstream-unix"),
				ComposeProject: settings.GetString("compose-project"), Shutdown: settings.GetDuration("shutdown-timeout"),
				Health: settings.GetBool("health"),
			}
			if config.Health {
				return health(config.ListenSocket)
			}
			return run(config)
		},
	}
	flags := command.Flags()
	flags.String("listen-unix", "/var/run/cpa-docker-read/docker.sock", "private downstream Unix socket")
	flags.String("upstream-unix", "/var/run/docker-host.sock", "host Docker Unix socket")
	flags.String("compose-project", "", "exact readable Docker Compose project")
	flags.Duration("shutdown-timeout", 10*time.Second, "maximum graceful shutdown wait")
	flags.Bool("health", false, "probe the private read-only socket and exit")
	if err := settings.BindPFlags(flags); err != nil {
		panic(err)
	}
	return command
}

func run(config appConfig) error {
	if config.Shutdown <= 0 {
		return errors.New("shutdown timeout must be positive")
	}
	listenSocket := strings.TrimSpace(config.ListenSocket)
	if listenSocket == "" || !filepath.IsAbs(listenSocket) || strings.ContainsAny(listenSocket, "\r\n\x00") {
		return errors.New("listen socket must be an absolute path")
	}
	if err := os.MkdirAll(filepath.Dir(listenSocket), 0o700); err != nil {
		return fmt.Errorf("create Docker read socket directory: %w", err)
	}
	if info, err := os.Lstat(listenSocket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("refusing to replace a non-socket Docker read endpoint")
		}
		if err := os.Remove(listenSocket); err != nil {
			return fmt.Errorf("remove stale Docker read socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect Docker read socket: %w", err)
	}
	listener, err := net.Listen("unix", listenSocket)
	if err != nil {
		return fmt.Errorf("listen on Docker read socket: %w", err)
	}
	defer listener.Close()
	defer os.Remove(listenSocket)
	if err := os.Chmod(listenSocket, 0o600); err != nil {
		return fmt.Errorf("restrict Docker read socket: %w", err)
	}
	logger, err := zap.NewProduction()
	if err != nil {
		return err
	}
	defer logger.Sync()
	proxy, err := dockerreadproxy.New(dockerreadproxy.Config{
		Project: config.ComposeProject, UpstreamSocket: config.UpstreamSocket, Logger: logger,
	})
	if err != nil {
		return err
	}
	defer proxy.CloseIdleConnections()
	server := &http.Server{
		Handler: proxy.Handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errorsChannel := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errorsChannel <- err
	}()
	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), config.Shutdown)
		defer cancel()
		return server.Shutdown(shutdownContext)
	case err := <-errorsChannel:
		return err
	}
}

func health(socket string) error {
	socket = strings.TrimSpace(socket)
	if socket == "" || !filepath.IsAbs(socket) {
		return errors.New("health check requires an absolute listen socket")
	}
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		}},
	}
	response, err := client.Get("http://docker/_ping")
	if err != nil {
		return errors.New("Docker read proxy health check failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Docker read proxy health status is %d", response.StatusCode)
	}
	return nil
}
