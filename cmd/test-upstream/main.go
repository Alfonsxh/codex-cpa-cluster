package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const envPrefix = "CLIPROXY_TEST_UPSTREAM"

type appConfig struct {
	Address         string
	InternalKey     string
	InternalKeyFile string
}

type fixtureStats struct {
	active    atomic.Int64
	started   atomic.Int64
	completed atomic.Int64
	canceled  atomic.Int64
	maxActive atomic.Int64
}

func (stats *fixtureStats) begin() {
	active := stats.active.Add(1)
	stats.started.Add(1)
	for {
		maximum := stats.maxActive.Load()
		if active <= maximum || stats.maxActive.CompareAndSwap(maximum, active) {
			return
		}
	}
}

func (stats *fixtureStats) finish(canceled bool) {
	stats.active.Add(-1)
	if canceled {
		stats.canceled.Add(1)
		return
	}
	stats.completed.Add(1)
}

func (stats *fixtureStats) payload() gin.H {
	return gin.H{
		"active":     stats.active.Load(),
		"started":    stats.started.Load(),
		"completed":  stats.completed.Load(),
		"canceled":   stats.canceled.Load(),
		"max_active": stats.maxActive.Load(),
	}
}

func (stats *fixtureStats) reset() bool {
	if stats.active.Load() != 0 {
		return false
	}
	stats.started.Store(0)
	stats.completed.Store(0)
	stats.canceled.Store(0)
	stats.maxActive.Store(0)
	return true
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
	settings.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	settings.AutomaticEnv()
	command := &cobra.Command{
		Use:           "cpa-test-upstream",
		Short:         "Run an isolated non-production CPA contract fixture",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return run(appConfig{
				Address:         settings.GetString("address"),
				InternalKey:     settings.GetString("internal-key"),
				InternalKeyFile: settings.GetString("internal-key-file"),
			})
		},
	}
	command.Flags().String("address", ":8317", "fixture listen address")
	command.Flags().String("internal-key", "", "dedicated fixture-only internal key")
	command.Flags().String(
		"internal-key-file",
		"",
		"permission-restricted file containing the dedicated fixture-only internal key",
	)
	if err := settings.BindPFlags(command.Flags()); err != nil {
		panic(err)
	}
	return command
}

func run(config appConfig) error {
	if strings.TrimSpace(config.Address) == "" {
		return errors.New("test upstream address is required")
	}
	internalKey, err := resolveInternalKey(config)
	if err != nil {
		return err
	}
	router, err := newRouter(internalKey)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              config.Address,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	return server.ListenAndServe()
}

func resolveInternalKey(config appConfig) (string, error) {
	direct := strings.TrimSpace(config.InternalKey)
	keyFile := strings.TrimSpace(config.InternalKeyFile)
	if direct != "" && keyFile != "" {
		return "", errors.New("test upstream accepts exactly one internal key source")
	}
	if direct != "" {
		if len(direct) > 4096 || strings.ContainsAny(direct, "\r\n") {
			return "", errors.New("test upstream internal key is invalid")
		}
		return direct, nil
	}
	if keyFile == "" {
		return "", errors.New("test upstream internal key is required")
	}
	metadata, err := os.Stat(keyFile)
	if err != nil {
		return "", fmt.Errorf("stat test upstream internal key file: %w", err)
	}
	if !metadata.Mode().IsRegular() || metadata.Mode().Perm()&0o077 != 0 {
		return "", errors.New("test upstream internal key file must be a permission-restricted regular file")
	}
	if metadata.Size() <= 0 || metadata.Size() > 4096 {
		return "", errors.New("test upstream internal key file size is invalid")
	}
	payload, err := os.ReadFile(keyFile)
	if err != nil {
		return "", fmt.Errorf("read test upstream internal key file: %w", err)
	}
	key := strings.TrimSpace(string(payload))
	if key == "" || len(key) > 4096 || strings.ContainsAny(key, "\r\n") {
		return "", errors.New("test upstream internal key file is invalid")
	}
	return key, nil
}

func newRouter(internalKey string) (*gin.Engine, error) {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false
	if err := router.SetTrustedProxies(nil); err != nil {
		return nil, fmt.Errorf("disable trusted proxies: %w", err)
	}
	router.Use(func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer "+internalKey {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"type": "authentication_error", "code": "invalid_api_key"},
			})
			return
		}
		c.Next()
	})
	stats := &fixtureStats{}
	router.GET("/v1/fixture/stats", func(c *gin.Context) {
		c.JSON(http.StatusOK, stats.payload())
	})
	router.POST("/v1/fixture/reset", func(c *gin.Context) {
		if !stats.reset() {
			c.JSON(http.StatusConflict, gin.H{
				"error": gin.H{"type": "conflict_error", "code": "fixture_requests_active"},
			})
			return
		}
		c.JSON(http.StatusOK, stats.payload())
	})
	router.GET("/v1/models", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"object": "list",
			"data":   []gin.H{{"id": "fixture-model", "object": "model"}},
		})
	})
	router.POST("/v1/responses", func(c *gin.Context) {
		stats.begin()
		canceled := false
		defer func() { stats.finish(canceled) }()
		if c.GetHeader("X-Codex-CPA-Fixture-Drain-Body") == "1" {
			if _, err := io.Copy(io.Discard, c.Request.Body); err != nil {
				canceled = true
				return
			}
			c.JSON(http.StatusOK, gin.H{"drained": true})
			return
		}
		var request struct {
			Stream         bool `json:"stream"`
			FixtureDelayMS int  `json:"fixture_delay_ms"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1024*1024))
		if err := decoder.Decode(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{"type": "invalid_request_error", "code": "invalid_json"},
			})
			return
		}
		if request.FixtureDelayMS < 0 || request.FixtureDelayMS > 10_000 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{"type": "invalid_request_error", "code": "invalid_fixture_delay"},
			})
			return
		}
		if !request.Stream {
			if request.FixtureDelayMS > 0 {
				select {
				case <-c.Request.Context().Done():
					canceled = true
					return
				case <-time.After(time.Duration(request.FixtureDelayMS) * time.Millisecond):
				}
			}
			c.JSON(http.StatusOK, gin.H{
				"id": "resp_fixture", "object": "response", "status": "completed",
				"output": []gin.H{{
					"type": "message",
					"role": "assistant",
					"content": []gin.H{{
						"type": "output_text",
						"text": "OK",
					}},
				}},
			})
			return
		}
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Status(http.StatusOK)
		writer := bufio.NewWriter(c.Writer)
		for _, event := range []string{
			"event: response.created\ndata: {\"type\":\"response.created\"}\n\n",
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n",
			"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n",
			"data: [DONE]\n\n",
		} {
			if _, err := writer.WriteString(event); err != nil {
				canceled = true
				return
			}
			if err := writer.Flush(); err != nil {
				canceled = true
				return
			}
			if flusher, ok := c.Writer.(http.Flusher); ok {
				flusher.Flush()
			}
			if request.FixtureDelayMS > 0 {
				select {
				case <-c.Request.Context().Done():
					canceled = true
					return
				case <-time.After(time.Duration(request.FixtureDelayMS) * time.Millisecond):
				}
			}
		}
	})
	router.NoRoute(func(c *gin.Context) { c.Status(http.StatusNotFound) })
	return router, nil
}
