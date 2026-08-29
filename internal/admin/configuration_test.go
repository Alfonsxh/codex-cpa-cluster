package admin

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
)

func TestConfigurationDefinitionsMatchCompleteGoContract(t *testing.T) {
	if len(configurationDefinitions) != 68 {
		t.Fatalf("configuration definition count = %d, want 68", len(configurationDefinitions))
	}
	if len(configurationPresentationByKey) != len(configurationDefinitions) {
		t.Fatalf("configuration presentation count = %d, want %d", len(configurationPresentationByKey), len(configurationDefinitions))
	}
	seen := make(map[string]struct{}, len(configurationDefinitions))
	defaults := make(map[string]any, len(configurationDefinitions))
	for _, definition := range configurationDefinitions {
		if _, duplicate := seen[definition.Key]; duplicate {
			t.Fatalf("duplicate configuration key %s", definition.Key)
		}
		seen[definition.Key] = struct{}{}
		presentation, found := configurationPresentationByKey[definition.Key]
		if !found || presentation.Group == "" || presentation.Description == "" || configurationGroupDescriptions[presentation.Group] == "" {
			t.Fatalf("missing presentation metadata for %s: %#v", definition.Key, presentation)
		}
		value, err := normalizeConfigurationValue(definition, definition.Default)
		if err != nil {
			t.Fatalf("normalize default %s: %v", definition.Key, err)
		}
		defaults[definition.Key] = value
	}
	mode := configurationDefinitionByKey["account_failover.mode"]
	if _, found := mode.Choices["observe"]; found {
		t.Fatal("retired observe mode remains selectable")
	}
	if _, found := mode.Choices["off"]; !found || len(mode.Choices) != 2 {
		t.Fatalf("failover choices = %#v", mode.Choices)
	}
	if !reflect.DeepEqual(mode.ChoiceOrder, []string{"off", "active"}) {
		t.Fatalf("failover choice order = %#v", mode.ChoiceOrder)
	}
	if err := validateConfiguration(defaults); err != nil {
		t.Fatalf("default configuration is invalid: %v", err)
	}
}

func TestConfigurationCatalogReturnsCompleteMetadataWithoutProxySecret(t *testing.T) {
	server, store := newTestAdmin(t)
	if err := store.WriteAccounts(context.Background(), []controlplane.Account{{
		ID: "alpha", Email: "alpha@example.com", Port: 18319, ProxyMode: "inherit", GroupEnabled: true,
	}}); err != nil {
		t.Fatalf("write compatible account fixture: %v", err)
	}
	const proxySecret = "socks5://catalog-user:catalog-secret@127.0.0.1:1080"
	if err := store.WriteSecret(context.Background(), defaultProxySecretName, proxySecret); err != nil {
		t.Fatalf("write configuration proxy: %v", err)
	}

	response := performAdminRequest(server, http.MethodGet, "/admin/api/settings/configuration", nil,
		map[string]string{"X-Management-Key": "test-management-key"}, nil)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "catalog-secret") {
		t.Fatalf("configuration catalog = %d %s", response.Code, response.Body.String())
	}
	var catalog configurationCatalogResponse
	decodeAdminResponse(t, response, &catalog)
	if catalog.Version != 1 || catalog.FieldCount != 68 || len(catalog.Groups) != 10 || catalog.GeneratedAt <= 0 {
		t.Fatalf("configuration catalog summary = %#v", catalog)
	}

	fields := make(map[string]configurationCatalogField, catalog.FieldCount)
	for _, group := range catalog.Groups {
		if group.Name == "" || group.Description == "" || len(group.Fields) == 0 {
			t.Fatalf("invalid configuration group = %#v", group)
		}
		for _, field := range group.Fields {
			fields[field.Key] = field
		}
	}
	if len(fields) != 68 {
		t.Fatalf("configuration catalog fields = %d", len(fields))
	}
	proxy := fields["cpa.proxy_url"]
	if proxy.Value != "" || proxy.Configured == nil || !*proxy.Configured || proxy.ValueType != "proxy_url_secret" {
		t.Fatalf("sanitized proxy field = %#v", proxy)
	}
	failover := fields["account_failover.mode"]
	if !reflect.DeepEqual(failover.Choices, []configurationCatalogChoice{
		{Value: "off", Label: "关闭"}, {Value: "active", Label: "自动执行"},
	}) {
		t.Fatalf("failover catalog choices = %#v", failover.Choices)
	}
	weekly := fields["user_quota.default_weekly_tokens"]
	if weekly.Minimum == nil || *weekly.Minimum != 1 || weekly.Maximum == nil || *weekly.Maximum != 1_000_000_000_000 || weekly.Unit != "Token" {
		t.Fatalf("weekly quota metadata = %#v", weekly)
	}
}

