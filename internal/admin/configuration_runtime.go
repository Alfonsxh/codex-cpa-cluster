package admin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/google/renameio/v2"
)

// ConfigurationAccountProjector atomically refreshes the generated account
// configuration and routing projections after the authoritative settings have
// changed.
type ConfigurationAccountProjector interface {
	RefreshAccounts(context.Context) error
}

// ConfigurationRuntime restarts only an explicitly selected service in the
// Compose project owned by this Admin process. Implementations must enforce
// exact project labels and reject unknown services.
type ConfigurationRuntime interface {
	RestartConfigurationTarget(context.Context, string) error
}

// ConfigurationDeploymentProjector refreshes the private Compose environment
// projection without applying a release or changing the active Edge slot.
type ConfigurationDeploymentProjector interface {
	ProjectConfiguration(context.Context, map[string]any) error
}

type ConfigurationRuntimeApplier struct {
	Accounts interface {
		ReadAccounts(context.Context) ([]controlplane.Account, error)
	}
	Projection ConfigurationAccountProjector
	Runtime    ConfigurationRuntime
	Deployment ConfigurationDeploymentProjector
}

func (applier *ConfigurationRuntimeApplier) ApplyConfiguration(
	ctx context.Context,
	change ConfigurationChange,
) error {
	if applier == nil {
		return errors.New("configuration runtime applier is unavailable")
	}
	modes := make(map[string]struct{}, len(change.Modes))
	for _, mode := range change.Modes {
		modes[mode] = struct{}{}
	}
	_, deploymentChanged := modes["deployment"]
	_, accountsChanged := modes["accounts"]
	_, collectorChanged := modes["collector"]
	_, quotaChanged := modes["quota"]

	// Validate every dependency before the first file or runtime mutation so a
	// partially configured candidate Admin always fails closed.
	if deploymentChanged && applier.Deployment == nil {
		return errors.New("deployment configuration projector is unavailable")
	}
	if accountsChanged && (applier.Accounts == nil || applier.Projection == nil || applier.Runtime == nil) {
		return errors.New("account configuration runtime is unavailable")
	}
	if (collectorChanged || quotaChanged) && applier.Runtime == nil {
		return errors.New("collector configuration runtime is unavailable")
	}

	if deploymentChanged {
		if err := applier.Deployment.ProjectConfiguration(ctx, change.After); err != nil {
			return fmt.Errorf("project deployment configuration: %w", err)
		}
	}
	if accountsChanged {
		accounts, err := applier.Accounts.ReadAccounts(ctx)
		if err != nil {
			return fmt.Errorf("read accounts before configuration refresh: %w", err)
		}
		if err := applier.Projection.RefreshAccounts(ctx); err != nil {
			return fmt.Errorf("refresh account configuration projection: %w", err)
		}
		sort.Slice(accounts, func(left, right int) bool { return accounts[left].ID < accounts[right].ID })
		for _, account := range accounts {
			if !account.GroupEnabled {
				continue
			}
			if err := applier.Runtime.RestartConfigurationTarget(ctx, account.ID); err != nil {
				return fmt.Errorf("restart configured account %s: %w", account.ID, err)
			}
		}
	}
	if collectorChanged || quotaChanged {
		if err := applier.Runtime.RestartConfigurationTarget(ctx, "usage-collector"); err != nil {
			return fmt.Errorf("restart usage collector: %w", err)
		}
	}
	return nil
}

// ComposeEnvironmentProjector preserves the already-applied component and CPA
// image identities while regenerating only the deployment settings owned by
// the Configuration Center. The target must already be initialized.
type ComposeEnvironmentProjector struct {
	Root string
}

