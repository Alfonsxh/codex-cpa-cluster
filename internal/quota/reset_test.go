package quota

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
)

func TestResetterRevalidatesCreditAndWindowBeforeSingleConsume(t *testing.T) {
	store, root := seedResetStore(t)
	defer store.Close()
	var consumed int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/usage":
			_, _ = writer.Write([]byte(`{
			  "rate_limit":{"limit_reached":true,"primary_window":{"limit_window_seconds":604800,"used_percent":100,"reset_at":1784781238}},
			  "rate_limit_reached_type":{"details":"default"},
			  "rate_limit_reset_credits":{"available_count":2,"applicable_available_count":1}
			}`))
		case "/credits":
			_, _ = writer.Write([]byte(`{"available_count":2,"credits":[
			  {"id":"credit-selected","status":"available","reset_type":"codex_rate_limits","expires_at":"2026-08-13T00:00:00Z","title":"Full reset"},
			  {"id":"credit-other","status":"available","title":"Other reset"}
			]}`))
		case "/consume":
			consumed++
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["credit_id"] != "credit-selected" || strings.TrimSpace(stringValue(body["redeem_request_id"])) == "" {
				http.Error(writer, "bad payload", http.StatusBadRequest)
				return
			}
			_, _ = writer.Write([]byte(`{"code":"rate_limit_reset_credit_consumed","windows_reset":1,"credit":{"id":"must-not-leak","status":"redeemed","reset_type":"default","redeemed_at":"2026-07-17T08:00:00Z"}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	resetter, err := NewResetter(ResetterConfig{
		Root: root, Store: store, Fence: directResetFence{},
		Client: Client{
			Endpoint: server.URL + "/usage", ResetCreditsEndpoint: server.URL + "/credits",
			ResetEndpoint: server.URL + "/consume",
		},
	})
	if err != nil {
		t.Fatalf("NewResetter: %v", err)
	}
	result, err := resetter.Reset(context.Background(), "alpha", "credit-selected")
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if consumed != 1 || result.Account != "alpha" || result.WindowsReset != 1 || len(result.Windows) != 1 ||
		result.Windows[0].Label != "常规周限额" || result.Credit.Title != "Full reset" || result.Credit.Status != "redeemed" {
		t.Fatalf("reset result = %#v, consumed=%d", result, consumed)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "must-not-leak") || strings.Contains(string(raw), "credit-selected") {
		t.Fatalf("reset result leaked credit id: %s", raw)
	}
}

func TestResetterRejectsChangedCreditOrUnavailableWindowWithoutConsume(t *testing.T) {
	store, root := seedResetStore(t)
	defer store.Close()
	var consumed int
	applicable := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/usage":
			count := 0
			if applicable {
				count = 1
			}
			_, _ = writer.Write([]byte(`{"rate_limit":{"limit_reached":true,"primary_window":{"limit_window_seconds":604800,"used_percent":100}},"rate_limit_reached_type":{"details":"default"},"rate_limit_reset_credits":{"applicable_available_count":` + string(rune('0'+count)) + `}}`))
		case "/credits":
			if applicable {
				_, _ = writer.Write([]byte(`{"available_count":1,"credits":[]}`))
			} else {
				_, _ = writer.Write([]byte(`{"available_count":1,"credits":[{"id":"credit-selected","status":"available"}]}`))
			}
		case "/consume":
			consumed++
			_, _ = writer.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	resetter, err := NewResetter(ResetterConfig{
		Root: root, Store: store, Fence: directResetFence{},
		Client: Client{Endpoint: server.URL + "/usage", ResetCreditsEndpoint: server.URL + "/credits", ResetEndpoint: server.URL + "/consume"},
	})
	if err != nil {
		t.Fatalf("NewResetter: %v", err)
	}
	if _, err := resetter.Reset(context.Background(), "alpha", "credit-selected"); !errors.Is(err, ErrResetUnavailable) {
		t.Fatalf("unavailable window error = %v", err)
	}
	applicable = true
	if _, err := resetter.Reset(context.Background(), "alpha", "credit-selected"); !errors.Is(err, ErrResetCreditChanged) {
		t.Fatalf("changed credit error = %v", err)
	}
	if consumed != 0 {
		t.Fatalf("unsafe reset consumed %d credits", consumed)
	}
}

func seedResetStore(t *testing.T) (*controlplane.Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := controlplane.Open(context.Background(), root, controlplane.Options{})
	if err != nil {
		t.Fatalf("open control-plane store: %v", err)
	}
	if err := store.WriteAccounts(context.Background(), []controlplane.Account{{
		ID: "alpha", Email: "alpha@example.com", Port: 18318, ProxyMode: "direct", GroupEnabled: true,
	}}); err != nil {
		store.Close()
		t.Fatalf("write account: %v", err)
	}
	authDirectory := filepath.Join(root, "auth", "alpha")
	if err := os.MkdirAll(authDirectory, 0o700); err != nil {
		store.Close()
		t.Fatalf("create auth directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(authDirectory, "codex.json"), []byte(`{"type":"codex","access_token":"test-access-token","account_id":"account-123"}`), 0o600); err != nil {
		store.Close()
		t.Fatalf("write OAuth fixture: %v", err)
	}
	return store, root
}

type directResetFence struct{}

func (directResetFence) WithWriteFence(_ context.Context, operation func() error) error {
	return operation()
}
