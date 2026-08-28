package runtimeops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/accountprojection"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
)

func TestDiagnosticsHealthMatchesLegacyAccountSelectionWithoutLeakingKeys(t *testing.T) {
	root := t.TempDir()
	for account, files := range map[string][]string{
		"alpha": {"first.json", "second.JSON", "ignore.txt"},
		"beta":  {"only.json"},
	} {
		directory := filepath.Join(root, "auth", account)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatalf("create auth directory: %v", err)
		}
		for _, name := range files {
			if err := os.WriteFile(filepath.Join(directory, name), []byte("{}"), 0o600); err != nil {
				t.Fatalf("write auth fixture: %v", err)
			}
		}
	}

	var mu sync.Mutex
	seenAuthorization := make([]string, 0, 2)
	probe := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != diagnosticModelsPath {
			t.Fatalf("probe path = %q", request.URL.Path)
		}
		authorization := request.Header.Get("Authorization")
		mu.Lock()
		seenAuthorization = append(seenAuthorization, authorization)
		mu.Unlock()
		models := 1
		if authorization == "Bearer legacy-alpha-key" {
			models = 2
		}
		items := make([]string, models)
		for index := range items {
			items[index] = fmt.Sprintf(`{"id":"model-%d"}`, index+1)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"data":[%s]}`, strings.Join(items, ","))
	}))
	t.Cleanup(probe.Close)

	diagnostics := newTestDiagnostics(t, DiagnosticsConfig{
		Root: root,
		Store: &diagnosticStoreStub{
			accounts: []controlplane.Account{
				{ID: "alpha", Email: "alpha@example.com"},
				{ID: "beta", Email: "beta@example.com"},
				{ID: "gamma", Email: "gamma@example.com"},
			},
			routes: map[string]string{"shared@example.com": "beta"},
			records: []controlplane.KeyRecord{
				{Label: "shared@example.com:alpha", User: "shared@example.com", Account: "alpha", Key: "shared-route-key", Status: "active"},
				{Label: "shared@example.com:beta", User: "shared@example.com", Account: "beta", Key: "shared-route-key", Status: "active"},
				{Label: "legacy@example.com:alpha", User: "legacy@example.com", Account: "alpha", Key: "legacy-alpha-key", Status: "active"},
				{Label: "legacy@example.com:beta", User: "legacy@example.com", Account: "beta", Key: "legacy-beta-key", Status: "active"},
				{Label: "inactive@example.com:gamma", User: "inactive@example.com", Account: "gamma", Key: "inactive-key", Status: "rotated"},
			},
		},
		Projection:       &diagnosticProjectionStub{},
		GatewayProbeURLs: []string{probe.URL},
		RenderEnabled:    true,
	})
	var output strings.Builder
	result, err := diagnostics.Health(context.Background(), &output)
	if err != nil {
		t.Fatalf("health diagnostics: %v", err)
	}
	if result.Action != "health" || result.Target != "all" {
		t.Fatalf("health result = %#v", result)
	}
	want := "alpha alpha@example.com HTTP 200 AUTH_FILES 2 MODELS 2\n" +
		"beta beta@example.com HTTP 200 AUTH_FILES 1 MODELS 1\n" +
		"gamma gamma@example.com NO_KEY\n"
	if output.String() != want {
		t.Fatalf("health output = %q, want %q", output.String(), want)
	}
	for _, key := range []string{"shared-route-key", "legacy-alpha-key", "legacy-beta-key", "inactive-key"} {
		if strings.Contains(output.String(), key) {
			t.Fatalf("health output leaked %q", key)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(seenAuthorization, ",") != "Bearer legacy-alpha-key,Bearer shared-route-key" {
		t.Fatalf("health probe credentials = %#v", seenAuthorization)
	}
}

func TestDiagnosticsVerifyRoutingRetriesLegacyInternalAndPublicProbeOrder(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(first.Close)
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer route-secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(second.Close)
	diagnostics := newTestDiagnostics(t, DiagnosticsConfig{
		Root: t.TempDir(),
		Store: &diagnosticStoreStub{records: []controlplane.KeyRecord{
			{Label: "alice@example.com:alpha", Account: "alpha", Key: "route-secret", Status: "active"},
			{Label: "old@example.com:beta", Account: "beta", Key: "old-secret", Status: "revoked"},
		}},
		Projection:       &diagnosticProjectionStub{},
		GatewayProbeURLs: []string{first.URL, second.URL},
		RenderEnabled:    true,
	})
	var output strings.Builder
	result, err := diagnostics.VerifyRouting(context.Background(), &output)
	if err != nil {
		t.Fatalf("verify routing: %v", err)
	}
	if result.Action != "verify-routing" || result.Target != "all" {
		t.Fatalf("verify result = %#v", result)
	}
	if output.String() != "LABEL\tACCOUNT\tHTTP\nalice@example.com:alpha\talpha\t200\n" {
		t.Fatalf("routing output = %q", output.String())
	}
	if strings.Contains(output.String(), "route-secret") || strings.Contains(output.String(), "old-secret") {
		t.Fatal("routing output leaked a Key")
	}
}

func TestDiagnosticsRenderHonorsReadOnlyAndPropagatesProjectionFailure(t *testing.T) {
	projection := &diagnosticProjectionStub{}
	base := DiagnosticsConfig{
		Root: t.TempDir(), Store: &diagnosticStoreStub{}, Projection: projection,
		GatewayProbeURLs: []string{"http://127.0.0.1:8319"}, RenderEnabled: false,
	}
	readOnly := newTestDiagnostics(t, base)
	if _, err := readOnly.Render(context.Background(), io.Discard); !errors.Is(err, ErrRuntimeReadOnly) {
		t.Fatalf("read-only render error = %v", err)
	}
	if projection.calls != 0 {
		t.Fatalf("read-only projection calls = %d", projection.calls)
	}

	projection.err = errors.New("invalid compose projection")
	base.RenderEnabled = true
	writable := newTestDiagnostics(t, base)
	if _, err := writable.Render(context.Background(), io.Discard); err == nil || !strings.Contains(err.Error(), "invalid compose projection") {
		t.Fatalf("projection failure = %v", err)
	}
	projection.err = nil
	result, err := writable.Render(context.Background(), io.Discard)
	if err != nil || result.Action != "render" || result.Target != "all" {
		t.Fatalf("render result = (%#v, %v)", result, err)
	}
}

func newTestDiagnostics(t *testing.T, config DiagnosticsConfig) *Diagnostics {
	t.Helper()
	diagnostics, err := NewDiagnostics(config)
	if err != nil {
		t.Fatalf("new diagnostics: %v", err)
	}
	return diagnostics
}

type diagnosticStoreStub struct {
	accounts []controlplane.Account
	routes   map[string]string
	records  []controlplane.KeyRecord
}

func (store *diagnosticStoreStub) ReadAccounts(context.Context) ([]controlplane.Account, error) {
	return append([]controlplane.Account(nil), store.accounts...), nil
}

func (store *diagnosticStoreStub) ReadRoutes(context.Context) (map[string]string, error) {
	result := make(map[string]string, len(store.routes))
	for user, account := range store.routes {
		result[user] = account
	}
	return result, nil
}

func (store *diagnosticStoreStub) ReadKeyRecords(context.Context) ([]controlplane.KeyRecord, error) {
	return append([]controlplane.KeyRecord(nil), store.records...), nil
}

type diagnosticProjectionStub struct {
	calls int
	err   error
}

func (projection *diagnosticProjectionStub) Render(context.Context) (accountprojection.Result, error) {
	projection.calls++
	return accountprojection.Result{Accounts: 2, Users: 3}, projection.err
}