func (projector *ComposeEnvironmentProjector) ProjectConfiguration(
	ctx context.Context,
	values map[string]any,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root := strings.TrimSpace(projector.Root)
	if root == "" {
		return errors.New("Compose environment root is required")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve Compose environment root: %w", err)
	}
	path := filepath.Join(filepath.Clean(absoluteRoot), "state", "compose.env")
	information, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect existing Compose environment: %w", err)
	}
	if !information.Mode().IsRegular() || information.Mode()&os.ModeSymlink != 0 {
		return errors.New("existing Compose environment must be a regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read existing Compose environment: %w", err)
	}
	existing, err := parseComposeEnvironment(raw)
	if err != nil {
		return err
	}

	projected := map[string]string{
		"CLIPROXY_IMAGE":              firstNonEmpty(existing["CLIPROXY_IMAGE"], configurationText(values, "runtime.cliproxy_image")),
		"ADMIN_IMAGE":                 firstNonEmpty(existing["ADMIN_IMAGE"], "codex-cpa-admin:local"),
		"WEB_RUNTIME_IMAGE":           firstNonEmpty(existing["WEB_RUNTIME_IMAGE"], "codex-cpa-web:local"),
		"GATEWAY_RUNTIME_IMAGE":       firstNonEmpty(existing["GATEWAY_RUNTIME_IMAGE"], "codex-cpa-gateway:local"),
		"EDGE_RUNTIME_IMAGE":          firstNonEmpty(existing["EDGE_RUNTIME_IMAGE"], "codex-cpa-edge:local"),
		"ADMIN_BASE_IMAGE":            configurationText(values, "runtime.admin_base_image"),
		"GATEWAY_IMAGE":               configurationText(values, "runtime.gateway_image"),
		"EDGE_IMAGE":                  configurationText(values, "runtime.gateway_image"),
		"GATEWAY_LISTEN_ADDRESS":      configurationText(values, "gateway.listen_address"),
		"GATEWAY_PORT":                configurationText(values, "gateway.port"),
		"GATEWAY_INTERNAL_PORT":       configurationText(values, "gateway.internal_port"),
		"MANAGEMENT_LISTEN_ADDRESS":   configurationText(values, "management.listen_address"),
		"MANAGEMENT_PORT":             configurationText(values, "management.port"),
		"BUSINESS_CPA_LISTEN_ADDRESS": configurationText(values, "accounts.listen_address"),
	}
	order := []string{
		"CLIPROXY_IMAGE", "ADMIN_IMAGE", "WEB_RUNTIME_IMAGE", "GATEWAY_RUNTIME_IMAGE", "EDGE_RUNTIME_IMAGE",
		"ADMIN_BASE_IMAGE", "GATEWAY_IMAGE", "EDGE_IMAGE", "GATEWAY_LISTEN_ADDRESS", "GATEWAY_PORT",
		"GATEWAY_INTERNAL_PORT", "MANAGEMENT_LISTEN_ADDRESS", "MANAGEMENT_PORT", "BUSINESS_CPA_LISTEN_ADDRESS",
	}
	var content strings.Builder
	content.WriteString("# Generated from state/control-plane.sqlite3; do not edit.\n")
	for _, key := range order {
		value := strings.TrimSpace(projected[key])
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("Compose environment value %s is empty or invalid", key)
		}
		content.WriteString(key)
		content.WriteByte('=')
		content.WriteString(value)
		content.WriteByte('\n')
	}
	if err := renameio.WriteFile(path, []byte(content.String()), 0o600); err != nil {
		return fmt.Errorf("replace Compose environment: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure Compose environment: %w", err)
	}
	return nil
}

func parseComposeEnvironment(raw []byte) (map[string]string, error) {
	result := make(map[string]string)
	for lineNumber, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" || strings.ContainsAny(key, " \t\r\n\x00") {
			return nil, fmt.Errorf("invalid Compose environment line %d", lineNumber+1)
		}
		if _, duplicate := result[key]; duplicate {
			return nil, fmt.Errorf("duplicate Compose environment key %s", key)
		}
		result[key] = strings.TrimSpace(value)
	}
	return result, nil
}

func configurationText(values map[string]any, key string) string {
	value, found := values[key]
	if !found || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
