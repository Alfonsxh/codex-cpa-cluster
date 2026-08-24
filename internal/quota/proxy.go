package quota

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
)

const (
	defaultProxySecret       = "cpa_default_proxy_url"
	accountProxySecretPrefix = "cpa_account_proxy_url:"
)

type ProxySecretStore interface {
	ReadSecret(context.Context, string) (string, bool, error)
}

type ProxyResolver struct {
	Store    ProxySecretStore
	Settings map[string]any
}

func (resolver ProxyResolver) Resolve(ctx context.Context, account controlplane.Account) (string, error) {
	if resolver.Store == nil {
		return "", errors.New("proxy resolver requires a secret store")
	}
	mode := strings.TrimSpace(account.ProxyMode)
	if mode == "" {
		mode = "inherit"
	}
	var secretName string
	switch mode {
	case "direct":
		return "", nil
	case "custom":
		secretName = accountProxySecretPrefix + account.ID
	case "inherit":
		if !boolSetting(resolver.Settings["cpa.proxy_enabled"], false) {
			return "", nil
		}
		secretName = defaultProxySecret
	default:
		return "", fmt.Errorf("account %s has an invalid proxy mode", account.ID)
	}
	value, found, err := resolver.Store.ReadSecret(ctx, secretName)
	if err != nil {
		return "", fmt.Errorf("read effective proxy for account %s", account.ID)
	}
	if !found || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("effective proxy is unavailable for account %s", account.ID)
	}
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("effective proxy is invalid for account %s", account.ID)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5":
		return parsed.String(), nil
	default:
		return "", fmt.Errorf("effective proxy is invalid for account %s", account.ID)
	}
}

func boolSetting(value any, fallback bool) bool {
	if typed, ok := value.(bool); ok {
		return typed
	}
	return fallback
}