func TestConfigurationValueNormalizationCoversSupportedTypesAndBoundaries(t *testing.T) {
	tests := []struct {
		key     string
		raw     any
		want    any
		wantErr bool
	}{
		{key: "cpa.debug", raw: "yes", want: true},
		{key: "cpa.request_retry", raw: "10", want: int64(10)},
		{key: "cpa.request_retry", raw: 10.5, wantErr: true},
		{key: "user_quota.default_weekly_tokens", raw: "", want: nil},
		{key: "collector.interval_seconds", raw: "0.5", want: 0.5},
		{key: "identity.allowed_email_domains", raw: "Example.COM, api.example.com", want: []string{"example.com", "api.example.com"}},
		{key: "branding.public_base_url", raw: "https://cpa.example.com/", want: "https://cpa.example.com"},
		{key: "branding.public_base_url", raw: "https://user:secret@cpa.example.com", wantErr: true},
		{key: "cpa.proxy_url", raw: "socks5://user:secret@127.0.0.1:1080", want: "socks5://user:secret@127.0.0.1:1080"},
		{key: "cpa.session_affinity_ttl", raw: "30s", want: "30s"},
		{key: "cpa.session_affinity_ttl", raw: "29s", wantErr: true},
		{key: "notification.timezone", raw: "Asia/Shanghai", want: "Asia/Shanghai"},
		{key: "notification.daily_times", raw: "18:00,9:00,09:00", want: "09:00,18:00"},
		{key: "runtime.cliproxy_image", raw: "invalid image", wantErr: true},
		{key: "admin.account_usage.reasoning_effort_color.max", raw: "#B2731E", want: "#b2731e"},
	}
	for _, test := range tests {
		t.Run(test.key+"/"+valueString(test.raw), func(t *testing.T) {
			value, err := normalizeConfigurationValue(configurationDefinitionByKey[test.key], test.raw)
			if test.wantErr {
				if err == nil {
					t.Fatalf("normalization unexpectedly succeeded: %#v", value)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(value, test.want) {
				t.Fatalf("normalized = %#v, %v; want %#v", value, err, test.want)
			}
		})
	}
}

func TestConfigurationEndpointAppliesModesKeepsProxySecretOutOfSettingsAndRollsBack(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	ctx := context.Background()
	if err := store.WriteAccounts(ctx, []controlplane.Account{{
		ID: "alpha", Email: "alpha@example.com", Port: 18319, ProxyMode: "inherit", GroupEnabled: true,
	}}); err != nil {
		t.Fatalf("write compatible account fixture: %v", err)
	}
	applier := &recordingConfigurationApplier{}
	server, err := New(Config{Store: store, ConfigurationApplier: applier})
	if err != nil {
		t.Fatalf("New configuration Admin: %v", err)
	}
	t.Cleanup(server.Close)
	headers := map[string]string{"X-Management-Key": "test-management-key"}

	response := performAdminRequest(server, http.MethodPost, "/admin/api/settings/configuration", map[string]any{
		"confirm": "", "values": map[string]any{"cpa.debug": true},
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_request")
	response = performAdminRequest(server, http.MethodPost, "/admin/api/settings/configuration", map[string]any{
		"confirm": "save", "values": map[string]any{"account_failover.mode": "observe"},
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_request")
	response = performAdminRequest(server, http.MethodPost, "/admin/api/settings/configuration", map[string]any{
		"confirm": "save", "values": map[string]any{"notification.enabled": true},
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_request")

	const firstProxy = "socks5://user:first-secret@127.0.0.1:1080"
	response = performAdminRequest(server, http.MethodPost, "/admin/api/settings/configuration", map[string]any{
		"confirm": "save",
		"values": map[string]any{
			"cpa.debug": true, "collector.batch_size": 50, "accounts.listen_address": "127.0.0.2",
			"cpa.proxy_enabled": true, "cpa.proxy_url": firstProxy,
		},
	}, headers, nil)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "first-secret") {
		t.Fatalf("configuration update = %d %s", response.Code, response.Body.String())
	}
	var payload configurationUpdateResponse
	decodeAdminResponse(t, response, &payload)
	if !payload.PendingDeployment || !reflect.DeepEqual(payload.Applied, []string{"accounts", "collector", "live"}) {
		t.Fatalf("configuration response = %#v", payload)
	}
	if len(applier.calls) != 1 || !reflect.DeepEqual(applier.calls[0].Modes, []string{"accounts", "collector", "deployment", "live"}) {
		t.Fatalf("configuration apply calls = %#v", applier.calls)
	}
	settings, err := store.ReadSettings(ctx)
	if err != nil {
		t.Fatalf("read stored settings: %v", err)
	}
	if _, leaked := settings["cpa.proxy_url"]; leaked {
		t.Fatalf("proxy URL leaked into settings: %#v", settings)
	}
	storedProxy, found, err := store.ReadSecret(ctx, defaultProxySecretName)
	if err != nil || !found || storedProxy != firstProxy {
		t.Fatalf("stored proxy = (%q, %v, %v)", storedProxy, found, err)
	}

	const rejectedProxy = "https://user:rollback-secret@proxy.example.com:8443"
	applier.errors = []error{errors.New("simulated runtime apply failure"), nil}
	response = performAdminRequest(server, http.MethodPost, "/admin/api/settings/configuration", map[string]any{
		"confirm": "save", "values": map[string]any{"cpa.proxy_url": rejectedProxy, "cpa.debug": false},
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadGateway, "configuration_apply_failed")
	if strings.Contains(response.Body.String(), "rollback-secret") || len(applier.calls) != 3 || !applier.calls[2].Rollback {
		t.Fatalf("rollback response/calls = %s %#v", response.Body.String(), applier.calls)
	}
	storedProxy, found, err = store.ReadSecret(ctx, defaultProxySecretName)
	if err != nil || !found || storedProxy != firstProxy {
		t.Fatalf("rolled back proxy = (%q, %v, %v)", storedProxy, found, err)
	}
	settings, err = store.ReadSettings(ctx)
	if err != nil || settings["cpa.debug"] != true {
		t.Fatalf("rolled back settings = %#v, %v", settings, err)
	}
}

func TestConfigurationUpdateMigratesObserveAndLegacyProxyOutOfSettings(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	ctx := context.Background()
	if err := store.WriteAccounts(ctx, []controlplane.Account{{
		ID: "alpha", Email: "alpha@example.com", Port: 18319, ProxyMode: "inherit", GroupEnabled: true,
	}}); err != nil {
		t.Fatalf("write compatible account fixture: %v", err)
	}
	const legacyProxy = "http://legacy-user:legacy-secret@127.0.0.1:1080"
	if err := store.WriteSettings(ctx, map[string]any{
		"account_failover.mode": "observe", "cpa.proxy_url": legacyProxy, "gost.enabled": false,
		"gateway.port": int64(18317), "delivery.gateway_drain_timeout_seconds": int64(3600),
	}); err != nil {
		t.Fatalf("write legacy settings: %v", err)
	}
	server, err := New(Config{Store: store})
	if err != nil {
		t.Fatalf("New migration Admin: %v", err)
	}
	t.Cleanup(server.Close)
	response := performAdminRequest(server, http.MethodPost, "/admin/api/settings/configuration", map[string]any{
		"confirm": "save", "values": map[string]any{"cpa.debug": false},
	}, map[string]string{"X-Management-Key": "test-management-key"}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("configuration normalization = %d %s", response.Code, response.Body.String())
	}
	settings, err := store.ReadSettings(ctx)
	if err != nil || settings["account_failover.mode"] != "off" {
		t.Fatalf("normalized settings = %#v, %v", settings, err)
	}
	if _, found := settings["cpa.proxy_url"]; found {
		t.Fatalf("legacy proxy remains in settings: %#v", settings)
	}
	if _, found := settings["gost.enabled"]; found {
		t.Fatalf("retired setting remains: %#v", settings)
	}
	for _, key := range []string{"gateway.port", "delivery.gateway_drain_timeout_seconds"} {
		if _, found := settings[key]; found {
			t.Fatalf("retired deployment setting %s remains: %#v", key, settings)
		}
	}
	proxy, found, err := store.ReadSecret(ctx, defaultProxySecretName)
	if err != nil || !found || proxy != legacyProxy {
		t.Fatalf("migrated proxy = (%q, %v, %v)", proxy, found, err)
	}
}

func TestConfigurationRuntimeApplierUsesExactTargetsAndSingleCollectorRestart(t *testing.T) {
	projection := &recordingConfigurationProjection{}
	runtime := &recordingConfigurationRuntime{}
	deployment := &recordingDeploymentProjection{}
	applier := &ConfigurationRuntimeApplier{
		Accounts: configurationAccounts{accounts: []controlplane.Account{
			{ID: "beta", GroupEnabled: false}, {ID: "alpha", GroupEnabled: true},
		}},
		Projection:         projection,
		Runtime:            runtime,
		AccountEnvironment: deployment,
	}
	change := ConfigurationChange{
		After: map[string]any{"accounts.listen_address": "127.0.0.2"},
		Modes: []string{"quota", "accounts", "collector", "deployment", "live"},
	}
	if err := applier.ApplyConfiguration(context.Background(), change); err != nil {
		t.Fatalf("ApplyConfiguration: %v", err)
	}
	if projection.calls != 1 || deployment.calls != 1 ||
		!reflect.DeepEqual(runtime.targets, []string{"alpha", "usage-collector"}) {
		t.Fatalf("configuration side effects = projection %d, deployment %d, targets %#v",
			projection.calls, deployment.calls, runtime.targets)
	}

	missingRuntime := &ConfigurationRuntimeApplier{AccountEnvironment: deployment}
	if err := missingRuntime.ApplyConfiguration(context.Background(), ConfigurationChange{Modes: []string{"accounts"}}); err == nil {
		t.Fatal("account apply unexpectedly accepted missing runtime dependencies")
	}
}

func TestComposeEnvironmentProjectorWritesOnlyAccountRuntimeSettings(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatalf("create state: %v", err)
	}
	path := filepath.Join(state, "compose.env")
	if err := os.WriteFile(path, []byte(strings.Join([]string{
		"CLIPROXY_IMAGE=sha256:applied-cpa",
		"ADMIN_IMAGE=sha256:applied-admin",
		"WEB_RUNTIME_IMAGE=sha256:applied-web",
		"GATEWAY_RUNTIME_IMAGE=sha256:applied-gateway",
		"EDGE_RUNTIME_IMAGE=sha256:applied-edge",
		"GATEWAY_PORT=18317",
	}, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("seed Compose environment: %v", err)
	}
	values := make(map[string]any, len(configurationDefinitions))
	for _, definition := range configurationDefinitions {
		value, err := normalizeConfigurationValue(definition, definition.Default)
		if err != nil {
			t.Fatalf("normalize default %s: %v", definition.Key, err)
		}
		values[definition.Key] = value
	}
	projector := &AccountComposeEnvironmentProjector{Root: root}
	if err := projector.ProjectConfiguration(context.Background(), values); err != nil {
		t.Fatalf("ProjectConfiguration: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read projected environment: %v", err)
	}
	content := string(raw)
	want := "# Generated from state/control-plane.sqlite3; do not edit.\n" +
		"CLIPROXY_IMAGE=sha256:applied-cpa\n" +
		"BUSINESS_CPA_LISTEN_ADDRESS=127.0.0.1\n"
	if content != want {
		t.Fatalf("account Compose projection = %q, want %q", content, want)
	}
	information, err := os.Stat(path)
	if err != nil || information.Mode().Perm() != 0o600 {
		t.Fatalf("projected environment mode = %v, %v", information, err)
	}
}

func TestComposeEnvironmentProjectorUpdatesCPAImageAndDropsRetiredKeys(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatalf("create state: %v", err)
	}
	path := filepath.Join(state, "compose.env")
	before := strings.Join([]string{
		"# Generated from state/control-plane.sqlite3; do not edit.",
		"CLIPROXY_IMAGE=registry.example.test/cpa@sha256:old",
		"ADMIN_IMAGE=sha256:applied-admin",
		"WEB_RUNTIME_IMAGE=sha256:applied-web",
		"GATEWAY_PORT=18317",
		"BUSINESS_CPA_LISTEN_ADDRESS=127.0.0.1",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatalf("seed Compose environment: %v", err)
	}
	projector := &AccountComposeEnvironmentProjector{Root: root}
	if err := projector.ProjectCPAImage(context.Background(), "registry.example.test/cpa:v2@sha256:new"); err != nil {
		t.Fatalf("ProjectCPAImage: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read projected environment: %v", err)
	}
	want := "# Generated from state/control-plane.sqlite3; do not edit.\n" +
		"CLIPROXY_IMAGE=registry.example.test/cpa:v2@sha256:new\n" +
		"BUSINESS_CPA_LISTEN_ADDRESS=127.0.0.1\n"
	if string(raw) != want {
		t.Fatalf("CPA image projection did not canonicalize retired settings:\n--- got ---\n%s--- want ---\n%s", raw, want)
	}
	if err := projector.ProjectCPAImage(context.Background(), "mutable image:latest"); err == nil {
		t.Fatal("ProjectCPAImage accepted an invalid reference")
	}
}

type recordingConfigurationApplier struct {
	calls  []ConfigurationChange
	errors []error
}

func (applier *recordingConfigurationApplier) ApplyConfiguration(
	_ context.Context,
	change ConfigurationChange,
) error {
	applier.calls = append(applier.calls, change)
	if len(applier.errors) == 0 {
		return nil
	}
	err := applier.errors[0]
	applier.errors = applier.errors[1:]
	return err
}

type recordingConfigurationProjection struct{ calls int }

func (projection *recordingConfigurationProjection) RefreshAccounts(context.Context) error {
	projection.calls++
	return nil
}

type recordingConfigurationRuntime struct{ targets []string }

func (runtime *recordingConfigurationRuntime) RestartConfigurationTarget(_ context.Context, target string) error {
	runtime.targets = append(runtime.targets, target)
	return nil
}

type recordingDeploymentProjection struct{ calls int }

func (projection *recordingDeploymentProjection) ProjectConfiguration(context.Context, map[string]any) error {
	projection.calls++
	return nil
}

type configurationAccounts struct{ accounts []controlplane.Account }

func (accounts configurationAccounts) ReadAccounts(context.Context) ([]controlplane.Account, error) {
	return append([]controlplane.Account(nil), accounts.accounts...), nil
}
