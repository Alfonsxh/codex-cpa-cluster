package runtimeops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/go-resty/resty/v2"
)

const defaultDrainPollInterval = 250 * time.Millisecond

type GatewayDrainerConfig struct {
	ProbeURLs    []string
	WaitTimeout  time.Duration
	PollInterval time.Duration
	HTTPClient   *resty.Client
}

type GatewayDrainer struct {
	probeURLs    []string
	waitTimeout  time.Duration
	pollInterval time.Duration
	http         *resty.Client
}

type gatewayInflightStat struct {
	Label    string `json:"label"`
	Account  string `json:"account"`
	Inflight int64  `json:"inflight"`
}

func NewGatewayDrainer(config GatewayDrainerConfig) (*GatewayDrainer, error) {
	if config.WaitTimeout <= 0 {
		return nil, errors.New("account drain timeout must be positive")
	}
	if config.PollInterval <= 0 {
		config.PollInterval = defaultDrainPollInterval
	}
	probeURLs := make([]string, 0, len(config.ProbeURLs))
	seen := make(map[string]struct{})
	for _, raw := range config.ProbeURLs {
		base := strings.TrimRight(strings.TrimSpace(raw), "/")
		if base == "" {
			continue
		}
		url := base + "/__stats"
		if _, duplicate := seen[url]; duplicate {
			continue
		}
		seen[url] = struct{}{}
		probeURLs = append(probeURLs, url)
	}
	if len(probeURLs) == 0 {
		return nil, errors.New("at least one Gateway drain probe URL is required")
	}
	if config.HTTPClient == nil {
		config.HTTPClient = resty.New()
	}
	return &GatewayDrainer{
		probeURLs: probeURLs, waitTimeout: config.WaitTimeout,
		pollInterval: config.PollInterval, http: config.HTTPClient,
	}, nil
}

func (drainer *GatewayDrainer) WaitAccountDrained(ctx context.Context, rawAccountID string) error {
	accountID, err := controlplane.NormalizeAccountID(rawAccountID)
	if err != nil || accountID != rawAccountID {
		return fmt.Errorf("invalid drain account %q", rawAccountID)
	}
	waitContext, cancel := context.WithTimeout(ctx, drainer.waitTimeout)
	defer cancel()
	ticker := time.NewTicker(drainer.pollInterval)
	defer ticker.Stop()
	lastInflight := int64(-1)
	var lastError error
	for {
		inflight, readError := drainer.accountInflight(waitContext, accountID)
		if readError == nil && inflight == 0 {
			return nil
		}
		lastInflight, lastError = inflight, readError
		select {
		case <-waitContext.Done():
			return fmt.Errorf(
				"wait for account %s to drain (last inflight %d): %w",
				accountID, lastInflight, errors.Join(lastError, waitContext.Err()),
			)
		case <-ticker.C:
		}
	}
}

// InflightKeyCounts returns the number of distinct API-key identities with at
// least one request in flight per account. It never includes labels in the
// returned payload and is therefore safe for the public aggregate endpoint.
func (drainer *GatewayDrainer) InflightKeyCounts(ctx context.Context) (map[string]int64, error) {
	identities := make(map[string]map[string]struct{})
	for _, url := range drainer.probeURLs {
		stats, err := drainer.readInflight(ctx, url)
		if err != nil {
			return nil, err
		}
		for _, stat := range stats {
			if stat.Inflight == 0 {
				continue
			}
			if identities[stat.Account] == nil {
				identities[stat.Account] = make(map[string]struct{})
			}
			identities[stat.Account][stat.Label] = struct{}{}
		}
	}
	result := make(map[string]int64, len(identities))
	for account, labels := range identities {
		result[account] = int64(len(labels))
	}
	return result, nil
}

func (drainer *GatewayDrainer) accountInflight(ctx context.Context, accountID string) (int64, error) {
	total := int64(0)
	for _, url := range drainer.probeURLs {
		stats, err := drainer.readInflight(ctx, url)
		if err != nil {
			return -1, err
		}
		for _, stat := range stats {
			if stat.Account == accountID {
				total += stat.Inflight
			}
		}
	}
	return total, nil
}

func (drainer *GatewayDrainer) readInflight(ctx context.Context, url string) ([]gatewayInflightStat, error) {
	stats := make([]gatewayInflightStat, 0)
	response, err := drainer.http.R().SetContext(ctx).Get(url)
	if err != nil {
		return nil, fmt.Errorf("read Gateway drain state from %s: %w", url, err)
	}
	if response.StatusCode() != 200 {
		return nil, fmt.Errorf("read Gateway drain state from %s: status %d", url, response.StatusCode())
	}
	body := response.Body()
	if len(body) == 0 || len(body) > 1024*1024 {
		return nil, fmt.Errorf("read Gateway drain state from %s: invalid response size", url)
	}
	if err := json.Unmarshal(body, &stats); err != nil {
		return nil, fmt.Errorf("decode Gateway drain state from %s: %w", url, err)
	}
	for _, stat := range stats {
		if stat.Inflight < 0 {
			return nil, fmt.Errorf("Gateway %s returned a negative in-flight count", url)
		}
	}
	return stats, nil
}
